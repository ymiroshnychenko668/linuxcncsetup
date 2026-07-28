package ui

import (
	"os"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestGitSetupMainMenuPlacement(t *testing.T) {
	ansibleIndex := mainMenuActionIndex(t, actionInstallAnsible)
	gitSetupIndex := mainMenuActionIndex(t, actionOpenGitSetup)

	if gitSetupIndex != ansibleIndex+1 {
		t.Fatalf(
			"Git setup is at index %d; want %d immediately after Ansible",
			gitSetupIndex,
			ansibleIndex+1,
		)
	}
	if mainSections[gitSetupIndex].title != "Git setup" {
		t.Fatalf("Git setup menu title is %q", mainSections[gitSetupIndex].title)
	}
}

func TestGitSetupSubmenuStructure(t *testing.T) {
	expected := []sectionAction{
		actionInstallGitTools,
		actionGitHubLogin,
		actionBack,
	}
	if len(gitSetupSections) != len(expected) {
		t.Fatalf(
			"Git setup submenu has %d entries; want %d",
			len(gitSetupSections),
			len(expected),
		)
	}
	for index, action := range expected {
		if gitSetupSections[index].action != action {
			t.Errorf(
				"Git setup action %d is %d; want %d",
				index,
				gitSetupSections[index].action,
				action,
			)
		}
	}
}

func TestEnterAndLeaveGitSetupSubmenu(t *testing.T) {
	mainIndex := mainMenuActionIndex(t, actionOpenGitSetup)
	model := New()
	model.selected = mainIndex
	model.prepareSelectedAction()

	if model.page != menuGitSetup || model.selected != 0 {
		t.Fatalf(
			"entering Git setup produced page %d selection %d",
			model.page,
			model.selected,
		)
	}
	if model.confirming {
		t.Fatal("opening Git setup should not request confirmation")
	}
	if model.pageTitle() != "Git setup" {
		t.Fatalf("Git setup page title is %q", model.pageTitle())
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving Git setup should not execute a command")
	}
	result := updated.(Model)
	if result.page != menuMain || result.selected != mainIndex {
		t.Fatalf(
			"Esc returned to page %d selection %d; want main selection %d",
			result.page,
			result.selected,
			mainIndex,
		)
	}
}

func TestGitSetupBackItemReturnsToMainMenu(t *testing.T) {
	mainIndex := mainMenuActionIndex(t, actionOpenGitSetup)
	model := New()
	model.page = menuGitSetup
	model.selected = sectionActionIndex(t, gitSetupSections, actionBack)

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("selecting Back should not execute a command")
	}
	result := updated.(Model)
	if result.page != menuMain || result.selected != mainIndex {
		t.Fatalf(
			"Back returned to page %d selection %d; want main selection %d",
			result.page,
			result.selected,
			mainIndex,
		)
	}
}

func TestGitSetupConfirmationViews(t *testing.T) {
	tests := []struct {
		name     string
		action   sectionAction
		expected []string
	}{
		{
			name:   "install tools",
			action: actionInstallGitTools,
			expected: []string{
				"Install Git tools?",
				"Git, OpenSSH client",
				"GitHub CLI",
				"cnc <cnc@cnc.cn>",
				"does not create, replace, or upload",
				"Sign-in is a separate action",
			},
		},
		{
			name:   "GitHub login",
			action: actionGitHubLogin,
			expected: []string{
				"Sign in to GitHub with gh?",
				"interactive web login",
				"SSH Git protocol",
				"OAuth token",
				"may offer to",
				"Ansible does not manage the key",
				"without sudo",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := New()
			model.page = menuGitSetup
			model.selected = sectionActionIndex(t, gitSetupSections, test.action)
			model.confirming = true
			content := model.renderDetail()
			for _, expected := range test.expected {
				if !strings.Contains(content, expected) {
					t.Errorf("confirmation does not contain %q:\n%s", expected, content)
				}
			}
		})
	}
}

