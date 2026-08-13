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
	ErrNoSelection        = errors.New("no terminal text has been selected")
	ErrSelectionTooLarge  = errors.New("terminal selection is too large")
	ErrInvalidSelection   = errors.New("terminal selection is not valid UTF-8")
	ErrShuttingDown       = errors.New("service is shutting down")
)

const (
	minimumTmuxMajor              = 3
	minimumTmuxMinor              = 2
	maxSelectionBytes             = 1 << 20
	tmuxListFormat                = "#{session_name}\t#{@remoteterminal-id}\t#{@remoteterminal-name}\t#{session_attached}\t#{session_windows}\t#{session_created}"
	tmuxWindowListFormat          = "#{window_id}"
	tmuxSelectionListFormat       = "#{buffer_name}\t#{buffer_size}"
	tmuxSelectionBufferPrefix     = "rtclip-"
	tmuxSelectionBindingPrefix    = tmuxSelectionBufferPrefix + "#{session_name}-"
	tmuxClipboardModeOption       = "set-clipboard"
	tmuxClipboardMode             = "off"
	tmuxClipboardOption           = "terminal-overrides[99]"
	tmuxClipboardTerminalOverride = `xterm-256color:Tc`
	tmuxStatusOption              = "status"
	tmuxStatusMode                = "off"
	tmuxHistoryLimitOption        = "history-limit"
	tmuxHistoryLimit              = "50000"
	terminalForegroundColor       = "#d2d2d2"
	terminalBackgroundColor       = "#2b2b2b"
	tmuxWindowStyleOption         = "window-style"
	tmuxWindowActiveStyleOption   = "window-active-style"
	tmuxWindowStyle               = "fg=" + terminalForegroundColor + ",bg=" + terminalBackgroundColor
	tmuxColorEnvironment          = "COLORTERM"
	tmuxColorEnvironmentValue     = "truecolor"
	tmuxNoColorEnvironment        = "NO_COLOR"
	ttydRendererPreference        = "rendererType=canvas"
	ttydMacSelectionPreference    = "macOptionClickForcesSelection=true"
	ttydThemePreference           = `theme={"foreground":"` + terminalForegroundColor + `","background":"` + terminalBackgroundColor + `"}`
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
	tmuxVersion, err := m.runner.Run(ctx, m.config.TmuxBinary, "-V")
	if err != nil {
		return fmt.Errorf("tmux dependency check failed: %w", err)
	}
	if err := validateTmuxVersion(tmuxVersion); err != nil {
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
	if _, err := m.privateTmuxSocketAvailable(); err != nil {
		return fmt.Errorf("clean stale terminal selections: %w", err)
	}
	if _, err := m.List(ctx); err != nil {
		return fmt.Errorf("discover tmux sessions: %w", err)
	}
	if err := m.cleanupOwnedSelectionBuffers(ctx); err != nil {
		return fmt.Errorf("clean stale terminal selections: %w", err)
	}
	return nil
}

func (m *Manager) List(ctx context.Context) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked(ctx)
}

