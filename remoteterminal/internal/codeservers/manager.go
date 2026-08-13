// Package codeservers manages isolated code-server workspaces in the
// application's private tmux server. Each workspace is identified by an opaque
// ID, listens on a private Unix socket, and keeps its editor profile in a
// directory derived from the canonical workspace path.
package codeservers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidPath           = errors.New("invalid directory path")
	ErrDirectoryNotFound     = errors.New("directory not found")
	ErrDirectoryInaccessible = errors.New("directory is not accessible")
	ErrInvalidID             = errors.New("invalid code-server id")
	ErrNotFound              = errors.New("code-server not found")
	ErrLimitReached          = errors.New("code-server limit reached")
	ErrShuttingDown          = errors.New("service is shutting down")
	ErrNotRunning            = errors.New("code-server is not running")
	ErrStartFailed           = errors.New("code-server failed to start")
)

const (
	maxDirectoryEntries     = 1000
	maxDirectoryScanEntries = 10000
	tmuxListFormat          = "#{session_name}\t#{@remoteterminal-code-server-id}\t#{@remoteterminal-code-server-folder}\t#{session_created}"
	tmuxIDOption            = "@remoteterminal-code-server-id"
	tmuxFolderOption        = "@remoteterminal-code-server-folder"
	shutdownGrace           = 6 * time.Second
	defaultPollInterval     = 100 * time.Millisecond
	defaultLivenessPoll     = 500 * time.Millisecond
	defaultProxyVerify      = 2 * time.Second
)

// Instance is the public description of one active code-server workspace.
type Instance struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	FolderPath string    `json:"folderPath"`
	CreatedAt  time.Time `json:"createdAt"`
	URL        string    `json:"url"`
}

// Directory is one selectable child in a directory listing. Path is always a
// canonical absolute path, including when Name refers to a symbolic link.
type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// DirectoryListing describes a canonical directory and its selectable child
// directories. ParentPath is nil at the filesystem root.
type DirectoryListing struct {
	Path        string      `json:"path"`
	ParentPath  *string     `json:"parentPath"`
	Directories []Directory `json:"directories"`
	Truncated   bool        `json:"truncated"`
}

// Config contains executable paths, storage roots, and lifecycle limits.
// RuntimeDir is typically /run/remoteterminal and StateDir is typically
// /var/lib/remoteterminal.
type Config struct {
	TmuxBinary       string
	CodeServerBinary string
	RuntimeDir       string
	StateDir         string
	HomeDir          string
	MaxServers       int
	StartTimeout     time.Duration
}

// Runner executes short-lived commands without a shell.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type proxyState struct {
	handler   http.Handler
	transport *http.Transport
}

type lifecyclePhase uint8

const (
	phaseLaunching lifecyclePhase = iota
	phaseActive
	phaseStopping
	phaseFailed
)

// managedInstance is an in-process reservation around the tmux source of
// truth. Reservations keep concurrent Create/Delete calls from racing without
// holding the manager mutex while code-server starts or stops.
type managedInstance struct {
	instance      Instance
	phase         lifecyclePhase
	transition    chan struct{}
	transitionErr error
	lastVerified  time.Time
}

// Manager serializes workspace lifecycle operations. Its source of truth is
// the dedicated tmux server, not an in-memory process table.
type Manager struct {
	mu                  sync.Mutex
	commandMu           sync.Mutex
	config              Config
	runner              Runner
	random              io.Reader
	healthProbe         func(context.Context, string) error
	socketReady         func(string) bool
	pollInterval        time.Duration
	livenessInterval    time.Duration
	proxyVerifyInterval time.Duration
	shutdownGrace       time.Duration
	instances           map[string]*managedInstance
	proxies             map[string]*proxyState
	shutdownComplete    bool
	shutdownInProgress  bool
	shutdownDone        chan struct{}
	shutdownErr         error
	shutdownRequested   uint32
	canonicalHome       string
}

func NewManager(config Config) *Manager {
	manager := &Manager{
		config:              config,
		runner:              execRunner{},
		random:              rand.Reader,
		pollInterval:        defaultPollInterval,
		livenessInterval:    defaultLivenessPoll,
		proxyVerifyInterval: defaultProxyVerify,
		shutdownGrace:       shutdownGrace,
		instances:           make(map[string]*managedInstance),
		proxies:             make(map[string]*proxyState),
		socketReady: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.Mode()&os.ModeSocket != 0
		},
	}
	manager.healthProbe = manager.probeHealth
	return manager
}

func (m *Manager) tmuxSocket() string {
	return filepath.Join(m.config.RuntimeDir, "code-server.tmux.sock")
}

func (m *Manager) runtimeRoot() string {
	return filepath.Join(m.config.RuntimeDir, "code-server")
}

func (m *Manager) instanceDir(id string) string {
	return filepath.Join(m.runtimeRoot(), id)
}

func (m *Manager) httpSocket(id string) string {
	return filepath.Join(m.instanceDir(id), "http.sock")
}

func (m *Manager) sessionSocket(id string) string {
	return filepath.Join(m.instanceDir(id), "ipc.sock")
}

func (m *Manager) configPath(id string) string {
	return filepath.Join(m.instanceDir(id), "config.yaml")
}

func (m *Manager) profilesRoot() string {
	return filepath.Join(m.config.StateDir, "code-server", "profiles")
}

func (m *Manager) profileDir(folder string) string {
	digest := sha256.Sum256([]byte(folder))
	return filepath.Join(m.profilesRoot(), hex.EncodeToString(digest[:]))
}

