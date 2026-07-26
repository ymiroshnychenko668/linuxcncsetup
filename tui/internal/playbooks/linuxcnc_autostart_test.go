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
