package sessions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTmuxSession struct {
	target  string
	id      string
	name    string
	mouse   bool
	created int64
}

type fakeRunner struct {
	mu                         sync.Mutex
	sessions                   map[string]*fakeTmuxSession
	calls                      [][]string
	newSessionErrorAfterCreate error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{sessions: make(map[string]*fakeTmuxSession)}
}

func (f *fakeRunner) Run(_ context.Context, binary string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := append([]string{binary}, args...)
	f.calls = append(f.calls, call)
	if len(args) == 1 && (args[0] == "-V" || args[0] == "--version") {
		return []byte("version"), nil
	}
	commandArgs := args
	if len(commandArgs) >= 2 && commandArgs[0] == "-f" {
		if commandArgs[1] != "/dev/null" {
			return nil, fmt.Errorf("unsafe tmux config: %v", call)
		}
		commandArgs = commandArgs[2:]
	}
	if len(commandArgs) < 4 || commandArgs[0] != "-S" {
		return nil, fmt.Errorf("unexpected command: %v", call)
	}
	socket := commandArgs[1]
	command := commandArgs[2]
	switch command {
	case "list-sessions":
		if len(f.sessions) == 0 {
			return []byte("no server running"), errors.New("exit status 1")
		}
		targets := make([]string, 0, len(f.sessions))
		for target := range f.sessions {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		var lines []string
		for _, target := range targets {
			session := f.sessions[target]
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t0\t1\t%d", target, session.id, session.name, session.created))
		}
		return []byte(strings.Join(lines, "\n")), nil
	case "new-session":
		if len(args) != 8 || args[0] != "-f" || args[1] != "/dev/null" ||
			len(commandArgs) != 6 || commandArgs[3] != "-d" || commandArgs[4] != "-s" {
			return nil, fmt.Errorf("unsafe new-session args: %v", call)
		}
		target := commandArgs[5]
		if _, exists := f.sessions[target]; exists {
			return nil, errors.New("duplicate target")
		}
		if err := os.WriteFile(socket, []byte("fake socket marker"), 0600); err != nil {
			return nil, err
		}
		f.sessions[target] = &fakeTmuxSession{target: target, created: 1700000000}
		if f.newSessionErrorAfterCreate != nil {
			return []byte("simulated cancellation after tmux accepted new-session"), f.newSessionErrorAfterCreate
		}
		return nil, nil
	case "set-option":
		if len(commandArgs) != 7 || commandArgs[3] != "-t" {
			return nil, fmt.Errorf("unsafe set-option args: %v", call)
		}
		session := f.sessions[commandArgs[4]]
		if session == nil {
			return nil, errors.New("target missing")
		}
		switch commandArgs[5] {
		case "@remoteterminal-id":
			session.id = commandArgs[6]
		case "@remoteterminal-name":
			session.name = commandArgs[6]
		case "mouse":
			if commandArgs[6] != "on" {
				return nil, errors.New("mouse must be enabled")
			}
			session.mouse = true
		default:
			return nil, errors.New("unexpected option")
		}
		return nil, nil
	case "kill-session":
		if len(commandArgs) != 5 || commandArgs[3] != "-t" {
			return nil, fmt.Errorf("unsafe kill args: %v", call)
		}
		if _, exists := f.sessions[commandArgs[4]]; !exists {
			return nil, errors.New("target missing")
		}
		delete(f.sessions, commandArgs[4])
		if len(f.sessions) == 0 {
			_ = os.Remove(socket)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected command: %v", call)
	}
}

type fakeProcess struct {
	once     sync.Once
	done     chan struct{}
	listener net.Listener
}

func (p *fakeProcess) Wait() error { <-p.done; return nil }
func (p *fakeProcess) Signal(os.Signal) error {
	p.once.Do(func() {
		if p.listener != nil {
			_ = p.listener.Close()
		}
		close(p.done)
	})
	return nil
}
func (p *fakeProcess) Kill() error { return p.Signal(os.Kill) }

type fakeStarter struct {
	mu      sync.Mutex
	calls   [][]string
	process *fakeProcess
}

func (s *fakeStarter) Start(binary string, args ...string) (Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]string{binary}, args...))
	var socket string
	for index := range args {
		if args[index] == "-i" && index+1 < len(args) {
			socket = args[index+1]
		}
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	s.process = &fakeProcess{done: make(chan struct{}), listener: listener}
	return s.process, nil
}

func testManager(t *testing.T, maximum int) (*Manager, *fakeRunner, *fakeStarter) {
	t.Helper()
	runtime, err := os.MkdirTemp("", "rt-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	runner := newFakeRunner()
	starter := &fakeStarter{}
	manager := newManager(Config{
		TmuxBinary: "tmux", TtydBinary: "ttyd", RuntimeDir: runtime,
		MaxSessions: maximum, StartTimeout: time.Second,
	}, runner, starter)
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownContext)
	})
	return manager, runner, starter
}

