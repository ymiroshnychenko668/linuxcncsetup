package remoteterminalassets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeProvidesProductionInstallTreeOutsideCheckout(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	outsideCheckout := t.TempDir()
	if err := os.Chdir(outsideCheckout); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	sourceDirectory, playbookPath, cleanup, err := Materialize()
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	if !filepath.IsAbs(sourceDirectory) || !filepath.IsAbs(playbookPath) {
		t.Fatalf("materialized paths must be absolute: source=%q playbook=%q", sourceDirectory, playbookPath)
	}
	if filepath.Base(sourceDirectory) == "." ||
		!strings.HasPrefix(filepath.Base(sourceDirectory), "linuxcncsetup-remoteterminal-") {
		t.Fatalf("unexpected temporary source path: %q", sourceDirectory)
	}
	wantPlaybook := filepath.Join(sourceDirectory, "ansible", "install.yml")
	if playbookPath != wantPlaybook {
		t.Fatalf("playbook path = %q; want %q", playbookPath, wantPlaybook)
	}

	requiredFiles := []string{
		"go.mod",
		"go.sum",
		"ansible/install.yml",
		"ansible/roles/remoteterminal/defaults/main.yml",
		"ansible/roles/remoteterminal/files/ttyd-disable-browser-clipboard.patch",
		"ansible/roles/remoteterminal/handlers/main.yml",
		"ansible/roles/remoteterminal/tasks/main.yml",
		"ansible/roles/remoteterminal/tasks/build_application.yml",
		"ansible/roles/remoteterminal/tasks/build_ttyd.yml",
		"ansible/roles/remoteterminal/tasks/install_go.yml",
		"ansible/roles/remoteterminal/tasks/install_code_server.yml",
		"ansible/roles/remoteterminal/tasks/dependencies.yml",
		"ansible/roles/remoteterminal/tasks/deploy.yml",
		"ansible/roles/remoteterminal/tasks/preflight.yml",
		"ansible/roles/remoteterminal/tasks/prepare_build.yml",
		"ansible/roles/remoteterminal/tasks/tls.yml",
		"ansible/roles/remoteterminal/templates/remoteterminal.env.j2",
		"ansible/roles/remoteterminal/templates/remoteterminal.service.j2",
		"cmd/remoteterminal/main.go",
		"internal/auth/pam_linux.go",
		"internal/config/config.go",
		"internal/codeservers/manager.go",
		"internal/codeservers/proxy.go",
		"internal/httpapi/server.go",
		"internal/sessions/manager.go",
		"web/index.html",
		"web/package.json",
		"web/package-lock.json",
		"web/tsconfig.json",
		"web/vite.config.ts",
		"web/src/main.tsx",
		"web/src/components/LoginView.tsx",
		"web/src/components/CodeServerPanel.tsx",
		"web/src/components/CopySelectionModal.tsx",
		"web/src/components/LaunchCodeServerModal.tsx",
		"web/src/components/ShutdownCodeServerModal.tsx",
		"web/src/components/TerminalPanel.tsx",
		"web/src/components/Workspace.tsx",
	}
	for _, relativePath := range requiredFiles {
		info, err := os.Stat(filepath.Join(sourceDirectory, filepath.FromSlash(relativePath)))
		if err != nil {
			t.Errorf("required production file %q: %v", relativePath, err)
			continue
		}
		if !info.Mode().IsRegular() {
			t.Errorf("required production path %q is not a regular file", relativePath)
		}
	}

	assertReadableSourceModes(t, sourceDirectory)
}

