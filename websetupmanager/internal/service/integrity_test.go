//go:build linux

package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"golang.org/x/sys/unix"
)

func TestGetValidationRunIsScopedToTheManagedLibrary(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()

	owned := h.createSetup(t, "Своя проверка", "validation-owned-setup")
	ownedRunID, err := domain.NewValidationRunID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(ctx, `
		INSERT INTO validation_runs(id, setup_id, revision, state, result_json)
		VALUES (?, ?, ?, 'succeeded', '{"issues":[]}')`, ownedRunID, owned.ID, owned.Revision); err != nil {
		t.Fatal(err)
	}
	if run, err := h.service.GetValidationRun(ctx, owned.ID, ownedRunID); err != nil || run.SetupID != owned.ID {
		t.Fatalf("owned validation run = %+v, %v", run, err)
	}

	otherLibraryID, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	otherSetupID, err := domain.NewSetupID()
	if err != nil {
		t.Fatal(err)
	}
	otherRunID, err := domain.NewValidationRunID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(ctx,
		`INSERT INTO library_instances(id, fingerprint) VALUES (?, ?)`,
		otherLibraryID, "foreign-fingerprint-"+otherLibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(ctx,
		`INSERT INTO setups(id, library_id, name) VALUES (?, ?, 'Чужой сетап')`,
		otherSetupID, otherLibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(ctx, `
		INSERT INTO validation_runs(id, setup_id, revision, state, result_json)
		VALUES (?, ?, 1, 'succeeded', '{"issues":[]}')`, otherRunID, otherSetupID); err != nil {
		t.Fatal(err)
	}
	if run, err := h.service.GetValidationRun(ctx, otherSetupID, otherRunID); run != nil ||
		!domain.IsErrorCode(err, domain.CodeValidationNotFound) {
		t.Fatalf("foreign validation run escaped library scope: %+v, %v", run, err)
	}
}

func TestAttentionSurvivesUserMutationsUntilVerified(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Требует внимания", "sticky-attention-create")
	firstID, _, _ := h.attachProgram(t, setup.ID, "first.ngc", []byte("G1 X1\n"), true)
	secondID, _, _ := h.attachProgram(t, setup.ID, "second.ngc", []byte("G1 X2\n"), false)
	const reason = "external integrity sentinel"
	if _, err := h.db.SQL().ExecContext(ctx, `
		UPDATE setups SET status = 'attention', ready_revision = NULL, attention_reason = ?
		 WHERE library_id = ? AND id = ?`, reason, h.service.libraryID, setup.ID); err != nil {
		t.Fatal(err)
	}
	setup, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertAttention := func(operation string, next *domain.Setup, operationErr error) {
		t.Helper()
		if operationErr != nil {
			t.Fatalf("%s: %v", operation, operationErr)
		}
		if next == nil || next.Status != domain.SetupStatusAttention {
			t.Fatalf("%s cleared attention: %+v", operation, next)
		}
		var persisted sql.NullString
		if err := h.db.SQL().QueryRowContext(ctx, `
			SELECT attention_reason FROM setups WHERE library_id = ? AND id = ?`,
			h.service.libraryID, setup.ID).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if !persisted.Valid || persisted.String != reason {
			t.Fatalf("%s changed attention reason to %q", operation, persisted.String)
		}
		setup = next
	}

	next, err := h.service.UpdateSetup(ctx, setup.ID, UpdateSetupInput{
		ExpectedRevision: setup.Revision, Name: "Новые метаданные", Description: "описание",
		IdempotencyKey: "sticky-attention-metadata",
	})
	assertAttention("metadata", next, err)

	extraContent := []byte("G1 X3\n")
	next, err = h.service.AddPrograms(ctx, setup.ID, AddProgramsInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "sticky-attention-add",
		Programs: []UploadArtifactInput{{
			DisplayName: "third.ngc", Content: bytes.NewReader(extraContent), ExpectedSize: int64(len(extraContent)),
		}},
	})
	assertAttention("add programs", next, err)
	third := artifactByName(t, setup, "third.ngc")

	first := artifactByID(t, setup, firstID)
	next, err = h.service.RenameArtifact(ctx, setup.ID, first.ID, RenameArtifactInput{
		ExpectedRevision: setup.Revision, ExpectedVersion: first.Version,
		DisplayName: "renamed.ngc", IdempotencyKey: "sticky-attention-rename",
	})
	assertAttention("rename", next, err)

	second := artifactByID(t, setup, secondID)
	next, err = h.service.SetPrimaryProgram(ctx, setup.ID, second.ID, SetPrimaryInput{
		ExpectedRevision: setup.Revision, ExpectedVersion: second.Version,
		IdempotencyKey: "sticky-attention-primary",
	})
	assertAttention("primary", next, err)

	first = artifactByID(t, setup, firstID)
	replacement := []byte("G1 X11\n")
	next, err = h.service.ReplaceArtifact(ctx, setup.ID, first.ID, ReplaceArtifactInput{
		ExpectedRevision: setup.Revision, ExpectedVersion: first.Version, DisplayName: first.DisplayName,
		Content: bytes.NewReader(replacement), ExpectedSize: int64(len(replacement)),
		IdempotencyKey: "sticky-attention-replace",
	})
	assertAttention("replace", next, err)

	sheet := []byte("%PDF-1.4\n%%EOF\n")
	next, err = h.service.PutSetupSheet(ctx, setup.ID, ReplaceArtifactInput{
		ExpectedRevision: setup.Revision, DisplayName: "sheet.pdf",
		Content: bytes.NewReader(sheet), ExpectedSize: int64(len(sheet)),
		IdempotencyKey: "sticky-attention-sheet",
	})
	assertAttention("setup sheet", next, err)

	third = artifactByID(t, setup, third.ID)
	next, err = h.service.DeleteArtifact(ctx, setup.ID, third.ID, DeleteArtifactInput{
		ExpectedRevision: setup.Revision, ExpectedVersion: third.Version,
		IdempotencyKey: "sticky-attention-delete",
	})
	assertAttention("delete", next, err)
}

func TestPhysicalExpectedVersionGuardsArtifactMutations(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		operation func(context.Context, *lifecycleTestHarness, *domain.Setup, domain.Artifact) (*domain.Setup, error)
	}{
		{
			name: "replace during streaming upload", target: "first.ngc",
			operation: func(ctx context.Context, h *lifecycleTestHarness, setup *domain.Setup, artifact domain.Artifact) (*domain.Setup, error) {
				content := []byte("G1 X99\n")
				record, err := h.service.loadArtifact(ctx, h.db.SQL(), setup.ID, artifact.ID)
				if err != nil {
					return nil, err
				}
				reader := &mutationReader{source: bytes.NewReader(content), mutate: func() error {
					return replaceManagedObjectIdentity(h, record, []byte("G1 X1\n"))
				}}
				return h.service.ReplaceArtifact(ctx, setup.ID, artifact.ID, ReplaceArtifactInput{
					ExpectedRevision: setup.Revision, ExpectedVersion: artifact.Version,
					DisplayName: artifact.DisplayName, Content: reader, ExpectedSize: int64(len(content)),
					IdempotencyKey: "physical-version-replace",
				})
			},
		},
		{
			name: "rename", target: "first.ngc",
			operation: func(ctx context.Context, h *lifecycleTestHarness, setup *domain.Setup, artifact domain.Artifact) (*domain.Setup, error) {
				return h.service.RenameArtifact(ctx, setup.ID, artifact.ID, RenameArtifactInput{
					ExpectedRevision: setup.Revision, ExpectedVersion: artifact.Version,
					DisplayName: "renamed.ngc", IdempotencyKey: "physical-version-rename",
				})
			},
		},
		{
			name: "delete", target: "second.ngc",
			operation: func(ctx context.Context, h *lifecycleTestHarness, setup *domain.Setup, artifact domain.Artifact) (*domain.Setup, error) {
				return h.service.DeleteArtifact(ctx, setup.ID, artifact.ID, DeleteArtifactInput{
					ExpectedRevision: setup.Revision, ExpectedVersion: artifact.Version,
					IdempotencyKey: "physical-version-delete",
				})
			},
		},
		{
			name: "set primary", target: "second.ngc",
			operation: func(ctx context.Context, h *lifecycleTestHarness, setup *domain.Setup, artifact domain.Artifact) (*domain.Setup, error) {
				return h.service.SetPrimaryProgram(ctx, setup.ID, artifact.ID, SetPrimaryInput{
					ExpectedRevision: setup.Revision, ExpectedVersion: artifact.Version,
					IdempotencyKey: "physical-version-primary",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLifecycleTestHarness(t, nil)
			ctx := context.Background()
			setup := h.createSetup(t, "Физическая версия", "physical-version-create-"+test.target)
			h.attachProgram(t, setup.ID, "first.ngc", []byte("G1 X1\n"), true)
			h.attachProgram(t, setup.ID, "second.ngc", []byte("G1 X2\n"), false)
			setup, err := h.service.GetSetup(ctx, setup.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeArtifacts := append([]domain.Artifact(nil), setup.Artifacts...)
			beforeRefs := storageRefCounts(t, h, setup.ID)
			target := artifactByName(t, setup, test.target)

			if test.name != "replace during streaming upload" {
				record, err := h.service.loadArtifact(ctx, h.db.SQL(), setup.ID, target.ID)
				if err != nil {
					t.Fatal(err)
				}
				original := []byte("G1 X1\n")
				if test.target == "second.ngc" {
					original = []byte("G1 X2\n")
				}
				if err := replaceManagedObjectIdentity(h, record, original); err != nil {
					t.Fatal(err)
				}
			}

			result, operationErr := test.operation(ctx, h, setup, target)
			if result != nil || !domain.IsErrorCode(operationErr, domain.CodeArtifactChanged) {
				t.Fatalf("mutation result = %+v, %v", result, operationErr)
			}
			after, err := h.service.GetSetup(ctx, setup.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Revision != setup.Revision || after.Status != domain.SetupStatusAttention {
				t.Fatalf("setup state/revision = %s/%d, want attention/%d", after.Status, after.Revision, setup.Revision)
			}
			if !reflect.DeepEqual(after.Artifacts, beforeArtifacts) {
				t.Fatalf("failed mutation changed artifact refs:\nbefore=%+v\nafter=%+v", beforeArtifacts, after.Artifacts)
			}
			if afterRefs := storageRefCounts(t, h, setup.ID); !reflect.DeepEqual(afterRefs, beforeRefs) {
				t.Fatalf("failed mutation changed ref counts: before=%v after=%v", beforeRefs, afterRefs)
			}
		})
	}
}

func TestAttentionRepairRequiresFullObjectVerification(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Проверяемое восстановление", "verified-repair-create")
	h.attachProgram(t, setup.ID, "repair.ngc", []byte("G1 X1\n"), true)
	if _, err := h.db.SQL().ExecContext(ctx, `
		UPDATE setups SET status = 'attention', attention_reason = 'previous external change'
		 WHERE library_id = ? AND id = ?`, h.service.libraryID, setup.ID); err != nil {
		t.Fatal(err)
	}
	identity, err := h.service.InspectManagedContent(ctx)
	if err != nil || identity.SetupsRecovered != 0 {
		t.Fatalf("identity inspection repaired attention: %+v, %v", identity, err)
	}
	stillAttention, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || stillAttention.Status != domain.SetupStatusAttention {
		t.Fatalf("state after identity inspection = %+v, %v", stillAttention, err)
	}
	verified, err := h.service.Reconcile(ctx)
	if err != nil || verified.SetupsRecovered != 1 {
		t.Fatalf("full verification did not repair attention: %+v, %v", verified, err)
	}
	repaired, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || repaired.Status != domain.SetupStatusDraft {
		t.Fatalf("state after full verification = %+v, %v", repaired, err)
	}
}

func TestFullReconcileRebindsIdenticalColdCopyWithoutClearingAttention(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Cold-copy recovery", "cold-copy-create")
	artifactID, object, _ := h.attachProgram(t, setup.ID, "restored.ngc", []byte("G1 X42\n"), true)
	h.markReady(t, setup.ID, setup.Revision)

	record, err := h.service.loadArtifact(ctx, h.db.SQL(), setup.ID, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceManagedObjectIdentity(h, record, []byte("G1 X42\n")); err != nil {
		t.Fatal(err)
	}
	if result, err := h.service.InspectManagedContent(ctx); err != nil || result.SetupsAttention != 1 {
		t.Fatalf("identity pass = %+v, %v", result, err)
	}
	if result, err := h.service.Reconcile(ctx); err != nil || result.SetupsAttention != 1 {
		t.Fatalf("full rebind pass = %+v, %v", result, err)
	}

	rebound, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Status != domain.SetupStatusAttention || len(rebound.Artifacts) != 1 ||
		rebound.Artifacts[0].Version == object.Version {
		t.Fatalf("rebound setup = %+v", rebound)
	}
	content, err := h.service.ReadArtifactRange(
		ctx, setup.ID, artifactID, rebound.Artifacts[0].Version, 0, rebound.Artifacts[0].ByteSize,
	)
	if err != nil || string(content.Data) != "G1 X42\n" {
		t.Fatalf("rebound content = %q, %v", content.Data, err)
	}
}

type mutationReader struct {
	source io.Reader
	mutate func() error
	once   sync.Once
	err    error
}

func (r *mutationReader) Read(buffer []byte) (int, error) {
	count, readErr := r.source.Read(buffer)
	if count > 0 {
		r.once.Do(func() { r.err = r.mutate() })
		if r.err != nil {
			return count, r.err
		}
	}
	return count, readErr
}

func replaceManagedObjectIdentity(h *lifecycleTestHarness, record *artifactRecord, content []byte) error {
	if err := h.store.RemoveObject(record.StorageKey, record.SHA256); err != nil {
		return err
	}
	file, err := h.roots.OpenLibrary(record.StorageKey, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	object, err := h.store.InspectObject(record.StorageKey, record.SHA256, "")
	if err != nil {
		return err
	}
	if object.Version == record.Version {
		return errors.New("replacement unexpectedly retained the old physical version")
	}
	return nil
}

func storageRefCounts(t *testing.T, h *lifecycleTestHarness, setupID string) map[string]int64 {
	t.Helper()
	rows, err := h.db.SQL().QueryContext(context.Background(), `
		SELECT o.id, o.ref_count
		  FROM storage_objects o
		 WHERE o.id IN (SELECT storage_object_id FROM setup_artifacts WHERE setup_id = ?)
		 ORDER BY o.id`, setupID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make(map[string]int64)
	for rows.Next() {
		var id string
		var count int64
		if err := rows.Scan(&id, &count); err != nil {
			t.Fatal(err)
		}
		result[id] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func artifactByName(t *testing.T, setup *domain.Setup, name string) domain.Artifact {
	t.Helper()
	for _, artifact := range setup.Artifacts {
		if artifact.DisplayName == name {
			return artifact
		}
	}
	t.Fatalf("artifact %q was not found in %+v", name, setup.Artifacts)
	return domain.Artifact{}
}

func artifactByID(t *testing.T, setup *domain.Setup, id string) domain.Artifact {
	t.Helper()
	for _, artifact := range setup.Artifacts {
		if artifact.ID == id {
			return artifact
		}
	}
	t.Fatalf("artifact %q was not found in %+v", id, setup.Artifacts)
	return domain.Artifact{}
}
