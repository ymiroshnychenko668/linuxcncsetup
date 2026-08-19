package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSMBMountConfigurationEntryAndListStructure(t *testing.T) {
	if configurationSections[2].action != actionOpenSMBMounts {
		t.Fatal("SMB mounts should appear after IRQ affinity in Configuration")
	}

	mounts := []smbMount{
		{ID: "0123456789abcdef", Server: "10.0.1.20", Share: "jobs", MountPoint: "/mnt/jobs", Mounted: true},
		{ID: "fedcba9876543210", Server: "10.0.1.21", Share: "archive", MountPoint: "/media/archive", Automount: true},
	}
	sections := smbMountListSections(mounts)
	expected := []sectionAction{
		actionSMBSelect,
		actionSMBSelect,
		actionSMBAdd,
		actionSMBRefresh,
		actionBack,
	}
	if len(sections) != len(expected) {
		t.Fatalf("SMB list has %d entries; want %d", len(sections), len(expected))
	}
	for index, action := range expected {
		if sections[index].action != action {
			t.Errorf("SMB list action %d = %d; want %d", index, sections[index].action, action)
		}
	}
	if !strings.Contains(sections[0].description, "mounted") ||
		!strings.Contains(sections[1].description, "automount ready") {
		t.Fatalf("list does not expose current state: %#v", sections)
	}
}

func TestSMBMountCRUDNavigation(t *testing.T) {
	mount := smbMount{
		ID:         "0123456789abcdef",
		Server:     "192.168.10.12",
		Share:      "programs",
		MountPoint: "/mnt/programs",
	}
	model := New()
	model.page = menuSMBMounts
	model.smbMounts = []smbMount{mount}
	model.selected = 0
	model.prepareSelectedAction()
	if model.page != menuSMBMountDetail || model.smbSelectedID != mount.ID {
		t.Fatalf("opening mount produced page %d and ID %q", model.page, model.smbSelectedID)
	}

	model.selected = smbSectionActionIndex(t, smbMountDetailSections, actionSMBEdit)
	model.prepareSelectedAction()
	if model.page != menuSMBMountForm || !model.smbDraft.editing() {
		t.Fatalf("edit produced page %d and draft %#v", model.page, model.smbDraft)
	}
	if model.smbDraft.server != mount.Server || model.smbDraft.share != mount.Share ||
		model.smbDraft.mountPoint != mount.MountPoint {
		t.Fatalf("edit form did not load selected mount: %#v", model.smbDraft)
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving SMB edit form should not execute a command")
	}
	result := updated.(Model)
	if result.page != menuSMBMountDetail || result.currentSection().action != actionSMBEdit {
		t.Fatalf("Esc returned to page %d action %d", result.page, result.currentSection().action)
	}

	result.back()
	if result.page != menuSMBMounts || result.currentSection().value != mount.ID {
		t.Fatalf("detail Back returned to page %d section %#v", result.page, result.currentSection())
	}

	result.selected = smbSectionActionIndex(t, result.visibleSections(), actionSMBAdd)
	result.prepareSelectedAction()
	if result.page != menuSMBMountForm || result.smbDraft.editing() {
		t.Fatalf("Add produced page %d and draft %#v", result.page, result.smbDraft)
	}
	if result.smbDraft.server != smbDefaultServer || result.smbDraft.share != smbDefaultShare ||
		result.smbDraft.mountPoint != smbDefaultMountPoint {
		t.Fatalf("unexpected Add defaults: %#v", result.smbDraft)
	}
}

func TestSMBMountFormEditing(t *testing.T) {
	model := New()
	model.page = menuSMBMountForm
	model.smbDraft = smbMountDraft{server: "192.168.1.2", focusedField: smbServerField}

	if !model.updateSMBMountForm(tea.KeyPressMsg{Code: '3', Text: "3"}) ||
		model.smbDraft.server != "192.168.1.23" {
		t.Fatalf("printable input produced server %q", model.smbDraft.server)
	}
	if !model.updateSMBMountForm(tea.KeyPressMsg{Code: tea.KeyBackspace}) ||
		model.smbDraft.server != "192.168.1.2" {
		t.Fatalf("Backspace produced server %q", model.smbDraft.server)
	}
	if !model.updateSMBMountForm(tea.KeyPressMsg{Code: tea.KeyTab}) ||
		model.smbDraft.focusedField != smbShareField {
		t.Fatalf("Tab selected field %d", model.smbDraft.focusedField)
	}
	if !model.updateSMBMountForm(tea.KeyPressMsg{Code: tea.KeyUp}) ||
		model.smbDraft.focusedField != smbServerField {
		t.Fatalf("Up selected field %d", model.smbDraft.focusedField)
	}
	if !model.updateSMBMountForm(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}) ||
		model.smbDraft.server != "" {
		t.Fatalf("Ctrl+U produced server %q", model.smbDraft.server)
	}
	for _, message := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter},
		{Code: tea.KeyEscape},
		{Code: tea.KeyF5},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		if model.updateSMBMountForm(message) {
			t.Fatalf("outer-model key %q was consumed", message.String())
		}
	}

	model.smbDraft.focusedField = smbShareField
	updated, command := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if command != nil {
		t.Fatal("typing q in the SMB form should not quit")
	}
	if !strings.HasSuffix(updated.(Model).smbDraft.share, "q") {
		t.Fatalf("typing q did not edit the share: %q", updated.(Model).smbDraft.share)
	}
}

