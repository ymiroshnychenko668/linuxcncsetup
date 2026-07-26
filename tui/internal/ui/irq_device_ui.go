package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

// ManagedIRQDefaultPolicy is the optional broad fallback in managed schema v2.
// Device-only policies leave this nil and never touch unrelated IRQs.
type ManagedIRQDefaultPolicy struct {
	HousekeepingCPUList string
	ProtectedCPUList    string
	HousekeepingCPUs    []int
	ProtectedCPUs       []int
}

// ManagedIRQDeviceRule is one persisted stable-device affinity rule.
type ManagedIRQDeviceRule struct {
	Selector IRQDeviceSelector
	CPUList  string
	CPUs     []int
	Label    string
}

func (m *Model) refreshIRQDeviceInventory(refreshSnapshot bool) {
	selectedID := ""
	selectedAction := actionNone
	if m.page == menuIRQDevices &&
		m.selected >= 0 &&
		m.selected < len(m.irqDeviceSections) {
		current := m.irqDeviceSections[m.selected]
		if current.action == actionIRQDeviceSelect ||
			current.action == actionIRQKernelCounters {
			selectedID = current.value
			selectedAction = current.action
		}
	}
	m.irqDeviceDetailOffset = 0
	if refreshSnapshot || !m.irqSnapshotLoaded {
		m.refreshIRQSnapshot()
	}
	if !m.irqSnapshotLoaded {
		m.clearIRQDeviceInventory()
		return
	}

	inventory, err := ProbeIRQDeviceInventory(m.irqSnapshot)
	if err != nil {
		m.clearIRQDeviceInventory()
		m.status = fmt.Sprintf("Cannot read /proc/interrupts by device: %v", err)
		return
	}
	m.irqDeviceInventory = inventory
	m.irqDeviceInventoryLoaded = true
	m.rebuildIRQDeviceSections()
	if m.page == menuIRQDevices {
		m.selected = 0
	}
	if selectedID != "" && m.page == menuIRQDevices {
		m.selectIRQInventoryRow(selectedAction, selectedID)
	}
	m.status = fmt.Sprintf(
		"Loaded %d device(s) and %d kernel counter(s) from /proc/interrupts.",
		len(inventory.Devices),
		len(inventory.Pseudo),
	)
	if len(inventory.Problems) > 0 {
		m.status += fmt.Sprintf(" %d row(s) need attention.", len(inventory.Problems))
	}
}

func (m *Model) clearIRQDeviceInventory() {
	m.irqDeviceInventory = IRQDeviceInventory{}
	m.irqDeviceInventoryLoaded = false
	m.irqDeviceSections = nil
	if m.page == menuIRQDevices {
		m.selected = 0
	}
}

func (m *Model) rebuildIRQDeviceSections() {
	sections := make([]section, 0, len(m.irqDeviceInventory.Devices)+
		len(m.irqDeviceInventory.Pseudo)+2)
	for _, device := range m.irqDeviceInventory.Devices {
		vectorLabel := fmt.Sprintf("%d IRQs", len(device.IRQs))
		if len(device.IRQs) == 1 {
			vectorLabel = fmt.Sprintf("IRQ %d", device.IRQs[0].Number)
		}
		title := irqDeviceSectionTitle(device)
		sections = append(sections, section{
			title: title,
			description: fmt.Sprintf(
				"%s; %s; total interrupts %d.",
				irqDeviceSelectorText(device.Selector),
				vectorLabel,
				device.Total,
			),
			action: actionIRQDeviceSelect,
			value:  device.ID,
		})
	}
	for _, row := range m.irqDeviceInventory.Pseudo {
		sections = append(sections, section{
			title:       compactIRQSectionTitle("kernel: "+row.ID, 34),
			description: strings.TrimSpace(row.Description) + " (read-only kernel counter).",
			action:      actionIRQKernelCounters,
			value:       row.ID,
		})
	}
	sections = append(sections,
		section{
			title:       "↻ Refresh /proc/interrupts",
			description: "Reread device counters and current requested/effective affinity.",
			action:      actionIRQDeviceRefresh,
		},
		section{
			title:       "← Back",
			description: "Return to IRQ affinity.",
			action:      actionBack,
		},
	)
	m.irqDeviceSections = sections
}

