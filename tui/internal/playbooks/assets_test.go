package playbooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMaterialize(t *testing.T) {
	for _, playbook := range []Playbook{
		Autologin,
		InstallLinuxCNCConfig,
		IRQAffinity,
		InstallDevTools,
		InstallSway,
		LinuxCNCAutostart,
	} {
		t.Run(string(playbook), func(t *testing.T) {
			playbookPath, cleanup, err := Materialize(playbook)
			if err != nil {
				t.Fatalf("Materialize() error: %v", err)
			}
			directory := filepath.Dir(playbookPath)
			t.Cleanup(cleanup)

			for _, path := range []string{
				playbookPath,
				filepath.Join(directory, "tasks", "devtools_git.yml"),
				filepath.Join(directory, "tasks", "devtools_linger.yml"),
				filepath.Join(directory, "tasks", "devtools_vscode.yml"),
				filepath.Join(directory, "tasks", "lightdm.yml"),
				filepath.Join(directory, "tasks", "sway.yml"),
				filepath.Join(directory, "tasks", "install_sway.yml"),
				filepath.Join(directory, "tasks", "irq_affinity_absent.yml"),
				filepath.Join(directory, "tasks", "irq_affinity_present.yml"),
				filepath.Join(directory, "tasks", "linuxcnc_autostart.yml"),
				filepath.Join(directory, "templates", "irq-affinity-policy.yml.j2"),
				filepath.Join(directory, "templates", "linuxcncsetup-irq-affinity.py.j2"),
				filepath.Join(directory, "templates", "linuxcncsetup-irq-affinity.service.j2"),
				filepath.Join(directory, "templates", "linuxcnc-autostart.sh.j2"),
				filepath.Join(directory, "templates", "linuxcnc-autostart-sway.conf.j2"),
				filepath.Join(directory, "templates", "sway-config.j2"),
			} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("materialized asset %q: %v", path, err)
				}
			}

			cleanup()
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("cleanup left playbook directory behind: %v", err)
			}
		})
	}
}

func TestMaterializeRejectsUnknownPlaybook(t *testing.T) {
	if _, _, err := Materialize(Playbook("../outside.yml")); err == nil {
		t.Fatal("Materialize() accepted an unknown playbook")
	}
}

func TestLinuxCNCConfigPlaybookPreservesExistingCheckout(t *testing.T) {
	playbookPath, cleanup, err := Materialize(InstallLinuxCNCConfig)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	content, err := os.ReadFile(playbookPath)
	if err != nil {
		t.Fatalf("read materialized playbook: %v", err)
	}
	playbook := string(content)
	for _, expected := range []string{
		"git@github.com:ymiroshnychenko668/corvuscnc.git",
		"{{ corvus_target_home }}/linuxcnc/configs/corvuscnc",
		`become_user: "{{ target_user }}"`,
		`"SSH_AUTH_SOCK": corvus_ssh_auth_sock | default("")`,
		"accept_newhostkey: true",
		"update: false",
		"force: false",
		"corvus_missing_packages | length > 0",
		"when: not corvus_destination_before.stat.exists",
		"recurse: false",
		"corvus_ini_files.matched | int > 0",
		"not ansible_check_mode or corvus_destination_before.stat.exists",
	} {
		if !strings.Contains(playbook, expected) {
			t.Errorf("CorvusCNC playbook does not contain %q", expected)
		}
	}
}

