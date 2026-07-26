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
				filepath.Join(directory, "tasks", "devtools_claude.yml"),
				filepath.Join(directory, "tasks", "devtools_codex.yml"),
				filepath.Join(directory, "tasks", "devtools_git.yml"),
				filepath.Join(directory, "tasks", "devtools_linger.yml"),
				filepath.Join(directory, "tasks", "devtools_vscode.yml"),
				filepath.Join(directory, "tasks", "devtools_warp.yml"),
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

func TestDeveloperToolsPlaybookIncludesIndependentAgentComponents(t *testing.T) {
	playbook, _ := materializedDeveloperToolsAsset(
		t,
		"tasks/devtools_codex.yml",
	)

	for _, expected := range []string{
		"\n      - codex\n",
		"\n      - claude\n",
		"\n      - warp\n",
		"tasks/devtools_codex.yml",
		"tasks/devtools_claude.yml",
		"tasks/devtools_warp.yml",
	} {
		if !strings.Contains(playbook, expected) {
			t.Errorf("developer-tools playbook does not contain %q", expected)
		}
	}
}

func TestDeveloperToolsSharedAPTOnlyInstallsMissingPackages(t *testing.T) {
	playbook, _ := materializedDeveloperToolsAsset(
		t,
		"tasks/devtools_codex.yml",
	)
	packageBlock := assetBlockBetween(
		t,
		playbook,
		"- name: Read installed developer package facts",
		"- name: Configure Git and GitHub SSH",
	)

	for _, expected := range []string{
		"ansible.builtin.package_facts:",
		"manager: auto",
		"devtools_missing_packages: >-",
		"difference(ansible_facts.packages.keys() | list)",
		`name: "{{ devtools_missing_packages }}"`,
		"- devtools_missing_packages | length > 0",
		"cache_valid_time: 3600",
		"lock_timeout: 120",
	} {
		if !strings.Contains(packageBlock, expected) {
			t.Errorf("shared developer-package block does not contain %q", expected)
		}
	}
	if strings.Contains(
		packageBlock,
		`name: "{{ devtools_packages_by_component[devtools_component] }}"`,
	) {
		t.Fatal("shared APT task still installs the unfiltered component package list")
	}
}

func TestAgentComponentsDeferInstallerPackagesUntilInstallationIsNeeded(t *testing.T) {
	playbook, _ := materializedDeveloperToolsAsset(
		t,
		"tasks/devtools_codex.yml",
	)
	for _, expected := range []string{
		"      codex: []",
		"      claude: []",
		"      warp: []",
	} {
		if !strings.Contains(playbook, expected) {
			t.Errorf(
				"developer-tools playbook does not defer installer packages with %q",
				expected,
			)
		}
	}

	tests := []struct {
		name        string
		task        string
		start       string
		end         string
		missingFact string
		absentState string
	}{
		{
			name:        "Codex",
			task:        "tasks/devtools_codex.yml",
			start:       "- name: Read packages required by the Codex installer",
			end:         "- name: Check for a Codex installer downloader",
			missingFact: "devtools_codex_missing_packages",
			absentState: "not devtools_codex_command_entry.stat.exists",
		},
		{
			name:        "Claude Code",
			task:        "tasks/devtools_claude.yml",
			start:       "- name: Read packages required by the Claude Code installer",
			end:         "- name: Check for a Claude Code installer downloader",
			missingFact: "devtools_claude_missing_packages",
			absentState: "not devtools_claude_command_entry.stat.exists",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, task := materializedDeveloperToolsAsset(t, test.task)
			block := assetBlockBetween(t, task, test.start, test.end)
			for _, expected := range []string{
				"ansible.builtin.package_facts:",
				"ansible.builtin.apt:",
				test.missingFact,
				test.absentState,
				"| length > 0",
			} {
				if !strings.Contains(block, expected) {
					t.Errorf(
						"%s installer-prerequisite block does not contain %q",
						test.name,
						expected,
					)
				}
			}
		})
	}
}