func irqDeviceSectionTitle(device IRQDeviceGroup) string {
	label := irqActionFamily(device.Actions)
	if label == "" {
		label = device.Label
	}
	if device.Selector.Kind == IRQDeviceSelectorPCIBDF {
		label += " " + strings.TrimPrefix(device.Selector.Value, "0000:")
	}
	vectorLabel := fmt.Sprintf(" ×%d", len(device.IRQs))
	if len(device.IRQs) == 1 {
		vectorLabel = fmt.Sprintf(" IRQ%d", device.IRQs[0].Number)
	}
	if !device.Editable || !device.Persistable {
		label = "[read-only] " + label
	}
	return compactIRQSectionTitle(label+vectorLabel, 18)
}

func compactIRQSectionTitle(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	if maximum < 2 {
		return string(runes[:maximum])
	}
	return string(runes[:maximum-1]) + "…"
}

func (m *Model) selectIRQDevice(id string) {
	m.selectIRQInventoryRow(actionIRQDeviceSelect, id)
}

func (m *Model) selectIRQInventoryRow(action sectionAction, id string) {
	for index, candidate := range m.irqDeviceSections {
		if candidate.action == action && candidate.value == id {
			m.selected = index
			return
		}
	}
}

func (m Model) irqDeviceByID(id string) (IRQDeviceGroup, bool) {
	for _, device := range m.irqDeviceInventory.Devices {
		if device.ID == id {
			return device, true
		}
	}
	return IRQDeviceGroup{}, false
}

func (m *Model) beginIRQDeviceEdit(id string) bool {
	device, found := m.irqDeviceByID(id)
	if !found {
		m.status = "The selected IRQ device disappeared; refresh and retry."
		return false
	}
	if !device.Editable {
		m.status = "This device is read-only: " + device.ReadOnlyReason
		return false
	}
	if !device.Persistable {
		m.status = "This device has no safe stable selector: " +
			device.PersistabilityReason
		return false
	}

	m.irqSelectedDeviceID = device.ID
	if saved, found := m.savedIRQDeviceRule(device.Selector); found {
		m.irqDeviceCPUs = append([]int(nil), saved.CPUs...)
	} else {
		var cpus []int
		for _, irq := range device.IRQs {
			cpus = append(cpus, irq.RequestedCPUs...)
		}
		cpus = intersection(m.irqSnapshot.OnlineCPUs, cpus)
		if len(cpus) == 0 {
			for _, irq := range device.IRQs {
				cpus = append(cpus, irq.EffectiveCPUs...)
			}
			cpus = intersection(m.irqSnapshot.OnlineCPUs, cpus)
		}
		if len(cpus) == 0 {
			cpus = append([]int(nil), m.irqSnapshot.OnlineCPUs...)
		}
		m.irqDeviceCPUs = sortedUniqueNonNegative(cpus)
	}
	m.status = ""
	return true
}

func (m *Model) rebuildIRQDeviceCPUSections() {
	sections := make([]section, 0, len(m.irqSnapshot.OnlineCPUs)+2)
	for _, cpu := range m.irqSnapshot.OnlineCPUs {
		checkbox := "[ ]"
		if containsCPU(m.irqDeviceCPUs, cpu) {
			checkbox = "[x]"
		}
		sections = append(sections, section{
			title: fmt.Sprintf("%s CPU %d", checkbox, cpu),
			description: fmt.Sprintf(
				"Include logical CPU %d in this device's IRQ affinity.",
				cpu,
			),
			action: actionIRQDeviceToggleCPU,
			value:  strconv.Itoa(cpu),
		})
	}
	sections = append(sections,
		section{
			title:       "Continue →",
			description: "Review preview, persistence, or explicit live apply.",
			action:      actionIRQDeviceContinue,
		},
		section{
			title:       "← Back",
			description: "Return to the live IRQ device table.",
			action:      actionBack,
		},
	)
	m.irqDeviceCPUSections = sections
}

func (m *Model) toggleIRQDeviceCPU(value string) {
	cpu, err := strconv.Atoi(value)
	if err != nil || !containsCPU(m.irqSnapshot.OnlineCPUs, cpu) {
		m.status = "Cannot toggle an unknown CPU."
		return
	}
	if containsCPU(m.irqDeviceCPUs, cpu) {
		if len(m.irqDeviceCPUs) == 1 {
			m.status = "A device affinity must contain at least one online CPU."
			return
		}
		m.irqDeviceCPUs = removeCPU(m.irqDeviceCPUs, cpu)
	} else {
		m.irqDeviceCPUs = sortedUniqueNonNegative(
			append(m.irqDeviceCPUs, cpu),
		)
	}
	m.status = ""
	m.rebuildIRQDeviceCPUSections()
}

