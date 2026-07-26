package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseProcInterruptsPreservesRealRowsAndCPUCounts(t *testing.T) {
	table, err := ParseProcInterrupts([]byte(strings.Join([]string{
		"            CPU0       CPU2       CPU5",
		" 39:          1          2          3  IR-PCI-MSIX-0000:0a:00.0  0-edge  nvme0q0",
		" 40:         10         20         30  IR-PCI-MSIX-0000:0a:00.0  1-edge  nvme0q1",
		" NMI:         4          5          6  Non-maskable interrupts",
		" ERR:         7",
	}, "\n")))
	if err != nil {
		t.Fatalf("ParseProcInterrupts() error: %v", err)
	}
	if want := []int{0, 2, 5}; !reflect.DeepEqual(table.CPUs, want) {
		t.Fatalf("CPUs = %v; want %v", table.CPUs, want)
	}
	if len(table.Rows) != 4 {
		t.Fatalf("rows = %d; want 4", len(table.Rows))
	}

	nvme := table.Rows[0]
	if !nvme.Numeric || nvme.Number != 39 || nvme.ID != "39" {
		t.Fatalf("unexpected numeric row identity: %#v", nvme)
	}
	if nvme.Total != 6 || !nvme.CompleteCPUCounts {
		t.Fatalf("unexpected numeric row counts: %#v", nvme)
	}
	if want := []IRQCPUCount{{CPU: 0, Count: 1}, {CPU: 2, Count: 2}, {CPU: 5, Count: 3}}; !reflect.DeepEqual(nvme.PerCPU, want) {
		t.Fatalf("per-CPU counts = %#v; want %#v", nvme.PerCPU, want)
	}
	if nvme.Controller != "IR-PCI-MSIX-0000:0a:00.0" ||
		nvme.Description != "IR-PCI-MSIX-0000:0a:00.0 0-edge nvme0q0" {
		t.Fatalf("unexpected description parsing: %#v", nvme)
	}
	if !strings.Contains(nvme.Raw, "39:") {
		t.Fatalf("raw source row was not retained: %q", nvme.Raw)
	}

	nmi := table.Rows[2]
	if nmi.Numeric || nmi.Number != -1 || nmi.ID != "NMI" ||
		nmi.Total != 15 || !nmi.CompleteCPUCounts {
		t.Fatalf("unexpected NMI row: %#v", nmi)
	}
	global := table.Rows[3]
	if global.Numeric || global.CompleteCPUCounts ||
		!reflect.DeepEqual(global.PerCPU, []IRQCPUCount{{CPU: -1, Count: 7}}) {
		t.Fatalf("unexpected global pseudo-interrupt row: %#v", global)
	}
}

func TestParseProcInterruptsRejectsMalformedNumericCounters(t *testing.T) {
	for _, fixture := range []string{
		"not a CPU header\n 1: 0 0 timer\n",
		"CPU0 CPU1\n 1: 10 IR-IO-APIC timer\n",
		"CPU0 CPU0\n 1: 10 20 IR-IO-APIC timer\n",
	} {
		if _, err := ParseProcInterrupts([]byte(fixture)); err == nil {
			t.Fatalf("ParseProcInterrupts() accepted malformed input:\n%s", fixture)
		}
	}
}

