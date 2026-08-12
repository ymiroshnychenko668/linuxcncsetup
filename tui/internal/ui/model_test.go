package ui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInitialView(t *testing.T) {
	model := New()
	view := model.View()

	if !strings.Contains(view.Content, "LinuxCNC Setup") {
		t.Fatal("initial view does not contain the application title")
	}
	if !strings.Contains(view.Content, "Install Ansible") {
		t.Fatal("Ansible installation is not the first menu item")
	}
	if !strings.Contains(view.Content, "Debian") {
		t.Fatal("initial view does not contain the selected section details")
	}
	if !view.AltScreen {
		t.Fatal("application should use the alternate screen")
	}
}

func TestSelectionWraps(t *testing.T) {
	model := New()

	model.moveSelection(-1)
	if model.selected != len(mainSections)-1 {
		t.Fatalf("moving above the first item selected %d; want %d", model.selected, len(mainSections)-1)
	}

	model.moveSelection(1)
	if model.selected != 0 {
		t.Fatalf("moving below the last item selected %d; want 0", model.selected)
	}
}

func TestAnsibleConfirmationView(t *testing.T) {
	model := New()
	model.confirming = true
	view := model.View()

	if !strings.Contains(view.Content, "Install Ansible now?") {
		t.Fatal("confirmation view does not contain its warning")
	}
	if !strings.Contains(view.Content, "sudo apt-get install -y ansible") {
		t.Fatal("confirmation view does not show the command that will run")
	}
}

func TestAutologinSubmenuStructure(t *testing.T) {
	if mainSections[1].action != actionOpenRemoteTerminal {
		t.Fatal("Remote Terminal should follow Ansible installation")
	}
	if mainSections[2].action != actionOpenGitSetup {
		t.Fatal("Git setup should follow Remote Terminal")
	}
	if mainSections[3].action != actionInstallLinuxCNCConfig {
		t.Fatal("CorvusCNC installation should follow Git setup")
	}
	if mainSections[4].action != actionInstallSway {
		t.Fatal("Sway installation should follow CorvusCNC installation")
	}
	if mainSections[5].action != actionOpenDevTools {
		t.Fatal("developer-tools submenu should follow Sway installation")
	}
	if mainSections[6].action != actionOpenAutologin {
		t.Fatal("automatic login should follow Sway installation")
	}
	if mainSections[7].action != actionOpenLinuxCNCAutostart {
		t.Fatal("LinuxCNC autostart should follow automatic login")
	}
	if autologinSections[0].action != actionAutologinLightDM {
		t.Fatal("LightDM should be the first automatic-login option")
	}
	if !strings.Contains(autologinSections[0].description, "Use LightDM") {
		t.Fatal("LightDM option should describe selecting LightDM")
	}
	if autologinSections[1].action != actionAutologinSway {
		t.Fatal("Sway should be the second automatic-login option")
	}
	if autologinSections[2].action != actionBack {
		t.Fatal("automatic-login submenu should end with a back action")
	}
	if mainSections[8].action != actionReboot {
		t.Fatal("reboot should follow the LinuxCNC autostart submenu")
	}
}

func TestEnterAndLeaveAutologinSubmenu(t *testing.T) {
	mainIndex := mainMenuActionIndex(t, actionOpenAutologin)
	model := New()
	model.selected = mainIndex
	model.prepareSelectedAction()

	if model.page != menuAutologin || model.selected != 0 {
		t.Fatalf("entering submenu produced page %d selection %d", model.page, model.selected)
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving a submenu should not execute a command")
	}

	result := updated.(Model)
	if result.page != menuMain || result.selected != mainIndex {
		t.Fatalf("Esc returned to page %d selection %d", result.page, result.selected)
	}
}

