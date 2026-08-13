package codeservers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type fakeTmuxSession struct {
	id      string
	folder  string
	created int64
}

type fakeRunner struct {
	mu                    sync.Mutex
	sessions              map[string]*fakeTmuxSession
	calls                 [][]string
	tmuxSocket            string
	tmuxListener          net.Listener
	startErrorAfterCreate error
	exitOnInterrupt       bool
	killSessionError      error
	killServerError       error
	killServerLeavesAlive bool
	reportExitOnEmptyList bool
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{sessions: make(map[string]*fakeTmuxSession), exitOnInterrupt: true}
}

func (f *fakeRunner) snapshotCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([][]string, len(f.calls))
	for index := range f.calls {
		calls[index] = append([]string(nil), f.calls[index]...)
	}
	return calls
}

func (f *fakeRunner) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func (f *fakeRunner) Run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{binary}, args...))
	if len(args) == 1 && args[0] == "-V" {
		return []byte("version"), nil
	}
	if reflect.DeepEqual(args, []string{"--config", "/dev/null", "--version"}) {
		return []byte("version"), nil
	}
	commandArgs := args
	if len(commandArgs) >= 2 && commandArgs[0] == "-f" {
		if commandArgs[1] != "/dev/null" {
			return nil, errors.New("tmux did not use an empty configuration")
		}
		commandArgs = commandArgs[2:]
	}
	if len(commandArgs) < 3 || commandArgs[0] != "-S" {
		return nil, fmt.Errorf("unexpected fake command: %v", args)
	}
	f.tmuxSocket = commandArgs[1]
	command := commandArgs[2]
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch command {
	case "list-sessions":
		if len(f.sessions) == 0 {
			if f.reportExitOnEmptyList {
				f.reportExitOnEmptyList = false
				return []byte("server exited unexpectedly\n"), errors.New("exit status 1")
			}
			return []byte("no server running"), errors.New("exit status 1")
		}
		targets := make([]string, 0, len(f.sessions))
		for target := range f.sessions {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		lines := make([]string, 0, len(targets))
		for _, target := range targets {
			session := f.sessions[target]
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%d", target, session.id, session.folder, session.created))
		}
		return []byte(strings.Join(lines, "\n")), nil
	case "new-session":
		if len(commandArgs) < 7 || commandArgs[3] != "-d" || commandArgs[4] != "-s" {
			return nil, fmt.Errorf("unsafe new-session argv: %v", commandArgs)
		}
		target := commandArgs[5]
		if _, exists := f.sessions[target]; exists {
			return nil, errors.New("duplicate session")
		}
		if err := os.MkdirAll(filepath.Dir(f.tmuxSocket), 0700); err != nil {
			return nil, err
		}
		if f.tmuxListener == nil {
			listener, err := net.Listen("unix", f.tmuxSocket)
			if err != nil {
				return nil, err
			}
			f.tmuxListener = listener
		}
		f.sessions[target] = &fakeTmuxSession{created: 1700000000}
		if f.startErrorAfterCreate != nil {
			return []byte("canceled after accepted"), f.startErrorAfterCreate
		}
		return nil, nil
	case "set-option":
		if len(commandArgs) != 7 || commandArgs[3] != "-t" {
			return nil, fmt.Errorf("unsafe set-option argv: %v", commandArgs)
		}
		session := f.sessions[commandArgs[4]]
		if session == nil {
			return nil, errors.New("can't find session")
		}
		switch commandArgs[5] {
		case tmuxIDOption:
			session.id = commandArgs[6]
		case tmuxFolderOption:
			session.folder = commandArgs[6]
		default:
			return nil, fmt.Errorf("unknown option %q", commandArgs[5])
		}
		return nil, nil
	case "has-session":
		if len(commandArgs) != 5 || commandArgs[3] != "-t" {
			return nil, fmt.Errorf("unsafe has-session argv: %v", commandArgs)
		}
		if _, exists := f.sessions[commandArgs[4]]; !exists {
			return []byte("can't find session"), errors.New("exit status 1")
		}
		return nil, nil
	case "send-keys":
		if len(commandArgs) != 6 || commandArgs[3] != "-t" || commandArgs[5] != "C-c" {
			return nil, fmt.Errorf("unsafe interrupt argv: %v", commandArgs)
		}
		target := strings.TrimSuffix(commandArgs[4], ":0.0")
		if _, exists := f.sessions[target]; !exists {
			return []byte("can't find session"), errors.New("exit status 1")
		}
		if f.exitOnInterrupt {
			delete(f.sessions, target)
		}
		return nil, nil
	case "kill-session":
		if len(commandArgs) != 5 || commandArgs[3] != "-t" {
			return nil, fmt.Errorf("unsafe kill-session argv: %v", commandArgs)
		}
		if f.killSessionError != nil {
			return []byte("kill failed"), f.killSessionError
		}
		delete(f.sessions, commandArgs[4])
		return nil, nil
	case "kill-server":
		if len(commandArgs) != 3 {
			return nil, fmt.Errorf("unsafe kill-server argv: %v", commandArgs)
		}
		if f.killServerError != nil {
			return []byte("kill server failed"), f.killServerError
		}
		if f.killServerLeavesAlive {
			return nil, nil
		}
		f.sessions = make(map[string]*fakeTmuxSession)
		if f.tmuxListener != nil {
			_ = f.tmuxListener.Close()
			f.tmuxListener = nil
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected fake tmux command: %q", command)
	}
}

func testManager(t *testing.T, maxServers int) (*Manager, *fakeRunner, Config) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "cs-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	config := Config{
		TmuxBinary:       "tmux-test",
		CodeServerBinary: "code-server-test",
		RuntimeDir:       filepath.Join(base, "run"),
		StateDir:         filepath.Join(base, "state"),
		HomeDir:          home,
		MaxServers:       maxServers,
		StartTimeout:     time.Second,
	}
	runner := newFakeRunner()
	t.Cleanup(func() {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		if runner.tmuxListener != nil {
			_ = runner.tmuxListener.Close()
			runner.tmuxListener = nil
		}
	})
	manager := NewManager(config)
	manager.runner = runner
	manager.pollInterval = time.Millisecond
	manager.livenessInterval = time.Millisecond
	manager.proxyVerifyInterval = time.Second
	manager.shutdownGrace = 10 * time.Millisecond
	manager.healthProbe = func(context.Context, string) error { return nil }
	manager.socketReady = func(string) bool { return true }
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return manager, runner, config
}