func TestGitSetupActionMessages(t *testing.T) {
	tests := []struct {
		action     sectionAction
		actionName string
		running    string
		cancelled  string
		success    string
	}{
		{
			action:     actionInstallGitTools,
			actionName: "Git tools installation",
			running:    "Installing Git, OpenSSH client, and GitHub CLI...",
			cancelled:  "Git tools installation cancelled.",
			success:    "Git tools installed. Use GitHub sign-in to authenticate with gh.",
		},
		{
			action:     actionGitHubLogin,
			actionName: "GitHub CLI sign-in",
			running:    "Starting the interactive GitHub CLI sign-in...",
			cancelled:  "GitHub CLI sign-in cancelled.",
			success:    "GitHub CLI sign-in completed successfully.",
		},
	}

	for _, test := range tests {
		if got := actionName(test.action); got != test.actionName {
			t.Errorf("actionName(%d) = %q; want %q", test.action, got, test.actionName)
		}
		if got := actionRunningMessage(test.action); got != test.running {
			t.Errorf("actionRunningMessage(%d) = %q; want %q", test.action, got, test.running)
		}
		if got := actionCancelledMessage(test.action); got != test.cancelled {
			t.Errorf("actionCancelledMessage(%d) = %q; want %q", test.action, got, test.cancelled)
		}
		if got := actionSuccessMessage(test.action); got != test.success {
			t.Errorf("actionSuccessMessage(%d) = %q; want %q", test.action, got, test.success)
		}
	}
}

func TestGitToolsRequireAnsible(t *testing.T) {
	model := New()
	model.page = menuGitSetup
	model.selected = sectionActionIndex(t, gitSetupSections, actionInstallGitTools)
	t.Setenv("PATH", t.TempDir())

	model.prepareSelectedAction()

	if model.confirming {
		t.Fatal("Git tools installation should not confirm without Ansible")
	}
	if model.status != "Install Ansible first, then retry this action." {
		t.Fatalf("unexpected missing-Ansible status: %q", model.status)
	}
}

func TestGitHubLoginRequiresInstalledGitTools(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is rejected before executable discovery")
	}

	model := New()
	model.page = menuGitSetup
	model.selected = sectionActionIndex(t, gitSetupSections, actionGitHubLogin)
	t.Setenv("PATH", t.TempDir())

	model.prepareSelectedAction()

	if model.confirming {
		t.Fatal("GitHub login should not confirm without the Git tools")
	}
	if model.status != "Install Git tools first, then retry GitHub sign-in." {
		t.Fatalf("unexpected missing-tools status: %q", model.status)
	}
}

func TestGitHubLoginUsesInteractiveGHWithoutInheritedTokens(t *testing.T) {
	environment := []string{
		"HOME=/home/operator",
		"PATH=/usr/bin",
		"SSH_AUTH_SOCK=/run/user/1000/agent.sock",
		"GH_TOKEN=secret-gh",
		"GITHUB_TOKEN=secret-github",
		"GH_ENTERPRISE_TOKEN=secret-enterprise",
		"GITHUB_ENTERPRISE_TOKEN=secret-github-enterprise",
		"GH_CONFIG_DIR=/tmp/wrong-gh-config",
		"GH_HOST=enterprise.example",
		"GH_PROMPT_DISABLED=1",
	}
	specs := githubLoginCommandSpecs("/usr/bin/gh", environment)
	if len(specs) != 2 {
		t.Fatalf("GitHub login has %d commands; want login and status", len(specs))
	}

	wantLoginArgs := []string{
		"auth",
		"login",
		"--hostname",
		"github.com",
		"--git-protocol",
		"ssh",
		"--web",
	}
	if specs[0].name != "/usr/bin/gh" || !reflect.DeepEqual(specs[0].args, wantLoginArgs) {
		t.Fatalf("login command = %#v; want gh %#v", specs[0], wantLoginArgs)
	}
	wantStatusArgs := []string{"auth", "status", "--hostname", "github.com"}
	if specs[1].name != "/usr/bin/gh" || !reflect.DeepEqual(specs[1].args, wantStatusArgs) {
		t.Fatalf("status command = %#v; want gh %#v", specs[1], wantStatusArgs)
	}

	for _, spec := range specs {
		joined := "\n" + strings.Join(spec.env, "\n") + "\n"
		for _, forbidden := range []string{
			"\nGH_TOKEN=",
			"\nGITHUB_TOKEN=",
			"\nGH_ENTERPRISE_TOKEN=",
			"\nGITHUB_ENTERPRISE_TOKEN=",
			"\nGH_CONFIG_DIR=",
			"\nGH_HOST=",
			"\nGH_PROMPT_DISABLED=",
		} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("GitHub login inherited %q in %#v", forbidden, spec.env)
			}
		}
		for _, expected := range []string{
			"HOME=/home/operator",
			"PATH=/usr/bin",
			"SSH_AUTH_SOCK=/run/user/1000/agent.sock",
		} {
			if !strings.Contains(joined, "\n"+expected+"\n") {
				t.Errorf("GitHub login environment omitted %q", expected)
			}
		}
	}
}