func TestIRQHelperRejectsUnassignedOnlineCPU(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	playbookPath, cleanup, err := Materialize(IRQAffinity)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	helperPath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcncsetup-irq-affinity.py.j2",
	)

	fixtureRoot := t.TempDir()
	onlinePath := filepath.Join(fixtureRoot, "online")
	policyPath := filepath.Join(fixtureRoot, "policy.json")
	if err := os.WriteFile(onlinePath, []byte("0-3\n"), 0o644); err != nil {
		t.Fatalf("write online CPU fixture: %v", err)
	}
	if err := os.WriteFile(
		policyPath,
		[]byte(`{"schema_version":1,"housekeeping_cpus":"0-1","protected_cpus":"3"}`),
		0o644,
	); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}

	script := strings.Join([]string{
		"import importlib.machinery, importlib.util, pathlib, sys",
		"loader = importlib.machinery.SourceFileLoader('irq_helper', sys.argv[1])",
		"spec = importlib.util.spec_from_loader(loader.name, loader)",
		"module = importlib.util.module_from_spec(spec)",
		"loader.exec_module(module)",
		"module.require_secure_regular_file = lambda path: None",
		"module.ONLINE_CPUS = pathlib.Path(sys.argv[2])",
		"try:",
		"    module.load_policy(pathlib.Path(sys.argv[3]))",
		"except module.PolicyError as error:",
		"    assert 'no policy role' in str(error), str(error)",
		"else:",
		"    raise AssertionError('unassigned online CPU was accepted')",
	}, "\n")
	command := exec.Command(python, "-B", "-c", script, helperPath, onlinePath, policyPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper policy validation failed: %v\n%s", err, output)
	}
}

func TestIRQHelperDoesNotClaimUnverifiableAffinityWasApplied(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	playbookPath, cleanup, err := Materialize(IRQAffinity)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	helperPath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcncsetup-irq-affinity.py.j2",
	)

	irqRoot := filepath.Join(t.TempDir(), "irq")
	irqDirectory := filepath.Join(irqRoot, "42")
	if err := os.MkdirAll(irqDirectory, 0o755); err != nil {
		t.Fatalf("create IRQ fixture: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(irqDirectory, "smp_affinity_list"),
		[]byte("0-1\n"),
		0o644,
	); err != nil {
		t.Fatalf("write affinity fixture: %v", err)
	}

	script := strings.Join([]string{
		"import importlib.machinery, importlib.util, pathlib, sys",
		"loader = importlib.machinery.SourceFileLoader('irq_helper', sys.argv[1])",
		"spec = importlib.util.spec_from_loader(loader.name, loader)",
		"module = importlib.util.module_from_spec(spec)",
		"loader.exec_module(module)",
		"module.IRQ_ROOT = pathlib.Path(sys.argv[2])",
		"result = module.classify_irq(42, '0')",
		"assert result['status'] == 'constrained', result",
		"assert 'could not be verified' in result['detail'], result",
	}, "\n")
	command := exec.Command(python, "-B", "-c", script, helperPath, irqRoot)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper affinity classification failed: %v\n%s", err, output)
	}
}

func TestIRQHelperRejectsSplitSMTSiblingsAndUnknownIRQBalanceState(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	playbookPath, cleanup, err := Materialize(IRQAffinity)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	helperPath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcncsetup-irq-affinity.py.j2",
	)

	fixtureRoot := t.TempDir()
	onlinePath := filepath.Join(fixtureRoot, "online")
	cpuRoot := filepath.Join(fixtureRoot, "cpus")
	policyPath := filepath.Join(fixtureRoot, "policy.json")
	if err := os.WriteFile(onlinePath, []byte("0-3\n"), 0o644); err != nil {
		t.Fatalf("write online CPU fixture: %v", err)
	}
	if err := os.WriteFile(
		policyPath,
		[]byte(`{"schema_version":1,"housekeeping_cpus":"0-1","protected_cpus":"2-3"}`),
		0o644,
	); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
	for cpu, siblings := range map[string]string{
		"0": "0,2\n",
		"1": "1,3\n",
		"2": "0,2\n",
		"3": "1,3\n",
	} {
		topology := filepath.Join(cpuRoot, "cpu"+cpu, "topology")
		if err := os.MkdirAll(topology, 0o755); err != nil {
			t.Fatalf("create topology fixture: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(topology, "thread_siblings_list"),
			[]byte(siblings),
			0o644,
		); err != nil {
			t.Fatalf("write topology fixture: %v", err)
		}
	}

	script := strings.Join([]string{
		"import importlib.machinery, importlib.util, pathlib, sys",
		"loader = importlib.machinery.SourceFileLoader('irq_helper', sys.argv[1])",
		"spec = importlib.util.spec_from_loader(loader.name, loader)",
		"module = importlib.util.module_from_spec(spec)",
		"loader.exec_module(module)",
		"module.require_secure_regular_file = lambda path: None",
		"module.ONLINE_CPUS = pathlib.Path(sys.argv[2])",
		"module.CPU_ROOT = pathlib.Path(sys.argv[3])",
		"try:",
		"    module.load_policy(pathlib.Path(sys.argv[4]))",
		"except module.PolicyError as error:",
		"    assert 'SMT sibling group' in str(error), str(error)",
		"else:",
		"    raise AssertionError('split SMT sibling group was accepted')",
		"module.systemctl_state = lambda operation: (1, 'query failed')",
		"module.process_ids_named = lambda name: []",
		"try:",
		"    module.require_no_irqbalance()",
		"except module.PolicyError as error:",
		"    assert 'repair the systemctl query' in str(error), str(error)",
		"else:",
		"    raise AssertionError('unknown irqbalance state was accepted')",
	}, "\n")
	command := exec.Command(
		python,
		"-B",
		"-c",
		script,
		helperPath,
		onlinePath,
		cpuRoot,
		policyPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper safety validation failed: %v\n%s", err, output)
	}
}