// Initialize validates dependencies and storage, then enforces the service
// restart boundary by terminating only the dedicated code-server tmux server
// and clearing its ephemeral runtime state. Durable per-folder profiles remain.
func (m *Manager) Initialize(ctx context.Context) error {
	if err := validateConfig(m.config); err != nil {
		return err
	}
	if _, err := m.runner.Run(ctx, m.config.TmuxBinary, "-V"); err != nil {
		return fmt.Errorf("tmux dependency check failed: %w", err)
	}
	// Even a version probe can otherwise create or parse the account's normal
	// ~/.config/code-server/config.yaml. Remote Terminal never reads or mutates
	// that user-managed configuration.
	if _, err := m.runner.Run(ctx, m.config.CodeServerBinary, "--config", "/dev/null", "--version"); err != nil {
		return fmt.Errorf("code-server dependency check failed: %w", err)
	}

	canonicalHome, err := canonicalDirectory(m.config.HomeDir)
	if err != nil {
		return fmt.Errorf("validate home directory: %w", err)
	}
	if err := ensureDirectoryAccessible(canonicalHome); err != nil {
		return fmt.Errorf("validate home directory: %w", err)
	}

	if err := prepareRuntimeDirectory(m.config.RuntimeDir); err != nil {
		return fmt.Errorf("prepare runtime directory: %w", err)
	}
	if err := secureDirectory(m.runtimeRoot()); err != nil {
		return fmt.Errorf("prepare code-server runtime directory: %w", err)
	}
	if err := secureDirectory(m.profilesRoot()); err != nil {
		return fmt.Errorf("prepare code-server profiles directory: %w", err)
	}
	// Linux sockaddr_un paths normally permit 107 non-NUL bytes. Keep a small
	// margin for platform variance and fail before starting an unusable process.
	if len(m.httpSocket(strings.Repeat("a", 32))) >= 104 ||
		len(m.sessionSocket(strings.Repeat("a", 32))) >= 104 {
		return errors.New("runtime directory path is too long for code-server Unix sockets")
	}

	m.commandMu.Lock()
	defer m.commandMu.Unlock()
	confirmed, err := m.killPrivateServer(ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("code-server tmux shutdown was not confirmed")
	}
	if err := clearDirectory(m.runtimeRoot()); err != nil {
		return fmt.Errorf("clear stale code-server runtime state: %w", err)
	}
	m.mu.Lock()
	m.closeAllProxiesLocked()
	m.instances = make(map[string]*managedInstance)
	m.shutdownComplete = false
	m.shutdownInProgress = false
	m.shutdownDone = nil
	m.shutdownErr = nil
	m.canonicalHome = canonicalHome
	atomic.StoreUint32(&m.shutdownRequested, 0)
	m.mu.Unlock()
	return nil
}

func validateConfig(config Config) error {
	if config.TmuxBinary == "" || config.CodeServerBinary == "" {
		return errors.New("tmux and code-server binaries are required")
	}
	if validatePathText(config.RuntimeDir) != nil || !filepath.IsAbs(config.RuntimeDir) {
		return errors.New("runtime directory must be absolute")
	}
	if validatePathText(config.StateDir) != nil || !filepath.IsAbs(config.StateDir) {
		return errors.New("state directory must be absolute")
	}
	if err := validateManagedRoot("runtime directory", config.RuntimeDir); err != nil {
		return err
	}
	if err := validateManagedRoot("state directory", config.StateDir); err != nil {
		return err
	}
	if pathsOverlap(config.RuntimeDir, config.StateDir) {
		return errors.New("runtime and state directories must not overlap")
	}
	if err := validatePathText(config.HomeDir); err != nil || !filepath.IsAbs(config.HomeDir) {
		return errors.New("home directory must be an absolute valid path")
	}
	if config.MaxServers < 1 || config.MaxServers > 8 {
		return errors.New("maximum code-server instances must be between 1 and 8")
	}
	if config.StartTimeout <= 0 {
		return errors.New("code-server start timeout must be positive")
	}
	return nil
}

func validateManagedRoot(name, path string) error {
	cleaned := filepath.Clean(path)
	for _, broad := range []string{
		string(filepath.Separator),
		"/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64",
		"/opt", "/proc", "/root", "/run", "/sbin", "/sys", "/tmp",
		"/usr", "/var",
	} {
		if cleaned == broad {
			return fmt.Errorf("%s must be an application-specific directory, not %s", name, broad)
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	leftToRight, leftErr := filepath.Rel(left, right)
	rightToLeft, rightErr := filepath.Rel(right, left)
	inside := func(relative string, err error) bool {
		return err == nil && (relative == "." || relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	}
	return inside(leftToRight, leftErr) || inside(rightToLeft, rightErr)
}

// Browse returns up to 1,000 readable child directories. Empty path selects
// the configured account home. Inaccessible and invalid children are omitted.
func (m *Manager) Browse(ctx context.Context, requestedPath string) (DirectoryListing, error) {
	if err := ctx.Err(); err != nil {
		return DirectoryListing{}, err
	}
	if requestedPath == "" {
		requestedPath = m.config.HomeDir
		if m.canonicalHome != "" {
			requestedPath = m.canonicalHome
		}
	}
	canonical, err := canonicalDirectory(requestedPath)
	if err != nil {
		return DirectoryListing{}, err
	}
	if err := ensureDirectoryAccessible(canonical); err != nil {
		return DirectoryListing{}, err
	}
	directory, err := os.Open(canonical)
	if err != nil {
		return DirectoryListing{}, mapDirectoryError(err)
	}
	defer directory.Close()
	listing := DirectoryListing{
		Path:        canonical,
		Directories: make([]Directory, 0, maxDirectoryEntries),
	}
	if parent := filepath.Dir(canonical); parent != canonical {
		listing.ParentPath = &parent
	}
	// Stream entries so browsing an unusually large directory cannot allocate
	// memory proportional to every entry before applying the response limit.
	scanned := 0
readEntries:
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			scanned++
			if scanned > maxDirectoryScanEntries {
				listing.Truncated = true
				break readEntries
			}
			if err := ctx.Err(); err != nil {
				return DirectoryListing{}, err
			}
			child, err := canonicalDirectory(filepath.Join(canonical, entry.Name()))
			if err != nil || ensureDirectoryAccessible(child) != nil {
				continue
			}
			if len(listing.Directories) == maxDirectoryEntries {
				listing.Truncated = true
				break readEntries
			}
			listing.Directories = append(listing.Directories, Directory{Name: entry.Name(), Path: child})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return DirectoryListing{}, mapDirectoryError(readErr)
		}
	}
	// File.ReadDir returns filesystem order; provide a stable user-facing list.
	sort.SliceStable(listing.Directories, func(i, j int) bool {
		return listing.Directories[i].Name < listing.Directories[j].Name
	})
	return listing, nil
}

// List derives the active workspace list from the dedicated tmux server and
// reconciles runtime/proxy state left by naturally exited sessions.
func (m *Manager) List(ctx context.Context) ([]Instance, error) {
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		return nil, ErrShuttingDown
	}
	m.commandMu.Lock()
	defer m.commandMu.Unlock()
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		return nil, ErrShuttingDown
	}
	discovered, err := m.discover(ctx)
	if err != nil {
		return nil, err
	}
	protected := m.reconcileDiscovered(discovered)
	m.cleanupStaleRuntime(protected)
	return m.activeInstances(), nil
}

