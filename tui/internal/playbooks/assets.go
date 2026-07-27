// Package playbooks embeds the Ansible assets required by the installed TUI.
package playbooks

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed *.yml tasks/*.yml templates/*
var assets embed.FS

// Playbook identifies an embedded entrypoint.
type Playbook string

const (
	Autologin             Playbook = "autologin.yml"
	InstallLinuxCNCConfig Playbook = "install-linuxcnc-config.yml"
	IRQAffinity           Playbook = "irq-affinity.yml"
	GRUBRealtime          Playbook = "grub-realtime.yml"
	InstallDevTools       Playbook = "install-devtools.yml"
	InstallSway           Playbook = "install-sway.yml"
	LinuxCNCAutostart     Playbook = "linuxcnc-autostart.yml"
)

// Materialize writes the embedded playbook tree to a temporary directory.
// The caller must invoke cleanup after ansible-playbook exits.
func Materialize(playbook Playbook) (playbookPath string, cleanup func(), err error) {
	switch playbook {
	case Autologin, InstallLinuxCNCConfig, IRQAffinity, GRUBRealtime, InstallDevTools,
		InstallSway, LinuxCNCAutostart:
	default:
		return "", nil, fmt.Errorf("unknown embedded playbook: %q", playbook)
	}

	directory, err := os.MkdirTemp("", "linuxcncsetup-playbooks-*")
	if err != nil {
		return "", nil, fmt.Errorf("create playbook directory: %w", err)
	}

	cleanup = func() {
		_ = os.RemoveAll(directory)
	}

	if err := os.CopyFS(directory, assets); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write embedded playbooks: %w", err)
	}

	return filepath.Join(directory, string(playbook)), cleanup, nil
}
