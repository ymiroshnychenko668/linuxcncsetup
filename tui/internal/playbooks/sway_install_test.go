package playbooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwayWaybarTemplateHidesVolume(t *testing.T) {
	playbookPath, cleanup, err := Materialize(InstallSway)
	if err != nil {
		t.Fatalf("Materialize() error: %v", err)
	}
	t.Cleanup(cleanup)

	templatePath := filepath.Join(
		filepath.Dir(playbookPath),
		"templates",
		"waybar-config.json.j2",
	)
	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read Waybar template: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse Waybar template: %v", err)
	}
	if _, exists := config["pulseaudio"]; exists {
		t.Fatal("Waybar template still defines the pulseaudio volume module")
	}

	modules, ok := config["modules-right"].([]any)
	if !ok {
		t.Fatalf("Waybar modules-right is not an array: %#v", config["modules-right"])
	}
	for _, module := range modules {
		if module == "pulseaudio" || module == "wireplumber" {
			t.Fatalf("Waybar still displays an audio module: %q", module)
		}
	}
}

func TestSwayTemplateDefersMetadataUntilAfterTargetUserValidation(t *testing.T) {
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

	templateBlock := swayTaskBlockBetween(
		t,
		tasks,
		"- name: Install the Sway desktop configuration",
		"- name: Enforce Sway configuration ownership and permissions",
	)
	for _, forbidden := range []string{
		"\n        owner:",
		"\n        group:",
		"\n        mode:",
	} {
		if strings.Contains(templateBlock, forbidden) {
			t.Errorf(
				"target-user template task mutates its root-staged source with %q",
				strings.TrimSpace(forbidden),
			)
		}
	}

	metadataBlock := swayTaskBlockBetween(
		t,
		tasks,
		"- name: Enforce Sway configuration ownership and permissions",
		"- name: Validate the installed Sway configuration and included snippets",
	)
	for _, expected := range []string{
		`owner: "{{ target_user }}"`,
		`group: "{{ sway_target_group_result.stdout }}"`,
		`mode: "0644"`,
	} {
		if !strings.Contains(metadataBlock, expected) {
			t.Errorf("root metadata task does not contain %q", expected)
		}
	}
	if strings.Contains(metadataBlock, "become_user:") {
		t.Fatal("Sway metadata enforcement unexpectedly runs as an unprivileged user")
	}
}

func swayTaskBlockBetween(
	t *testing.T,
	content string,
	startMarker string,
	endMarker string,
) string {
	t.Helper()
	start := strings.Index(content, startMarker)
	if start < 0 {
		t.Fatalf("Sway tasks do not contain start marker %q", startMarker)
	}
	endRelative := strings.Index(content[start:], endMarker)
	if endRelative < 0 {
		t.Fatalf(
			"Sway tasks do not contain end marker %q after %q",
			endMarker,
			startMarker,
		)
	}
	return content[start : start+endRelative]
}
