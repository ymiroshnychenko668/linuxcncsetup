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
	actionOpenDevTools
	actionOpenLinuxCNCAutostart
	actionOpenLinuxCNCAutostartSway
	actionLinuxCNCAutostartX11
	actionConfigureLinuxCNCAutostartSway
	actionOpenAutologin
	actionAutologinLightDM
	actionAutologinSway
	actionOpenConfiguration
	actionOpenGRUBRealtime
	actionGRUBToggleCPU
	actionGRUBContinue
	actionGRUBApply
	actionInstallLinuxCNCConfig
	actionOpenIRQAffinity
	actionIRQDevices
	actionIRQFullInterrupts
	actionIRQDeviceSelect
	actionIRQKernelCounters
	actionIRQDeviceToggleCPU
	actionIRQDeviceContinue
	actionIRQDevicePreview
	actionIRQDevicePersist
	actionIRQDeviceApplyLive
	actionIRQDeviceRemove
	actionIRQDeviceRefresh
	actionIRQStatus
	actionIRQGuidedSetup
	actionIRQToggleCPU
	actionIRQContinue
	actionIRQPreview
	actionIRQApply
	actionIRQDisable
	actionInstallDevToolsAll
	actionInstallDevToolsGit
	actionInstallDevToolsVSCode
	actionInstallDevToolsCodex
	actionInstallDevToolsClaude
	actionInstallDevToolsWarp
	actionInstallDevToolsHtop
	actionInstallDevToolsMC
	actionInstallDevToolsTerminator
	actionEnableUserLinger
	actionReboot
	actionBack
)

type menuPage int

