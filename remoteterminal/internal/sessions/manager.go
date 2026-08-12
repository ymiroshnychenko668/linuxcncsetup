// Package sessions owns the application's private tmux server and the ttyd
// processes that expose individual tmux sessions through Unix sockets.
package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidName        = errors.New("invalid session name")
	ErrInvalidID          = errors.New("invalid session id")
	ErrNotFound           = errors.New("session not found")
	ErrNameExists         = errors.New("session name already exists")
	ErrLimitReached       = errors.New("session limit reached")
	ErrTerminalNotRunning = errors.New("terminal is not connected")
	ErrShuttingDown       = errors.New("service is shutting down")
)

const (
	tmuxListFormat                = "#{session_name}\t#{@remoteterminal-id}\t#{@remoteterminal-name}\t#{session_attached}\t#{session_windows}\t#{session_created}"
	tmuxClipboardModeOption       = "set-clipboard"
	tmuxClipboardMode             = "external"
	tmuxClipboardOption           = "terminal-overrides"
	tmuxClipboardTerminalOverride = `xterm-256color:Ms=\E]52;c;%p2%s\007`
)

// Session is the public description returned by the API. ID is an opaque,
// random identifier; Name is user-selected display text and is never used in a
// filesystem path or tmux target.
type Session struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Attached          bool      `json:"attached"`
	Windows           int       `json:"windows"`
	CreatedAt         time.Time `json:"createdAt,omitempty"`
	TerminalConnected bool      `json:"terminalConnected"`
}

// Runner executes short-lived commands. Arguments are always provided as an
// array and never interpreted by a shell.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// Process is the subset of os.Process/exec.Cmd behavior needed for ttyd.
type Process interface {
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

type Starter interface {
	Start(string, ...string) (Process, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type execProcess struct{ cmd *exec.Cmd }

func (p *execProcess) Wait() error                   { return p.cmd.Wait() }
func (p *execProcess) Signal(signal os.Signal) error { return p.cmd.Process.Signal(signal) }
func (p *execProcess) Kill() error                   { return p.cmd.Process.Kill() }

type execStarter struct{}

func (execStarter) Start(name string, args ...string) (Process, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

type managedProcess struct {
	process   Process
	done      chan struct{}
	err       error
	proxy     http.Handler
	transport *http.Transport
}

// Config contains process paths and resource limits for Manager.
type Config struct {
	TmuxBinary   string
	TtydBinary   string
	RuntimeDir   string
	MaxSessions  int
	StartTimeout time.Duration
}

// Manager serializes create/connect/delete operations so count and process
// invariants remain correct even when requests arrive concurrently.
type Manager struct {
	mu           sync.Mutex
	config       Config
	runner       Runner
	starter      Starter
	random       io.Reader
	processes    map[string]*managedProcess
	shuttingDown bool
	socketReady  func(string) bool
}

func NewManager(config Config) *Manager {
	return newManager(config, execRunner{}, execStarter{})
}

func newManager(config Config, runner Runner, starter Starter) *Manager {
	return &Manager{
		config:    config,
		runner:    runner,
		starter:   starter,
		random:    rand.Reader,
		processes: make(map[string]*managedProcess),
		socketReady: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.Mode()&os.ModeSocket != 0
		},
	}
}

func (m *Manager) tmuxSocket() string { return filepath.Join(m.config.RuntimeDir, "tmux.sock") }
func (m *Manager) ttydDir() string    { return filepath.Join(m.config.RuntimeDir, "ttyd") }
func (m *Manager) ttydSocket(id string) string {
	return filepath.Join(m.ttydDir(), id+".sock")
}

// Initialize validates dependencies, creates private runtime directories,
// removes stale ttyd sockets and verifies discovery of existing tmux sessions.
func (m *Manager) Initialize(ctx context.Context) error {
	if m.config.MaxSessions < 1 {
		return errors.New("max sessions must be positive")
	}
	if m.config.StartTimeout <= 0 {
		return errors.New("terminal start timeout must be positive")
	}
	// Linux Unix-domain socket paths are normally limited to 107 bytes. Leave
	// room for the generated ID and suffix and fail early with a useful error.
	if len(filepath.Join(m.ttydDir(), strings.Repeat("a", 32)+".sock")) >= 104 {
		return errors.New("runtime directory path is too long for ttyd Unix sockets")
	}
	if _, err := m.runner.Run(ctx, m.config.TmuxBinary, "-V"); err != nil {
		return fmt.Errorf("tmux dependency check failed: %w", err)
	}
	if _, err := m.runner.Run(ctx, m.config.TtydBinary, "--version"); err != nil {
		return fmt.Errorf("ttyd dependency check failed: %w", err)
	}
	if err := os.MkdirAll(m.ttydDir(), 0700); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}
	if err := os.Chmod(m.config.RuntimeDir, 0700); err != nil {
		return fmt.Errorf("secure runtime directory: %w", err)
	}
	if err := os.Chmod(m.ttydDir(), 0700); err != nil {
		return fmt.Errorf("secure ttyd directory: %w", err)
	}
	entries, err := os.ReadDir(m.ttydDir())
	if err != nil {
		return fmt.Errorf("read ttyd runtime directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(m.ttydDir(), entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale ttyd state: %w", err)
		}
	}
	if _, err := m.List(ctx); err != nil {
		return fmt.Errorf("discover tmux sessions: %w", err)
	}
	return nil
}

func (m *Manager) List(ctx context.Context) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked(ctx)
}

