package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSMBMountSubmenuStructure(t *testing.T) {
	if configurationSections[2].action != actionOpenSMBMounts {
		t.Fatal("SMB mounts should appear after IRQ affinity in Configuration")
	}

	expected := []sectionAction{
		actionSMBMount,
		actionSMBUnmount,
		actionSMBRemove,
		actionBack,
	}
	if len(smbMountSections) != len(expected) {
		t.Fatalf("SMB submenu has %d entries; want %d", len(smbMountSections), len(expected))
	}
	for index, action := range expected {
		if smbMountSections[index].action != action {
			t.Errorf("SMB submenu action %d = %d; want %d", index, smbMountSections[index].action, action)
		}
	}
}

func TestEnterAndLeaveSMBMountSubmenu(t *testing.T) {
	model := New()
	model.page = menuConfiguration
	model.selected = smbSectionActionIndex(t, configurationSections, actionOpenSMBMounts)
	model.prepareSelectedAction()

	if model.page != menuSMBMounts || model.selected != 0 {
		t.Fatalf("entering SMB mounts produced page %d selection %d", model.page, model.selected)
	}
	if model.pageTitle() != "SMB mounts" {
		t.Fatalf("SMB page title is %q", model.pageTitle())
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving SMB mounts should not execute a command")
	}
	result := updated.(Model)
	if result.page != menuConfiguration {
		t.Fatalf("Esc returned to page %d; want Configuration", result.page)
	}
	if result.currentSection().action != actionOpenSMBMounts {
		t.Fatalf("Esc selected action %d; want SMB mounts", result.currentSection().action)
	}
}

func TestSMBMountBackItemReturnsToConfiguration(t *testing.T) {
	model := New()
	model.page = menuSMBMounts
	model.selected = smbSectionActionIndex(t, smbMountSections, actionBack)
	model.prepareSelectedAction()

	if model.page != menuConfiguration {
		t.Fatalf("Back returned to page %d; want Configuration", model.page)
	}
	if model.currentSection().action != actionOpenSMBMounts {
		t.Fatalf("Back selected action %d; want SMB mounts", model.currentSection().action)
	}
}

func TestSMBMountConfirmationViewsExplainSemantics(t *testing.T) {
	tests := []struct {
		action   sectionAction
		expected []string
	}{
		{
			action: actionSMBMount,
			expected: []string{
				"Mount the SMB share?",
				"persistent",
				"systemd automounting",
				"will not write a test file",
			},
		},
		{
			action: actionSMBUnmount,
			expected: []string{
				"Unmount the SMB share now?",
				"normal unmount",
				"persistent entry remains",
				"next reboot",
			},
		},
		{
			action: actionSMBRemove,
			expected: []string{
				"Remove the persistent SMB mount?",
				"only the fstab",
				"Unrelated or legacy",
				"left unchanged",
			},
		},
	}

	for _, test := range tests {
		t.Run(actionName(test.action), func(t *testing.T) {
			model := New()
			model.page = menuSMBMounts
			model.selected = smbSectionActionIndex(t, smbMountSections, test.action)
			model.confirming = true
			model.width = 150
			view := model.View()

			for _, expected := range append(
				test.expected,
				smbShareSource,
				smbShareMountPoint,
			) {
				if !strings.Contains(view.Content, expected) {
					t.Errorf("confirmation does not contain %q:\n%s", expected, view.Content)
				}
			}
		})
	}
}

func TestSMBMountPlaybookVariables(t *testing.T) {
	tests := []struct {
		action    sectionAction
		operation string
	}{
		{action: actionSMBMount, operation: "mount"},
		{action: actionSMBUnmount, operation: "unmount"},
		{action: actionSMBRemove, operation: "remove"},
	}

	for _, test := range tests {
		variables, err := smbMountPlaybookVariables("operator", test.action)
		if err != nil {
			t.Fatalf("smbMountPlaybookVariables(%d) error: %v", test.action, err)
		}
		if len(variables) != 4 {
			t.Fatalf("SMB variables = %#v; want exactly four values", variables)
		}
		if variables["target_user"] != "operator" ||
			variables["smb_operation"] != test.operation ||
			variables["smb_source"] != smbShareSource ||
			variables["smb_mount_point"] != smbShareMountPoint {
			t.Errorf("unexpected SMB variables: %#v", variables)
		}
	}

	if variables, err := smbMountPlaybookVariables("operator", actionOpenSMBMounts); err == nil {
		t.Fatalf("submenu opener produced variables %#v; want an error", variables)
	}
}

func TestSMBMountActionRequiresAnsible(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	model := New()
	model.page = menuSMBMounts
	model.selected = smbSectionActionIndex(t, smbMountSections, actionSMBMount)
	model.prepareSelectedAction()

	if model.confirming {
		t.Fatal("SMB mount entered confirmation without ansible-playbook")
	}
	if model.status != "Install Ansible first, then retry this action." {
		t.Fatalf("unexpected missing-Ansible status: %q", model.status)
	}
}

func TestSMBMountActionMessages(t *testing.T) {
	tests := []struct {
		action  sectionAction
		name    string
		run     string
		cancel  string
		success string
	}{
		{
			action:  actionSMBMount,
			name:    "SMB share mount",
			run:     "Configuring and mounting",
			cancel:  "mount cancelled",
			success: "mounted and configured",
		},
		{
			action:  actionSMBUnmount,
			name:    "SMB share unmount",
			run:     "Unmounting",
			cancel:  "unmount cancelled",
			success: "unmounted for this boot",
		},
		{
			action:  actionSMBRemove,
			name:    "SMB mount removal",
			run:     "Removing",
			cancel:  "removal cancelled",
			success: "removed from persistent",
		},
	}

	for _, test := range tests {
		if got := actionName(test.action); got != test.name {
			t.Errorf("actionName(%d) = %q; want %q", test.action, got, test.name)
		}
		if got := actionRunningMessage(test.action); !strings.Contains(got, test.run) {
			t.Errorf("actionRunningMessage(%d) = %q", test.action, got)
		}
		if got := actionCancelledMessage(test.action); !strings.Contains(got, test.cancel) {
			t.Errorf("actionCancelledMessage(%d) = %q", test.action, got)
		}
		if got := actionSuccessMessage(test.action); !strings.Contains(got, test.success) {
			t.Errorf("actionSuccessMessage(%d) = %q", test.action, got)
		}
	}
}

func smbSectionActionIndex(
	t *testing.T,
	sections []section,
	action sectionAction,
) int {
	t.Helper()
	for index, candidate := range sections {
		if candidate.action == action {
			return index
		}
	}
	t.Fatalf("action %d not found", action)
	return -1
}