const (
	menuMain menuPage = iota
	menuLinuxCNCAutostart
	menuLinuxCNCConfigs
	menuAutologin
	menuConfiguration
	menuGRUBRealtime
	menuGRUBCPUs
	menuGRUBReview
	menuIRQAffinity
	menuIRQDevices
	menuIRQDeviceCPUs
	menuIRQDeviceReview
	menuIRQCPUs
	menuIRQReview
	menuDevTools
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
		title:       "Install CorvusCNC config",
		description: "Clone the ready-to-use CorvusCNC repository into ~/linuxcnc/configs/corvuscnc.",
		action:      actionInstallLinuxCNCConfig,
	},
	{
		title:       "Install Sway",
		description: "Add the complete Sway desktop without changing the active display manager.",
		action:      actionInstallSway,
	},
	{
		title:       "Development tools",
		description: "Install individual developer tools or configure all of them together.",
		action:      actionOpenDevTools,
	},
	{
		title:       "Automatic login",
		description: "Choose automatic login through LightDM or greetd with Sway.",
		action:      actionOpenAutologin,
	},
	{
		title:       "LinuxCNC autostart",
		description: "Choose a desktop and LinuxCNC configuration to start automatically.",
		action:      actionOpenLinuxCNCAutostart,
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
		action:      actionOpenConfiguration,
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

var configurationSections = []section{
	{
		title:       "GRUB real-time setup",
		description: "Choose protected CPUs and install explained kernel parameters with Ansible.",
		action:      actionOpenGRUBRealtime,
	},
	{
		title:       "IRQ affinity",
		description: "Keep movable device interrupts off CPUs reserved for LinuxCNC real-time work.",
		action:      actionOpenIRQAffinity,
	},
	{
		title:       "← Back",
		description: "Return to the main menu.",
		action:      actionBack,
	},
}

var irqAffinitySections = []section{
	{
		title:       "IRQ devices",
		description: "Inspect real interrupt counters by device and adjust a device's CPU affinity.",
		action:      actionIRQDevices,
	},
	{
		title:       "Current status",
		description: "Inspect CPU isolation, IRQ affinity, irqbalance, and the managed boot policy.",
		action:      actionIRQStatus,
	},
	{
		title:       "Default IRQ policy",
		description: "Optionally keep all movable IRQs on housekeeping CPUs by default.",
		action:      actionIRQGuidedSetup,
	},
	{
		title:       "Disable managed tuning",
		description: "Remove only the IRQ policy files managed by LinuxCNC Setup at the next boot.",
		action:      actionIRQDisable,
	},
	{
		title:       "← Back",
		description: "Return to Configuration.",
		action:      actionBack,
	},
}

var irqDeviceReviewSections = []section{
	{
		title:       "Preview persistent rule",
		description: "Run Ansible check mode for this device rule without changing the system.",
		action:      actionIRQDevicePreview,
	},
	{
		title:       "Save for next boot",
		description: "Persist this device rule without changing live IRQ affinity.",
		action:      actionIRQDevicePersist,
	},
	{
		title:       "Apply to device now",
		description: "Change the matching device IRQs immediately after explicit confirmation.",
		action:      actionIRQDeviceApplyLive,
	},
	{
		title:       "Remove saved rule",
		description: "Remove only this device's persistent rule; live IRQ affinity is unchanged.",
		action:      actionIRQDeviceRemove,
	},
	{
		title:       "← Back",
		description: "Return to the device CPU selector.",
		action:      actionBack,
	},
}

var irqReviewSections = []section{
	{
		title:       "Preview Ansible changes",
		description: "Run the embedded playbook in check mode without changing the system.",
		action:      actionIRQPreview,
	},
	{
		title:       "Apply at next boot",
		description: "Install and enable the managed boot policy without changing live IRQ affinity.",
		action:      actionIRQApply,
	},
	{
		title:       "← Back",
		description: "Return to CPU selection.",
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
	width                    int
	height                   int
	page                     menuPage
	selected                 int
	confirming               bool
	status                   string
	linuxCNCSections         []section
	irqSnapshot              IRQSnapshot
	irqSnapshotLoaded        bool
	irqProtectedCPUs         []int
	irqCPUSections           []section
	irqDeviceInventory       IRQDeviceInventory
	irqDeviceInventoryLoaded bool
	irqDeviceSections        []section
	irqSelectedDeviceID      string
	irqDeviceCPUs            []int
	irqDeviceCPUSections     []section
	irqDeviceDetailOffset    int
	grubProtectedCPUs        []int
	grubCPUSections          []section
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
			if message.action == actionIRQApply ||
				message.action == actionIRQDisable ||
				message.action == actionIRQDevicePersist ||
				message.action == actionIRQDeviceApplyLive ||
				message.action == actionIRQDeviceRemove ||
				message.action == actionGRUBApply {
				m.refreshIRQSnapshot()
				m.refreshIRQDeviceInventory(false)
			}
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
				return m, m.executeAction(current.action, current.value)
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
		case " ", "space":
			if m.page == menuIRQCPUs && m.currentSection().action == actionIRQToggleCPU {
				m.toggleIRQCPU(m.currentSection().value)
			}
			if m.page == menuIRQDeviceCPUs &&
				m.currentSection().action == actionIRQDeviceToggleCPU {
				m.toggleIRQDeviceCPU(m.currentSection().value)
			}
			if m.page == menuGRUBCPUs &&
				m.currentSection().action == actionGRUBToggleCPU {
				m.toggleGRUBCPU(m.currentSection().value)
			}
		case "r":
			if m.page == menuIRQDevices {
				m.refreshIRQDeviceInventory(true)
			}
		case "pgup":
			m.scrollIRQDeviceDetail(-1)
		case "pgdown":
			m.scrollIRQDeviceDetail(1)
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
	if m.page == menuIRQCPUs {
		helpText = "↑/↓ select • Space/Enter toggle • Enter continue • Esc back"
	}
	if m.page == menuIRQDevices {
		helpText = "↑/↓ device • PgUp/PgDn details • Enter edit • r refresh • Esc back"
	}
	if m.page == menuIRQDeviceCPUs {
		helpText = "↑/↓ select • Space/Enter toggle • Enter continue • Esc back"
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

	sections := m.visibleSections()
	maxVisible := max(m.height-10, 3)
	start := 0
	if len(sections) > maxVisible {
		start = m.selected - maxVisible/2
		start = max(start, 0)
		start = min(start, len(sections)-maxVisible)
	}
	end := min(start+maxVisible, len(sections))
	if start > 0 {
		builder.WriteString(helpStyle.Render("  ↑ more"))
		builder.WriteString("\n")
	}

	for index := start; index < end; index++ {
		section := sections[index]
		style := itemStyle
		prefix := "  "
		if index == m.selected {
			style = selectedStyle
			prefix = "› "
		}

		builder.WriteString(style.Render(prefix + section.title))
		builder.WriteString("\n")
	}
	if end < len(sections) {
		builder.WriteString(helpStyle.Render("  ↓ more"))
		builder.WriteString("\n")
	}

	return builder.String()
}

func (m Model) renderDetail() string {
	current := m.currentSection()
	var lines []string
	switch current.action {
	case actionIRQFullInterrupts, actionIRQDeviceSelect, actionIRQKernelCounters,
		actionIRQDevicePreview, actionIRQDevicePersist,
		actionIRQDeviceApplyLive, actionIRQDeviceRemove:
		// These views provide their own full identity and use the remaining
		// panel height as a scrollable /proc/interrupts viewport.
	default:
		lines = []string{
			titleStyle.Render(current.title),
			"",
			current.description,
			"",
		}
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
	case actionOpenDevTools:
		lines = append(lines, "Press Enter to choose a developer-tools component.")
	case actionInstallDevToolsAll,
		actionInstallDevToolsGit,
		actionInstallDevToolsVSCode,
		actionInstallDevToolsCodex,
		actionInstallDevToolsClaude,
		actionInstallDevToolsWarp,
		actionInstallDevToolsHtop,
		actionInstallDevToolsMC,
		actionInstallDevToolsTerminator,
		actionEnableUserLinger:
		lines = append(lines, renderDevToolsAction(current.action, m.confirming)...)
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
	case actionOpenConfiguration:
		lines = append(lines, "Press Enter to open configuration tools.")
	case actionOpenGRUBRealtime:
		lines = append(lines, renderGRUBIntroduction())
	case actionGRUBToggleCPU:
		lines = append(lines, m.renderGRUBCPUSelection())
	case actionGRUBContinue:
		lines = append(lines, m.renderGRUBCPUSelection(), "", "Press Enter to review every parameter.")
	case actionGRUBApply:
		lines = append(lines, m.renderGRUBReview(m.confirming))
	case actionInstallLinuxCNCConfig:
		lines = append(lines, renderLinuxCNCConfigInstall(m.confirming)...)
	case actionOpenIRQAffinity:
		lines = append(lines,
			"Press Enter to inspect or configure IRQ affinity.",
			"",
			"The guided setup changes only the policy",
			"used at the next boot.",
		)
	case actionIRQDevices:
		lines = append(lines,
			"Press Enter to open the live device table.",
			"",
			"It reads the real /proc/interrupts counters",
			"and groups vectors by physical device.",
		)
	case actionIRQFullInterrupts:
		lines = append(lines, m.renderIRQFullInterrupts())
	case actionIRQDeviceSelect:
		lines = append(lines, m.renderIRQDeviceDetail(current.value))
	case actionIRQKernelCounters:
		lines = append(lines, m.renderIRQKernelCounters(current.value))
	case actionIRQDeviceToggleCPU:
		lines = append(lines, m.renderIRQDeviceCPUSelection(current.value))
	case actionIRQDeviceContinue:
		lines = append(lines,
			m.renderIRQDeviceCPUSelection(""),
			"",
			"Press Enter to review this device rule.",
		)
	case actionIRQDeviceRefresh:
		lines = append(lines, "Press Enter to reread /proc/interrupts and IRQ affinity.")
	case actionIRQDevicePreview:
		if m.confirming {
			lines = append(lines,
				m.renderIRQDeviceRuleSummary(),
				"",
				warningStyle.Render("Preview this persistent device rule?"),
				"",
				"Ansible check mode will show only the",
				"managed policy and service changes.",
				"No IRQ affinity will be changed.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(
				lines,
				m.renderIRQDeviceReview(),
				"",
				"Press Enter to preview with Ansible.",
			)
		}
	case actionIRQDevicePersist:
		if m.confirming {
			lines = append(lines,
				m.renderIRQDeviceRuleSummary(),
				"",
				warningStyle.Render("Save this device rule?"),
				"",
				"The rule is matched by stable device",
				"identity at each boot. Numeric IRQs are",
				"not saved. Live IRQs stay unchanged.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(
				lines,
				m.renderIRQDeviceReview(),
				"",
				"Press Enter to save for the next boot.",
			)
		}
	case actionIRQDeviceApplyLive:
		if m.confirming {
			lines = append(lines,
				m.renderIRQDeviceRuleSummary(),
				"",
				warningStyle.Render("Apply this device affinity now?"),
				"",
				"This immediately writes every currently",
				"matching IRQ's smp_affinity_list.",
				"LinuxCNC must be stopped. This does not",
				"save the rule for the next boot.",
				"Press y to apply or n to cancel.",
			)
		} else {
			lines = append(lines, m.renderIRQDeviceReview())
			if err := m.validateIRQDeviceAction(
				actionIRQDeviceApplyLive,
			); err == nil {
				lines = append(
					lines,
					"",
					"Press Enter for live-apply confirmation.",
				)
			}
		}
	case actionIRQDeviceRemove:
		if m.confirming {
			lines = append(lines,
				m.renderIRQDeviceRuleSummary(),
				"",
				warningStyle.Render("Remove this saved device rule?"),
				"",
				"Other device/default rules are retained.",
				"Live IRQ affinity is not changed.",
				"Press y to remove or n to cancel.",
			)
		} else {
			lines = append(
				lines,
				m.renderIRQDeviceReview(),
				"",
				"Press Enter to remove the saved rule.",
			)
		}
	case actionIRQStatus:
		lines = append(lines, m.renderIRQStatus(), "", "Press Enter to refresh.")
	case actionIRQGuidedSetup:
		lines = append(lines,
			"Press Enter to choose CPUs that should be",
			"protected from movable device interrupts.",
			"",
			"No live IRQ affinity will be changed.",
		)
	case actionIRQToggleCPU:
		lines = append(lines, m.renderIRQCPUSelection(current.value))
	case actionIRQContinue:
		lines = append(lines,
			m.renderIRQCPUSelection(""),
			"",
			"Press Enter to review the Ansible policy.",
		)
	case actionIRQPreview:
		lines = append(lines, m.renderIRQReview())
		if m.confirming {
			lines = append(lines,
				"",
				warningStyle.Render("Run the Ansible preview?"),
				"",
				"Check mode shows the managed files and",
				"service changes without applying them.",
				"sudo may ask for your account password.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, "", "Press Enter to preview with Ansible.")
		}
	case actionIRQApply:
		lines = append(lines, m.renderIRQReview())
		if m.confirming {
			lines = append(lines,
				"",
				warningStyle.Render("Enable this IRQ policy?"),
				"",
				"Ansible will install a boot-time policy.",
				"It will not change live IRQs or reboot.",
				"LinuxCNC must be stopped while applying.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, "", "Press Enter to configure with Ansible.")
		}
	case actionIRQDisable:
		if m.confirming {
			lines = append(lines,
				warningStyle.Render("Disable managed IRQ tuning?"),
				"",
				"Ansible removes only files owned by",
				"LinuxCNC Setup. Live IRQs are unchanged.",
				"Kernel defaults return after reboot.",
				"Press y to continue or n to cancel.",
			)
		} else {
			lines = append(lines, m.renderIRQManagedPolicy(), "", "Press Enter to disable.")
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
	if m.page == menuIRQDevices {
		m.irqDeviceDetailOffset = 0
	}
	m.status = ""
}

func (m *Model) prepareSelectedAction() {
	current := m.currentSection()
	switch current.action {
	case actionOpenDevTools:
		m.openPage(menuDevTools)
		return

	case actionOpenConfiguration:
		m.openPage(menuConfiguration)
		return

	case actionOpenGRUBRealtime:
		if !m.beginGRUBSetup() {
			return
		}
		m.openPage(menuGRUBCPUs)
		m.rebuildGRUBCPUSections()
		return

	case actionGRUBToggleCPU:
		m.toggleGRUBCPU(current.value)
		return

	case actionGRUBContinue:
		if err := m.validateGRUBDraft(); err != nil {
			m.status = fmt.Sprintf("Cannot continue: %v", err)
			return
		}
		m.openPage(menuGRUBReview)
		return

	case actionGRUBApply:
		if _, err := exec.LookPath("ansible-playbook"); err != nil {
			m.status = "Install Ansible first, then retry this action."
			return
		}
		if err := m.validateGRUBDraft(); err != nil {
			m.status = fmt.Sprintf("Cannot configure GRUB: %v", err)
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot configure GRUB: sudo was not found."
				return
			}
		}

	case actionOpenIRQAffinity:
		m.openPage(menuIRQAffinity)
		m.refreshIRQSnapshot()
		return

	case actionIRQDevices:
		m.refreshIRQDeviceInventory(true)
		if !m.irqDeviceInventoryLoaded {
			return
		}
		m.openPage(menuIRQDevices)
		m.rebuildIRQDeviceSections()
		return

	case actionIRQDeviceSelect:
		if !m.beginIRQDeviceEdit(current.value) {
			return
		}
		m.openPage(menuIRQDeviceCPUs)
		m.rebuildIRQDeviceCPUSections()
		return

	case actionIRQKernelCounters:
		m.refreshIRQDeviceInventory(true)
		return

	case actionIRQFullInterrupts:
		m.refreshIRQDeviceInventory(true)
		return

	case actionIRQDeviceToggleCPU:
		m.toggleIRQDeviceCPU(current.value)
		return

	case actionIRQDeviceContinue:
		if err := m.validateIRQDeviceDraft(); err != nil {
			m.status = fmt.Sprintf("Cannot continue: %v", err)
			return
		}
		m.openPage(menuIRQDeviceReview)
		return

	case actionIRQDeviceRefresh:
		m.refreshIRQDeviceInventory(true)
		return

	case actionIRQStatus:
		m.refreshIRQSnapshot()
		if m.irqSnapshotLoaded {
			m.status = "IRQ status refreshed."
		}
		return

	case actionIRQGuidedSetup:
		if !m.beginIRQGuidedSetup() {
			return
		}
		m.openPage(menuIRQCPUs)
		m.rebuildIRQCPUSections()
		return

	case actionIRQToggleCPU:
		m.toggleIRQCPU(current.value)
		return

	case actionIRQContinue:
		if err := m.validateIRQDraft(); err != nil {
			m.status = fmt.Sprintf("Cannot continue: %v", err)
			return
		}
		m.openPage(menuIRQReview)
		return

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

	case actionInstallDevToolsAll,
		actionInstallDevToolsGit,
		actionInstallDevToolsVSCode,
		actionInstallDevToolsCodex,
		actionInstallDevToolsClaude,
		actionInstallDevToolsWarp,
		actionInstallDevToolsHtop,
		actionInstallDevToolsMC,
		actionInstallDevToolsTerminator,
		actionEnableUserLinger:
		if !m.prepareDevToolsInstall() {
			return
		}

	case actionInstallLinuxCNCConfig:
		if !m.prepareLinuxCNCConfigInstall() {
			return
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

	case actionIRQDevicePreview, actionIRQDevicePersist,
		actionIRQDeviceApplyLive, actionIRQDeviceRemove:
		if _, err := exec.LookPath("ansible-playbook"); err != nil {
			m.status = "Install Ansible first, then retry this action."
			return
		}
		if err := m.validateIRQDeviceAction(current.action); err != nil {
			if current.action == actionIRQDeviceApplyLive {
				m.status = "Live apply is blocked; no affinity was written."
			} else {
				m.status = fmt.Sprintf(
					"Cannot configure device affinity: %v",
					err,
				)
			}
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot configure device affinity: sudo was not found."
				return
			}
		}

	case actionIRQPreview, actionIRQApply:
		if _, err := exec.LookPath("ansible-playbook"); err != nil {
			m.status = "Install Ansible first, then retry this action."
			return
		}
		if err := m.validateIRQDraft(); err != nil {
			m.status = fmt.Sprintf("Cannot configure IRQ affinity: %v", err)
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot configure IRQ affinity: sudo was not found."
				return
			}
		}

	case actionIRQDisable:
		m.refreshIRQSnapshot()
		if !m.irqSnapshotLoaded {
			return
		}
		if !m.irqManagedPolicyPresent() {
			m.status = "No LinuxCNC Setup IRQ policy is installed."
			return
		}
		if _, err := exec.LookPath("ansible-playbook"); err != nil {
			m.status = "Install Ansible first, then retry this action."
			return
		}
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err != nil {
				m.status = "Cannot disable IRQ affinity: sudo was not found."
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
	case menuConfiguration:
		return configurationSections
	case menuGRUBRealtime:
		return grubRealtimeSections
	case menuGRUBCPUs:
		return m.grubCPUSections
	case menuGRUBReview:
		return grubReviewSections
	case menuIRQAffinity:
		return irqAffinitySections
	case menuIRQDevices:
		if len(m.irqDeviceSections) == 0 {
			return []section{{
				title:       "← Back",
				description: "Return to IRQ affinity.",
				action:      actionBack,
			}}
		}
		return m.irqDeviceSections
	case menuIRQDeviceCPUs:
		if len(m.irqDeviceCPUSections) == 0 {
			return []section{{
				title:       "← Back",
				description: "Return to the IRQ device table.",
				action:      actionBack,
			}}
		}
		return m.irqDeviceCPUSections
	case menuIRQDeviceReview:
		return irqDeviceReviewSections
	case menuIRQCPUs:
		if len(m.irqCPUSections) == 0 {
			return []section{{
				title:       "← Back",
				description: "Return to IRQ affinity.",
				action:      actionBack,
			}}
		}
		return m.irqCPUSections
	case menuIRQReview:
		return irqReviewSections
	case menuDevTools:
		return devToolsSections
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
	case menuConfiguration:
		return "Configuration"
	case menuGRUBRealtime:
		return "GRUB real-time setup"
	case menuGRUBCPUs:
		return "Protected boot CPUs"
	case menuGRUBReview:
		return "Review GRUB parameters"
	case menuIRQAffinity:
		return "IRQ affinity"
	case menuIRQDevices:
		return "IRQ devices"
	case menuIRQDeviceCPUs:
		return "Device CPUs"
	case menuIRQDeviceReview:
		return "Review device rule"
	case menuIRQCPUs:
		return "Protected CPUs"
	case menuIRQReview:
		return "Review IRQ policy"
	case menuDevTools:
		return "Development tools"
	default:
		return "LinuxCNC Setup"
	}
}

func (m *Model) openPage(page menuPage) {
	m.page = page
	m.selected = 0
	m.confirming = false
	m.status = ""
	m.irqDeviceDetailOffset = 0
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
	case menuConfiguration:
		m.openPage(menuMain)
		m.selectAction(actionOpenConfiguration)
	case menuGRUBRealtime:
		m.openPage(menuConfiguration)
		m.selectAction(actionOpenGRUBRealtime)
	case menuGRUBCPUs:
		m.openPage(menuConfiguration)
		m.selectAction(actionOpenGRUBRealtime)
	case menuGRUBReview:
		m.openPage(menuGRUBCPUs)
		m.selectAction(actionGRUBContinue)
	case menuIRQAffinity:
		m.openPage(menuConfiguration)
		m.selectAction(actionOpenIRQAffinity)
	case menuIRQDevices:
		m.openPage(menuIRQAffinity)
		m.selectAction(actionIRQDevices)
	case menuIRQDeviceCPUs:
		m.openPage(menuIRQDevices)
		m.selectIRQDevice(m.irqSelectedDeviceID)
	case menuIRQDeviceReview:
		m.openPage(menuIRQDeviceCPUs)
		m.selectAction(actionIRQDeviceContinue)
	case menuIRQCPUs:
		m.openPage(menuIRQAffinity)
		m.selectAction(actionIRQGuidedSetup)
	case menuIRQReview:
		m.openPage(menuIRQCPUs)
		m.selectAction(actionIRQContinue)
	case menuDevTools:
		m.openPage(menuMain)
		m.selectAction(actionOpenDevTools)
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

func (m Model) executeAction(action sectionAction, value string) tea.Cmd {
	switch action {
	case actionInstallAnsible:
		return installAnsible()
	case actionInstallSway:
		return runSwayInstallPlaybook()
	case actionInstallLinuxCNCConfig:
		return runLinuxCNCConfigInstallPlaybook()
	case actionInstallDevToolsAll,
		actionInstallDevToolsGit,
		actionInstallDevToolsVSCode,
		actionInstallDevToolsCodex,
		actionInstallDevToolsClaude,
		actionInstallDevToolsWarp,
		actionInstallDevToolsHtop,
		actionInstallDevToolsMC,
		actionInstallDevToolsTerminator,
		actionEnableUserLinger:
		return runDevToolsInstallPlaybook(action)
	case actionConfigureLinuxCNCAutostartSway:
		return runLinuxCNCAutostartPlaybook(value)
	case actionAutologinLightDM:
		return runAutologinPlaybook(action, "lightdm")
	case actionAutologinSway:
		return runAutologinPlaybook(action, "sway")
	case actionGRUBApply:
		return m.runGRUBPlaybook(action)
	case actionIRQDevicePreview:
		return m.runIRQDevicePersistentPlaybook(action, true, false)
	case actionIRQDevicePersist:
		return m.runIRQDevicePersistentPlaybook(action, false, false)
	case actionIRQDeviceApplyLive:
		return m.runIRQDeviceLivePlaybook(action)
	case actionIRQDeviceRemove:
		return m.runIRQDevicePersistentPlaybook(action, false, true)
	case actionIRQPreview:
		return m.runIRQAffinityPlaybook(action, "present", true)
	case actionIRQApply:
		return m.runIRQAffinityPlaybook(action, "present", false)
	case actionIRQDisable:
		return m.runIRQAffinityPlaybook(action, "absent", false)
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
	checkMode ...bool,
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
	if len(checkMode) > 0 && checkMode[0] {
		args = append(args, "--check")
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
	if name, ok := devToolsActionName(action); ok {
		return name
	}

	switch action {
	case actionInstallAnsible:
		return "Ansible installation"
	case actionInstallSway:
		return "Wayland and Sway installation"
	case actionInstallLinuxCNCConfig:
		return "CorvusCNC configuration installation"
	case actionConfigureLinuxCNCAutostartSway:
		return "LinuxCNC Sway autostart configuration"
	case actionAutologinLightDM:
		return "LightDM auto-login configuration"
	case actionAutologinSway:
		return "Sway auto-login configuration"
	case actionGRUBApply:
		return "GRUB real-time configuration"
	case actionIRQDevicePreview:
		return "Device IRQ affinity preview"
	case actionIRQDevicePersist:
		return "Persistent device IRQ affinity"
	case actionIRQDeviceApplyLive:
		return "Live device IRQ affinity"
	case actionIRQDeviceRemove:
		return "Device IRQ affinity rule removal"
	case actionIRQPreview:
		return "IRQ affinity preview"
	case actionIRQApply:
		return "IRQ affinity configuration"
	case actionIRQDisable:
		return "IRQ affinity removal"
	case actionReboot:
		return "System reboot"
	default:
		return "Action"
	}
}

func actionRunningMessage(action sectionAction) string {
	if message, ok := devToolsRunningMessage(action); ok {
		return message
	}

	switch action {
	case actionInstallAnsible:
		return "Installing Ansible..."
	case actionInstallSway:
		return "Running the Wayland and Sway installation playbook..."
	case actionInstallLinuxCNCConfig:
		return "Running the CorvusCNC configuration installation playbook..."
	case actionConfigureLinuxCNCAutostartSway:
		return "Running the LinuxCNC Sway autostart playbook..."
	case actionAutologinLightDM:
		return "Running the LightDM auto-login playbook..."
	case actionAutologinSway:
		return "Running the Sway auto-login playbook..."
	case actionGRUBApply:
		return "Installing the managed GRUB real-time profile with Ansible..."
	case actionIRQDevicePreview:
		return "Previewing the persistent device IRQ rule..."
	case actionIRQDevicePersist:
		return "Saving the device IRQ rule for future boots..."
	case actionIRQDeviceApplyLive:
		return "Applying affinity to the matching device IRQs..."
	case actionIRQDeviceRemove:
		return "Removing the persistent device IRQ rule..."
	case actionIRQPreview:
		return "Previewing the IRQ affinity playbook..."
	case actionIRQApply:
		return "Installing the IRQ affinity boot policy..."
	case actionIRQDisable:
		return "Removing the managed IRQ affinity policy..."
	case actionReboot:
		return "Requesting system reboot..."
	default:
		return "Running..."
	}
}

func actionCancelledMessage(action sectionAction) string {
	if message, ok := devToolsCancelledMessage(action); ok {
		return message
	}

	switch action {
	case actionInstallAnsible:
		return "Ansible installation cancelled."
	case actionInstallSway:
		return "Wayland and Sway installation cancelled."
	case actionInstallLinuxCNCConfig:
		return "CorvusCNC configuration installation cancelled."
	case actionConfigureLinuxCNCAutostartSway:
		return "LinuxCNC Sway autostart configuration cancelled."
	case actionAutologinLightDM:
		return "LightDM auto-login configuration cancelled."
	case actionAutologinSway:
		return "Sway auto-login configuration cancelled."
	case actionGRUBApply:
		return "GRUB real-time configuration cancelled."
	case actionIRQDevicePreview:
		return "Device IRQ affinity preview cancelled."
	case actionIRQDevicePersist:
		return "Persistent device IRQ rule cancelled."
	case actionIRQDeviceApplyLive:
		return "Live device IRQ affinity cancelled."
	case actionIRQDeviceRemove:
		return "Device IRQ rule removal cancelled."
	case actionIRQPreview:
		return "IRQ affinity preview cancelled."
	case actionIRQApply:
		return "IRQ affinity configuration cancelled."
	case actionIRQDisable:
		return "IRQ affinity removal cancelled."
	case actionReboot:
		return "System reboot cancelled."
	default:
		return "Action cancelled."
	}
}

func actionSuccessMessage(action sectionAction) string {
	if message, ok := devToolsSuccessMessage(action); ok {
		return message
	}

	switch action {
	case actionInstallAnsible:
		return "Ansible installed successfully."
	case actionInstallSway:
		return "Wayland and Sway installed. Log out and validate Sway before enabling auto-login."
	case actionInstallLinuxCNCConfig:
		return "CorvusCNC configuration installed in ~/linuxcnc/configs/corvuscnc and ready to select."
	case actionConfigureLinuxCNCAutostartSway:
		return "LinuxCNC autostart configured for Sway workspace 1. It will start at the next Sway login."
	case actionAutologinLightDM:
		return "LightDM auto-login configured. Reboot when ready."
	case actionAutologinSway:
		return "Sway auto-login configured. Reboot when ready."
	case actionGRUBApply:
		return "GRUB real-time parameters installed. Reboot when ready to activate them."
	case actionIRQDevicePreview:
		return "Device IRQ rule preview completed. No changes were applied."
	case actionIRQDevicePersist:
		return "Device IRQ rule saved. It will be resolved by device at the next boot."
	case actionIRQDeviceApplyLive:
		return "Device IRQ affinity applied. Review the refreshed effective affinity."
	case actionIRQDeviceRemove:
		return "Persistent device IRQ rule removed. Live IRQ affinity was not changed."
	case actionIRQPreview:
		return "IRQ affinity preview completed. No system changes were applied."
	case actionIRQApply:
		return "IRQ affinity boot policy installed. Reboot when ready to activate it."
	case actionIRQDisable:
		return "Managed IRQ affinity policy removed. Reboot to return to kernel defaults."
	case actionReboot:
		return "Reboot requested. The system is shutting down."
	default:
		return "Action completed successfully."
	}
}
