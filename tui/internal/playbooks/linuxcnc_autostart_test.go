package playbooks

import (
	"os"
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
