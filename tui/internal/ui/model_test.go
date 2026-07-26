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
	if mainSections[1].action != actionInstallSway {
		t.Fatal("Sway installation should follow Ansible installation")
	}
	if mainSections[2].action != actionOpenLinuxCNCAutostart {
		t.Fatal("LinuxCNC autostart should follow Sway installation")
	}
	if mainSections[3].action != actionOpenAutologin {
		t.Fatal("automatic login should follow LinuxCNC autostart")
	}
	if autologinSections[0].action != actionAutologinLightDM {
		t.Fatal("LightDM should be the first automatic-login option")
	}
	if autologinSections[1].action != actionAutologinSway {
		t.Fatal("Sway should be the second automatic-login option")
	}
	if autologinSections[2].action != actionBack {
		t.Fatal("automatic-login submenu should end with a back action")
	}
	if mainSections[4].action != actionReboot {
		t.Fatal("reboot should follow the automatic-login submenu")
	}
}

func TestEnterAndLeaveAutologinSubmenu(t *testing.T) {
	model := New()
	model.selected = 3
	model.prepareSelectedAction()

	if model.page != menuAutologin || model.selected != 0 {
		t.Fatalf("entering submenu produced page %d selection %d", model.page, model.selected)
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving a submenu should not execute a command")
	}

	result := updated.(Model)
	if result.page != menuMain || result.selected != 3 {
		t.Fatalf("Esc returned to page %d selection %d", result.page, result.selected)
	}
}

func TestLinuxCNCAutostartSubmenuStructure(t *testing.T) {
	if linuxCNCAutostartSections[0].action != actionOpenLinuxCNCAutostartSway {
		t.Fatal("Sway should be the first LinuxCNC autostart option")
	}
	if linuxCNCAutostartSections[1].action != actionLinuxCNCAutostartX11 {
		t.Fatal("X11 should be the second LinuxCNC autostart option")
	}
	if linuxCNCAutostartSections[2].action != actionBack {
		t.Fatal("LinuxCNC autostart submenu should end with a back action")
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

	model := New()
	model.selected = 2
	model.prepareSelectedAction()
	if model.page != menuLinuxCNCAutostart || model.selected != 0 {
		t.Fatalf("entering platform menu produced page %d selection %d", model.page, model.selected)
	}

	model.prepareSelectedAction()
	if model.page != menuLinuxCNCConfigs || model.selected != 0 {
		t.Fatalf("entering config menu produced page %d selection %d", model.page, model.selected)
	}
	if len(model.visibleSections()) != 2 {
		t.Fatalf("config menu has %d entries; want one config and Back", len(model.visibleSections()))
	}
	if model.currentSection().value != configPath {
		t.Fatalf("selected config %q; want %q", model.currentSection().value, configPath)
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving the config menu should not execute a command")
	}
	result := updated.(Model)
	if result.page != menuLinuxCNCAutostart || result.selected != 0 {
		t.Fatalf("Esc returned to page %d selection %d", result.page, result.selected)
	}

	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving the platform menu should not execute a command")
	}
	result = updated.(Model)
	if result.page != menuMain || result.selected != 2 {
		t.Fatalf("second Esc returned to page %d selection %d", result.page, result.selected)
	}
}

func TestLinuxCNCAutostartX11IsUnimplemented(t *testing.T) {
	model := New()
	model.page = menuLinuxCNCAutostart
	model.selected = 1
	model.prepareSelectedAction()

	if model.confirming {
		t.Fatal("X11 placeholder should not open a confirmation")
	}
	if !strings.Contains(model.status, "not implemented") {
		t.Fatalf("unexpected X11 placeholder status: %q", model.status)
	}
}

func TestLinuxCNCAutostartConfirmationView(t *testing.T) {
	configPath := "/home/user/linuxcnc/configs/mill profile/mill.ini"
	model := New()
	model.page = menuLinuxCNCConfigs
	model.linuxCNCSections = linuxCNCConfigSections([]linuxCNCConfig{{
		label: "mill",
		path:  configPath,
	}})
	model.width = 160
	model.confirming = true
	view := model.View()

	for _, expected := range []string{
		"Enable LinuxCNC autostart?",
		configPath,
		"workspace 1",
		"next Sway login",
		"machine",
		"will not be launched now",
	} {
		if !strings.Contains(view.Content, expected) {
			t.Fatalf("confirmation view does not contain %q", expected)
		}
	}
}

func TestSwayInstallationConfirmationView(t *testing.T) {
	model := New()
	model.selected = 1
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
	model.selected = 4
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