func TestOfficialAgentInstallersArePerUserAndNotPipedToShell(t *testing.T) {
	tests := []struct {
		name        string
		task        string
		officialURL string
		command     string
		extra       []string
	}{
		{
			name:        "Codex",
			task:        "tasks/devtools_codex.yml",
			officialURL: "https://chatgpt.com/codex/install.sh",
			command:     "codex",
			extra: []string{
				"CODEX_NON_INTERACTIVE",
			},
		},
		{
			name:        "Claude Code",
			task:        "tasks/devtools_claude.yml",
			officialURL: "https://claude.ai/install.sh",
			command:     "claude",
			extra: []string{
				"- stable",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			playbook, task := materializedDeveloperToolsAsset(t, test.task)
			combined := playbook + "\n" + task

			expected := []string{
				test.officialURL,
				"ansible.builtin.stat:",
				"ansible.builtin.get_url:",
				"ansible.builtin.command:",
				`become_user: "{{ target_user }}"`,
				`HOME: "{{ devtools_target_home }}"`,
				"creates:",
				`mode: "0700"`,
				"--version",
				"/.local/bin/" + test.command,
			}
			expected = append(expected, test.extra...)
			for _, value := range expected {
				if !strings.Contains(combined, value) {
					t.Errorf("%s installer does not contain %q", test.name, value)
				}
			}

			for _, unsafe := range []string{
				"ansible.builtin.shell:",
				"| sh",
				"| bash",
				"validate_certs: false",
			} {
				if strings.Contains(task, unsafe) {
					t.Errorf("%s installer contains unsafe pattern %q", test.name, unsafe)
				}
			}
		})
	}
}

func TestWarpUsesOfficialDebWithoutRefreshingAPTIndexes(t *testing.T) {
	playbook, task := materializedDeveloperToolsAsset(
		t,
		"tasks/devtools_warp.yml",
	)
	combined := playbook + "\n" + task

	for _, expected := range []string{
		"https://app.warp.dev/download?package=deb",
		"https://app.warp.dev/download?package=deb_arm64",
		"warp-terminal",
		"dpkg-query",
		"--print-architecture",
		"amd64",
		"arm64",
		"ansible.builtin.assert:",
		"ansible.builtin.get_url:",
		"/usr/bin/dpkg-deb",
		"- Package",
		"- Architecture",
		"- Version",
		"/usr/bin/apt-get",
		"--no-install-recommends",
		"--no-remove",
		"DEBIAN_FRONTEND: noninteractive",
		"/etc/apt/sources.list.d/warpdotdev.list",
		"/etc/apt/keyrings/warpdotdev.gpg",
		"Remove the temporary Warp package directory",
	} {
		if !strings.Contains(combined, expected) {
			t.Errorf("Warp installer does not contain %q", expected)
		}
	}

	for _, unsafe := range []string{
		"ansible.builtin.shell:",
		"ansible.builtin.apt:",
		"update_cache:",
		"apt-get update",
		"apt-key",
		"--dearmor",
		"ansible.builtin.copy:",
		"validate_certs: false",
		"arch=amd64",
	} {
		if strings.Contains(task, unsafe) {
			t.Errorf(
				"Warp direct-package installer contains forbidden pattern %q",
				unsafe,
			)
		}
	}
}

func TestWarpSkipsDirectPackageWhenAlreadyInstalled(t *testing.T) {
	_, task := materializedDeveloperToolsAsset(
		t,
		"tasks/devtools_warp.yml",
	)

	installBlock := assetBlockBetween(
		t,
		task,
		"- name: Install Warp Terminal from the official Debian package",
		"- name: Preview the Warp Terminal installation in check mode",
	)
	for _, expected := range []string{
		"- not devtools_warp_installed",
		"ansible.builtin.get_url:",
		"/usr/bin/apt-get",
		"always:",
		"Remove the temporary Warp package directory",
	} {
		if !strings.Contains(installBlock, expected) {
			t.Errorf(
				"Warp installed-state guard block does not contain %q",
				expected,
			)
		}
	}
}