func TestLinuxCNCAutostartSubmenuStructure(t *testing.T) {
	if linuxCNCAutostartSections[0].action != actionOpenLinuxCNCAutostartSway {
		t.Fatal("Sway should be the first LinuxCNC autostart option")
	}
	if linuxCNCAutostartSections[1].action != actionOpenLinuxCNCAutostartX11 {
		t.Fatal("XFCE X11 should be the second LinuxCNC autostart option")
	}
	if linuxCNCAutostartSections[1].title != "XFCE (X11)" {
		t.Fatalf(
			"XFCE X11 title = %q",
			linuxCNCAutostartSections[1].title,
		)
	}
	if linuxCNCAutostartSections[2].action != actionBack {
		t.Fatal("LinuxCNC autostart submenu should end with a back action")
	}
}

func TestConfigurationSubmenuStructure(t *testing.T) {
	if mainSections[mainMenuActionIndex(t, actionOpenConfiguration)].action != actionOpenConfiguration {
		t.Fatal("Configuration should open its submenu")
	}
	if configurationSections[0].action != actionOpenGRUBRealtime {
		t.Fatal("GRUB real-time setup should be the first configuration tool")
	}
	if configurationSections[1].action != actionOpenIRQAffinity {
		t.Fatal("IRQ affinity should follow GRUB real-time setup")
	}
	if configurationSections[2].action != actionOpenSMBMounts {
		t.Fatal("SMB mounts should follow IRQ affinity")
	}
	if configurationSections[3].action != actionBack {
		t.Fatal("configuration submenu should end with a back action")
	}
	if irqAffinitySections[0].action != actionIRQDevices {
		t.Fatal("the live IRQ device table should be the first IRQ affinity option")
	}
	if irqAffinitySections[1].action != actionIRQStatus {
		t.Fatal("IRQ status should follow the device table")
	}
	if irqAffinitySections[2].action != actionIRQGuidedSetup {
		t.Fatal("the default policy should follow IRQ status")
	}
	if irqAffinitySections[3].action != actionIRQDisable {
		t.Fatal("disable should follow the default policy")
	}
}

func TestEnterAndLeaveConfigurationSubmenu(t *testing.T) {
	mainIndex := mainMenuActionIndex(t, actionOpenConfiguration)
	model := New()
	model.selected = mainIndex
	model.prepareSelectedAction()

	if model.page != menuConfiguration || model.selected != 0 {
		t.Fatalf("entering Configuration produced page %d selection %d", model.page, model.selected)
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving Configuration should not execute a command")
	}
	result := updated.(Model)
	if result.page != menuMain || result.selected != mainIndex {
		t.Fatalf("Esc returned to page %d selection %d", result.page, result.selected)
	}
}

func TestIRQCPUSelectionAndValidation(t *testing.T) {
	model := New()
	model.page = menuIRQCPUs
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{OnlineCPUs: []int{0, 1, 2, 3, 4, 5}}
	model.irqProtectedCPUs = []int{4, 5}
	model.rebuildIRQCPUSections()

	if len(model.irqCPUSections) != 8 {
		t.Fatalf("CPU page has %d entries; want six CPUs, Continue, and Back", len(model.irqCPUSections))
	}
	if err := model.validateIRQDraft(); err != nil {
		t.Fatalf("recommended policy is invalid: %v", err)
	}
	if !strings.Contains(model.irqCPUSections[4].title, "[x]") {
		t.Fatal("protected CPU is not visibly selected")
	}

	model.toggleIRQCPU("3")
	if !containsCPU(model.irqProtectedCPUs, 3) {
		t.Fatal("toggling CPU 3 did not protect it")
	}
	model.toggleIRQCPU("0")
	model.toggleIRQCPU("1")
	model.toggleIRQCPU("2")
	if containsCPU(model.irqProtectedCPUs, 2) {
		t.Fatal("the UI allowed every online CPU to become protected")
	}
	if !strings.Contains(model.status, "At least one CPU") {
		t.Fatalf("unexpected all-protected warning: %q", model.status)
	}
}

func TestIRQReviewView(t *testing.T) {
	model := New()
	model.page = menuIRQReview
	model.width = 160
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{
		OnlineCPUs: []int{0, 1, 2, 3, 4, 5},
		IRQs:       make([]IRQEntry, 12),
	}
	model.irqProtectedCPUs = []int{4, 5}
	view := model.View()

	for _, expected := range []string{
		"Protected LinuxCNC CPUs: 4-5",
		"Housekeeping/IRQ CPUs:   0-3",
		"Nothing is applied to live IRQs now",
		"Preview Ansible changes",
	} {
		if !strings.Contains(view.Content, expected) {
			t.Fatalf("IRQ review does not contain %q", expected)
		}
	}
}

