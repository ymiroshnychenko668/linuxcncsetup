package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAndFormatCPUList(t *testing.T) {
	cpus, err := ParseCPUList("5,0-2,2,4")
	if err != nil {
		t.Fatalf("ParseCPUList() error: %v", err)
	}
	if want := []int{0, 1, 2, 4, 5}; !reflect.DeepEqual(cpus, want) {
		t.Fatalf("ParseCPUList() = %v; want %v", cpus, want)
	}
	if got, want := FormatCPUList([]int{5, 2, 1, 0, 4, 2}), "0-2,4-5"; got != want {
		t.Fatalf("FormatCPUList() = %q; want %q", got, want)
	}

	for _, invalid := range []string{"0-", "-1", "3-1", "0,,2", "cpu2"} {
		if _, err := ParseCPUList(invalid); err == nil {
			t.Errorf("ParseCPUList(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestParseCPUMask(t *testing.T) {
	for value, want := range map[string][]int{
		"3f":         {0, 1, 2, 3, 4, 5},
		"00000001":   {0},
		"1,00000000": {32},
		"0x5":        {0, 2},
	} {
		got, err := ParseCPUMask(value)
		if err != nil {
			t.Fatalf("ParseCPUMask(%q) error: %v", value, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ParseCPUMask(%q) = %v; want %v", value, got, want)
		}
	}
	if _, err := ParseCPUMask("not-a-mask"); err == nil {
		t.Fatal("ParseCPUMask() accepted a malformed mask")
	}
}

func TestRecommendedProtectedCPUs(t *testing.T) {
	tests := []struct {
		name     string
		online   []int
		isolated []int
		want     []int
	}{
		{name: "single CPU cannot isolate", online: []int{0}, want: []int{}},
		{name: "up to five uses last CPU", online: []int{0, 1, 2, 3}, want: []int{3}},
		{name: "six uses last two CPUs", online: []int{0, 1, 2, 3, 4, 5}, want: []int{4, 5}},
		{name: "noncontiguous CPU IDs", online: []int{0, 2, 4, 8, 10, 12}, want: []int{10, 12}},
		{name: "existing isolation wins", online: []int{0, 1, 2, 3, 4, 5}, isolated: []int{2, 4}, want: []int{2, 4}},
		{name: "offline isolation is filtered", online: []int{0, 1, 2, 3}, isolated: []int{3, 9}, want: []int{3}},
		{name: "all isolated falls back", online: []int{0, 1, 2}, isolated: []int{0, 1, 2}, want: []int{2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RecommendedProtectedCPUs(test.online, test.isolated)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("RecommendedProtectedCPUs() = %v; want %v", got, test.want)
			}
		})
	}
}

func TestValidateIRQPolicy(t *testing.T) {
	online := []int{0, 1, 2, 3, 4, 5}
	protected := []int{4, 5}
	housekeeping := HousekeepingCPUs(online, protected)
	if want := []int{0, 1, 2, 3}; !reflect.DeepEqual(housekeeping, want) {
		t.Fatalf("HousekeepingCPUs() = %v; want %v", housekeeping, want)
	}
	if err := ValidateIRQPolicy(online, protected, housekeeping); err != nil {
		t.Fatalf("ValidateIRQPolicy() error: %v", err)
	}

	tests := []struct {
		name         string
		protected    []int
		housekeeping []int
		errorText    string
	}{
		{name: "no protected", housekeeping: []int{0, 1, 2, 3, 4, 5}, errorText: "protected"},
		{name: "no housekeeping", protected: []int{0, 1, 2, 3, 4, 5}, errorText: "housekeeping"},
		{name: "overlap", protected: []int{4, 5}, housekeeping: []int{0, 1, 2, 3, 4}, errorText: "both"},
		{name: "offline protected", protected: []int{4, 8}, housekeeping: []int{0, 1, 2, 3, 5}, errorText: "not online"},
		{name: "unassigned", protected: []int{5}, housekeeping: []int{0, 1, 2, 3}, errorText: "no policy role"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateIRQPolicy(online, test.protected, test.housekeeping)
			if err == nil || !strings.Contains(err.Error(), test.errorText) {
				t.Fatalf("ValidateIRQPolicy() error = %v; want text %q", err, test.errorText)
			}
		})
	}
}

func TestProbeIRQSnapshotWithOptions(t *testing.T) {
	root := t.TempDir()
	paths := IRQProbePathsForRoot(root)

	writeProbeFixture(t, filepath.Join(paths.SysRoot, "devices/system/cpu/online"), "0-5\n")
	for cpu := 0; cpu < 6; cpu++ {
		topology := filepath.Join(paths.SysRoot, "devices/system/cpu", fmt.Sprintf("cpu%d/topology", cpu))
		writeProbeFixture(t, filepath.Join(topology, "core_id"), fmt.Sprintf("%d\n", cpu/2))
		writeProbeFixture(t, filepath.Join(topology, "physical_package_id"), "0\n")
		firstSibling := cpu - cpu%2
		writeProbeFixture(t, filepath.Join(topology, "thread_siblings_list"), fmt.Sprintf("%d-%d\n", firstSibling, firstSibling+1))
	}

	writeProbeFixture(t, filepath.Join(paths.ProcRoot, "cmdline"),
		"BOOT_IMAGE=/vmlinuz quiet isolcpus=domain,managed_irq,4-5 nohz_full=4-5 irqaffinity=0-3\n")
	writeProbeFixture(t, filepath.Join(paths.ProcRoot, "irq/default_smp_affinity"), "0000000f\n")
	writeProbeFixture(t, filepath.Join(paths.ProcRoot, "interrupts"), strings.Join([]string{
		"           CPU0       CPU1       CPU2       CPU3       CPU4       CPU5",
		" 42:         10          0          0          0          0          0  PCI-MSI  eth-old",
		" 43:          0         12          0          0          0          0  IO-APIC  i8042",
		" NMI:          1          1          1          1          1          1  Non-maskable interrupts",
	}, "\n"))
	writeProbeFixture(t, filepath.Join(paths.ProcRoot, "irq/42/smp_affinity_list"), "0-3\n")
	writeProbeFixture(t, filepath.Join(paths.ProcRoot, "irq/42/effective_affinity_list"), "0-1\n")
	writeProbeFixture(t, filepath.Join(paths.ProcRoot, "irq/43/smp_affinity_list"), "0-3\n")
	writeProbeFixture(t, filepath.Join(paths.ProcRoot, "irq/43/effective_affinity_list"), "0-3\n")
	writeProbeFixture(t, filepath.Join(paths.SysRoot, "kernel/irq/42/actions"), "enp3s0\n")

	writeProbeFixture(t, filepath.Join(paths.USRRoot, "sbin/irqbalance"), "# fixture\n")
	writeProbeFixture(t, filepath.Join(paths.EtcRoot, "linuxcncsetup/irq-affinity.yml"),
		`{"schema_version":1,"housekeeping_cpus":"0-3","protected_cpus":"4-5"}`)
	writeProbeFixture(t, filepath.Join(paths.LocalRoot, "libexec/linuxcncsetup-irq-affinity"), "# fixture\n")
	writeProbeFixture(t, filepath.Join(paths.EtcRoot, "systemd/system/linuxcncsetup-irq-affinity.service"), "[Service]\n")
	writeProbeFixture(t, filepath.Join(paths.RunRoot, "linuxcncsetup/irq-affinity-result.json"), `{
		"schema_version": 1,
		"generated_at": "2026-07-26T12:00:00Z",
		"status": "applied",
		"message": "policy applied",
		"policy": {"housekeeping_cpus": "0-3", "protected_cpus": "4-5"},
		"online_cpus": "0-5",
		"default_smp_affinity": "0000000f",
		"counts": {
			"applied": 0,
			"constrained": 1,
			"kernel_managed": 0,
			"unwritable": 0,
			"no_affinity_interface": 0,
			"disappeared": 0,
			"failed": 0
		},
		"irqs": [
			{"irq": 42, "status": "constrained", "requested": "0-3", "effective": "0-1", "detail": "hardware limit"}
		]
	}`)

	runner := func(_ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "is-active":
			return []byte("active\n"), nil
		case "is-enabled":
			return []byte("enabled\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %v", args)
		}
	}
	snapshot, err := ProbeIRQSnapshotWithOptions(IRQProbeOptions{
		Paths:         paths,
		CommandRunner: runner,
	})
	if err != nil {
		t.Fatalf("ProbeIRQSnapshotWithOptions() error: %v", err)
	}

	assertCPUsEqual(t, "online", snapshot.OnlineCPUs, []int{0, 1, 2, 3, 4, 5})
	assertCPUsEqual(t, "isolated", snapshot.IsolatedCPUs, []int{4, 5})
	assertCPUsEqual(t, "nohz_full", snapshot.NoHZFullCPUs, []int{4, 5})
	assertCPUsEqual(t, "kernel IRQ affinity", snapshot.KernelIRQAffinity, []int{0, 1, 2, 3})
	assertCPUsEqual(t, "default affinity", snapshot.DefaultAffinity, []int{0, 1, 2, 3})
	if len(snapshot.CPUs) != 6 {
		t.Fatalf("got %d CPU topology records; want 6", len(snapshot.CPUs))
	}
	if got, want := snapshot.CPUs[4].ThreadSiblings, []int{4, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CPU 4 siblings = %v; want %v", got, want)
	}

	if len(snapshot.IRQs) != 2 {
		t.Fatalf("got %d IRQ records; want 2", len(snapshot.IRQs))
	}
	if snapshot.IRQs[0].Number != 42 || snapshot.IRQs[0].Name != "enp3s0" {
		t.Fatalf("unexpected first IRQ record: %#v", snapshot.IRQs[0])
	}
	assertCPUsEqual(t, "IRQ requested affinity", snapshot.IRQs[0].RequestedCPUs, []int{0, 1, 2, 3})
	assertCPUsEqual(t, "IRQ effective affinity", snapshot.IRQs[0].EffectiveCPUs, []int{0, 1})

	if !snapshot.IRQBalance.Installed || !snapshot.IRQBalance.ActiveKnown || !snapshot.IRQBalance.Active ||
		!snapshot.IRQBalance.EnabledKnown || !snapshot.IRQBalance.Enabled {
		t.Fatalf("unexpected irqbalance status: %#v", snapshot.IRQBalance)
	}
	managed := snapshot.ManagedPolicy
	if !managed.ConfigPresent || !managed.HelperPresent || !managed.ServicePresent || !managed.ResultPresent {
		t.Fatalf("managed components were not detected: %#v", managed)
	}
	if managed.ConfigData == nil || FormatCPUList(managed.ConfigData.ProtectedCPUs) != "4-5" {
		t.Fatalf("unexpected managed config: %#v", managed.ConfigData)
	}
	if managed.ResultData == nil || managed.ResultData.Status != "applied" ||
		managed.ResultData.Counts.Constrained != 1 || managed.ResultData.Counts.Failed != 0 {
		t.Fatalf("unexpected managed result: %#v", managed.ResultData)
	}
	if len(snapshot.Problems) != 0 {
		t.Fatalf("unexpected probe problems: %v", snapshot.Problems)
	}
}

func TestProbeOnlineCPUsFallsBackToCPUDirectories(t *testing.T) {
	root := t.TempDir()
	paths := IRQProbePathsForRoot(root)
	for _, cpu := range []int{0, 2, 3} {
		directory := filepath.Join(paths.SysRoot, "devices/system/cpu", fmt.Sprintf("cpu%d", cpu))
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create CPU directory: %v", err)
		}
	}
	writeProbeFixture(t, filepath.Join(paths.SysRoot, "devices/system/cpu/cpu3/online"), "0\n")

	online, err := probeOnlineCPUs(paths)
	if err != nil {
		t.Fatalf("probeOnlineCPUs() error: %v", err)
	}
	assertCPUsEqual(t, "fallback online", online, []int{0, 2})
}