// TakeLatestSelection returns and consumes the newest paste buffer created by a
// mouse selection in the requested session. tmux paste buffers are
// server-global, so Connect gives drag selections a prefix derived from tmux's
// private, immutable session name. This method only considers that prefix,
// reads the chosen buffer by its exact name, and removes every matching buffer
// before returning the text. A successful selection can therefore be returned
// only once and a selection from another session is never read or removed.
func (m *Manager) TakeLatestSelection(ctx context.Context, id string) (string, error) {
	if !ValidID(id) {
		return "", ErrInvalidID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return "", ErrShuttingDown
	}
	if _, err := m.findLocked(ctx, id); err != nil {
		return "", err
	}

	bufferOutput, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "list-buffers", "-F", tmuxSelectionListFormat)
	if err != nil {
		return "", commandError("inspect terminal selections", bufferOutput, err)
	}
	bufferName, size, matchingBuffers, err := sessionSelectionBuffers(bufferOutput, id)
	if bufferName == "" {
		return "", ErrNoSelection
	}
	if err != nil {
		return "", m.consumeSelectionErrorLocked(ctx, matchingBuffers, err)
	}
	if size == 0 {
		return "", m.consumeSelectionErrorLocked(ctx, matchingBuffers, ErrNoSelection)
	}
	if size > maxSelectionBytes {
		return "", m.consumeSelectionErrorLocked(ctx, matchingBuffers, ErrSelectionTooLarge)
	}

	selection, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "show-buffer", "-b", bufferName)
	if err != nil {
		// show-buffer may return part or all of the user's selected text along
		// with an error. Never include that output in an error which the HTTP
		// layer records in the service log.
		return "", fmt.Errorf("read terminal selection: %w", err)
	}
	if len(selection) == 0 {
		return "", m.consumeSelectionErrorLocked(ctx, matchingBuffers, ErrNoSelection)
	}
	if len(selection) > maxSelectionBytes {
		return "", m.consumeSelectionErrorLocked(ctx, matchingBuffers, ErrSelectionTooLarge)
	}
	if !utf8.Valid(selection) {
		return "", m.consumeSelectionErrorLocked(ctx, matchingBuffers, ErrInvalidSelection)
	}
	if err := m.deleteSelectionBuffersLocked(ctx, matchingBuffers, false); err != nil {
		return "", err
	}
	return string(selection), nil
}

// DiscardSelections removes every pending mouse-selection buffer for a
// session without reading its contents. The browser uses this as a source
// barrier after it captures an xterm-forced selection: while the copy dialog
// blocks further terminal interaction, all older tmux selections are removed
// so they cannot reappear on the next Copy selection action.
func (m *Manager) DiscardSelections(ctx context.Context, id string) error {
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
	return m.deleteSessionSelectionBuffersLocked(ctx, id)
}

// sessionSelectionBuffers selects the first matching entry because tmux lists
// buffers newest first and also returns every matching name for one-shot
// cleanup. The binding's generated paste-buffer names end in a decimal
// counter, so every accepted name must stay inside that exact reserved shape.
// Processes running as the same service account already share the private tmux
// trust boundary and could intentionally create an indistinguishable name.
func sessionSelectionBuffers(output []byte, id string) (string, uint64, []string, error) {
	prefix := tmuxSelectionPrefix(id)
	var newestName string
	var newestSize uint64
	matching := make([]string, 0)
	seen := make(map[string]struct{})
	var sizeErr error
	for _, line := range strings.Split(string(output), "\n") {
		name, rawSize, ok := strings.Cut(line, "\t")
		if !ok || !validSelectionBufferName(name, prefix) {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		matching = append(matching, name)
		if newestName != "" {
			continue
		}
		size, err := strconv.ParseUint(rawSize, 10, 64)
		if err != nil {
			sizeErr = fmt.Errorf("inspect terminal selection: invalid tmux buffer size %q", rawSize)
			newestName = name
			continue
		}
		newestName = name
		newestSize = size
	}
	return newestName, newestSize, matching, sizeErr
}

func tmuxSelectionPrefix(id string) string {
	return tmuxSelectionBufferPrefix + tmuxTarget(id) + "-"
}

func validSelectionBufferName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return false
	}
	for _, character := range name[len(prefix):] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// validOwnedSelectionBufferName recognizes the exact namespace reserved for