func (m *Manager) listLocked(ctx context.Context) ([]Session, error) {
	if _, err := os.Stat(m.tmuxSocket()); errors.Is(err, os.ErrNotExist) {
		m.reconcileProcessesLocked(nil)
		return []Session{}, nil
	}
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "list-sessions", "-F", tmuxListFormat)
	if err != nil {
		// A stale socket or a tmux server with no sessions is equivalent to an
		// empty application server. Other errors remain actionable internally.
		message := strings.ToLower(string(output))
		if strings.Contains(message, "no server running") ||
			strings.Contains(message, "failed to connect") ||
			strings.Contains(message, "error connecting") ||
			strings.Contains(message, "no sessions") {
			_ = os.Remove(m.tmuxSocket())
			m.reconcileProcessesLocked(nil)
			return []Session{}, nil
		}
		return nil, fmt.Errorf("list tmux sessions: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	result := make([]Session, 0, len(lines))
	validIDs := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			continue
		}
		target, id, name := fields[0], fields[1], fields[2]
		if !ValidID(id) || target != tmuxTarget(id) || ValidateName(name) != nil {
			continue
		}
		windows, err := strconv.Atoi(fields[4])
		if err != nil || windows < 0 {
			continue
		}
		createdUnix, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil || createdUnix <= 0 {
			continue
		}
		process := m.processes[id]
		connected := process != nil && !processFinished(process)
		result = append(result, Session{
			ID:                id,
			Name:              name,
			Attached:          fields[3] != "0",
			Windows:           windows,
			CreatedAt:         time.Unix(createdUnix, 0).UTC(),
			TerminalConnected: connected,
		})
		validIDs[id] = struct{}{}
	}
	m.reconcileProcessesLocked(validIDs)
	return result, nil
}

// reconcileProcessesLocked stops ttyd children whose tmux sessions ended
// naturally or were removed outside this service. Wait closes its done channel
// before attempting the manager mutex, so stopProcessLocked cannot deadlock
// while this method holds that mutex.
func (m *Manager) reconcileProcessesLocked(validIDs map[string]struct{}) {
	for id := range m.processes {
		if _, exists := validIDs[id]; !exists {
			m.stopProcessLocked(id, time.Second)
		}
	}
}

func (m *Manager) Create(ctx context.Context, name string) (Session, error) {
	if err := ValidateName(name); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return Session{}, ErrShuttingDown
	}
	existing, err := m.listLocked(ctx)
	if err != nil {
		return Session{}, err
	}
	if len(existing) >= m.config.MaxSessions {
		return Session{}, ErrLimitReached
	}
	for _, session := range existing {
		if session.Name == name {
			return Session{}, ErrNameExists
		}
	}
	id, err := newID(m.random)
	if err != nil {
		return Session{}, fmt.Errorf("generate session id: %w", err)
	}
	target := tmuxTarget(id)
	// CommandContext can report cancellation after tmux has already accepted
	// and created the session. Prepare exact-target cleanup before invoking
	// new-session so every error path removes that otherwise untagged session.
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = m.runner.Run(cleanupCtx, m.config.TmuxBinary,
			"-S", m.tmuxSocket(), "kill-session", "-t", target)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-f", "/dev/null", "-S", m.tmuxSocket(), "new-session", "-d", "-s", target); err != nil {
		cleanup()
		return Session{}, commandError("create tmux session", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-t", target, "@remoteterminal-id", id); err != nil {
		cleanup()
		return Session{}, commandError("tag tmux session", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-t", target, "@remoteterminal-name", name); err != nil {
		cleanup()
		return Session{}, commandError("name tmux session", output, err)
	}
	return Session{ID: id, Name: name, Windows: 1, CreatedAt: time.Now().UTC()}, nil
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return ErrShuttingDown
	}
	if _, err := m.findLocked(ctx, id); err != nil {
		return err
	}
	m.stopProcessLocked(id, 2*time.Second)
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "kill-session", "-t", tmuxTarget(id))
	if err != nil {
		return commandError("delete tmux session", output, err)
	}
	return nil
}