func (m *Manager) discover(ctx context.Context) ([]Instance, error) {
	exists, err := m.inspectPrivateSocket()
	if err != nil {
		return nil, err
	}
	if !exists {
		return []Instance{}, nil
	}
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "list-sessions", "-F", tmuxListFormat)
	if err != nil {
		if tmuxServerMissingFromProbe(output) {
			return []Instance{}, nil
		}
		return nil, commandError("list code-server tmux sessions", output, err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	instances := make([]Instance, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			continue
		}
		target, id := fields[0], fields[1]
		if !ValidID(id) || target != tmuxTarget(id) {
			continue
		}
		folderBytes, err := base64.RawURLEncoding.DecodeString(fields[2])
		if err != nil {
			continue
		}
		folder := string(folderBytes)
		if validatePathText(folder) != nil || !filepath.IsAbs(folder) || filepath.Clean(folder) != folder {
			continue
		}
		createdUnix, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || createdUnix <= 0 {
			continue
		}
		instances = append(instances, newInstance(id, folder, time.Unix(createdUnix, 0).UTC()))
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].CreatedAt.Equal(instances[j].CreatedAt) {
			return instances[i].ID < instances[j].ID
		}
		return instances[i].CreatedAt.Before(instances[j].CreatedAt)
	})
	return instances, nil
}

// reconcileDiscovered updates the short-lived in-process reservations while
// preserving launching/stopping entries that intentionally may not match a
// tmux list at that instant. It returns IDs whose runtime must be retained.
func (m *Manager) reconcileDiscovered(discovered []Instance) map[string]struct{} {
	now := time.Now()
	valid := make(map[string]Instance, len(discovered))
	for _, instance := range discovered {
		valid[instance.ID] = instance
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, instance := range valid {
		record := m.instances[id]
		if record == nil {
			m.instances[id] = &managedInstance{
				instance: instance, phase: phaseActive, lastVerified: now,
			}
			continue
		}
		record.instance = instance
		if record.phase == phaseActive {
			record.lastVerified = now
		}
	}
	for id, record := range m.instances {
		if _, exists := valid[id]; exists || record.phase != phaseActive {
			continue
		}
		m.closeProxyLocked(id)
		delete(m.instances, id)
	}
	protected := make(map[string]struct{}, len(m.instances))
	for id := range m.instances {
		protected[id] = struct{}{}
	}
	return protected
}

func (m *Manager) activeInstances() []Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	instances := make([]Instance, 0, len(m.instances))
	for _, record := range m.instances {
		if record.phase == phaseActive {
			instances = append(instances, record.instance)
		}
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].CreatedAt.Equal(instances[j].CreatedAt) {
			return instances[i].ID < instances[j].ID
		}
		return instances[i].CreatedAt.Before(instances[j].CreatedAt)
	})
	return instances
}

func (m *Manager) cleanupStaleRuntime(protected map[string]struct{}) {
	entries, err := os.ReadDir(m.runtimeRoot())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		if _, keep := protected[entry.Name()]; !keep {
			_ = os.RemoveAll(filepath.Join(m.runtimeRoot(), entry.Name()))
		}
	}
}

// Create starts code-server for a canonical folder or returns the already
// active instance for that same folder. Reused is true only in the latter case.
func (m *Manager) Create(ctx context.Context, folderPath string) (instance Instance, reused bool, err error) {
	canonical, err := canonicalDirectory(folderPath)
	if err != nil {
		return Instance{}, false, err
	}
	// Revalidate access immediately before acquiring the lifecycle lock. The
	// directory is checked again by code-server when it opens the fixed argv.
	if err := ensureDirectoryAccessible(canonical); err != nil {
		return Instance{}, false, err
	}

	for {
		if atomic.LoadUint32(&m.shutdownRequested) != 0 {
			return Instance{}, false, ErrShuttingDown
		}

		record, waitFor, existing, reserveErr := m.reserveAndStart(ctx, canonical)
		if reserveErr != nil {
			return Instance{}, false, reserveErr
		}
		if existing != nil {
			return *existing, true, nil
		}
		if waitFor != nil {
			m.mu.Lock()
			transition := waitFor.transition
			m.mu.Unlock()
			if transition == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return Instance{}, false, ctx.Err()
			case <-transition:
			}
			m.mu.Lock()
			phase, transitionErr, waitedInstance := waitFor.phase, waitFor.transitionErr, waitFor.instance
			m.mu.Unlock()
			if phase == phaseActive && transitionErr == nil {
				return waitedInstance, true, nil
			}
			if transitionErr != nil {
				return Instance{}, false, transitionErr
			}
			continue
		}

		waitErr := m.waitUntilReady(ctx, record)
		if waitErr != nil {
			return Instance{}, false, m.failLaunch(record, waitErr, true)
		}
		m.mu.Lock()
		if atomic.LoadUint32(&m.shutdownRequested) != 0 {
			m.mu.Unlock()
			return Instance{}, false, m.failLaunch(record, ErrShuttingDown, true)
		}
		record.phase = phaseActive
		record.lastVerified = time.Now()
		record.transitionErr = nil
		close(record.transition)
		record.transition = nil
		instance = record.instance
		m.mu.Unlock()
		return instance, false, nil
	}
}

