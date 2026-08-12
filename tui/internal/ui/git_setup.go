package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

var gitSetupSections = []section{
	{
		title:       "Install Git tools",
		description: "Install Git, OpenSSH client, and GitHub CLI without creating SSH keys.",
		action:      actionInstallGitTools,
	},
	{
		title:       "Sign in to GitHub with gh",
		description: "Authenticate interactively and let GitHub CLI manage the SSH-key choice.",
		action:      actionGitHubLogin,
	},
	{
		title:       "← Back",
		description: "Return to the main menu.",
		action:      actionBack,
	},
}

func renderGitSetupAction(action sectionAction, confirming bool) []string {
	if !confirming {
		switch action {
		case actionInstallGitTools:
			return []string{"Press Enter to install these tools with Ansible."}
		case actionGitHubLogin:
			return []string{"Press Enter to start the interactive GitHub login."}
		default:
			return []string{"This Git setup action is not implemented."}
		}
	}

	switch action {
	case actionInstallGitTools:
		return []string{
			warningStyle.Render("Install Git tools?"),
			"",
			"Ansible installs Git, OpenSSH client,",
			"and GitHub CLI from Debian repositories.",
			"Sets global Git to cnc <cnc@cnc.cn>.",
			"",
			"It does not create, replace, or upload",
			"any SSH key. Sign-in is a separate action.",
			"",
			"sudo will ask for your account password.",
			"Press y to continue or n to cancel.",
		}
	case actionGitHubLogin:
		return []string{
			warningStyle.Render("Sign in to GitHub with gh?"),
			"",
			"Runs GitHub CLI's interactive web login",
			"for github.com using the SSH Git protocol.",
			"gh stores an OAuth token and may offer to",
			"select, create, or upload an SSH key.",
			"",
			"Ansible does not manage the key. Review",
			"and approve every choice in the gh prompts.",
			"Run the TUI without sudo for this action.",
			"",
			"Press y to continue or n to cancel.",
		}
	default:
		return []string{"This Git setup action is not implemented."}
	}
}

func (m *Model) prepareGitToolsInstall() bool {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		m.status = "Install Ansible first, then retry this action."
		return false
	}
	if _, err := targetUsername(); err != nil {
		m.status = fmt.Sprintf("Cannot install Git tools: %v", err)
		return false
	}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			m.status = "Cannot install Git tools: sudo was not found."
			return false
		}
	}
	return true
}

func (m *Model) prepareGitHubLogin() bool {
	if os.Geteuid() == 0 {
		m.status = "Run LinuxCNC Setup without sudo to sign in to GitHub."
		return false
	}
	for _, command := range []string{"gh", "git", "ssh"} {
		if _, err := exec.LookPath(command); err != nil {
			m.status = "Install Git tools first, then retry GitHub sign-in."
			return false
		}
	}
	return true
}

func runGitToolsInstallPlaybook() tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: actionInstallGitTools, err: err}
		}
	}

	return runEmbeddedPlaybook(
		actionInstallGitTools,
		playbooks.InstallGit,
		map[string]any{"target_user": targetUser},
	)
}

func runGitHubLogin() tea.Cmd {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{
				action: actionGitHubLogin,
				err:    fmt.Errorf("find gh: %w", err),
			}
		}
	}

	sequence := &commandSequence{
		commands: githubLoginCommandSpecs(ghPath, os.Environ()),
	}
	return tea.Exec(sequence, func(err error) tea.Msg {
		return actionFinishedMsg{action: actionGitHubLogin, err: err}
	})
}

func githubLoginCommandSpecs(ghPath string, environment []string) []commandSpec {
	safeEnvironment := githubLoginEnvironment(environment)
	return []commandSpec{
		{
			name: ghPath,
			args: []string{
				"auth",
				"login",
				"--hostname",
				"github.com",
				"--git-protocol",
				"ssh",
				"--web",
			},
			env: safeEnvironment,
		},
		{
			name: ghPath,
			args: []string{
				"auth",
				"status",
				"--hostname",
				"github.com",
			},
			env: safeEnvironment,
		},
	}
}

func githubLoginEnvironment(environment []string) []string {
	blocked := map[string]bool{
		"GH_CONFIG_DIR":           true,
		"GH_ENTERPRISE_TOKEN":     true,
		"GH_HOST":                 true,
		"GH_PROMPT_DISABLED":      true,
		"GH_TOKEN":                true,
		"GITHUB_ENTERPRISE_TOKEN": true,
		"GITHUB_TOKEN":            true,
	}

	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && blocked[key] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
