package sessions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTmuxSession struct {
	target       string
	id           string
	name         string
	mouse        bool
	status       string
	historyLimit string
	windowIDs    []string
	windowStyles map[string]string
	activeStyles map[string]string
	environment  map[string]string
	created      int64
}

type fakeTmuxBuffer struct {
	name       string
	data       []byte
	sizeOutput string
}

type fakeRunner struct {
	mu                         sync.Mutex
	sessions                   map[string]*fakeTmuxSession
	calls                      [][]string
	clipboardMode              string
	clipboardTerminalOverride  string
	selectionBuffers           []fakeTmuxBuffer
	selectionListOutput        []byte
	selectionInspectError      error
	selectionReadError         error
	selectionBindings          map[string]string
	globalEnvironment          map[string]string
	globalWindowStyle          string
	globalWindowActiveStyle    string
	tmuxVersionOutput          []byte
	nextWindowID               int
	newSessionErrorAfterCreate error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		sessions:          make(map[string]*fakeTmuxSession),
		selectionBindings: make(map[string]string),
		tmuxVersionOutput: []byte("tmux 3.3a"),
		globalEnvironment: map[string]string{
			tmuxColorEnvironment:   "not-truecolor",
			tmuxNoColorEnvironment: "1",
		},
	}
}

func (f *fakeRunner) Run(_ context.Context, binary string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := append([]string{binary}, args...)
	f.calls = append(f.calls, call)
	if len(args) == 1 && args[0] == "-V" {
		return append([]byte(nil), f.tmuxVersionOutput...), nil
	}
	if len(args) == 1 && args[0] == "--version" {
		return []byte("ttyd 1.7.7"), nil
	}
	commandArgs := args
	if len(commandArgs) >= 2 && commandArgs[0] == "-f" {
		if commandArgs[1] != "/dev/null" {
			return nil, fmt.Errorf("unsafe tmux config: %v", call)
		}
		commandArgs = commandArgs[2:]
	}
	if len(commandArgs) < 3 || commandArgs[0] != "-S" {
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
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t0\t%d\t%d",
				target, session.id, session.name, len(session.windowIDs), session.created))
		}
		return []byte(strings.Join(lines, "\n")), nil
	case "list-windows":
		if len(commandArgs) != 7 || commandArgs[3] != "-t" ||
			commandArgs[5] != "-F" || commandArgs[6] != tmuxWindowListFormat {
			return nil, fmt.Errorf("unsafe window list args: %v", call)
		}
		session := f.sessions[commandArgs[4]]
		if session == nil {
			return nil, errors.New("target missing")
		}
		return []byte(strings.Join(session.windowIDs, "\n")), nil
	case "list-buffers":
		if len(commandArgs) != 5 || commandArgs[3] != "-F" || commandArgs[4] != tmuxSelectionListFormat {
			return nil, fmt.Errorf("unsafe selection list args: %v", call)
		}
		if f.selectionInspectError != nil {
			return append([]byte(nil), f.selectionListOutput...), f.selectionInspectError
		}
		if f.selectionListOutput != nil {
			return append([]byte(nil), f.selectionListOutput...), nil
		}
		lines := make([]string, 0, len(f.selectionBuffers))
		for _, buffer := range f.selectionBuffers {
			size := buffer.sizeOutput
			if size == "" {
				size = strconv.Itoa(len(buffer.data))
			}
			lines = append(lines, buffer.name+"\t"+size)
		}
		return []byte(strings.Join(lines, "\n")), nil
	case "show-buffer":
		if len(commandArgs) != 5 || commandArgs[3] != "-b" {
			return nil, fmt.Errorf("unsafe show-buffer args: %v", call)
		}
		for _, buffer := range f.selectionBuffers {
			if buffer.name != commandArgs[4] {
				continue
			}
			if f.selectionReadError != nil {
				return append([]byte(nil), buffer.data...), f.selectionReadError
			}
			return append([]byte(nil), buffer.data...), nil
		}
		return []byte("no buffer " + commandArgs[4]), errors.New("exit status 1")
	case "bind-key":
		if len(commandArgs) != 10 || commandArgs[3] != "-T" ||
			(commandArgs[4] != "copy-mode" && commandArgs[4] != "copy-mode-vi") ||
			commandArgs[5] != "MouseDragEnd1Pane" || commandArgs[6] != "send-keys" ||
			commandArgs[7] != "-X" || commandArgs[8] != "copy-selection-and-cancel" ||
			commandArgs[9] != tmuxSelectionBindingPrefix {
			return nil, fmt.Errorf("unsafe selection binding args: %v", call)
		}
		f.selectionBindings[commandArgs[4]] = strings.Join(commandArgs[6:], "\x00")
		return nil, nil
	case "start-server":
		if len(args) != 39 || args[0] != "-f" || args[1] != "/dev/null" ||
			len(commandArgs) != 37 ||
			commandArgs[3] != ";" ||
			commandArgs[4] != "set-environment" || commandArgs[5] != "-g" ||
			commandArgs[6] != tmuxColorEnvironment || commandArgs[7] != tmuxColorEnvironmentValue ||
			commandArgs[8] != ";" ||
			commandArgs[9] != "set-environment" || commandArgs[10] != "-gu" ||
			commandArgs[11] != tmuxNoColorEnvironment || commandArgs[12] != ";" ||
			commandArgs[13] != "set-option" || commandArgs[14] != "-g" ||
			commandArgs[15] != tmuxStatusOption || commandArgs[16] != tmuxStatusMode ||
			commandArgs[17] != ";" ||
			commandArgs[18] != "set-option" || commandArgs[19] != "-g" ||
			commandArgs[20] != tmuxHistoryLimitOption || commandArgs[21] != tmuxHistoryLimit ||
			commandArgs[22] != ";" ||
			commandArgs[23] != "set-option" || commandArgs[24] != "-g" ||
			commandArgs[25] != tmuxWindowStyleOption || commandArgs[26] != tmuxWindowStyle ||
			commandArgs[27] != ";" ||
			commandArgs[28] != "set-option" || commandArgs[29] != "-g" ||
			commandArgs[30] != tmuxWindowActiveStyleOption || commandArgs[31] != tmuxWindowStyle ||
			commandArgs[32] != ";" || commandArgs[33] != "new-session" ||
			commandArgs[34] != "-d" || commandArgs[35] != "-s" {
			return nil, fmt.Errorf("unsafe new-session args: %v", call)
		}
		target := commandArgs[36]
		if _, exists := f.sessions[target]; exists {
			return nil, errors.New("duplicate target")
		}
		if err := os.WriteFile(socket, []byte("fake socket marker"), 0600); err != nil {
			return nil, err
		}
		f.globalEnvironment[tmuxColorEnvironment] = tmuxColorEnvironmentValue
		delete(f.globalEnvironment, tmuxNoColorEnvironment)
		f.globalWindowStyle = tmuxWindowStyle
		f.globalWindowActiveStyle = tmuxWindowStyle
		environment := make(map[string]string, len(f.globalEnvironment))
		for name, value := range f.globalEnvironment {
			environment[name] = value
		}
		windowID := "@" + strconv.Itoa(f.nextWindowID)
		f.nextWindowID++
		f.sessions[target] = &fakeTmuxSession{
			target: target, status: tmuxStatusMode, historyLimit: tmuxHistoryLimit,
			windowIDs:    []string{windowID},
			windowStyles: map[string]string{windowID: tmuxWindowStyle},
			activeStyles: map[string]string{windowID: tmuxWindowStyle},
			environment:  environment, created: 1700000000,
		}
		if f.newSessionErrorAfterCreate != nil {
			return []byte("simulated cancellation after tmux accepted new-session"), f.newSessionErrorAfterCreate
		}
		return nil, nil
	case "set-environment":
		if len(commandArgs) == 6 && commandArgs[3] == "-g" &&
			commandArgs[4] == tmuxColorEnvironment && commandArgs[5] == tmuxColorEnvironmentValue {
			f.globalEnvironment[tmuxColorEnvironment] = tmuxColorEnvironmentValue
			return nil, nil
		}
		if len(commandArgs) == 5 && commandArgs[3] == "-gu" && commandArgs[4] == tmuxNoColorEnvironment {
			delete(f.globalEnvironment, tmuxNoColorEnvironment)
			return nil, nil
		}
		if len(commandArgs) == 7 && commandArgs[3] == "-t" &&
			commandArgs[5] == tmuxColorEnvironment && commandArgs[6] == tmuxColorEnvironmentValue {
			session := f.sessions[commandArgs[4]]
			if session == nil {
				return nil, errors.New("target missing")
			}
			session.environment[tmuxColorEnvironment] = tmuxColorEnvironmentValue
			return nil, nil
		}
		if len(commandArgs) == 7 && commandArgs[3] == "-u" && commandArgs[4] == "-t" &&
			commandArgs[6] == tmuxNoColorEnvironment {
			session := f.sessions[commandArgs[5]]
			if session == nil {
				return nil, errors.New("target missing")
			}
			delete(session.environment, tmuxNoColorEnvironment)
			return nil, nil
		}
		return nil, fmt.Errorf("unsafe set-environment args: %v", call)
	case "set-window-option":
		if len(commandArgs) != 7 || commandArgs[3] != "-t" ||
			(commandArgs[5] != tmuxWindowStyleOption && commandArgs[5] != tmuxWindowActiveStyleOption) ||
			commandArgs[6] != tmuxWindowStyle {
			return nil, fmt.Errorf("unsafe window option args: %v", call)
		}
		var session *fakeTmuxSession
		for _, candidate := range f.sessions {
			for _, windowID := range candidate.windowIDs {
				if windowID == commandArgs[4] {
					session = candidate
					break
				}
			}
		}
		if session == nil {
			return nil, errors.New("target missing")
		}
		switch commandArgs[5] {
		case tmuxWindowStyleOption:
			session.windowStyles[commandArgs[4]] = commandArgs[6]
		case tmuxWindowActiveStyleOption:
			session.activeStyles[commandArgs[4]] = commandArgs[6]
		}
		return nil, nil
	case "set-option":
		if len(commandArgs) == 6 && commandArgs[3] == "-s" {
			switch commandArgs[4] {
			case tmuxClipboardModeOption:
				if commandArgs[5] != tmuxClipboardMode {
					return nil, fmt.Errorf("unsafe clipboard mode: %v", call)
				}
				f.clipboardMode = commandArgs[5]
			case tmuxClipboardOption:
				if commandArgs[5] != tmuxClipboardTerminalOverride {
					return nil, fmt.Errorf("unsafe clipboard terminal override: %v", call)
				}
				f.clipboardTerminalOverride = commandArgs[5]
			default:
				return nil, fmt.Errorf("unsafe server set-option args: %v", call)
			}
			return nil, nil
		}
		if len(commandArgs) == 6 && commandArgs[3] == "-g" {
			if commandArgs[5] != tmuxWindowStyle {
				return nil, fmt.Errorf("unsafe global window style: %v", call)
			}
			switch commandArgs[4] {
			case tmuxWindowStyleOption:
				f.globalWindowStyle = commandArgs[5]
			case tmuxWindowActiveStyleOption:
				f.globalWindowActiveStyle = commandArgs[5]
			default:
				return nil, fmt.Errorf("unsafe global set-option args: %v", call)
			}
			return nil, nil
		}
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
		case tmuxStatusOption:
			if commandArgs[6] != tmuxStatusMode {
				return nil, errors.New("status must be disabled")
			}
			session.status = commandArgs[6]
		case tmuxHistoryLimitOption:
			if commandArgs[6] != tmuxHistoryLimit {
				return nil, errors.New("unexpected history limit")
			}
			session.historyLimit = commandArgs[6]
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

