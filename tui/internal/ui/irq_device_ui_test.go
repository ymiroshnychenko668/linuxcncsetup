package ui

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestIRQDeviceListNavigationShowsRealProcData(t *testing.T) {
	model := irqDeviceUIFixtureModel()
	sections := model.visibleSections()

	if len(sections) != 6 {
		t.Fatalf("device page has %d rows; want two devices, one kernel row, full table, refresh, and back", len(sections))
	}
	expectedActions := []sectionAction{
		actionIRQDeviceSelect,
		actionIRQDeviceSelect,
		actionIRQKernelCounters,
		actionIRQFullInterrupts,
		actionIRQDeviceRefresh,
		actionBack,
	}
	for index, expected := range expectedActions {
		if sections[index].action != expected {
			t.Fatalf("device row %d action = %d; want %d", index, sections[index].action, expected)
		}
	}
	if sections[0].value != "pci:0000:0a:00.0" ||
		!strings.Contains(sections[0].title, "nvme0") {
		t.Fatalf("unexpected editable device row: %#v", sections[0])
	}
	if !strings.Contains(sections[1].title, "read-only") {
		t.Fatalf("ambiguous device is not visibly read-only: %#v", sections[1])
	}

	content := model.View().Content
	for _, expected := range []string{
		"nvme0 [0000:0a:00.0]",
		"Stable match: PCI 0000:0a:00.0",
		"CPU0",
		"CPU1",
		"Requested",
		"IRQ 39",
		"nvme0q0",
		"requested=0-1",
		"effective=2",
		"IRQ 40",
		"nvme0q1",
		"effective=3",
		"Σ",
		"combined",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("device detail does not contain %q", expected)
		}
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if command != nil {
		t.Fatal("moving through the device list should not execute a command")
	}
	result := updated.(Model)
	if result.selected != 1 {
		t.Fatalf("Down selected row %d; want read-only device row 1", result.selected)
	}
	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("opening a read-only device should not execute a command")
	}
	result = updated.(Model)
	if result.page != menuIRQDevices {
		t.Fatalf("read-only device unexpectedly opened page %d", result.page)
	}
	if !strings.Contains(result.status, "read-only") ||
		!strings.Contains(result.status, "ambiguous") {
		t.Fatalf("read-only selection warning = %q", result.status)
	}

	result.selected = 2
	content = result.View().Content
	for _, expected := range []string{
		"Read-only /proc/interrupts row",
		"NMI",
		"CPU0",
		"26",
		"NMI: 5 6 7 8 Non-maskable interrupts",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("kernel-counter detail does not contain %q", expected)
		}
	}

	result.selected = 3
	content = result.View().Content
	for _, expected := range []string{
		"Complete /proc/interrupts",
		"4 rows across 4 CPUs",
		"39",
		"nvme0q0",
		"NMI",
		"Non-maskable interrupts",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("full interrupt table does not contain %q", expected)
		}
	}
}