func TestIRQApplyConfirmationView(t *testing.T) {
	model := New()
	model.page = menuIRQReview
	model.selected = 1
	model.width = 160
	model.confirming = true
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{OnlineCPUs: []int{0, 1, 2, 3}}
	model.irqProtectedCPUs = []int{3}
	view := model.View()

	for _, expected := range []string{
		"Enable this IRQ policy?",
		"next boot",
		"not change live IRQs",
		"will not",
	} {
		if !strings.Contains(view.Content, expected) {
			t.Fatalf("IRQ apply confirmation does not contain %q", expected)
		}
	}
}

func TestIRQStatusShowsManagedPolicyAndBootResult(t *testing.T) {
	model := New()
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{
		OnlineCPUs: []int{0, 1, 2, 3},
		ManagedPolicy: ManagedIRQPolicyStatus{
			Config:  ManagedIRQComponentStatus{Present: true},
			Helper:  ManagedIRQComponentStatus{Present: true},
			Service: ManagedIRQComponentStatus{Present: true},
			Enabled: true,
			ConfigData: &ManagedIRQConfig{
				HousekeepingCPUs: []int{0, 1, 2},
				ProtectedCPUs:    []int{3},
			},
			ResultData: &ManagedIRQResult{
				GeneratedAt: "2026-07-26T12:00:00Z",
				Status:      "applied",
				Message:     "policy applied",
				Policy: ManagedIRQResultPolicy{
					HousekeepingCPUs: []int{0, 1, 2},
					ProtectedCPUs:    []int{3},
				},
				Counts: ManagedIRQResultCounts{
					Applied:       7,
					Constrained:   2,
					KernelManaged: 3,
					Unwritable:    1,
					Failed:        0,
				},
			},
		},
	}

	status := model.renderIRQStatus()
	for _, expected := range []string{
		"Protected CPUs:    3",
		"Housekeeping CPUs: 0-2",
		"Last boot result:     applied",
		"Applied/constrained: 7/2",
		"Kernel-managed:     3",
		"Unwritable/failed:  1/0",
		"2026-07-26T12:00:00Z",
		"policy applied",
	} {
		if !strings.Contains(status, expected) {
			t.Errorf("IRQ status does not contain %q:\n%s", expected, status)
		}
	}
}

func TestIRQSMTSiblingsMoveTogether(t *testing.T) {
	model := New()
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{
		OnlineCPUs: []int{0, 1, 2, 3},
		CPUs: []CPUInfo{
			{ID: 0, ThreadSiblings: []int{0, 2}},
			{ID: 1, ThreadSiblings: []int{1, 3}},
			{ID: 2, ThreadSiblings: []int{0, 2}},
			{ID: 3, ThreadSiblings: []int{1, 3}},
		},
	}
	model.irqProtectedCPUs = []int{2}
	if err := model.validateIRQDraft(); err == nil || !strings.Contains(err.Error(), "SMT sibling") {
		t.Fatalf("split SMT policy error = %v", err)
	}

	model.irqProtectedCPUs = model.expandIRQSiblingGroups(model.irqProtectedCPUs)
	if got := FormatCPUList(model.irqProtectedCPUs); got != "0,2" {
		t.Fatalf("expanded protected CPUs = %q; want 0,2", got)
	}
	if err := model.validateIRQDraft(); err != nil {
		t.Fatalf("whole-core SMT policy is invalid: %v", err)
	}

	model.rebuildIRQCPUSections()
	model.toggleIRQCPU("1")
	if got := FormatCPUList(model.irqProtectedCPUs); got != "0,2" {
		t.Fatalf("all-protected guard changed CPUs to %q", got)
	}
	if !strings.Contains(model.status, "At least one CPU") {
		t.Fatalf("unexpected all-protected status: %q", model.status)
	}

	model.toggleIRQCPU("2")
	if len(model.irqProtectedCPUs) != 0 {
		t.Fatalf("removing CPU 2 did not remove sibling group: %v", model.irqProtectedCPUs)
	}
}

