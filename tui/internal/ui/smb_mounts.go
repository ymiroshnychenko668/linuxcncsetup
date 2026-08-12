package ui

import (
	"fmt"
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

const (
	smbShareSource     = "//10.0.1.246/share"
	smbShareMountPoint = "/mnt/smb_share"
)

var smbMountSections = []section{
	{
		title:       "Mount SMB share",
		description: "Configure persistent systemd automounting and mount the share now.",
		action:      actionSMBMount,
	},
	{
		title:       "Unmount SMB share",
		description: "Unmount it for this boot while retaining its persistent configuration.",
		action:      actionSMBUnmount,
	},
	{
		title:       "Remove SMB mount",
		description: "Unmount it and remove only the persistent entry managed by LinuxCNC Setup.",
		action:      actionSMBRemove,
	},
	{
		title:       "← Back",
		description: "Return to Configuration.",
		action:      actionBack,
	},
}

func renderSMBMountAction(action sectionAction, confirming bool) []string {
	if !confirming {
		switch action {
		case actionSMBMount:
			return []string{
				"Press Enter to configure and mount with Ansible.",
				"",
				renderSMBShareLocation(),
			}
		case actionSMBUnmount:
			return []string{
				"Press Enter to unmount the share for this boot.",
				"",
				renderSMBShareLocation(),
				"",
				"The persistent automount entry is retained.",
			}
		case actionSMBRemove:
			return []string{
				"Press Enter to remove the managed mount.",
				"",
				renderSMBShareLocation(),
			}
		default:
			return []string{"This SMB mount action is not implemented."}
		}
	}

	switch action {
	case actionSMBMount:
		return []string{
			warningStyle.Render("Mount the SMB share?"),
			"",
			renderSMBShareLocation(),
			"",
			"Ansible installs cifs-utils if needed,",
			"creates a guest-access CIFS entry in",
			"/etc/fstab, enables systemd automounting,",
			"and mounts the share now.",
			"",
			"It will not write a test file to the share.",
			"sudo will ask for your account password.",
			"Press y to continue or n to cancel.",
		}
	case actionSMBUnmount:
		return []string{
			warningStyle.Render("Unmount the SMB share now?"),
			"",
			renderSMBShareLocation(),
			"",
			"Ansible stops the automount unit first,",
			"then performs a normal unmount. A busy",
			"mount will fail instead of being forced.",
			"",
			"The persistent entry remains and becomes",
			"available again after the next reboot.",
			"Press y to continue or n to cancel.",
		}
	case actionSMBRemove:
		return []string{
			warningStyle.Render("Remove the persistent SMB mount?"),
			"",
			renderSMBShareLocation(),
			"",
			"Ansible stops automounting, performs a",
			"normal unmount, and removes only the fstab",
			"block owned by LinuxCNC Setup.",
			"",
			"Unrelated or legacy fstab entries are",
			"refused and left unchanged.",
			"Press y to continue or n to cancel.",
		}
	default:
		return []string{"This SMB mount action is not implemented."}
	}
}

func renderSMBShareLocation() string {
	return fmt.Sprintf("Share: %s\nMount point: %s", smbShareSource, smbShareMountPoint)
}

func (m *Model) prepareSMBMountAction(action sectionAction) bool {
	if _, ok := smbMountOperation(action); !ok {
		m.status = "This SMB mount action is not implemented."
		return false
	}
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		m.status = "Install Ansible first, then retry this action."
		return false
	}
	if _, err := targetUsername(); err != nil {
		m.status = fmt.Sprintf("Cannot manage SMB mounts: %v", err)
		return false
	}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			m.status = "Cannot manage SMB mounts: sudo was not found."
			return false
		}
	}
	return true
}

func smbMountOperation(action sectionAction) (string, bool) {
	switch action {
	case actionSMBMount:
		return "mount", true
	case actionSMBUnmount:
		return "unmount", true
	case actionSMBRemove:
		return "remove", true
	default:
		return "", false
	}
}

func smbMountPlaybookVariables(targetUser string, action sectionAction) (map[string]any, error) {
	operation, ok := smbMountOperation(action)
	if !ok {
		return nil, fmt.Errorf("unsupported SMB mount action: %d", action)
	}

	return map[string]any{
		"target_user":     targetUser,
		"smb_operation":   operation,
		"smb_source":      smbShareSource,
		"smb_mount_point": smbShareMountPoint,
	}, nil
}

func runSMBMountPlaybook(action sectionAction) tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}

	variables, err := smbMountPlaybookVariables(targetUser, action)
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}

	return runEmbeddedPlaybook(
		action,
		playbooks.SMBMounts,
		variables,
	)
}

func smbMountActionName(action sectionAction) (string, bool) {
	switch action {
	case actionSMBMount:
		return "SMB share mount", true
	case actionSMBUnmount:
		return "SMB share unmount", true
	case actionSMBRemove:
		return "SMB mount removal", true
	default:
		return "", false
	}
}

func smbMountRunningMessage(action sectionAction) (string, bool) {
	switch action {
	case actionSMBMount:
		return "Configuring and mounting the SMB share with Ansible...", true
	case actionSMBUnmount:
		return "Unmounting the SMB share with Ansible...", true
	case actionSMBRemove:
		return "Removing the managed SMB mount with Ansible...", true
	default:
		return "", false
	}
}

func smbMountCancelledMessage(action sectionAction) (string, bool) {
	switch action {
	case actionSMBMount:
		return "SMB share mount cancelled.", true
	case actionSMBUnmount:
		return "SMB share unmount cancelled.", true
	case actionSMBRemove:
		return "SMB mount removal cancelled.", true
	default:
		return "", false
	}
}

func smbMountSuccessMessage(action sectionAction) (string, bool) {
	switch action {
	case actionSMBMount:
		return "SMB share mounted and configured for automatic mounting.", true
	case actionSMBUnmount:
		return "SMB share unmounted for this boot; its persistent configuration remains.", true
	case actionSMBRemove:
		return "Managed SMB mount unmounted and removed from persistent configuration.", true
	default:
		return "", false
	}
}