func materializedDeveloperToolsAsset(
	t *testing.T,
	relativePath string,
) (string, string) {
	t.Helper()

	playbookPath, cleanup, err := Materialize(InstallDevTools)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	playbookData, err := os.ReadFile(playbookPath)
	if err != nil {
		t.Fatalf("read materialized developer-tools playbook: %v", err)
	}
	assetData, err := os.ReadFile(
		filepath.Join(filepath.Dir(playbookPath), filepath.FromSlash(relativePath)),
	)
	if err != nil {
		t.Fatalf("read materialized developer-tools asset %q: %v", relativePath, err)
	}

	return string(playbookData), string(assetData)
}

func TestSwayInstallUsesPrivateHeadlessValidationRuntime(t *testing.T) {
	playbookPath, cleanup, err := Materialize(InstallSway)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	taskPath := filepath.Join(
		filepath.Dir(playbookPath),
		"tasks",
		"install_sway.yml",
	)
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read Sway installation tasks: %v", err)
	}
	tasks := string(data)

	configRootBlock := assetBlockBetween(
		t,
		tasks,
		"- name: Create a private user configuration root",
		"- name: Create Sway configuration directories",
	)
	for _, expected := range []string{
		`path: "{{ sway_target_home }}/.config"`,
		`owner: "{{ target_user }}"`,
		`group: "{{ sway_target_group_result.stdout }}"`,
		`mode: "0700"`,
	} {
		if !strings.Contains(configRootBlock, expected) {
			t.Errorf("private configuration-root task does not contain %q", expected)
		}
	}

	childDirectoriesBlock := assetBlockBetween(
		t,
		tasks,
		"- name: Create Sway configuration directories",
		"- name: Install and validate the Sway configuration atomically",
	)
	if strings.Contains(childDirectoriesBlock, "\n    - .config\n") {
		t.Fatal("the mode-0755 child-directory loop also changes the private .config root")
	}
	for _, expected := range []string{
		`mode: "0755"`,
		"- .config/sway",
		"- .config/sway/config.d",
		"- .config/waybar",
	} {
		if !strings.Contains(childDirectoriesBlock, expected) {
			t.Errorf("Sway child-directory task does not contain %q", expected)
		}
	}

	validationBlock := assetBlockBetween(
		t,
		tasks,
		"- name: Install and validate the Sway configuration atomically",
		"- name: Install the remaining Sway desktop configuration",
	)
	for _, expected := range []string{
		"ansible.builtin.tempfile:",
		"prefix: linuxcncsetup-sway-validate-",
		`become_user: "{{ target_user }}"`,
		"register: sway_validation_runtime",
		`mode: "0700"`,
		"validate: /usr/bin/sway --validate --config %s",
		"- name: Validate the installed Sway configuration and included snippets",
		`HOME: "{{ sway_target_home }}"`,
		`XDG_RUNTIME_DIR: "{{ sway_validation_runtime.path }}"`,
		"WLR_BACKENDS: headless",
		"WLR_RENDERER: pixman",
		`WLR_LIBINPUT_NO_DEVICES: "1"`,
		`WAYLAND_DISPLAY: ""`,
		`DISPLAY: ""`,
		`SWAYSOCK: ""`,
		"always:",
		"- name: Remove the temporary Sway validation runtime",
		`path: "{{ sway_validation_runtime.path }}"`,
		"state: absent",
		"- sway_validation_runtime is defined",
		"- sway_validation_runtime.path is defined",
	} {
		if !strings.Contains(validationBlock, expected) {
			t.Errorf("atomic Sway validation block does not contain %q", expected)
		}
	}
	if got := strings.Count(
		validationBlock,
		`XDG_RUNTIME_DIR: "{{ sway_validation_runtime.path }}"`,
	); got != 2 {
		t.Errorf("temporary validation runtime is referenced %d times; want template and final validation", got)
	}
	if strings.Contains(validationBlock, "/run/user/") {
		t.Fatal("headless Sway validation still depends on a live /run/user runtime")
	}
	if strings.Index(validationBlock, "always:") >
		strings.Index(validationBlock, "- name: Remove the temporary Sway validation runtime") {
		t.Fatal("temporary validation runtime removal is not inside the always block")
	}
}