// reserveAndStart serializes only tmux discovery and the short launch command
// sequence. The potentially long health wait happens after commandMu is
// released, so List/Delete/Proxy remain responsive while code-server boots.
func (m *Manager) reserveAndStart(ctx context.Context, folder string) (
	record *managedInstance, waitFor *managedInstance, existing *Instance, err error,
) {
	m.commandMu.Lock()
	defer m.commandMu.Unlock()
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		return nil, nil, nil, ErrShuttingDown
	}
	discovered, err := m.discover(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	protected := m.reconcileDiscovered(discovered)
	m.cleanupStaleRuntime(protected)

	m.mu.Lock()
	for _, current := range m.instances {
		if current.instance.FolderPath != folder {
			continue
		}
		switch current.phase {
		case phaseActive:
			value := current.instance
			m.mu.Unlock()
			return nil, nil, &value, nil
		case phaseLaunching, phaseStopping:
			m.mu.Unlock()
			return nil, current, nil, nil
		case phaseFailed:
			err := current.transitionErr
			m.mu.Unlock()
			if err == nil {
				err = ErrStartFailed
			}
			return nil, nil, nil, err
		}
	}
	if len(m.instances) >= m.config.MaxServers {
		m.mu.Unlock()
		return nil, nil, nil, ErrLimitReached
	}
	m.mu.Unlock()

	var id string
	for attempts := 0; attempts < 16; attempts++ {
		id, err = newID(m.random)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("generate code-server id: %w", err)
		}
		m.mu.Lock()
		_, reserved := m.instances[id]
		m.mu.Unlock()
		if reserved {
			continue
		}
		exists, existsErr := m.sessionExists(ctx, tmuxTarget(id))
		if existsErr != nil {
			return nil, nil, nil, existsErr
		}
		if !exists {
			break
		}
		id = ""
	}
	if id == "" {
		return nil, nil, nil, errors.New("could not allocate a unique code-server id")
	}
	if err := m.prepareStorageLocked(id, folder); err != nil {
		return nil, nil, nil, err
	}
	record = &managedInstance{
		instance:   newInstance(id, folder, time.Now().UTC()),
		phase:      phaseLaunching,
		transition: make(chan struct{}),
	}
	m.mu.Lock()
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		m.mu.Unlock()
		_ = os.RemoveAll(m.instanceDir(id))
		return nil, nil, nil, ErrShuttingDown
	}
	m.instances[id] = record
	m.mu.Unlock()

	target := tmuxTarget(id)
	args := CodeServerArguments(m.config.CodeServerBinary, m.configPath(id),
		m.httpSocket(id), m.sessionSocket(id), m.profileDir(folder), folder)
	tmuxArgs := []string{"-f", "/dev/null", "-S", m.tmuxSocket(),
		"new-session", "-d", "-s", target}
	tmuxArgs = append(tmuxArgs, args...)
	output, runErr := m.runner.Run(ctx, m.config.TmuxBinary, tmuxArgs...)
	if runErr != nil {
		cause := fmt.Errorf("%w: %v", ErrStartFailed,
			commandError("start code-server tmux session", output, runErr))
		ownsSession := false
		if !tmuxDuplicateSession(output, runErr) {
			// A tmux client can fail after the server accepted new-session.
			// The exact target was proven absent immediately before this call;
			// verify it now, while commandMu excludes competing manager starts.
			// A duplicate-session error is never treated as ownership.
			verifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			ownsSession, _ = m.sessionExists(verifyCtx, target)
			cancel()
		}
		return nil, nil, nil, m.failLaunchWhileCommandLocked(record, cause, ownsSession)
	}
	if output, runErr = m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-t", target, tmuxIDOption, id); runErr != nil {
		cause := fmt.Errorf("%w: %v", ErrStartFailed,
			commandError("tag code-server tmux session", output, runErr))
		return nil, nil, nil, m.failLaunchWhileCommandLocked(record, cause, true)
	}
	encodedFolder := base64.RawURLEncoding.EncodeToString([]byte(folder))
	if output, runErr = m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "set-option", "-t", target, tmuxFolderOption, encodedFolder); runErr != nil {
		cause := fmt.Errorf("%w: %v", ErrStartFailed,
			commandError("tag code-server folder", output, runErr))
		return nil, nil, nil, m.failLaunchWhileCommandLocked(record, cause, true)
	}
	discovered, err = m.discover(ctx)
	if err != nil {
		return nil, nil, nil, m.failLaunchWhileCommandLocked(record, err, true)
	}
	var found bool
	for _, instance := range discovered {
		if instance.ID == id {
			m.mu.Lock()
			record.instance = instance
			m.mu.Unlock()
			found = true
			break
		}
	}
	if !found {
		cause := fmt.Errorf("%w: tmux session metadata was not visible after launch", ErrStartFailed)
		return nil, nil, nil, m.failLaunchWhileCommandLocked(record, cause, true)
	}
	m.reconcileDiscovered(discovered)
	return record, nil, nil, nil
}