func TestMaterializeExcludesDevelopmentAndGeneratedFiles(t *testing.T) {
	sourceDirectory, _, cleanup, err := Materialize()
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	forbiddenPaths := []string{
		"README.md",
		"IMPLEMENTATION_PLAN.md",
		"Makefile",
		"scripts/build.sh",
		"build",
		".env",
		"internal/auth/auth_test.go",
		"internal/config/config_test.go",
		"internal/httpapi/server_test.go",
		"internal/sessions/manager_test.go",
		"web/node_modules",
		"web/dist",
		"web/src/api.test.ts",
		"web/src/components/LoginView.test.tsx",
		"web/src/components/Workspace.test.tsx",
		"web/src/test",
		"web/tsconfig.app.tsbuildinfo",
		"web/tsconfig.node.tsbuildinfo",
	}
	for _, relativePath := range forbiddenPaths {
		_, err := os.Lstat(filepath.Join(sourceDirectory, filepath.FromSlash(relativePath)))
		if !os.IsNotExist(err) {
			t.Errorf("non-production path %q was materialized: %v", relativePath, err)
		}
	}

	err = filepath.WalkDir(sourceDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.Contains(entry.Name(), "_test.") || strings.HasSuffix(entry.Name(), ".tsbuildinfo") {
			t.Errorf("non-production file was materialized: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk materialized source: %v", err)
	}
}

func TestMaterializeCleanupIsIdempotent(t *testing.T) {
	sourceDirectory, _, cleanup, err := Materialize()
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}

	cleanup()
	if _, err := os.Stat(sourceDirectory); !os.IsNotExist(err) {
		t.Fatalf("cleanup left materialized source behind: %v", err)
	}

	cleanup()
}

func TestMaterializedInstallPlaybookSyntax(t *testing.T) {
	ansiblePlaybook, err := exec.LookPath("ansible-playbook")
	if err != nil {
		t.Skip("ansible-playbook is not installed")
	}

	_, playbookPath, cleanup, err := Materialize()
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	command := exec.Command(
		ansiblePlaybook,
		"--syntax-check",
		"--inventory", "localhost,",
		playbookPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("materialized playbook syntax check failed: %v\n%s", err, output)
	}
}

func TestMaterializedDependenciesRequireTmux32ForTerminalColorQueries(t *testing.T) {
	sourceDirectory, _, cleanup, err := Materialize()
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	contents, err := os.ReadFile(filepath.Join(
		sourceDirectory,
		"ansible", "roles", "remoteterminal", "tasks", "dependencies.yml",
	))
	if err != nil {
		t.Fatalf("read materialized dependency checks: %v", err)
	}
	dependencies := string(contents)
	for _, required := range []string{
		`- "{{ remoteterminal_tmux_binary }}"`,
		"- -V\n",
		"register: remoteterminal_tmux_version\n",
		"remoteterminal_tmux_version.stdout.split()[1].split('.')[0] | int",
		"regex_replace('[^0-9].*$', '') | int",
		"remoteterminal_tmux_version_major | int > 3",
		"remoteterminal_tmux_version_minor | int >= 2",
		"OSC 10/11\n      default-colour queries to render terminal block fills",
	} {
		if !strings.Contains(dependencies, required) {
			t.Fatalf("materialized dependency checks are missing %q", required)
		}
	}
	if strings.Contains(dependencies, "/usr/bin/tmux") {
		t.Fatal("materialized dependency check bypasses the configured tmux binary")
	}
}

func TestMaterializedDependenciesRefreshAPTOnlyForMissingPackages(t *testing.T) {
	sourceDirectory, _, cleanup, err := Materialize()
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	contents, err := os.ReadFile(filepath.Join(
		sourceDirectory,
		"ansible", "roles", "remoteterminal", "tasks", "dependencies.yml",
	))
	if err != nil {
		t.Fatalf("read materialized dependency tasks: %v", err)
	}
	dependencies := string(contents)
	for _, required := range []string{
		"ansible.builtin.package_facts:\n",
		"remoteterminal_missing_apt_packages: >-\n",
		"difference(ansible_facts.packages.keys() | list)",
		"- /usr/bin/apt-get\n",
		"- Acquire::Retries=3\n",
		"- DPkg::Lock::Timeout=120\n",
		"register: remoteterminal_apt_cache_refresh\n",
		"until: remoteterminal_apt_cache_refresh.rc == 0\n",
		"name: \"{{ remoteterminal_missing_apt_packages }}\"\n",
		"remoteterminal_missing_apt_packages | length > 0\n",
	} {
		if !strings.Contains(dependencies, required) {
			t.Fatalf("materialized dependency tasks are missing %q", required)
		}
	}
	if strings.Contains(dependencies, "update_cache:") {
		t.Fatal("dependency installation hides APT refresh diagnostics behind the apt module")
	}
}