func TestInitializeValidatesConfigurationAndClearsOnlyRuntimeState(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	if manager.canonicalHome != config.HomeDir {
		t.Fatalf("canonicalHome = %q, want %q", manager.canonicalHome, config.HomeDir)
	}
	if info, err := os.Stat(manager.runtimeRoot()); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("runtime root mode = %v err=%v", modeOf(info), err)
	}
	if info, err := os.Stat(manager.profilesRoot()); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("profiles root mode = %v err=%v", modeOf(info), err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("dependency calls = %d, want 2: %v", len(runner.calls), runner.calls)
	}
	if !reflect.DeepEqual(runner.calls[1], []string{config.CodeServerBinary, "--config", "/dev/null", "--version"}) {
		t.Fatalf("code-server dependency probe touched user config: %v", runner.calls[1])
	}

	invalid := NewManager(Config{MaxServers: 9})
	if err := invalid.Initialize(context.Background()); err == nil {
		t.Fatal("Initialize accepted invalid configuration")
	}
}

func TestBrowseCanonicalizesSymlinksSortsAndReportsParent(t *testing.T) {
	manager, _, config := testManager(t, 2)
	actual := filepath.Join(config.HomeDir, "z-actual")
	if err := os.MkdirAll(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(config.HomeDir, "a-child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.HomeDir, "plain-file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, filepath.Join(config.HomeDir, "m-link")); err != nil {
		t.Fatal(err)
	}

	listing, err := manager.Browse(context.Background(), "")
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if listing.Path != config.HomeDir || listing.ParentPath == nil || *listing.ParentPath != filepath.Dir(config.HomeDir) {
		t.Fatalf("listing path/parent = %q/%v", listing.Path, listing.ParentPath)
	}
	want := []Directory{
		{Name: "a-child", Path: filepath.Join(config.HomeDir, "a-child")},
		{Name: "m-link", Path: actual},
		{Name: "z-actual", Path: actual},
	}
	if !reflect.DeepEqual(listing.Directories, want) {
		t.Fatalf("directories = %#v, want %#v", listing.Directories, want)
	}
	root, err := manager.Browse(context.Background(), string(filepath.Separator))
	if err != nil {
		t.Fatalf("Browse root: %v", err)
	}
	if root.ParentPath != nil {
		t.Fatalf("root parent = %v, want nil", root.ParentPath)
	}
}

func TestBrowseRejectsInvalidAndMissingPaths(t *testing.T) {
	manager, _, config := testManager(t, 2)
	for _, path := range []string{"relative", config.HomeDir + "\nchild", "\x00"} {
		if _, err := manager.Browse(context.Background(), path); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Browse(%q) error = %v, want ErrInvalidPath", path, err)
		}
	}
	if _, err := manager.Browse(context.Background(), filepath.Join(config.HomeDir, "missing")); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("missing error = %v, want ErrDirectoryNotFound", err)
	}
}

func TestBrowseBoundsScannedEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("creates enough directory entries to exercise the scan bound")
	}
	manager, _, config := testManager(t, 2)
	root := filepath.Join(config.HomeDir, "large")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= maxDirectoryScanEntries; index++ {
		name := filepath.Join(root, fmt.Sprintf("file-%05d", index))
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listing, err := manager.Browse(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !listing.Truncated || len(listing.Directories) != 0 {
		t.Fatalf("large listing = truncated=%t dirs=%d", listing.Truncated, len(listing.Directories))
	}
}

func TestMapDirectoryErrorClassifiesPathShapeFailures(t *testing.T) {
	for _, test := range []struct {
		err  error
		want error
	}{
		{&os.PathError{Op: "stat", Path: "/file/child", Err: syscall.ENOTDIR}, ErrDirectoryNotFound},
		{&os.PathError{Op: "eval", Path: "/loop", Err: syscall.ELOOP}, ErrInvalidPath},
		{&os.PathError{Op: "stat", Path: strings.Repeat("x", 5000), Err: syscall.ENAMETOOLONG}, ErrInvalidPath},
		{&os.PathError{Op: "stat", Path: "/bad", Err: syscall.EINVAL}, ErrInvalidPath},
	} {
		if got := mapDirectoryError(test.err); !errors.Is(got, test.want) {
			t.Errorf("mapDirectoryError(%v) = %v, want %v", test.err, got, test.want)
		}
	}
}

func TestValidateConfigRejectsBroadAndOverlappingManagedRoots(t *testing.T) {
	base := Config{
		TmuxBinary: "tmux", CodeServerBinary: "code-server",
		RuntimeDir: "/run/remoteterminal", StateDir: "/var/lib/remoteterminal",
		HomeDir: "/home/operator", MaxServers: 2, StartTimeout: time.Second,
	}
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"filesystem root", func(c *Config) { c.RuntimeDir = "/" }},
		{"broad runtime", func(c *Config) { c.RuntimeDir = "/run" }},
		{"broad state", func(c *Config) { c.StateDir = "/var" }},
		{"state below runtime", func(c *Config) { c.StateDir = "/run/remoteterminal/state" }},
		{"runtime below state", func(c *Config) { c.RuntimeDir = "/var/lib/remoteterminal/run" }},
		{"same root", func(c *Config) { c.StateDir = c.RuntimeDir }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.change(&config)
			if err := validateConfig(config); err == nil {
				t.Fatal("validateConfig accepted unsafe managed roots")
			}
		})
	}
}

