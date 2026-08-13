package ui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestRemoteTerminalDraftDefaults(t *testing.T) {
	draft := newRemoteTerminalDraft()

	if targetUser, err := targetUsername(); err == nil {
		if draft.user != targetUser {
			t.Fatalf("default user = %q; want %q", draft.user, targetUser)
		}
	} else if draft.user != "" {
		t.Fatalf("default user = %q when no target user is available", draft.user)
	}
	if draft.machineName == "" {
		t.Fatal("default machine name is empty")
	}
	if draft.port != "8443" {
		t.Fatalf("default port = %q; want 8443", draft.port)
	}
	if draft.transport != remoteTerminalTransportHTTP {
		t.Fatalf("default transport = %q; want HTTP", draft.transport)
	}
	if draft.listenAddress != "" && !validRemoteTerminalIPv4(draft.listenAddress) {
		t.Fatalf("detected address = %q; want a valid LAN IPv4 address", draft.listenAddress)
	}
}

func TestEnterEditAndLeaveRemoteTerminal(t *testing.T) {
	mainIndex := mainMenuActionIndex(t, actionOpenRemoteTerminal)
	model := New()
	model.selected = mainIndex
	model.prepareSelectedAction()

	if model.page != menuRemoteTerminal || model.currentSection().action != actionInstallRemoteTerminal {
		t.Fatalf("Remote Terminal opened page %d action %d", model.page, model.currentSection().action)
	}
	model.remoteTerminal.focusedField = remoteTerminalMachineNameField
	updated, command := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if command != nil {
		t.Fatal("typing q in the form should not quit")
	}
	result := updated.(Model)
	if !strings.HasSuffix(result.remoteTerminal.machineName, "q") {
		t.Fatalf("typing q did not edit the machine name: %q", result.remoteTerminal.machineName)
	}

	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("leaving Remote Terminal should not execute a command")
	}
	result = updated.(Model)
	if result.page != menuMain || result.selected != mainIndex {
		t.Fatalf("Esc returned to page %d selection %d; want main selection %d", result.page, result.selected, mainIndex)
	}
}

func TestRemoteTerminalFormEditing(t *testing.T) {
	model := New()
	model.remoteTerminal = remoteTerminalDraft{user: "operato", focusedField: remoteTerminalUserField}

	if !model.updateRemoteTerminalForm(tea.KeyPressMsg{Code: 'q', Text: "q"}) {
		t.Fatal("printable q was not consumed by the form")
	}
	if model.remoteTerminal.user != "operatoq" {
		t.Fatalf("edited user = %q", model.remoteTerminal.user)
	}
	if !model.updateRemoteTerminalForm(tea.KeyPressMsg{Code: tea.KeyBackspace}) ||
		model.remoteTerminal.user != "operato" {
		t.Fatalf("backspace produced user %q", model.remoteTerminal.user)
	}
	if !model.updateRemoteTerminalForm(tea.KeyPressMsg{Code: tea.KeyTab}) ||
		model.remoteTerminal.focusedField != remoteTerminalMachineNameField {
		t.Fatalf("Tab selected field %d", model.remoteTerminal.focusedField)
	}
	if !model.updateRemoteTerminalForm(tea.KeyPressMsg{Code: tea.KeyUp}) ||
		model.remoteTerminal.focusedField != remoteTerminalUserField {
		t.Fatalf("Up selected field %d", model.remoteTerminal.focusedField)
	}
	model.remoteTerminal.focusedField = remoteTerminalTransportField
	model.remoteTerminal.transport = remoteTerminalTransportHTTPS
	if !model.updateRemoteTerminalForm(tea.KeyPressMsg{Code: tea.KeyRight}) ||
		model.remoteTerminal.transport != remoteTerminalTransportHTTP {
		t.Fatalf("Right selected transport %q", model.remoteTerminal.transport)
	}
	if !model.updateRemoteTerminalForm(tea.KeyPressMsg{Code: ' ', Text: " "}) ||
		model.remoteTerminal.transport != remoteTerminalTransportHTTPS {
		t.Fatalf("Space selected transport %q", model.remoteTerminal.transport)
	}
	if !model.updateRemoteTerminalForm(tea.KeyPressMsg{Code: 'x', Text: "x"}) ||
		model.remoteTerminal.transport != remoteTerminalTransportHTTPS {
		t.Fatalf("typing changed transport to %q", model.remoteTerminal.transport)
	}
	if !strings.Contains(model.status, "Left/Right") {
		t.Fatalf("transport editing hint = %q", model.status)
	}
	for _, message := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter},
		{Code: tea.KeyEscape},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		if model.updateRemoteTerminalForm(message) {
			t.Fatalf("outer-model key %q was consumed", message.String())
		}
	}
}