func TestDeployedFrontendRootIsFrozenBeforeReadabilityProbe(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(
		"ansible", "roles", "remoteterminal", "tasks", "deploy.yml",
	))
	if err != nil {
		t.Fatalf("read deployment tasks: %v", err)
	}
	deployment := string(contents)
	rootContract := `- name: Enforce deployed frontend root ownership and mode
  ansible.builtin.file:
    path: "{{ remoteterminal_release_dir }}/web"
    state: directory
    owner: root
    group: root
    mode: "0755"`
	if !strings.Contains(deployment, rootContract) {
		t.Fatal("deployment does not normalize the frontend root directory itself")
	}

	installIndex := strings.Index(deployment, "- name: Install versioned frontend assets")
	rootIndex := strings.Index(deployment, rootContract)
	readabilityIndex := strings.Index(deployment, "- name: Verify service account can read deployed frontend entrypoint")
	if installIndex < 0 || rootIndex < 0 || readabilityIndex < 0 ||
		!(installIndex < rootIndex && rootIndex < readabilityIndex) {
		t.Fatal("frontend root must be normalized after rsync and before the service-account probe")
	}
}

func TestServiceTemplateSanitizesTerminalColorEnvironment(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "templates", "remoteterminal.service.j2"))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(contents)
	for _, directive := range []string{
		"Environment=COLORTERM=truecolor\n",
		"UnsetEnvironment=NO_COLOR\n",
	} {
		if !strings.Contains(unit, directive) {
			t.Fatalf("service template is missing %q", strings.TrimSpace(directive))
		}
	}
}

func TestCodeServerDependencyIsPinnedAndWiredIntoService(t *testing.T) {
	defaults, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "defaults", "main.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		`remoteterminal_code_server_version: "4.132.0"`,
		`code-server-4.132.0-linux-amd64.tar.gz`,
		`sha256:a38d26f4cb81f768feddff79e2937fd3f39c83d3da8be3da7225e1087e62e4ed`,
	} {
		if !strings.Contains(string(defaults), value) {
			t.Fatalf("Ansible defaults are missing pinned code-server value %q", value)
		}
	}

	environment, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "templates", "remoteterminal.env.j2"))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "templates", "remoteterminal.service.j2"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(environment), "REMOTE_TERMINAL_CODE_SERVER_BINARY=") ||
		!strings.Contains(string(environment), "REMOTE_TERMINAL_STATE_DIR=") ||
		!strings.Contains(string(environment), "REMOTE_TERMINAL_MAX_CODE_SERVERS=") {
		t.Fatal("service environment does not expose the managed code-server settings")
	}
	if !strings.Contains(string(unit), "ConditionFileIsExecutable={{ remoteterminal_code_server_tool_dir }}/bin/code-server") {
		t.Fatal("service unit does not require the managed code-server executable")
	}
}