func TestSwayTemplateValidatesHeadlesslyWithoutRuntimeArtifacts(t *testing.T) {
	swayPath, err := exec.LookPath("sway")
	if err != nil {
		t.Skip("sway is not available")
	}

	playbookPath, cleanup, err := Materialize(InstallSway)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)
	templatePath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"sway-config.j2",
	)
	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read Sway configuration template: %v", err)
	}
	rendered := string(data)
	for placeholder, value := range map[string]string{
		"{{ sway_effective_terminal }}": "foot",
		"{{ sway_output_name }}":        "DP-1",
		"{{ sway_output_mode }}":        "1920x1080",
		"{{ sway_output_position }}":    "0,0",
		"{{ sway_keyboard_layout }}":    "us",
	} {
		rendered = strings.ReplaceAll(rendered, placeholder, value)
	}
	if strings.Contains(rendered, "{{") || strings.Contains(rendered, "{%") {
		t.Fatalf("minimal Sway template rendering left a Jinja expression")
	}

	fixtureRoot := t.TempDir()
	targetHome := filepath.Join(fixtureRoot, "home")
	configDirectory := filepath.Join(targetHome, ".config", "sway")
	if err := os.MkdirAll(
		filepath.Join(configDirectory, "config.d"),
		0o755,
	); err != nil {
		t.Fatalf("create Sway configuration fixture: %v", err)
	}
	configPath := filepath.Join(configDirectory, "config")
	if err := os.WriteFile(configPath, []byte(rendered), 0o644); err != nil {
		t.Fatalf("write rendered Sway configuration: %v", err)
	}

	runtimeDirectory, err := os.MkdirTemp(
		"",
		"linuxcncsetup-sway-validate-",
	)
	if err != nil {
		t.Fatalf("create private validation runtime: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(runtimeDirectory)
	})
	if err := os.Chmod(runtimeDirectory, 0o700); err != nil {
		t.Fatalf("make validation runtime private: %v", err)
	}
	info, err := os.Stat(runtimeDirectory)
	if err != nil {
		t.Fatalf("stat private validation runtime: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("validation runtime mode = %#o; want 0700", got)
	}

	command := exec.Command(
		swayPath,
		"--validate",
		"--config",
		configPath,
	)
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"HOME=" + targetHome,
		"XDG_CONFIG_HOME=" + filepath.Join(targetHome, ".config"),
		"XDG_CONFIG_DIRS=/etc/xdg",
		"XDG_RUNTIME_DIR=" + runtimeDirectory,
		"WLR_BACKENDS=headless",
		"WLR_RENDERER=pixman",
		"WLR_LIBINPUT_NO_DEVICES=1",
		"WAYLAND_DISPLAY=",
		"DISPLAY=",
		"SWAYSOCK=",
	}
	output, validationErr := command.CombinedOutput()
	if validationErr != nil ||
		strings.Contains(string(output), "Error(s) loading config!") {
		t.Fatalf(
			"headless Sway template validation failed: %v\n%s",
			validationErr,
			output,
		)
	}
	// Sway may leave its headless Wayland socket and lock in the private
	// runtime. The playbook's always block removes the entire directory.
	if err := os.RemoveAll(runtimeDirectory); err != nil {
		t.Fatalf("clean private validation runtime: %v", err)
	}
	if _, err := os.Stat(runtimeDirectory); !os.IsNotExist(err) {
		t.Fatalf("validation runtime survived cleanup: %v", err)
	}
}

func assetBlockBetween(
	t *testing.T,
	content string,
	startMarker string,
	endMarker string,
) string {
	t.Helper()
	start := strings.Index(content, startMarker)
	if start < 0 {
		t.Fatalf("asset does not contain start marker %q", startMarker)
	}
	endRelative := strings.Index(content[start:], endMarker)
	if endRelative < 0 {
		t.Fatalf("asset does not contain end marker %q after %q", endMarker, startMarker)
	}
	return content[start : start+endRelative]
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