func TestSMBMountFormF5TestsAndEnterReviews(t *testing.T) {
	binDirectory := t.TempDir()
	for _, name := range []string{"ansible-playbook", "sudo"} {
		path := filepath.Join(binDirectory, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDirectory)
	t.Setenv("SUDO_USER", "operator")

	model := New()
	model.page = menuSMBMountForm
	model.smbDraft = smbMountDraft{
		server:     "192.168.1.20",
		share:      "jobs",
		mountPoint: "",
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	if command != nil {
		t.Fatal("F5 should open a test confirmation before executing")
	}
	result := updated.(Model)
	if !result.confirming || result.smbPendingAction != actionSMBTest {
		t.Fatalf("F5 produced confirming=%v pending=%d", result.confirming, result.smbPendingAction)
	}
	if !strings.Contains(result.View().Content, "Test this SMB connection?") {
		t.Fatalf("F5 confirmation was not rendered:\n%s", result.View().Content)
	}

	updated, command = result.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if command != nil {
		t.Fatal("cancelling the F5 test should not execute a command")
	}
	result = updated.(Model)
	if result.confirming || result.smbPendingAction != actionNone ||
		!strings.Contains(result.status, "test cancelled") {
		t.Fatalf("test cancellation left model %#v", result)
	}

	result.smbDraft.mountPoint = "/mnt/jobs"
	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("Enter should open a save confirmation before executing")
	}
	result = updated.(Model)
	if !result.confirming || result.smbPendingAction != actionSMBSave {
		t.Fatalf("Enter produced confirming=%v pending=%d", result.confirming, result.smbPendingAction)
	}
}

func TestSMBMountDraftValidation(t *testing.T) {
	valid := smbMountDraft{
		server:     "192.168.50.10",
		share:      "machine_jobs",
		mountPoint: "/mnt/machine_jobs",
	}
	if err := validateSMBMountDraft(valid, nil); err != nil {
		t.Fatalf("valid SMB mount draft rejected: %v", err)
	}

	tests := []struct {
		name   string
		change func(*smbMountDraft)
		want   string
	}{
		{"hostname", func(d *smbMountDraft) { d.server = "nas.local" }, "IPv4"},
		{"loopback", func(d *smbMountDraft) { d.server = "127.0.0.1" }, "non-loopback"},
		{"reserved", func(d *smbMountDraft) { d.server = "240.0.0.1" }, "non-loopback"},
		{"large octet", func(d *smbMountDraft) { d.server = "192.168.1.999" }, "IPv4"},
		{"nested share", func(d *smbMountDraft) { d.share = "jobs/current" }, "share/folder"},
		{"space in share", func(d *smbMountDraft) { d.share = "machine jobs" }, "share/folder"},
		{"root", func(d *smbMountDraft) { d.mountPoint = "/" }, "/mnt or /media"},
		{"outside managed roots", func(d *smbMountDraft) { d.mountPoint = "/home/operator/jobs" }, "/mnt or /media"},
		{"traversal", func(d *smbMountDraft) { d.mountPoint = "/mnt/jobs/../other" }, "/mnt or /media"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := valid
			test.change(&draft)
			err := validateSMBMountDraft(draft, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v; want %q", err, test.want)
			}
		})
	}

	existing := []smbMount{{
		ID:         "0123456789abcdef",
		Server:     valid.server,
		Share:      "other",
		MountPoint: valid.mountPoint,
	}}
	if err := validateSMBMountDraft(valid, existing); err == nil || !strings.Contains(err.Error(), "already managed") {
		t.Fatalf("duplicate mount directory error = %v", err)
	}
	valid.previousID = existing[0].ID
	if err := validateSMBMountDraft(valid, existing); err != nil {
		t.Fatalf("editing the selected mount rejected its own directory: %v", err)
	}
}