func TestRemoteTerminalDraftValidation(t *testing.T) {
	valid := remoteTerminalDraft{
		user:          "operator",
		machineName:   "Workshop Mill",
		listenAddress: "192.168.1.20",
		transport:     remoteTerminalTransportHTTPS,
		port:          "8443",
	}
	if err := validateRemoteTerminalDraftSyntax(valid); err != nil {
		t.Fatalf("valid draft rejected: %v", err)
	}

	tests := []struct {
		name   string
		change func(*remoteTerminalDraft)
		want   string
	}{
		{"user", func(d *remoteTerminalDraft) { d.user = "root:other" }, "user"},
		{"machine", func(d *remoteTerminalDraft) { d.machineName = "Mill #1" }, "machine name"},
		{"loopback", func(d *remoteTerminalDraft) { d.listenAddress = "127.0.0.2" }, "LAN address"},
		{"network address", func(d *remoteTerminalDraft) { d.listenAddress = "10.0.0.0" }, "LAN address"},
		{"bad octet", func(d *remoteTerminalDraft) { d.listenAddress = "192.168.1.256" }, "LAN address"},
		{"transport", func(d *remoteTerminalDraft) { d.transport = "ftp" }, "transport"},
		{"privileged port", func(d *remoteTerminalDraft) { d.port = "443" }, "port"},
		{"large port", func(d *remoteTerminalDraft) { d.port = "65536" }, "port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := valid
			test.change(&draft)
			err := validateRemoteTerminalDraftSyntax(draft)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v; want %q", err, test.want)
			}
		})
	}

	valid.user = "root"
	if err := validateRemoteTerminalDraft(valid); err == nil || !strings.Contains(err.Error(), "non-root") {
		t.Fatalf("root-account validation error = %v", err)
	}
}

func TestRemoteTerminalVirtualInterfacesAreNotSuggested(t *testing.T) {
	for _, name := range []string{"tailscale0", "tun0", "wg0", "docker0", "br-ab12", "virbr0", "veth123"} {
		if !remoteTerminalVirtualInterface(name) {
			t.Errorf("virtual interface %q was not excluded", name)
		}
	}
	for _, name := range []string{"eth0", "enp3s0", "wlan0"} {
		if remoteTerminalVirtualInterface(name) {
			t.Errorf("physical interface %q was excluded", name)
		}
	}
}

func TestRemoteTerminalRenderAndVariables(t *testing.T) {
	draft := remoteTerminalDraft{
		user:          "operator",
		machineName:   "Workshop Mill",
		listenAddress: "192.168.1.20",
		transport:     remoteTerminalTransportHTTPS,
		port:          "9443",
	}
	model := New()
	model.remoteTerminal = draft

	confirmation := strings.Join(model.renderRemoteTerminalForm(true), "\n")
	for _, expected := range []string{
		"Install Remote Terminal?",
		"operator",
		"Workshop Mill",
		"192.168.1.20",
		"Transport:         HTTPS",
		"9443",
		"https://192.168.1.20:9443/",
	} {
		if !strings.Contains(confirmation, expected) {
			t.Errorf("confirmation does not contain %q:\n%s", expected, confirmation)
		}
	}

	variables, err := remoteTerminalPlaybookVariables(draft, "/tmp/remote-source")
	if err != nil {
		t.Fatalf("remoteTerminalPlaybookVariables() error: %v", err)
	}
	if len(variables) != 6 ||
		variables["remoteterminal_user"] != "operator" ||
		variables["remoteterminal_machine_name"] != "Workshop Mill" ||
		variables["remoteterminal_listen_address"] != "192.168.1.20" ||
		variables["remoteterminal_transport"] != remoteTerminalTransportHTTPS ||
		variables["remoteterminal_port"] != 9443 ||
		variables["remoteterminal_source_dir"] != "/tmp/remote-source" {
		t.Fatalf("unexpected playbook variables: %#v", variables)
	}
	if _, err := remoteTerminalPlaybookVariables(draft, "relative/source"); err == nil {
		t.Fatal("relative source directory was accepted")
	}
}

func TestRemoteTerminalHTTPDisclosureAndVariables(t *testing.T) {
	draft := remoteTerminalDraft{
		user:          "operator",
		machineName:   "Workshop Mill",
		listenAddress: "192.168.1.20",
		transport:     remoteTerminalTransportHTTP,
		port:          "8080",
	}
	model := New()
	model.remoteTerminal = draft

	form := strings.Join(model.renderRemoteTerminalForm(false), "\n")
	confirmation := strings.Join(renderCompactRemoteTerminalConfirmation(draft), "\n")
	for name, rendered := range map[string]string{
		"form":         form,
		"confirmation": confirmation,
	} {
		for _, expected := range []string{
			"HTTP",
			"system password",
			"plaintext",
			"code-server",
			"secure-origin",
			"allowlist",
		} {
			if !strings.Contains(rendered, expected) {
				t.Errorf("%s does not contain %q:\n%s", name, expected, rendered)
			}
		}
	}
	if !strings.Contains(confirmation, "http://192.168.1.20:8080/") {
		t.Errorf("confirmation does not contain the HTTP URL:\n%s", confirmation)
	}

	variables, err := remoteTerminalPlaybookVariables(draft, "/tmp/remote-source")
	if err != nil {
		t.Fatalf("remoteTerminalPlaybookVariables() error: %v", err)
	}
	if variables["remoteterminal_transport"] != remoteTerminalTransportHTTP {
		t.Fatalf("transport variable = %#v", variables["remoteterminal_transport"])
	}
}

