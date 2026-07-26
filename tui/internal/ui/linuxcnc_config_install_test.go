package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestLinuxCNCConfigInstallConfirmation(t *testing.T) {
	model := New()
	model.page = menuConfiguration
	model.selected = sectionActionIndex(t, configurationSections, actionInstallLinuxCNCConfig)
	model.width = 180
	model.confirming = true

	view := model.View()
	for _, expected := range []string{
		"Install CorvusCNC configuration?",
		corvusCNCRepository,
		corvusCNCInstallDestination,
		"SSH key",
		"existing checkout",
		"unchanged.",
		"will not be started",
	} {
		if !strings.Contains(view.Content, expected) {
			t.Fatalf("confirmation view does not contain %q", expected)
		}
	}
}

func TestLinuxCNCConfigInstallRequiresAnsible(t *testing.T) {
	model := New()
	model.page = menuConfiguration
	model.selected = sectionActionIndex(t, configurationSections, actionInstallLinuxCNCConfig)
	t.Setenv("PATH", t.TempDir())

	model.prepareSelectedAction()

	if model.confirming {
		t.Fatal("configuration installation should not confirm without Ansible")
	}
	if model.status != "Install Ansible first, then retry this action." {
		t.Fatalf("unexpected missing-Ansible status: %q", model.status)
	}
}

func TestLinuxCNCConfigInstallCancellation(t *testing.T) {
	model := New()
	model.page = menuConfiguration
	model.selected = sectionActionIndex(t, configurationSections, actionInstallLinuxCNCConfig)
	model.confirming = true

	updated, command := model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if command != nil {
		t.Fatal("cancelling should not execute a command")
	}

	result := updated.(Model)
	if result.confirming {
		t.Fatal("cancelling should close the confirmation")
	}
	if result.status != "CorvusCNC configuration installation cancelled." {
		t.Fatalf("unexpected cancellation status: %q", result.status)
	}
}

func TestLinuxCNCConfigInstallActionMessages(t *testing.T) {
	if got := actionName(actionInstallLinuxCNCConfig); got != "CorvusCNC configuration installation" {
		t.Fatalf("actionName() = %q", got)
	}
	if got := actionRunningMessage(actionInstallLinuxCNCConfig); !strings.Contains(got, "CorvusCNC") {
		t.Fatalf("actionRunningMessage() = %q", got)
	}
	if got := actionSuccessMessage(actionInstallLinuxCNCConfig); !strings.Contains(got, corvusCNCInstallDestination) {
		t.Fatalf("actionSuccessMessage() = %q", got)
	}
}

func TestLinuxCNCConfigInstallPlaybookVariables(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/run/user/1000/ssh-agent.socket")
	variables := linuxCNCConfigInstallPlaybookVariables("operator")
	if len(variables) != 2 ||
		variables["target_user"] != "operator" ||
		variables["corvus_ssh_auth_sock"] != "/run/user/1000/ssh-agent.socket" {
		t.Fatalf("unexpected playbook variables: %#v", variables)
	}
}

func TestLinuxCNCConfigInstallOmitsEmptySSHAgentSocket(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "  ")
	variables := linuxCNCConfigInstallPlaybookVariables("operator")
	if len(variables) != 1 || variables["target_user"] != "operator" {
		t.Fatalf("unexpected playbook variables: %#v", variables)
	}
}