func TestIRQDeviceSelectionCPUTogglingAndReviewNavigation(t *testing.T) {
	model := irqDeviceUIFixtureModel()

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("selecting a device should not execute a command")
	}
	result := updated.(Model)
	if result.page != menuIRQDeviceCPUs || result.selected != 0 {
		t.Fatalf("device selection opened page %d at row %d", result.page, result.selected)
	}
	if result.irqSelectedDeviceID != "pci:0000:0a:00.0" {
		t.Fatalf("selected device ID = %q", result.irqSelectedDeviceID)
	}
	if want := []int{0, 1}; !reflect.DeepEqual(result.irqDeviceCPUs, want) {
		t.Fatalf("initial device CPUs = %v; want requested affinity %v", result.irqDeviceCPUs, want)
	}
	if len(result.irqDeviceCPUSections) != 6 {
		t.Fatalf("CPU page has %d rows; want four CPUs, Continue, and Back", len(result.irqDeviceCPUSections))
	}

	result.selected = 0
	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if command != nil {
		t.Fatal("Space toggling a CPU should not execute a command")
	}
	result = updated.(Model)
	if want := []int{1}; !reflect.DeepEqual(result.irqDeviceCPUs, want) {
		t.Fatalf("after removing CPU 0, target CPUs = %v; want %v", result.irqDeviceCPUs, want)
	}

	result.selected = 1
	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if command != nil {
		t.Fatal("rejecting an empty CPU mask should not execute a command")
	}
	result = updated.(Model)
	if want := []int{1}; !reflect.DeepEqual(result.irqDeviceCPUs, want) {
		t.Fatalf("minimum-one rule changed target CPUs to %v; want %v", result.irqDeviceCPUs, want)
	}
	if !strings.Contains(result.status, "at least one") {
		t.Fatalf("minimum-one warning = %q", result.status)
	}

	result.selected = 2
	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("Enter toggling a CPU should not execute a command")
	}
	result = updated.(Model)
	if want := []int{1, 2}; !reflect.DeepEqual(result.irqDeviceCPUs, want) {
		t.Fatalf("after adding CPU 2, target CPUs = %v; want %v", result.irqDeviceCPUs, want)
	}

	result.selected = 4
	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("continuing to review should not execute a command")
	}
	result = updated.(Model)
	if result.page != menuIRQDeviceReview || result.selected != 0 {
		t.Fatalf("Continue opened page %d at row %d", result.page, result.selected)
	}
	content := result.View().Content
	for _, expected := range []string{
		"Device:        nvme0 [0000:0a:00.0]",
		"Stable match:  PCI 0000:0a:00.0",
		"Current IRQs:  39,40",
		"Target CPUs:   1-2",
		"numeric IRQ IDs are never saved",
		"Apply now is separate",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("review does not contain %q", expected)
		}
	}

	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil {
		t.Fatal("returning from review should not execute a command")
	}
	result = updated.(Model)
	if result.page != menuIRQDeviceCPUs ||
		result.currentSection().action != actionIRQDeviceContinue {
		t.Fatalf("Esc returned to page %d action %d", result.page, result.currentSection().action)
	}
}

