//go:build linux

package service

import (
	"context"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func TestImportPreflightUsesPersistenceUnicodeCaseFold(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	result, err := h.service.PreflightImport(context.Background(), ImportPreflightInput{Items: []ImportPreflightItem{
		{ClientID: "sharp-left", Role: domain.ArtifactRoleProgram, DisplayName: "Straße.ngc"},
		{ClientID: "sharp-right", Role: domain.ArtifactRoleProgram, DisplayName: "STRASSE.NGC"},
		{ClientID: "sigma-left", Role: domain.ArtifactRoleProgram, DisplayName: "μέρος-Σ.ngc"},
		{ClientID: "sigma-right", Role: domain.ArtifactRoleProgram, DisplayName: "μέρος-ς.NGC"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Collisions) != 2 {
		t.Fatalf("collisions = %+v", result.Collisions)
	}
	want := map[string]bool{
		"sharp-left,sharp-right": true,
		"sigma-left,sigma-right": true,
	}
	for _, collision := range result.Collisions {
		key := collision.ClientIDs[0] + "," + collision.ClientIDs[1]
		if !want[key] {
			t.Fatalf("unexpected collision %+v", collision)
		}
	}
}
