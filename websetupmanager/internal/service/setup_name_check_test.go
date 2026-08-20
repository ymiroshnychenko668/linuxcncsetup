//go:build linux

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func TestFindSetupNameMatchUsesCanonicalFoldBeyondLibraryPages(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()

	// These names all match a broad library search for sigma and sort before
	// the exact match. This guards against reimplementing the warning through a
	// bounded UI page.
	tx, err := h.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 125; index++ {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO setups(id, library_id, name, status, revision, source)
			VALUES (?, ?, ?, 'draft', ?, 'created')`,
			fmt.Sprintf("stp_filler_%03d", index), h.service.libraryID,
			fmt.Sprintf("%03d-Σ", index), domain.InitialRevision); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	target := h.createSetup(t, "ς", "unicode-name-match-create")

	match, err := h.service.FindSetupNameMatch(ctx, "Σ")
	if err != nil {
		t.Fatal(err)
	}
	if match == nil || match.SetupID != target.ID || match.Name != "ς" {
		t.Fatalf("match = %+v, want %s", match, target.ID)
	}
	missing, err := h.service.FindSetupNameMatch(ctx, "Нет совпадения")
	if err != nil || missing != nil {
		t.Fatalf("missing = %+v, %v", missing, err)
	}
}
