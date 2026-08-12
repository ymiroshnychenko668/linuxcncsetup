package playbooks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLightDMAutologinIsFullyImplemented(t *testing.T) {
	playbookPath, cleanup, err := Materialize(Autologin)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	playbook := read(playbookPath)
	lightdm := read(filepath.Join(
		filepath.Dir(playbookPath),
		"tasks",
		"lightdm.yml",
	))

	for _, expected := range []string{
		"Apply the LightDM automatic-login implementation",
		`when: autologin_mode == "lightdm"`,
		"tasks/lightdm.yml",
	} {
		if !strings.Contains(playbook, expected) {
			t.Errorf("automatic-login playbook does not contain %q", expected)
		}
	}

	for _, expected := range []string{
		"name: lightdm",
		"policy_rc_d: 101",
		"/etc/pam.d/lightdm-autologin",
		"/etc/lightdm/lightdm.conf.d/50-linuxcnc-autologin.conf",
		"pam-autologin-service=lightdm-autologin",
		"autologin-user={{ target_user }}",
		"autologin-user-timeout=0",
		"/usr/sbin/lightdm",
		"--show-config",
		"managed automatic-login settings to be effective",
		"/etc/X11/default-display-manager",
		"/usr/sbin/lightdm",
		"name: greetd.service",
		"enabled: false",
		"Unmask LightDM without starting it",
		"masked: false",
		"name: lightdm.service",
		"enabled: true",
		"force: true",
		"is-enabled",
		"failed_when: false",
		"Resolve the display-manager boot alias",
		"/usr/bin/readlink",
		"--canonicalize",
		"/etc/systemd/system/display-manager.service",
		"--property=FragmentPath",
		"autologin_display_manager_alias.stdout | trim ==",
		"autologin_lightdm_unit_path.stdout | trim",
		`regex_findall(`,
		`autologin-user=(\S+)\s*$`,
		"Require LightDM to own the display-manager boot alias",
	} {
		if !strings.Contains(lightdm, expected) {
			t.Errorf("LightDM automatic-login tasks do not contain %q", expected)
		}
	}

	for description, pattern := range map[string]string{
		"display-manager service state change": `(?m)^\s*state:\s*["']?(started|stopped|restarted|reloaded)["']?\s*$`,
		"direct systemctl session mutation":    `(?m)(systemctl\s+(start|stop|restart|reload)\b|^\s*-\s*(start|stop|restart|reload)\s*$)`,
		"forced automatic-login session":       `(?m)^\s*autologin-session=`,
	} {
		if regexp.MustCompile(pattern).MatchString(lightdm) {
			t.Errorf(
				"LightDM automatic-login tasks contain %s matching %q",
				description,
				pattern,
			)
		}
	}
}
