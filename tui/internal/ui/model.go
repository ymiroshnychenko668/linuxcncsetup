package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
	sidebarWidth  = 28
)

type sectionAction int

const (
	actionNone sectionAction = iota
	actionInstallAnsible
	actionInstallSway
	actionOpenLinuxCNCAutostart
	actionOpenLinuxCNCAutostartSway
	actionLinuxCNCAutostartX11
	actionConfigureLinuxCNCAutostartSway
	actionOpenAutologin
	actionAutologinLightDM
	actionAutologinSway
	actionReboot
	actionBack
)

type menuPage int

const (
	menuMain menuPage = iota
	menuLinuxCNCAutostart
	menuLinuxCNCConfigs
	menuAutologin
)

type section struct {
	title       string
	description string
	action      sectionAction
	value       string
}

var mainSections = []section{
	{
		title:       "Install Ansible",
		description: "Install Ansible from Debian's package repositories.",
		action:      actionInstallAnsible,
	},
	{
		title:       "Install Sway",
		description: "Add the complete Sway desktop without changing the active display manager.",
		action:      actionInstallSway,
	},
	{
		title:       "LinuxCNC autostart",
		description: "Choose a desktop and LinuxCNC configuration to start automatically.",
		action:      actionOpenLinuxCNCAutostart,
	},
	{
		title:       "Automatic login",
		description: "Choose automatic login through LightDM or greetd with Sway.",
		action:      actionOpenAutologin,
	},
	{
		title:       "Reboot system",
		description: "Reboot the workstation after saving all current work.",
		action:      actionReboot,
	},
	{
		title:       "System overview",
		description: "Inspect the workstation before making changes.",
	},
	{
		title:       "Configuration",
		description: "Configure LinuxCNC, networking, and desktop integration.",
	},
	{
		title:       "Diagnostics",
		description: "Run read-only checks and review their output.",
	},
}

var linuxCNCAutostartSections = []section{
	{
		title:       "Sway (Wayland)",
		description: "Choose a LinuxCNC configuration to start on workspace 1 at the next Sway login.",
		action:      actionOpenLinuxCNCAutostartSway,
	},
	{
		title:       "X11",
		description: "X11 LinuxCNC autostart is not implemented yet.",
		action:      actionLinuxCNCAutostartX11,
	},
	{
		title:       "← Back",
		description: "Return to the main menu.",
		action:      actionBack,
	},
}

var autologinSections = []section{
	{
		title:       "LightDM",
		description: "Keep LightDM and automatically start the user's current or default session.",
		action:      actionAutologinLightDM,
	},
	{
		title:       "Sway (Wayland)",
		description: "Use greetd to automatically start a Sway session once per boot.",
		action:      actionAutologinSway,
	},
	{
		title:       "← Back",
		description: "Return to the main menu.",
		action:      actionBack,
	},
}

var (
	appStyle = lipgloss.NewStyle().
			Padding(1, 2)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F0C674"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#282A2E")).
			Background(lipgloss.Color("#8ABEB7")).
			Padding(0, 1)

	itemStyle = lipgloss.NewStyle().
			Padding(0, 1)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#707880")).
			Padding(1, 2)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#707880"))

	warningStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#A54242"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8ABEB7"))
)

// Model is the root Bubble Tea model.
type Model struct {
	width            int
	height           int
	page             menuPage
	selected         int
	confirming       bool
	status           string
	linuxCNCSections []section
}

type actionFinishedMsg struct {
	action sectionAction
	err    error
}

type commandSpec struct {
	name string
	args []string
}

type commandSequence struct {
	commands []commandSpec
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
}