func TestIRQManagedPolicyDetectsResultOnlyCleanup(t *testing.T) {
	model := New()
	model.irqSnapshot.ManagedPolicy.Result = ManagedIRQComponentStatus{Present: true}
	if !model.irqManagedPolicyPresent() {
		t.Fatal("a stale managed result should be eligible for cleanup")
	}
	if got := renderManagedIRQState(model.irqSnapshot.ManagedPolicy); got != "partial installation detected" {
		t.Fatalf("result-only managed state = %q", got)
	}
}

func TestIRQBalanceStateShowsEnabledConflict(t *testing.T) {
	got := renderIRQBalanceState(IRQBalanceStatus{
		Installed:    true,
		Active:       false,
		ActiveKnown:  true,
		Enabled:      true,
		EnabledKnown: true,
	})
	for _, expected := range []string{"inactive", "enabled", "must be resolved"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("irqbalance state %q does not contain %q", got, expected)
		}
	}
}

func TestDiscoverLinuxCNCConfigs(t *testing.T) {
	root := t.TempDir()
	firstDirectory := filepath.Join(root, "machine-a")
	secondDirectory := filepath.Join(root, "machine-b")
	for _, directory := range []string{firstDirectory, secondDirectory} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create test directory: %v", err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(firstDirectory, "z-axis.ini"):  "[DISPLAY]\nDISPLAY = axis\n",
		filepath.Join(secondDirectory, "alpha.ini"):  "[DISPLAY]\nDISPLAY = qtvcp\n",
		filepath.Join(secondDirectory, "ignore.txt"): "not a configuration\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write test configuration: %v", err)
		}
	}
	t.Setenv(linuxCNCConfigDirectoryEnvironment, root)

	configs, discoveredRoot, err := discoverLinuxCNCConfigs()
	if err != nil {
		t.Fatalf("discoverLinuxCNCConfigs() error: %v", err)
	}
	if discoveredRoot != root {
		t.Fatalf("discovered root %q; want %q", discoveredRoot, root)
	}
	if len(configs) != 2 {
		t.Fatalf("discovered %d configurations; want 2", len(configs))
	}
	if configs[0].label != "alpha" || configs[1].label != "z-axis" {
		t.Fatalf("configurations are not sorted by basename: %#v", configs)
	}
	for _, config := range configs {
		if !filepath.IsAbs(config.path) {
			t.Fatalf("configuration path is not absolute: %q", config.path)
		}
	}
}