func TestIRQDeviceDetailTableShowsVectorsInOneScreen(t *testing.T) {
	model := irqDeviceUIFixtureModel()
	model.height = 22

	content := model.View().Content
	if !strings.Contains(content, "IRQ 39") ||
		!strings.Contains(content, "nvme0q0") {
		t.Fatal("device table does not show the first IRQ vector")
	}
	if !strings.Contains(content, "IRQ 40") ||
		!strings.Contains(content, "nvme0q1") {
		t.Fatal("device table does not show the second IRQ vector")
	}
	if strings.Contains(content, "PgDn: more details") {
		t.Fatal("two-vector device table unexpectedly requires paging")
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if command != nil {
		t.Fatal("scrolling device details should not execute a command")
	}
	result := updated.(Model)
	if result.irqDeviceDetailOffset != 0 {
		t.Fatal("PgDn changed the offset of a table that already fits")
	}

	updated, command = result.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if command != nil {
		t.Fatal("changing the selected device should not execute a command")
	}
	result = updated.(Model)
	if result.selected != 1 {
		t.Fatalf("Down selected row %d; want second device", result.selected)
	}
	if result.irqDeviceDetailOffset != 0 {
		t.Fatalf("changing device retained detail offset %d; want 0", result.irqDeviceDetailOffset)
	}
}

func TestIRQDeviceRefreshPreservesHighlightedRowAndClampsDisappearedRow(t *testing.T) {
	snapshot, err := ProbeIRQSnapshot()
	if err != nil {
		t.Skipf("live IRQ snapshot is unavailable: %v", err)
	}
	inventory, err := ProbeIRQDeviceInventory(snapshot)
	if err != nil {
		t.Skipf("live IRQ device inventory is unavailable: %v", err)
	}
	if len(inventory.Devices) < 2 || len(inventory.Pseudo) == 0 {
		t.Skipf(
			"live inventory has %d devices and %d pseudo rows; need two and one",
			len(inventory.Devices),
			len(inventory.Pseudo),
		)
	}

	model := New()
	model.page = menuIRQDevices
	model.irqSnapshot = snapshot
	model.irqSnapshotLoaded = true
	model.irqDeviceInventory = inventory
	model.irqDeviceInventoryLoaded = true
	model.irqSelectedDeviceID = inventory.Devices[0].ID
	model.rebuildIRQDeviceSections()

	highlightedDeviceID := inventory.Devices[1].ID
	model.selectIRQInventoryRow(actionIRQDeviceSelect, highlightedDeviceID)
	if model.currentSection().value != highlightedDeviceID {
		t.Fatalf("could not highlight device %q", highlightedDeviceID)
	}
	model.refreshIRQDeviceInventory(false)
	current := model.currentSection()
	if current.action != actionIRQDeviceSelect ||
		current.value != highlightedDeviceID {
		t.Fatalf(
			"refresh selected action/value %d/%q; want highlighted device %q",
			current.action,
			current.value,
			highlightedDeviceID,
		)
	}
	if current.value == model.irqSelectedDeviceID {
		t.Fatal("refresh returned to the last edited device instead of the highlighted device")
	}

	highlightedPseudoID := model.irqDeviceInventory.Pseudo[0].ID
	model.selectIRQInventoryRow(actionIRQKernelCounters, highlightedPseudoID)
	model.refreshIRQDeviceInventory(false)
	current = model.currentSection()
	if current.action != actionIRQKernelCounters ||
		current.value != highlightedPseudoID {
		t.Fatalf(
			"refresh selected action/value %d/%q; want highlighted pseudo row %q",
			current.action,
			current.value,
			highlightedPseudoID,
		)
	}

	model.irqDeviceSections = append(
		[]section{{
			title:  "disappeared fixture",
			action: actionIRQDeviceSelect,
			value:  "pci:ffff:ff:ff.7",
		}},
		model.irqDeviceSections...,
	)
	model.selected = 0
	model.refreshIRQDeviceInventory(false)
	if model.selected < 0 || model.selected >= len(model.visibleSections()) {
		t.Fatalf(
			"disappeared row left selection %d outside %d refreshed rows",
			model.selected,
			len(model.visibleSections()),
		)
	}
	if model.selected != 0 {
		t.Fatalf("disappeared row clamped to %d; want first refreshed row", model.selected)
	}
	_ = model.View()
}

func TestIRQDeviceDefaultViewHeightAndWrappedDetailPaging(t *testing.T) {
	model := irqDeviceUIFixtureModel()
	model.width = defaultWidth
	model.height = defaultHeight
	model.irqDeviceInventory.Devices[0].IRQs[0].Description =
		strings.Repeat("pci-controller/path:queue-data, ", 10)
	model.irqDeviceInventory.Devices[0].IRQs[1].Description =
		strings.Repeat("second-vector/path:queue-data, ", 6) + "LATE42"
	model.rebuildIRQDeviceSections()

	logical := model.irqDeviceDetailLines(model.currentSection().value)
	physical := model.wrapIRQDeviceLines(logical)
	if len(physical) <= len(logical) {
		t.Fatalf(
			"long fixture did not wrap: logical=%d physical=%d width=%d",
			len(logical),
			len(physical),
			model.irqDeviceDetailWidth(),
		)
	}
	logicalMaximumOffset := max(
		len(logical)-model.irqDeviceDetailHeight()+1,
		0,
	)
	physicalMaximumOffset := max(
		len(physical)-model.irqDeviceDetailHeight()+1,
		0,
	)
	if physicalMaximumOffset <= logicalMaximumOffset {
		t.Fatalf(
			"physical maximum offset %d does not exceed logical offset %d",
			physicalMaximumOffset,
			logicalMaximumOffset,
		)
	}

	content := model.View().Content
	if rows := renderedTerminalRows(content); rows > defaultHeight {
		t.Fatalf("default device view is %d rows; terminal height is %d", rows, defaultHeight)
	}
	if strings.Contains(content, "LATE42") {
		t.Fatal("late wrapped detail unexpectedly appears on the first page")
	}

	result := model
	sawLateDetail := false
	for step := 0; step < 100; step++ {
		previousOffset := result.irqDeviceDetailOffset
		updated, command := result.Update(
			tea.KeyPressMsg{Code: tea.KeyPgDown},
		)
		if command != nil {
			t.Fatal("PgDn should not execute a command")
		}
		result = updated.(Model)
		page := result.View().Content
		if strings.Contains(page, "LATE42") {
			sawLateDetail = true
		}
		if rows := renderedTerminalRows(page); rows > defaultHeight {
			t.Fatalf(
				"device view after PgDn is %d rows; terminal height is %d",
				rows,
				defaultHeight,
			)
		}
		if result.irqDeviceDetailOffset == previousOffset {
			break
		}
	}
	if result.irqDeviceDetailOffset != physicalMaximumOffset {
		t.Fatalf(
			"PgDn stopped at physical offset %d; want %d",
			result.irqDeviceDetailOffset,
			physicalMaximumOffset,
		)
	}
	if !sawLateDetail {
		t.Fatal("PgDn did not traverse far enough to expose the late wrapped detail")
	}
}

func TestIRQDeviceReviewActionsAndConfirmations(t *testing.T) {
	model := irqDeviceUIFixtureModel()
	model.page = menuIRQDeviceReview
	model.selected = 0
	model.irqSelectedDeviceID = "pci:0000:0a:00.0"
	model.irqDeviceCPUs = []int{2, 3}

	sections := model.visibleSections()
	expectedActions := []sectionAction{
		actionIRQDevicePreview,
		actionIRQDevicePersist,
		actionIRQDeviceApplyLive,
		actionIRQDeviceRemove,
		actionBack,
	}
	if len(sections) != len(expectedActions) {
		t.Fatalf("review has %d actions; want %d", len(sections), len(expectedActions))
	}
	for index, expected := range expectedActions {
		if sections[index].action != expected {
			t.Fatalf("review row %d action = %d; want %d", index, sections[index].action, expected)
		}
	}

	model.selected = 1
	model.confirming = true
	content := model.View().Content
	for _, expected := range []string{
		"Save this device rule?",
		"matched by stable device",
		"Numeric IRQs are",
		"Live IRQs stay unchanged",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("persistent confirmation does not contain %q", expected)
		}
	}

	model.selected = 2
	content = model.View().Content
	for _, expected := range []string{
		"Apply this device affinity now?",
		"immediately writes every currently",
		"LinuxCNC must be stopped",
		"does not",
		"save the rule",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("live confirmation does not contain %q", expected)
		}
	}

	model.confirming = false
	if err := model.validateIRQDeviceAction(actionIRQDeviceRemove); err == nil ||
		!strings.Contains(err.Error(), "no saved rule") {
		t.Fatalf("remove without a saved rule error = %v", err)
	}
	device, _ := model.irqDeviceByID(model.irqSelectedDeviceID)
	model.irqSnapshot.ManagedPolicy.ConfigData = &ManagedIRQConfig{
		SchemaVersion: 2,
		DeviceRules: []ManagedIRQDeviceRule{{
			Selector: device.Selector,
			CPUList:  "0-1",
			CPUs:     []int{0, 1},
			Label:    device.Label,
		}},
	}
	if err := model.validateIRQDeviceAction(actionIRQDeviceRemove); err != nil {
		t.Fatalf("remove with a saved rule is invalid: %v", err)
	}

	model.selected = 2
	model.confirming = true
	updated, command := model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if command != nil {
		t.Fatal("cancelling live apply should not execute a command")
	}
	result := updated.(Model)
	if result.confirming ||
		result.status != "Live device IRQ affinity cancelled." {
		t.Fatalf("unexpected live-apply cancellation state: confirming=%v status=%q", result.confirming, result.status)
	}
}