type pausingCommand struct {
	command *exec.Cmd
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

func (sequence *commandSequence) Run() error {
	for _, spec := range sequence.commands {
		command := exec.Command(spec.name, spec.args...)
		command.Stdin = sequence.stdin
		command.Stdout = sequence.stdout
		command.Stderr = sequence.stderr

		if err := command.Run(); err != nil {
			return fmt.Errorf("%s: %w", spec.name, err)
		}
	}
	return nil
}

func (sequence *commandSequence) SetStdin(reader io.Reader) {
	sequence.stdin = reader
}

func (sequence *commandSequence) SetStdout(writer io.Writer) {
	sequence.stdout = writer
}

func (sequence *commandSequence) SetStderr(writer io.Writer) {
	sequence.stderr = writer
}

func (command *pausingCommand) Run() error {
	command.command.Stdin = command.stdin
	command.command.Stdout = command.stdout
	command.command.Stderr = command.stderr

	err := command.command.Run()
	output := command.stdout
	if output == nil {
		output = io.Discard
	}
	errorOutput := command.stderr
	if errorOutput == nil {
		errorOutput = output
	}

	if err != nil {
		fmt.Fprintf(errorOutput, "\nPlaybook failed: %v\n", err)
	} else {
		fmt.Fprintln(output, "\nPlaybook completed successfully.")
	}
	fmt.Fprint(output, "Press Enter to return to LinuxCNC Setup...")

	if command.stdin != nil {
		_, _ = bufio.NewReader(command.stdin).ReadString('\n')
	}
	return err
}

func (command *pausingCommand) SetStdin(reader io.Reader) {
	command.stdin = reader
}

func (command *pausingCommand) SetStdout(writer io.Writer) {
	command.stdout = writer
}

func (command *pausingCommand) SetStderr(writer io.Writer) {
	command.stderr = writer
}

// New constructs the initial application model.
func New() Model {
	return Model{
		width:  defaultWidth,
		height: defaultHeight,
	}
}

// Init implements tea.Model.
func (Model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height

	case actionFinishedMsg:
		m.confirming = false
		if message.err != nil {
			m.status = fmt.Sprintf("%s failed: %v", actionName(message.action), message.err)
		} else {
			m.status = actionSuccessMessage(message.action)
		}

	case tea.KeyPressMsg:
		key := message.String()
		if m.confirming {
			switch key {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "n", "esc":
				action := m.currentSection().action
				m.confirming = false
				m.status = actionCancelledMessage(action)
			case "y":
				current := m.currentSection()
				m.confirming = false
				m.status = actionRunningMessage(current.action)
				return m, executeAction(current.action, current.value)
			}
			return m, nil
		}

		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.moveSelection(-1)
		case "down", "j":
			m.moveSelection(1)
		case "enter":
			m.prepareSelectedAction()
		case "esc":
			m.back()
		}
	}

	return m, nil
}

// View implements tea.Model.
func (m Model) View() tea.View {
	contentWidth := max(m.width-appStyle.GetHorizontalFrameSize(), 1)
	contentHeight := max(m.height-appStyle.GetVerticalFrameSize(), 1)
	bodyHeight := max(contentHeight-3, 1)
	detailWidth := max(contentWidth-sidebarWidth-1, 20)

	sidebar := panelStyle.
		Width(sidebarWidth).
		Height(bodyHeight).
		Render(m.renderSidebar())

	detail := panelStyle.
		Width(detailWidth).
		Height(bodyHeight).
		Render(m.renderDetail())

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, detail)
	helpText := "↑/k up • ↓/j down • Enter select • q quit"
	if m.page != menuMain {
		helpText = "↑/k up • ↓/j down • Enter select • Esc back • q quit"
	}
	if m.confirming {
		helpText = "y confirm • n/Esc cancel • q quit"
	}
	help := helpStyle.Render(helpText)
	content := appStyle.Render(lipgloss.JoinVertical(lipgloss.Left, body, help))

	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "LinuxCNC Setup"
	return view
}

