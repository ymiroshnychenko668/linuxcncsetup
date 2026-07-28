package ui

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

const linuxCNCConfigDirectoryEnvironment = "LINUXCNC_CONFIG_DIR"

type linuxCNCConfig struct {
	label string
	path  string
}

type linuxCNCAutostartDesktop int

const (
	linuxCNCDesktopSway linuxCNCAutostartDesktop = iota
	linuxCNCDesktopXFCE
)

func (desktop linuxCNCAutostartDesktop) configureAction() sectionAction {
	if desktop == linuxCNCDesktopXFCE {
		return actionConfigureLinuxCNCAutostartX11
	}
	return actionConfigureLinuxCNCAutostartSway
}

func (desktop linuxCNCAutostartDesktop) openerAction() sectionAction {
	if desktop == linuxCNCDesktopXFCE {
		return actionOpenLinuxCNCAutostartX11
	}
	return actionOpenLinuxCNCAutostartSway
}

func (desktop linuxCNCAutostartDesktop) pageTitle() string {
	if desktop == linuxCNCDesktopXFCE {
		return "XFCE X11 configuration"
	}
	return "Sway configuration"
}

func (desktop linuxCNCAutostartDesktop) configDescription(configPath string) string {
	if desktop == linuxCNCDesktopXFCE {
		return fmt.Sprintf(
			"Start this LinuxCNC configuration automatically on XFCE X11 workspace 1.\n\nPath:\n%s",
			configPath,
		)
	}
	return fmt.Sprintf(
		"Start this LinuxCNC configuration automatically on Sway workspace 1.\n\nPath:\n%s",
		configPath,
	)
}

func discoverLinuxCNCConfigs() ([]linuxCNCConfig, string, error) {
	root, err := linuxCNCConfigDirectory()
	if err != nil {
		return nil, "", err
	}

	rootInfo, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, root, nil
		}
		return nil, root, fmt.Errorf("inspect %s: %w", root, err)
	}
	if !rootInfo.IsDir() {
		return nil, root, fmt.Errorf("%s is not a directory", root)
	}

	var configs []linuxCNCConfig
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".ini") {
			return nil
		}

		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", path, err)
		}
		configs = append(configs, linuxCNCConfig{
			label: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			path:  filepath.Clean(absolutePath),
		})
		return nil
	})
	if err != nil {
		return nil, root, fmt.Errorf("search %s: %w", root, err)
	}

	sort.Slice(configs, func(left, right int) bool {
		if configs[left].label == configs[right].label {
			return configs[left].path < configs[right].path
		}
		return configs[left].label < configs[right].label
	})
	return configs, root, nil
}

func linuxCNCConfigDirectory() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(linuxCNCConfigDirectoryEnvironment)); configured != "" {
		absolutePath, err := filepath.Abs(configured)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", configured, err)
		}
		return filepath.Clean(absolutePath), nil
	}

	username, err := targetUsername()
	if err != nil {
		return "", err
	}
	account, err := user.Lookup(username)
	if err != nil {
		return "", fmt.Errorf("look up %s: %w", username, err)
	}
	if account.HomeDir == "" || !filepath.IsAbs(account.HomeDir) {
		return "", fmt.Errorf("%s has no absolute home directory", username)
	}
	return filepath.Join(account.HomeDir, "linuxcnc", "configs"), nil
}

func linuxCNCConfigSections(
	configs []linuxCNCConfig,
	desktop linuxCNCAutostartDesktop,
) []section {
	sections := make([]section, 0, len(configs)+1)
	for _, config := range configs {
		sections = append(sections, section{
			title:       config.label,
			description: desktop.configDescription(config.path),
			action:      desktop.configureAction(),
			value:       config.path,
		})
	}
	sections = append(sections, section{
		title:       "← Back",
		description: "Return to the desktop selection.",
		action:      actionBack,
	})
	return sections
}

func validateLinuxCNCConfig(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("configuration path must be absolute")
	}
	if !strings.EqualFold(filepath.Ext(path), ".ini") {
		return fmt.Errorf("configuration must be an .ini file")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("configuration is not a regular file")
	}
	return nil
}