func TestIRQDevicePersistentActionsRejectUnparsedManagedPolicyButLiveIsAllowed(t *testing.T) {
	model := irqDeviceUIFixtureModel()
	model.irqSelectedDeviceID = "pci:0000:0a:00.0"
	model.irqDeviceCPUs = []int{2, 3}
	model.irqSnapshot.ManagedPolicy = ManagedIRQPolicyStatus{
		Config: ManagedIRQComponentStatus{
			Path:    "/etc/linuxcncsetup/irq-affinity.yml",
			Present: true,
		},
		ConfigPresent: true,
		ConfigPath:    "/etc/linuxcncsetup/irq-affinity.yml",
		ConfigData:    nil,
	}

	for _, action := range []sectionAction{
		actionIRQDevicePreview,
		actionIRQDevicePersist,
		actionIRQDeviceRemove,
	} {
		err := model.validateIRQDeviceAction(action)
		if err == nil {
			t.Fatalf("persistent action %d accepted an unparsed managed policy", action)
		}
		for _, expected := range []string{
			"/etc/linuxcncsetup/irq-affinity.yml",
			"could not be parsed and validated",
			"refusing to overwrite",
		} {
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("persistent action %d error %q does not contain %q", action, err, expected)
			}
		}
	}

	if err := model.validateIRQDeviceAction(actionIRQDeviceApplyLive); err != nil {
		t.Fatalf("live apply was blocked by an unparsed persistent policy: %v", err)
	}
}

