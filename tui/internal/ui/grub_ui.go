package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

var grubRealtimeSections = []section{{
	title: "Choose protected CPUs", action: actionOpenGRUBRealtime,
}}

var grubReviewSections = []section{
	{
		title:       "Apply with Ansible",
		description: "Write the managed GRUB drop-in, back it up, and regenerate grub.cfg.",
		action:      actionGRUBApply,
	},
	{
		title:       "← Back",
		description: "Adjust the protected CPU selection.",
		action:      actionBack,
	},
}

func (m *Model) beginGRUBSetup() bool {
	m.refreshIRQSnapshot()
	if !m.irqSnapshotLoaded || len(m.irqSnapshot.OnlineCPUs) < 2 {
		m.status = "GRUB real-time setup requires at least two online CPUs."
		return false
	}
	m.grubProtectedCPUs = m.expandIRQSiblingGroups(
		RecommendedProtectedCPUs(
			m.irqSnapshot.OnlineCPUs,
			m.irqSnapshot.IsolatedCPUs,
		),
	)
	if err := m.validateGRUBDraft(); err != nil {
		m.status = "Cannot create a safe GRUB CPU split: " + err.Error()
		return false
	}
	m.status = ""
	return true
}

func (m *Model) rebuildGRUBCPUSections() {
	sections := make([]section, 0, len(m.irqSnapshot.OnlineCPUs)+2)
	for _, cpu := range m.irqSnapshot.OnlineCPUs {
		protected := containsCPU(m.grubProtectedCPUs, cpu)
		check := "[ ]"
		role := "housekeeping: OS, IRQs and kernel work"
		if protected {
			check = "[x]"
			role = "protected: LinuxCNC real-time work"
		}
		description := fmt.Sprintf("CPU %d → %s.", cpu, role)
		if siblings := m.irqSiblingGroup(cpu); len(siblings) > 1 {
			description += " SMT siblings " + FormatCPUList(siblings) + " move together."
		}
		sections = append(sections, section{
			title:       fmt.Sprintf("%s CPU %d", check, cpu),
			description: description,
			action:      actionGRUBToggleCPU,
			value:       strconv.Itoa(cpu),
		})
	}
	sections = append(sections,
		section{
			title:       "Continue →",
			description: "Review the exact boot parameters and what each one does.",
			action:      actionGRUBContinue,
		},
		section{title: "← Back", description: "Return to Configuration.", action: actionBack},
	)
	m.grubCPUSections = sections
}

func (m *Model) toggleGRUBCPU(value string) {
	cpu, err := strconv.Atoi(value)
	if err != nil || !containsCPU(m.irqSnapshot.OnlineCPUs, cpu) {
		m.status = "Unknown CPU."
		return
	}
	siblings := m.irqSiblingGroup(cpu)
	candidate := append([]int(nil), m.grubProtectedCPUs...)
	if containsCPU(candidate, cpu) {
		for _, sibling := range siblings {
			candidate = removeCPU(candidate, sibling)
		}
	} else {
		candidate = sortedUniqueNonNegative(append(candidate, siblings...))
	}
	housekeeping := HousekeepingCPUs(m.irqSnapshot.OnlineCPUs, candidate)
	if len(candidate) == 0 || len(housekeeping) == 0 {
		m.status = "Keep at least one protected CPU and one housekeeping CPU."
		return
	}
	m.grubProtectedCPUs = candidate
	m.status = ""
	m.rebuildGRUBCPUSections()
}

func (m Model) validateGRUBDraft() error {
	housekeeping := HousekeepingCPUs(m.irqSnapshot.OnlineCPUs, m.grubProtectedCPUs)
	return ValidateIRQPolicy(m.irqSnapshot.OnlineCPUs, m.grubProtectedCPUs, housekeeping)
}

func renderGRUBIntroduction() string {
	return strings.Join([]string{
		"Guided, Ansible-managed real-time boot configuration.",
		"",
		"You choose CPU roles; the review explains every kernel parameter.",
		"The playbook owns only /etc/default/grub.d/99-linuxcncsetup-rt.cfg",
		"and preserves the distribution's main /etc/default/grub file.",
	}, "\n")
}

func (m Model) renderGRUBCPUSelection() string {
	return strings.Join([]string{
		"Choose CPUs reserved for LinuxCNC real-time threads.",
		"",
		"Protected:    " + displayCPUList(m.grubProtectedCPUs),
		"Housekeeping: " + displayCPUList(HousekeepingCPUs(
			m.irqSnapshot.OnlineCPUs, m.grubProtectedCPUs,
		)),
		"",
		"Space or Enter toggles a CPU. SMT siblings move together.",
	}, "\n")
}

func (m Model) renderGRUBReview(confirming bool) string {
	protected := displayCPUList(m.grubProtectedCPUs)
	housekeeping := displayCPUList(HousekeepingCPUs(
		m.irqSnapshot.OnlineCPUs, m.grubProtectedCPUs,
	))
	lines := []string{
		"Protected CPUs:    " + protected,
		"Housekeeping CPUs: " + housekeeping,
		"",
		"isolcpus=" + protected + "  — keeps normal scheduler work off RT CPUs",
		"nohz_full=" + protected + " — suppresses periodic scheduler ticks while one task runs",
		"rcu_nocbs=" + protected + " — moves RCU callback work away from RT CPUs",
		"irqaffinity=" + housekeeping + " — defaults movable device IRQs to OS CPUs",
		"kthread_cpus=" + housekeeping + " — defaults eligible kernel threads to OS CPUs",
		"nmi_watchdog=0 — disables watchdog NMIs that can add latency",
		"",
		"Not enabled: mitigations=off (security risk), idle=poll (heat/power cost).",
		"Ansible writes one managed drop-in and regenerates grub.cfg.",
		"Nothing becomes active until reboot.",
	}
	if confirming {
		lines = append(lines, "", warningStyle.Render("Apply this boot configuration?"), "Press y to apply or n to cancel.")
	} else {
		lines = append(lines, "", "Press Enter to apply with Ansible.")
	}
	return strings.Join(lines, "\n")
}

func (m Model) runGRUBPlaybook(action sectionAction) tea.Cmd {
	housekeeping := HousekeepingCPUs(m.irqSnapshot.OnlineCPUs, m.grubProtectedCPUs)
	return runEmbeddedPlaybook(action, playbooks.GRUBRealtime, map[string]any{
		"grub_rt_state":             "present",
		"grub_rt_protected_cpus":    FormatCPUList(m.grubProtectedCPUs),
		"grub_rt_housekeeping_cpus": FormatCPUList(housekeeping),
	})
}