// this service's mouse binding. A matching-looking buffer with an invalid
// session ID or a non-decimal counter is never removed at startup.
func validOwnedSelectionBufferName(name string) bool {
	const targetPrefix = tmuxSelectionBufferPrefix + "rt_"
	if !strings.HasPrefix(name, targetPrefix) {
		return false
	}
	id, counter, ok := strings.Cut(name[len(targetPrefix):], "-")
	if !ok || !ValidID(id) || counter == "" {
		return false
	}
	for _, character := range counter {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (m *Manager) cleanupOwnedSelectionBuffers(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	available, err := m.privateTmuxSocketAvailable()
	if err != nil {
		return err
	}
	if !available {
		return nil
	}
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "list-buffers", "-F", tmuxSelectionListFormat)
	if err != nil {
		return commandError("inspect stale terminal selections", output, err)
	}
	names := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		name, _, ok := strings.Cut(line, "\t")
		if !ok || !validOwnedSelectionBufferName(name) {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return m.deleteSelectionBuffersLocked(ctx, names, true)
}

func (m *Manager) privateTmuxSocketAvailable() (bool, error) {
	info, err := os.Lstat(m.tmuxSocket())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect private tmux socket: %w", err)
	}
	// Lstat deliberately rejects an existing link at each check. The private
	// runtime directory and tmux server share one service-account trust boundary;
	// this guard is defense in depth, not isolation from that same UID.
	if info.Mode()&os.ModeSocket == 0 {
		return false, errors.New("private tmux socket path is not a Unix socket")
	}
	return true, nil
}

func (m *Manager) deleteSelectionBuffersLocked(ctx context.Context, names []string, verifyPrivateSocket bool) error {
	// tmux lists buffers newest first. Delete the selected newest buffer last so
	// a failure while removing stale predecessors leaves it available to retry.
	for index := len(names) - 1; index >= 0; index-- {
		name := names[index]
		// Keep the final guard next to the destructive command even though both
		// callers build the list with stricter, context-specific validators.
		if !validOwnedSelectionBufferName(name) {
			return errors.New("refusing to remove an unowned tmux buffer")
		}
		if verifyPrivateSocket {
			available, err := m.privateTmuxSocketAvailable()
			if err != nil {
				return err
			}
			if !available {
				return errors.New("private tmux socket disappeared during selection cleanup")
			}
		}
		if _, err := m.runner.Run(ctx, m.config.TmuxBinary,
			"-S", m.tmuxSocket(), "delete-buffer", "-b", name); err != nil {
			// tmux command output is intentionally discarded: an implementation
			// or test double may include clipboard contents in failing output.
			return fmt.Errorf("remove terminal selection buffer %q: %w", name, err)
		}
	}
	return nil
}

func (m *Manager) consumeSelectionErrorLocked(ctx context.Context, names []string, selectionErr error) error {
	if err := m.deleteSelectionBuffersLocked(ctx, names, false); err != nil {
		return err
	}
	return selectionErr
}

func parseTmuxWindowTargets(output []byte) ([]string, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, errors.New("list tmux session windows: session has no windows")
	}
	lines := strings.Split(trimmed, "\n")
	targets := make([]string, 0, len(lines))
	for _, target := range lines {
		if len(target) < 2 || target[0] != '@' {
			return nil, fmt.Errorf("list tmux session windows: invalid window target %q", target)
		}
		for _, character := range target[1:] {
			if character < '0' || character > '9' {
				return nil, fmt.Errorf("list tmux session windows: invalid window target %q", target)
			}
		}
		targets = append(targets, target)
	}
	return targets, nil
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
	// Prepare the private server's environment and inherited session options in
	// the same tmux command queue which creates the first pane. Running these as
	// later commands would be too late: the initial shell would already have
	// inherited a service manager's NO_COLOR value and the default 2000-line
	// history limit. The literal separators are tmux argv, never shell syntax.
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-f", "/dev/null", "-S", m.tmuxSocket(),
		"start-server", ";",
		"set-environment", "-g", tmuxColorEnvironment, tmuxColorEnvironmentValue, ";",
		"set-environment", "-gu", tmuxNoColorEnvironment, ";",
		"set-option", "-g", tmuxStatusOption, tmuxStatusMode, ";",
		"set-option", "-g", tmuxHistoryLimitOption, tmuxHistoryLimit, ";",
		// Codex discovers the terminal's default colours with OSC 10/11 at
		// startup and only renders its subtle block backgrounds when tmux can
		// answer those queries. Explicit styles make tmux's tty defaults match
		// the ttyd theme before the first pane (and Codex) can start.
		"set-option", "-g", tmuxWindowStyleOption, tmuxWindowStyle, ";",
		"set-option", "-g", tmuxWindowActiveStyleOption, tmuxWindowStyle, ";",
		"new-session", "-d", "-s", target); err != nil {
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
	if err := m.deleteSessionSelectionBuffersLocked(ctx, id); err != nil {
		return err
	}
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "kill-session", "-t", tmuxTarget(id))
	if err != nil {
		return commandError("delete tmux session", output, err)
	}
	return nil
}