func TestValidateTmuxVersionForDefaultColorQueries(t *testing.T) {
	for _, output := range []string{"tmux 3.2", "tmux 3.2a", "tmux 3.3a", "tmux 4.0", "tmux next-3.6"} {
		if err := validateTmuxVersion([]byte(output)); err != nil {
			t.Errorf("validateTmuxVersion(%q) = %v, want success", output, err)
		}
	}
	for _, output := range []string{"tmux 2.9", "tmux 3.1c", "tmux master", "3.3a", ""} {
		if err := validateTmuxVersion([]byte(output)); err == nil {
			t.Errorf("validateTmuxVersion(%q) succeeded, want rejection", output)
		}
	}
}

func TestParseTmuxWindowTargetsRejectsUnsafeOutput(t *testing.T) {
	got, err := parseTmuxWindowTargets([]byte("@0\n@42\n"))
	if err != nil || strings.Join(got, ",") != "@0,@42" {
		t.Fatalf("parseTmuxWindowTargets(valid) = %#v, %v", got, err)
	}
	for _, output := range []string{"", "@", "@1\nnot-a-target", "@-1", "@1 extra"} {
		if got, err := parseTmuxWindowTargets([]byte(output)); err == nil {
			t.Errorf("parseTmuxWindowTargets(%q) = %#v, want rejection", output, got)
		}
	}
}