func (m Model) renderSidebar() string {
	var builder strings.Builder
	builder.WriteString(titleStyle.Render(m.pageTitle()))
	builder.WriteString("\n\n")

	for index, section := range m.visibleSections() {
		style := itemStyle
		prefix := "  "
		if index == m.selected {
			style = selectedStyle
			prefix = "› "
		}

		builder.WriteString(style.Render(prefix + section.title))
		builder.WriteString("\n")
	}

	return builder.String()
}

func (m Model) renderDetail() string {
	current := m.currentSection()
	lines := []string{
		titleStyle.Render(current.title),
		"",
		current.description,
		"",
	}

	switch current.action {
	case actionInstallAnsible:
		if m.confirming {
			lines = append(lines,
				warningStyle.Render("Install Ansible now?"),
				"",
				"This will run:",
				"  sudo apt-get update",
				"  sudo apt-get install -y ansible",
				"",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, "Press Enter to begin.")
		}
	case actionInstallSway:
		if m.confirming {
			lines = append(lines,
				warningStyle.Render("Install Wayland + Sway?"),
				"",
				"Ansible installs the Sway desktop,",
				"backs up and writes user configuration,",
				"and prepares PipeWire for the next login.",
				"",
				"It will not switch display managers,",
				"remove XFCE/Xorg, or reboot.",
				"sudo will ask for your account password.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, "Press Enter to install and configure with Ansible.")
		}
	case actionOpenLinuxCNCAutostart:
		lines = append(lines, "Press Enter to choose Sway or X11.")
	case actionOpenLinuxCNCAutostartSway:
		lines = append(lines,
			"Press Enter to scan the current user's",
			"~/linuxcnc/configs directory.",
		)
	case actionLinuxCNCAutostartX11:
		lines = append(lines, warningStyle.Render("X11 support is not implemented yet."))
	case actionConfigureLinuxCNCAutostartSway:
		if m.confirming {
			lines = append(lines,
				warningStyle.Render("Enable LinuxCNC autostart?"),
				"",
				"Selected configuration:",
				"  "+current.value,
				"",
				"Ansible will configure Sway to select",
				"workspace 1 and launch this configuration",
				"once at the next Sway login.",
				"",
				"LinuxCNC may initialize connected machine",
				"hardware when that session starts.",
				"LinuxCNC will not be launched now.",
				"sudo will ask for your account password.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, "Press Enter to configure with Ansible.")
		}
	case actionOpenAutologin:
		lines = append(lines, "Press Enter to choose an automatic-login method.")
	case actionAutologinLightDM:
		if m.confirming {
			lines = append(lines,
				warningStyle.Render("Configure LightDM auto-login?"),
				"",
				"Ansible will install LightDM, write an",
				"auto-login drop-in, disable greetd at",
				"the next boot, and enable LightDM.",
				"",
				"sudo will ask for your account password.",
				"The current session will not be stopped.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, "Press Enter to configure with Ansible.")
		}
	case actionAutologinSway:
		if m.confirming {
			lines = append(lines,
				warningStyle.Render("Configure Sway auto-login?"),
				"",
				"Manual Sway login must work first.",
				"",
				"sudo installs greetd and tuigreet,",
				"configures one automatic Sway login",
				"per boot, and selects greetd next boot.",
				"",
				"Your current session stays active.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, "Press Enter to configure with Ansible.")
		}
	case actionReboot:
		if m.confirming {
			lines = append(lines,
				warningStyle.Render("Reboot the system now?"),
				"",
				"Save all work before continuing.",
				"This will immediately close every session",
				"and reboot the workstation.",
				"",
				"Press y to reboot or n to cancel.",
			)
		} else {
			lines = append(lines, "Press Enter to request a reboot.")
		}
	case actionBack:
		lines = append(lines, "Press Enter or Esc to return.")
	default:
		lines = append(lines, "This section is not implemented yet.")
	}

	if m.status != "" {
		lines = append(lines, "", statusStyle.Render(m.status))
	}

	lines = append(lines, "", fmt.Sprintf("Terminal: %d×%d", m.width, m.height))
	return strings.Join(lines, "\n")
}

func (m *Model) moveSelection(delta int) {
	sections := m.visibleSections()
	m.selected = (m.selected + delta + len(sections)) % len(sections)
	m.status = ""
}

func (m *Model) prepareSelectedAction() {
	current := m.currentSection()
	switch current.action {
	case actionOpenLinuxCNCAutostart:
		m.openPage(menuLinuxCNCAutostart)
		return

	case actionOpenLinuxCNCAutostartSway:
		configs, root, err := discoverLinuxCNCConfigs()
		if err != nil {
			m.status = fmt.Sprintf("Cannot list LinuxCNC configurations: %v", err)
			return
		}
		m.linuxCNCSections = linuxCNCConfigSections(configs)
		m.openPage(menuLinuxCNCConfigs)
		if len(configs) == 0 {
			m.status = fmt.Sprintf("No .ini configurations found under %s.", root)
		} else {
			m.status = fmt.Sprintf("Found %d configuration(s) under %s.", len(configs), root)
		}
		return

	case actionLinuxCNCAutostartX11:
		m.status = "X11 LinuxCNC autostart is not implemented yet."
		return

	case actionOpenAutologin:
		m.openPage(menuAutologin)
		return

	case actionInstallAnsible:
		if _, err := exec.LookPath("ansible"); err == nil {
			m.status = "Ansible is already installed."
			return
		}
		if _, err := exec.LookPath("apt-get"); err != nil {
			m.status = "Cannot install Ansible: apt-get was not found."
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot install Ansible: sudo was not found."
				return
			}
		}

	case actionInstallSway:
		if _, err := exec.LookPath("ansible-playbook"); err != nil {
			m.status = "Install Ansible first, then retry this action."
			return
		}
		if _, err := targetUsername(); err != nil {
			m.status = fmt.Sprintf("Cannot install Sway: %v", err)
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot install Sway: sudo was not found."
				return
			}
		}

	case actionConfigureLinuxCNCAutostartSway:
		if _, err := exec.LookPath("ansible-playbook"); err != nil {
			m.status = "Install Ansible first, then retry this action."
			return
		}
		if _, err := targetUsername(); err != nil {
			m.status = fmt.Sprintf("Cannot configure LinuxCNC autostart: %v", err)
			return
		}
		if err := validateLinuxCNCConfig(current.value); err != nil {
			m.status = fmt.Sprintf("Cannot configure LinuxCNC autostart: %v", err)
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot configure LinuxCNC autostart: sudo was not found."
				return
			}
		}

	case actionAutologinLightDM, actionAutologinSway:
		if _, err := exec.LookPath("ansible-playbook"); err != nil {
			m.status = "Install Ansible first, then retry this action."
			return
		}
		if _, err := targetUsername(); err != nil {
			m.status = fmt.Sprintf("Cannot configure auto-login: %v", err)
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot configure auto-login: sudo was not found."
				return
			}
		}

	case actionReboot:
		if _, err := exec.LookPath("systemctl"); err != nil {
			m.status = "Cannot reboot: systemctl was not found."
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot reboot: sudo was not found."
				return
			}
		}

	case actionBack:
		m.back()
		return

	default:
		m.status = "This section is not implemented yet."
		return
	}

	m.status = ""
	m.confirming = true
}

