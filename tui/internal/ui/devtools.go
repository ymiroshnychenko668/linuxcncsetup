package ui

import (
	"fmt"
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

var devToolsSections = []section{
	{
		title:       "Install all",
		description: "Install and configure every development-tools component.",
		action:      actionInstallDevToolsAll,
	},
	{
		title:       "Git & GitHub SSH",
		description: "Install Git, set the cnc identity, and prepare an Ed25519 key for GitHub.",
		action:      actionInstallDevToolsGit,
	},
	{
		title:       "Visual Studio Code",
		description: "Add Microsoft's signed amd64 repository and install Visual Studio Code.",
		action:      actionInstallDevToolsVSCode,
	},
	{
		title:       "Codex CLI",
		description: "Install Codex CLI for this user with OpenAI's official installer.",
		action:      actionInstallDevToolsCodex,
	},
	{
		title:       "Claude Code",
		description: "Install Claude Code for this user with Anthropic's official installer.",
		action:      actionInstallDevToolsClaude,
	},
	{
		title:       "Warp Terminal",
		description: "Install Warp's official Debian package without refreshing APT indexes.",
		action:      actionInstallDevToolsWarp,
	},
	{
		title:       "htop",
		description: "Install the interactive process viewer from Debian's repositories.",
		action:      actionInstallDevToolsHtop,
	},
	{
		title:       "Midnight Commander",
		description: "Install the terminal file manager from Debian's repositories.",
		action:      actionInstallDevToolsMC,
	},
	{
		title:       "Terminator",
		description: "Install the graphical terminal emulator from Debian's repositories.",
		action:      actionInstallDevToolsTerminator,
	},
	{
		title:       "User lingering",
		description: "Allow this user's services to continue running after logout.",
		action:      actionEnableUserLinger,
	},
	{
		title:       "← Back",
		description: "Return to the main menu.",
		action:      actionBack,
	},
}

func devToolsComponent(action sectionAction) (string, bool) {
	switch action {
	case actionInstallDevToolsAll:
		return "all", true
	case actionInstallDevToolsGit:
		return "git", true
	case actionInstallDevToolsVSCode:
		return "vscode", true
	case actionInstallDevToolsCodex:
		return "codex", true
	case actionInstallDevToolsClaude:
		return "claude", true
	case actionInstallDevToolsWarp:
		return "warp", true
	case actionInstallDevToolsHtop:
		return "htop", true
	case actionInstallDevToolsMC:
		return "mc", true
	case actionInstallDevToolsTerminator:
		return "terminator", true
	case actionEnableUserLinger:
		return "linger", true
	default:
		return "", false
	}
}

func devToolsActionName(action sectionAction) (string, bool) {
	switch action {
	case actionInstallDevToolsAll:
		return "Developer tools installation", true
	case actionInstallDevToolsGit:
		return "Git and GitHub SSH setup", true
	case actionInstallDevToolsVSCode:
		return "Visual Studio Code installation", true
	case actionInstallDevToolsCodex:
		return "Codex CLI installation", true
	case actionInstallDevToolsClaude:
		return "Claude Code installation", true
	case actionInstallDevToolsWarp:
		return "Warp Terminal installation", true
	case actionInstallDevToolsHtop:
		return "htop installation", true
	case actionInstallDevToolsMC:
		return "Midnight Commander installation", true
	case actionInstallDevToolsTerminator:
		return "Terminator installation", true
	case actionEnableUserLinger:
		return "User lingering configuration", true
	default:
		return "", false
	}
}

func devToolsRunningMessage(action sectionAction) (string, bool) {
	switch action {
	case actionInstallDevToolsAll:
		return "Installing all developer tools...", true
	case actionInstallDevToolsGit:
		return "Installing Git and configuring GitHub SSH...", true
	case actionInstallDevToolsVSCode:
		return "Installing Visual Studio Code...", true
	case actionInstallDevToolsCodex:
		return "Installing Codex CLI...", true
	case actionInstallDevToolsClaude:
		return "Installing Claude Code...", true
	case actionInstallDevToolsWarp:
		return "Installing Warp Terminal...", true
	case actionInstallDevToolsHtop:
		return "Installing htop...", true
	case actionInstallDevToolsMC:
		return "Installing Midnight Commander...", true
	case actionInstallDevToolsTerminator:
		return "Installing Terminator...", true
	case actionEnableUserLinger:
		return "Enabling user lingering...", true
	default:
		return "", false
	}
}

func devToolsCancelledMessage(action sectionAction) (string, bool) {
	name, ok := devToolsActionName(action)
	if !ok {
		return "", false
	}
	return name + " cancelled.", true
}

func devToolsSuccessMessage(action sectionAction) (string, bool) {
	switch action {
	case actionInstallDevToolsAll:
		return "All developer tools installed successfully.", true
	case actionInstallDevToolsGit:
		return "Git and GitHub SSH configured successfully.", true
	case actionInstallDevToolsVSCode:
		return "Visual Studio Code installed successfully.", true
	case actionInstallDevToolsCodex:
		return "Codex CLI installed successfully.", true
	case actionInstallDevToolsClaude:
		return "Claude Code installed successfully.", true
	case actionInstallDevToolsWarp:
		return "Warp Terminal installed successfully.", true
	case actionInstallDevToolsHtop:
		return "htop installed successfully.", true
	case actionInstallDevToolsMC:
		return "Midnight Commander installed successfully.", true
	case actionInstallDevToolsTerminator:
		return "Terminator installed successfully.", true
	case actionEnableUserLinger:
		return "User lingering enabled successfully.", true
	default:
		return "", false
	}
}

func renderDevToolsAction(action sectionAction, confirming bool) []string {
	if !confirming {
		return []string{"Press Enter to run this component with Ansible."}
	}

	var lines []string
	switch action {
	case actionInstallDevToolsAll:
		lines = []string{
			warningStyle.Render("Install all developer tools?"),
			"",
			"Installs Git, VS Code, Codex CLI,",
			"Claude Code, Warp Terminal, htop,",
			"mc, Terminator, and supporting packages.",
			"Configures cnc <cnc@cnc.cn>, GitHub",
			"SSH, signed APT repositories, official",
			"per-user installers, and user lingering.",
			"Codex and Claude request sign-in on first run.",
		}
	case actionInstallDevToolsGit:
		lines = []string{
			warningStyle.Render("Install Git and configure GitHub SSH?"),
			"",
			"Installs Git and the OpenSSH client.",
			"Sets global Git to cnc <cnc@cnc.cn>.",
			"Creates an Ed25519 key if needed and",
			"shows its public key for GitHub.",
			"An existing private key is not replaced.",
		}
	case actionInstallDevToolsVSCode:
		lines = []string{
			warningStyle.Render("Install Visual Studio Code?"),
			"",
			"Adds Microsoft's signed amd64 APT",
			"repository and installs VS Code.",
			"This component requires x86-64.",
		}
	case actionInstallDevToolsCodex:
		lines = []string{
			warningStyle.Render("Install Codex CLI?"),
			"",
			"Runs OpenAI's official per-user installer.",
			"Codex requests sign-in on first run.",
		}
	case actionInstallDevToolsClaude:
		lines = []string{
			warningStyle.Render("Install Claude Code?"),
			"",
			"Runs Anthropic's official per-user installer.",
			"Claude Code requests sign-in on first run.",
		}
	case actionInstallDevToolsWarp:
		lines = []string{
			warningStyle.Render("Install Warp Terminal?"),
			"",
			"Downloads Warp's official native .deb and",
			"installs it without refreshing APT indexes.",
			"The package configures signed updates.",
			"Supports x86-64 and ARM64.",
		}
	case actionInstallDevToolsHtop:
		lines = []string{
			warningStyle.Render("Install htop?"),
			"",
			"Installs htop from Debian's repositories.",
		}
	case actionInstallDevToolsMC:
		lines = []string{
			warningStyle.Render("Install Midnight Commander?"),
			"",
			"Installs mc from Debian's repositories.",
		}
	case actionInstallDevToolsTerminator:
		lines = []string{
			warningStyle.Render("Install Terminator?"),
			"",
			"Installs Terminator from Debian's repositories.",
		}
	case actionEnableUserLinger:
		lines = []string{
			warningStyle.Render("Enable user lingering?"),
			"",
			"Runs loginctl enable-linger for your user.",
			"User services may continue after logout.",
			"No developer package is installed.",
		}
	default:
		return []string{"This developer-tools action is not implemented."}
	}

	return append(lines,
		"",
		"sudo will ask for your account password.",
		"Press y to continue or n to cancel.",
	)
}

func (m *Model) prepareDevToolsInstall() bool {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		m.status = "Install Ansible first, then retry this action."
		return false
	}
	if _, err := targetUsername(); err != nil {
		m.status = fmt.Sprintf("Cannot configure developer tools: %v", err)
		return false
	}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			m.status = "Cannot configure developer tools: sudo was not found."
			return false
		}
	}
	return true
}

func devToolsPlaybookVariables(targetUser string, action sectionAction) (map[string]any, error) {
	component, ok := devToolsComponent(action)
	if !ok {
		return nil, fmt.Errorf("unknown developer-tools action: %d", action)
	}
	return map[string]any{
		"devtools_component": component,
		"target_user":        targetUser,
	}, nil
}

func runDevToolsInstallPlaybook(action sectionAction) tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}

	variables, err := devToolsPlaybookVariables(targetUser, action)
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}

	return runEmbeddedPlaybook(action, playbooks.InstallDevTools, variables)
}