func TestSecureDirectoryRejectsSymbolicLinkTargets(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := secureDirectory(link); err == nil {
		t.Fatal("secureDirectory accepted a symbolic-link path")
	}
}

func TestCreateUsesFixedArgvPersistsProfileAndReusesCanonicalFolder(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project with spaces;$(unsafe)")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(config.HomeDir, "project-link")
	if err := os.Symlink(folder, link); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")

	instance, reused, err := manager.Create(context.Background(), link)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if reused {
		t.Fatal("new Create reported reused")
	}
	if instance.ID != "30313233343536373839616263646566" || instance.FolderPath != folder ||
		instance.Name != filepath.Base(folder) || instance.URL != "/code/"+instance.ID+"/" {
		t.Fatalf("unexpected instance: %#v", instance)
	}
	profile := manager.profileDir(folder)
	wantArgs := CodeServerArguments(config.CodeServerBinary, manager.configPath(instance.ID),
		manager.httpSocket(instance.ID), manager.sessionSocket(instance.ID), profile, folder)
	if wantArgs[len(wantArgs)-2] != "--" || wantArgs[len(wantArgs)-1] != folder {
		t.Fatalf("CodeServerArguments ending = %q", wantArgs[len(wantArgs)-2:])
	}
	var startCall []string
	for _, call := range runner.calls {
		if len(call) > 6 && call[1] == "-f" && call[5] == "new-session" {
			startCall = call
		}
	}
	if startCall == nil {
		t.Fatalf("new-session call missing: %v", runner.calls)
	}
	wantStart := append([]string{config.TmuxBinary, "-f", "/dev/null", "-S", manager.tmuxSocket(),
		"new-session", "-d", "-s", tmuxTarget(instance.ID)}, wantArgs...)
	if !reflect.DeepEqual(startCall, wantStart) {
		t.Fatalf("new-session argv = %#v, want %#v", startCall, wantStart)
	}
	for _, child := range []string{"user-data", "extensions"} {
		if info, statErr := os.Stat(filepath.Join(profile, child)); statErr != nil || !info.IsDir() {
			t.Fatalf("profile %s missing: %v", child, statErr)
		}
	}
	configData, readErr := os.ReadFile(manager.configPath(instance.ID))
	if readErr != nil || string(configData) != "auth: none\ncert: false\n" {
		t.Fatalf("config = %q err=%v", configData, readErr)
	}
	if info, statErr := os.Stat(manager.configPath(instance.ID)); statErr != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %v err=%v", modeOf(info), statErr)
	}

	reusedInstance, reused, err := manager.Create(context.Background(), folder)
	if err != nil || !reused || reusedInstance.ID != instance.ID {
		t.Fatalf("reuse = %#v/%t/%v", reusedInstance, reused, err)
	}
}

func TestCreateEnforcesLimitAndProfilesDifferByCanonicalPath(t *testing.T) {
	manager, _, config := testManager(t, 1)
	first := filepath.Join(config.HomeDir, "first")
	second := filepath.Join(config.HomeDir, "second")
	for _, folder := range []string{first, second} {
		if err := os.Mkdir(folder, 0700); err != nil {
			t.Fatal(err)
		}
	}
	manager.random = strings.NewReader("0123456789abcdef")
	if _, _, err := manager.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Create(context.Background(), second); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("second Create error = %v, want ErrLimitReached", err)
	}
	if manager.profileDir(first) == manager.profileDir(second) {
		t.Fatal("different canonical folders share a profile")
	}
}

