package ui

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	tea "charm.land/bubbletea/v2"

	remoteterminalassets "github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal"
)

const (
	remoteTerminalUserField = iota
	remoteTerminalMachineNameField
	remoteTerminalListenAddressField
	remoteTerminalTransportField
	remoteTerminalPortField
	remoteTerminalFieldCount
)

const (
	remoteTerminalTransportHTTPS = "https"
	remoteTerminalTransportHTTP  = "http"
)

var (
	remoteTerminalUsernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[$]?$`)
	remoteTerminalMachinePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
)

const (
	remoteTerminalAnsiblePath = "/usr/bin/ansible-playbook"
	remoteTerminalSudoPath    = "/usr/bin/sudo"
)

var remoteTerminalAnsibleExecutable = trustedRemoteTerminalAnsibleExecutable
var remoteTerminalSudoExecutable = trustedRemoteTerminalSudoExecutable

// remoteTerminalDraft is the editable set of required installer inputs. None
// of these values are credentials; the account password is entered only in the
// Remote Terminal login page after installation.
type remoteTerminalDraft struct {
	user          string
	machineName   string
	listenAddress string
	transport     string
	port          string
	focusedField  int
}

func newRemoteTerminalDraft() remoteTerminalDraft {
	targetUser, _ := targetUsername()
	hostname, _ := os.Hostname()
	if strings.TrimSpace(hostname) == "" {
		hostname = "LinuxCNC machine"
	}

	return remoteTerminalDraft{
		user:          targetUser,
		machineName:   strings.TrimSpace(hostname),
		listenAddress: firstRemoteTerminalIPv4(),
		transport:     remoteTerminalTransportHTTP,
		port:          "8443",
	}
}

// updateRemoteTerminalForm applies an editing key and reports whether it was
// consumed. Enter, Escape, and Ctrl-C remain available to the root model for
// review, navigation, and quitting. Printable text, including "q", belongs to
// editable text fields; the transport toggle uses Left/Right or Space.
func (m *Model) updateRemoteTerminalForm(message tea.KeyPressMsg) bool {
	key := message.Key()
	if key.Code == tea.KeyEnter || key.Code == tea.KeyReturn ||
		key.Code == tea.KeyEscape || message.String() == "ctrl+c" {
		return false
	}

	switch message.String() {
	case "tab", "down":
		m.remoteTerminal.focusedField =
			(m.remoteTerminal.focusedField + 1) % remoteTerminalFieldCount
		m.status = ""
		return true
	case "shift+tab", "up":
		m.remoteTerminal.focusedField =
			(m.remoteTerminal.focusedField + remoteTerminalFieldCount - 1) % remoteTerminalFieldCount
		m.status = ""
		return true
	case "left", "right", " ", "space":
		if m.remoteTerminal.focusedField == remoteTerminalTransportField {
			m.remoteTerminal.toggleTransport()
			m.status = ""
			return true
		}
	case "backspace":
		if m.remoteTerminal.focusedField == remoteTerminalTransportField {
			m.status = "Use Left/Right or Space to choose HTTPS or HTTP."
			return true
		}
		value := []rune(m.remoteTerminal.fieldValue(m.remoteTerminal.focusedField))
		if len(value) > 0 {
			m.remoteTerminal.setFieldValue(
				m.remoteTerminal.focusedField,
				string(value[:len(value)-1]),
			)
		}
		m.status = ""
		return true
	}

	if key.Text == "" {
		return false
	}
	if m.remoteTerminal.focusedField == remoteTerminalTransportField {
		m.status = "Use Left/Right or Space to choose HTTPS or HTTP."
		return true
	}
	for _, character := range key.Text {
		if unicode.IsControl(character) {
			m.status = "Line breaks and control characters are not allowed in installer fields."
			return true
		}
	}
	m.remoteTerminal.setFieldValue(
		m.remoteTerminal.focusedField,
		m.remoteTerminal.fieldValue(m.remoteTerminal.focusedField)+key.Text,
	)
	m.status = ""
	return true
}

func (draft remoteTerminalDraft) fieldValue(field int) string {
	switch field {
	case remoteTerminalUserField:
		return draft.user
	case remoteTerminalMachineNameField:
		return draft.machineName
	case remoteTerminalListenAddressField:
		return draft.listenAddress
	case remoteTerminalTransportField:
		return draft.transport
	case remoteTerminalPortField:
		return draft.port
	default:
		return ""
	}
}

func (draft *remoteTerminalDraft) setFieldValue(field int, value string) {
	switch field {
	case remoteTerminalUserField:
		draft.user = value
	case remoteTerminalMachineNameField:
		draft.machineName = value
	case remoteTerminalListenAddressField:
		draft.listenAddress = value
	case remoteTerminalTransportField:
		draft.transport = value
	case remoteTerminalPortField:
		draft.port = value
	}
}

func (draft remoteTerminalDraft) normalized() remoteTerminalDraft {
	draft.user = strings.TrimSpace(draft.user)
	draft.machineName = strings.TrimSpace(draft.machineName)
	draft.listenAddress = strings.TrimSpace(draft.listenAddress)
	draft.transport = strings.ToLower(strings.TrimSpace(draft.transport))
	if draft.transport == "" {
		draft.transport = remoteTerminalTransportHTTP
	}
	draft.port = strings.TrimSpace(draft.port)
	return draft
}

func (draft *remoteTerminalDraft) toggleTransport() {
	if draft.normalized().transport == remoteTerminalTransportHTTP {
		draft.transport = remoteTerminalTransportHTTPS
		return
	}
	draft.transport = remoteTerminalTransportHTTP
}

func (draft remoteTerminalDraft) transportDisplayValue() string {
	if draft.normalized().transport == remoteTerminalTransportHTTP {
		return "HTTP (LAN default)"
	}
	return "HTTPS (optional)"
}

func (draft remoteTerminalDraft) endpointURL() string {
	draft = draft.normalized()
	return fmt.Sprintf("%s://%s:%s/", draft.transport, draft.listenAddress, draft.port)
}

func (m Model) renderRemoteTerminalForm(confirming bool) []string {
	draft := m.remoteTerminal
	if confirming {
		draft = draft.normalized()
		lines := []string{
			warningStyle.Render("Install Remote Terminal?"),
			"",
			fmt.Sprintf("Linux system user: %s", draft.user),
			fmt.Sprintf("Machine name:      %s", draft.machineName),
			fmt.Sprintf("LAN IPv4 address:  %s", draft.listenAddress),
			fmt.Sprintf("Transport:         %s", strings.ToUpper(draft.transport)),
			fmt.Sprintf("Port:              %s", draft.port),
			"",
			fmt.Sprintf("Endpoint: %s", draft.endpointURL()),
			"",
			"Ansible builds and installs the service,",
			"web application, and pinned ttyd dependency.",
		}
		if draft.transport == remoteTerminalTransportHTTP {
			lines = append(lines,
				"",
				"DANGER: HTTP sends the Linux system password",
				"and terminal traffic over the LAN in plaintext.",
				"code-server webviews require this exact HTTP origin",
				"in the client browser's secure-origin allowlist.",
			)
			return append(lines, "", "sudo will ask for your account password.", "Press y to continue or n to cancel.")
		}
		return append(lines,
			"It creates a self-signed TLS certificate",
			"unless trusted TLS material is supplied later.",
			"",
			"sudo will ask for your account password.",
			"Press y to continue or n to cancel.",
		)
	}

	labels := []string{
		"Linux system user",
		"Machine name",
		"LAN IPv4 address",
		"Transport",
		"Port",
	}
	lines := []string{
		"Choose the local account that owns terminal sessions",
		"and the transport used by the LAN endpoint.",
		"",
	}
	for field, label := range labels {
		prefix := "  "
		if field == draft.focusedField {
			prefix = "› "
		}
		value := draft.fieldValue(field)
		if field == remoteTerminalTransportField {
			value = draft.transportDisplayValue()
		}
		if value == "" {
			value = "(required)"
		}
		lines = append(lines, fmt.Sprintf("%s%-18s %s", prefix, label+":", value))
	}
	if draft.normalized().transport == remoteTerminalTransportHTTP {
		lines = append(lines,
			"",
			"DANGER: HTTP sends the system password and",
			"terminal traffic over the LAN in plaintext.",
			"code-server webviews need this origin in",
			"the client browser secure-origin allowlist.",
		)
	}
	return append(lines,
		"",
		"Type to edit; Left/Right or Space selects transport.",
		"Tab or ↑/↓ changes field.",
		"Press Enter to validate and review the installation.",
	)
}

func renderCompactRemoteTerminalConfirmation(draft remoteTerminalDraft) []string {
	draft = draft.normalized()
	lines := []string{
		warningStyle.Render("Install Remote Terminal?"),
		fmt.Sprintf("Machine: %s; user: %s", draft.machineName, draft.user),
		fmt.Sprintf("%s %s", strings.ToUpper(draft.transport), draft.endpointURL()),
	}
	if draft.transport == remoteTerminalTransportHTTP {
		return append(lines,
			"DANGER: system password is",
			"plaintext over LAN HTTP.",
			"Terminal traffic is plaintext.",
			"code-server webviews need this",
			"origin in the client browser",
			"secure-origin allowlist.",
			"sudo will ask for your password.",
			"Press y to install; n to cancel.",
		)
	}
	return append(lines,
		"Builds service, web UI, and ttyd.",
		"TLS: self-signed certificate.",
		"sudo will ask for your password.",
		"Press y to install; n to cancel.",
	)
}

func (m *Model) prepareRemoteTerminalInstall() bool {
	m.remoteTerminal = m.remoteTerminal.normalized()
	if err := validateRemoteTerminalDraft(m.remoteTerminal); err != nil {
		m.status = fmt.Sprintf("Cannot install Remote Terminal: %v", err)
		return false
	}
	if _, err := remoteTerminalAnsibleExecutable(); err != nil {
		m.status = "Install Ansible first, then retry this action."
		return false
	}
	if os.Geteuid() != 0 {
		if _, err := remoteTerminalSudoExecutable(); err != nil {
			m.status = "Cannot install Remote Terminal: sudo was not found."
			return false
		}
	}
	m.status = ""
	return true
}

func trustedRemoteTerminalAnsibleExecutable() (string, error) {
	return trustedRemoteTerminalExecutable(remoteTerminalAnsiblePath)
}

func trustedRemoteTerminalSudoExecutable() (string, error) {
	return trustedRemoteTerminalExecutable(remoteTerminalSudoPath)
}

func trustedRemoteTerminalExecutable(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not a trusted executable", path)
	}
	if system, ok := info.Sys().(*syscall.Stat_t); !ok || system.Uid != 0 {
		return "", fmt.Errorf("%s is not owned by root", path)
	}
	return path, nil
}

func validateRemoteTerminalDraft(draft remoteTerminalDraft) error {
	draft = draft.normalized()
	if err := validateRemoteTerminalDraftSyntax(draft); err != nil {
		return err
	}

	account, err := user.Lookup(draft.user)
	if err != nil {
		return fmt.Errorf("Linux system user %q does not exist", draft.user)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("Linux system user %q has an invalid UID", draft.user)
	}
	if uid == 0 {
		return fmt.Errorf("Linux system user must be a non-root account")
	}
	return nil
}

func validateRemoteTerminalDraftSyntax(draft remoteTerminalDraft) error {
	if !remoteTerminalUsernamePattern.MatchString(draft.user) {
		return fmt.Errorf("Linux system user is invalid")
	}
	if !remoteTerminalMachinePattern.MatchString(draft.machineName) {
		return fmt.Errorf("machine name must be 1-64 characters and use letters, numbers, spaces, dots, underscores, or hyphens")
	}
	if !validRemoteTerminalIPv4(draft.listenAddress) {
		return fmt.Errorf("LAN address must be a valid non-loopback IPv4 address whose final octet is not 0 or 255")
	}
	if draft.transport != remoteTerminalTransportHTTPS && draft.transport != remoteTerminalTransportHTTP {
		return fmt.Errorf("transport must be HTTPS or HTTP")
	}
	if draft.port == "" {
		return fmt.Errorf("port must be between 1024 and 65535")
	}
	for _, character := range draft.port {
		if character < '0' || character > '9' {
			return fmt.Errorf("port must be between 1024 and 65535")
		}
	}
	port, err := strconv.ParseUint(draft.port, 10, 16)
	if err != nil || port < 1024 || port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}
	return nil
}

func validRemoteTerminalIPv4(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return false
	}
	octets := make([]int, len(parts))
	for index, part := range parts {
		if part == "" || len(part) > 3 {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		value, err := strconv.Atoi(part)
		if err != nil || value > 255 {
			return false
		}
		octets[index] = value
	}
	return octets[0] >= 1 && octets[0] <= 223 && octets[0] != 127 &&
		octets[3] != 0 && octets[3] != 255
}

func firstRemoteTerminalIPv4() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if remoteTerminalVirtualInterface(networkInterface.Name) {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || !ip.IsGlobalUnicast() {
				continue
			}
			candidate := ip.To4().String()
			if validRemoteTerminalIPv4(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func remoteTerminalVirtualInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{
		"tailscale", "tun", "tap", "wg", "zt", "docker", "br-", "virbr", "veth",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func remoteTerminalPlaybookVariables(
	draft remoteTerminalDraft,
	sourceDirectory string,
) (map[string]any, error) {
	draft = draft.normalized()
	if err := validateRemoteTerminalDraftSyntax(draft); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(sourceDirectory) {
		return nil, fmt.Errorf("Remote Terminal source directory must be absolute")
	}
	port, err := strconv.Atoi(draft.port)
	if err != nil {
		return nil, fmt.Errorf("parse port: %w", err)
	}
	return map[string]any{
		"remoteterminal_user":           draft.user,
		"remoteterminal_machine_name":   draft.machineName,
		"remoteterminal_listen_address": draft.listenAddress,
		"remoteterminal_transport":      draft.transport,
		"remoteterminal_port":           port,
		"remoteterminal_source_dir":     sourceDirectory,
	}, nil
}

func remoteTerminalAnsibleArguments(
	playbookPath string,
	variables map[string]any,
) ([]string, error) {
	extraVariables, err := json.Marshal(variables)
	if err != nil {
		return nil, fmt.Errorf("encode Remote Terminal playbook variables: %w", err)
	}
	return []string{
		"--inventory", "localhost,",
		"--connection", "local",
		"--diff",
		"--become",
		"--extra-vars", string(extraVariables),
		playbookPath,
	}, nil
}

func (m Model) runRemoteTerminalInstall() tea.Cmd {
	ansiblePath, err := remoteTerminalAnsibleExecutable()
	if err != nil {
		return remoteTerminalFinishedCommand(fmt.Errorf("validate ansible-playbook: %w", err))
	}

	sourceDirectory, playbookPath, cleanup, err := remoteterminalassets.Materialize()
	if err != nil {
		return remoteTerminalFinishedCommand(err)
	}
	variables, err := remoteTerminalPlaybookVariables(m.remoteTerminal, sourceDirectory)
	if err != nil {
		cleanup()
		return remoteTerminalFinishedCommand(err)
	}
	arguments, err := remoteTerminalAnsibleArguments(playbookPath, variables)
	if err != nil {
		cleanup()
		return remoteTerminalFinishedCommand(err)
	}

	var command *exec.Cmd
	if os.Geteuid() == 0 {
		command = exec.Command(ansiblePath, arguments...)
	} else {
		sudoPath, err := remoteTerminalSudoExecutable()
		if err != nil {
			cleanup()
			return remoteTerminalFinishedCommand(fmt.Errorf("validate sudo: %w", err))
		}
		sudoArguments := append([]string{"--", ansiblePath}, arguments...)
		command = exec.Command(sudoPath, sudoArguments...)
	}
	runner := &pausingCommand{command: command}
	return tea.Exec(runner, func(err error) tea.Msg {
		cleanup()
		return actionFinishedMsg{action: actionInstallRemoteTerminal, err: err}
	})
}

func remoteTerminalFinishedCommand(err error) tea.Cmd {
	return func() tea.Msg {
		return actionFinishedMsg{action: actionInstallRemoteTerminal, err: err}
	}
}