func TestIRQDeviceActionFinishedPreservesReviewSelectionForLaterDevice(t *testing.T) {
	snapshot, err := ProbeIRQSnapshot()
	if err != nil {
		t.Skipf("live IRQ snapshot is unavailable: %v", err)
	}
	inventory, err := ProbeIRQDeviceInventory(snapshot)
	if err != nil {
		t.Skipf("live IRQ device inventory is unavailable: %v", err)
	}
	const laterDeviceIndex = 5
	if len(inventory.Devices) <= laterDeviceIndex {
		t.Skipf(
			"live IRQ inventory has %d devices; need at least %d for the regression",
			len(inventory.Devices),
			laterDeviceIndex+1,
		)
	}
	selectedDeviceID := inventory.Devices[laterDeviceIndex].ID

	tests := []struct {
		name      string
		action    sectionAction
		selection int
	}{
		{
			name:      "persist",
			action:    actionIRQDevicePersist,
			selection: 1,
		},
		{
			name:      "live apply",
			action:    actionIRQDeviceApplyLive,
			selection: 2,
		},
		{
			name:      "remove",
			action:    actionIRQDeviceRemove,
			selection: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := New()
			model.width = 180
			model.height = 42
			model.page = menuIRQDeviceReview
			model.selected = test.selection
			model.confirming = true
			model.irqSnapshot = snapshot
			model.irqSnapshotLoaded = true
			model.irqDeviceInventory = inventory
			model.irqDeviceInventoryLoaded = true
			model.irqSelectedDeviceID = selectedDeviceID
			model.irqDeviceCPUs = append([]int(nil), snapshot.OnlineCPUs[:1]...)
			model.rebuildIRQDeviceSections()
			if len(model.irqDeviceSections) <= laterDeviceIndex ||
				model.irqDeviceSections[laterDeviceIndex].value != selectedDeviceID {
				t.Fatalf(
					"regression device %q is not at section index %d",
					selectedDeviceID,
					laterDeviceIndex,
				)
			}

			updated, command := model.Update(actionFinishedMsg{
				action: test.action,
			})
			if command != nil {
				t.Fatal("handling a finished device action should not execute another command")
			}
			result := updated.(Model)
			if result.page != menuIRQDeviceReview {
				t.Fatalf("finished action changed review page to %d", result.page)
			}
			if result.selected != test.selection {
				t.Fatalf(
					"finished action changed review selection from %d to %d",
					test.selection,
					result.selected,
				)
			}
			if result.currentSection().action != test.action {
				t.Fatalf(
					"review action after refresh = %d; want %d",
					result.currentSection().action,
					test.action,
				)
			}
			if _, found := result.irqDeviceByID(selectedDeviceID); !found {
				t.Fatalf("selected live device %q disappeared during refresh", selectedDeviceID)
			}

			// This was the original panic point when a device-list index was
			// copied into the shorter review-action list.
			_ = result.View()
		})
	}
}

