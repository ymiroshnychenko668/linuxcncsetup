package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestDevToolsMainMenuPlacement(t *testing.T) {
	swayIndex := mainMenuActionIndex(t, actionInstallSway)
	devToolsIndex := mainMenuActionIndex(t, actionOpenDevTools)
	autostartIndex := mainMenuActionIndex(t, actionOpenLinuxCNCAutostart)

	if mainSections[devToolsIndex].title != "Development tools" {
		t.Fatalf(
			"developer-tools menu title is %q; want %q",
			mainSections[devToolsIndex].title,
			"Development tools",
		)
	}
	if !(swayIndex < devToolsIndex && devToolsIndex < autostartIndex) {
		t.Fatalf(
			"developer-tools menu must be between Install Sway and LinuxCNC autostart; indexes are sway=%d devtools=%d autostart=%d",
			swayIndex,
			devToolsIndex,
			autostartIndex,
		)
	}
}

func TestDevToolsSubmenuStructure(t *testing.T) {
	expected := []sectionAction{
		actionInstallDevToolsAll,
		actionInstallDevToolsGit,
		actionInstallDevToolsVSCode,
		actionInstallDevToolsCodex,
		actionInstallDevToolsClaude,
		actionInstallDevToolsWarp,
		actionInstallDevToolsHtop,
		actionInstallDevToolsMC,
		actionInstallDevToolsTerminator,
		actionEnableUserLinger,
		actionBack,
	}

	if len(devToolsSections) != len(expected) {
		t.Fatalf(
			"developer-tools submenu has %d entries; want %d",
			len(devToolsSections),
			len(expected),
		)
	}
	for index, action := range expected {
		if devToolsSections[index].action != action {
			t.Errorf(
				"developer-tools submenu action %d is %d; want %d",
				index,
				devToolsSections[index].action,
				action,
			)
		}
	}
}

func TestNewDevToolsSubmenuEntries(t *testing.T) {
	tests := []struct {
		action      sectionAction
		title       string
		description []string
	}{
		{
			action:      actionInstallDevToolsCodex,
			title:       "Codex CLI",
			description: []string{"OpenAI", "official installer"},
		},
		{
			action:      actionInstallDevToolsClaude,
			title:       "Claude Code",
			description: []string{"Anthropic", "official installer"},
		},
		{
			action:      actionInstallDevToolsWarp,
			title:       "Warp Terminal",
			description: []string{"official Debian package", "without refreshing APT"},
		},
	}

	vscodeIndex := sectionActionIndex(t, devToolsSections, actionInstallDevToolsVSCode)
	for offset, test := range tests {
		index := sectionActionIndex(t, devToolsSections, test.action)
		if index != vscodeIndex+offset+1 {
			t.Errorf(
				"%s is at index %d; want index %d immediately after Visual Studio Code",
				test.title,
				index,
				vscodeIndex+offset+1,
			)
		}

		entry := devToolsSections[index]
		if entry.title != test.title {
			t.Errorf("menu title is %q; want %q", entry.title, test.title)
		}
		for _, expected := range test.description {
			if !strings.Contains(entry.description, expected) {
				t.Errorf(
					"%s description does not contain %q: %q",
					test.title,
					expected,
					entry.description,
				)
			}
		}
	}
}

func TestEnterAndLeaveDevToolsSubmenu(t *testing.T) {
	mainIndex := mainMenuActionIndex(t, actionOpenDevTools)
	model := New()
	model.selected = mainIndex
	model.prepareSelectedAction()

	if model.page != menuDevTools || model.selected != 0 {
		t.Fatalf(
			"entering developer-tools submenu produced page %d selection %d",
			model.page,
			model.selected,
		)
	}
	if model.confirming {
		t.Fatal("opening the developer-tools submenu should not request confirmation")
	}
	if model.pageTitle() != "Development tools" {
		t.Fatalf("developer-tools page title is %q", model.pageTitle())
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving the developer-tools submenu should not execute a command")
	}

	result := updated.(Model)
	if result.page != menuMain || result.selected != mainIndex {
		t.Fatalf(
			"Esc returned to page %d selection %d; want main page selection %d",
			result.page,
			result.selected,
			mainIndex,
		)
	}
}