func (m Model) validateIRQDeviceDraft() error {
	if !m.irqDeviceInventoryLoaded {
		return fmt.Errorf("the IRQ device inventory is not loaded")
	}
	device, found := m.irqDeviceByID(m.irqSelectedDeviceID)
	if !found {
		return fmt.Errorf("the selected device is no longer present")
	}
	if !device.Editable {
		return fmt.Errorf("%s", device.ReadOnlyReason)
	}
	if !device.Persistable {
		return fmt.Errorf("%s", device.PersistabilityReason)
	}
	if len(m.irqDeviceCPUs) == 0 {
		return fmt.Errorf("at least one target CPU is required")
	}
	online := intSet(m.irqSnapshot.OnlineCPUs)
	for _, cpu := range m.irqDeviceCPUs {
		if _, found := online[cpu]; !found {
			return fmt.Errorf("CPU %d is not online", cpu)
		}
	}
	return nil
}

func (m Model) validateIRQDeviceAction(action sectionAction) error {
	if err := m.validateIRQDeviceDraft(); err != nil {
		return err
	}
	device, _ := m.irqDeviceByID(m.irqSelectedDeviceID)
	if action != actionIRQDeviceApplyLive {
		policy := m.irqSnapshot.ManagedPolicy
		configPresent := policy.Config.Present || policy.ConfigPresent
		if configPresent && policy.ConfigData == nil {
			path := policy.Config.Path
			if path == "" {
				path = policy.ConfigPath
			}
			if path == "" {
				path = managedIRQConfigPath
			}
			return fmt.Errorf(
				"the existing policy at %s could not be parsed and validated; refusing to overwrite it",
				path,
			)
		}
	}
	if action == actionIRQDeviceApplyLive {
		var blocked []string
		for _, irq := range device.IRQs {
			if !irq.AffinityFileWritable {
				blocked = append(blocked, strconv.Itoa(irq.Number))
			}
		}
		if len(blocked) > 0 {
			return fmt.Errorf(
				"live apply is blocked because IRQs %s are kernel-managed or read-only; no affinity will be written",
				strings.Join(blocked, ","),
			)
		}
	}
	if action == actionIRQDeviceRemove {
		if _, found := m.savedIRQDeviceRule(device.Selector); !found {
			return fmt.Errorf("this device has no saved rule")
		}
	}
	return nil
}

func (m Model) renderIRQDeviceDetail(id string) string {
	return m.renderScrollableIRQDeviceLines(m.irqDeviceDetailLines(id))
}

func (m Model) irqDeviceDetailLines(id string) []string {
	if !m.irqDeviceInventoryLoaded {
		return []string{"The device inventory is unavailable. Press r to retry."}
	}
	device, found := m.irqDeviceByID(id)
	if !found {
		return []string{"This device disappeared. Press r to refresh."}
	}

	lines := []string{
		"Device: " + device.Label,
		"Stable match: " + irqDeviceSelectorText(device.Selector),
		fmt.Sprintf("Vectors: %d    Total: %d", len(device.IRQs), device.Total),
		"",
		"Current /proc/interrupts vectors:",
	}
	for _, irq := range device.IRQs {
		action := strings.TrimSpace(irq.Action)
		if action == "" {
			action = "unnamed"
		}
		writeState := ""
		if !irq.AffinityFileWritable {
			writeState = " [read-only]"
		}
		lines = append(lines,
			fmt.Sprintf("IRQ %d  %s%s", irq.Number, action, writeState),
			fmt.Sprintf(
				"  requested=%s  effective=%s  total=%d",
				displayIRQAffinity(irq.RequestedAffinity),
				displayIRQAffinity(irq.EffectiveAffinity),
				irq.Total,
			),
		)
		if description := strings.TrimSpace(irq.Description); description != "" {
			lines = append(lines, "  /proc: "+description)
		}
		lines = append(lines, renderIRQCPUCounts(irq.PerCPU, "  ")...)
	}
	lines = append(lines, "", "Combined /proc/interrupts totals:")
	lines = append(lines, renderIRQCPUCounts(device.PerCPU, "  ")...)

	if !device.Editable || !device.Persistable {
		reason := device.ReadOnlyReason
		if reason == "" {
			reason = device.PersistabilityReason
		}
		lines = append(lines, "", warningStyle.Render("Read-only: "+reason))
	} else {
		lines = append(lines,
			"",
			"Press Enter to choose CPUs for this device.",
			"All listed vectors will use the same rule.",
		)
	}
	return lines
}