func TestRememberMeTimeoutIsWiredIntoDeployment(t *testing.T) {
	roleRoot := filepath.Join("ansible", "roles", "remoteterminal")
	read := func(relative string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(roleRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return string(contents)
	}

	if !strings.Contains(read("defaults/main.yml"), `remoteterminal_remember_timeout: ""`) ||
		!strings.Contains(read("tasks/preflight.yml"), "remoteterminal_remember_timeout is match") ||
		!strings.Contains(read("templates/remoteterminal.env.j2"), "REMOTE_TERMINAL_REMEMBER_TIMEOUT={{ remoteterminal_remember_timeout }}") {
		t.Fatal("Remember me timeout is not validated and rendered by the deployment role")
	}
}

func TestWorkspaceSidebarUsesCompactDensity(t *testing.T) {
	styles, err := os.ReadFile(filepath.Join("web", "src", "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(styles)
	for _, contract := range []string{
		"--sidebar-width: 236px",
		".sidebar__header { display: flex; height: 56px",
		".session-tab { display: flex; min-height: 44px",
		".sidebar__footer { display: flex; min-height: 52px",
	} {
		if !strings.Contains(contents, contract) {
			t.Fatalf("compact sidebar styling is missing %q", contract)
		}
	}
}

func TestProductionTransportDefaultsToHTTPAndSkipsTLS(t *testing.T) {
	roleRoot := filepath.Join("ansible", "roles", "remoteterminal")
	read := func(relative string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(roleRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		return string(contents)
	}

	contracts := map[string][]string{
		"defaults/main.yml": {
			"remoteterminal_transport: http",
		},
		"tasks/preflight.yml": {
			"remoteterminal_transport in ['https', 'http']",
			"remoteterminal_tls_enabled: \"{{ remoteterminal_transport == 'https' }}\"",
			"remoteterminal_public_endpoint:",
		},
		"tasks/deploy.yml": {
			"when: remoteterminal_tls_enabled | bool",
			"remoteterminal_tls_configuration_fingerprint: disabled-http-v1",
			`url: "{{ remoteterminal_public_endpoint }}healthz"`,
			"plaintext HTTP is enabled",
		},
		"templates/remoteterminal.env.j2": {
			"REMOTE_TERMINAL_TRANSPORT={{ remoteterminal_transport }}",
			"{% if remoteterminal_tls_enabled | bool %}",
		},
	}
	for relative, required := range contracts {
		content := read(relative)
		for _, contract := range required {
			if !strings.Contains(content, contract) {
				t.Fatalf("%s is missing transport contract %q", relative, contract)
			}
		}
	}
}

func TestGoToolchainIsExactlyPinnedApplicationOwnedAndUsedForBuild(t *testing.T) {
	roleRoot := filepath.Join("ansible", "roles", "remoteterminal")
	read := func(relative string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(roleRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		return string(contents)
	}

	defaults := read("defaults/main.yml")
	for _, pinned := range []string{
		`remoteterminal_go_version: "1.26.5"`,
		`remoteterminal_go_archive_url: "https://go.dev/dl/go1.26.5.linux-amd64.tar.gz"`,
		`remoteterminal_go_archive_checksum: "sha256:5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053"`,
	} {
		if !strings.Contains(defaults, pinned) {
			t.Fatalf("Ansible defaults are missing exact Go pin %q", pinned)
		}
	}
	if strings.Contains(defaults, "golang-go") {
		t.Fatal("Ansible defaults still install or reference Debian golang-go")
	}

	mainTasks := read("tasks/main.yml")
	installIndex := strings.Index(mainTasks, "include_tasks: install_go.yml")
	buildIndex := strings.Index(mainTasks, "include_tasks: build_application.yml")
	if installIndex < 0 || buildIndex < 0 || installIndex >= buildIndex {
		t.Fatal("pinned Go must be installed before the application is built")
	}

	installer := read("tasks/install_go.yml")
	for _, contract := range []string{
		"Download official checksum-pinned Go archive",
		`checksum: "{{ remoteterminal_go_archive_checksum }}"`,
		"Validate Go archive members before extraction",
		"normalized in seen",
		"top-level go archive member is not a directory",
		"Audit installed Go tree permissions, links, types, and bounds",
		"Audit extracted Go tree permissions, links, and bounds",
		`become_user: "{{ remoteterminal_user }}"`,
		`GOTOOLCHAIN: "local"`,
		`GOENV: "off"`,
		`GOWORK: "off"`,
		"go version go",
		"linux/amd64",
		"Atomically activate complete Go tool candidate",
		"remoteterminal_go_installed_version_file.content | default('') | b64decode).splitlines()",
		"remoteterminal_go_candidate_version_file.content | b64decode).splitlines()",
		"remoteterminal_go_final_version_file.content | b64decode).splitlines()",
		"(remoteterminal_go_final_marker.content | b64decode).splitlines() ==",
		"binary_sha256={{ remoteterminal_go_candidate_artifacts.results[0].stat.checksum }}",
		"renameat2",
		"remoteterminal_go_release_fingerprint",
	} {
		if !strings.Contains(installer, contract) {
			t.Fatalf("Go installer is missing security/idempotence contract %q", contract)
		}
	}
	if !strings.Contains(installer, `{{ remoteterminal_install_root }}/tools/go-`) {
		t.Fatal("Go installer does not use the application-owned tool root")
	}

	buildTasks := read("tasks/build_application.yml")
	for _, contract := range []string{
		`- "{{ remoteterminal_go_binary }}"`,
		"remoteterminal_go_release_fingerprint",
		`GOROOT: "{{ remoteterminal_go_tool_dir }}"`,
		`PATH: "{{ remoteterminal_go_tool_dir }}/bin:/usr/bin:/bin"`,
	} {
		if !strings.Contains(buildTasks, contract) {
			t.Fatalf("application build is missing pinned Go contract %q", contract)
		}
	}

	err := filepath.WalkDir(filepath.Join(roleRoot, "tasks"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), "/usr/bin/go") || strings.Contains(string(contents), "golang-go") {
			t.Errorf("role task still references distribution Go: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk Ansible role tasks: %v", err)
	}

	uninstall := read("tasks/uninstall.yml")
	if !strings.Contains(uninstall, "pinned Go build toolchain is removed with the application install") {
		t.Fatal("uninstall report does not explain app-owned Go removal semantics")
	}
}

func TestPinnedToolCompletionMarkersUseLineBasedComparisons(t *testing.T) {
	ttydTasks, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "build_ttyd.yml"))
	if err != nil {
		t.Fatalf("read ttyd tasks: %v", err)
	}
	codeServerTasks, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "install_code_server.yml"))
	if err != nil {
		t.Fatalf("read code-server tasks: %v", err)
	}

	for name, content := range map[string]string{
		"ttyd":        string(ttydTasks),
		"code-server": string(codeServerTasks),
	} {
		if !strings.Contains(content, "installed_marker.content | default('') | b64decode).splitlines() ==") {
			t.Fatalf("%s reuse check does not compare the real marker lines", name)
		}
	}
	if strings.Contains(string(ttydTasks), "completion_prefix ~ '\\nsha256='") {
		t.Fatal("ttyd reuse check still constructs a literal backslash-n marker")
	}
	if strings.Contains(string(codeServerTasks), "completion_prefix | trim) ~ '\\n'") {
		t.Fatal("code-server reuse check still constructs a literal backslash-n marker")
	}
}

func TestCodeServerExtractionRemovesGroupAndOtherWritePermissions(t *testing.T) {
	codeServerTasks, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "install_code_server.yml"))
	if err != nil {
		t.Fatalf("read code-server tasks: %v", err)
	}

	contents := string(codeServerTasks)
	if !strings.Contains(contents, "Remove group and other write permissions from extracted code-server entries") ||
		!strings.Contains(contents, "stat.S_IMODE(details.st_mode) & ~0o022") {
		t.Fatal("code-server extraction does not deterministically remove group/other write permissions")
	}
	if !strings.Contains(contents, "details.st_mode & 0o022") {
		t.Fatal("code-server extraction no longer validates group/other write permissions")
	}
}