func (m Model) visibleSections() []section {
	switch m.page {
	case menuLinuxCNCAutostart:
		return linuxCNCAutostartSections
	case menuLinuxCNCConfigs:
		if len(m.linuxCNCSections) == 0 {
			return linuxCNCConfigSections(nil)
		}
		return m.linuxCNCSections
	case menuAutologin:
		return autologinSections
	default:
		return mainSections
	}
}

func (m Model) currentSection() section {
	return m.visibleSections()[m.selected]
}

func (m Model) pageTitle() string {
	switch m.page {
	case menuLinuxCNCAutostart:
		return "LinuxCNC autostart"
	case menuLinuxCNCConfigs:
		return "Sway configuration"
	case menuAutologin:
		return "Automatic login"
	default:
		return "LinuxCNC Setup"
	}
}

func (m *Model) openPage(page menuPage) {
	m.page = page
	m.selected = 0
	m.confirming = false
	m.status = ""
}

func (m *Model) back() {
	switch m.page {
	case menuLinuxCNCConfigs:
		m.openPage(menuLinuxCNCAutostart)
		m.selectAction(actionOpenLinuxCNCAutostartSway)
	case menuLinuxCNCAutostart:
		m.openPage(menuMain)
		m.selectAction(actionOpenLinuxCNCAutostart)
	case menuAutologin:
		m.openPage(menuMain)
		m.selectAction(actionOpenAutologin)
	}
}

