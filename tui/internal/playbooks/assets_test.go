package playbooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterialize(t *testing.T) {
	for _, playbook := range []Playbook{Autologin, InstallSway, LinuxCNCAutostart} {
		t.Run(string(playbook), func(t *testing.T) {
			playbookPath, cleanup, err := Materialize(playbook)
			if err != nil {
				t.Fatalf("Materialize() error: %v", err)
			}
			directory := filepath.Dir(playbookPath)
			t.Cleanup(cleanup)

			for _, path := range []string{
				playbookPath,
				filepath.Join(directory, "tasks", "lightdm.yml"),
				filepath.Join(directory, "tasks", "sway.yml"),
				filepath.Join(directory, "tasks", "install_sway.yml"),
				filepath.Join(directory, "tasks", "linuxcnc_autostart.yml"),
				filepath.Join(directory, "templates", "linuxcnc-autostart.sh.j2"),
				filepath.Join(directory, "templates", "linuxcnc-autostart-sway.conf.j2"),
				filepath.Join(directory, "templates", "sway-config.j2"),
			} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("materialized asset %q: %v", path, err)
				}
			}

			cleanup()
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("cleanup left playbook directory behind: %v", err)
			}
		})
	}
}

func TestMaterializeRejectsUnknownPlaybook(t *testing.T) {
	if _, _, err := Materialize(Playbook("../outside.yml")); err == nil {
		t.Fatal("Materialize() accepted an unknown playbook")
	}
}