func TestPinnedTtydWebBuildRegeneratesPatchedHeader(t *testing.T) {
	read := func(relativePath string) string {
		contents, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		return string(contents)
	}

	defaults := read("defaults/main.yml")
	for _, pin := range []string{
		`remoteterminal_ttyd_commit: b1eaaee2ca70774c6cb045645914ebc27a70dedd`,
		`remoteterminal_ttyd_corepack_version: "0.29.4"`,
		`remoteterminal_ttyd_corepack_package_checksum: "sha256:ebd45f1694cb56bfc114fc05b9322ac6c60fb535e5c33af17dfb913a796668c4"`,
		`remoteterminal_ttyd_yarn_version: "3.6.3"`,
		`remoteterminal_ttyd_yarn_binary_checksum: "sha256:08ead1821a257416e6f217e89365425bf4b6d2430c3279318bedcec1a245fff5"`,
		`remoteterminal_ttyd_web_patch_filename: ttyd-disable-browser-clipboard.patch`,
		`remoteterminal_ttyd_web_inputs_digest: 93a1f8ef447a761164d2e5b86d00a5eb9e061aa10fc6fb25796d1e270289fd61`,
		"  - patch\n",
	} {
		if !strings.Contains(defaults, pin) {
			t.Fatalf("ttyd defaults are missing pinned web-build input %q", pin)
		}
	}

	webPatch := read("files/ttyd-disable-browser-clipboard.patch")
	for _, removal := range []string{
		"-import { ClipboardAddon } from '@xterm/addon-clipboard';",
		"-    private clipboardAddon = new ClipboardAddon();",
		"-        terminal.loadAddon(clipboardAddon);",
		"-            terminal.onSelectionChange(() => {",
		"-                    document.execCommand('copy');",
		"-                this.overlayAddon?.showOverlay('\\u2702', 200);",
	} {
		if !strings.Contains(webPatch, removal) {
			t.Fatalf("bundled ttyd patch does not remove %q", removal)
		}
	}

	build := read("tasks/build_ttyd.yml")
	for _, contract := range []string{
		"Hash bundled ttyd web patch as an immutable build input",
		"rstrip=false) | hash('sha256')",
		"remoteterminal_ttyd_web_patch_digest ~ '\\n'",
		"remoteterminal_ttyd_web_inputs_digest ~ '\\n'",
		"remoteterminal_ttyd_corepack_version ~ '\\n'",
		"remoteterminal_ttyd_corepack_package_checksum ~ '\\n'",
		"remoteterminal_ttyd_yarn_version ~ '\\n'",
		"remoteterminal_ttyd_yarn_binary_checksum ~ '\\n'",
		"Apply exact ttyd browser-clipboard patch without fuzzy matching",
		"--fuzz=0",
		"Inventory every patched ttyd web build input",
		"Hash every patched ttyd web build input with SHA-256",
		"remoteterminal_ttyd_web_input_stats.results",
		"Verify every patched ttyd web input against the pinned manifest digest",
		"Download checksum-pinned Corepack package for ttyd web build",
		`- "{{ remoteterminal_ttyd_corepack_package_path }}"`,
		`- "yarn@{{ remoteterminal_ttyd_yarn_version }}"`,
		"Verify checksum-pinned ttyd Yarn executable before use",
		"Install immutable ttyd web dependencies from pinned Yarn lock",
		"Regenerate patched ttyd src/html.h from web sources",
		"remoteterminal_ttyd_generated_web_artifacts.results[0].stat.checksum !=",
		"navigator[.]clipboard|document[.]execCommand|ClipboardAddon",
	} {
		if !strings.Contains(build, contract) {
			t.Fatalf("ttyd build is missing patched web-build contract %q", contract)
		}
	}
	if strings.Contains(build, "ansible.builtin.find:\n") && strings.Contains(build, "file_type: file\n        hidden: true\n        recurse: true\n        get_checksum: true") {
		t.Fatal("ttyd web manifest asks ansible-core 2.14 find for unsupported SHA-256 checksums")
	}
	patchIndex := strings.Index(build, "Apply exact ttyd browser-clipboard patch without fuzzy matching")
	webBuildIndex := strings.Index(build, "Regenerate patched ttyd src/html.h from web sources")
	cmakeIndex := strings.Index(build, "Configure pinned ttyd build")
	if patchIndex < 0 || webBuildIndex < 0 || cmakeIndex < 0 || !(patchIndex < webBuildIndex && webBuildIndex < cmakeIndex) {
		t.Fatal("ttyd must apply its web patch and regenerate src/html.h before CMake configures the binary")
	}

	preflight := read("tasks/preflight.yml")
	for _, validation := range []string{
		"remoteterminal_ttyd_commit is match('^[0-9a-f]{40}$')",
		"remoteterminal_ttyd_corepack_version is match('^[0-9]+\\.[0-9]+\\.[0-9]+$')",
		"remoteterminal_ttyd_corepack_package_checksum is match('^sha256:[0-9a-f]{64}$')",
		"remoteterminal_ttyd_yarn_version is match('^[0-9]+\\.[0-9]+\\.[0-9]+$')",
		"remoteterminal_ttyd_yarn_binary_checksum is match('^sha256:[0-9a-f]{64}$')",
		"remoteterminal_ttyd_web_inputs_digest is match('^[0-9a-f]{64}$')",
	} {
		if !strings.Contains(preflight, validation) {
			t.Fatalf("ttyd preflight is missing %q", validation)
		}
	}

	applicationBuild := read("tasks/build_application.yml")
	if !strings.Contains(applicationBuild, "remoteterminal_ttyd_build_fingerprint ~ '\\n'") {
		t.Fatal("application release identity does not include the full patched ttyd build fingerprint")
	}
}