func (m *Model) selectAction(action sectionAction) {
	for index, candidate := range m.visibleSections() {
		if candidate.action == action {
			m.selected = index
			return
		}
	}
}

func executeAction(action sectionAction, value string) tea.Cmd {
	switch action {
	case actionInstallAnsible:
		return installAnsible()
	case actionInstallSway:
		return runSwayInstallPlaybook()
	case actionConfigureLinuxCNCAutostartSway:
		return runLinuxCNCAutostartPlaybook(value)
	case actionAutologinLightDM:
		return runAutologinPlaybook(action, "lightdm")
	case actionAutologinSway:
		return runAutologinPlaybook(action, "sway")
	case actionReboot:
		return rebootSystem()
	default:
		return nil
	}
}

func installAnsible() tea.Cmd {
	commands := make([]commandSpec, 0, 3)
	if os.Geteuid() == 0 {
		commands = append(commands,
			commandSpec{name: "apt-get", args: []string{"update"}},
			commandSpec{name: "apt-get", args: []string{"install", "-y", "ansible"}},
		)
	} else {
		commands = append(commands,
			commandSpec{name: "sudo", args: []string{"--", "apt-get", "update"}},
			commandSpec{name: "sudo", args: []string{"--", "apt-get", "install", "-y", "ansible"}},
		)
	}
	commands = append(commands, commandSpec{name: "ansible", args: []string{"--version"}})

	sequence := &commandSequence{commands: commands}
	return tea.Exec(sequence, func(err error) tea.Msg {
		return actionFinishedMsg{action: actionInstallAnsible, err: err}
	})
}

func runSwayInstallPlaybook() tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: actionInstallSway, err: err}
		}
	}

	return runEmbeddedPlaybook(
		actionInstallSway,
		playbooks.InstallSway,
		map[string]any{"target_user": targetUser},
	)
}

func runLinuxCNCAutostartPlaybook(configPath string) tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: actionConfigureLinuxCNCAutostartSway, err: err}
		}
	}

	return runEmbeddedPlaybook(
		actionConfigureLinuxCNCAutostartSway,
		playbooks.LinuxCNCAutostart,
		map[string]any{
			"target_user":     targetUser,
			"linuxcnc_config": configPath,
		},
	)
}

func runAutologinPlaybook(action sectionAction, mode string) tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}

	variables := map[string]any{
		"autologin_mode": mode,
		"target_user":    targetUser,
	}
	if mode == "sway" {
		variables["sway_validated"] = true
	}

	return runEmbeddedPlaybook(action, playbooks.Autologin, variables)
}

