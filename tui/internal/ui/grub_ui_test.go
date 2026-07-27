package ui

import (
	"strings"
	"testing"
)

func TestGRUBReviewExplainsEveryManagedParameter(t *testing.T) {
	model := New()
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{OnlineCPUs: []int{0, 1, 2, 3}}
	model.grubProtectedCPUs = []int{2, 3}

	rendered := model.renderGRUBReview(false)
	for _, expected := range []string{
		"Protected CPUs:    2-3",
		"Housekeeping CPUs: 0-1",
		"isolcpus=2-3",
		"nohz_full=2-3",
		"rcu_nocbs=2-3",
		"irqaffinity=0-1",
		"kthread_cpus=0-1",
		"nmi_watchdog=0",
		"mitigations=off (security risk)",
		"idle=poll (heat/power cost)",
		"Nothing becomes active until reboot",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("GRUB review does not contain %q", expected)
		}
	}
}

func TestGRUBCPUSelectionKeepsBothRolesPopulated(t *testing.T) {
	model := New()
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{OnlineCPUs: []int{0, 1}}
	model.grubProtectedCPUs = []int{1}
	model.rebuildGRUBCPUSections()

	model.toggleGRUBCPU("1")
	if len(model.grubProtectedCPUs) != 1 || model.grubProtectedCPUs[0] != 1 {
		t.Fatalf("removed the final protected CPU: %v", model.grubProtectedCPUs)
	}
	if !strings.Contains(model.status, "at least one protected") {
		t.Fatalf("missing role validation message: %q", model.status)
	}
}