func (m *Manager) waitUntilReady(ctx context.Context, record *managedInstance) error {
	m.mu.Lock()
	id := record.instance.ID
	m.mu.Unlock()
	deadlineAt := time.Now().Add(m.config.StartTimeout)
	healthTicker := time.NewTicker(m.pollInterval)
	defer healthTicker.Stop()
	livenessTicker := time.NewTicker(m.livenessInterval)
	defer livenessTicker.Stop()
	timeout := time.NewTimer(m.config.StartTimeout)
	defer timeout.Stop()

	probe := true
	for {
		if atomic.LoadUint32(&m.shutdownRequested) != 0 {
			return ErrShuttingDown
		}
		if probe {
			probe = false
			if err := m.healthProbe(ctx, m.httpSocket(id)); err == nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if atomic.LoadUint32(&m.shutdownRequested) != 0 {
					return ErrShuttingDown
				}
				exists, existsErr := m.sessionExists(ctx, tmuxTarget(id))
				if existsErr != nil {
					return existsErr
				}
				if !exists {
					return fmt.Errorf("%w: process exited immediately after becoming ready", ErrStartFailed)
				}
				if atomic.LoadUint32(&m.shutdownRequested) != 0 {
					return ErrShuttingDown
				}
				if time.Now().After(deadlineAt) {
					return fmt.Errorf("%w: timed out waiting for health endpoint", ErrStartFailed)
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("%w: timed out waiting for health endpoint", ErrStartFailed)
		case <-healthTicker.C:
			probe = true
		case <-livenessTicker.C:
			exists, existsErr := m.sessionExists(ctx, tmuxTarget(id))
			if existsErr != nil {
				return existsErr
			}
			if !exists {
				return fmt.Errorf("%w: process exited before becoming ready", ErrStartFailed)
			}
		}
	}
}

func (m *Manager) failLaunch(record *managedInstance, cause error, ownsSession bool) error {
	m.commandMu.Lock()
	defer m.commandMu.Unlock()
	return m.failLaunchWhileCommandLocked(record, cause, ownsSession)
}

func (m *Manager) failLaunchWhileCommandLocked(record *managedInstance, cause error, ownsSession bool) error {
	m.mu.Lock()
	id := record.instance.ID
	m.mu.Unlock()
	confirmed := !ownsSession
	var cleanupErr error
	if ownsSession {
		confirmed, cleanupErr = m.stopSession(context.Background(), id, false)
	}
	if confirmed {
		m.mu.Lock()
		m.closeProxyLocked(id)
		delete(m.instances, id)
		record.transitionErr = cause
		if record.transition != nil {
			close(record.transition)
			record.transition = nil
		}
		m.mu.Unlock()
		if removeErr := os.RemoveAll(m.instanceDir(id)); removeErr != nil && cleanupErr == nil {
			cleanupErr = fmt.Errorf("remove code-server runtime state: %w", removeErr)
		}
	} else {
		m.mu.Lock()
		record.phase = phaseFailed
		record.transitionErr = cause
		if record.transition != nil {
			close(record.transition)
			record.transition = nil
		}
		m.mu.Unlock()
	}
	if cleanupErr != nil {
		return fmt.Errorf("%w; exact-session cleanup was not confirmed: %v", cause, cleanupErr)
	}
	return cause
}

func (m *Manager) prepareStorageLocked(id, folder string) error {
	instanceDir := m.instanceDir(id)
	if err := os.Mkdir(instanceDir, 0700); err != nil {
		return fmt.Errorf("create code-server runtime directory: %w", err)
	}
	profileDir := m.profileDir(folder)
	for _, path := range []string{profileDir, filepath.Join(profileDir, "user-data"), filepath.Join(profileDir, "extensions")} {
		if err := secureDirectory(path); err != nil {
			_ = os.RemoveAll(instanceDir)
			return fmt.Errorf("prepare code-server profile: %w", err)
		}
	}
	configFile, err := os.OpenFile(m.configPath(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = os.RemoveAll(instanceDir)
		return fmt.Errorf("create code-server configuration: %w", err)
	}
	_, writeErr := io.WriteString(configFile, "auth: none\ncert: false\n")
	closeErr := configFile.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.RemoveAll(instanceDir)
		if writeErr != nil {
			return fmt.Errorf("write code-server configuration: %w", writeErr)
		}
		return fmt.Errorf("close code-server configuration: %w", closeErr)
	}
	return nil
}

// Delete gracefully interrupts the exact code-server tmux pane, then uses an
// exact kill-session fallback after six seconds and removes ephemeral state.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		return ErrShuttingDown
	}

	for {
		m.commandMu.Lock()
		m.mu.Lock()
		record := m.instances[id]
		m.mu.Unlock()
		if record == nil {
			discovered, discoverErr := m.discover(ctx)
			if discoverErr != nil {
				m.commandMu.Unlock()
				confirmed, stopErr := m.stopSession(ctx, id, false)
				if confirmed {
					if removeErr := os.RemoveAll(m.instanceDir(id)); removeErr != nil {
						return fmt.Errorf("remove code-server runtime state: %w", removeErr)
					}
					return contextResult(ctx)
				}
				if stopErr != nil {
					return stopErr
				}
				return discoverErr
			}
			m.reconcileDiscovered(discovered)
			m.mu.Lock()
			record = m.instances[id]
			m.mu.Unlock()
		}
		if record == nil {
			m.commandMu.Unlock()
			return ErrNotFound
		}

		m.mu.Lock()
		switch record.phase {
		case phaseLaunching, phaseStopping:
			transition := record.transition
			m.mu.Unlock()
			m.commandMu.Unlock()
			if transition == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-transition:
			}
			m.mu.Lock()
			transitionErr := record.transitionErr
			phase := record.phase
			m.mu.Unlock()
			if phase == phaseActive && transitionErr == nil {
				continue
			}
			if transitionErr != nil {
				return transitionErr
			}
			return nil
		case phaseActive, phaseFailed:
			record.phase = phaseStopping
			record.transition = make(chan struct{})
			record.transitionErr = nil
			m.closeProxyLocked(id)
		}
		m.mu.Unlock()
		m.commandMu.Unlock()

		confirmed, stopErr := m.stopSession(ctx, id, true)
		m.mu.Lock()
		if confirmed {
			m.closeProxyLocked(id)
			delete(m.instances, id)
		} else {
			record.phase = phaseActive
			record.lastVerified = time.Time{}
		}
		record.transitionErr = stopErr
		if record.transition != nil {
			close(record.transition)
			record.transition = nil
		}
		m.mu.Unlock()
		if confirmed {
			if removeErr := os.RemoveAll(m.instanceDir(id)); removeErr != nil && stopErr == nil {
				stopErr = fmt.Errorf("remove code-server runtime state: %w", removeErr)
			}
		}
		return stopErr
	}
}