func TestParseMultipleAndLegacyManagedSMBMounts(t *testing.T) {
	fstab := strings.Join([]string{
		"UUID=root / ext4 defaults 0 1",
		"# BEGIN LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef",
		"//192.168.1.20/jobs /mnt/jobs cifs username=guest,guest,x-systemd.automount 0 0",
		"# END LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef",
		"# an unrelated comment",
		"# BEGIN LINUXCNCSETUP MANAGED SMB SHARE",
		"//10.0.1.246/share /media/legacy cifs username=guest,guest,x-systemd.automount 0 0",
		"# END LINUXCNCSETUP MANAGED SMB SHARE",
	}, "\n")
	mounts, err := parseManagedSMBMounts(fstab)
	if err != nil {
		t.Fatalf("parseManagedSMBMounts() error: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("parsed %d mounts; want 2: %#v", len(mounts), mounts)
	}
	if mounts[0].MountPoint != "/media/legacy" || mounts[0].ID != smbLegacyMountID ||
		mounts[1].MountPoint != "/mnt/jobs" || mounts[1].ID != "0123456789abcdef" {
		t.Fatalf("unexpected parsed mounts: %#v", mounts)
	}

	applySMBMountInfo(mounts, strings.Join([]string{
		"35 24 0:42 / /mnt/jobs rw,relatime - cifs //192.168.1.20/jobs rw",
		"36 24 0:43 / /media/legacy rw,relatime - autofs systemd-1 rw",
	}, "\n"))
	if !mounts[1].Mounted || mounts[1].Automount {
		t.Fatalf("CIFS state not detected: %#v", mounts[1])
	}
	if mounts[0].Mounted || !mounts[0].Automount {
		t.Fatalf("automount state not detected: %#v", mounts[0])
	}
}

func TestDiscoverSMBMountsFromFiles(t *testing.T) {
	directory := t.TempDir()
	fstabPath := filepath.Join(directory, "fstab")
	mountInfoPath := filepath.Join(directory, "mountinfo")
	fstab := "# BEGIN LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef\n" +
		"//192.168.1.20/jobs /mnt/jobs cifs guest 0 0\n" +
		"# END LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef\n"
	if err := os.WriteFile(fstabPath, []byte(fstab), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mountInfoPath, []byte("35 24 0:42 / /mnt/jobs rw - cifs //192.168.1.20/jobs rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mounts, err := discoverSMBMounts(fstabPath, mountInfoPath)
	if err != nil || len(mounts) != 1 || !mounts[0].Mounted {
		t.Fatalf("discoverSMBMounts() = %#v, %v", mounts, err)
	}
}

func TestMalformedManagedSMBMountsAreRejected(t *testing.T) {
	tests := []string{
		"# END LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef\n",
		"# BEGIN LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef\n//192.168.1.20/jobs /mnt/jobs cifs guest 0 0\n",
		"# BEGIN LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef\n//192.168.1.20/jobs /mnt/jobs cifs guest 0 0\n# END LINUXCNCSETUP MANAGED SMB MOUNT fedcba9876543210\n",
		"# BEGIN LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef\n//192.168.1.20/jobs /mnt/jobs ext4 defaults 0 0\n# END LINUXCNCSETUP MANAGED SMB MOUNT 0123456789abcdef\n",
	}
	for index, fixture := range tests {
		if mounts, err := parseManagedSMBMounts(fixture); err == nil {
			t.Errorf("fixture %d parsed as %#v; want an error", index, mounts)
		}
	}
}

func TestSMBMountFormAndConfirmationExplainTestingAndPersistence(t *testing.T) {
	model := New()
	model.page = menuSMBMountForm
	model.smbDraft = smbMountDraft{
		server:     "192.168.1.20",
		share:      "jobs",
		mountPoint: "/mnt/jobs",
	}
	form := strings.Join(model.renderSMBMountForm(false), "\n")
	for _, expected := range []string{"Server IPv4", "Share / folder", "Local mount directory", "F5", "guest"} {
		if !strings.Contains(form, expected) {
			t.Errorf("form does not contain %q:\n%s", expected, form)
		}
	}

	model.smbPendingAction = actionSMBTest
	testConfirmation := strings.Join(model.renderSMBMountForm(true), "\n")
	for _, expected := range []string{"Test this SMB connection?", "//192.168.1.20/jobs", "smbclient", "does not write", "does not", "/etc/fstab"} {
		if !strings.Contains(testConfirmation, expected) {
			t.Errorf("test confirmation does not contain %q:\n%s", expected, testConfirmation)
		}
	}

	model.smbPendingAction = actionSMBSave
	saveConfirmation := strings.Join(model.renderSMBMountForm(true), "\n")
	for _, expected := range []string{"Create and mount", "/mnt/jobs", "marked fstab block", "systemd automounting", "no password"} {
		if !strings.Contains(saveConfirmation, expected) {
			t.Errorf("save confirmation does not contain %q:\n%s", expected, saveConfirmation)
		}
	}
}

func TestSMBMountDetailActionsExplainSelectedEntry(t *testing.T) {
	mount := smbMount{
		ID:         "0123456789abcdef",
		Server:     "192.168.1.20",
		Share:      "jobs",
		MountPoint: "/mnt/jobs",
		Mounted:    true,
	}
	model := New()
	model.page = menuSMBMountDetail
	model.smbMounts = []smbMount{mount}
	model.smbSelectedID = mount.ID

	tests := []struct {
		action   sectionAction
		expected []string
	}{
		{actionSMBMount, []string{"Mount this SMB share now?", "systemd automount", "expected CIFS"}},
		{actionSMBTest, []string{"Test this SMB connection?", "smbclient", "read-only", "/etc/fstab is unchanged"}},
		{actionSMBUnmount, []string{"Unmount this SMB share now?", "normal", "busy mount fails", "persistent entry remains"}},
		{actionSMBRemove, []string{"Delete this persistent SMB mount?", "owned fstab block", "local directory", "unrelated"}},
	}
	for _, test := range tests {
		rendered := strings.Join(model.renderSMBMountAction(test.action, true), "\n")
		for _, expected := range append(test.expected, mount.source(), mount.MountPoint, "mounted") {
			if !strings.Contains(rendered, expected) {
				t.Errorf("action %d confirmation does not contain %q:\n%s", test.action, expected, rendered)
			}
		}
	}
}

func TestSMBMountPlaybookVariables(t *testing.T) {
	mount := smbMount{
		Server:     "192.168.1.20",
		Share:      "jobs",
		MountPoint: "/mnt/jobs",
	}
	mount.ID = smbMountID(mount)
	previous := smbMount{
		ID:         smbLegacyMountID,
		Server:     "10.0.1.246",
		Share:      "share",
		MountPoint: "/mnt/smb_share",
	}

	tests := []struct {
		action    sectionAction
		operation string
		previous  *smbMount
	}{
		{actionSMBSave, "apply", &previous},
		{actionSMBTest, "test", nil},
		{actionSMBMount, "mount", nil},
		{actionSMBUnmount, "unmount", nil},
		{actionSMBRemove, "remove", nil},
	}
	for _, test := range tests {
		variables, err := smbMountPlaybookVariables("operator", test.action, mount, test.previous)
		if err != nil {
			t.Fatalf("smbMountPlaybookVariables(%d) error: %v", test.action, err)
		}
		if len(variables) != 10 ||
			variables["target_user"] != "operator" ||
			variables["smb_operation"] != test.operation ||
			variables["smb_server"] != mount.Server ||
			variables["smb_share"] != mount.Share ||
			variables["smb_source"] != mount.source() {
			t.Errorf("unexpected SMB variables: %#v", variables)
		}
		if test.previous != nil &&
			(variables["smb_previous_mount_id"] != previous.ID ||
				variables["smb_previous_source"] != previous.source() ||
				variables["smb_previous_mount_point"] != previous.MountPoint) {
			t.Errorf("previous SMB variables were not preserved: %#v", variables)
		}
	}

	if variables, err := smbMountPlaybookVariables("operator", actionSMBAdd, mount, nil); err == nil {
		t.Fatalf("submenu action produced variables %#v; want an error", variables)
	}
}

func TestSMBMountActionRequiresAnsible(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	model := New()
	model.page = menuSMBMountForm
	model.smbDraft = smbMountDraft{
		server:     "192.168.1.20",
		share:      "jobs",
		mountPoint: "/mnt/jobs",
	}
	if model.prepareSMBMountAction(actionSMBSave) {
		t.Fatal("SMB save prepared without ansible-playbook")
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
		{actionSMBSave, "SMB mount save", "Saving and mounting", "save cancelled", "saved, mounted"},
		{actionSMBTest, "SMB connection test", "Testing guest access", "test cancelled", "test succeeded"},
		{actionSMBMount, "SMB share mount", "Mounting the selected", "mount cancelled", "mounted successfully"},
		{actionSMBUnmount, "SMB share unmount", "Unmounting the selected", "unmount cancelled", "unmounted for this boot"},
		{actionSMBRemove, "SMB mount deletion", "Deleting the selected", "deletion cancelled", "deleted from persistent"},
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

func smbSectionActionIndex(t *testing.T, sections []section, action sectionAction) int {
	t.Helper()
	for index, candidate := range sections {
		if candidate.action == action {
			return index
		}
	}
	t.Fatalf("action %d not found", action)
	return -1
}