func TestDevToolsBackItemReturnsToMainMenu(t *testing.T) {
	mainIndex := mainMenuActionIndex(t, actionOpenDevTools)
	model := New()
	model.page = menuDevTools
	model.selected = sectionActionIndex(t, devToolsSections, actionBack)

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("selecting Back should not execute a command")
	}

	result := updated.(Model)
	if result.page != menuMain || result.selected != mainIndex {
		t.Fatalf(
			"Back returned to page %d selection %d; want main page selection %d",
			result.page,
			result.selected,
			mainIndex,
		)
	}
}

func TestDevToolsConfirmationViewsDescribeSelectedComponent(t *testing.T) {
	tests := []struct {
		name     string
		action   sectionAction
		expected []string
	}{
		{
			name:   "all",
			action: actionInstallDevToolsAll,
			expected: []string{
				"Install all developer tools?",
				"Git",
				"VS Code",
				"Codex CLI",
				"Claude Code",
				"Warp Terminal",
				"htop",
				"mc",
				"Terminator",
				"user lingering",
				"sign-in on first run",
			},
		},
		{
			name:   "git",
			action: actionInstallDevToolsGit,
			expected: []string{
				"Install Git and configure GitHub SSH?",
				"cnc <cnc@cnc.cn>",
				"Ed25519",
				"GitHub",
				"existing private key is not replaced",
			},
		},
		{
			name:   "vscode",
			action: actionInstallDevToolsVSCode,
			expected: []string{
				"Install Visual Studio Code?",
				"Microsoft",
				"signed amd64 APT",
				"repository and installs VS Code",
				"requires x86-64",
			},
		},
		{
			name:   "codex",
			action: actionInstallDevToolsCodex,
			expected: []string{
				"Install Codex CLI?",
				"OpenAI",
				"official per-user installer",
				"sign-in on first run",
			},
		},
		{
			name:   "claude",
			action: actionInstallDevToolsClaude,
			expected: []string{
				"Install Claude Code?",
				"Anthropic",
				"official per-user installer",
				"sign-in on first run",
			},
		},
		{
			name:   "warp",
			action: actionInstallDevToolsWarp,
			expected: []string{
				"Install Warp Terminal?",
				"official native .deb",
				"without refreshing APT indexes",
				"configures signed updates",
				"x86-64 and ARM64",
			},
		},
		{
			name:     "htop",
			action:   actionInstallDevToolsHtop,
			expected: []string{"Install htop?"},
		},
		{
			name:     "midnight commander",
			action:   actionInstallDevToolsMC,
			expected: []string{"Install Midnight Commander?"},
		},
		{
			name:     "terminator",
			action:   actionInstallDevToolsTerminator,
			expected: []string{"Install Terminator?"},
		},
		{
			name:   "user lingering",
			action: actionEnableUserLinger,
			expected: []string{
				"Enable user lingering?",
				"loginctl enable-linger",
				"User services may continue after logout",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := New()
			model.page = menuDevTools
			model.selected = sectionActionIndex(t, devToolsSections, test.action)
			model.confirming = true

			content := model.renderDetail()
			for _, expected := range test.expected {
				if !strings.Contains(content, expected) {
					t.Errorf(
						"%s confirmation does not contain %q:\n%s",
						test.name,
						expected,
						content,
					)
				}
			}
		})
	}
}