func (m Model) renderIRQKernelCounters(id string) string {
	return m.renderScrollableIRQDeviceLines(m.irqKernelCounterLines(id))
}

func (m Model) irqKernelCounterLines(id string) []string {
	for _, row := range m.irqDeviceInventory.Pseudo {
		if row.ID != id {
			continue
		}
		lines := []string{
			"Read-only /proc/interrupts row",
			"",
			"ID: " + row.ID,
			"Description: " + strings.TrimSpace(row.Description),
			fmt.Sprintf("Total: %d", row.Total),
			"",
		}
		lines = append(lines, renderIRQCPUCounts(row.PerCPU, "")...)
		if row.Raw != "" {
			lines = append(lines, "", "Raw:", row.Raw)
		}
		return lines
	}
	return []string{"Kernel counter not found. Press r to refresh."}
}

func (m Model) irqDeviceDetailHeight() int {
	return max(m.height-12, 6)
}

func (m Model) irqDeviceDetailWidth() int {
	contentWidth := max(m.width-appStyle.GetHorizontalFrameSize(), 1)
	detailWidth := max(contentWidth-sidebarWidth-1, 20)
	return max(detailWidth-panelStyle.GetHorizontalFrameSize(), 10)
}

func (m Model) wrapIRQDeviceLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	wrapped := lipgloss.Wrap(
		strings.Join(lines, "\n"),
		m.irqDeviceDetailWidth(),
		"/,:",
	)
	return strings.Split(wrapped, "\n")
}

func (m Model) renderScrollableIRQDeviceLines(lines []string) string {
	lines = m.wrapIRQDeviceLines(lines)
	limit := m.irqDeviceDetailHeight()
	if len(lines) <= limit {
		return strings.Join(lines, "\n")
	}

	maximumOffset := max(len(lines)-limit+1, 0)
	start := min(max(m.irqDeviceDetailOffset, 0), maximumOffset)
	remaining := limit
	rendered := make([]string, 0, limit)
	if start > 0 {
		rendered = append(rendered, helpStyle.Render("↑ PgUp: earlier details"))
		remaining--
	}

	end := min(start+remaining, len(lines))
	if end < len(lines) {
		remaining--
		end = min(start+remaining, len(lines))
	}
	rendered = append(rendered, lines[start:end]...)
	if end < len(lines) {
		rendered = append(rendered, helpStyle.Render("↓ PgDn: more details"))
	}
	return strings.Join(rendered, "\n")
}

func (m *Model) scrollIRQDeviceDetail(direction int) {
	if m.page != menuIRQDevices || direction == 0 {
		return
	}

	current := m.currentSection()
	var lines []string
	switch current.action {
	case actionIRQDeviceSelect:
		lines = m.wrapIRQDeviceLines(m.irqDeviceDetailLines(current.value))
	case actionIRQKernelCounters:
		lines = m.wrapIRQDeviceLines(m.irqKernelCounterLines(current.value))
	default:
		return
	}

	limit := m.irqDeviceDetailHeight()
	maximumOffset := max(len(lines)-limit+1, 0)
	step := max(limit-2, 1)
	m.irqDeviceDetailOffset = min(
		max(m.irqDeviceDetailOffset+direction*step, 0),
		maximumOffset,
	)
}

func renderIRQCPUCounts(counts []IRQCPUCount, indent string) []string {
	if len(counts) == 0 {
		return []string{indent + "No per-CPU counters were exposed."}
	}
	lines := make([]string, 0, (len(counts)+1)/2)
	for index := 0; index < len(counts); index += 2 {
		line := indent + formatIRQCPUCount(counts[index])
		if index+1 < len(counts) {
			line += "  " + formatIRQCPUCount(counts[index+1])
		}
		lines = append(lines, line)
	}
	return lines
}