func TestIRQHelperResolvesPCIDeviceVectorsAndRejectsPCIActionSelector(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	playbookPath, cleanup, err := Materialize(IRQAffinity)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	helperPath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcncsetup-irq-affinity.py.j2",
	)

	fixtureRoot := t.TempDir()
	procIRQRoot := filepath.Join(fixtureRoot, "proc-irq")
	sysIRQRoot := filepath.Join(fixtureRoot, "sys-irq")
	for irq, identity := range map[string][2]string{
		"39": {"nvme0q0\n", "IR-PCI-MSIX-0000:0a:00.0\n"},
		"40": {"nvme0q1\n", "IR-PCI-MSIX-0000:0a:00.0\n"},
		"48": {"xhci_hcd\n", "IR-PCI-MSIX-0000:08:00.0\n"},
		"56": {"xhci_hcd\n", "IR-PCI-MSIX-0000:0b:00.3\n"},
		"8":  {"rtc0\n", "IO-APIC\n"},
	} {
		if err := os.MkdirAll(filepath.Join(procIRQRoot, irq), 0o755); err != nil {
			t.Fatalf("create proc IRQ fixture: %v", err)
		}
		sysIRQDirectory := filepath.Join(sysIRQRoot, irq)
		if err := os.MkdirAll(sysIRQDirectory, 0o755); err != nil {
			t.Fatalf("create sys IRQ fixture: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(sysIRQDirectory, "actions"),
			[]byte(identity[0]),
			0o644,
		); err != nil {
			t.Fatalf("write actions fixture: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(sysIRQDirectory, "chip_name"),
			[]byte(identity[1]),
			0o644,
		); err != nil {
			t.Fatalf("write chip_name fixture: %v", err)
		}
	}

	script := strings.Join([]string{
		"import importlib.machinery, importlib.util, pathlib, sys",
		"loader = importlib.machinery.SourceFileLoader('irq_helper', sys.argv[1])",
		"spec = importlib.util.spec_from_loader(loader.name, loader)",
		"module = importlib.util.module_from_spec(spec)",
		"loader.exec_module(module)",
		"module.IRQ_ROOT = pathlib.Path(sys.argv[2])",
		"module.SYS_IRQ_ROOT = pathlib.Path(sys.argv[3])",
		"online = {0, 1, 2, 3}",
		"raw_rules = [",
		"  {'selector': {'kind': 'pci_bdf', 'value': '0000:0a:00.0'}, 'cpus': '2', 'label': 'NVMe'},",
		"  {'selector': {'kind': 'pci_bdf', 'value': '0000:08:00.0'}, 'cpus': '1', 'label': 'USB A'},",
		"  {'selector': {'kind': 'action', 'value': 'xhci_hcd'}, 'cpus': '0', 'label': 'unsafe USB action'},",
		"  {'selector': {'kind': 'action', 'value': 'rtc0'}, 'cpus': '0', 'label': 'RTC'},",
		"]",
		"rules = [module.normalize_device_rule(rule, i, online) for i, rule in enumerate(raw_rules)]",
		"plans = module.resolve_device_rules(rules)",
		"assert plans[0]['status'] == 'ready', plans[0]",
		"assert plans[0]['apply_irqs'] == [39, 40], plans[0]",
		"assert plans[1]['status'] == 'ready', plans[1]",
		"assert plans[1]['apply_irqs'] == [48], plans[1]",
		"assert plans[2]['status'] == 'unsafe_selector', plans[2]",
		"assert plans[2]['apply_irqs'] == [], plans[2]",
		"assert plans[3]['status'] == 'ready', plans[3]",
		"assert plans[3]['apply_irqs'] == [8], plans[3]",
	}, "\n")
	command := exec.Command(
		python,
		"-B",
		"-c",
		script,
		helperPath,
		procIRQRoot,
		sysIRQRoot,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper device resolution failed: %v\n%s", err, output)
	}
}

func TestIRQHelperDeviceOnlyPolicyDoesNotTouchBroadAffinity(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	playbookPath, cleanup, err := Materialize(IRQAffinity)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	helperPath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcncsetup-irq-affinity.py.j2",
	)

	fixtureRoot := t.TempDir()
	onlinePath := filepath.Join(fixtureRoot, "online")
	procIRQRoot := filepath.Join(fixtureRoot, "proc-irq")
	sysIRQRoot := filepath.Join(fixtureRoot, "sys-irq")
	policyPath := filepath.Join(fixtureRoot, "policy.json")
	defaultAffinityPath := filepath.Join(fixtureRoot, "default_smp_affinity")
	if err := os.WriteFile(onlinePath, []byte("0-3\n"), 0o644); err != nil {
		t.Fatalf("write online CPU fixture: %v", err)
	}
	if err := os.WriteFile(defaultAffinityPath, []byte("f\n"), 0o644); err != nil {
		t.Fatalf("write default affinity fixture: %v", err)
	}
	if err := os.WriteFile(
		policyPath,
		[]byte(`{
			"schema_version": 2,
			"default_policy": null,
			"device_rules": [{
				"selector": {"kind": "pci_bdf", "value": "0000:0a:00.0"},
				"cpus": "2",
				"label": "NVMe"
			}]
		}`),
		0o644,
	); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
	for _, irq := range []string{"39", "40"} {
		procIRQDirectory := filepath.Join(procIRQRoot, irq)
		if err := os.MkdirAll(procIRQDirectory, 0o755); err != nil {
			t.Fatalf("create proc IRQ fixture: %v", err)
		}
		for name, value := range map[string]string{
			"smp_affinity_list":       "0-3\n",
			"effective_affinity_list": "2\n",
		} {
			if err := os.WriteFile(
				filepath.Join(procIRQDirectory, name),
				[]byte(value),
				0o644,
			); err != nil {
				t.Fatalf("write affinity fixture: %v", err)
			}
		}
		sysIRQDirectory := filepath.Join(sysIRQRoot, irq)
		if err := os.MkdirAll(sysIRQDirectory, 0o755); err != nil {
			t.Fatalf("create sys IRQ fixture: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(sysIRQDirectory, "actions"),
			[]byte("nvme0q"+irq+"\n"),
			0o644,
		); err != nil {
			t.Fatalf("write actions fixture: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(sysIRQDirectory, "chip_name"),
			[]byte("IR-PCI-MSIX-0000:0a:00.0\n"),
			0o644,
		); err != nil {
			t.Fatalf("write chip fixture: %v", err)
		}
	}

	script := strings.Join([]string{
		"import importlib.machinery, importlib.util, pathlib, sys",
		"loader = importlib.machinery.SourceFileLoader('irq_helper', sys.argv[1])",
		"spec = importlib.util.spec_from_loader(loader.name, loader)",
		"module = importlib.util.module_from_spec(spec)",
		"loader.exec_module(module)",
		"module.require_secure_regular_file = lambda path: None",
		"module.require_no_linuxcnc = lambda: None",
		"module.require_no_irqbalance = lambda: None",
		"module.ONLINE_CPUS = pathlib.Path(sys.argv[2])",
		"module.IRQ_ROOT = pathlib.Path(sys.argv[3])",
		"module.SYS_IRQ_ROOT = pathlib.Path(sys.argv[4])",
		"module.DEFAULT_AFFINITY = pathlib.Path(sys.argv[5])",
		"captured = []",
		"module.atomic_write_result = lambda path, result: captured.append(result)",
		"rc = module.apply_policy(pathlib.Path(sys.argv[6]), pathlib.Path('/unused'))",
		"assert rc == 0, rc",
		"assert captured and captured[0]['status'] == 'applied', captured",
		"result = captured[0]",
		"assert result['default_smp_affinity'] == '', result",
		"assert result['irqs'] == [], result",
		"assert result['device_rules'][0]['status'] == 'applied', result",
		"assert result['device_rules'][0]['matched_irqs'] == [39, 40], result",
		"assert pathlib.Path(sys.argv[5]).read_text() == 'f\\n'",
	}, "\n")
	command := exec.Command(
		python,
		"-B",
		"-c",
		script,
		helperPath,
		onlinePath,
		procIRQRoot,
		sysIRQRoot,
		defaultAffinityPath,
		policyPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper device-only apply failed: %v\n%s", err, output)
	}
}

func TestIRQHelperLiveApplyStopsBeforeWriteWhenRTIsRunning(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	playbookPath, cleanup, err := Materialize(IRQAffinity)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	helperPath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcncsetup-irq-affinity.py.j2",
	)
	onlinePath := filepath.Join(t.TempDir(), "online")
	if err := os.WriteFile(onlinePath, []byte("0-3\n"), 0o644); err != nil {
		t.Fatalf("write online CPU fixture: %v", err)
	}

	script := strings.Join([]string{
		"import importlib.machinery, importlib.util, pathlib, sys",
		"loader = importlib.machinery.SourceFileLoader('irq_helper', sys.argv[1])",
		"spec = importlib.util.spec_from_loader(loader.name, loader)",
		"module = importlib.util.module_from_spec(spec)",
		"loader.exec_module(module)",
		"module.ONLINE_CPUS = pathlib.Path(sys.argv[2])",
		"module.require_no_linuxcnc = lambda: (_ for _ in ()).throw(module.PolicyError('RT is running'))",
		"module.require_no_irqbalance = lambda: (_ for _ in ()).throw(AssertionError('irqbalance check reached'))",
		"module.write_virtual_file = lambda path, value: (_ for _ in ()).throw(AssertionError('write reached'))",
		"captured = []",
		"module.atomic_write_result = lambda path, result: captured.append(result)",
		"rule = {'selector': {'kind': 'pci_bdf', 'value': '0000:0a:00.0'}, 'cpus': '2', 'label': 'NVMe'}",
		"rc = module.apply_device_live(rule, pathlib.Path('/unused'))",
		"assert rc == 1, rc",
		"assert captured[0]['status'] == 'failed', captured",
		"assert 'RT is running' in captured[0]['message'], captured",
	}, "\n")
	command := exec.Command(
		python,
		"-B",
		"-c",
		script,
		helperPath,
		onlinePath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper live safety check failed: %v\n%s", err, output)
	}
}

func TestIRQHelperLiveApplyPreflightsEveryDeviceVectorBeforeWriting(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	playbookPath, cleanup, err := Materialize(IRQAffinity)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	helperPath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"linuxcncsetup-irq-affinity.py.j2",
	)

	fixtureRoot := t.TempDir()
	onlinePath := filepath.Join(fixtureRoot, "online")
	procIRQRoot := filepath.Join(fixtureRoot, "proc-irq")
	sysIRQRoot := filepath.Join(fixtureRoot, "sys-irq")
	if err := os.WriteFile(onlinePath, []byte("0-3\n"), 0o644); err != nil {
		t.Fatalf("write online CPU fixture: %v", err)
	}
	for irq := 39; irq <= 45; irq++ {
		procIRQDirectory := filepath.Join(procIRQRoot, strconv.Itoa(irq))
		if err := os.MkdirAll(procIRQDirectory, 0o755); err != nil {
			t.Fatalf("create proc IRQ fixture: %v", err)
		}
		affinityPath := filepath.Join(procIRQDirectory, "smp_affinity_list")
		if err := os.WriteFile(affinityPath, []byte("0-3\n"), 0o644); err != nil {
			t.Fatalf("write affinity fixture: %v", err)
		}
		if irq != 39 {
			if err := os.Chmod(affinityPath, 0o444); err != nil {
				t.Fatalf("make IRQ %d affinity read-only: %v", irq, err)
			}
		}

		sysIRQDirectory := filepath.Join(sysIRQRoot, strconv.Itoa(irq))
		if err := os.MkdirAll(sysIRQDirectory, 0o755); err != nil {
			t.Fatalf("create sys IRQ fixture: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(sysIRQDirectory, "actions"),
			[]byte("nvme0q"+strconv.Itoa(irq-39)+"\n"),
			0o644,
		); err != nil {
			t.Fatalf("write actions fixture: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(sysIRQDirectory, "chip_name"),
			[]byte("IR-PCI-MSIX-0000:0a:00.0\n"),
			0o644,
		); err != nil {
			t.Fatalf("write chip_name fixture: %v", err)
		}
	}

	script := strings.Join([]string{
		"import importlib.machinery, importlib.util, pathlib, sys",
		"loader = importlib.machinery.SourceFileLoader('irq_helper', sys.argv[1])",
		"spec = importlib.util.spec_from_loader(loader.name, loader)",
		"module = importlib.util.module_from_spec(spec)",
		"loader.exec_module(module)",
		"module.ONLINE_CPUS = pathlib.Path(sys.argv[2])",
		"module.IRQ_ROOT = pathlib.Path(sys.argv[3])",
		"module.SYS_IRQ_ROOT = pathlib.Path(sys.argv[4])",
		"module.require_no_linuxcnc = lambda: None",
		"module.require_no_irqbalance = lambda: None",
		"writes = []",
		"module.write_virtual_file = lambda path, value: writes.append((str(path), value))",
		"captured = []",
		"module.atomic_write_result = lambda path, result: captured.append(result)",
		"rule = {'selector': {'kind': 'pci_bdf', 'value': '0000:0a:00.0'}, 'cpus': '2', 'label': 'NVMe'}",
		"rc = module.apply_device_live(rule, pathlib.Path('/unused'))",
		"assert rc == 1, rc",
		"assert writes == [], writes",
		"assert captured and captured[0]['status'] == 'failed', captured",
		"result = captured[0]",
		"assert result['device_rules'][0]['status'] == 'ready', result",
		"assert result['device_rules'][0]['matched_irqs'] == list(range(39, 46)), result",
		"for irq in range(40, 46):",
		"    assert f'IRQ {irq}' in result['message'], result['message']",
		"assert 'IRQ 39' not in result['message'], result['message']",
		"assert 'no IRQ affinity was changed' in result['message'], result['message']",
	}, "\n")
	command := exec.Command(
		python,
		"-B",
		"-c",
		script,
		helperPath,
		onlinePath,
		procIRQRoot,
		sysIRQRoot,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper all-vector preflight failed: %v\n%s", err, output)
	}
	for irq := 39; irq <= 45; irq++ {
		data, err := os.ReadFile(
			filepath.Join(procIRQRoot, strconv.Itoa(irq), "smp_affinity_list"),
		)
		if err != nil {
			t.Fatalf("read IRQ %d affinity after preflight: %v", irq, err)
		}
		if string(data) != "0-3\n" {
			t.Fatalf("IRQ %d affinity changed to %q", irq, data)
		}
	}
}