func TestProbeIRQDeviceInventoryGroupsVectorsByStableDevice(t *testing.T) {
	root := t.TempDir()
	paths := IRQProbePathsForRoot(root)
	writeIRQDeviceFixture(t, filepath.Join(paths.ProcRoot, "interrupts"), strings.Join([]string{
		"            CPU0       CPU1       CPU2       CPU3",
		"  1:          1          2          3          4  IR-IO-APIC  1-fasteoi  i8042",
		"  2:          2          0          0          0  custom-chip  foo",
		"  3:          0          3          0          0  custom-chip  foo",
		"  4:          0          0          4          0  custom-chip  alpha",
		" 39:         10          0          0          0  IR-PCI-MSIX-0000:0a:00.0  0-edge  nvme0q0",
		" 40:          0         20          0          0  IR-PCI-MSIX-0000:0a:00.0  1-edge  nvme0q1",
		" 48:          0          0         30          0  IR-PCI-MSIX-0000:08:00.0  0-edge  xhci_hcd",
		" 56:          0          0          0         40  IR-PCI-MSIX-0000:0b:00.3  0-edge  xhci_hcd",
		" NMI:         5          6          7          8  Non-maskable interrupts",
	}, "\n"))

	writeIRQMetadataFixture(t, paths, 1, "i8042", "IR-IO-APIC")
	writeIRQMetadataFixture(t, paths, 2, "foo", "custom-chip")
	writeIRQMetadataFixture(t, paths, 3, "foo", "custom-chip")
	// Duplicate action names still represent multiple handlers on one shared
	// IRQ and must not be mistaken for one exact action.
	writeIRQMetadataFixture(t, paths, 4, "alpha, alpha", "custom-chip")
	writeIRQMetadataFixture(t, paths, 39, "nvme0q0", "IR-PCI-MSIX-0000:0a:00.0")
	writeIRQMetadataFixture(t, paths, 40, "nvme0q1", "IR-PCI-MSIX-0000:0a:00.0")
	writeIRQMetadataFixture(t, paths, 48, "xhci_hcd", "IR-PCI-MSIX-0000:08:00.0")
	writeIRQMetadataFixture(t, paths, 56, "xhci_hcd", "IR-PCI-MSIX-0000:0b:00.3")

	snapshot := IRQSnapshot{
		OnlineCPUs: []int{0, 1, 2, 3},
		IRQs: []IRQEntry{
			irqDeviceTestEntry(1, "i8042", "0-3", "0", true),
			irqDeviceTestEntry(2, "foo", "0-3", "0", true),
			irqDeviceTestEntry(3, "foo", "0-3", "1", true),
			irqDeviceTestEntry(4, "alpha", "0-3", "2", true),
			irqDeviceTestEntry(39, "nvme0q0", "0-1", "0", true),
			irqDeviceTestEntry(40, "nvme0q1", "0-1", "1", false),
			irqDeviceTestEntry(48, "xhci_hcd", "0-3", "2", true),
			irqDeviceTestEntry(56, "xhci_hcd", "0-3", "3", true),
		},
	}

	inventory, err := ProbeIRQDeviceInventoryWithOptions(
		snapshot,
		IRQDeviceProbeOptions{Paths: paths},
	)
	if err != nil {
		t.Fatalf("ProbeIRQDeviceInventoryWithOptions() error: %v", err)
	}
	if want := []int{0, 1, 2, 3}; !reflect.DeepEqual(inventory.CPUs, want) {
		t.Fatalf("inventory CPUs = %v; want %v", inventory.CPUs, want)
	}
	if len(inventory.Devices) != 6 {
		t.Fatalf("device groups = %d; want 6: %#v", len(inventory.Devices), inventory.Devices)
	}
	if len(inventory.Pseudo) != 1 || inventory.Pseudo[0].ID != "NMI" ||
		inventory.Pseudo[0].Total != 26 {
		t.Fatalf("unexpected pseudo rows: %#v", inventory.Pseudo)
	}
	if len(inventory.Problems) != 0 {
		t.Fatalf("unexpected inventory problems: %v", inventory.Problems)
	}

	nvme := requireIRQDeviceGroup(t, inventory, "pci:0000:0a:00.0")
	if nvme.Label != "nvme0 [0000:0a:00.0]" {
		t.Fatalf("NVMe label = %q", nvme.Label)
	}
	if nvme.Selector != (IRQDeviceSelector{Kind: IRQDeviceSelectorPCIBDF, Value: "0000:0a:00.0"}) {
		t.Fatalf("NVMe selector = %#v", nvme.Selector)
	}
	if !nvme.Persistable || !nvme.Editable || len(nvme.IRQs) != 2 || nvme.Total != 30 {
		t.Fatalf("unexpected NVMe group: %#v", nvme)
	}
	if want := []IRQCPUCount{{CPU: 0, Count: 10}, {CPU: 1, Count: 20}, {CPU: 2, Count: 0}, {CPU: 3, Count: 0}}; !reflect.DeepEqual(nvme.PerCPU, want) {
		t.Fatalf("NVMe counters = %#v; want %#v", nvme.PerCPU, want)
	}
	if nvme.IRQs[0].RequestedAffinity != "0-1" ||
		!reflect.DeepEqual(nvme.IRQs[0].RequestedCPUs, []int{0, 1}) ||
		nvme.IRQs[0].EffectiveAffinity != "0" ||
		!reflect.DeepEqual(nvme.IRQs[0].EffectiveCPUs, []int{0}) {
		t.Fatalf("IRQ affinity was not copied from snapshot: %#v", nvme.IRQs[0])
	}
	if nvme.IRQs[1].AffinityFileWritable {
		t.Fatalf("per-vector read-only state was lost: %#v", nvme.IRQs[1])
	}

	xhciA := requireIRQDeviceGroup(t, inventory, "pci:0000:08:00.0")
	xhciB := requireIRQDeviceGroup(t, inventory, "pci:0000:0b:00.3")
	if xhciA.Label == xhciB.Label || len(xhciA.IRQs) != 1 || len(xhciB.IRQs) != 1 {
		t.Fatalf("same-action PCI controllers were incorrectly combined: %#v / %#v", xhciA, xhciB)
	}

	legacy := requireIRQDeviceGroup(t, inventory, "action:i8042")
	if legacy.Selector.Kind != IRQDeviceSelectorAction || !legacy.Persistable || !legacy.Editable {
		t.Fatalf("single exact non-PCI action should be editable: %#v", legacy)
	}

	ambiguous := requireIRQDeviceGroup(t, inventory, "action:foo")
	if ambiguous.Persistable || ambiguous.Editable ||
		!strings.Contains(ambiguous.PersistabilityReason, "multiple IRQs") {
		t.Fatalf("ambiguous action group should be read-only: %#v", ambiguous)
	}

	shared := requireIRQDeviceGroup(t, inventory, "irq:4")
	if shared.Persistable || !strings.Contains(shared.ReadOnlyReason, "multiple action") {
		t.Fatalf("shared IRQ should be read-only: %#v", shared)
	}
}

