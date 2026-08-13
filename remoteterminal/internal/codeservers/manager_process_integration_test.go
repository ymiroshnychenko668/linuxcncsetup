//go:build linux
// +build linux

package codeservers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	codeServerProcessIntegrationFlag = "REMOTETERMINAL_RUN_PROCESS_INTEGRATION"
	codeServerTmuxIntegrationPath    = "REMOTETERMINAL_TMUX_BINARY"
	codeServerIntegrationPath        = "REMOTETERMINAL_CODE_SERVER_BINARY"
)

// TestCodeServerManagerRealProcessLifecycle exercises the complete Manager ->
// tmux -> code-server -> Unix-socket proxy path. It is deliberately opt-in:
// all three environment variables below must be explicitly set.
//
//	REMOTETERMINAL_RUN_PROCESS_INTEGRATION=1
//	REMOTETERMINAL_TMUX_BINARY=/absolute/path/to/tmux
//	REMOTETERMINAL_CODE_SERVER_BINARY=/absolute/path/to/code-server
func TestCodeServerManagerRealProcessLifecycle(t *testing.T) {
	if os.Getenv(codeServerProcessIntegrationFlag) != "1" {
		t.Skipf("set %s=1 and explicit %s/%s paths to run the real-process integration test",
			codeServerProcessIntegrationFlag, codeServerTmuxIntegrationPath, codeServerIntegrationPath)
	}
	tmuxBinary := os.Getenv(codeServerTmuxIntegrationPath)
	codeServerBinary := os.Getenv(codeServerIntegrationPath)
	if tmuxBinary == "" || codeServerBinary == "" {
		t.Skipf("real-process integration test requires both %s and %s",
			codeServerTmuxIntegrationPath, codeServerIntegrationPath)
	}
	requireCodeServerIntegrationExecutable(t, codeServerTmuxIntegrationPath, tmuxBinary)
	requireCodeServerIntegrationExecutable(t, codeServerIntegrationPath, codeServerBinary)

	root, err := os.MkdirTemp("/tmp", "rtcs-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalWorkingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()

	// A hostile ordinary code-server config must never affect a managed
	// instance because both the dependency probe and launch use an explicit
	// application-owned config file.
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config", "code-server"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "code-server", "config.yaml"),
		[]byte("auth: password\ncert: true\nbind-addr: 0.0.0.0:1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	// If tmux ever passed this through an unquoted shell, the command
	// substitution would create this marker and the selected path would change.
	marker := filepath.Join(root, "SHOULD_NOT_EXIST")
	workspace := filepath.Join(root, "workspace $(touch SHOULD_NOT_EXIST)")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	otherWorkspace := filepath.Join(root, "other")
	if err := os.Mkdir(otherWorkspace, 0700); err != nil {
		t.Fatal(err)
	}

	// A second private tmux server is a canary for exact-socket cleanup.
	unrelatedSocket := filepath.Join(root, "unrelated.sock")
	unrelatedName := "unrelated_canary"
	if output, err := exec.Command(tmuxBinary, "-f", "/dev/null", "-S", unrelatedSocket,
		"new-session", "-d", "-s", unrelatedName).CombinedOutput(); err != nil {
		t.Fatalf("start unrelated tmux canary: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, tmuxBinary, "-S", unrelatedSocket, "kill-server").Run()
	})
	requireCodeServerTmuxSession(t, tmuxBinary, unrelatedSocket, unrelatedName)

	manager := NewManager(Config{
		TmuxBinary:       tmuxBinary,
		CodeServerBinary: codeServerBinary,
		RuntimeDir:       filepath.Join(root, "run"),
		StateDir:         filepath.Join(root, "state"),
		HomeDir:          home,
		MaxServers:       1,
		StartTimeout:     15 * time.Second,
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = manager.Shutdown(cleanupCtx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := manager.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	created, reused, err := manager.Create(ctx, workspace)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if reused || !ValidID(created.ID) || created.FolderPath != workspace ||
		created.URL != instanceBasePath(created.ID)+"/" {
		t.Fatalf("Create returned an invalid instance: reused=%t instance=%+v", reused, created)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace metacharacters were interpreted by a shell: marker error=%v", err)
	}
	requireCodeServerTmuxSession(t, tmuxBinary, manager.tmuxSocket(), tmuxTarget(created.ID))
	for _, path := range []string{manager.configPath(created.ID), manager.httpSocket(created.ID)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("managed path %s: %v", path, err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("managed path %s mode = %o, want 600", path, info.Mode().Perm())
		}
	}

	active, err := manager.List(ctx)
	if err != nil || len(active) != 1 || active[0].ID != created.ID {
		t.Fatalf("List after Create = %+v, err=%v", active, err)
	}
	reusedInstance, reused, err := manager.Create(ctx, alias)
	if err != nil || !reused || reusedInstance.ID != created.ID {
		t.Fatalf("Create canonical alias = %+v reused=%t err=%v", reusedInstance, reused, err)
	}
	if _, _, err := manager.Create(ctx, otherWorkspace); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("Create over limit error = %v, want %v", err, ErrLimitReached)
	}

	handler, err := manager.Proxy(ctx, created.ID)
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()
	redirectClient := proxyServer.Client()
	redirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		proxyServer.URL+created.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := redirectClient.Do(request)
	if err != nil {
		t.Fatalf("HTTP through code-server Unix proxy: %v", err)
	}
	if response.StatusCode < http.StatusMultipleChoices || response.StatusCode >= http.StatusBadRequest {
		_ = response.Body.Close()
		t.Fatalf("proxied code-server root status = %d, want redirect", response.StatusCode)
	}
	location, locationErr := response.Location()
	closeRedirectErr := response.Body.Close()
	if locationErr != nil || closeRedirectErr != nil {
		t.Fatalf("resolve proxied code-server redirect: location=%v close=%v", locationErr, closeRedirectErr)
	}
	if location.Scheme != request.URL.Scheme || location.Host != request.URL.Host ||
		!strings.HasPrefix(location.Path, instanceBasePath(created.ID)+"/") {
		t.Fatalf("code-server redirect escaped instance prefix: %s", location)
	}
	followRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = proxyServer.Client().Do(followRequest)
	if err != nil {
		t.Fatalf("follow proxied code-server redirect: %v", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read proxied code-server page: read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || len(body) == 0 ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") ||
		!strings.HasPrefix(response.Request.URL.Path, instanceBasePath(created.ID)+"/") {
		t.Fatalf("proxied code-server page: status=%d type=%q final_url=%s body=%q",
			response.StatusCode, response.Header.Get("Content-Type"), response.Request.URL,
			bytes.TrimSpace(body))
	}

	profile := manager.profileDir(workspace)
	if err := manager.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if active, err = manager.List(ctx); err != nil || len(active) != 0 {
		t.Fatalf("List after Delete = %+v, err=%v", active, err)
	}
	if _, err := os.Stat(manager.instanceDir(created.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral instance directory survived Delete: %v", err)
	}
	if info, err := os.Stat(profile); err != nil || !info.IsDir() {
		t.Fatalf("durable per-folder profile did not survive Delete: mode=%v err=%v", modeOf(info), err)
	}
	requireCodeServerTmuxSession(t, tmuxBinary, unrelatedSocket, unrelatedName)
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	requireCodeServerTmuxSession(t, tmuxBinary, unrelatedSocket, unrelatedName)
}

func requireCodeServerIntegrationExecutable(t *testing.T, variable, path string) {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must be an absolute path, got %q", variable, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s=%q: %v", variable, path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("%s=%q is not an executable regular file", variable, path)
	}
}

func requireCodeServerTmuxSession(t *testing.T, tmuxBinary, socket, target string) {
	t.Helper()
	if output, err := exec.Command(tmuxBinary, "-S", socket,
		"has-session", "-t", target).CombinedOutput(); err != nil {
		t.Fatalf("tmux session %q is unavailable: %v (%s)", target, err,
			strings.TrimSpace(string(output)))
	}
}