func TestIRQDeviceRuleVariablesReplaceOnlySelectedPersistentRule(t *testing.T) {
	model := irqDeviceUIFixtureModel()
	model.irqSelectedDeviceID = "pci:0000:0a:00.0"
	model.irqDeviceCPUs = []int{2, 3}
	device, _ := model.irqDeviceByID(model.irqSelectedDeviceID)
	otherRule := ManagedIRQDeviceRule{
		Selector: IRQDeviceSelector{
			Kind:  IRQDeviceSelectorAction,
			Value: "rtc0",
		},
		CPUList: "1",
		CPUs:    []int{1},
		Label:   "rtc0",
	}
	model.irqSnapshot.ManagedPolicy.ConfigData = &ManagedIRQConfig{
		SchemaVersion: 2,
		DefaultPolicy: &ManagedIRQDefaultPolicy{
			HousekeepingCPUList: "0-2",
			ProtectedCPUList:    "3",
			HousekeepingCPUs:    []int{0, 1, 2},
			ProtectedCPUs:       []int{3},
		},
		DeviceRules: []ManagedIRQDeviceRule{
			{
				Selector: device.Selector,
				CPUList:  "0-1",
				CPUs:     []int{0, 1},
				Label:    "old NVMe label",
			},
			otherRule,
		},
	}

	rules := model.updatedIRQDeviceRules(device, false)
	if len(rules) != 2 {
		t.Fatalf("updated rules = %#v; want unrelated rule plus replacement", rules)
	}
	if !selectorsEqual(rules[0].Selector, otherRule.Selector) {
		t.Fatalf("unrelated rule was not retained: %#v", rules)
	}
	replacement := rules[1]
	if !selectorsEqual(replacement.Selector, device.Selector) ||
		replacement.CPUList != "2-3" ||
		!reflect.DeepEqual(replacement.CPUs, []int{2, 3}) ||
		replacement.Label != device.Label {
		t.Fatalf("selected rule replacement = %#v", replacement)
	}

	variables := irqDeviceRuleVariables(rules)
	if len(variables) != 2 {
		t.Fatalf("playbook device rules = %#v", variables)
	}
	selector, ok := variables[1]["selector"].(map[string]any)
	if !ok {
		t.Fatalf("selector variable has type %T", variables[1]["selector"])
	}
	if selector["kind"] != "pci_bdf" ||
		selector["value"] != "0000:0a:00.0" ||
		variables[1]["cpus"] != "2-3" ||
		variables[1]["label"] != device.Label {
		t.Fatalf("replacement playbook variables = %#v", variables[1])
	}

	removed := model.updatedIRQDeviceRules(device, true)
	if len(removed) != 1 || !selectorsEqual(removed[0].Selector, otherRule.Selector) {
		t.Fatalf("removing selected rule changed unrelated rules: %#v", removed)
	}
	defaultVariables := irqDefaultPolicyVariables(model.currentIRQDefaultPolicy())
	if defaultVariables["housekeeping_cpus"] != "0-2" ||
		defaultVariables["protected_cpus"] != "3" {
		t.Fatalf("default policy was not preserved: %#v", defaultVariables)
	}
}

