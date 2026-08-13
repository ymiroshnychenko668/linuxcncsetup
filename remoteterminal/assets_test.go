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
		"ansible/roles/remoteterminal/handlers/main.yml",
		"ansible/roles/remoteterminal/tasks/main.yml",
		"ansible/roles/remoteterminal/tasks/build_application.yml",
		"ansible/roles/remoteterminal/tasks/build_ttyd.yml",
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
		"internal/httpapi/server.go",
		"internal/sessions/manager.go",
		"web/index.html",
		"web/package.json",
		"web/package-lock.json",
		"web/tsconfig.json",
		"web/vite.config.ts",
		"web/src/main.tsx",
		"web/src/components/LoginView.tsx",
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