func (m *Manager) deleteSessionSelectionBuffersLocked(ctx context.Context, id string) error {
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "list-buffers", "-F", tmuxSelectionListFormat)
	if err != nil {
		return commandError("inspect session selection buffers", output, err)
	}
	_, _, names, _ := sessionSelectionBuffers(output, id)
	// Buffer names are sufficient for cleanup; malformed size metadata must not
	// prevent a session-boundary or explicit privacy cleanup.
	if err := m.deleteSelectionBuffersLocked(ctx, names, false); err != nil {
		return err
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
	// A browser mount/reload is a fresh clipboard-source boundary. Remove any
	// selection buffers left by an older view before this terminal can become
	// copy-enabled again. The frontend keeps Copy selection disabled until this
	// Connect call has completed and the ttyd iframe has loaded, so an aborted
	// xterm discard cannot resurface after reload, logout, or auth expiry.
	if err := m.deleteSessionSelectionBuffersLocked(ctx, id); err != nil {
		return Session{}, "", err
	}
	// Reapply the explicit default colours before even reusing an existing ttyd
	// process. Besides keeping recovered private servers consistent with the
	// current browser theme, this lets programs receive deterministic OSC 10/11
	// replies from tmux. The global window options cover future windows; apply
	// the same values explicitly to every existing window in the recovered
	// session so an old local value cannot mask the terminal colour contract.
	windowOutput, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "list-windows", "-t", tmuxTarget(id), "-F", tmuxWindowListFormat)
	if err != nil {
		return Session{}, "", commandError("list tmux session windows", windowOutput, err)
	}
	windowTargets, err := parseTmuxWindowTargets(windowOutput)
	if err != nil {
		return Session{}, "", err
	}
	for _, option := range []string{tmuxWindowStyleOption, tmuxWindowActiveStyleOption} {
		if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
			"-S", m.tmuxSocket(), "set-option", "-g", option, tmuxWindowStyle); err != nil {
			return Session{}, "", commandError("configure tmux default terminal colors", output, err)
		}
		for _, windowTarget := range windowTargets {
			if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
				"-S", m.tmuxSocket(), "set-window-option", "-t", windowTarget, option, tmuxWindowStyle); err != nil {
				return Session{}, "", commandError("configure tmux terminal colors", output, err)
			}
		}
	}
	// Keep clipboard transfer out of the terminal protocol: the authenticated,
	// one-shot HTTP endpoint is the only clipboard source. The private server
	// always exposes ttyd as xterm-256color. A stable indexed override makes
	// repeated connects idempotent without replacing tmux's built-in overrides;
	// Tc preserves true colour while deliberately omitting the Ms/OSC 52
	// capability.
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-s", tmuxClipboardModeOption, tmuxClipboardMode); err != nil {
		return Session{}, "", commandError("restrict tmux clipboard integration", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-s", tmuxClipboardOption, tmuxClipboardTerminalOverride); err != nil {
		return Session{}, "", commandError("restrict tmux terminal clipboard capability", output, err)
	}
	// Sanitize both scopes. The global environment covers future sessions and
	// the target environment covers future panes in sessions recovered from an
	// older private server. The systemd unit and Create command queue handle the
	// initial pane before Connect can run.
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-environment", "-g", tmuxColorEnvironment, tmuxColorEnvironmentValue); err != nil {
		return Session{}, "", commandError("configure tmux color environment", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-environment", "-gu", tmuxNoColorEnvironment); err != nil {
		return Session{}, "", commandError("remove tmux no-color environment", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-environment", "-t", tmuxTarget(id), tmuxColorEnvironment, tmuxColorEnvironmentValue); err != nil {
		return Session{}, "", commandError("configure session color environment", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-environment", "-u", "-t", tmuxTarget(id), tmuxNoColorEnvironment); err != nil {
		return Session{}, "", commandError("remove session no-color environment", output, err)
	}
	// tmux paste buffers are shared by every session in a server. Copy the drag
	// selection directly with a format-expanded prefix based on the private tmux
	// session name. copy-selection-and-cancel creates the paste buffer without a
	// shell command; with set-clipboard off it cannot emit OSC 52. The API can
	// then consume this session's selection without exposing another terminal's
	// most recent buffer. Configure both stock copy-mode key tables because the
	// mode-keys option may select either one.
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
			"-S", m.tmuxSocket(), "bind-key", "-T", table, "MouseDragEnd1Pane",
			"send-keys", "-X", "copy-selection-and-cancel", tmuxSelectionBindingPrefix); err != nil {
			return Session{}, "", commandError("scope tmux mouse selections", output, err)
		}
	}
	// Hide tmux's status row so the pane receives every row fitted by xterm.js.
	// Restore the large scrollback setting for sessions created by older
	// releases as well; tmux applies history-limit to windows created after the
	// option is set, while Create above covers the initial window.
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-t", tmuxTarget(id), tmuxStatusOption, tmuxStatusMode); err != nil {
		return Session{}, "", commandError("hide tmux status line", output, err)
	}
	if output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-t", tmuxTarget(id), tmuxHistoryLimitOption, tmuxHistoryLimit); err != nil {
		return Session{}, "", commandError("increase tmux history limit", output, err)
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
		"-t", ttydRendererPreference,
		"-t", ttydMacSelectionPreference,
		"-t", ttydThemePreference,
		"-b", terminalBasePath(id),
		"-i", ttydSocket,
		"--",
		tmuxBinary, "-S", tmuxSocket, "attach-session", "-t", tmuxTarget(id),
	}
}