func TestValidateNameAndID(t *testing.T) {
	for _, name := range []string{"Main", "Робоча сесія", "Mill 01", "x_y-z"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("valid name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", " leading", "trailing ", "-dash", "slash/name", "line\nbreak", strings.Repeat("я", 49)} {
		if !errors.Is(ValidateName(name), ErrInvalidName) {
			t.Errorf("invalid name %q accepted", name)
		}
	}
	if !ValidID("0123456789abcdef0123456789abcdef") {
		t.Fatal("valid ID rejected")
	}
	for _, id := range []string{"short", "0123456789ABCDEF0123456789ABCDEF", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"} {
		if ValidID(id) {
			t.Errorf("invalid ID %q accepted", id)
		}
	}
}

func TestTtydArgumentsAreFixedAndShellFree(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	got := TtydArguments("/usr/bin/tmux", "/run/rt/tmux.sock", "/run/rt/ttyd/id.sock", id)
	want := []string{
		"-W", "-O", "-b", "/terminal/" + id,
		"-i", "/run/rt/ttyd/id.sock", "--", "/usr/bin/tmux", "-S", "/run/rt/tmux.sock",
		"attach-session", "-t", "rt_" + id,
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("TtydArguments()\n got: %#v\nwant: %#v", got, want)
	}
}

func TestCreateListConnectDeleteLifecycle(t *testing.T) {
	manager, runner, starter := testManager(t, 2)
	created, err := manager.Create(context.Background(), "Main terminal")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(created.ID) || created.Name != "Main terminal" {
		t.Fatalf("unexpected created session: %+v", created)
	}
	listed, err := manager.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
	connected, terminalURL, err := manager.Connect(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !connected.TerminalConnected || terminalURL != "/terminal/"+created.ID+"/" || manager.ActiveTerminals() != 1 {
		t.Fatalf("unexpected connect result: %+v, %q", connected, terminalURL)
	}
	if len(starter.calls) != 1 {
		t.Fatalf("ttyd starts = %d, want 1", len(starter.calls))
	}
	runner.mu.Lock()
	mouseEnabled := runner.sessions[tmuxTarget(created.ID)].mouse
	runner.mu.Unlock()
	if !mouseEnabled {
		t.Fatal("Connect() did not enable tmux mouse scrolling")
	}
	runner.mu.Lock()
	runner.sessions[tmuxTarget(created.ID)].mouse = false
	runner.mu.Unlock()
	if _, _, err := manager.Connect(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if len(starter.calls) != 1 {
		t.Fatalf("second connect started another ttyd: %d calls", len(starter.calls))
	}
	runner.mu.Lock()
	mouseEnabled = runner.sessions[tmuxTarget(created.ID)].mouse
	runner.mu.Unlock()
	if !mouseEnabled {
		t.Fatal("repeated Connect() did not restore tmux mouse scrolling before reusing ttyd")
	}
	if err := manager.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveTerminals() != 0 {
		t.Fatal("delete left ttyd running")
	}
	runner.mu.Lock()
	remaining := len(runner.sessions)
	runner.mu.Unlock()
	if remaining != 0 {
		t.Fatal("delete left tmux session running")
	}
}

func TestCreateCleansHiddenSessionWhenNewSessionReturnsErrorAfterSideEffect(t *testing.T) {
	manager, runner, _ := testManager(t, 2)
	runner.mu.Lock()
	runner.newSessionErrorAfterCreate = context.Canceled
	runner.mu.Unlock()

	if _, err := manager.Create(context.Background(), "Canceled create"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context.Canceled", err)
	}

	runner.mu.Lock()
	remaining := len(runner.sessions)
	calls := append([][]string(nil), runner.calls...)
	runner.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("Create() error left %d hidden tmux sessions", remaining)
	}

	createdTarget := ""
	killedTarget := ""
	for _, call := range calls {
		if len(call) == 9 && call[1] == "-f" && call[2] == "/dev/null" && call[5] == "new-session" {
			createdTarget = call[8]
		}
		if len(call) == 6 && call[3] == "kill-session" {
			killedTarget = call[5]
		}
	}
	if createdTarget == "" || killedTarget != createdTarget {
		t.Fatalf("new-session target %q, cleanup kill target %q; calls=%v", createdTarget, killedTarget, calls)
	}

	listed, err := manager.List(context.Background())
	if err != nil || len(listed) != 0 {
		t.Fatalf("List() after canceled create = %+v, %v", listed, err)
	}
}

func TestListStopsTtydWhenTmuxSessionEndsOutOfBand(t *testing.T) {
	manager, runner, starter := testManager(t, 2)
	created, err := manager.Create(context.Background(), "Ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Connect(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	delete(runner.sessions, tmuxTarget(created.ID))
	runner.mu.Unlock()

	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("List() returned ended tmux session: %+v", listed)
	}
	if manager.ActiveTerminals() != 0 {
		t.Fatal("out-of-band tmux exit left ttyd managed")
	}
	select {
	case <-starter.process.done:
	default:
		t.Fatal("out-of-band tmux exit did not terminate ttyd")
	}
	if _, err := os.Stat(manager.ttydSocket(created.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ttyd socket remains after reconciliation: %v", err)
	}
}

func TestConcurrentCreateEnforcesLimitAndNameUniqueness(t *testing.T) {
	manager, _, _ := testManager(t, 3)
	names := []string{"One", "Two", "Three", "Four", "Five", "One"}
	var wait sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for _, name := range names {
		name := name
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := manager.Create(context.Background(), name)
			if err == nil {
				mu.Lock()
				created++
				mu.Unlock()
				return
			}
			if !errors.Is(err, ErrLimitReached) && !errors.Is(err, ErrNameExists) {
				t.Errorf("unexpected create error: %v", err)
			}
		}()
	}
	wait.Wait()
	if created != 3 {
		t.Fatalf("created %d sessions, want exactly 3", created)
	}
	listed, err := manager.List(context.Background())
	if err != nil || len(listed) != 3 {
		t.Fatalf("List() = %d sessions, %v", len(listed), err)
	}
}

func TestProxyPreservesBasePathOverUnixSocket(t *testing.T) {
	manager, _, starter := testManager(t, 1)
	created, err := manager.Create(context.Background(), "Proxy")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Connect(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	// Replace the fake listener's inert accept loop with an HTTP server on the
	// same socket while retaining a live managed process marker.
	_ = starter.process.listener.Close()
	socket := manager.ttydSocket(created.ID)
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	starter.process.listener = listener
	type receivedRequest struct {
		path   string
		header http.Header
	}
	received := make(chan receivedRequest, 1)
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- receivedRequest{path: r.URL.Path, header: r.Header.Clone()}
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = upstream.Serve(listener) }()
	defer upstream.Close()

	proxy, err := manager.Proxy(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/terminal/"+created.ID+"/token", nil)
	request.Header.Set("Cookie", "__Host-remoteterminal_session=secret; preference=private")
	request.Header.Set("Authorization", "Bearer application-secret")
	request.Header.Set("Proxy-Authorization", "Basic proxy-secret")
	request.Header.Set("X-CSRF-Token", "csrf-secret")
	request.Header.Set("X-XSRF-Token", "xsrf-secret")
	request.Header.Set("X-Remote-Terminal-Session", "internal-secret")
	request.Header.Set("Origin", "https://machine.test:8443")
	request.Header.Set("X-Terminal-Preference", "preserved")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("proxy status = %d", response.Code)
	}
	select {
	case got := <-received:
		if got.path != "/terminal/"+created.ID+"/token" {
			t.Fatalf("upstream path = %q", got.path)
		}
		for _, name := range []string{"Cookie", "Authorization", "Proxy-Authorization", "X-CSRF-Token", "X-XSRF-Token", "X-Remote-Terminal-Session"} {
			if value := got.header.Get(name); value != "" {
				t.Errorf("sensitive header %s reached ttyd: %q", name, value)
			}
		}
		if got.header.Get("Origin") != "https://machine.test:8443" || got.header.Get("X-Terminal-Preference") != "preserved" {
			t.Fatalf("required/non-app headers were not preserved: %#v", got.header)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy did not reach Unix upstream")
	}
}

func TestProxySupportsWebSocketUpgradeOverUnixSocket(t *testing.T) {
	manager, _, starter := testManager(t, 1)
	created, err := manager.Create(context.Background(), "WebSocket")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Connect(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	_ = starter.process.listener.Close()
	socket := manager.ttydSocket(created.ID)
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	starter.process.listener = listener
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream response is not hijackable")
			return
		}
		connection, buffer, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		_, _ = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buffer.Flush()
	})}
	go func() { _ = upstream.Serve(listener) }()
	defer upstream.Close()

	proxy, err := manager.Proxy(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	outer := httptest.NewServer(proxy)
	defer outer.Close()
	address := strings.TrimPrefix(outer.URL, "http://")
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, _ = fmt.Fprintf(connection, "GET /terminal/%s/ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n", created.ID, address)
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d", response.StatusCode)
	}
}

func TestInitializeCleansStaleTtydFiles(t *testing.T) {
	runtime := t.TempDir()
	directory := filepath.Join(runtime, "ttyd")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(directory, "stale.sock")
	if err := os.WriteFile(stale, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := newManager(Config{TmuxBinary: "tmux", TtydBinary: "ttyd", RuntimeDir: runtime, MaxSessions: 1, StartTimeout: time.Second}, newFakeRunner(), &fakeStarter{})
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file still exists: %v", err)
	}
	for _, path := range []string{runtime, directory} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("%s mode = %o, want 0700", path, info.Mode().Perm())
		}
	}
}