func TestCreateCleansExactSessionAndRuntimeOnFailure(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	runner.startErrorAfterCreate = context.Canceled
	if _, _, err := manager.Create(context.Background(), folder); !errors.Is(err, ErrStartFailed) {
		t.Fatalf("Create error = %v, want ErrStartFailed", err)
	}
	if len(runner.sessions) != 0 {
		t.Fatalf("sessions after failure = %#v", runner.sessions)
	}
	id := "30313233343536373839616263646566"
	if _, err := os.Stat(manager.instanceDir(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime survived failed Create: %v", err)
	}
}

func TestCreateCollisionNeverKillsPreExistingSession(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	collisionID := "30313233343536373839616263646566"
	nextID := "6768696a6b6c6d6e6f70717273747576"
	manager.random = strings.NewReader("0123456789abcdefghijklmnopqrstuv")
	runner.mu.Lock()
	runner.sessions[tmuxTarget(collisionID)] = &fakeTmuxSession{
		id:      collisionID,
		folder:  base64.RawURLEncoding.EncodeToString([]byte(filepath.Join(config.HomeDir, "existing"))),
		created: 1700000000,
	}
	listener, err := net.Listen("unix", manager.tmuxSocket())
	if err == nil {
		runner.tmuxListener = listener
	}
	runner.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if instance.ID != nextID {
		t.Fatalf("created ID = %q, want %q after collision", instance.ID, nextID)
	}
	runner.mu.Lock()
	_, collisionSurvived := runner.sessions[tmuxTarget(collisionID)]
	runner.mu.Unlock()
	if !collisionSurvived {
		t.Fatal("pre-existing colliding tmux target was killed")
	}
	for _, call := range runner.snapshotCalls() {
		if reflect.DeepEqual(call, []string{config.TmuxBinary, "-S", manager.tmuxSocket(),
			"kill-session", "-t", tmuxTarget(collisionID)}) {
			t.Fatalf("collision triggered unsafe exact cleanup: %v", call)
		}
	}
}

func TestListAndProxyStayResponsiveDuringReadinessWait(t *testing.T) {
	manager, _, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "slow")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	manager.config.StartTimeout = 2 * time.Second
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var once sync.Once
	manager.healthProbe = func(context.Context, string) error {
		once.Do(func() { close(probeStarted) })
		select {
		case <-releaseProbe:
			return nil
		default:
			return errors.New("not ready")
		}
	}
	createDone := make(chan error, 1)
	go func() {
		_, _, err := manager.Create(context.Background(), folder)
		createDone <- err
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("Create did not reach readiness wait")
	}

	listDone := make(chan error, 1)
	go func() {
		_, err := manager.List(context.Background())
		listDone <- err
	}()
	select {
	case err := <-listDone:
		if err != nil {
			t.Fatalf("List during launch: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("List blocked behind code-server readiness wait")
	}
	id := "30313233343536373839616263646566"
	proxyDone := make(chan error, 1)
	go func() {
		_, err := manager.Proxy(context.Background(), id)
		proxyDone <- err
	}()
	select {
	case err := <-proxyDone:
		if !errors.Is(err, ErrNotRunning) {
			t.Fatalf("Proxy during launch = %v, want ErrNotRunning", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Proxy blocked behind code-server readiness wait")
	}
	close(releaseProbe)
	if err := <-createDone; err != nil {
		t.Fatalf("Create after readiness release: %v", err)
	}
}

func TestCreateRechecksShutdownAfterSuccessfulHealthProbe(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	manager.healthProbe = func(context.Context, string) error {
		close(probeEntered)
		<-releaseProbe
		return nil
	}
	createDone := make(chan error, 1)
	go func() {
		_, _, err := manager.Create(context.Background(), folder)
		createDone <- err
	}()
	<-probeEntered
	atomic.StoreUint32(&manager.shutdownRequested, 1)
	close(releaseProbe)
	if err := <-createDone; !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Create = %v, want ErrShuttingDown", err)
	}
	runner.mu.Lock()
	sessions := len(runner.sessions)
	runner.mu.Unlock()
	if sessions != 0 {
		t.Fatalf("session survived shutdown race: %d", sessions)
	}
}

func TestShutdownInterruptsLaunchWaitingForHealth(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "slow-project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	manager.config.StartTimeout = 5 * time.Second
	probeStarted := make(chan struct{}, 1)
	manager.healthProbe = func(context.Context, string) error {
		select {
		case probeStarted <- struct{}{}:
		default:
		}
		return errors.New("not ready")
	}

	createDone := make(chan error, 1)
	go func() {
		_, _, err := manager.Create(context.Background(), folder)
		createDone <- err
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("Create never began its health probe")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case err := <-createDone:
		if !errors.Is(err, ErrShuttingDown) {
			t.Fatalf("Create error = %v, want ErrShuttingDown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown request did not interrupt readiness polling")
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if len(runner.sessions) != 0 {
		t.Fatalf("sessions survived interrupted launch: %#v", runner.sessions)
	}
}

func TestListSkipsMalformedSessionsAndReconcilesRuntime(t *testing.T) {
	manager, runner, _ := testManager(t, 2)
	validID := "11111111111111111111111111111111"
	runner.sessions[tmuxTarget(validID)] = &fakeTmuxSession{
		id: validID, folder: base64.RawURLEncoding.EncodeToString([]byte("/tmp/work")), created: 1700000000,
	}
	runner.sessions["not-ours"] = &fakeTmuxSession{id: "bad", folder: "bad", created: 1}
	runner.mu.Lock()
	listener, err := net.Listen("unix", manager.tmuxSocket())
	if err == nil {
		runner.tmuxListener = listener
	}
	runner.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	staleID := "22222222222222222222222222222222"
	if err := os.MkdirAll(manager.instanceDir(staleID), 0700); err != nil {
		t.Fatal(err)
	}
	instances, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].ID != validID || instances[0].FolderPath != "/tmp/work" {
		t.Fatalf("instances = %#v", instances)
	}
	if _, err := os.Stat(manager.instanceDir(staleID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale runtime survived reconcile: %v", err)
	}
}

func TestDeleteInterruptsThenRemovesRuntimeButPreservesProfile(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	profile := manager.profileDir(folder)
	if err := manager.Delete(context.Background(), instance.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(manager.instanceDir(instance.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime survived Delete: %v", err)
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("profile removed by Delete: %v", err)
	}
	if len(runner.sessions) != 0 {
		t.Fatalf("sessions survived Delete: %#v", runner.sessions)
	}
	if err := manager.Delete(context.Background(), "bad"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid Delete error = %v", err)
	}
}

func TestDeleteFallsBackToExactKillSession(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	runner.exitOnInterrupt = false
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(context.Background(), instance.ID); err != nil {
		t.Fatal(err)
	}
	wantKill := []string{config.TmuxBinary, "-S", manager.tmuxSocket(), "kill-session", "-t", tmuxTarget(instance.ID)}
	if !containsCall(runner.calls, wantKill) {
		t.Fatalf("exact kill-session call missing from %v", runner.calls)
	}
}

func TestDeleteCanceledContextStillUsesFreshExactFallback(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	runner.exitOnInterrupt = false
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	// Force the cancellation through discovery as well as stop polling; the
	// exact ID must still be cleaned with a fresh context.
	manager.mu.Lock()
	delete(manager.instances, instance.ID)
	manager.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Delete(ctx, instance.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete = %v, want context.Canceled after confirmed fallback", err)
	}
	if runner.sessionCount() != 0 {
		t.Fatal("canceled Delete left session running")
	}
	if _, err := os.Stat(manager.instanceDir(instance.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime survived confirmed fallback: %v", err)
	}
	wantKill := []string{config.TmuxBinary, "-S", manager.tmuxSocket(), "kill-session", "-t", tmuxTarget(instance.ID)}
	if !containsCall(runner.snapshotCalls(), wantKill) {
		t.Fatal("canceled Delete did not use exact fresh-context fallback")
	}
}

func TestDeletePreservesRuntimeWhenExactStopUnconfirmedAndCanRetry(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	runner.exitOnInterrupt = false
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.killSessionError = errors.New("injected kill failure")
	runner.mu.Unlock()
	if err := manager.Delete(context.Background(), instance.ID); err == nil {
		t.Fatal("Delete succeeded without confirming exact session absence")
	}
	if runner.sessionCount() != 1 {
		t.Fatal("failed Delete changed live session unexpectedly")
	}
	if _, err := os.Stat(manager.instanceDir(instance.ID)); err != nil {
		t.Fatalf("failed Delete removed retryable runtime: %v", err)
	}
	runner.mu.Lock()
	runner.killSessionError = nil
	runner.mu.Unlock()
	if err := manager.Delete(context.Background(), instance.ID); err != nil {
		t.Fatalf("retry Delete: %v", err)
	}
	if runner.sessionCount() != 0 {
		t.Fatal("retry Delete did not stop session")
	}
}

func TestProxyFastPathAndBoundedDeadSessionEviction(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	before := len(runner.snapshotCalls())
	first, err := manager.Proxy(context.Background(), instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.commandMu.Lock()
	fastDone := make(chan error, 1)
	go func() {
		_, err := manager.Proxy(context.Background(), instance.ID)
		fastDone <- err
	}()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("cached Proxy while lifecycle command busy: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		manager.commandMu.Unlock()
		t.Fatal("cached Proxy waited for the tmux lifecycle mutex")
	}
	manager.commandMu.Unlock()
	for index := 0; index < 20; index++ {
		handler, err := manager.Proxy(context.Background(), instance.ID)
		if err != nil || reflect.ValueOf(handler).Pointer() != reflect.ValueOf(first).Pointer() {
			t.Fatalf("cached Proxy %d = %v/%v", index, handler, err)
		}
	}
	after := len(runner.snapshotCalls())
	if after != before {
		t.Fatalf("cached Proxy issued %d tmux commands", after-before)
	}

	runner.mu.Lock()
	delete(runner.sessions, tmuxTarget(instance.ID))
	runner.mu.Unlock()
	manager.mu.Lock()
	manager.instances[instance.ID].lastVerified = time.Now().Add(-2 * manager.proxyVerifyInterval)
	manager.mu.Unlock()
	if _, err := manager.Proxy(context.Background(), instance.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Proxy dead session = %v, want ErrNotFound", err)
	}
}

func TestShutdownUsesDedicatedServerAndRejectsFurtherOperations(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	if _, _, err := manager.Create(context.Background(), folder); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	wantKillServer := []string{config.TmuxBinary, "-S", manager.tmuxSocket(), "kill-server"}
	if !containsCall(runner.calls, wantKillServer) {
		t.Fatalf("dedicated kill-server call missing: %v", runner.calls)
	}
	if _, err := manager.List(context.Background()); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("List after Shutdown = %v", err)
	}
}

func TestShutdownAcceptsLastSessionServerExitAndStillConfirmsKill(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.reportExitOnEmptyList = true
	runner.mu.Unlock()

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after last-session server exit: %v", err)
	}
	if _, err := os.Stat(manager.instanceDir(instance.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime survived confirmed shutdown: %v", err)
	}

	calls := runner.snapshotCalls()
	killIndex := -1
	confirmationIndex := -1
	for index, call := range calls {
		if len(call) < 4 || call[1] != "-S" || call[2] != manager.tmuxSocket() {
			continue
		}
		switch call[3] {
		case "kill-server":
			killIndex = index
		case "list-sessions":
			if killIndex >= 0 && index > killIndex {
				confirmationIndex = index
			}
		}
	}
	if killIndex < 0 || confirmationIndex < 0 {
		t.Fatalf("shutdown did not preserve kill-server post-confirmation: %v", calls)
	}
}

func TestTmuxServerMissingFromProbeMatchesUnexpectedExitExactly(t *testing.T) {
	for _, test := range []struct {
		output string
		want   bool
	}{
		{output: "server exited unexpectedly", want: true},
		{output: "  SERVER EXITED UNEXPECTEDLY\n", want: true},
		{output: "warning: server exited unexpectedly"},
		{output: "server exited unexpectedly: protocol failure"},
		{output: "server exited unexpectedly again"},
	} {
		if got := tmuxServerMissingFromProbe([]byte(test.output)); got != test.want {
			t.Errorf("tmuxServerMissingFromProbe(%q) = %t, want %t", test.output, got, test.want)
		}
	}
}

func TestShutdownPreservesRuntimeWhenKillServerUnconfirmedAndRetries(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	runner.exitOnInterrupt = false
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	instance, _, err := manager.Create(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.killServerError = errors.New("injected kill-server failure")
	runner.mu.Unlock()
	if err := manager.Shutdown(context.Background()); err == nil {
		t.Fatal("Shutdown succeeded with unconfirmed kill-server")
	}
	if _, err := os.Stat(manager.instanceDir(instance.ID)); err != nil {
		t.Fatalf("failed Shutdown removed retryable runtime: %v", err)
	}
	manager.mu.Lock()
	_, retained := manager.instances[instance.ID]
	manager.mu.Unlock()
	if !retained {
		t.Fatal("failed Shutdown discarded retryable lifecycle state")
	}
	runner.mu.Lock()
	runner.killServerError = nil
	runner.mu.Unlock()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
	if _, err := os.Stat(manager.instanceDir(instance.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime survived successful retry: %v", err)
	}
}

func TestKillPrivateServerRequiresPostKillConfirmation(t *testing.T) {
	manager, runner, config := testManager(t, 2)
	folder := filepath.Join(config.HomeDir, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manager.random = strings.NewReader("0123456789abcdef")
	if _, _, err := manager.Create(context.Background(), folder); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.killServerLeavesAlive = true
	runner.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	confirmed, err := manager.killPrivateServer(ctx)
	if confirmed || err == nil {
		t.Fatalf("killPrivateServer = confirmed=%t err=%v, want unconfirmed error", confirmed, err)
	}
	if runner.sessionCount() == 0 {
		t.Fatal("test runner unexpectedly removed session")
	}
}

func TestInitializeRejectsSymlinkTmuxSocketWithoutInvokingKillServer(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "cssymlink-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	runtimeDir := filepath.Join(base, "run")
	stateDir := filepath.Join(base, "state")
	for _, directory := range []string{home, runtimeDir, stateDir} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	canary := filepath.Join(base, "canary.sock")
	listener, err := net.Listen("unix", canary)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	manager := NewManager(Config{
		TmuxBinary: "tmux-test", CodeServerBinary: "code-server-test",
		RuntimeDir: runtimeDir, StateDir: stateDir, HomeDir: home,
		MaxServers: 2, StartTimeout: time.Second,
	})
	runner := newFakeRunner()
	manager.runner = runner
	if err := os.Symlink(canary, manager.tmuxSocket()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Initialize(context.Background()); err == nil || !strings.Contains(err.Error(), "real Unix socket") {
		t.Fatalf("Initialize unsafe tmux socket = %v", err)
	}
	for _, call := range runner.snapshotCalls() {
		if len(call) > 3 && call[3] == "kill-server" {
			t.Fatalf("unsafe socket reached tmux kill-server: %v", call)
		}
	}
	if _, err := os.Lstat(canary); err != nil {
		t.Fatalf("canary socket was affected: %v", err)
	}
}

func TestValidIDAndCodeServerArguments(t *testing.T) {
	if !ValidID("0123456789abcdef0123456789abcdef") || ValidID("0123456789ABCDEF0123456789ABCDEF") || ValidID("short") {
		t.Fatal("ValidID accepted or rejected an invalid case")
	}
	args := CodeServerArguments("/opt/code-server", "/run/config.yaml", "/run/http.sock", "/run/ipc.sock", "/state/profile", "/work")
	want := []string{
		"/opt/code-server", "--config", "/run/config.yaml", "--auth", "none",
		"--socket", "/run/http.sock", "--socket-mode", "0600",
		"--session-socket", "/run/ipc.sock", "--user-data-dir", "/state/profile/user-data",
		"--extensions-dir", "/state/profile/extensions", "--ignore-last-opened",
		"--disable-telemetry", "--disable-update-check", "--disable-proxy", "--", "/work",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("CodeServerArguments = %#v, want %#v", args, want)
	}
}

func containsCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

func modeOf(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