func TestDevToolsComponentMapping(t *testing.T) {
	tests := []struct {
		action    sectionAction
		component string
	}{
		{action: actionInstallDevToolsAll, component: "all"},
		{action: actionInstallDevToolsGit, component: "git"},
		{action: actionInstallDevToolsVSCode, component: "vscode"},
		{action: actionInstallDevToolsCodex, component: "codex"},
		{action: actionInstallDevToolsClaude, component: "claude"},
		{action: actionInstallDevToolsWarp, component: "warp"},
		{action: actionInstallDevToolsHtop, component: "htop"},
		{action: actionInstallDevToolsMC, component: "mc"},
		{action: actionInstallDevToolsTerminator, component: "terminator"},
		{action: actionEnableUserLinger, component: "linger"},
	}

	for _, test := range tests {
		t.Run(test.component, func(t *testing.T) {
			component, ok := devToolsComponent(test.action)
			if !ok {
				t.Fatalf("action %d has no developer-tools component", test.action)
			}
			if component != test.component {
				t.Fatalf(
					"action %d maps to component %q; want %q",
					test.action,
					component,
					test.component,
				)
			}
		})
	}

	for _, action := range []sectionAction{actionNone, actionOpenDevTools, actionBack} {
		if component, ok := devToolsComponent(action); ok || component != "" {
			t.Errorf(
				"non-component action %d maps to component %q with ok=%t",
				action,
				component,
				ok,
			)
		}
	}
}

func TestDevToolsPlaybookVariables(t *testing.T) {
	tests := []struct {
		action    sectionAction
		component string
	}{
		{action: actionInstallDevToolsAll, component: "all"},
		{action: actionInstallDevToolsGit, component: "git"},
		{action: actionInstallDevToolsVSCode, component: "vscode"},
		{action: actionInstallDevToolsCodex, component: "codex"},
		{action: actionInstallDevToolsClaude, component: "claude"},
		{action: actionInstallDevToolsWarp, component: "warp"},
		{action: actionInstallDevToolsHtop, component: "htop"},
		{action: actionInstallDevToolsMC, component: "mc"},
		{action: actionInstallDevToolsTerminator, component: "terminator"},
		{action: actionEnableUserLinger, component: "linger"},
	}

	for _, test := range tests {
		t.Run(test.component, func(t *testing.T) {
			variables, err := devToolsPlaybookVariables("operator", test.action)
			if err != nil {
				t.Fatalf("devToolsPlaybookVariables() error: %v", err)
			}
			if len(variables) != 2 {
				t.Fatalf("playbook variables = %#v; want exactly two values", variables)
			}
			if got := variables["target_user"]; got != "operator" {
				t.Errorf("target_user = %#v; want %q", got, "operator")
			}
			if got := variables["devtools_component"]; got != test.component {
				t.Errorf("devtools_component = %#v; want %q", got, test.component)
			}
		})
	}

	if variables, err := devToolsPlaybookVariables("operator", actionOpenDevTools); err == nil {
		t.Fatalf("opener action produced variables %#v; want an error", variables)
	}
}

func TestDevToolsCancellationNamesSelectedAction(t *testing.T) {
	tests := []struct {
		action sectionAction
		status string
	}{
		{
			action: actionInstallDevToolsAll,
			status: "Developer tools installation cancelled.",
		},
		{
			action: actionInstallDevToolsGit,
			status: "Git and GitHub SSH setup cancelled.",
		},
		{
			action: actionInstallDevToolsVSCode,
			status: "Visual Studio Code installation cancelled.",
		},
		{
			action: actionInstallDevToolsCodex,
			status: "Codex CLI installation cancelled.",
		},
		{
			action: actionInstallDevToolsClaude,
			status: "Claude Code installation cancelled.",
		},
		{
			action: actionInstallDevToolsWarp,
			status: "Warp Terminal installation cancelled.",
		},
		{
			action: actionInstallDevToolsHtop,
			status: "htop installation cancelled.",
		},
		{
			action: actionInstallDevToolsMC,
			status: "Midnight Commander installation cancelled.",
		},
		{
			action: actionInstallDevToolsTerminator,
			status: "Terminator installation cancelled.",
		},
		{
			action: actionEnableUserLinger,
			status: "User lingering configuration cancelled.",
		},
	}

	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			model := New()
			model.page = menuDevTools
			model.selected = sectionActionIndex(t, devToolsSections, test.action)
			model.confirming = true

			updated, command := model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
			if command != nil {
				t.Fatal("cancelling should not execute a command")
			}

			result := updated.(Model)
			if result.confirming {
				t.Fatal("cancelling should close the confirmation")
			}
			if result.status != test.status {
				t.Fatalf("cancellation status is %q; want %q", result.status, test.status)
			}
		})
	}
}

