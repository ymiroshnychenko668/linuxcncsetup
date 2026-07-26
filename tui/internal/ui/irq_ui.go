package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

func (m *Model) refreshIRQSnapshot() {
	snapshot, err := ProbeIRQSnapshot()
	if err != nil {
		m.irqSnapshotLoaded = false
		m.status = fmt.Sprintf("Cannot inspect IRQ affinity: %v", err)
		return
	}
	m.irqSnapshot = snapshot
	m.irqSnapshotLoaded = true
}

func (m *Model) beginIRQGuidedSetup() bool {
	m.refreshIRQSnapshot()
	if !m.irqSnapshotLoaded {
		return false
	}
	if len(m.irqSnapshot.OnlineCPUs) < 2 {
		m.status = "IRQ affinity requires at least two online CPUs."
		return false
	}

	recommended := RecommendedProtectedCPUs(
		m.irqSnapshot.OnlineCPUs,
		m.irqSnapshot.IsolatedCPUs,
	)
	if config := m.irqSnapshot.ManagedPolicy.ConfigData; config != nil {
		if err := ValidateIRQPolicy(
			m.irqSnapshot.OnlineCPUs,
			config.ProtectedCPUs,
			config.HousekeepingCPUs,
		); err == nil {
			recommended = append([]int(nil), config.ProtectedCPUs...)
		}
	}
	m.irqProtectedCPUs = m.expandIRQSiblingGroups(recommended)
	if len(m.irqProtectedCPUs) == 0 {
		m.status = "Could not select a safe protected CPU set."
		return false
	}
	if len(m.irqProtectedCPUs) >= len(m.irqSnapshot.OnlineCPUs) {
		m.status = "Cannot keep SMT siblings together while leaving a housekeeping CPU."
		return false
	}
	m.status = ""
	return true
}

func (m *Model) rebuildIRQCPUSections() {
	sections := make([]section, 0, len(m.irqSnapshot.OnlineCPUs)+2)
	for _, cpu := range m.irqSnapshot.OnlineCPUs {
		protected := containsCPU(m.irqProtectedCPUs, cpu)
		checkbox := "[ ]"
		role := "housekeeping / IRQ"
		if protected {
			checkbox = "[x]"
			role = "protected LinuxCNC RT"
		}
		description := fmt.Sprintf("CPU %d is assigned to: %s.", cpu, role)
		if siblings := m.irqSiblingGroup(cpu); len(siblings) > 1 {
			description += " SMT siblings " + FormatCPUList(siblings) + " move together."
		}
		sections = append(sections, section{
			title:       fmt.Sprintf("%s CPU %d", checkbox, cpu),
			description: description,
			action:      actionIRQToggleCPU,
			value:       strconv.Itoa(cpu),
		})
	}
	sections = append(sections,
		section{
			title:       "Continue →",
			description: "Validate the CPU roles and review the boot policy.",
			action:      actionIRQContinue,
		},
		section{
			title:       "← Back",
			description: "Return to IRQ affinity.",
			action:      actionBack,
		},
	)
	m.irqCPUSections = sections
}

func (m *Model) toggleIRQCPU(value string) {
	cpu, err := strconv.Atoi(value)
	if err != nil || !containsCPU(m.irqSnapshot.OnlineCPUs, cpu) {
		m.status = "Cannot toggle an unknown CPU."
		return
	}

	siblings := m.irqSiblingGroup(cpu)
	if containsCPU(m.irqProtectedCPUs, cpu) {
		for _, sibling := range siblings {
			m.irqProtectedCPUs = removeCPU(m.irqProtectedCPUs, sibling)
		}
	} else {
		candidate := append([]int(nil), m.irqProtectedCPUs...)
		candidate = append(candidate, siblings...)
		candidate = sortedUniqueNonNegative(candidate)
		if len(candidate) >= len(m.irqSnapshot.OnlineCPUs) {
			m.status = "At least one CPU must remain available for the OS and device IRQs."
			return
		}
		m.irqProtectedCPUs = candidate
	}
	if len(siblings) > 1 {
		m.status = "Kept SMT sibling CPUs " + FormatCPUList(siblings) + " in the same role."
	} else {
		m.status = ""
	}
	m.rebuildIRQCPUSections()
}

func (m Model) validateIRQDraft() error {
	if !m.irqSnapshotLoaded {
		return fmt.Errorf("IRQ status has not been loaded")
	}
	housekeeping := HousekeepingCPUs(m.irqSnapshot.OnlineCPUs, m.irqProtectedCPUs)
	if err := ValidateIRQPolicy(
		m.irqSnapshot.OnlineCPUs,
		m.irqProtectedCPUs,
		housekeeping,
	); err != nil {
		return err
	}
	return m.validateIRQSiblingPolicy()
}