func runEmbeddedPlaybook(
	action sectionAction,
	playbook playbooks.Playbook,
	variables map[string]any,
) tea.Cmd {
	playbookPath, cleanup, err := playbooks.Materialize(playbook)
	if err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}

	extraVars, err := json.Marshal(variables)
	if err != nil {
		cleanup()
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: fmt.Errorf("encode playbook variables: %w", err)}
		}
	}

	args := []string{
		"--inventory", "localhost,",
		"--connection", "local",
		"--diff",
		"--extra-vars", string(extraVars),
	}
	args = append(args, playbookPath)

	ansiblePath, err := exec.LookPath("ansible-playbook")
	if err != nil {
		cleanup()
		return func() tea.Msg {
			return actionFinishedMsg{
				action: action,
				err:    fmt.Errorf("find ansible-playbook: %w", err),
			}
		}
	}

	var command *exec.Cmd
	if os.Geteuid() == 0 {
		command = exec.Command(ansiblePath, args...)
	} else {
		sudoArgs := append([]string{"--", ansiblePath}, args...)
		command = exec.Command("sudo", sudoArgs...)
	}
	runner := &pausingCommand{command: command}
	return tea.Exec(runner, func(err error) tea.Msg {
		cleanup()
		return actionFinishedMsg{action: action, err: err}
	})
}

func rebootSystem() tea.Cmd {
	var command *exec.Cmd
	if os.Geteuid() == 0 {
		command = exec.Command("systemctl", "reboot")
	} else {
		command = exec.Command("sudo", "--", "systemctl", "reboot")
	}

	return tea.ExecProcess(command, func(err error) tea.Msg {
		return actionFinishedMsg{action: actionReboot, err: err}
	})
}

func targetUsername() (string, error) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		return sudoUser, nil
	}

	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("identify current user: %w", err)
	}
	if currentUser.Username == "" || currentUser.Username == "root" {
		return "", fmt.Errorf("run the TUI as the user who should log in automatically")
	}
	return currentUser.Username, nil
}

func actionName(action sectionAction) string {
	switch action {
	case actionInstallAnsible:
		return "Ansible installation"
	case actionInstallSway:
		return "Wayland and Sway installation"
	case actionConfigureLinuxCNCAutostartSway:
		return "LinuxCNC Sway autostart configuration"
	case actionAutologinLightDM:
		return "LightDM auto-login configuration"
	case actionAutologinSway:
		return "Sway auto-login configuration"
	case actionReboot:
		return "System reboot"
	default:
		return "Action"
	}
}

func actionRunningMessage(action sectionAction) string {
	switch action {
	case actionInstallAnsible:
		return "Installing Ansible..."
	case actionInstallSway:
		return "Running the Wayland and Sway installation playbook..."
	case actionConfigureLinuxCNCAutostartSway:
		return "Running the LinuxCNC Sway autostart playbook..."
	case actionAutologinLightDM:
		return "Running the LightDM auto-login playbook..."
	case actionAutologinSway:
		return "Running the Sway auto-login playbook..."
	case actionReboot:
		return "Requesting system reboot..."
	default:
		return "Running..."
	}
}

func actionCancelledMessage(action sectionAction) string {
	switch action {
	case actionInstallAnsible:
		return "Ansible installation cancelled."
	case actionInstallSway:
		return "Wayland and Sway installation cancelled."
	case actionConfigureLinuxCNCAutostartSway:
		return "LinuxCNC Sway autostart configuration cancelled."
	case actionAutologinLightDM:
		return "LightDM auto-login configuration cancelled."
	case actionAutologinSway:
		return "Sway auto-login configuration cancelled."
	case actionReboot:
		return "System reboot cancelled."
	default:
		return "Action cancelled."
	}
}

func actionSuccessMessage(action sectionAction) string {
	switch action {
	case actionInstallAnsible:
		return "Ansible installed successfully."
	case actionInstallSway:
		return "Wayland and Sway installed. Log out and validate Sway before enabling auto-login."
	case actionConfigureLinuxCNCAutostartSway:
		return "LinuxCNC autostart configured for Sway workspace 1. It will start at the next Sway login."
	case actionAutologinLightDM:
		return "LightDM auto-login configured. Reboot when ready."
	case actionAutologinSway:
		return "Sway auto-login configured. Reboot when ready."
	case actionReboot:
		return "Reboot requested. The system is shutting down."
	default:
		return "Action completed successfully."
	}
}