// Connect starts ttyd exactly once for a session and waits for its private
// Unix socket to become ready.
func (m *Manager) Connect(ctx context.Context, id string) (Session, string, error) {
	if !ValidID(id) {
		return Session{}, "", ErrInvalidID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return Session{}, "", ErrShuttingDown
	}
	session, err := m.findLocked(ctx, id)
	if err != nil {
		return Session{}, "", err
	}
	// tmux normally emits OSC 52 with an empty clipboard selector, while ttyd's
	// xterm clipboard addon accepts the explicit "c" system-clipboard selector.
	// The private server always exposes ttyd as xterm-256color, so replace its
	// otherwise-empty terminal-overrides array before a client attaches. Setting
	// the complete option (rather than appending) keeps repeated connects
	// idempotent and cannot affect the user's ordinary tmux server. External
	// mode lets tmux copy out but prevents pane applications from setting the
	// browser clipboard through tmux.
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-s", tmuxClipboardModeOption, tmuxClipboardMode); err != nil {
		return Session{}, "", commandError("restrict tmux clipboard integration", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-s", tmuxClipboardOption, tmuxClipboardTerminalOverride); err != nil {
		return Session{}, "", commandError("enable browser clipboard integration", output, err)
	}
	// tmux owns the scrollback while attached through ttyd. Without mouse
	// handling, xterm.js sees tmux's alternate screen and converts wheel input
	// into Up/Down keys, which the shell interprets as command history. Apply
	// this on every connect so sessions created by an older release are fixed
	// before their terminal is exposed again.
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-t", tmuxTarget(id), "mouse", "on"); err != nil {
		return Session{}, "", commandError("enable tmux mouse scrolling", output, err)
	}
	if process := m.processes[id]; process != nil && !processFinished(process) && m.socketReady(m.ttydSocket(id)) {
		session.TerminalConnected = true
		return session, terminalBasePath(id) + "/", nil
	}
	if process := m.processes[id]; process != nil {
		m.stopProcessLocked(id, time.Second)
	}
	socket := m.ttydSocket(id)
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Session{}, "", fmt.Errorf("remove stale ttyd socket: %w", err)
	}
	basePath := terminalBasePath(id)
	args := TtydArguments(m.config.TmuxBinary, m.tmuxSocket(), socket, id)
	process, err := m.starter.Start(m.config.TtydBinary, args...)
	if err != nil {
		return Session{}, "", fmt.Errorf("start ttyd: %w", err)
	}
	managed := &managedProcess{process: process, done: make(chan struct{})}
	m.processes[id] = managed
	go m.waitProcess(id, managed, socket)

	deadline := time.NewTimer(m.config.StartTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if m.socketReady(socket) {
			session.TerminalConnected = true
			return session, basePath + "/", nil
		}
		select {
		case <-ctx.Done():
			m.stopProcessLocked(id, time.Second)
			return Session{}, "", ctx.Err()
		case <-managed.done:
			delete(m.processes, id)
			return Session{}, "", fmt.Errorf("ttyd exited before its socket became ready: %w", managed.err)
		case <-deadline.C:
			m.stopProcessLocked(id, time.Second)
			return Session{}, "", errors.New("timed out waiting for ttyd socket")
		case <-ticker.C:
		}
	}
}

// Proxy returns a reverse proxy bound to the already-running session socket.
// The caller must authenticate and validate WebSocket Origin before invoking it.
func (m *Manager) Proxy(ctx context.Context, id string) (http.Handler, error) {
	if !ValidID(id) {
		return nil, ErrInvalidID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.findLocked(ctx, id); err != nil {
		return nil, err
	}
	process := m.processes[id]
	if process == nil || processFinished(process) || !m.socketReady(m.ttydSocket(id)) {
		return nil, ErrTerminalNotRunning
	}
	if process.proxy == nil {
		process.proxy, process.transport = newUnixProxy(m.ttydSocket(id))
	}
	return process.proxy, nil
}

func (m *Manager) ActiveTerminals() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, process := range m.processes {
		if !processFinished(process) {
			count++
		}
	}
	return count
}