func (m Model) renderIRQStatus() string {
	if !m.irqSnapshotLoaded {
		return "IRQ status is unavailable. Press Enter to retry."
	}

	isolated := displayCPUList(m.irqSnapshot.IsolatedCPUs)
	noHZFull := displayCPUList(m.irqSnapshot.NoHZFullCPUs)
	kernelIRQ := displayCPUList(m.irqSnapshot.KernelIRQAffinity)
	defaultIRQ := displayCPUList(m.irqSnapshot.DefaultAffinity)

	lines := []string{
		"Online CPUs:          " + displayCPUList(m.irqSnapshot.OnlineCPUs),
		"isolcpus:             " + isolated,
		"nohz_full:            " + noHZFull,
		"Kernel irqaffinity:   " + kernelIRQ,
		"Default IRQ affinity: " + defaultIRQ,
		fmt.Sprintf("Discovered IRQs:      %d", len(m.irqSnapshot.IRQs)),
		"irqbalance:           " + renderIRQBalanceState(m.irqSnapshot.IRQBalance),
		"Managed policy:       " + renderManagedIRQState(m.irqSnapshot.ManagedPolicy),
	}
	if config := m.irqSnapshot.ManagedPolicy.ConfigData; config != nil {
		defaultPolicy := config.DefaultPolicy
		if defaultPolicy == nil &&
			len(config.HousekeepingCPUs) > 0 &&
			len(config.ProtectedCPUs) > 0 {
			defaultPolicy = &ManagedIRQDefaultPolicy{
				HousekeepingCPUs: config.HousekeepingCPUs,
				ProtectedCPUs:    config.ProtectedCPUs,
			}
		}
		if defaultPolicy != nil {
			lines = append(lines,
				"  Protected CPUs:    "+
					displayCPUList(defaultPolicy.ProtectedCPUs),
				"  Housekeeping CPUs: "+
					displayCPUList(defaultPolicy.HousekeepingCPUs),
			)
		} else {
			lines = append(lines, "  Default policy:      disabled")
		}
		lines = append(
			lines,
			fmt.Sprintf("  Device rules:        %d", len(config.DeviceRules)),
		)
		for index, rule := range config.DeviceRules {
			if index == 3 {
				lines = append(lines, "    …")
				break
			}
			label := strings.TrimSpace(rule.Label)
			if label == "" {
				label = irqDeviceSelectorText(rule.Selector)
			}
			lines = append(
				lines,
				fmt.Sprintf("    %s → %s", label, rule.CPUList),
			)
		}
	}
	if result := m.irqSnapshot.ManagedPolicy.ResultData; result != nil {
		resultStatus := result.Status
		if config := m.irqSnapshot.ManagedPolicy.ConfigData; config != nil &&
			!managedIRQResultMatchesConfig(result, config) {
			resultStatus += " (does not match installed policy)"
		}
		lines = append(lines,
			"",
			"Last boot result:     "+resultStatus,
			fmt.Sprintf(
				"  Applied/constrained: %d/%d",
				result.Counts.Applied,
				result.Counts.Constrained,
			),
			"  Kernel-managed:     "+strconv.Itoa(result.Counts.KernelManaged),
			fmt.Sprintf(
				"  Unwritable/failed:  %d/%d",
				result.Counts.Unwritable,
				result.Counts.Failed,
			),
		)
		if result.DeviceRuleCounts.Configured > 0 {
			lines = append(lines,
				fmt.Sprintf(
					"  Device applied/partial: %d/%d",
					result.DeviceRuleCounts.Applied,
					result.DeviceRuleCounts.Partial,
				),
				fmt.Sprintf(
					"  Device no-match/failed: %d/%d",
					result.DeviceRuleCounts.NoMatch,
					result.DeviceRuleCounts.Failed,
				),
			)
			unsafe := result.DeviceRuleCounts.UnsafeSelector +
				result.DeviceRuleCounts.AmbiguousSelector
			if unsafe > 0 {
				lines = append(
					lines,
					fmt.Sprintf("  Unsafe/ambiguous:   %d", unsafe),
				)
			}
		}
		if result.GeneratedAt != "" {
			lines = append(lines, "  Generated:           "+result.GeneratedAt)
		}
		if result.Message != "" {
			lines = append(lines, "  Message:             "+result.Message)
		}
	} else if m.irqSnapshot.ManagedPolicy.ConfigData != nil {
		lines = append(lines, "", "Activation:           pending reboot; no boot result yet")
	}
	if len(m.irqSnapshot.IsolatedCPUs) == 0 {
		lines = append(lines, "", "Warning: no CPUs are currently isolated.")
	}
	if len(m.irqSnapshot.Problems) > 0 {
		lines = append(lines, "", "Probe warnings:")
		for index, problem := range m.irqSnapshot.Problems {
			if index == 3 {
				lines = append(lines, "  …")
				break
			}
			lines = append(lines, "  - "+problem)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderIRQCPUSelection(selectedValue string) string {
	if !m.irqSnapshotLoaded {
		return "IRQ status is unavailable."
	}
	housekeeping := HousekeepingCPUs(m.irqSnapshot.OnlineCPUs, m.irqProtectedCPUs)
	lines := []string{
		"Protected LinuxCNC CPUs: " + displayCPUList(m.irqProtectedCPUs),
		"Housekeeping/IRQ CPUs:   " + displayCPUList(housekeeping),
		"",
		"Space or Enter toggles the selected CPU.",
		"Detected SMT siblings are kept in the same role.",
	}
	if selectedValue != "" {
		lines = append(lines, "", "Selected CPU: "+selectedValue)
	}
	if len(m.irqSnapshot.IsolatedCPUs) == 0 {
		lines = append(lines,
			"",
			"These are IRQ roles only. CPU scheduler",
			"isolation must be configured separately.",
		)
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderIRQReview() string {
	if err := m.validateIRQDraft(); err != nil {
		return "Invalid IRQ policy: " + err.Error()
	}
	housekeeping := HousekeepingCPUs(m.irqSnapshot.OnlineCPUs, m.irqProtectedCPUs)
	return strings.Join([]string{
		"Protected LinuxCNC CPUs: " + displayCPUList(m.irqProtectedCPUs),
		"Housekeeping/IRQ CPUs:   " + displayCPUList(housekeeping),
		fmt.Sprintf("Current IRQ entries:      %d", len(m.irqSnapshot.IRQs)),
		"",
		"At the next boot, the resolver will:",
		"  • set the default IRQ CPU mask;",
		"  • discover current IRQ numbers;",
		"  • move every writable IRQ to housekeeping CPUs;",
		"  • report kernel-managed IRQs it cannot move.",
		"",
		"Nothing is applied to live IRQs now.",
	}, "\n")
}

func (m Model) renderIRQManagedPolicy() string {
	if !m.irqSnapshotLoaded {
		return "Managed policy status is unavailable."
	}
	if !m.irqManagedPolicyPresent() {
		return "No LinuxCNC Setup IRQ policy is installed."
	}
	return "Managed policy: " + renderManagedIRQState(m.irqSnapshot.ManagedPolicy)
}

func (m Model) irqManagedPolicyPresent() bool {
	policy := m.irqSnapshot.ManagedPolicy
	return policy.Config.Present || policy.Helper.Present ||
		policy.Service.Present || policy.Result.Present
}

func (m Model) runIRQAffinityPlaybook(
	action sectionAction,
	state string,
	checkMode bool,
) tea.Cmd {
	variables := map[string]any{"irq_affinity_state": state}
	if state == "present" {
		housekeeping := HousekeepingCPUs(m.irqSnapshot.OnlineCPUs, m.irqProtectedCPUs)
		if err := ValidateIRQPolicy(
			m.irqSnapshot.OnlineCPUs,
			m.irqProtectedCPUs,
			housekeeping,
		); err != nil {
			return func() tea.Msg {
				return actionFinishedMsg{action: action, err: err}
			}
		}
		variables["irq_housekeeping_cpus"] = FormatCPUList(housekeeping)
		variables["irq_protected_cpus"] = FormatCPUList(m.irqProtectedCPUs)
	}
	return runEmbeddedPlaybook(
		action,
		playbooks.IRQAffinity,
		variables,
		checkMode,
	)
}

func displayCPUList(cpus []int) string {
	if len(cpus) == 0 {
		return "none"
	}
	return FormatCPUList(cpus)
}

func containsCPU(cpus []int, candidate int) bool {
	for _, cpu := range cpus {
		if cpu == candidate {
			return true
		}
	}
	return false
}

func removeCPU(cpus []int, removed int) []int {
	result := make([]int, 0, len(cpus))
	for _, cpu := range cpus {
		if cpu != removed {
			result = append(result, cpu)
		}
	}
	return result
}

func (m Model) irqSiblingGroup(cpu int) []int {
	for _, info := range m.irqSnapshot.CPUs {
		if info.ID != cpu {
			continue
		}
		siblings := intersection(m.irqSnapshot.OnlineCPUs, info.ThreadSiblings)
		if !containsCPU(siblings, cpu) {
			siblings = append(siblings, cpu)
		}
		if len(siblings) > 0 {
			return sortedUniqueNonNegative(siblings)
		}
	}
	return []int{cpu}
}

func (m Model) expandIRQSiblingGroups(cpus []int) []int {
	expanded := append([]int(nil), cpus...)
	for _, cpu := range cpus {
		expanded = append(expanded, m.irqSiblingGroup(cpu)...)
	}
	return sortedUniqueNonNegative(expanded)
}

func (m Model) validateIRQSiblingPolicy() error {
	protected := intSet(m.irqProtectedCPUs)
	checked := make(map[string]struct{})
	for _, cpu := range m.irqSnapshot.OnlineCPUs {
		siblings := m.irqSiblingGroup(cpu)
		if len(siblings) < 2 {
			continue
		}
		key := FormatCPUList(siblings)
		if _, found := checked[key]; found {
			continue
		}
		checked[key] = struct{}{}

		protectedCount := 0
		for _, sibling := range siblings {
			if _, found := protected[sibling]; found {
				protectedCount++
			}
		}
		if protectedCount > 0 && protectedCount < len(siblings) {
			return fmt.Errorf("SMT sibling CPUs %s must use the same role", key)
		}
	}
	return nil
}

func managedIRQResultMatchesConfig(
	result *ManagedIRQResult,
	config *ManagedIRQConfig,
) bool {
	if result == nil || config == nil {
		return false
	}
	resultDefault := result.Policy.DefaultPolicy
	if resultDefault == nil &&
		(len(result.Policy.HousekeepingCPUs) > 0 ||
			len(result.Policy.ProtectedCPUs) > 0) {
		resultDefault = &ManagedIRQDefaultPolicy{
			HousekeepingCPUs: result.Policy.HousekeepingCPUs,
			ProtectedCPUs:    result.Policy.ProtectedCPUs,
		}
	}
	configDefault := config.DefaultPolicy
	if configDefault == nil &&
		(len(config.HousekeepingCPUs) > 0 || len(config.ProtectedCPUs) > 0) {
		configDefault = &ManagedIRQDefaultPolicy{
			HousekeepingCPUs: config.HousekeepingCPUs,
			ProtectedCPUs:    config.ProtectedCPUs,
		}
	}
	if (resultDefault == nil) != (configDefault == nil) {
		return false
	}
	if resultDefault != nil &&
		(FormatCPUList(resultDefault.HousekeepingCPUs) !=
			FormatCPUList(configDefault.HousekeepingCPUs) ||
			FormatCPUList(resultDefault.ProtectedCPUs) !=
				FormatCPUList(configDefault.ProtectedCPUs)) {
		return false
	}

	resultRules := make(map[string]string, len(result.Policy.DeviceRules))
	for _, rule := range result.Policy.DeviceRules {
		key := managedIRQSelectorKey(rule.Selector)
		if _, duplicate := resultRules[key]; duplicate {
			return false
		}
		resultRules[key] = FormatCPUList(rule.CPUs)
	}
	configRules := make(map[string]string, len(config.DeviceRules))
	for _, rule := range config.DeviceRules {
		key := managedIRQSelectorKey(rule.Selector)
		if _, duplicate := configRules[key]; duplicate {
			return false
		}
		configRules[key] = FormatCPUList(rule.CPUs)
	}
	if len(resultRules) != len(configRules) {
		return false
	}
	for selector, cpus := range resultRules {
		if configRules[selector] != cpus {
			return false
		}
	}
	return true
}

func renderIRQBalanceState(status IRQBalanceStatus) string {
	if !status.Installed {
		return "not installed"
	}

	states := make([]string, 0, 2)
	if status.ActiveKnown {
		if status.Active {
			states = append(states, "active")
		} else {
			states = append(states, "inactive")
		}
	} else {
		states = append(states, "activity unknown")
	}
	if status.EnabledKnown {
		if status.Enabled {
			states = append(states, "enabled")
		} else {
			states = append(states, "disabled")
		}
	} else {
		states = append(states, "enablement unknown")
	}

	result := "installed, " + strings.Join(states, ", ")
	if status.Active || status.Enabled ||
		!status.ActiveKnown || !status.EnabledKnown {
		result += " (must be resolved before applying)"
	}
	return result
}

func renderManagedIRQState(status ManagedIRQPolicyStatus) string {
	if !status.Config.Present && !status.Helper.Present &&
		!status.Service.Present && !status.Result.Present {
		return "not installed"
	}
	if !status.Config.Present || !status.Helper.Present || !status.Service.Present {
		return "partial installation detected"
	}
	if status.EnabledKnown && status.Enabled {
		return "installed and enabled"
	}
	if status.EnabledKnown {
		return "installed, service disabled"
	}
	return "installed, service state unknown"
}