// stopSession returns true only after the exact target is confirmed absent.
// Every unconfirmed graceful path reaches a fresh-context exact kill fallback.
func (m *Manager) stopSession(ctx context.Context, id string, graceful bool) (bool, error) {
	target := tmuxTarget(id)
	if graceful {
		output, interruptErr := m.runner.Run(ctx, m.config.TmuxBinary,
			"-S", m.tmuxSocket(), "send-keys", "-t", target+":0.0", "C-c")
		if interruptErr != nil && tmuxTargetMissing(output) {
			return true, contextResult(ctx)
		}
		if interruptErr != nil {
			graceful = false
		}
		if interruptErr == nil {
			exists, existsErr := m.sessionExists(ctx, target)
			if existsErr == nil && !exists {
				return true, contextResult(ctx)
			}
		}

		if graceful {
			graceTimer := time.NewTimer(m.shutdownGrace)
			livenessTicker := time.NewTicker(m.livenessInterval)
			waiting := true
			for waiting {
				select {
				case <-ctx.Done():
					waiting = false
				case <-graceTimer.C:
					waiting = false
				case <-livenessTicker.C:
					exists, existsErr := m.sessionExists(ctx, target)
					if existsErr == nil && !exists {
						graceTimer.Stop()
						livenessTicker.Stop()
						return true, contextResult(ctx)
					}
					if existsErr != nil {
						waiting = false
					}
				}
			}
			graceTimer.Stop()
			livenessTicker.Stop()
		}
	}

	killCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, killErr := m.runner.Run(killCtx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "kill-session", "-t", target)
	if killErr != nil && tmuxTargetMissing(output) {
		return true, contextResult(ctx)
	}
	exists, existsErr := m.sessionExists(killCtx, target)
	if existsErr == nil && !exists {
		return true, contextResult(ctx)
	}
	if killErr != nil {
		return false, commandError("kill code-server tmux session", output, killErr)
	}
	if existsErr != nil {
		return false, fmt.Errorf("confirm code-server tmux session stopped: %w", existsErr)
	}
	return false, errors.New("code-server tmux session survived exact kill-session")
}

// Proxy returns a cached Unix-socket reverse proxy for an active instance. The
// handler owns application-prefix stripping and upstream response rewriting;
// the HTTP layer remains responsible for authentication and Origin checks.
func (m *Manager) Proxy(ctx context.Context, id string) (http.Handler, error) {
	if !ValidID(id) {
		return nil, ErrInvalidID
	}
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		return nil, ErrShuttingDown
	}
	if err := m.ensureKnownInstance(ctx, id); err != nil {
		return nil, err
	}
	if !m.socketReady(m.httpSocket(id)) {
		m.evictIfDead(ctx, id)
		return nil, ErrNotRunning
	}

	// Cache exact tmux liveness across the asset burst made by an editor page.
	// The socket is still checked above on every call, and the bounded cache
	// prevents a stale Unix socket from keeping a dead session usable forever.
	m.mu.Lock()
	record := m.instances[id]
	if record == nil {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	if record.phase != phaseActive {
		m.mu.Unlock()
		return nil, ErrNotRunning
	}
	if time.Since(record.lastVerified) < m.proxyVerifyInterval {
		handler, err := m.proxyHandlerLocked(id)
		m.mu.Unlock()
		return handler, err
	}
	m.mu.Unlock()

	m.commandMu.Lock()
	m.mu.Lock()
	record = m.instances[id]
	if record == nil {
		m.mu.Unlock()
		m.commandMu.Unlock()
		return nil, ErrNotFound
	}
	if record.phase != phaseActive {
		m.mu.Unlock()
		m.commandMu.Unlock()
		return nil, ErrNotRunning
	}
	verify := time.Since(record.lastVerified) >= m.proxyVerifyInterval
	m.mu.Unlock()
	if verify {
		exists, err := m.sessionExists(ctx, tmuxTarget(id))
		if err != nil {
			m.commandMu.Unlock()
			return nil, err
		}
		if !exists {
			m.mu.Lock()
			if current := m.instances[id]; current == record && current.phase == phaseActive {
				m.closeProxyLocked(id)
				delete(m.instances, id)
			}
			m.mu.Unlock()
			m.commandMu.Unlock()
			_ = os.RemoveAll(m.instanceDir(id))
			return nil, ErrNotFound
		}
		m.mu.Lock()
		if current := m.instances[id]; current == record && current.phase == phaseActive {
			current.lastVerified = time.Now()
		}
		m.mu.Unlock()
	}
	m.commandMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		return nil, ErrShuttingDown
	}
	record = m.instances[id]
	if record == nil {
		return nil, ErrNotFound
	}
	if record.phase != phaseActive {
		return nil, ErrNotRunning
	}
	return m.proxyHandlerLocked(id)
}

func (m *Manager) proxyHandlerLocked(id string) (http.Handler, error) {
	if atomic.LoadUint32(&m.shutdownRequested) != 0 {
		return nil, ErrShuttingDown
	}
	if state := m.proxies[id]; state != nil {
		return state.handler, nil
	}
	handler, transport := newCodeServerProxy(m.httpSocket(id), id)
	m.proxies[id] = &proxyState{handler: handler, transport: transport}
	return handler, nil
}

func (m *Manager) ensureKnownInstance(ctx context.Context, id string) error {
	m.mu.Lock()
	record := m.instances[id]
	m.mu.Unlock()
	if record != nil {
		return nil
	}
	m.commandMu.Lock()
	defer m.commandMu.Unlock()
	m.mu.Lock()
	record = m.instances[id]
	m.mu.Unlock()
	if record == nil {
		discovered, err := m.discover(ctx)
		if err != nil {
			return err
		}
		protected := m.reconcileDiscovered(discovered)
		m.cleanupStaleRuntime(protected)
		m.mu.Lock()
		record = m.instances[id]
		m.mu.Unlock()
	}
	if record == nil {
		return ErrNotFound
	}
	return nil
}