// Shutdown stops ttyd children but intentionally leaves tmux sessions intact.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shuttingDown = true
	type identifiedProcess struct {
		id      string
		process *managedProcess
	}
	processes := make([]identifiedProcess, 0, len(m.processes))
	for id, process := range m.processes {
		processes = append(processes, identifiedProcess{id: id, process: process})
		delete(m.processes, id)
		if process.transport != nil {
			process.transport.CloseIdleConnections()
		}
		_ = process.process.Signal(syscall.SIGTERM)
	}
	m.mu.Unlock()

	for _, item := range processes {
		select {
		case <-item.process.done:
			_ = os.Remove(m.ttydSocket(item.id))
		case <-ctx.Done():
			for _, remaining := range processes {
				_ = remaining.process.process.Kill()
				_ = os.Remove(m.ttydSocket(remaining.id))
			}
			return ctx.Err()
		}
	}
	return nil
}

func (m *Manager) findLocked(ctx context.Context, id string) (Session, error) {
	sessions, err := m.listLocked(ctx)
	if err != nil {
		return Session{}, err
	}
	for _, session := range sessions {
		if session.ID == id {
			return session, nil
		}
	}
	return Session{}, ErrNotFound
}

func (m *Manager) waitProcess(id string, process *managedProcess, socket string) {
	process.err = process.process.Wait()
	close(process.done)
	m.mu.Lock()
	owned := m.processes[id] == process
	if process.transport != nil {
		process.transport.CloseIdleConnections()
	}
	if owned {
		delete(m.processes, id)
	}
	m.mu.Unlock()
	if owned {
		_ = os.Remove(socket)
	}
}

func (m *Manager) stopProcessLocked(id string, timeout time.Duration) {
	process := m.processes[id]
	if process == nil {
		_ = os.Remove(m.ttydSocket(id))
		return
	}
	delete(m.processes, id)
	if process.transport != nil {
		process.transport.CloseIdleConnections()
	}
	_ = process.process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-process.done:
	case <-timer.C:
		_ = process.process.Kill()
	}
	_ = os.Remove(m.ttydSocket(id))
}

func processFinished(process *managedProcess) bool {
	select {
	case <-process.done:
		return true
	default:
		return false
	}
}

func terminalBasePath(id string) string { return "/terminal/" + id }
func tmuxTarget(id string) string       { return "rt_" + id }

// TtydArguments is exported so the security-sensitive fixed process argument
// contract can be asserted directly in tests.
func TtydArguments(tmuxBinary, tmuxSocket, ttydSocket, id string) []string {
	return []string{
		"-W",
		"-O",
		"-b", terminalBasePath(id),
		"-i", ttydSocket,
		"--",
		tmuxBinary, "-S", tmuxSocket, "attach-session", "-t", tmuxTarget(id),
	}
}

func ValidateName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 48 || !utf8.ValidString(name) {
		return ErrInvalidName
	}
	for index, value := range name {
		if unicode.IsControl(value) || value == '/' || value == '\\' {
			return ErrInvalidName
		}
		if index == 0 && !(unicode.IsLetter(value) || unicode.IsDigit(value)) {
			return ErrInvalidName
		}
	}
	return nil
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func newID(reader io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(reader, value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func commandError(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w (%s)", action, err, message)
}

func newUnixProxy(socket string) (http.Handler, *http.Transport) {
	target, _ := url.Parse("http://unix")
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(dialCtx, "unix", socket)
		},
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		stripApplicationCredentials(request.Header)
	}
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "terminal is unavailable", http.StatusBadGateway)
	}
	return proxy, transport
}

func stripApplicationCredentials(header http.Header) {
	for _, name := range []string{
		"Cookie",
		"Cookie2",
		"Authorization",
		"Proxy-Authorization",
		"X-CSRF-Token",
		"X-XSRF-Token",
		"X-Forwarded-Authorization",
		"X-Forwarded-Access-Token",
	} {
		header.Del(name)
	}
	for name := range header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Remote-Terminal-") {
			header.Del(name)
		}
	}
}