func TestIRQDeviceInventoryUsesProcChipFallbackButRequiresExactAction(t *testing.T) {
	root := t.TempDir()
	paths := IRQProbePathsForRoot(root)
	writeIRQDeviceFixture(t, filepath.Join(paths.ProcRoot, "interrupts"), strings.Join([]string{
		"            CPU0       CPU1",
		" 70:          9          1  IR-PCI-MSIX-0000:0c:00.0  0-edge  fallback_name",
	}, "\n"))

	snapshot := IRQSnapshot{IRQs: []IRQEntry{irqDeviceTestEntry(70, "fallback_name", "0-1", "0", true)}}
	inventory, err := ProbeIRQDeviceInventoryWithOptions(
		snapshot,
		IRQDeviceProbeOptions{Paths: paths},
	)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	group := requireIRQDeviceGroup(t, inventory, "pci:0000:0c:00.0")
	if group.Selector.Kind != IRQDeviceSelectorPCIBDF || group.Persistable ||
		!strings.Contains(group.PersistabilityReason, "exact") {
		t.Fatalf("fallback identity should remain visible but read-only: %#v", group)
	}
	if group.IRQs[0].Action != "fallback_name" ||
		group.IRQs[0].ChipName != "IR-PCI-MSIX-0000:0c:00.0" {
		t.Fatalf("fallback metadata was not preserved: %#v", group.IRQs[0])
	}
}

func TestIRQDeviceGroupWithNoWritableVectorIsReadOnly(t *testing.T) {
	root := t.TempDir()
	paths := IRQProbePathsForRoot(root)
	writeIRQDeviceFixture(t, filepath.Join(paths.ProcRoot, "interrupts"), strings.Join([]string{
		"            CPU0       CPU1",
		" 85:          0         99  IR-PCI-MSI-0000:07:00.0  0-edge  rtw89_pci",
	}, "\n"))
	writeIRQMetadataFixture(t, paths, 85, "rtw89_pci", "IR-PCI-MSI-0000:07:00.0")

	snapshot := IRQSnapshot{IRQs: []IRQEntry{irqDeviceTestEntry(85, "rtw89_pci", "0-1", "1", false)}}
	inventory, err := ProbeIRQDeviceInventoryWithOptions(
		snapshot,
		IRQDeviceProbeOptions{Paths: paths},
	)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	group := requireIRQDeviceGroup(t, inventory, "pci:0000:07:00.0")
	if !group.Persistable || group.Editable ||
		!strings.Contains(group.ReadOnlyReason, "writable") {
		t.Fatalf("unexpected read-only group: %#v", group)
	}
}

func irqDeviceTestEntry(
	number int,
	name string,
	requested string,
	effective string,
	writable bool,
) IRQEntry {
	requestedCPUs, _ := ParseCPUList(requested)
	effectiveCPUs, _ := ParseCPUList(effective)
	return IRQEntry{
		Number:               number,
		Name:                 name,
		RequestedAffinity:    requested,
		RequestedCPUs:        requestedCPUs,
		EffectiveAffinity:    effective,
		EffectiveCPUs:        effectiveCPUs,
		AffinityReadable:     true,
		EffectiveReadable:    true,
		AffinityFileWritable: writable,
	}
}

func writeIRQMetadataFixture(
	t *testing.T,
	paths IRQProbePaths,
	irq int,
	actions string,
	chipName string,
) {
	t.Helper()
	root := filepath.Join(paths.SysRoot, "kernel/irq", strconv.Itoa(irq))
	writeIRQDeviceFixture(t, filepath.Join(root, "actions"), actions+"\n")
	writeIRQDeviceFixture(t, filepath.Join(root, "chip_name"), chipName+"\n")
}

func writeIRQDeviceFixture(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func requireIRQDeviceGroup(
	t *testing.T,
	inventory IRQDeviceInventory,
	id string,
) IRQDeviceGroup {
	t.Helper()
	for _, group := range inventory.Devices {
		if group.ID == id {
			return group
		}
	}
	t.Fatalf("device group %q not found in %#v", id, inventory.Devices)
	return IRQDeviceGroup{}
}