func TestEnterAndLeaveLinuxCNCAutostartMenus(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "mill.ini")
	if err := os.WriteFile(configPath, []byte("[DISPLAY]\nDISPLAY = qtvcp\n"), 0o644); err != nil {
		t.Fatalf("write test configuration: %v", err)
	}
	t.Setenv(linuxCNCConfigDirectoryEnvironment, root)

	mainIndex := mainMenuActionIndex(t, actionOpenLinuxCNCAutostart)
	for _, test := range []struct {
		name            string
		desktop         linuxCNCAutostartDesktop
		platformIndex   int
		configureAction sectionAction
	}{
		{
			name:            "Sway",
			desktop:         linuxCNCDesktopSway,
			platformIndex:   0,
			configureAction: actionConfigureLinuxCNCAutostartSway,
		},
		{
			name:            "XFCE X11",
			desktop:         linuxCNCDesktopXFCE,
			platformIndex:   1,
			configureAction: actionConfigureLinuxCNCAutostartX11,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := New()
			model.selected = mainIndex
			model.prepareSelectedAction()
			if model.page != menuLinuxCNCAutostart || model.selected != 0 {
				t.Fatalf(
					"entering platform menu produced page %d selection %d",
					model.page,
					model.selected,
				)
			}

			model.selected = test.platformIndex
			model.prepareSelectedAction()
			if model.page != menuLinuxCNCConfigs || model.selected != 0 {
				t.Fatalf(
					"entering config menu produced page %d selection %d",
					model.page,
					model.selected,
				)
			}
			if model.linuxCNCDesktop != test.desktop {
				t.Fatalf(
					"selected desktop = %d; want %d",
					model.linuxCNCDesktop,
					test.desktop,
				)
			}
			if len(model.visibleSections()) != 2 {
				t.Fatalf(
					"config menu has %d entries; want one config and Back",
					len(model.visibleSections()),
				)
			}
			if model.currentSection().value != configPath {
				t.Fatalf(
					"selected config %q; want %q",
					model.currentSection().value,
					configPath,
				)
			}
			if model.currentSection().action != test.configureAction {
				t.Fatalf(
					"configure action = %d; want %d",
					model.currentSection().action,
					test.configureAction,
				)
			}
			if model.pageTitle() != test.desktop.pageTitle() {
				t.Fatalf(
					"page title = %q; want %q",
					model.pageTitle(),
					test.desktop.pageTitle(),
				)
			}

			updated, command := model.Update(
				tea.KeyPressMsg{Code: tea.KeyEscape},
			)
			if command != nil {
				t.Fatal("leaving the config menu should not execute a command")
			}
			result := updated.(Model)
			if result.page != menuLinuxCNCAutostart ||
				result.selected != test.platformIndex {
				t.Fatalf(
					"Esc returned to page %d selection %d",
					result.page,
					result.selected,
				)
			}

			updated, command = result.Update(
				tea.KeyPressMsg{Code: tea.KeyEscape},
			)
			if command != nil {
				t.Fatal("leaving the platform menu should not execute a command")
			}
			result = updated.(Model)
			if result.page != menuMain || result.selected != mainIndex {
				t.Fatalf(
					"second Esc returned to page %d selection %d",
					result.page,
					result.selected,
				)
			}
		})
	}
}

func TestLinuxCNCAutostartX11ShowsEmptyConfigSelection(t *testing.T) {
	root := t.TempDir()
	t.Setenv(linuxCNCConfigDirectoryEnvironment, root)

	model := New()
	model.page = menuLinuxCNCAutostart
	model.selected = 1
	model.prepareSelectedAction()

	if model.page != menuLinuxCNCConfigs {
		t.Fatalf("XFCE X11 opened page %d; want config selection", model.page)
	}
	if model.linuxCNCDesktop != linuxCNCDesktopXFCE {
		t.Fatalf("XFCE X11 selected desktop %d", model.linuxCNCDesktop)
	}
	if len(model.visibleSections()) != 1 ||
		model.currentSection().action != actionBack {
		t.Fatalf("empty XFCE config selection = %#v", model.visibleSections())
	}
	if !strings.Contains(model.status, "No .ini configurations") {
		t.Fatalf("unexpected empty XFCE status: %q", model.status)
	}
}

func TestLinuxCNCAutostartConfirmationView(t *testing.T) {
	configPath := "/home/user/linuxcnc/configs/mill profile/mill.ini"
	for _, test := range []struct {
		name     string
		desktop  linuxCNCAutostartDesktop
		expected []string
	}{
		{
			name:    "Sway",
			desktop: linuxCNCDesktopSway,
			expected: []string{
				"Enable LinuxCNC autostart?",
				"next Sway login",
			},
		},
		{
			name:    "XFCE X11",
			desktop: linuxCNCDesktopXFCE,
			expected: []string{
				"Enable XFCE X11 autostart?",
				"per-user XFCE XDG autostart entry",
				"focuses an existing",
				"inactive in XFCE Wayland",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := New()
			model.page = menuLinuxCNCConfigs
			model.linuxCNCDesktop = test.desktop
			model.linuxCNCSections = linuxCNCConfigSections(
				[]linuxCNCConfig{{
					label: "mill",
					path:  configPath,
				}},
				test.desktop,
			)
			model.width = 160
			model.confirming = true
			view := model.View()

			expected := append([]string{
				configPath,
				"workspace 1",
				"machine",
				"will not be launched now",
			}, test.expected...)
			for _, text := range expected {
				if !strings.Contains(view.Content, text) {
					t.Fatalf("confirmation view does not contain %q", text)
				}
			}
		})
	}
}