func TestTtydRepairPersistsRestartIntentBeforeToolMutation(t *testing.T) {
	build, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "build_ttyd.yml"))
	if err != nil {
		t.Fatal(err)
	}
	buildText := string(build)
	for _, contract := range []string{
		"Record whether ttyd must be replaced during this deployment",
		"remoteterminal_ttyd_tool_replaced",
		"Inspect durable restart marker before ttyd mutation",
		"Reject unsafe durable restart marker before ttyd mutation",
		"Persist restart requirement before ttyd mutation",
		"ttyd-tool-reconciliation-required",
	} {
		if !strings.Contains(buildText, contract) {
			t.Fatalf("ttyd installer is missing durable repair contract %q", contract)
		}
	}
	restartIndex := strings.Index(buildText, "Persist restart requirement before ttyd mutation")
	exchangeIndex := strings.Index(buildText, "Atomically exchange complete candidate with existing ttyd tool")
	if restartIndex < 0 || exchangeIndex < 0 || restartIndex >= exchangeIndex {
		t.Fatal("ttyd repair must persist restart intent before exchanging the live tool")
	}

	deployment, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "deploy.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(deployment), "(remoteterminal_ttyd_tool_replaced | bool)") {
		t.Fatal("deployment restart condition does not include same-fingerprint ttyd replacement")
	}
}

