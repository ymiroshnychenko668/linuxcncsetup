package playbooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxCNCAutostartValidatesSwayWithoutALiveSession(t *testing.T) {
	playbookPath, cleanup, err := Materialize(LinuxCNCAutostart)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	playbookData, err := os.ReadFile(playbookPath)
	if err != nil {
		t.Fatalf("read LinuxCNC autostart playbook: %v", err)
	}
	taskPath := filepath.Join(
		filepath.Dir(playbookPath),
		"tasks",
		"linuxcnc_autostart.yml",
	)
	taskData, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read LinuxCNC autostart tasks: %v", err)
	}

	playbook := string(playbookData)
	tasks := string(taskData)
	combined := playbook + "\n" + tasks

	for _, forbidden := range []string{
		`XDG_RUNTIME_DIR: "/run/user/`,
		"XDG_RUNTIME_DIR is not set",
	} {
		if strings.Contains(combined, forbidden) {
			t.Errorf("LinuxCNC autostart validation contains %q", forbidden)
		}
	}

	for _, expected := range []string{
		"prefix: linuxcncsetup-linuxcnc-pre-sway-",
		"prefix: linuxcncsetup-linuxcnc-snippet-sway-",
		"prefix: linuxcncsetup-linuxcnc-final-sway-",
		"WLR_BACKENDS: headless",
		"WLR_RENDERER: pixman",
		`WLR_LIBINPUT_NO_DEVICES: "1"`,
		`WAYLAND_DISPLAY: ""`,
		`DISPLAY: ""`,
		`SWAYSOCK: ""`,
		"validate: /usr/bin/sway --validate --config %s",
		`"Error(s) loading config!"`,
		"always:",
		"state: absent",
	} {
		if !strings.Contains(combined, expected) {
			t.Errorf("LinuxCNC autostart validation does not contain %q", expected)
		}
	}

	if got := strings.Count(combined, "WLR_BACKENDS: headless"); got != 3 {
		t.Errorf("headless Sway validation environments = %d; want 3", got)
	}
	if got := strings.Count(combined, "XDG_RUNTIME_DIR:"); got != 3 {
		t.Errorf("isolated Sway validation runtimes = %d; want 3", got)
	}
}

func TestLinuxCNCAutostartSharesFullscreenLauncherWithWaybar(t *testing.T) {
	playbookPath, cleanup, err := Materialize(LinuxCNCAutostart)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	root := filepath.Dir(playbookPath)
	read := func(relativePath string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		return string(data)
	}

	playbook := read("linuxcnc-autostart.yml")
	tasks := read("tasks/linuxcnc_autostart.yml")
	launcher := read("templates/linuxcnc-autostart.sh.j2")
	swaySnippet := read("templates/linuxcnc-autostart-sway.conf.j2")

	for _, expected := range []string{
		".config/waybar/config",
		"Validate the existing Waybar JSON configuration",
		"linuxcnc_autostart_waybar_config",
	} {
		if !strings.Contains(playbook, expected) {
			t.Errorf("LinuxCNC autostart preflight does not contain %q", expected)
		}
	}

	sharedLauncher := `/.local/bin/linuxcnc-autostart`
	for _, expected := range []string{
		`"custom/linuxcnc"`,
		`"modules-left"`,
		`"on-click"`,
		sharedLauncher,
		"Open LinuxCNC or switch to workspace 1",
		"to_nice_json",
		"validate: /usr/bin/python3 -m json.tool %s",
	} {
		if !strings.Contains(tasks, expected) {
			t.Errorf("Waybar launcher tasks do not contain %q", expected)
		}
	}

	for _, expected := range []string{
		`[app_id="(?i)^qtvcp$"] focus`,
		`[class="(?i)^qtvcp$"] focus`,
		`'workspace number 1'`,
		`exec /usr/bin/linuxcnc`,
	} {
		if !strings.Contains(launcher, expected) {
			t.Errorf("shared LinuxCNC launcher does not contain %q", expected)
		}
	}
	if focusAt, launchAt := strings.Index(launcher, `] focus`),
		strings.Index(launcher, `exec /usr/bin/linuxcnc`); focusAt > launchAt {
		t.Error("shared LinuxCNC launcher starts LinuxCNC before checking for its window")
	}

	for _, expected := range []string{
		`assign [app_id="(?i)^qtvcp$"] workspace number 1`,
		`assign [class="(?i)^qtvcp$"] workspace number 1`,
		`for_window [app_id="(?i)^qtvcp$"] fullscreen enable`,
		`for_window [class="(?i)^qtvcp$"] fullscreen enable`,
		`exec --no-startup-id "$HOME` + sharedLauncher + `"`,
	} {
		if !strings.Contains(swaySnippet, expected) {
			t.Errorf("Sway autostart snippet does not contain %q", expected)
		}
	}
}