func TestIRQDevicePlaybookVariablesSeparatePersistenceAndLiveApply(t *testing.T) {
	model := irqDeviceUIFixtureModel()
	model.irqSelectedDeviceID = "pci:0000:0a:00.0"
	model.irqDeviceCPUs = []int{2, 3}

	binaryDirectory := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "args")
	writeExecutableTestFixture(t, filepath.Join(binaryDirectory, "ansible-playbook"), `#!/bin/sh
: > "$IRQ_UI_CAPTURE"
for argument in "$@"; do
    printf '%s\n' "$argument" >> "$IRQ_UI_CAPTURE"
done
`)
	writeExecutableTestFixture(t, filepath.Join(binaryDirectory, "sudo"), `#!/bin/sh
if [ "$1" = "--" ]; then
    shift
fi
exec "$@"
`)
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("IRQ_UI_CAPTURE", capturePath)

	finished := executeIRQDeviceTeaCommand(
		t,
		model.runIRQDevicePersistentPlaybook(
			actionIRQDevicePreview,
			true,
			false,
		),
	)
	if finished.err != nil {
		t.Fatalf("persistent preview command failed: %v", finished.err)
	}
	persistentArgs := readCapturedIRQDeviceArgs(t, capturePath)
	if !containsExactString(persistentArgs, "--check") {
		t.Fatalf("persistent preview did not use Ansible check mode: %v", persistentArgs)
	}
	persistentVariables := capturedIRQDeviceExtraVars(t, persistentArgs)
	if persistentVariables["irq_affinity_operation"] != "configure" ||
		persistentVariables["irq_affinity_state"] != "present" {
		t.Fatalf("persistent operation variables = %#v", persistentVariables)
	}
	if _, found := persistentVariables["irq_device_rules"]; !found {
		t.Fatalf("persistent operation has no device_rules: %#v", persistentVariables)
	}
	if _, found := persistentVariables["irq_device_rule"]; found {
		t.Fatalf("persistent operation unexpectedly uses singular live rule: %#v", persistentVariables)
	}

	finished = executeIRQDeviceTeaCommand(
		t,
		model.runIRQDeviceLivePlaybook(actionIRQDeviceApplyLive),
	)
	if finished.err != nil {
		t.Fatalf("live-apply command failed: %v", finished.err)
	}
	liveArgs := readCapturedIRQDeviceArgs(t, capturePath)
	if containsExactString(liveArgs, "--check") {
		t.Fatalf("live apply unexpectedly used Ansible check mode: %v", liveArgs)
	}
	liveVariables := capturedIRQDeviceExtraVars(t, liveArgs)
	if liveVariables["irq_affinity_operation"] != "apply_device_live" ||
		liveVariables["irq_affinity_state"] != "present" {
		t.Fatalf("live operation variables = %#v", liveVariables)
	}
	if _, found := liveVariables["irq_device_rule"]; !found {
		t.Fatalf("live operation has no singular device rule: %#v", liveVariables)
	}
	if _, found := liveVariables["irq_device_rules"]; found {
		t.Fatalf("live operation unexpectedly rewrites persistent rules: %#v", liveVariables)
	}
}

func TestIRQGlobalKernelCounterFormatting(t *testing.T) {
	lines := renderIRQCPUCounts([]IRQCPUCount{{CPU: -1, Count: 7}}, "")
	if !reflect.DeepEqual(lines, []string{"global=7"}) {
		t.Fatalf("global counter formatting = %v", lines)
	}
}