func formatIRQCPUCount(count IRQCPUCount) string {
	if count.CPU < 0 {
		return fmt.Sprintf("global=%d", count.Count)
	}
	return fmt.Sprintf("CPU%d=%d", count.CPU, count.Count)
}

func displayIRQAffinity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unavailable"
	}
	return value
}

func irqDeviceSelectorText(selector IRQDeviceSelector) string {
	switch selector.Kind {
	case IRQDeviceSelectorPCIBDF:
		return "PCI " + selector.Value
	case IRQDeviceSelectorAction:
		return "action " + selector.Value
	default:
		return "unstable/unknown"
	}
}

func (m Model) renderIRQDeviceCPUSelection(selectedValue string) string {
	device, found := m.irqDeviceByID(m.irqSelectedDeviceID)
	if !found {
		return "The selected IRQ device is no longer present."
	}
	lines := []string{
		"Device: " + device.Label,
		"Match:  " + irqDeviceSelectorText(device.Selector),
		"Target CPUs: " + displayCPUList(m.irqDeviceCPUs),
		"",
		"Space or Enter toggles one logical CPU.",
		"This rule applies to every matching IRQ vector.",
	}
	if selectedValue != "" {
		lines = append(lines, "", "Selected CPU: "+selectedValue)
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderIRQDeviceReview() string {
	if err := m.validateIRQDeviceDraft(); err != nil {
		return "Invalid device rule: " + err.Error()
	}
	device, _ := m.irqDeviceByID(m.irqSelectedDeviceID)
	saved := "none"
	if rule, found := m.savedIRQDeviceRule(device.Selector); found {
		saved = rule.CPUList
	}
	writable := 0
	for _, irq := range device.IRQs {
		if irq.AffinityFileWritable {
			writable++
		}
	}
	lines := []string{
		m.renderIRQDeviceRuleSummary(),
		fmt.Sprintf("Writable now: %d/%d", writable, len(device.IRQs)),
		"Saved rule:    " + saved,
		"",
		"Boot: numeric IRQ IDs are never saved.",
		"Apply now is separate from the boot rule.",
	}
	if writable != len(device.IRQs) {
		var blocked []string
		for _, irq := range device.IRQs {
			if !irq.AffinityFileWritable {
				blocked = append(blocked, strconv.Itoa(irq.Number))
			}
		}
		lines = append(lines,
			"",
			warningStyle.Render(
				"Live blocked by read-only IRQs "+strings.Join(blocked, ",")+".",
			),
			"Boot reports partial if they remain read-only.",
		)
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderIRQDeviceRuleSummary() string {
	device, found := m.irqDeviceByID(m.irqSelectedDeviceID)
	if !found {
		return "The selected IRQ device is no longer present."
	}
	return strings.Join([]string{
		"Device:        " + device.Label,
		"Stable match:  " + irqDeviceSelectorText(device.Selector),
		fmt.Sprintf("Current IRQs:  %s", irqDeviceNumbers(device.IRQs)),
		"Target CPUs:   " + FormatCPUList(m.irqDeviceCPUs),
	}, "\n")
}

func irqDeviceNumbers(irqs []IRQDeviceIRQ) string {
	numbers := make([]string, 0, len(irqs))
	for _, irq := range irqs {
		numbers = append(numbers, strconv.Itoa(irq.Number))
	}
	if len(numbers) == 0 {
		return "none"
	}
	return strings.Join(numbers, ",")
}

func selectorsEqual(left, right IRQDeviceSelector) bool {
	return left.Kind == right.Kind &&
		strings.EqualFold(left.Value, right.Value)
}

func (m Model) savedIRQDeviceRule(
	selector IRQDeviceSelector,
) (ManagedIRQDeviceRule, bool) {
	config := m.irqSnapshot.ManagedPolicy.ConfigData
	if config == nil {
		return ManagedIRQDeviceRule{}, false
	}
	for _, rule := range config.DeviceRules {
		if selectorsEqual(rule.Selector, selector) {
			return rule, true
		}
	}
	return ManagedIRQDeviceRule{}, false
}

func (m Model) currentIRQDefaultPolicy() *ManagedIRQDefaultPolicy {
	config := m.irqSnapshot.ManagedPolicy.ConfigData
	if config == nil {
		return nil
	}
	if config.DefaultPolicy != nil {
		copy := *config.DefaultPolicy
		copy.HousekeepingCPUs = append(
			[]int(nil),
			config.DefaultPolicy.HousekeepingCPUs...,
		)
		copy.ProtectedCPUs = append(
			[]int(nil),
			config.DefaultPolicy.ProtectedCPUs...,
		)
		return &copy
	}
	if len(config.HousekeepingCPUs) > 0 && len(config.ProtectedCPUs) > 0 {
		return &ManagedIRQDefaultPolicy{
			HousekeepingCPUList: FormatCPUList(config.HousekeepingCPUs),
			ProtectedCPUList:    FormatCPUList(config.ProtectedCPUs),
			HousekeepingCPUs:    append([]int(nil), config.HousekeepingCPUs...),
			ProtectedCPUs:       append([]int(nil), config.ProtectedCPUs...),
		}
	}
	return nil
}

func (m Model) updatedIRQDeviceRules(
	device IRQDeviceGroup,
	remove bool,
) []ManagedIRQDeviceRule {
	var rules []ManagedIRQDeviceRule
	if config := m.irqSnapshot.ManagedPolicy.ConfigData; config != nil {
		for _, rule := range config.DeviceRules {
			if selectorsEqual(rule.Selector, device.Selector) {
				continue
			}
			rules = append(rules, rule)
		}
	}
	if !remove {
		rules = append(rules, ManagedIRQDeviceRule{
			Selector: device.Selector,
			CPUList:  FormatCPUList(m.irqDeviceCPUs),
			CPUs:     append([]int(nil), m.irqDeviceCPUs...),
			Label:    device.Label,
		})
	}
	return rules
}

func irqDeviceRuleVariables(rules []ManagedIRQDeviceRule) []map[string]any {
	result := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		result = append(result, map[string]any{
			"selector": map[string]any{
				"kind":  string(rule.Selector.Kind),
				"value": rule.Selector.Value,
			},
			"cpus":  FormatCPUList(rule.CPUs),
			"label": rule.Label,
		})
	}
	return result
}

func irqDefaultPolicyVariables(
	policy *ManagedIRQDefaultPolicy,
) map[string]any {
	if policy == nil {
		return nil
	}
	return map[string]any{
		"housekeeping_cpus": FormatCPUList(policy.HousekeepingCPUs),
		"protected_cpus":    FormatCPUList(policy.ProtectedCPUs),
	}
}

func (m Model) runIRQDevicePersistentPlaybook(
	action sectionAction,
	checkMode bool,
	remove bool,
) tea.Cmd {
	if err := m.validateIRQDeviceDraft(); err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}
	device, _ := m.irqDeviceByID(m.irqSelectedDeviceID)
	rules := m.updatedIRQDeviceRules(device, remove)
	defaultPolicy := m.currentIRQDefaultPolicy()
	state := "present"
	if defaultPolicy == nil && len(rules) == 0 {
		state = "absent"
	}

	variables := map[string]any{
		"irq_affinity_operation": "configure",
		"irq_affinity_state":     state,
	}
	if state == "present" {
		variables["irq_device_rules"] = irqDeviceRuleVariables(rules)
		if defaultPolicy != nil {
			variables["irq_default_policy"] =
				irqDefaultPolicyVariables(defaultPolicy)
		}
	}
	return runEmbeddedPlaybook(
		action,
		playbooks.IRQAffinity,
		variables,
		checkMode,
	)
}

func (m Model) runIRQDeviceLivePlaybook(action sectionAction) tea.Cmd {
	if err := m.validateIRQDeviceDraft(); err != nil {
		return func() tea.Msg {
			return actionFinishedMsg{action: action, err: err}
		}
	}
	device, _ := m.irqDeviceByID(m.irqSelectedDeviceID)
	rule := ManagedIRQDeviceRule{
		Selector: device.Selector,
		CPUList:  FormatCPUList(m.irqDeviceCPUs),
		CPUs:     append([]int(nil), m.irqDeviceCPUs...),
		Label:    device.Label,
	}
	return runEmbeddedPlaybook(
		action,
		playbooks.IRQAffinity,
		map[string]any{
			"irq_affinity_operation": "apply_device_live",
			"irq_affinity_state":     "present",
			"irq_device_rule": irqDeviceRuleVariables(
				[]ManagedIRQDeviceRule{rule},
			)[0],
		},
	)
}