func (m *Manager) evictIfDead(ctx context.Context, id string) {
	m.commandMu.Lock()
	defer m.commandMu.Unlock()
	exists, err := m.sessionExists(ctx, tmuxTarget(id))
	if err != nil || exists {
		return
	}
	m.mu.Lock()
	if record := m.instances[id]; record != nil && record.phase == phaseActive {
		m.closeProxyLocked(id)
		delete(m.instances, id)
	}
	m.mu.Unlock()
	_ = os.RemoveAll(m.instanceDir(id))
}

// Shutdown gracefully signals all active editors first, then terminates any
// survivors and the dedicated tmux server. It never affects another tmux
// socket. Calling Shutdown more than once is safe.
func (m *Manager) Shutdown(ctx context.Context) error {
	atomic.StoreUint32(&m.shutdownRequested, 1)
	for {
		m.mu.Lock()
		if m.shutdownComplete {
			m.mu.Unlock()
			return nil
		}
		if m.shutdownInProgress {
			done := m.shutdownDone
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
			}
			m.mu.Lock()
			err := m.shutdownErr
			m.mu.Unlock()
			return err
		}
		m.shutdownInProgress = true
		m.shutdownDone = make(chan struct{})
		done := m.shutdownDone
		m.mu.Unlock()

		complete, err := m.shutdownAttempt(ctx)
		m.mu.Lock()
		m.shutdownInProgress = false
		m.shutdownComplete = complete
		m.shutdownErr = err
		close(done)
		m.mu.Unlock()
		return err
	}
}

func (m *Manager) shutdownAttempt(ctx context.Context) (bool, error) {
	m.commandMu.Lock()
	instances, listErr := m.discover(ctx)
	m.commandMu.Unlock()
	firstErr := listErr
	if listErr == nil {
		for _, instance := range instances {
			output, err := m.runner.Run(ctx, m.config.TmuxBinary,
				"-S", m.tmuxSocket(), "send-keys", "-t", tmuxTarget(instance.ID)+":0.0", "C-c")
			if err != nil && !tmuxTargetMissing(output) && firstErr == nil {
				firstErr = commandError("interrupt code-server", output, err)
			}
		}

		if len(instances) > 0 {
			deadline := time.NewTimer(m.shutdownGrace)
			ticker := time.NewTicker(m.livenessInterval)
			waiting := true
			for waiting {
				select {
				case <-ctx.Done():
					if firstErr == nil {
						firstErr = ctx.Err()
					}
					waiting = false
				case <-deadline.C:
					waiting = false
				case <-ticker.C:
					m.commandMu.Lock()
					remaining, err := m.discover(ctx)
					m.commandMu.Unlock()
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						waiting = false
					} else if len(remaining) == 0 {
						waiting = false
					}
				}
			}
			deadline.Stop()
			ticker.Stop()
		}
	}

	killCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	m.commandMu.Lock()
	confirmed, killErr := m.killPrivateServer(killCtx)
	cancel()
	if killErr != nil && firstErr == nil {
		firstErr = killErr
	}
	if !confirmed {
		m.commandMu.Unlock()
		if firstErr == nil {
			firstErr = errors.New("code-server tmux shutdown was not confirmed")
		}
		return false, firstErr
	}

	m.mu.Lock()
	m.closeAllProxiesLocked()
	m.instances = make(map[string]*managedInstance)
	m.mu.Unlock()
	clearErr := clearDirectory(m.runtimeRoot())
	if clearErr != nil && firstErr == nil {
		firstErr = fmt.Errorf("clear code-server runtime state: %w", clearErr)
	}
	m.commandMu.Unlock()
	complete := killErr == nil && clearErr == nil
	return complete, firstErr
}

func (m *Manager) sessionExists(ctx context.Context, target string) (bool, error) {
	present, err := m.inspectPrivateSocket()
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "has-session", "-t", target)
	if err == nil {
		return true, nil
	}
	message := strings.ToLower(string(output))
	if tmuxServerMissingFromProbe(output) || strings.Contains(message, "can't find session") ||
		strings.Contains(message, "session not found") {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return false, commandError("inspect code-server tmux session", output, err)
}