// validateTmuxVersion enforces the first tmux release which answers OSC 10/11
// default-colour queries. Suffixes used by stable releases (for example 3.3a)
// do not affect this capability check.
func validateTmuxVersion(output []byte) error {
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[0] != "tmux" {
		return fmt.Errorf("could not parse tmux version from %q; tmux %d.%d or newer is required",
			strings.TrimSpace(string(output)), minimumTmuxMajor, minimumTmuxMinor)
	}
	version := strings.TrimPrefix(fields[1], "next-")
	dot := strings.IndexByte(version, '.')
	if dot < 1 || dot == len(version)-1 {
		return fmt.Errorf("could not parse tmux version from %q; tmux %d.%d or newer is required",
			strings.TrimSpace(string(output)), minimumTmuxMajor, minimumTmuxMinor)
	}
	minorEnd := dot + 1
	for minorEnd < len(version) && version[minorEnd] >= '0' && version[minorEnd] <= '9' {
		minorEnd++
	}
	if minorEnd == dot+1 {
		return fmt.Errorf("could not parse tmux version from %q; tmux %d.%d or newer is required",
			strings.TrimSpace(string(output)), minimumTmuxMajor, minimumTmuxMinor)
	}
	major, majorErr := strconv.Atoi(version[:dot])
	minor, minorErr := strconv.Atoi(version[dot+1 : minorEnd])
	if majorErr != nil || minorErr != nil {
		return fmt.Errorf("could not parse tmux version from %q; tmux %d.%d or newer is required",
			strings.TrimSpace(string(output)), minimumTmuxMajor, minimumTmuxMinor)
	}
	if major < minimumTmuxMajor || major == minimumTmuxMajor && minor < minimumTmuxMinor {
		return fmt.Errorf("tmux %d.%d or newer is required for terminal default-color queries (found %q)",
			minimumTmuxMajor, minimumTmuxMinor, strings.TrimSpace(string(output)))
	}
	return nil
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