func TestDevToolsActionMessages(t *testing.T) {
	tests := []struct {
		name       string
		action     sectionAction
		actionName string
		running    string
		cancelled  string
		success    string
	}{
		{
			name:       "all",
			action:     actionInstallDevToolsAll,
			actionName: "Developer tools installation",
			running:    "Installing all developer tools...",
			cancelled:  "Developer tools installation cancelled.",
			success:    "All developer tools installed successfully.",
		},
		{
			name:       "git",
			action:     actionInstallDevToolsGit,
			actionName: "Git and GitHub SSH setup",
			running:    "Installing Git and configuring GitHub SSH...",
			cancelled:  "Git and GitHub SSH setup cancelled.",
			success:    "Git and GitHub SSH configured successfully.",
		},
		{
			name:       "vscode",
			action:     actionInstallDevToolsVSCode,
			actionName: "Visual Studio Code installation",
			running:    "Installing Visual Studio Code...",
			cancelled:  "Visual Studio Code installation cancelled.",
			success:    "Visual Studio Code installed successfully.",
		},
		{
			name:       "codex",
			action:     actionInstallDevToolsCodex,
			actionName: "Codex CLI installation",
			running:    "Installing Codex CLI...",
			cancelled:  "Codex CLI installation cancelled.",
			success:    "Codex CLI installed successfully.",
		},
		{
			name:       "claude",
			action:     actionInstallDevToolsClaude,
			actionName: "Claude Code installation",
			running:    "Installing Claude Code...",
			cancelled:  "Claude Code installation cancelled.",
			success:    "Claude Code installed successfully.",
		},
		{
			name:       "warp",
			action:     actionInstallDevToolsWarp,
			actionName: "Warp Terminal installation",
			running:    "Installing Warp Terminal...",
			cancelled:  "Warp Terminal installation cancelled.",
			success:    "Warp Terminal installed successfully.",
		},
		{
			name:       "htop",
			action:     actionInstallDevToolsHtop,
			actionName: "htop installation",
			running:    "Installing htop...",
			cancelled:  "htop installation cancelled.",
			success:    "htop installed successfully.",
		},
		{
			name:       "midnight commander",
			action:     actionInstallDevToolsMC,
			actionName: "Midnight Commander installation",
			running:    "Installing Midnight Commander...",
			cancelled:  "Midnight Commander installation cancelled.",
			success:    "Midnight Commander installed successfully.",
		},
		{
			name:       "terminator",
			action:     actionInstallDevToolsTerminator,
			actionName: "Terminator installation",
			running:    "Installing Terminator...",
			cancelled:  "Terminator installation cancelled.",
			success:    "Terminator installed successfully.",
		},
		{
			name:       "user lingering",
			action:     actionEnableUserLinger,
			actionName: "User lingering configuration",
			running:    "Enabling user lingering...",
			cancelled:  "User lingering configuration cancelled.",
			success:    "User lingering enabled successfully.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := actionName(test.action); got != test.actionName {
				t.Errorf("actionName() = %q; want %q", got, test.actionName)
			}
			if got := actionRunningMessage(test.action); got != test.running {
				t.Errorf("actionRunningMessage() = %q; want %q", got, test.running)
			}
			if got := actionCancelledMessage(test.action); got != test.cancelled {
				t.Errorf("actionCancelledMessage() = %q; want %q", got, test.cancelled)
			}
			if got := actionSuccessMessage(test.action); got != test.success {
				t.Errorf("actionSuccessMessage() = %q; want %q", got, test.success)
			}
		})
	}
}

func mainMenuActionIndex(t *testing.T, action sectionAction) int {
	t.Helper()
	return sectionActionIndex(t, mainSections, action)
}

func sectionActionIndex(t *testing.T, sections []section, action sectionAction) int {
	t.Helper()

	index := -1
	for candidateIndex, candidate := range sections {
		if candidate.action != action {
			continue
		}
		if index >= 0 {
			t.Fatalf("menu contains action %d more than once", action)
		}
		index = candidateIndex
	}
	if index < 0 {
		t.Fatalf("menu does not contain action %d", action)
	}
	return index
}