func TestLinuxCNCAutostartX11ActionMessages(t *testing.T) {
	action := actionConfigureLinuxCNCAutostartX11
	for label, value := range map[string]string{
		"name":      actionName(action),
		"running":   actionRunningMessage(action),
		"cancelled": actionCancelledMessage(action),
		"success":   actionSuccessMessage(action),
	} {
		for _, expected := range []string{"LinuxCNC", "XFCE", "X11"} {
			if !strings.Contains(value, expected) {
				t.Errorf("%s message %q does not contain %q", label, value, expected)
			}
		}
	}
}

func TestSwayInstallationConfirmationView(t *testing.T) {
	model := New()
	model.selected = mainMenuActionIndex(t, actionInstallSway)
	model.confirming = true
	view := model.View()

	if !strings.Contains(view.Content, "Install Wayland + Sway?") {
		t.Fatal("Sway installation confirmation does not describe the action")
	}
	if !strings.Contains(view.Content, "not switch display managers") {
		t.Fatal("Sway installation confirmation does not state its additive scope")
	}
	if !strings.Contains(view.Content, "remove XFCE/Xorg") {
		t.Fatal("Sway installation confirmation does not exclude destructive migration")
	}
}

func TestLightDMConfirmationView(t *testing.T) {
	model := New()
	model.page = menuAutologin
	model.confirming = true
	view := model.View()

	if !strings.Contains(view.Content, "Configure LightDM auto-login?") {
		t.Fatal("LightDM confirmation does not describe the selected action")
	}
	if !strings.Contains(view.Content, "current session will not be stopped") {
		t.Fatal("LightDM confirmation does not state its session-safety behavior")
	}
	if !strings.Contains(view.Content, "sudo will ask") {
		t.Fatal("LightDM confirmation does not explain the privilege prompt")
	}
}

func TestSwayConfirmationView(t *testing.T) {
	model := New()
	model.page = menuAutologin
	model.selected = 1
	model.confirming = true
	view := model.View()

	if !strings.Contains(view.Content, "Configure Sway auto-login?") {
		t.Fatal("Sway confirmation does not describe the selected action")
	}
	if !strings.Contains(view.Content, "one automatic Sway") {
		t.Fatal("Sway confirmation does not explain the login behavior")
	}
	if !strings.Contains(view.Content, "Manual Sway login must work first") {
		t.Fatal("Sway confirmation does not require a successful manual login")
	}
}

func TestAutologinCancellationNamesSelectedAction(t *testing.T) {
	model := New()
	model.page = menuAutologin
	model.selected = 1
	model.confirming = true

	updated, command := model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if command != nil {
		t.Fatal("cancelling should not execute a command")
	}

	result := updated.(Model)
	if result.confirming {
		t.Fatal("cancelling should close the confirmation")
	}
	if result.status != "Sway auto-login configuration cancelled." {
		t.Fatalf("unexpected cancellation status: %q", result.status)
	}
}

func TestRebootConfirmationView(t *testing.T) {
	model := New()
	model.selected = mainMenuActionIndex(t, actionReboot)
	model.confirming = true
	view := model.View()

	if !strings.Contains(view.Content, "Reboot the system now?") {
		t.Fatal("reboot confirmation does not describe the selected action")
	}
	if !strings.Contains(view.Content, "Save all work") {
		t.Fatal("reboot confirmation does not warn about unsaved work")
	}
	if !strings.Contains(view.Content, "Press y to reboot") {
		t.Fatal("reboot confirmation does not require an explicit response")
	}
}

func TestPausingCommandReportsCompletion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runner := &pausingCommand{
		command: exec.Command("true"),
		stdin:   strings.NewReader("\n"),
		stdout:  &stdout,
		stderr:  &stderr,
	}

	if err := runner.Run(); err != nil {
		t.Fatalf("pausing command failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "completed successfully") {
		t.Fatal("pausing command did not report successful completion")
	}
	if !strings.Contains(stdout.String(), "Press Enter to return") {
		t.Fatal("pausing command did not wait before returning to the TUI")
	}
}