func renderedTerminalRows(content string) int {
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

func irqDeviceUIFixtureModel() Model {
	model := New()
	model.width = 180
	model.height = 42
	model.page = menuIRQDevices
	model.irqSnapshotLoaded = true
	model.irqSnapshot = IRQSnapshot{
		OnlineCPUs: []int{0, 1, 2, 3},
	}
	model.irqDeviceInventoryLoaded = true
	model.irqDeviceInventory = IRQDeviceInventory{
		CPUs: []int{0, 1, 2, 3},
		Devices: []IRQDeviceGroup{
			{
				ID:          "pci:0000:0a:00.0",
				Label:       "nvme0 [0000:0a:00.0]",
				Selector:    IRQDeviceSelector{Kind: IRQDeviceSelectorPCIBDF, Value: "0000:0a:00.0"},
				Persistable: true,
				Editable:    true,
				Actions:     []string{"nvme0q0", "nvme0q1"},
				IRQs: []IRQDeviceIRQ{
					{
						Number:               39,
						Action:               "nvme0q0",
						ChipName:             "IR-PCI-MSIX-0000:0a:00.0",
						PCIBDF:               "0000:0a:00.0",
						PerCPU:               irqDeviceUICounts(10, 0, 0, 0),
						Total:                10,
						RequestedAffinity:    "0-1",
						RequestedCPUs:        []int{0, 1},
						EffectiveAffinity:    "2",
						EffectiveCPUs:        []int{2},
						AffinityReadable:     true,
						EffectiveReadable:    true,
						AffinityFileWritable: true,
					},
					{
						Number:               40,
						Action:               "nvme0q1",
						ChipName:             "IR-PCI-MSIX-0000:0a:00.0",
						PCIBDF:               "0000:0a:00.0",
						PerCPU:               irqDeviceUICounts(0, 20, 0, 0),
						Total:                20,
						RequestedAffinity:    "0-1",
						RequestedCPUs:        []int{0, 1},
						EffectiveAffinity:    "3",
						EffectiveCPUs:        []int{3},
						AffinityReadable:     true,
						EffectiveReadable:    true,
						AffinityFileWritable: true,
					},
				},
				PerCPU: irqDeviceUICounts(10, 20, 0, 0),
				Total:  30,
			},
			{
				ID:                   "irq:9",
				Label:                "ambiguous shared IRQ",
				PersistabilityReason: "ambiguous shared action identity",
				ReadOnlyReason:       "ambiguous shared action identity",
				IRQs: []IRQDeviceIRQ{{
					Number: 9,
					Action: "foo, bar",
					PerCPU: irqDeviceUICounts(1, 1, 1, 1),
					Total:  4,
				}},
				PerCPU: irqDeviceUICounts(1, 1, 1, 1),
				Total:  4,
			},
		},
		Pseudo: []IRQInterruptRow{{
			ID:                "NMI",
			Number:            -1,
			PerCPU:            irqDeviceUICounts(5, 6, 7, 8),
			Total:             26,
			Description:       "Non-maskable interrupts",
			Raw:               "NMI: 5 6 7 8 Non-maskable interrupts",
			CompleteCPUCounts: true,
		}},
	}
	model.rebuildIRQDeviceSections()
	return model
}

func irqDeviceUICounts(values ...uint64) []IRQCPUCount {
	counts := make([]IRQCPUCount, 0, len(values))
	for cpu, value := range values {
		counts = append(counts, IRQCPUCount{CPU: cpu, Count: value})
	}
	return counts
}

type irqDeviceCommandHarness struct {
	command tea.Cmd
	result  chan<- actionFinishedMsg
}

func (harness irqDeviceCommandHarness) Init() tea.Cmd {
	return harness.command
}

func (harness irqDeviceCommandHarness) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if finished, ok := message.(actionFinishedMsg); ok {
		harness.result <- finished
		return harness, tea.Quit
	}
	return harness, nil
}

func (irqDeviceCommandHarness) View() tea.View {
	return tea.NewView("")
}

func executeIRQDeviceTeaCommand(
	t *testing.T,
	command tea.Cmd,
) actionFinishedMsg {
	t.Helper()
	result := make(chan actionFinishedMsg, 1)
	program := tea.NewProgram(
		irqDeviceCommandHarness{command: command, result: result},
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	if _, err := program.Run(); err != nil {
		t.Fatalf("run Bubble Tea command harness: %v", err)
	}
	select {
	case finished := <-result:
		return finished
	default:
		t.Fatal("command harness completed without an action result")
		return actionFinishedMsg{}
	}
}

func writeExecutableTestFixture(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write executable fixture %s: %v", path, err)
	}
}

func readCapturedIRQDeviceArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured command arguments: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func capturedIRQDeviceExtraVars(
	t *testing.T,
	arguments []string,
) map[string]any {
	t.Helper()
	for index, argument := range arguments {
		if argument != "--extra-vars" || index+1 >= len(arguments) {
			continue
		}
		var variables map[string]any
		if err := json.Unmarshal([]byte(arguments[index+1]), &variables); err != nil {
			t.Fatalf("decode --extra-vars value: %v", err)
		}
		return variables
	}
	t.Fatalf("--extra-vars not found in arguments: %v", arguments)
	return nil
}

func containsExactString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
