// Package remoteterminalassets embeds the production source and Ansible files
// needed to install Remote Terminal from the standalone LinuxCNC Setup TUI.
package remoteterminalassets

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Keep this list explicit. In particular, embedding the entire web or internal
// directory would also capture local dependency trees, build output, and tests.
//
//go:embed ansible
//go:embed go.mod go.sum
//go:embed cmd/remoteterminal/main.go
//go:embed internal/auth/auth.go internal/auth/pam.go internal/auth/pam_linux.go internal/auth/pam_unavailable.go internal/auth/pam_unsupported.go
//go:embed internal/config/config.go
//go:embed internal/codeservers/manager.go internal/codeservers/proxy.go
//go:embed internal/httpapi/connections.go internal/httpapi/server.go
//go:embed internal/sessions/manager.go
//go:embed web/index.html web/package.json web/package-lock.json web/tsconfig.json web/tsconfig.app.json web/tsconfig.node.json web/vite.config.ts
//go:embed web/src/App.tsx web/src/api.ts web/src/icons.tsx web/src/main.tsx web/src/styles.css web/src/vite-env.d.ts
//go:embed web/src/components/CodeServerPanel.tsx web/src/components/CopySelectionModal.tsx web/src/components/CreateSessionModal.tsx web/src/components/DeleteSessionModal.tsx web/src/components/LaunchCodeServerModal.tsx web/src/components/LoginView.tsx web/src/components/Modal.tsx web/src/components/RenameTabModal.tsx web/src/components/ShutdownCodeServerModal.tsx web/src/components/TerminalPanel.tsx web/src/components/Workspace.tsx
var sourceFiles embed.FS

// Materialize writes the embedded Remote Terminal source tree to a temporary
// directory. The returned source directory contains ansible/install.yml and is
// suitable for the remoteterminal_source_dir Ansible variable.
//
// The caller must invoke cleanup after ansible-playbook exits.
func Materialize() (
	sourceDirectory string,
	installPlaybookPath string,
	cleanup func(),
	err error,
) {
	temporaryBase, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve temporary directory: %w", err)
	}

	directory, err := os.MkdirTemp(temporaryBase, "linuxcncsetup-remoteterminal-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("create Remote Terminal source directory: %w", err)
	}

	var cleanupOnce sync.Once
	removeDirectory := func() {
		cleanupOnce.Do(func() {
			_ = os.RemoveAll(directory)
		})
	}

	if err := copySource(directory); err != nil {
		removeDirectory()
		return "", "", nil, fmt.Errorf("write embedded Remote Terminal source: %w", err)
	}

	if err := makeSourceReadable(directory); err != nil {
		removeDirectory()
		return "", "", nil, err
	}

	return directory, filepath.Join(directory, "ansible", "install.yml"), removeDirectory, nil
}

func copySource(directory string) error {
	return fs.WalkDir(sourceFiles, ".", func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if sourcePath == "." {
			return nil
		}

		destinationPath := filepath.Join(directory, filepath.FromSlash(sourcePath))
		if entry.IsDir() {
			return os.Mkdir(destinationPath, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported embedded source entry: %s", sourcePath)
		}
		return copyFile(sourcePath, destinationPath)
	})
}

func copyFile(sourcePath, destinationPath string) (err error) {
	source, err := sourceFiles.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := destination.Close(); err == nil {
			err = closeErr
		}
	}()

	_, err = io.Copy(destination, source)
	return err
}

func makeSourceReadable(directory string) error {
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symbolic link in embedded source: %s", path)
		}

		mode := fs.FileMode(0o644)
		if entry.IsDir() {
			mode = 0o755
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		return fmt.Errorf("set Remote Terminal source permissions: %w", err)
	}
	return nil
}