func TestAnsibleValidatesCanonicalNumbersAndMakesCodeServerRepairDurable(t *testing.T) {
	preflight, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "preflight.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"(remoteterminal_port | string) is match('^[1-9][0-9]*$')",
		"(remoteterminal_max_sessions | string) is match('^[1-9][0-9]*$')",
		"(remoteterminal_max_code_servers | string) is match('^[1-9][0-9]*$')",
		"(remoteterminal_build_jobs | string) is match('^[1-9][0-9]*$')",
		"(remoteterminal_generated_tls_days | string) is match('^[1-9][0-9]*$')",
		"(remoteterminal_generated_tls_renew_before_seconds | string) is match('^[1-9][0-9]*$')",
		"(remoteterminal_health_retries | string) is match('^[1-9][0-9]*$')",
		"(remoteterminal_health_delay | string) is match('^(0|[1-9][0-9]*)$')",
	} {
		if !strings.Contains(string(preflight), expression) {
			t.Fatalf("Ansible preflight is missing canonical integer validation %q", expression)
		}
	}

	environment, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "templates", "remoteterminal.env.j2"))
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{
		"REMOTE_TERMINAL_LISTEN_ADDRESS={{ remoteterminal_listen_address }}:{{ remoteterminal_port | int }}",
		"REMOTE_TERMINAL_MAX_SESSIONS={{ remoteterminal_max_sessions | int }}",
		"REMOTE_TERMINAL_MAX_CODE_SERVERS={{ remoteterminal_max_code_servers | int }}",
	} {
		if !strings.Contains(string(environment), setting) {
			t.Fatalf("service environment is missing canonical integer rendering %q", setting)
		}
	}

	installer, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "install_code_server.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{
		"Persist restart requirement before code-server mutation",
		"(remoteterminal_code_server_deferred_cleanup_paths | length > 0)",
		"Stop only the exact private code-server tmux server after tool replacement",
		"- /usr/bin/tmux",
		"{{ remoteterminal_runtime_dir }}/code-server.tmux.sock",
		"remoteterminal_code_server_repair_tmux_socket.stat.nlink | default(0) | int == 1",
		"Retain displaced code-server tool until service health verification",
	} {
		if !strings.Contains(string(installer), contract) {
			t.Fatalf("code-server installer is missing repair-safety contract %q", contract)
		}
	}
	restartIndex := strings.Index(string(installer), "Persist restart requirement before code-server mutation")
	exchangeIndex := strings.Index(string(installer), "Atomically exchange complete candidate with existing code-server tool")
	stopIndex := strings.Index(string(installer), "Stop only the exact private code-server tmux server after tool replacement")
	if restartIndex < 0 || exchangeIndex < 0 || stopIndex < 0 || !(restartIndex < exchangeIndex && exchangeIndex < stopIndex) {
		t.Fatal("code-server repair must persist restart intent, exchange the tool, then stop its exact private tmux server")
	}
	if strings.Contains(string(installer), "Remove displaced invalid code-server tool after successful exchange") {
		t.Fatal("code-server installer removes the displaced tool before deployment health verification")
	}

	deployment, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "deploy.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(deployment), "Remove validated inactive and displaced code-server tools after health verification") {
		t.Fatal("deployment does not defer displaced code-server cleanup until health verification")
	}
	healthIndex := strings.Index(string(deployment), "Verify deployed frontend is served over selected transport")
	cleanupIndex := strings.Index(string(deployment), "Remove validated inactive and displaced code-server tools after health verification")
	appliedIndex := strings.Index(string(deployment), "Record successfully applied deployment fingerprint after health verification")
	if healthIndex < 0 || cleanupIndex < 0 || appliedIndex < 0 || !(healthIndex < cleanupIndex && cleanupIndex < appliedIndex) {
		t.Fatal("displaced code-server cleanup must run after health verification while durable restart intent remains")
	}

	uninstall, err := os.ReadFile(filepath.Join("ansible", "roles", "remoteterminal", "tasks", "uninstall.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{
		"remoteterminal_uninstall_runtime_directory.stat.mode | default('') == '0700'",
		"remoteterminal_uninstall_tmux_socket.stat.mode | default('') == '0600'",
		"remoteterminal_uninstall_tmux_socket.stat.nlink | default(0) | int == 1",
		"remoteterminal_uninstall_code_server_tmux_socket.stat.mode | default('') == '0600'",
		"remoteterminal_uninstall_code_server_tmux_socket.stat.nlink | default(0) | int == 1",
	} {
		if !strings.Contains(string(uninstall), contract) {
			t.Fatalf("uninstall is missing private tmux cleanup invariant %q", contract)
		}
	}
}

func assertReadableSourceModes(t *testing.T, sourceDirectory string) {
	t.Helper()

	err := filepath.WalkDir(sourceDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("materialized source contains a symbolic link: %s", path)
			return nil
		}

		wantMode := os.FileMode(0o644)
		if entry.IsDir() {
			wantMode = 0o755
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Errorf("mode for %s = %04o; want %04o", path, got, wantMode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk materialized source permissions: %v", err)
	}
}