func TestRemoteTerminalAnsibleArgumentsUseJSON(t *testing.T) {
	variables := map[string]any{
		"remoteterminal_machine_name": "Mill with spaces",
		"remoteterminal_port":         8443,
	}
	arguments, err := remoteTerminalAnsibleArguments("/source/ansible/install.yml", variables)
	if err != nil {
		t.Fatalf("remoteTerminalAnsibleArguments() error: %v", err)
	}

	var decoded map[string]any
	for index, argument := range arguments {
		if argument == "--extra-vars" && index+1 < len(arguments) {
			if err := json.Unmarshal([]byte(arguments[index+1]), &decoded); err != nil {
				t.Fatalf("extra vars are not JSON: %v", err)
			}
		}
	}
	if decoded["remoteterminal_machine_name"] != "Mill with spaces" {
		t.Fatalf("decoded extra vars = %#v", decoded)
	}
	if arguments[len(arguments)-1] != "/source/ansible/install.yml" {
		t.Fatalf("final argument = %q", arguments[len(arguments)-1])
	}
}

func TestPrepareRemoteTerminalInstallRequiresAnsible(t *testing.T) {
	targetUser, err := targetUsername()
	if err != nil {
		t.Skipf("no usable non-root test account: %v", err)
	}
	model := New()
	model.remoteTerminal = remoteTerminalDraft{
		user:          targetUser,
		machineName:   "Workshop Mill",
		listenAddress: "192.168.1.20",
		port:          "8443",
	}
	originalResolver := remoteTerminalAnsibleExecutable
	remoteTerminalAnsibleExecutable = func() (string, error) {
		return "", errors.New("Ansible is unavailable")
	}
	t.Cleanup(func() {
		remoteTerminalAnsibleExecutable = originalResolver
	})

	if model.prepareRemoteTerminalInstall() {
		t.Fatal("installation preparation succeeded without Ansible")
	}
	if model.status != "Install Ansible first, then retry this action." {
		t.Fatalf("unexpected status: %q", model.status)
	}
}

func TestRunRemoteTerminalInstallMaterializesSource(t *testing.T) {
	targetUser, err := targetUsername()
	if err != nil {
		t.Skipf("no usable non-root test account: %v", err)
	}
	binaryDirectory := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "args")
	ansibleFixture := filepath.Join(binaryDirectory, "ansible-playbook")
	originalResolver := remoteTerminalAnsibleExecutable
	originalSudoResolver := remoteTerminalSudoExecutable
	remoteTerminalAnsibleExecutable = func() (string, error) {
		return ansibleFixture, nil
	}
	remoteTerminalSudoExecutable = func() (string, error) {
		return filepath.Join(binaryDirectory, "sudo"), nil
	}
	t.Cleanup(func() {
		remoteTerminalAnsibleExecutable = originalResolver
		remoteTerminalSudoExecutable = originalSudoResolver
	})
	writeExecutableTestFixture(t, ansibleFixture, `#!/bin/sh
last=""
: > "$REMOTE_TERMINAL_CAPTURE"
for argument in "$@"; do
    printf '%s\n' "$argument" >> "$REMOTE_TERMINAL_CAPTURE"
    last="$argument"
done
test -f "$last"
`)
	writeExecutableTestFixture(t, filepath.Join(binaryDirectory, "sudo"), `#!/bin/sh
if [ "$1" = "--" ]; then shift; fi
exec "$@"
`)
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REMOTE_TERMINAL_CAPTURE", capturePath)

	model := New()
	model.remoteTerminal = remoteTerminalDraft{
		user: targetUser, machineName: "Workshop Mill",
		listenAddress: "192.168.1.20", port: "8443",
	}
	finished := executeIRQDeviceTeaCommand(t, model.runRemoteTerminalInstall())
	if finished.action != actionInstallRemoteTerminal || finished.err != nil {
		t.Fatalf("installation command result = %#v", finished)
	}

	arguments := readCapturedIRQDeviceArgs(t, capturePath)
	variables := capturedIRQDeviceExtraVars(t, arguments)
	sourceDirectory, ok := variables["remoteterminal_source_dir"].(string)
	if !ok || !filepath.IsAbs(sourceDirectory) {
		t.Fatalf("source directory variable = %#v", variables["remoteterminal_source_dir"])
	}
	if _, err := os.Stat(sourceDirectory); !os.IsNotExist(err) {
		t.Fatalf("materialized source was not cleaned up: %v", err)
	}
}
