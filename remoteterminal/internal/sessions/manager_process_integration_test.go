//go:build linux
// +build linux

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	processIntegrationFlag = "REMOTETERMINAL_RUN_PROCESS_INTEGRATION"
	tmuxIntegrationPath    = "REMOTETERMINAL_TMUX_BINARY"
	ttydIntegrationPath    = "REMOTETERMINAL_TTYD_BINARY"
)

// TestManagerRealProcessLifecycle exercises the complete Manager -> ttyd ->
// tmux path. It is deliberately opt-in: all three environment variables below
// must be explicitly set, and dynamically linked ttyd builds may additionally
// require LD_LIBRARY_PATH in the go test environment.
//
//	REMOTETERMINAL_RUN_PROCESS_INTEGRATION=1
//	REMOTETERMINAL_TMUX_BINARY=/absolute/path/to/tmux
//	REMOTETERMINAL_TTYD_BINARY=/absolute/path/to/ttyd
func TestManagerRealProcessLifecycle(t *testing.T) {
	if os.Getenv(processIntegrationFlag) != "1" {
		t.Skipf("set %s=1 and explicit %s/%s paths to run the real-process integration test",
			processIntegrationFlag, tmuxIntegrationPath, ttydIntegrationPath)
	}
	tmuxBinary := os.Getenv(tmuxIntegrationPath)
	ttydBinary := os.Getenv(ttydIntegrationPath)
	if tmuxBinary == "" || ttydBinary == "" {
		t.Skipf("real-process integration test requires both %s and %s", tmuxIntegrationPath, ttydIntegrationPath)
	}
	requireIntegrationExecutable(t, tmuxIntegrationPath, tmuxBinary)
	requireIntegrationExecutable(t, ttydIntegrationPath, ttydBinary)

	tmuxVersion := integrationCommand(t, tmuxBinary, "-V")
	ttydVersion := integrationCommand(t, ttydBinary, "--version")
	t.Logf("real dependencies: %s; %s", strings.TrimSpace(tmuxVersion), strings.TrimSpace(ttydVersion))

	root, err := os.MkdirTemp("/tmp", "rt-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// The managed server must ignore the selected account's ordinary tmux
	// configuration. This setting would destroy every detached session if the
	// Manager accidentally started its private server with the default config.
	hostileHome := filepath.Join(root, "hostile-home")
	if err := os.Mkdir(hostileHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostileHome, ".tmux.conf"),
		[]byte("set-option -g destroy-unattached on\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", hostileHome)
	// Deliberately poison the manager's inherited colour environment. Create
	// must sanitize the private tmux server before its first pane starts.
	t.Setenv(tmuxColorEnvironment, "not-truecolor")
	t.Setenv(tmuxNoColorEnvironment, "1")
	// Debian's plugin-enabled libwebsockets searches a compiled absolute
	// install directory, not LD_LIBRARY_PATH, for its libuv event-loop plugin.
	// For an extracted-package test build, stage a private copy whose embedded
	// plugin directory points at this short temporary root. Installed or
	// statically linked ttyd builds simply take the empty branch.
	if plugin, libraryDirectory, err := stageIntegrationTtydLibraries(root); err != nil {
		t.Fatalf("stage ttyd event-loop libraries: %v", err)
	} else if plugin != "" {
		if current := os.Getenv("LD_LIBRARY_PATH"); current != "" {
			t.Setenv("LD_LIBRARY_PATH", libraryDirectory+string(os.PathListSeparator)+current)
		} else {
			t.Setenv("LD_LIBRARY_PATH", libraryDirectory)
		}
		t.Logf("staged extracted libwebsockets event-loop plugin from %s", plugin)
	}
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
	managedRuntime := filepath.Join(root, "managed")
	if err := os.Mkdir(managedRuntime, 0700); err != nil {
		t.Fatal(err)
	}

	// A second tmux server is a canary for the Manager's isolation contract.
	// Cleanup always addresses its exact private socket, never the default tmux
	// server or any session outside this temporary directory.
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
	requireTmuxSession(t, tmuxBinary, unrelatedSocket, unrelatedName)

	manager := NewManager(Config{
		TmuxBinary:   tmuxBinary,
		TtydBinary:   ttydBinary,
		RuntimeDir:   managedRuntime,
		MaxSessions:  2,
		StartTimeout: 5 * time.Second,
	})
	createdID := ""
	otherID := ""
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if ValidID(otherID) {
			_ = manager.Delete(cleanupCtx, otherID)
		}
		if ValidID(createdID) {
			_ = manager.Delete(cleanupCtx, createdID)
		}
		_ = manager.Shutdown(cleanupCtx)
		if ValidID(createdID) {
			_ = exec.CommandContext(cleanupCtx, tmuxBinary, "-S", manager.tmuxSocket(),
				"kill-session", "-t", tmuxTarget(createdID)).Run()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := manager.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	created, err := manager.Create(ctx, "Real process integration")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	createdID = created.ID
	if !ValidID(created.ID) || created.Name != "Real process integration" {
		t.Fatalf("Create returned an invalid session: %+v", created)
	}
	requireTmuxSession(t, tmuxBinary, manager.tmuxSocket(), tmuxTarget(created.ID))
	requireTmuxColorEnvironment(t, tmuxBinary, manager.tmuxSocket(), "-g")
	requireInitialPaneColorEnvironment(t, tmuxBinary, manager.tmuxSocket(), tmuxTarget(created.ID))
	initialCapture := integrationCommand(t, tmuxBinary, "-S", manager.tmuxSocket(),
		"capture-pane", "-p", "-t", tmuxTarget(created.ID))
	promptMarker := lastNonEmptyLine(initialCapture)
	if promptMarker == "" {
		t.Fatal("initial tmux shell did not render a prompt")
	}
	if output := integrationCommand(t, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-gv", tmuxStatusOption); strings.TrimSpace(output) != tmuxStatusMode {
		t.Fatalf("Create global status = %q, want %q", strings.TrimSpace(output), tmuxStatusMode)
	}
	if output := integrationCommand(t, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-gv", tmuxHistoryLimitOption); strings.TrimSpace(output) != tmuxHistoryLimit {
		t.Fatalf("Create global history-limit = %q, want %q", strings.TrimSpace(output), tmuxHistoryLimit)
	}
	for _, option := range []string{tmuxWindowStyleOption, tmuxWindowActiveStyleOption} {
		if output := integrationCommand(t, tmuxBinary, "-S", manager.tmuxSocket(),
			"show-options", "-gv", option); strings.TrimSpace(output) != tmuxWindowStyle {
			t.Fatalf("Create global %s = %q, want %q", option, strings.TrimSpace(output), tmuxWindowStyle)
		}
	}
	requireTmuxDefaultColorResponses(t, ctx, tmuxBinary, manager.tmuxSocket(), tmuxTarget(created.ID), root)
	requireTmuxBackgroundRendering(t, ctx, tmuxBinary, manager.tmuxSocket(), tmuxTarget(created.ID))
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"new-window", "-d", "-t", tmuxTarget(created.ID)+":", "-n", "recovered").CombinedOutput(); err != nil {
		t.Fatalf("create recovered-window fixture: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	windowTargets := integrationTmuxWindowTargets(t, ctx, tmuxBinary, manager.tmuxSocket(), tmuxTarget(created.ID))
	if len(windowTargets) != 2 {
		t.Fatalf("recovered-window fixture has %d windows, want 2", len(windowTargets))
	}
	for _, windowTarget := range windowTargets {
		for option, value := range map[string]string{
			tmuxWindowStyleOption:       "fg=red,bg=blue",
			tmuxWindowActiveStyleOption: "fg=green,bg=yellow",
		} {
			if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
				"set-window-option", "-t", windowTarget, option, value).CombinedOutput(); err != nil {
				t.Fatalf("poison recovered %s on %s: %v (%s)", option, windowTarget, err, strings.TrimSpace(string(output)))
			}
		}
	}
	initialTerminalOverrides := integrationCommand(t, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-s", "terminal-overrides")
	t.Logf("Initialize/Create: runtime=%s id=%s target=%s", managedRuntime, created.ID, tmuxTarget(created.ID))

	connected, basePath, err := manager.Connect(ctx, created.ID)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !connected.TerminalConnected || basePath != terminalBasePath(created.ID)+"/" {
		t.Fatalf("Connect returned session=%+v basePath=%q", connected, basePath)
	}
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-v", "-t", tmuxTarget(created.ID), "mouse").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "on" {
		t.Fatalf("Connect did not enable tmux mouse scrolling: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-v", "-t", tmuxTarget(created.ID), tmuxStatusOption).CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != tmuxStatusMode {
		t.Fatalf("Connect did not disable the tmux status row: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-v", "-t", tmuxTarget(created.ID), tmuxHistoryLimitOption).CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != tmuxHistoryLimit {
		t.Fatalf("Connect did not configure tmux scrollback: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	for _, option := range []string{tmuxWindowStyleOption, tmuxWindowActiveStyleOption} {
		if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
			"show-options", "-gv", option).CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != tmuxWindowStyle {
			t.Fatalf("Connect did not restore global tmux %s: %v (%s)", option, err, strings.TrimSpace(string(output)))
		}
		for _, windowTarget := range windowTargets {
			if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
				"show-options", "-wv", "-t", windowTarget, option).CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != tmuxWindowStyle {
				t.Fatalf("Connect did not configure tmux %s on %s: %v (%s)",
					option, windowTarget, err, strings.TrimSpace(string(output)))
			}
		}
	}
	requireTmuxColorEnvironment(t, tmuxBinary, manager.tmuxSocket(), "-g")
	requireTmuxColorEnvironment(t, tmuxBinary, manager.tmuxSocket(), "-t", tmuxTarget(created.ID))
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-sv", tmuxClipboardOption).CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != tmuxClipboardTerminalOverride {
		t.Fatalf("Connect did not configure browser clipboard integration: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	configuredTerminalOverrides := integrationCommand(t, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-s", "terminal-overrides")
	for _, original := range strings.Split(strings.TrimSpace(initialTerminalOverrides), "\n") {
		if original != "" && !strings.Contains(configuredTerminalOverrides, original) {
			t.Fatalf("Connect replaced built-in tmux terminal override %q; configured=%q",
				original, strings.TrimSpace(configuredTerminalOverrides))
		}
	}
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"show-options", "-sv", tmuxClipboardModeOption).CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != tmuxClipboardMode {
		t.Fatalf("Connect did not restrict browser clipboard integration: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
			"list-keys", "-T", table).CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("MouseDragEnd1Pane")) ||
			!bytes.Contains(output, []byte("copy-selection-and-cancel")) ||
			!bytes.Contains(output, []byte(tmuxSelectionBindingPrefix)) {
			t.Fatalf("Connect did not scope %s drag selection buffers: %v (%s)", table, err, strings.TrimSpace(string(output)))
		}
	}
	firstManaged, ttydPID := integrationManagedProcess(t, manager, created.ID)
	manager.mu.Lock()
	ttydCommand := append([]string(nil), firstManaged.process.(*execProcess).cmd.Args...)
	manager.mu.Unlock()
	wantTtydCommand := append([]string{ttydBinary}, TtydArguments(
		tmuxBinary, manager.tmuxSocket(), manager.ttydSocket(created.ID), created.ID)...)
	if strings.Join(ttydCommand, "\x00") != strings.Join(wantTtydCommand, "\x00") {
		t.Fatalf("managed ttyd command\n got: %#v\nwant: %#v", ttydCommand, wantTtydCommand)
	}
	ttydSocket := manager.ttydSocket(created.ID)
	if info, err := os.Stat(ttydSocket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("Connect did not create ttyd Unix socket %s: mode=%v err=%v", ttydSocket, modeOf(info), err)
	}

	proxy, err := manager.Proxy(ctx, created.ID)
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyServer.URL+basePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := proxyServer.Client().Do(request)
	if err != nil {
		t.Fatalf("HTTP through Manager Unix proxy: %v", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read proxied ttyd index: read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || len(body) == 0 || !bytes.Contains(bytes.ToLower(body), []byte("ttyd")) {
		t.Fatalf("proxied ttyd index: status=%d bytes=%d contains-ttyd=%t",
			response.StatusCode, len(body), bytes.Contains(bytes.ToLower(body), []byte("ttyd")))
	}
	if !bytes.Contains(body, []byte("navigator.clipboard.writeText")) {
		t.Fatal("proxied ttyd frontend does not contain the OSC 52 browser clipboard provider")
	}
	t.Logf("Connect/Proxy: ttyd_pid=%d socket=%s HTTP=%d body_bytes=%d",
		ttydPID, ttydSocket, response.StatusCode, len(body))

	value := "v" + strconv.FormatInt(time.Now().UnixNano(), 10)
	executedMarker := []byte("__REMOTE_TERMINAL_EXEC_" + value + "__")
	firstCommand := "export REMOTETERMINAL_IT_VALUE='" + value +
		"'; printf '\\n%s%s%s%s\\n' '__REMOTE_' 'TERMINAL_EXEC_' \"$REMOTETERMINAL_IT_VALUE\" '__'\r"

	firstWS, err := dialIntegrationWebSocket(proxyServer.URL, terminalBasePath(created.ID)+"/ws", 5*time.Second)
	if err != nil {
		t.Fatalf("first ttyd WebSocket handshake: %v", err)
	}
	firstWSOpen := true
	defer func() {
		if firstWSOpen {
			_ = firstWS.Close()
		}
	}()
	if firstWS.subprotocol != "tty" {
		t.Fatalf("ttyd negotiated WebSocket subprotocol %q, want %q", firstWS.subprotocol, "tty")
	}
	if err := firstWS.initializeTTyd(120, 40); err != nil {
		t.Fatalf("initialize first ttyd protocol connection: %v", err)
	}
	preferences, err := firstWS.readTTydPreferences(5 * time.Second)
	if err != nil {
		t.Fatalf("read ttyd renderer preferences: %v", err)
	}
	if renderer, _ := preferences["rendererType"].(string); renderer != "canvas" {
		t.Fatalf("ttyd rendererType = %#v, want canvas", preferences["rendererType"])
	}
	theme, ok := preferences["theme"].(map[string]interface{})
	if !ok || theme["foreground"] != terminalForegroundColor || theme["background"] != terminalBackgroundColor {
		t.Fatalf("ttyd theme = %#v, want foreground=%s background=%s",
			preferences["theme"], terminalForegroundColor, terminalBackgroundColor)
	}
	if output, err := firstWS.readTTydOutputUntil([]byte(promptMarker), 5*time.Second); err != nil {
		t.Fatalf("initial tmux attach did not render the existing shell prompt: %v; output=%q", err, output)
	}
	if err := firstWS.writeTTydInput(firstCommand); err != nil {
		t.Fatalf("send distinctive command through ttyd protocol: %v", err)
	}
	firstOutput, err := firstWS.readTTydOutputUntil(executedMarker, 8*time.Second)
	if err != nil {
		t.Fatalf("distinctive command output: %v; output=%q", err, firstOutput)
	}
	copyTmuxLineWithMouseBinding(t, ctx, tmuxBinary, manager.tmuxSocket(), tmuxTarget(created.ID), string(executedMarker))
	clipboardText, err := manager.LatestSelection(ctx, created.ID)
	if err != nil {
		t.Fatalf("read session-scoped mouse selection: %v", err)
	}
	if clipboardText != string(executedMarker) {
		t.Fatalf("session-scoped mouse selection = %q, want %q", clipboardText, executedMarker)
	}
	clipboardSequence := []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(clipboardText)) + "\a")
	clipboardOutput, err := firstWS.readTTydOutputUntil(clipboardSequence, 8*time.Second)
	if err != nil {
		t.Fatalf("OSC 52 browser clipboard output: %v; output=%q", err, clipboardOutput)
	}

	other, err := manager.Create(ctx, "Other clipboard integration")
	if err != nil {
		t.Fatalf("create second session for clipboard isolation: %v", err)
	}
	otherID = other.ID
	otherMarker := "__REMOTE_TERMINAL_OTHER_CLIPBOARD_" + value + "__"
	otherTarget := tmuxTarget(other.ID)
	otherCommand := "printf '\\n%s\\n' '" + otherMarker + "'"
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"send-keys", "-t", otherTarget, otherCommand, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("write second session marker: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if err := integrationEventually(3*time.Second, func() error {
		output, captureErr := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
			"capture-pane", "-p", "-t", otherTarget, "-S", "-20").CombinedOutput()
		if captureErr != nil {
			return captureErr
		}
		if !bytes.Contains(output, []byte(otherMarker)) {
			return errors.New("second session marker has not appeared")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	copyTmuxLineWithMouseBinding(t, ctx, tmuxBinary, manager.tmuxSocket(), otherTarget, otherMarker)
	otherClipboardText, err := manager.LatestSelection(ctx, other.ID)
	if err != nil || otherClipboardText != otherMarker {
		t.Fatalf("second session selection = %q, %v; want %q", otherClipboardText, err, otherMarker)
	}
	firstClipboardText, err := manager.LatestSelection(ctx, created.ID)
	if err != nil || firstClipboardText != clipboardText {
		t.Fatalf("newer selection from another session replaced first selection: got %q, %v; want %q", firstClipboardText, err, clipboardText)
	}
	t.Logf("WebSocket: HTTP=101 subprotocol=%s command_marker=%s", firstWS.subprotocol, executedMarker)
	if err := firstWS.Close(); err != nil {
		t.Fatalf("disconnect first ttyd WebSocket: %v", err)
	}
	firstWSOpen = false

	if err := integrationEventually(6*time.Second, func() error {
		sessions, listErr := manager.List(ctx)
		if listErr != nil {
			return listErr
		}
		for _, session := range sessions {
			if session.ID == created.ID {
				if session.Attached {
					return errors.New("tmux still reports the disconnected client as attached")
				}
				return nil
			}
		}
		return errors.New("managed tmux session disappeared after WebSocket disconnect")
	}); err != nil {
		t.Fatal(err)
	}

	reconnected, reconnectPath, err := manager.Connect(ctx, created.ID)
	if err != nil {
		t.Fatalf("Connect after WebSocket disconnect: %v", err)
	}
	if !reconnected.TerminalConnected || reconnectPath != basePath {
		t.Fatalf("reconnect returned session=%+v basePath=%q, want %q", reconnected, reconnectPath, basePath)
	}
	secondManaged, secondPID := integrationManagedProcess(t, manager, created.ID)
	if secondManaged != firstManaged || secondPID != ttydPID {
		t.Fatalf("reconnect replaced ttyd: first process=%p pid=%d, second process=%p pid=%d",
			firstManaged, ttydPID, secondManaged, secondPID)
	}

	secondWS, err := dialIntegrationWebSocket(proxyServer.URL, terminalBasePath(created.ID)+"/ws", 5*time.Second)
	if err != nil {
		t.Fatalf("second ttyd WebSocket handshake: %v", err)
	}
	secondWSOpen := true
	defer func() {
		if secondWSOpen {
			_ = secondWS.Close()
		}
	}()
	if secondWS.subprotocol != "tty" {
		t.Fatalf("reconnected ttyd negotiated WebSocket subprotocol %q", secondWS.subprotocol)
	}
	if err := secondWS.initializeTTyd(120, 40); err != nil {
		t.Fatalf("initialize second ttyd protocol connection: %v", err)
	}
	replayedOutput, err := secondWS.readTTydOutputUntil(executedMarker, 8*time.Second)
	if err != nil {
		t.Fatalf("reconnected tmux pane did not replay prior output: %v; output=%q", err, replayedOutput)
	}

	persistedMarker := []byte("__REMOTE_TERMINAL_STATE_" + value + "__")
	persistenceCommand := "printf '\\n%s%s%s\\n' '__REMOTE_TERMINAL_STATE_' \"$REMOTETERMINAL_IT_VALUE\" '__'\r"
	if err := secondWS.writeTTydInput(persistenceCommand); err != nil {
		t.Fatalf("send persistence command through reconnected ttyd: %v", err)
	}
	persistedOutput, err := secondWS.readTTydOutputUntil(persistedMarker, 8*time.Second)
	if err != nil {
		t.Fatalf("shell state did not persist across ttyd WebSocket clients: %v; output=%q", err, persistedOutput)
	}
	t.Logf("Disconnect/reconnect: same_ttyd_pid=%d replayed_marker=%s persisted_marker=%s",
		secondPID, executedMarker, persistedMarker)
	if err := secondWS.Close(); err != nil {
		t.Fatalf("disconnect second ttyd WebSocket: %v", err)
	}
	secondWSOpen = false

	if err := manager.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := integrationEventually(4*time.Second, func() error {
		if !processFinished(firstManaged) {
			return fmt.Errorf("ttyd pid %d has not been reaped", ttydPID)
		}
		if err := syscall.Kill(ttydPID, 0); !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("ttyd pid %d still exists (signal 0: %v)", ttydPID, err)
		}
		if _, err := os.Stat(ttydSocket); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ttyd socket still exists: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveTerminals() != 0 {
		t.Fatalf("Delete left %d active ttyd processes", manager.ActiveTerminals())
	}
	if output, err := exec.CommandContext(ctx, tmuxBinary, "-S", manager.tmuxSocket(),
		"has-session", "-t", tmuxTarget(created.ID)).CombinedOutput(); err == nil {
		t.Fatalf("Delete left managed tmux target %s (%s)", tmuxTarget(created.ID), strings.TrimSpace(string(output)))
	}
	if err := manager.Delete(ctx, other.ID); err != nil {
		t.Fatalf("delete second clipboard-isolation session: %v", err)
	}
	otherID = ""
	listed, err := manager.List(ctx)
	if err != nil || len(listed) != 0 {
		t.Fatalf("List after Delete = %+v, %v", listed, err)
	}
	requireTmuxSession(t, tmuxBinary, unrelatedSocket, unrelatedName)
	t.Logf("Delete: ttyd_pid=%d reaped=true ttyd_socket_removed=true managed_tmux_removed=true unrelated_tmux_present=true", ttydPID)
}

func copyTmuxLineWithMouseBinding(t *testing.T, ctx context.Context, binary, socket, target, marker string) {
	t.Helper()
	commands := [][]string{
		{"copy-mode", "-t", target},
		{"send-keys", "-t", target, "-X", "search-backward", marker},
		{"send-keys", "-t", target, "-X", "start-of-line"},
		{"send-keys", "-t", target, "-X", "begin-selection"},
		{"send-keys", "-t", target, "-X", "end-of-line"},
		// Injecting the key name dispatches the configured copy-mode binding,
		// just as the end of a browser mouse drag does.
		{"send-keys", "-t", target, "MouseDragEnd1Pane"},
	}
	for _, arguments := range commands {
		commandArguments := append([]string{"-S", socket}, arguments...)
		if output, err := exec.CommandContext(ctx, binary, commandArguments...).CombinedOutput(); err != nil {
			t.Fatalf("copy marker %q through tmux mouse binding (%s): %v (%s)",
				marker, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
		}
	}
}

func requireIntegrationExecutable(t *testing.T, environment, path string) {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must be an absolute path, got %q", environment, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s=%q: %v", environment, path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("%s=%q is not an executable regular file (mode %v)", environment, path, info.Mode())
	}
}

func stageIntegrationTtydLibraries(directory string) (string, string, error) {
	const (
		pluginName        = "libwebsockets-evlib_uv.so"
		libraryName       = "libwebsockets.so.17"
		compiledPluginDir = "/usr/lib/x86_64-linux-gnu"
	)
	for _, libraryDirectory := range filepath.SplitList(os.Getenv("LD_LIBRARY_PATH")) {
		if libraryDirectory == "" {
			continue
		}
		pluginSource, err := filepath.Abs(filepath.Join(libraryDirectory, pluginName))
		if err != nil {
			return "", "", err
		}
		info, err := os.Stat(pluginSource)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		librarySource, err := filepath.Abs(filepath.Join(libraryDirectory, libraryName))
		if err != nil {
			return "", "", err
		}
		library, err := os.ReadFile(librarySource)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		compiledPath := []byte(compiledPluginDir)
		if !bytes.Contains(library, compiledPath) {
			continue
		}
		if len(directory) > len(compiledPluginDir) {
			return "", "", fmt.Errorf("temporary plugin path %q exceeds embedded path length %d", directory, len(compiledPluginDir))
		}
		replacement := make([]byte, len(compiledPluginDir))
		copy(replacement, directory)
		library = bytes.ReplaceAll(library, compiledPath, replacement)
		if err := os.WriteFile(filepath.Join(directory, libraryName), library, 0600); err != nil {
			return "", "", err
		}
		if err := os.Symlink(pluginSource, filepath.Join(directory, pluginName)); err != nil {
			return "", "", err
		}
		return pluginSource, directory, nil
	}
	return "", "", nil
}

func integrationCommand(t *testing.T, binary string, args ...string) string {
	t.Helper()
	output, err := exec.Command(binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %s: %v (%s)", binary, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}

func requireTmuxSession(t *testing.T, binary, socket, target string) {
	t.Helper()
	output, err := exec.Command(binary, "-S", socket, "has-session", "-t", target).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux target %q is absent from socket %s: %v (%s)", target, socket, err, strings.TrimSpace(string(output)))
	}
}

func integrationTmuxWindowTargets(t *testing.T, ctx context.Context, binary, socket, target string) []string {
	t.Helper()
	output, err := exec.CommandContext(ctx, binary, "-S", socket,
		"list-windows", "-t", target, "-F", tmuxWindowListFormat).CombinedOutput()
	if err != nil {
		t.Fatalf("list tmux windows for %s: %v (%s)", target, err, strings.TrimSpace(string(output)))
	}
	targets, err := parseTmuxWindowTargets(output)
	if err != nil {
		t.Fatal(err)
	}
	return targets
}

func requireTmuxColorEnvironment(t *testing.T, binary, socket string, scope ...string) {
	t.Helper()
	arguments := []string{"-S", socket, "show-environment"}
	arguments = append(arguments, scope...)
	output := integrationCommand(t, binary, arguments...)
	color := ""
	noColor := false
	for _, line := range strings.Split(output, "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch name {
		case tmuxColorEnvironment:
			color = value
		case tmuxNoColorEnvironment:
			noColor = true
		}
	}
	if color != tmuxColorEnvironmentValue || noColor {
		t.Fatalf("tmux environment %v = COLORTERM=%q NO_COLOR-present=%t; output=%q",
			scope, color, noColor, strings.TrimSpace(output))
	}
}

func requireInitialPaneColorEnvironment(t *testing.T, binary, socket, target string) {
	t.Helper()
	rawPID := integrationCommand(t, binary, "-S", socket,
		"display-message", "-p", "-t", target, "#{pane_pid}")
	panePID, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil || panePID < 1 {
		t.Fatalf("invalid tmux pane pid %q: %v", strings.TrimSpace(rawPID), err)
	}
	environment, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(panePID), "environ"))
	if err != nil {
		t.Fatalf("read first pane environment: %v", err)
	}
	color := ""
	noColor := false
	for _, entry := range bytes.Split(environment, []byte{0}) {
		name, value, ok := bytes.Cut(entry, []byte{'='})
		if !ok {
			continue
		}
		switch string(name) {
		case tmuxColorEnvironment:
			color = string(value)
		case tmuxNoColorEnvironment:
			noColor = true
		}
	}
	if color != tmuxColorEnvironmentValue || noColor {
		t.Fatalf("first pane environment = COLORTERM=%q NO_COLOR-present=%t", color, noColor)
	}
}

func requireTmuxDefaultColorResponses(t *testing.T, ctx context.Context, binary, socket, target, directory string) {
	t.Helper()
	want := []byte("\x1b]10;rgb:d2d2/d2d2/d2d2\a\x1b]11;rgb:2b2b/2b2b/2b2b\a")
	replyPath := filepath.Join(directory, "tmux-default-color-replies")
	// Run the same startup probe used by terminal applications such as Codex.
	// The private integration path contains no shell metacharacters; tmux types
	// this fixed test command into the pane's shell exactly as one command line.
	probeCommand := "stty raw -echo min 0 time 20; printf '\\033]10;?\\007\\033]11;?\\007'; " +
		"dd bs=1 count=" + strconv.Itoa(len(want)) + " status=none > '" + replyPath + "'; stty sane"
	if output, err := exec.CommandContext(ctx, binary, "-S", socket,
		"send-keys", "-t", target, probeCommand, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("send OSC 10/11 default-color probe: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if err := integrationEventually(4*time.Second, func() error {
		got, err := os.ReadFile(replyPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("OSC 10/11 replies = %q, want %q", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func requireTmuxBackgroundRendering(t *testing.T, ctx context.Context, binary, socket, target string) {
	t.Helper()
	const (
		backgroundSequence = "\x1b[48;2;48;48;48m"
		marker             = "__REMOTE_TERMINAL_BACKGROUND_FILL__"
	)
	command := "printf '\\033[48;2;48;48;48m" + marker + "\\033[0m\\n'"
	if output, err := exec.CommandContext(ctx, binary, "-S", socket,
		"send-keys", "-t", target, command, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("emit true-color terminal background: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if err := integrationEventually(3*time.Second, func() error {
		capture, err := exec.CommandContext(ctx, binary, "-S", socket,
			"capture-pane", "-p", "-e", "-t", target, "-S", "-20").CombinedOutput()
		if err != nil {
			return err
		}
		if !bytes.Contains(capture, []byte(backgroundSequence+marker)) {
			return fmt.Errorf("tmux capture does not retain the true-color background for %s", marker)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func integrationManagedProcess(t *testing.T, manager *Manager, id string) (*managedProcess, int) {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	managed := manager.processes[id]
	if managed == nil || processFinished(managed) {
		t.Fatalf("no live managed ttyd process for %s", id)
	}
	process, ok := managed.process.(*execProcess)
	if !ok || process.cmd == nil || process.cmd.Process == nil {
		t.Fatalf("managed ttyd has process type %T, want *execProcess", managed.process)
	}
	return managed, process.cmd.Process.Pid
}

func modeOf(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode()
}

func integrationEventually(timeout time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := check(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("condition not met within %s: %w", timeout, lastErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type integrationWebSocket struct {
	connection  net.Conn
	reader      *bufio.Reader
	subprotocol string
}

func dialIntegrationWebSocket(serverURL, path string, timeout time.Duration) (*integrationWebSocket, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" || parsed.Host == "" {
		return nil, fmt.Errorf("unsupported WebSocket test server URL %q", serverURL)
	}
	connection, err := net.DialTimeout("tcp", parsed.Host, timeout)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = connection.Close()
		}
	}()
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, keyBytes); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: path},
		Host:   parsed.Host,
		Header: make(http.Header),
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	request.Header.Set("Sec-WebSocket-Key", key)
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Protocol", "tty")
	if err := request.Write(connection); err != nil {
		return nil, fmt.Errorf("write upgrade request: %w", err)
	}

	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, fmt.Errorf("read upgrade response: %w", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		_ = response.Body.Close()
		return nil, fmt.Errorf("upgrade status %d, body %q", response.StatusCode, body)
	}
	if !integrationHeaderHasToken(response.Header, "Connection", "upgrade") ||
		!integrationHeaderHasToken(response.Header, "Upgrade", "websocket") {
		return nil, fmt.Errorf("invalid upgrade response headers: %v", response.Header)
	}
	digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	wantAccept := base64.StdEncoding.EncodeToString(digest[:])
	if response.Header.Get("Sec-WebSocket-Accept") != wantAccept {
		return nil, fmt.Errorf("Sec-WebSocket-Accept=%q, want %q", response.Header.Get("Sec-WebSocket-Accept"), wantAccept)
	}
	subprotocol := response.Header.Get("Sec-WebSocket-Protocol")
	if subprotocol != "tty" {
		return nil, fmt.Errorf("Sec-WebSocket-Protocol=%q, want %q", subprotocol, "tty")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	failed = false
	return &integrationWebSocket{connection: connection, reader: reader, subprotocol: subprotocol}, nil
}

func integrationHeaderHasToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func (websocket *integrationWebSocket) initializeTTyd(columns, rows int) error {
	payload, err := json.Marshal(struct {
		AuthToken string `json:"AuthToken"`
		Columns   int    `json:"columns"`
		Rows      int    `json:"rows"`
	}{Columns: columns, Rows: rows})
	if err != nil {
		return err
	}
	return websocket.writeFrame(0x2, payload)
}

func (websocket *integrationWebSocket) writeTTydInput(input string) error {
	payload := make([]byte, len(input)+1)
	payload[0] = '0'
	copy(payload[1:], input)
	return websocket.writeFrame(0x2, payload)
}

func (websocket *integrationWebSocket) readTTydPreferences(timeout time.Duration) (map[string]interface{}, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("timed out after %s waiting for ttyd preferences", timeout)
		}
		opcode, payload, err := websocket.readMessage(remaining)
		if err != nil {
			return nil, err
		}
		if opcode != 0x2 || len(payload) == 0 || payload[0] != '2' {
			continue
		}
		preferences := make(map[string]interface{})
		if err := json.Unmarshal(payload[1:], &preferences); err != nil {
			return nil, fmt.Errorf("decode ttyd preferences: %w", err)
		}
		return preferences, nil
	}
}

func (websocket *integrationWebSocket) readTTydOutputUntil(marker []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var output bytes.Buffer
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return output.Bytes(), fmt.Errorf("timed out after %s waiting for %q", timeout, marker)
		}
		opcode, payload, err := websocket.readMessage(remaining)
		if err != nil {
			return output.Bytes(), err
		}
		if opcode != 0x2 || len(payload) == 0 || payload[0] != '0' {
			continue
		}
		if output.Len()+len(payload)-1 > 2<<20 {
			return output.Bytes(), errors.New("ttyd output exceeded 2 MiB")
		}
		_, _ = output.Write(payload[1:])
		if bytes.Contains(output.Bytes(), marker) {
			return output.Bytes(), nil
		}
	}
}

func (websocket *integrationWebSocket) readMessage(timeout time.Duration) (byte, []byte, error) {
	if err := websocket.connection.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, nil, err
	}
	var messageOpcode byte
	var message []byte
	for {
		fin, opcode, payload, err := websocket.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch opcode {
		case 0x8:
			return 0, nil, fmt.Errorf("WebSocket peer closed connection: %x", payload)
		case 0x9:
			if err := websocket.writeFrame(0xA, payload); err != nil {
				return 0, nil, err
			}
			continue
		case 0xA:
			continue
		case 0x1, 0x2:
			if messageOpcode != 0 {
				return 0, nil, errors.New("WebSocket started a new message before finishing the previous message")
			}
			messageOpcode = opcode
			message = append(message, payload...)
		case 0x0:
			if messageOpcode == 0 {
				return 0, nil, errors.New("WebSocket continuation frame without an initial frame")
			}
			message = append(message, payload...)
		default:
			return 0, nil, fmt.Errorf("unsupported WebSocket opcode %#x", opcode)
		}
		if len(message) > 4<<20 {
			return 0, nil, errors.New("WebSocket message exceeded 4 MiB")
		}
		if fin && messageOpcode != 0 {
			return messageOpcode, message, nil
		}
	}
}

func (websocket *integrationWebSocket) readFrame() (bool, byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(websocket.reader, header); err != nil {
		return false, 0, nil, err
	}
	if header[0]&0x70 != 0 {
		return false, 0, nil, fmt.Errorf("WebSocket frame uses unexpected RSV bits %#x", header[0]&0x70)
	}
	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0f
	if header[1]&0x80 != 0 {
		return false, 0, nil, errors.New("server sent a masked WebSocket frame")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		encoded := make([]byte, 2)
		if _, err := io.ReadFull(websocket.reader, encoded); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(encoded))
	case 127:
		encoded := make([]byte, 8)
		if _, err := io.ReadFull(websocket.reader, encoded); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(encoded)
	}
	if opcode >= 0x8 && (!fin || length > 125) {
		return false, 0, nil, errors.New("invalid WebSocket control frame")
	}
	if length > 4<<20 {
		return false, 0, nil, fmt.Errorf("WebSocket frame length %d exceeds 4 MiB", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(websocket.reader, payload); err != nil {
		return false, 0, nil, err
	}
	return fin, opcode, payload, nil
}

func (websocket *integrationWebSocket) writeFrame(opcode byte, payload []byte) error {
	if err := websocket.connection.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	header := []byte{0x80 | opcode}
	switch length := len(payload); {
	case length <= 125:
		header = append(header, 0x80|byte(length))
	case length <= 65535:
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0,
			byte(uint64(length)>>24), byte(uint64(length)>>16), byte(uint64(length)>>8), byte(length))
	}
	mask := make([]byte, 4)
	if _, err := io.ReadFull(rand.Reader, mask); err != nil {
		return err
	}
	header = append(header, mask...)
	masked := make([]byte, len(payload))
	for index := range payload {
		masked[index] = payload[index] ^ mask[index%len(mask)]
	}
	if err := integrationWriteAll(websocket.connection, header); err != nil {
		return err
	}
	return integrationWriteAll(websocket.connection, masked)
}

func integrationWriteAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (websocket *integrationWebSocket) Close() error {
	status := uint16(1000)
	payload := []byte{byte(status >> 8), byte(status)}
	writeErr := websocket.writeFrame(0x8, payload)
	closeErr := websocket.connection.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