func TestInitializeRejectsTmuxWithoutDefaultColorQueries(t *testing.T) {
	runtime, err := os.MkdirTemp("/tmp", "rt-version-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	runner := newFakeRunner()
	runner.tmuxVersionOutput = []byte("tmux 3.1c")
	manager := newManager(Config{
		TmuxBinary: "/usr/bin/tmux", TtydBinary: "/usr/bin/ttyd", RuntimeDir: runtime,
		MaxSessions: 1, StartTimeout: time.Second,
	}, runner, &fakeStarter{})
	err = manager.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tmux 3.2 or newer is required") ||
		!strings.Contains(err.Error(), "tmux 3.1c") {
		t.Fatalf("Initialize() error = %v, want clear minimum-version failure", err)
	}
	runner.mu.Lock()
	calls := append([][]string(nil), runner.calls...)
	runner.mu.Unlock()
	if !containsCommand(calls, []string{"/usr/bin/tmux", "-V"}) {
		t.Fatalf("Initialize() did not check configured tmux binary directly: %#v", calls)
	}
}

func TestTtydArgumentsAreFixedAndShellFree(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	got := TtydArguments("/usr/bin/tmux", "/run/rt/tmux.sock", "/run/rt/ttyd/id.sock", id)
	want := []string{
		"-W", "-O", "-t", ttydRendererPreference, "-t", ttydThemePreference, "-b", "/terminal/" + id,
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
	runner.mu.Lock()
	createdTmux := runner.sessions[tmuxTarget(created.ID)]
	initialColor := createdTmux.environment[tmuxColorEnvironment]
	_, initialNoColor := createdTmux.environment[tmuxNoColorEnvironment]
	initialStatus := createdTmux.status
	initialHistoryLimit := createdTmux.historyLimit
	initialStylesMatch := fakeSessionStylesMatch(createdTmux, tmuxWindowStyle)
	createCalls := append([][]string(nil), runner.calls...)
	runner.mu.Unlock()
	if initialColor != tmuxColorEnvironmentValue || initialNoColor {
		t.Fatalf("Create() initial pane environment = COLORTERM=%q NO_COLOR-present=%t", initialColor, initialNoColor)
	}
	if initialStatus != tmuxStatusMode || initialHistoryLimit != tmuxHistoryLimit {
		t.Fatalf("Create() inherited options = status %q history-limit %q", initialStatus, initialHistoryLimit)
	}
	if !initialStylesMatch {
		t.Fatalf("Create() inherited terminal colors = window-style %#v window-active-style %#v",
			createdTmux.windowStyles, createdTmux.activeStyles)
	}
	wantCreateCall := []string{
		"tmux", "-f", "/dev/null", "-S", manager.tmuxSocket(),
		"start-server", ";",
		"set-environment", "-g", tmuxColorEnvironment, tmuxColorEnvironmentValue, ";",
		"set-environment", "-gu", tmuxNoColorEnvironment, ";",
		"set-option", "-g", tmuxStatusOption, tmuxStatusMode, ";",
		"set-option", "-g", tmuxHistoryLimitOption, tmuxHistoryLimit, ";",
		"set-option", "-g", tmuxWindowStyleOption, tmuxWindowStyle, ";",
		"set-option", "-g", tmuxWindowActiveStyleOption, tmuxWindowStyle, ";",
		"new-session", "-d", "-s", tmuxTarget(created.ID),
	}
	if !containsCommand(createCalls, wantCreateCall) {
		t.Fatalf("Create() did not sanitize the first pane in its tmux command queue: %#v", createCalls)
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
	configuredTmux := runner.sessions[tmuxTarget(created.ID)]
	mouseEnabled := configuredTmux.mouse
	status := configuredTmux.status
	historyLimit := configuredTmux.historyLimit
	stylesMatch := fakeSessionStylesMatch(configuredTmux, tmuxWindowStyle)
	color := configuredTmux.environment[tmuxColorEnvironment]
	_, noColor := configuredTmux.environment[tmuxNoColorEnvironment]
	globalColor := runner.globalEnvironment[tmuxColorEnvironment]
	_, globalNoColor := runner.globalEnvironment[tmuxNoColorEnvironment]
	globalWindowStyle := runner.globalWindowStyle
	globalActiveStyle := runner.globalWindowActiveStyle
	clipboardMode := runner.clipboardMode
	clipboardTerminalOverride := runner.clipboardTerminalOverride
	selectionBindings := make(map[string]string, len(runner.selectionBindings))
	for table, binding := range runner.selectionBindings {
		selectionBindings[table] = binding
	}
	runner.mu.Unlock()
	if !mouseEnabled {
		t.Fatal("Connect() did not enable tmux mouse scrolling")
	}
	if status != tmuxStatusMode || historyLimit != tmuxHistoryLimit {
		t.Fatalf("Connect() tmux display options = status %q history-limit %q", status, historyLimit)
	}
	if !stylesMatch || globalWindowStyle != tmuxWindowStyle || globalActiveStyle != tmuxWindowStyle {
		t.Fatalf("Connect() terminal colors = window %#v active %#v global-window %q global-active %q",
			configuredTmux.windowStyles, configuredTmux.activeStyles, globalWindowStyle, globalActiveStyle)
	}
	if color != tmuxColorEnvironmentValue || noColor || globalColor != tmuxColorEnvironmentValue || globalNoColor {
		t.Fatalf("Connect() environment = session COLORTERM=%q NO_COLOR=%t, global COLORTERM=%q NO_COLOR=%t",
			color, noColor, globalColor, globalNoColor)
	}
	if clipboardTerminalOverride != tmuxClipboardTerminalOverride {
		t.Fatalf("Connect() clipboard override = %q, want %q", clipboardTerminalOverride, tmuxClipboardTerminalOverride)
	}
	if clipboardMode != tmuxClipboardMode {
		t.Fatalf("Connect() clipboard mode = %q, want %q", clipboardMode, tmuxClipboardMode)
	}
	wantSelectionBinding := strings.Join([]string{
		"send-keys", "-X", "copy-selection-and-cancel", tmuxSelectionBindingPrefix,
	}, "\x00")
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		if selectionBindings[table] != wantSelectionBinding {
			t.Fatalf("Connect() %s selection binding = %q, want %q", table, selectionBindings[table], wantSelectionBinding)
		}
	}
	runner.mu.Lock()
	configuredTmux = runner.sessions[tmuxTarget(created.ID)]
	configuredTmux.mouse = false
	configuredTmux.status = "on"
	configuredTmux.historyLimit = "2000"
	for _, windowID := range configuredTmux.windowIDs {
		configuredTmux.windowStyles[windowID] = "default"
		configuredTmux.activeStyles[windowID] = "default"
	}
	recoveredWindowID := "@" + strconv.Itoa(runner.nextWindowID)
	runner.nextWindowID++
	configuredTmux.windowIDs = append(configuredTmux.windowIDs, recoveredWindowID)
	configuredTmux.windowStyles[recoveredWindowID] = "bg=red"
	configuredTmux.activeStyles[recoveredWindowID] = "bg=blue"
	configuredTmux.environment[tmuxColorEnvironment] = "not-truecolor"
	configuredTmux.environment[tmuxNoColorEnvironment] = "1"
	runner.globalEnvironment[tmuxColorEnvironment] = "not-truecolor"
	runner.globalEnvironment[tmuxNoColorEnvironment] = "1"
	runner.globalWindowStyle = "default"
	runner.globalWindowActiveStyle = "default"
	runner.clipboardMode = ""
	runner.clipboardTerminalOverride = ""
	runner.selectionBindings = make(map[string]string)
	runner.mu.Unlock()
	if _, _, err := manager.Connect(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if len(starter.calls) != 1 {
		t.Fatalf("second connect started another ttyd: %d calls", len(starter.calls))
	}
	runner.mu.Lock()
	configuredTmux = runner.sessions[tmuxTarget(created.ID)]
	mouseEnabled = configuredTmux.mouse
	status = configuredTmux.status
	historyLimit = configuredTmux.historyLimit
	stylesMatch = fakeSessionStylesMatch(configuredTmux, tmuxWindowStyle)
	color = configuredTmux.environment[tmuxColorEnvironment]
	_, noColor = configuredTmux.environment[tmuxNoColorEnvironment]
	globalColor = runner.globalEnvironment[tmuxColorEnvironment]
	_, globalNoColor = runner.globalEnvironment[tmuxNoColorEnvironment]
	globalWindowStyle = runner.globalWindowStyle
	globalActiveStyle = runner.globalWindowActiveStyle
	clipboardMode = runner.clipboardMode
	clipboardTerminalOverride = runner.clipboardTerminalOverride
	selectionBindings = make(map[string]string, len(runner.selectionBindings))
	for table, binding := range runner.selectionBindings {
		selectionBindings[table] = binding
	}
	runner.mu.Unlock()
	if !mouseEnabled {
		t.Fatal("repeated Connect() did not restore tmux mouse scrolling before reusing ttyd")
	}
	if status != tmuxStatusMode || historyLimit != tmuxHistoryLimit {
		t.Fatal("repeated Connect() did not restore tmux display options before reusing ttyd")
	}
	if !stylesMatch || globalWindowStyle != tmuxWindowStyle || globalActiveStyle != tmuxWindowStyle {
		t.Fatal("repeated Connect() did not restore tmux terminal colors before reusing ttyd")
	}
	if color != tmuxColorEnvironmentValue || noColor || globalColor != tmuxColorEnvironmentValue || globalNoColor {
		t.Fatal("repeated Connect() did not sanitize tmux environments before reusing ttyd")
	}
	if clipboardTerminalOverride != tmuxClipboardTerminalOverride {
		t.Fatalf("repeated Connect() clipboard override = %q, want %q", clipboardTerminalOverride, tmuxClipboardTerminalOverride)
	}
	if clipboardMode != tmuxClipboardMode {
		t.Fatalf("repeated Connect() clipboard mode = %q, want %q", clipboardMode, tmuxClipboardMode)
	}
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		if selectionBindings[table] != wantSelectionBinding {
			t.Fatalf("repeated Connect() did not restore %s selection binding", table)
		}
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
		if len(call) == 40 && call[1] == "-f" && call[2] == "/dev/null" &&
			call[5] == "start-server" && call[36] == "new-session" {
			createdTarget = call[39]
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

func TestLatestSelectionReturnsNewestBufferForRequestedSession(t *testing.T) {
	manager, runner, _ := testManager(t, 2)
	created, err := manager.Create(context.Background(), "Clipboard")
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.Create(context.Background(), "Other terminal")
	if err != nil {
		t.Fatal(err)
	}
	want := "first line\nУкраїнський текст\n"
	wantName := tmuxSelectionPrefix(created.ID) + "12"
	otherName := tmuxSelectionPrefix(other.ID) + "13"
	runner.mu.Lock()
	// list-buffers is newest-first. The other terminal's newer selection must
	// be skipped, as must a manually named buffer with a nonnumeric suffix.
	runner.selectionBuffers = []fakeTmuxBuffer{
		{name: otherName, data: []byte("other terminal secret")},
		{name: tmuxSelectionPrefix(created.ID) + "manual", data: []byte("not automatic")},
		{name: wantName, data: []byte(want)},
		{name: tmuxSelectionPrefix(created.ID) + "7", data: []byte("older")},
		{name: "buffer14", data: []byte("global")},
	}
	runner.mu.Unlock()

	got, err := manager.LatestSelection(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("LatestSelection() = %q, want %q", got, want)
	}
	otherGot, err := manager.LatestSelection(context.Background(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherGot != "other terminal secret" {
		t.Fatalf("LatestSelection(other) = %q, want session-scoped buffer", otherGot)
	}

	runner.mu.Lock()
	calls := append([][]string(nil), runner.calls...)
	runner.mu.Unlock()
	wantListCall := []string{"tmux", "-S", manager.tmuxSocket(), "list-buffers", "-F", tmuxSelectionListFormat}
	wantReadCall := []string{"tmux", "-S", manager.tmuxSocket(), "show-buffer", "-b", wantName}
	wantOtherReadCall := []string{"tmux", "-S", manager.tmuxSocket(), "show-buffer", "-b", otherName}
	if !containsCommand(calls, wantListCall) || !containsCommand(calls, wantReadCall) || !containsCommand(calls, wantOtherReadCall) {
		t.Fatalf("LatestSelection() calls = %#v", calls)
	}
}

func TestLatestSelectionValidatesSessionAndBufferState(t *testing.T) {
	manager, runner, _ := testManager(t, 1)
	created, err := manager.Create(context.Background(), "Clipboard")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.LatestSelection(context.Background(), "invalid"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid ID error = %v, want ErrInvalidID", err)
	}
	if _, err := manager.LatestSelection(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session error = %v, want ErrNotFound", err)
	}
	if _, err := manager.LatestSelection(context.Background(), created.ID); !errors.Is(err, ErrNoSelection) {
		t.Fatalf("no-buffer error = %v, want ErrNoSelection", err)
	}

	runner.mu.Lock()
	runner.selectionBuffers = []fakeTmuxBuffer{{
		name: tmuxSelectionPrefix(created.ID) + "0", sizeOutput: "0",
	}}
	runner.mu.Unlock()
	if _, err := manager.LatestSelection(context.Background(), created.ID); !errors.Is(err, ErrNoSelection) {
		t.Fatalf("empty-buffer error = %v, want ErrNoSelection", err)
	}
}

func TestLatestSelectionBoundsAndValidatesTmuxOutput(t *testing.T) {
	manager, runner, _ := testManager(t, 1)
	created, err := manager.Create(context.Background(), "Clipboard")
	if err != nil {
		t.Fatal(err)
	}

	bufferName := tmuxSelectionPrefix(created.ID) + "0"
	runner.mu.Lock()
	runner.selectionBuffers = []fakeTmuxBuffer{{
		name: bufferName, data: []byte("small"), sizeOutput: strconv.Itoa(maxSelectionBytes + 1),
	}}
	runner.mu.Unlock()
	if _, err := manager.LatestSelection(context.Background(), created.ID); !errors.Is(err, ErrSelectionTooLarge) {
		t.Fatalf("preflight size error = %v, want ErrSelectionTooLarge", err)
	}

	runner.mu.Lock()
	runner.selectionBuffers = []fakeTmuxBuffer{{
		name: bufferName, data: bytes.Repeat([]byte{'x'}, maxSelectionBytes+1), sizeOutput: "1",
	}}
	runner.mu.Unlock()
	if _, err := manager.LatestSelection(context.Background(), created.ID); !errors.Is(err, ErrSelectionTooLarge) {
		t.Fatalf("post-read size error = %v, want ErrSelectionTooLarge", err)
	}

	runner.mu.Lock()
	runner.selectionBuffers = []fakeTmuxBuffer{{
		name: bufferName, data: []byte{0xff}, sizeOutput: "1",
	}}
	runner.mu.Unlock()
	if _, err := manager.LatestSelection(context.Background(), created.ID); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("invalid UTF-8 error = %v, want ErrInvalidSelection", err)
	}
}

func TestLatestSelectionRejectsMalformedSizeAndWrapsTmuxFailures(t *testing.T) {
	manager, runner, _ := testManager(t, 1)
	created, err := manager.Create(context.Background(), "Clipboard")
	if err != nil {
		t.Fatal(err)
	}

	bufferName := tmuxSelectionPrefix(created.ID) + "0"
	runner.mu.Lock()
	runner.selectionBuffers = []fakeTmuxBuffer{{
		name: bufferName, data: []byte("small"), sizeOutput: "not-a-size",
	}}
	runner.mu.Unlock()
	if _, err := manager.LatestSelection(context.Background(), created.ID); err == nil || !strings.Contains(err.Error(), "invalid tmux buffer size") {
		t.Fatalf("malformed size error = %v", err)
	}

	runner.mu.Lock()
	runner.selectionListOutput = []byte("tmux inspect failed")
	runner.selectionInspectError = context.DeadlineExceeded
	runner.mu.Unlock()
	if _, err := manager.LatestSelection(context.Background(), created.ID); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "tmux inspect failed") {
		t.Fatalf("inspect error = %v", err)
	}

	runner.mu.Lock()
	runner.selectionInspectError = nil
	runner.selectionListOutput = nil
	secretSelection := "SECRET_SELECTION_MUST_NOT_REACH_LOGS"
	runner.selectionBuffers = []fakeTmuxBuffer{{
		name: bufferName, data: []byte(secretSelection), sizeOutput: "5",
	}}
	runner.selectionReadError = context.Canceled
	runner.mu.Unlock()
	if _, err := manager.LatestSelection(context.Background(), created.ID); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secretSelection) {
		t.Fatalf("read error = %v", err)
	}
}

func containsCommand(calls [][]string, want []string) bool {
	for _, call := range calls {
		if strings.Join(call, "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func fakeSessionStylesMatch(session *fakeTmuxSession, want string) bool {
	if session == nil || len(session.windowIDs) == 0 {
		return false
	}
	for _, windowID := range session.windowIDs {
		if session.windowStyles[windowID] != want || session.activeStyles[windowID] != want {
			return false
		}
	}
	return true
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