func (m *Manager) killPrivateServer(ctx context.Context) (bool, error) {
	exists, err := m.inspectPrivateSocket()
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	output, err := m.runner.Run(ctx, m.config.TmuxBinary,
		"-S", m.tmuxSocket(), "kill-server")
	if err != nil && tmuxServerMissing(output) {
		if removeErr := os.Remove(m.tmuxSocket()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return true, fmt.Errorf("remove stale code-server tmux socket: %w", removeErr)
		}
		return true, nil
	}
	if err != nil {
		return false, commandError("stop code-server tmux server", output, err)
	}

	// A successful tmux client exit is not by itself proof that the server and
	// its socket are gone. Confirm through the exact private socket before any
	// unlink or runtime cleanup.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		probeOutput, probeErr := m.runner.Run(ctx, m.config.TmuxBinary,
			"-S", m.tmuxSocket(), "list-sessions", "-F", tmuxListFormat)
		if probeErr != nil && tmuxServerMissingFromProbe(probeOutput) {
			if removeErr := os.Remove(m.tmuxSocket()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return true, fmt.Errorf("remove stale code-server tmux socket: %w", removeErr)
			}
			return true, nil
		}
		if probeErr != nil {
			return false, commandError("confirm code-server tmux server stopped", probeOutput, probeErr)
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("confirm code-server tmux server stopped: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// inspectPrivateSocket refuses symlinks, non-sockets, foreign-owned sockets,
// and hard-linked sockets before tmux is allowed to interpret the path. The
// containing runtime directory is mode 0700, so after initialization only the
// service account can replace this entry.
func (m *Manager) inspectPrivateSocket() (bool, error) {
	info, err := os.Lstat(m.tmuxSocket())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect code-server tmux socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return false, errors.New("code-server tmux socket path is not a real Unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("cannot verify code-server tmux socket ownership")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return false, errors.New("code-server tmux socket is not owned by the service user")
	}
	if stat.Nlink != 1 {
		return false, errors.New("code-server tmux socket has an unsafe link count")
	}
	return true, nil
}

func (m *Manager) closeProxyLocked(id string) {
	state := m.proxies[id]
	if state == nil {
		return
	}
	state.transport.CloseIdleConnections()
	delete(m.proxies, id)
}

func (m *Manager) closeAllProxiesLocked() {
	for id := range m.proxies {
		m.closeProxyLocked(id)
	}
}

func (m *Manager) probeHealth(ctx context.Context, socket string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	transport := &http.Transport{
		Proxy:                 nil,
		ResponseHeaderTimeout: 500 * time.Millisecond,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(dialCtx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://unix/healthz", nil)
	if err != nil {
		return err
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	var body interface{}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&body); err != nil {
		return fmt.Errorf("decode health response: %w", err)
	}
	if _, ok := body.(map[string]interface{}); !ok {
		return errors.New("health response is not a JSON object")
	}
	return nil
}

// CodeServerArguments returns the complete fixed argv placed directly into the
// tmux pane. No argument is interpreted by a shell.
func CodeServerArguments(binary, configPath, httpSocket, sessionSocket, profileDir, folder string) []string {
	return []string{
		binary,
		"--config", configPath,
		"--auth", "none",
		"--socket", httpSocket,
		"--socket-mode", "0600",
		"--session-socket", sessionSocket,
		"--user-data-dir", filepath.Join(profileDir, "user-data"),
		"--extensions-dir", filepath.Join(profileDir, "extensions"),
		"--ignore-last-opened",
		"--disable-telemetry",
		"--disable-update-check",
		"--disable-proxy",
		"--",
		folder,
	}
}

func ValidID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func newID(reader io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(reader, value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func tmuxTarget(id string) string { return "cs_" + id }

func instanceBasePath(id string) string { return "/code/" + id }

func newInstance(id, folder string, createdAt time.Time) Instance {
	name := filepath.Base(folder)
	if folder == string(filepath.Separator) {
		name = folder
	}
	return Instance{
		ID:         id,
		Name:       name,
		FolderPath: folder,
		CreatedAt:  createdAt,
		URL:        instanceBasePath(id) + "/",
	}
}

func canonicalDirectory(path string) (string, error) {
	if err := validatePathText(path); err != nil || !filepath.IsAbs(path) {
		return "", ErrInvalidPath
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", mapDirectoryError(err)
	}
	canonical = filepath.Clean(canonical)
	if err := validatePathText(canonical); err != nil || !filepath.IsAbs(canonical) {
		return "", ErrInvalidPath
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", mapDirectoryError(err)
	}
	if !info.IsDir() {
		return "", ErrDirectoryNotFound
	}
	return canonical, nil
}

func validatePathText(path string) error {
	if path == "" || !utf8.ValidString(path) || strings.ContainsRune(path, '\x00') {
		return ErrInvalidPath
	}
	for _, value := range path {
		if unicode.IsControl(value) {
			return ErrInvalidPath
		}
	}
	return nil
}

func ensureDirectoryAccessible(path string) error {
	// Access uses the real UID/GID (the configured service account in
	// production) and verifies both listing and traversal before any launch.
	if err := syscall.Access(path, 4|1); err != nil { // R_OK | X_OK
		return mapDirectoryError(err)
	}
	directory, err := os.Open(path)
	if err != nil {
		return mapDirectoryError(err)
	}
	_, readErr := directory.Readdirnames(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return mapDirectoryError(readErr)
	}
	if closeErr != nil {
		return mapDirectoryError(closeErr)
	}
	return nil
}

func mapDirectoryError(err error) error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return ErrDirectoryNotFound
	}
	if errors.Is(err, os.ErrPermission) {
		return ErrDirectoryInaccessible
	}
	if errors.Is(err, os.ErrInvalid) || errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENAMETOOLONG) {
		return ErrInvalidPath
	}
	return fmt.Errorf("access directory: %w", err)
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if filepath.Clean(canonical) != filepath.Clean(path) {
		return errors.New("directory path contains a symbolic link")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("directory path is not a real directory")
	}
	return os.Chmod(path, 0700)
}

// prepareRuntimeDirectory validates an existing application runtime root
// without chmodding it first. This keeps a stale symlink from changing the
// permissions of an unrelated target before it is rejected.
func prepareRuntimeDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime directory path is not a real directory")
	}
	return os.Chmod(path, 0700)
}

func clearDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func tmuxServerMissing(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "no server running") ||
		strings.Contains(message, "failed to connect") ||
		strings.Contains(message, "error connecting") ||
		strings.Contains(message, "no sessions")
}

// tmuxServerMissingFromProbe recognizes the additional message emitted when a
// list/has-session client connects while the server is exiting after its last
// session disappears. Match that message exactly after normalization: unlike
// the established connection errors above, it is safe evidence of absence only
// when returned by a read-only server probe. In particular, kill-server errors
// do not use this helper and cannot bypass post-kill confirmation.
func tmuxServerMissingFromProbe(output []byte) bool {
	message := strings.TrimSpace(strings.ToLower(string(output)))
	return tmuxServerMissing(output) || message == "server exited unexpectedly"
}

func tmuxTargetMissing(output []byte) bool {
	message := strings.ToLower(string(output))
	return tmuxServerMissing(output) || strings.Contains(message, "can't find session") ||
		strings.Contains(message, "session not found")
}

func tmuxDuplicateSession(output []byte, err error) bool {
	message := strings.ToLower(string(output))
	if err != nil {
		message += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(message, "duplicate session") ||
		strings.Contains(message, "session already exists")
}

func contextResult(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func commandError(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w (%s)", action, err, message)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
