package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

const (
	corvusCNCRepository         = "git@github.com:ymiroshnychenko668/corvuscnc.git"
	corvusCNCInstallDestination = "~/linuxcnc/configs/corvuscnc"
)

func renderLinuxCNCConfigInstall(confirming bool) []string {
	if !confirming {
		return []string{"Press Enter to install this configuration with Ansible."}
	}

	return []string{
		warningStyle.Render("Install CorvusCNC configuration?"),
		"",
		"Ansible installs Git and OpenSSH if needed,",
		"then clones as your regular user from:",
		"  " + corvusCNCRepository,
		"into:",
		"  " + corvusCNCInstallDestination,
		"",
		"GitHub must authorize your user's SSH key.",
		"An existing checkout is verified and left",
		"unchanged. LinuxCNC will not be started.",
		"",
		"sudo will ask for your account password.",
		"Press y to continue or n to cancel.",
	}
}

func (m *Model) prepareLinuxCNCConfigInstall() bool {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		m.status = "Install Ansible first, then retry this action."
		return false
	}
	if _, err := targetUsername(); err != nil {
		m.status = fmt.Sprintf("Cannot install the CorvusCNC configuration: %v", err)
		return false
	}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			m.status = "Cannot install the CorvusCNC configuration: sudo was not found."
			return false
		}
	}
	return true
}

func linuxCNCConfigInstallPlaybookVariables(targetUser string) map[string]any {
	variables := map[string]any{"target_user": targetUser}
	if sshAuthSock := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); sshAuthSock != "" {
		variables["corvus_ssh_auth_sock"] = sshAuthSock
	}
	return variables
}

func runLinuxCNCConfigInstallPlaybook() tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: actionInstallLinuxCNCConfig, err: err}
		}
	}

	return runEmbeddedPlaybook(
		actionInstallLinuxCNCConfig,
		playbooks.InstallLinuxCNCConfig,
		linuxCNCConfigInstallPlaybookVariables(targetUser),
	)
}