func TestManagedIRQParseProblemsDoNotFailSnapshot(t *testing.T) {
	root := t.TempDir()
	paths := IRQProbePathsForRoot(root)
	writeProbeFixture(t, filepath.Join(paths.SysRoot, "devices/system/cpu/online"), "0-1\n")
	writeProbeFixture(t, filepath.Join(paths.EtcRoot, "linuxcncsetup/irq-affinity.yml"), "{not json")

	snapshot, err := ProbeIRQSnapshotWithOptions(IRQProbeOptions{Paths: paths})
	if err != nil {
		t.Fatalf("ProbeIRQSnapshotWithOptions() error: %v", err)
	}
	if !snapshot.ManagedPolicy.ConfigPresent || snapshot.ManagedPolicy.ConfigData != nil {
		t.Fatalf("unexpected managed config state: %#v", snapshot.ManagedPolicy)
	}
	problems := strings.Join(snapshot.Problems, "\n")
	if !strings.Contains(problems, "parse managed IRQ config") {
		t.Fatalf("managed parse problem not reported: %v", snapshot.Problems)
	}
}

func TestManagedIRQConfigParsingIsStrict(t *testing.T) {
	for name, data := range map[string]string{
		"unknown field": `{
			"schema_version":1,
			"housekeeping_cpus":"0",
			"protected_cpus":"1",
			"extra":true
		}`,
		"trailing value": `{
			"schema_version":1,
			"housekeeping_cpus":"0",
			"protected_cpus":"1"
		} {}`,
		"wrong schema": `{
			"schema_version":2,
			"housekeeping_cpus":"0",
			"protected_cpus":"1"
		}`,
		"empty role": `{
			"schema_version":1,
			"housekeeping_cpus":"",
			"protected_cpus":"1"
		}`,
		"overlap": `{
			"schema_version":1,
			"housekeeping_cpus":"0-1",
			"protected_cpus":"1"
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseManagedIRQConfig([]byte(data)); err == nil {
				t.Fatal("malformed managed config was accepted")
			}
		})
	}
}

func TestManagedIRQResultCountsMustMatchEntries(t *testing.T) {
	data := []byte(`{
		"schema_version":1,
		"generated_at":"2026-07-26T12:00:00Z",
		"status":"applied",
		"message":"policy applied",
		"policy":{"housekeeping_cpus":"0","protected_cpus":"1"},
		"online_cpus":"0-1",
		"default_smp_affinity":"1",
		"counts":{
			"applied":0,
			"constrained":0,
			"kernel_managed":0,
			"unwritable":0,
			"no_affinity_interface":0,
			"disappeared":0,
			"failed":0
		},
		"irqs":[
			{"irq":42,"status":"applied","requested":"0","effective":"0","detail":"ok"}
		]
	}`)
	if _, err := parseManagedIRQResult(data); err == nil ||
		!strings.Contains(err.Error(), "do not match") {
		t.Fatalf("mismatched result counts error = %v", err)
	}
}

func TestManagedIRQResultV2ParsesDeviceOnlyBootResult(t *testing.T) {
	result, err := parseManagedIRQResult([]byte(validManagedIRQResultV2()))
	if err != nil {
		t.Fatalf("parseManagedIRQResult() error: %v", err)
	}
	if result.SchemaVersion != 2 ||
		result.Operation != "boot_apply" ||
		result.Status != "applied_with_warnings" {
		t.Fatalf("unexpected result header: %#v", result)
	}
	if result.Policy.DefaultPolicy != nil ||
		len(result.Policy.DeviceRules) != 2 {
		t.Fatalf("unexpected device-only policy: %#v", result.Policy)
	}
	assertCPUsEqual(t, "result online", result.OnlineCPUs, []int{0, 1, 2, 3})
	if result.DefaultSMPAffinity != "" ||
		result.Counts != (ManagedIRQResultCounts{}) ||
		len(result.IRQs) != 0 {
		t.Fatalf("device-only result contains broad policy data: %#v", result)
	}
	if result.DeviceRuleCounts != (ManagedIRQDeviceRuleCounts{
		Configured: 2,
		Matched:    1,
		NoMatch:    1,
		Applied:    1,
	}) {
		t.Fatalf("device rule counts = %#v", result.DeviceRuleCounts)
	}
	if len(result.DeviceRules) != 2 {
		t.Fatalf("resolved device rules = %d; want 2", len(result.DeviceRules))
	}
	nvme := result.DeviceRules[0]
	if nvme.Selector != (IRQDeviceSelector{
		Kind:  IRQDeviceSelectorPCIBDF,
		Value: "0000:0a:00.0",
	}) ||
		nvme.Status != "applied" ||
		!reflect.DeepEqual(nvme.MatchedIRQs, []int{39, 40}) ||
		!reflect.DeepEqual(nvme.RequestedCPUs, []int{2, 3}) ||
		nvme.Counts.Applied != 2 ||
		len(nvme.IRQs) != 2 {
		t.Fatalf("unexpected NVMe result: %#v", nvme)
	}
	if result.DeviceRules[1].Status != "no_match" ||
		len(result.DeviceRules[1].MatchedIRQs) != 0 {
		t.Fatalf("unexpected no-match result: %#v", result.DeviceRules[1])
	}
}

func TestManagedIRQResultV2AcceptsFailedPreflightReadyRecords(t *testing.T) {
	data := []byte(`{
		"schema_version":2,
		"operation":"boot_apply",
		"generated_at":"2026-07-26T12:00:00Z",
		"status":"failed",
		"message":"unsafe or ambiguous device selectors were left unchanged",
		"policy":{
			"default_policy":{"housekeeping_cpus":"0-1","protected_cpus":"2-3"},
			"device_rules":[
				{"selector":{"kind":"pci_bdf","value":"0000:0a:00.0"},"cpus":"2","label":"NVMe"},
				{"selector":{"kind":"action","value":"xhci_hcd"},"cpus":"0","label":"USB action"}
			]
		},
		"online_cpus":"0-3",
		"default_smp_affinity":"",
		"counts":{"applied":0,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
		"irqs":[],
		"device_rule_counts":{"configured":2,"matched":2,"no_match":0,"unsafe_selector":1,"ambiguous_selector":0,"applied":0,"partial":0,"failed":0},
		"device_rules":[
			{
				"selector":{"kind":"pci_bdf","value":"0000:0a:00.0"},
				"label":"NVMe",
				"requested":"2",
				"status":"ready",
				"detail":"",
				"matched_irqs":[39,40],
				"counts":{"applied":0,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
				"irqs":[]
			},
			{
				"selector":{"kind":"action","value":"xhci_hcd"},
				"label":"USB action",
				"requested":"0",
				"status":"unsafe_selector",
				"detail":"action selectors cannot target PCI-backed IRQs",
				"matched_irqs":[48,56],
				"counts":{"applied":0,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
				"irqs":[]
			}
		]
	}`)
	result, err := parseManagedIRQResult(data)
	if err != nil {
		t.Fatalf("failed preflight result was rejected: %v", err)
	}
	if result.Policy.DefaultPolicy == nil ||
		len(result.DeviceRules) != 2 ||
		result.DeviceRules[0].Status != "ready" ||
		result.DeviceRules[1].Status != "unsafe_selector" {
		t.Fatalf("unexpected failed preflight result: %#v", result)
	}
}

func TestManagedIRQResultV2AcceptsFailureBeforePolicyLoad(t *testing.T) {
	data := []byte(`{
		"schema_version":2,
		"operation":"boot_apply",
		"generated_at":"2026-07-26T12:00:00Z",
		"status":"failed",
		"message":"cannot inspect policy",
		"policy":{"default_policy":null,"device_rules":[]},
		"online_cpus":"",
		"default_smp_affinity":"",
		"counts":{"applied":0,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
		"irqs":[],
		"device_rule_counts":{"configured":0,"matched":0,"no_match":0,"unsafe_selector":0,"ambiguous_selector":0,"applied":0,"partial":0,"failed":0},
		"device_rules":[]
	}`)
	result, err := parseManagedIRQResult(data)
	if err != nil {
		t.Fatalf("early failure result was rejected: %v", err)
	}
	if result.Policy.DefaultPolicy != nil ||
		len(result.Policy.DeviceRules) != 0 ||
		len(result.OnlineCPUs) != 0 {
		t.Fatalf("unexpected early failure result: %#v", result)
	}
}

func TestManagedIRQResultV2ParsesSuccessfulStandaloneLiveApply(t *testing.T) {
	data := []byte(`{
		"schema_version":2,
		"operation":"apply_device_live",
		"generated_at":"2026-07-26T12:00:00Z",
		"status":"applied",
		"message":"selected device rule was applied live",
		"policy":{
			"default_policy":null,
			"device_rules":[
				{"selector":{"kind":"pci_bdf","value":"0000:06:00.0"},"cpus":"2-3","label":"Ethernet"}
			]
		},
		"online_cpus":"0-3",
		"default_smp_affinity":"",
		"counts":{"applied":0,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
		"irqs":[],
		"device_rule_counts":{"configured":1,"matched":1,"no_match":0,"unsafe_selector":0,"ambiguous_selector":0,"applied":1,"partial":0,"failed":0},
		"device_rules":[
			{
				"selector":{"kind":"pci_bdf","value":"0000:06:00.0"},
				"label":"Ethernet",
				"requested":"2-3",
				"status":"applied",
				"detail":"all matching IRQs have the requested affinity",
				"matched_irqs":[47],
				"counts":{"applied":1,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
				"irqs":[
					{"irq":47,"status":"applied","requested":"2-3","effective":"2-3","detail":"requested and effective affinities match"}
				]
			}
		]
	}`)
	result, err := parseManagedIRQResult(data)
	if err != nil {
		t.Fatalf("successful live result was rejected: %v", err)
	}
	if result.Operation != "apply_device_live" ||
		result.Status != "applied" ||
		len(result.DeviceRules) != 1 ||
		result.DeviceRules[0].MatchedIRQs[0] != 47 {
		t.Fatalf("unexpected live result: %#v", result)
	}
}

func TestManagedIRQResultV2RejectsInconsistentDeviceData(t *testing.T) {
	valid := validManagedIRQResultV2()
	for name, data := range map[string]string{
		"summary count": strings.Replace(
			valid,
			`"configured":2`,
			`"configured":1`,
			1,
		),
		"invalid selector": strings.ReplaceAll(
			valid,
			"0000:0a:00.0",
			"not-a-bdf",
		),
		"invalid requested CPU list": strings.Replace(
			valid,
			`"requested":"2-3"`,
			`"requested":"3-2"`,
			1,
		),
		"warning status mismatch": strings.Replace(
			valid,
			`"status":"applied_with_warnings"`,
			`"status":"applied"`,
			1,
		),
		"unknown top-level field": strings.Replace(
			valid,
			`"schema_version":2,`,
			`"schema_version":2,"unexpected":true,`,
			1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseManagedIRQResult([]byte(data)); err == nil {
				t.Fatal("inconsistent v2 result was accepted")
			}
		})
	}
}

func validManagedIRQResultV2() string {
	return `{
		"schema_version":2,
		"operation":"boot_apply",
		"generated_at":"2026-07-26T12:00:00Z",
		"status":"applied_with_warnings",
		"message":"IRQ policy applied",
		"policy":{
			"default_policy":null,
			"device_rules":[
				{"selector":{"kind":"pci_bdf","value":"0000:0a:00.0"},"cpus":"2-3","label":"NVMe"},
				{"selector":{"kind":"action","value":"rtc0"},"cpus":"0","label":"RTC"}
			]
		},
		"online_cpus":"0-3",
		"default_smp_affinity":"",
		"counts":{"applied":0,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
		"irqs":[],
		"device_rule_counts":{"configured":2,"matched":1,"no_match":1,"unsafe_selector":0,"ambiguous_selector":0,"applied":1,"partial":0,"failed":0},
		"device_rules":[
			{
				"selector":{"kind":"pci_bdf","value":"0000:0a:00.0"},
				"label":"NVMe",
				"requested":"2-3",
				"status":"applied",
				"detail":"all matching IRQs have the requested affinity",
				"matched_irqs":[39,40],
				"counts":{"applied":2,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
				"irqs":[
					{"irq":39,"status":"applied","requested":"2-3","effective":"2-3","detail":"requested and effective affinities match"},
					{"irq":40,"status":"applied","requested":"2-3","effective":"2-3","detail":"requested and effective affinities match"}
				]
			},
			{
				"selector":{"kind":"action","value":"rtc0"},
				"label":"RTC",
				"requested":"0",
				"status":"no_match",
				"detail":"no current IRQ matches the stable selector",
				"matched_irqs":[],
				"counts":{"applied":0,"constrained":0,"kernel_managed":0,"unwritable":0,"no_affinity_interface":0,"disappeared":0,"failed":0},
				"irqs":[]
			}
		]
	}`
}

func TestManagedIRQResultMatchesConfigUsesDefaultAndStableDeviceRules(t *testing.T) {
	config := &ManagedIRQConfig{
		SchemaVersion: 2,
		DeviceRules: []ManagedIRQDeviceRule{
			{
				Selector: IRQDeviceSelector{
					Kind:  IRQDeviceSelectorPCIBDF,
					Value: "0000:0a:00.0",
				},
				CPUs:  []int{2, 3},
				Label: "Current NVMe label",
			},
			{
				Selector: IRQDeviceSelector{
					Kind:  IRQDeviceSelectorAction,
					Value: "rtc0",
				},
				CPUs:  []int{0},
				Label: "RTC",
			},
		},
	}
	result := &ManagedIRQResult{
		SchemaVersion: 2,
		Policy: ManagedIRQResultPolicy{
			DeviceRules: []ManagedIRQDeviceRule{
				{
					Selector: IRQDeviceSelector{
						Kind:  IRQDeviceSelectorAction,
						Value: "rtc0",
					},
					CPUs:  []int{0},
					Label: "Old display label",
				},
				{
					Selector: IRQDeviceSelector{
						Kind:  IRQDeviceSelectorPCIBDF,
						Value: "0000:0a:00.0",
					},
					CPUs:  []int{3, 2},
					Label: "Old NVMe label",
				},
			},
		},
	}
	if !managedIRQResultMatchesConfig(result, config) {
		t.Fatal("equivalent reordered stable device rules did not match")
	}

	result.Policy.DeviceRules[1].CPUs = []int{1}
	if managedIRQResultMatchesConfig(result, config) {
		t.Fatal("device rule with different CPUs matched")
	}
	result.Policy.DeviceRules[1].CPUs = []int{2, 3}
	result.Policy.DefaultPolicy = &ManagedIRQDefaultPolicy{
		HousekeepingCPUs: []int{0, 1},
		ProtectedCPUs:    []int{2, 3},
	}
	if managedIRQResultMatchesConfig(result, config) {
		t.Fatal("result with an unexpected default policy matched")
	}
}

func TestManagedIRQResultMatchesConfigRetainsV1Compatibility(t *testing.T) {
	config := &ManagedIRQConfig{
		SchemaVersion:    1,
		HousekeepingCPUs: []int{0, 1},
		ProtectedCPUs:    []int{2, 3},
	}
	result := &ManagedIRQResult{
		SchemaVersion: 1,
		Policy: ManagedIRQResultPolicy{
			HousekeepingCPUs: []int{1, 0},
			ProtectedCPUs:    []int{3, 2},
		},
	}
	if !managedIRQResultMatchesConfig(result, config) {
		t.Fatal("equivalent legacy CPU policies did not match")
	}
	result.Policy.ProtectedCPUs = []int{2}
	if managedIRQResultMatchesConfig(result, config) {
		t.Fatal("different legacy CPU policies matched")
	}
}

func TestManagedIRQProbeDoesNotClaimVendorUnitOwnership(t *testing.T) {
	root := t.TempDir()
	paths := IRQProbePathsForRoot(root)
	vendorUnit := filepath.Join(
		paths.USRRoot,
		"lib/systemd/system",
		managedIRQService,
	)
	writeProbeFixture(t, vendorUnit, "[Service]\n")

	var problems []string
	status := probeManagedIRQPolicy(paths, nil, []int{0}, &problems)
	if status.Service.Present {
		t.Fatal("vendor unit was reported as a linuxcncsetup-owned service")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "unmanaged") {
		t.Fatalf("vendor-unit warning not reported: %v", problems)
	}
}

func writeProbeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func assertCPUsEqual(t *testing.T, label string, got, want []int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s CPUs = %v; want %v", label, got, want)
	}
}