func TestLinuxCNCAutostartX11UsesNativeXFCEAutostart(t *testing.T) {
	playbookPath, cleanup, err := Materialize(LinuxCNCAutostartX11)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	root := filepath.Dir(playbookPath)
	read := func(relativePath string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		return string(data)
	}

	playbook := read("linuxcnc-autostart-x11.yml")
	tasks := read("tasks/linuxcnc_autostart_x11.yml")
	launcher := read("templates/linuxcnc-autostart-x11.sh.j2")
	desktopEntry := read("templates/linuxcnc-autostart-x11.desktop.j2")

	for _, expected := range []string{
		"Configure LinuxCNC autostart for XFCE on X11",
		"/usr/bin/xfce4-session",
		"/usr/bin/xfwm4",
		"/usr/share/xsessions/xfce.desktop",
		"Require a QtVCP LinuxCNC configuration",
		"tasks/linuxcnc_autostart_x11.yml",
		"next XFCE X11 login",
		"does nothing in XFCE",
		"LinuxCNC was not launched in the current session",
	} {
		if !strings.Contains(playbook, expected) {
			t.Errorf("XFCE X11 playbook does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{
		"swaymsg",
		".config/sway",
		".config/waybar",
		"WLR_BACKENDS",
		"SWAYSOCK",
	} {
		if strings.Contains(playbook, forbidden) {
			t.Errorf("XFCE X11 playbook contains Sway dependency %q", forbidden)
		}
	}

	for _, expected := range []string{
		`["desktop-file-utils", "wmctrl"]`,
		"difference(ansible_facts.packages.keys() | list)",
		"linuxcnc_x11_missing_packages",
		"/usr/bin/desktop-file-validate",
		"/usr/bin/wmctrl",
		`linuxcnc_x11_home }}/.local"`,
		`linuxcnc_x11_home }}/.local/bin"`,
		`linuxcnc_x11_home }}/.config"`,
		`linuxcnc_x11_home }}/.config/autostart"`,
		"follow: false",
		"Refuse symlinked or non-directory XFCE autostart paths",
		"Refuse symlinked or non-regular managed XFCE autostart files",
		"templates/linuxcnc-autostart-x11.sh.j2",
		"templates/linuxcnc-autostart-x11.desktop.j2",
		"linuxcncsetup-linuxcnc-x11.desktop",
		"validate: /bin/sh -n %s",
		"validate: /usr/bin/desktop-file-validate %s",
		"when: not ansible_check_mode",
	} {
		if !strings.Contains(tasks, expected) {
			t.Errorf("XFCE X11 tasks do not contain %q", expected)
		}
	}
	for _, forbidden := range []string{
		"gtk-launch",
		"exo-open",
		"state: started",
		"state: stopped",
		"state: restarted",
		"state: reloaded",
	} {
		if strings.Contains(tasks, forbidden) {
			t.Errorf("XFCE X11 tasks mutate the current session with %q", forbidden)
		}
	}

	for _, expected := range []string{
		"[Desktop Entry]",
		"Type=Application",
		"TryExec=/usr/bin/linuxcnc",
		`Exec="{{ linuxcnc_x11_home }}/.local/bin/linuxcnc-autostart-x11"`,
		"OnlyShowIn=XFCE;",
		"Terminal=false",
		"StartupNotify=false",
		"Hidden=false",
	} {
		if !strings.Contains(desktopEntry, expected) {
			t.Errorf("XFCE XDG autostart entry does not contain %q", expected)
		}
	}

	for _, expected := range []string{
		`[ "${XDG_SESSION_TYPE:-}" = "x11" ] || exit 0`,
		`[ -n "${DISPLAY:-}" ] || exit 0`,
		"linuxcncsetup-linuxcnc-autostart-x11.lock",
		"/usr/bin/flock --nonblock 9",
		"/usr/bin/wmctrl -lx",
		`tolower($3) ~ /(^|[.])qtvcp([.]|$)/`,
		"/usr/bin/wmctrl -s 0",
		`-t 0`,
		"add,fullscreen",
		"/usr/bin/pgrep",
		`exec /usr/bin/linuxcnc {{ linuxcnc_config | ansible.builtin.quote }}`,
		`/usr/bin/linuxcnc {{ linuxcnc_config | ansible.builtin.quote }} &`,
		`wait "$linuxcnc_pid"`,
	} {
		if !strings.Contains(launcher, expected) {
			t.Errorf("XFCE X11 launcher does not contain %q", expected)
		}
	}

	focusAt := strings.Index(launcher, "qtvcp_window_id=$(find_qtvcp_window)")
	launchAt := strings.LastIndex(launcher, "/usr/bin/linuxcnc")
	if focusAt < 0 || launchAt < 0 || focusAt > launchAt {
		t.Fatal("XFCE X11 launcher starts LinuxCNC before checking for QtVCP")
	}

	lockAt := strings.Index(launcher, "/usr/bin/flock --nonblock 9")
	if lockAt < 0 || focusAt > lockAt {
		t.Fatal("XFCE X11 launcher takes the startup lock before focusing an existing QtVCP window")
	}
	contentionAt := strings.Index(launcher, "if ! /usr/bin/flock --nonblock 9; then")
	if contentionAt < 0 {
		t.Fatal("XFCE X11 launcher does not handle startup lock contention")
	}
	waitAt := strings.Index(launcher[contentionAt:], "qtvcp_window_id=$(wait_for_qtvcp_window)")
	if waitAt < 0 {
		t.Fatal("XFCE X11 launcher does not wait for and focus a window owned by the active launcher")
	}

	preflightAt := strings.Index(tasks, "Inspect managed XFCE autostart directories")
	installAt := strings.Index(tasks, "Install missing XFCE launcher packages")
	createAt := strings.Index(tasks, "Create the target user's XFCE autostart directories")
	if preflightAt < 0 || installAt < 0 || createAt < 0 ||
		preflightAt > installAt || installAt > createAt {
		t.Fatal("XFCE X11 destination preflight must complete before package or filesystem changes")
	}
	if !strings.Contains(tasks[createAt:], "when: not item.stat.exists") {
		t.Fatal("XFCE X11 setup must preserve existing autostart directory metadata")
	}
}

func TestLinuxCNCAutostartX11FocusesWindowDuringLockContention(t *testing.T) {
	playbookPath, cleanup, err := Materialize(LinuxCNCAutostartX11)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	launcherData, err := os.ReadFile(filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcnc-autostart-x11.sh.j2",
	))
	if err != nil {
		t.Fatalf("read XFCE X11 launcher: %v", err)
	}

	testDir := t.TempDir()
	writeExecutable := func(name, body string) string {
		t.Helper()
		path := filepath.Join(testDir, name)
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	wmctrlPath := writeExecutable("wmctrl", `#!/bin/sh
printf 'wmctrl %s\n' "$*" >> "$AUTOSTART_TEST_LOG"
if [ "$1" = "-lx" ]; then
    count=0
    if [ -r "$AUTOSTART_TEST_STATE" ]; then
        read -r count < "$AUTOSTART_TEST_STATE"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$AUTOSTART_TEST_STATE"
    if [ "$count" -ge 2 ]; then
        printf '%s\n' '0x01000001 0 qtvcp.QtVcp test-host LinuxCNC'
    fi
fi
`)
	flockPath := writeExecutable("flock", `#!/bin/sh
printf 'flock %s\n' "$*" >> "$AUTOSTART_TEST_LOG"
exit 1
`)
	linuxcncPath := writeExecutable("linuxcnc", `#!/bin/sh
printf 'linuxcnc %s\n' "$*" >> "$AUTOSTART_TEST_LOG"
exit 70
`)

	shellQuote := func(value string) string {
		return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
	}
	launcher := string(launcherData)
	launcher = strings.ReplaceAll(launcher, "/usr/bin/wmctrl", shellQuote(wmctrlPath))
	launcher = strings.ReplaceAll(launcher, "/usr/bin/flock", shellQuote(flockPath))
	launcher = strings.ReplaceAll(launcher, "/usr/bin/linuxcnc", shellQuote(linuxcncPath))
	launcher = strings.ReplaceAll(
		launcher,
		"{{ linuxcnc_config | ansible.builtin.quote }}",
		shellQuote(filepath.Join(testDir, "machine config.ini")),
	)
	launcherPath := writeExecutable("launcher", launcher)

	logPath := filepath.Join(testDir, "commands.log")
	command := exec.Command("/bin/sh", launcherPath)
	command.Env = append(
		os.Environ(),
		"XDG_SESSION_TYPE=x11",
		"DISPLAY=:99",
		"XDG_RUNTIME_DIR="+testDir,
		"AUTOSTART_TEST_LOG="+logPath,
		"AUTOSTART_TEST_STATE="+filepath.Join(testDir, "wmctrl-state"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run rendered XFCE X11 launcher: %v\n%s", err, output)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read launcher command log: %v", err)
	}
	log := string(logData)
	for _, expected := range []string{
		"flock --nonblock 9",
		"wmctrl -i -r 0x01000001 -t 0",
		"wmctrl -i -r 0x01000001 -b add,fullscreen",
		"wmctrl -i -a 0x01000001",
	} {
		if !strings.Contains(log, expected) {
			t.Errorf("launcher command log does not contain %q:\n%s", expected, log)
		}
	}
	if strings.Contains(log, "linuxcnc ") {
		t.Errorf("launcher started duplicate LinuxCNC during lock contention:\n%s", log)
	}
}
