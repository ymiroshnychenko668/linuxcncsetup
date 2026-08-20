//go:build linux

package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
	"golang.org/x/sys/unix"
)

type lifecycleTestHarness struct {
	service *Service
	db      *database.DB
	roots   *storage.Roots
	store   *storage.Store
	clockNS atomic.Int64
}

func newLifecycleTestHarness(t *testing.T, configure func(*Options)) *lifecycleTestHarness {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	libraryDir := root + "/library"
	stateDir := root + "/state"
	if err := os.Mkdir(libraryDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	roots, err := storage.NewRoots(libraryDir, stateDir, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, stateDir)
	if err != nil {
		roots.Close()
		t.Fatal(err)
	}
	if err := db.EnsureLibrary(ctx, roots.LibraryID(), roots.LibraryFingerprint()); err != nil {
		db.Close()
		roots.Close()
		t.Fatal(err)
	}
	store, err := storage.NewStore(roots, storage.StoreOptions{})
	if err != nil {
		db.Close()
		roots.Close()
		t.Fatal(err)
	}
	options := Options{
		Database: db, Objects: store, LibraryID: roots.LibraryID(),
		GCodeExtensions: []string{".ngc", ".nc"}, RecentLimit: 30,
	}
	if configure != nil {
		configure(&options)
	}
	manager, err := New(options)
	if err != nil {
		db.Close()
		roots.Close()
		t.Fatal(err)
	}
	harness := &lifecycleTestHarness{service: manager, db: db, roots: roots, store: store}
	harness.clockNS.Store(time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC).UnixNano())
	manager.now = func() time.Time {
		return time.Unix(0, harness.clockNS.Add(int64(time.Millisecond))).UTC()
	}
	t.Cleanup(func() {
		manager.Close()
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
		if err := roots.Close(); err != nil {
			t.Errorf("close test roots: %v", err)
		}
	})
	return harness
}

func (h *lifecycleTestHarness) createSetup(t *testing.T, name, key string) *domain.Setup {
	t.Helper()
	setup, err := h.service.CreateSetup(context.Background(), CreateSetupInput{Name: name, IdempotencyKey: key})
	if err != nil {
		t.Fatalf("CreateSetup(%q): %v", name, err)
	}
	return setup
}

func (h *lifecycleTestHarness) attachProgram(
	t *testing.T, setupID, name string, content []byte, primary bool,
) (string, *storage.Object, string) {
	t.Helper()
	ctx := context.Background()
	staged, err := h.store.Stage(ctx, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	object, err := h.store.Publish(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	objectID, err := domain.NewStorageObjectID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(ctx, `
		INSERT INTO storage_objects(
			id, library_id, storage_key, media_type, byte_size, sha256
		) VALUES (?, ?, ?, 'text/x-gcode', ?, ?)`,
		objectID, h.service.libraryID, object.Key, object.Size, object.SHA256); err != nil {
		// Content-addressed fixtures may intentionally reuse a published file.
		if err := h.db.SQL().QueryRowContext(ctx, `
			SELECT id FROM storage_objects WHERE library_id = ? AND storage_key = ?`,
			h.service.libraryID, object.Key).Scan(&objectID); err != nil {
			t.Fatal(err)
		}
	}
	artifactID := h.attachExistingProgram(t, setupID, objectID, object, name, primary)
	return artifactID, object, objectID
}

func (h *lifecycleTestHarness) attachExistingProgram(
	t *testing.T, setupID, objectID string, object *storage.Object, name string, primary bool,
) string {
	t.Helper()
	artifactID, err := domain.NewArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	name, err = domain.NormalizeArtifactName(name)
	if err != nil {
		t.Fatal(err)
	}
	nameKey, err := domain.ArtifactNameKey(name)
	if err != nil {
		t.Fatal(err)
	}
	var position int
	if err := h.db.SQL().QueryRowContext(context.Background(), `
		SELECT count(*) FROM setup_artifacts WHERE setup_id = ? AND role = 'program'`,
		setupID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(context.Background(), `
		INSERT INTO setup_artifacts(
			id, setup_id, role, display_name, normalized_name, storage_object_id,
			position, is_primary, identity_device, identity_inode, identity_size,
			identity_mtime_ns, identity_ctime_ns, object_version
		) VALUES (?, ?, 'program', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactID, setupID, name, nameKey, objectID, position, boolInteger(primary),
		int64(object.Identity.Device), int64(object.Identity.Inode), object.Size,
		object.Identity.ModTimeNS, object.Identity.ChangeTimeNS, object.Version); err != nil {
		t.Fatal(err)
	}
	return artifactID
}

func (h *lifecycleTestHarness) attachExistingSheet(
	t *testing.T, setupID, objectID string, object *storage.Object, name string,
) string {
	t.Helper()
	artifactID, err := domain.NewArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	name, err = domain.NormalizeArtifactName(name)
	if err != nil {
		t.Fatal(err)
	}
	nameKey, err := domain.ArtifactNameKey(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(context.Background(), `
		INSERT INTO setup_artifacts(
			id, setup_id, role, display_name, normalized_name, storage_object_id,
			position, is_primary, identity_device, identity_inode, identity_size,
			identity_mtime_ns, identity_ctime_ns, object_version
		) VALUES (?, ?, 'setup_sheet', ?, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?)`,
		artifactID, setupID, name, nameKey, objectID,
		int64(object.Identity.Device), int64(object.Identity.Inode), object.Size,
		object.Identity.ModTimeNS, object.Identity.ChangeTimeNS, object.Version); err != nil {
		t.Fatal(err)
	}
	return artifactID
}

func (h *lifecycleTestHarness) markReady(t *testing.T, setupID string, revision domain.Revision) {
	t.Helper()
	result, err := h.db.SQL().ExecContext(context.Background(), `
		UPDATE setups SET status = 'ready', ready_revision = ?
		 WHERE id = ? AND library_id = ? AND revision = ?`,
		revision, setupID, h.service.libraryID, revision)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("mark ready changed %d rows", rows)
	}
}

func TestSetupLifecycleCreateListSearchUpdateAndIdempotency(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()

	first, err := h.service.CreateSetup(ctx, CreateSetupInput{
		Name: "Пресс", Description: "Чистовая линия", IdempotencyKey: "create-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.SetupStatusDraft || first.Revision != domain.InitialRevision ||
		!domain.IsValidID(first.ID) || first.Source != domain.SetupSourceCreated || len(first.Artifacts) != 0 {
		t.Fatalf("unexpected created setup: %+v", first)
	}
	replayed, err := h.service.CreateSetup(ctx, CreateSetupInput{
		Name: "Пресс", Description: "Чистовая линия", IdempotencyKey: "create-first",
	})
	if err != nil || replayed.ID != first.ID || !replayed.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("create replay = %+v, %v", replayed, err)
	}
	duplicate := h.createSetup(t, "Пресс", "create-duplicate-name")
	third := h.createSetup(t, "Сварка", "create-third")
	_, thirdObject, thirdObjectID := h.attachProgram(t, third.ID, "Финиш.НГЦ", []byte("G1 X1\n"), true)
	h.attachExistingSheet(t, third.ID, thirdObjectID, thirdObject, "карта.pdf")
	h.markReady(t, third.ID, third.Revision)
	if _, err := h.service.SetCurrentSetup(ctx, SetCurrentInput{
		SetupID: third.ID, ExpectedRevision: third.Revision,
		Confirmed: true, IdempotencyKey: "list-current",
	}); err != nil {
		t.Fatal(err)
	}

	page, err := h.service.ListSetups(ctx, ListSetupsOptions{Query: "ПРЕСС", Sort: "name", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	secondPage, err := h.service.ListSetups(ctx, ListSetupsOptions{
		Query: "ПРЕСС", Sort: "name", Limit: 1, Cursor: page.NextCursor,
	})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID == page.Items[0].ID {
		t.Fatalf("second page = %+v, %v", secondPage, err)
	}
	if _, err := h.service.ListSetups(ctx, ListSetupsOptions{
		Query: "другая", Sort: "name", Limit: 1, Cursor: page.NextCursor,
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("query-bound cursor error = %v", err)
	}
	programMatch, err := h.service.ListSetups(ctx, ListSetupsOptions{Query: "фИНИШ", Limit: 10})
	if err != nil || len(programMatch.Items) != 1 || programMatch.Items[0].ID != third.ID {
		t.Fatalf("program-name search = %+v, %v", programMatch, err)
	}
	yes, no := true, false
	withSheet, err := h.service.ListSetups(ctx, ListSetupsOptions{HasSetupSheet: &yes, Limit: 10})
	if err != nil || len(withSheet.Items) != 1 || withSheet.Items[0].ID != third.ID {
		t.Fatalf("setup-sheet filter = %+v, %v", withSheet, err)
	}
	withoutSheet, err := h.service.ListSetups(ctx, ListSetupsOptions{HasSetupSheet: &no, Limit: 10})
	if err != nil || len(withoutSheet.Items) != 2 {
		t.Fatalf("without-sheet filter = %+v, %v", withoutSheet, err)
	}
	currentOnly, err := h.service.ListSetups(ctx, ListSetupsOptions{Current: &yes, Limit: 10})
	if err != nil || len(currentOnly.Items) != 1 || currentOnly.Items[0].ID != third.ID {
		t.Fatalf("current filter = %+v, %v", currentOnly, err)
	}
	if _, err := h.service.ListSetups(ctx, ListSetupsOptions{
		Query: "ПРЕСС", Sort: "name", Limit: 1, Cursor: page.NextCursor, Current: &no,
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("filter-bound cursor error = %v", err)
	}

	updated, err := h.service.UpdateSetup(ctx, first.ID, UpdateSetupInput{
		ExpectedRevision: first.Revision, Name: "Пресс 2", Description: "Новая карточка",
		IdempotencyKey: "update-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != first.ID || updated.Revision != first.Revision+1 || updated.Name != "Пресс 2" {
		t.Fatalf("updated setup = %+v", updated)
	}
	updateReplay, err := h.service.UpdateSetup(ctx, first.ID, UpdateSetupInput{
		ExpectedRevision: first.Revision, Name: "Пресс 2", Description: "Новая карточка",
		IdempotencyKey: "update-first",
	})
	if err != nil || updateReplay.Revision != updated.Revision {
		t.Fatalf("update replay = %+v, %v", updateReplay, err)
	}
	stale := UpdateSetupInput{
		ExpectedRevision: first.Revision, Name: "Не перезаписать", IdempotencyKey: "stale-update",
	}
	if _, err := h.service.UpdateSetup(ctx, first.ID, stale); !domain.IsErrorCode(err, domain.CodeRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	if _, err := h.service.UpdateSetup(ctx, first.ID, stale); !domain.IsErrorCode(err, domain.CodeRevisionConflict) {
		t.Fatalf("stale update replay error = %v", err)
	}
	got, err := h.service.GetSetup(ctx, first.ID)
	if err != nil || got.Name != "Пресс 2" || got.Revision != updated.Revision {
		t.Fatalf("GetSetup after conflict = %+v, %v", got, err)
	}
	if duplicate.ID == first.ID {
		t.Fatal("duplicate display names reused the stable ID")
	}
}

func TestListSetupsTenThousandRowsUsesBoundedSQLPagination(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	tx, err := h.db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO setups(id, library_id, name, description, updated_at)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	const total = 10_000
	for index := range total {
		description := "обычная карточка"
		if index == total-1 {
			description = "УНИКАЛЬНАЯ игла поиска"
		}
		if _, err := statement.ExecContext(ctx,
			fmt.Sprintf("%032x", index+1), h.service.libraryID,
			fmt.Sprintf("Setup %05d", index), description,
			sqlTimestamp(time.Date(2026, 1, 1, 0, 0, index%60, 0, time.UTC)),
		); err != nil {
			statement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A one-connection pool would deadlock the previous per-row/N+1 search.
	// Keeping it at one also makes this test assert that filtering and paging
	// are executed by the single bounded SQL statement.
	h.db.SQL().SetMaxOpenConns(1)
	started := time.Now()
	match, err := h.service.ListSetups(ctx, ListSetupsOptions{Query: "уникальная ИГЛА", Limit: 25})
	if err != nil || len(match.Items) != 1 || match.Items[0].Name != "Setup 09999" {
		t.Fatalf("10k Unicode search = %+v, %v", match, err)
	}
	// Race instrumentation intentionally slows modernc SQLite substantially;
	// retain the production performance budget in normal tests while keeping
	// the full functional query under the race detector.
	if elapsed := time.Since(started); elapsed > 5*time.Second*time.Duration(raceTestSlowdown) {
		t.Fatalf("10k search took %s", elapsed)
	}
	firstPage, err := h.service.ListSetups(ctx, ListSetupsOptions{Sort: "name", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 25 || firstPage.NextCursor == "" {
		t.Fatalf("10k first page = %d items, cursor=%q", len(firstPage.Items), firstPage.NextCursor)
	}
	secondPage, err := h.service.ListSetups(ctx, ListSetupsOptions{
		Sort: "name", Limit: 25, Cursor: firstPage.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 25 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("10k second page = %+v, %v", secondPage, err)
	}
}

func TestLifecycleSavepointPreventsPartialCreateOnAuditFailure(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	if _, err := h.db.SQL().ExecContext(ctx, `
		CREATE TRIGGER lifecycle_test_fail_audit
		BEFORE INSERT ON audit_events WHEN NEW.operation = 'create'
		BEGIN SELECT RAISE(ABORT, 'test audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	input := CreateSetupInput{Name: "Не частичный", IdempotencyKey: "failed-create"}
	if _, err := h.service.CreateSetup(ctx, input); !domain.IsErrorCode(err, domain.CodeDatabaseUnavailable) {
		t.Fatalf("create failure = %v", err)
	}
	var setupCount int
	if err := h.db.SQL().QueryRowContext(ctx,
		"SELECT count(*) FROM setups WHERE library_id = ?", h.service.libraryID,
	).Scan(&setupCount); err != nil {
		t.Fatal(err)
	}
	if setupCount != 0 {
		t.Fatalf("failed create left %d visible setups", setupCount)
	}
	if _, err := h.service.CreateSetup(ctx, input); !domain.IsErrorCode(err, domain.CodeDatabaseUnavailable) {
		t.Fatalf("failed create replay = %v", err)
	}
}

func TestCurrentSelectionPersistsAcrossMutationAndRequiresExplicitClear(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Текущий", "current-create")
	h.attachProgram(t, setup.ID, "main.ngc", []byte("G0 X0\n"), true)
	h.markReady(t, setup.ID, setup.Revision)

	input := SetCurrentInput{
		SetupID: setup.ID, ExpectedRevision: setup.Revision,
		Confirmed: true, IdempotencyKey: "select-current",
	}
	if _, err := h.service.SetCurrentSetup(ctx, SetCurrentInput{
		SetupID: setup.ID, ExpectedRevision: setup.Revision,
		Confirmed: false, IdempotencyKey: "unconfirmed-current",
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("unconfirmed selection error = %v", err)
	}
	selected, err := h.service.SetCurrentSetup(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := h.service.SetCurrentSetup(ctx, input)
	if err != nil || !replayed.SelectedAt.Equal(selected.SelectedAt) {
		t.Fatalf("selection replay = %+v, %v", replayed, err)
	}

	updated, err := h.service.UpdateSetup(ctx, setup.ID, UpdateSetupInput{
		ExpectedRevision: setup.Revision, Name: "Текущий изменён", IdempotencyKey: "mutate-current",
	})
	if err != nil || updated.Status != domain.SetupStatusDraft {
		t.Fatalf("mutate current = %+v, %v", updated, err)
	}
	current, err := h.service.GetCurrentSetup(ctx)
	if err != nil || current == nil || current.SetupID != setup.ID || current.RevisionSelected != setup.Revision {
		t.Fatalf("current after mutation = %+v, %v", current, err)
	}
	archiveInput := ArchiveInput{ExpectedRevision: updated.Revision, IdempotencyKey: "archive-current"}
	if _, err := h.service.ArchiveSetup(ctx, setup.ID, archiveInput); !domain.IsErrorCode(err, domain.CodeCurrentSetupConflict) {
		t.Fatalf("archive current error = %v", err)
	}
	clearInput := ClearCurrentInput{
		ExpectedSetupID: setup.ID, ExpectedRevision: setup.Revision,
		Confirmed: true, IdempotencyKey: "clear-current",
	}
	if err := h.service.ClearCurrentSetup(ctx, clearInput); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ClearCurrentSetup(ctx, clearInput); err != nil {
		t.Fatalf("clear replay: %v", err)
	}
	if current, err := h.service.GetCurrentSetup(ctx); err != nil || current != nil {
		t.Fatalf("current after clear = %+v, %v", current, err)
	}
	archived, err := h.service.ArchiveSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: updated.Revision, IdempotencyKey: "archive-after-clear",
	})
	if err != nil || archived.Status != domain.SetupStatusArchived || archived.Revision != updated.Revision {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	defaultLibrary, err := h.service.ListSetups(ctx, ListSetupsOptions{Query: "Текущий", Limit: 10})
	if err != nil || len(defaultLibrary.Items) != 0 {
		t.Fatalf("archived setup leaked into default library = %+v, %v", defaultLibrary, err)
	}
	archivedLibrary, err := h.service.ListSetups(ctx, ListSetupsOptions{
		Query: "Текущий", Statuses: []domain.SetupStatus{domain.SetupStatusArchived}, Limit: 10,
	})
	if err != nil || len(archivedLibrary.Items) != 1 || archivedLibrary.Items[0].ID != setup.ID {
		t.Fatalf("archived filter = %+v, %v", archivedLibrary, err)
	}
	restored, err := h.service.RestoreSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: archived.Revision, IdempotencyKey: "restore-current",
	})
	if err != nil || restored.Status != domain.SetupStatusDraft || restored.ID != setup.ID {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	events, err := h.service.ListAuditEvents(ctx, setup.ID, 20)
	if err != nil || len(events) < 5 {
		t.Fatalf("audit events = %+v, %v", events, err)
	}
}

func TestCurrentSelectionRejectsStaleClearAndReplacement(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	ready := func(name, key string) *domain.Setup {
		setup := h.createSetup(t, name, key)
		h.attachProgram(t, setup.ID, name+".ngc", []byte("G0 X0\n"), true)
		h.markReady(t, setup.ID, setup.Revision)
		return setup
	}
	first := ready("Первый", "current-race-first")
	second := ready("Второй", "current-race-second")
	third := ready("Третий", "current-race-third")
	selectedFirst, err := h.service.SetCurrentSetup(ctx, SetCurrentInput{
		SetupID: first.ID, ExpectedRevision: first.Revision,
		Confirmed: true, IdempotencyKey: "select-race-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.SetCurrentSetup(ctx, SetCurrentInput{
		SetupID: second.ID, ExpectedRevision: second.Revision,
		ExpectedPreviousSetupID: first.ID, ExpectedPreviousRevision: first.Revision,
		Confirmed: true, IdempotencyKey: "select-race-second",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ClearCurrentSetup(ctx, ClearCurrentInput{
		ExpectedSetupID: selectedFirst.SetupID, ExpectedRevision: selectedFirst.RevisionSelected,
		Confirmed: true, IdempotencyKey: "stale-clear-current",
	}); !domain.IsErrorCode(err, domain.CodeCurrentSetupConflict) {
		t.Fatalf("stale clear error = %v", err)
	}
	if _, err := h.service.SetCurrentSetup(ctx, SetCurrentInput{
		SetupID: third.ID, ExpectedRevision: third.Revision,
		ExpectedPreviousSetupID: first.ID, ExpectedPreviousRevision: first.Revision,
		Confirmed: true, IdempotencyKey: "stale-replace-current",
	}); !domain.IsErrorCode(err, domain.CodeCurrentSetupConflict) {
		t.Fatalf("stale replacement error = %v", err)
	}
	current, err := h.service.GetCurrentSetup(ctx)
	if err != nil || current == nil || current.SetupID != second.ID {
		t.Fatalf("newer current selection changed by stale action: %+v, %v", current, err)
	}
}

func TestRecentAndUIStateAreBoundedStableIDState(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.RecentLimit = 2 })
	ctx := context.Background()
	first := h.createSetup(t, "Первый", "recent-first")
	second := h.createSetup(t, "Второй", "recent-second")
	third := h.createSetup(t, "Третий", "recent-third")
	artifactID, _, _ := h.attachProgram(t, second.ID, "recent.ngc", []byte("G1\n"), true)

	if err := h.service.TouchRecentSetup(ctx, first.ID, "", 0, "recent-touch-first"); err != nil {
		t.Fatal(err)
	}
	if err := h.service.TouchRecentSetup(ctx, second.ID, artifactID, 42, "recent-touch-second"); err != nil {
		t.Fatal(err)
	}
	if err := h.service.TouchRecentSetup(ctx, third.ID, "", 0, "recent-touch-third"); err != nil {
		t.Fatal(err)
	}
	recent, err := h.service.ListRecentSetups(ctx)
	if err != nil || len(recent) != 2 || recent[0].SetupID != third.ID || recent[1].SetupID != second.ID || recent[1].LastLine != 42 {
		t.Fatalf("recent = %+v, %v", recent, err)
	}
	if err := h.service.TouchRecentSetup(ctx, second.ID, artifactID, 77, "recent-retouch-second"); err != nil {
		t.Fatal(err)
	}
	recent, _ = h.service.ListRecentSetups(ctx)
	if recent[0].SetupID != second.ID || recent[0].LastLine != 77 {
		t.Fatalf("retouched recent = %+v", recent)
	}

	state, err := h.service.PutUIState(ctx, UIState{
		ClientID: "browser-01", Screen: "setup", SelectedSetupID: second.ID,
		SelectedArtifactID: artifactID, Filters: []byte(`{"status":["draft"]}`),
		View: []byte(`{"line":77}`),
	}, "ui-state-valid")
	if err != nil || state.UpdatedAt.IsZero() {
		t.Fatalf("PutUIState = %+v, %v", state, err)
	}
	replayedState, err := h.service.PutUIState(ctx, UIState{
		ClientID: "browser-01", Screen: "setup", SelectedSetupID: second.ID,
		SelectedArtifactID: artifactID, Filters: []byte(`{"status":["draft"]}`),
		View: []byte(`{"line":77}`),
	}, "ui-state-valid")
	if err != nil || !replayedState.UpdatedAt.Equal(state.UpdatedAt) {
		t.Fatalf("PutUIState replay = %+v, %v; original=%+v", replayedState, err, state)
	}
	loaded, err := h.service.GetUIState(ctx, "browser-01")
	if err != nil || loaded.SelectedSetupID != second.ID || loaded.SelectedArtifactID != artifactID || loaded.Screen != "setup" {
		t.Fatalf("GetUIState = %+v, %v", loaded, err)
	}
	defaults, err := h.service.GetUIState(ctx, "new-client")
	if err != nil || defaults.Screen != "library" || string(defaults.Filters) != "{}" {
		t.Fatalf("default UI state = %+v, %v", defaults, err)
	}
	if _, err := h.service.PutUIState(ctx, UIState{
		ClientID: "browser-01", Screen: "setup", SelectedSetupID: first.ID,
		SelectedArtifactID: artifactID,
	}, "ui-state-cross-setup"); !domain.IsErrorCode(err, domain.CodeArtifactNotFound) {
		t.Fatalf("cross-setup UI artifact error = %v", err)
	}
	if _, err := h.service.PutUIState(ctx, UIState{
		ClientID: "browser-01", Screen: "setup", Filters: []byte(`[]`),
	}, "ui-state-invalid-json"); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("non-object UI JSON error = %v", err)
	}
	if err := h.service.DeleteRecentSetup(ctx, second.ID, "recent-delete-second"); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ClearRecentSetups(ctx, "recent-clear-all"); err != nil {
		t.Fatal(err)
	}
	if recent, err := h.service.ListRecentSetups(ctx); err != nil || len(recent) != 0 {
		t.Fatalf("cleared recent = %+v, %v", recent, err)
	}
}

func TestArchiveRestoreDetectsManagedObjectReplacement(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Контроль", "restore-create")
	_, object, _ := h.attachProgram(t, setup.ID, "check.ngc", []byte("G1 X1\n"), true)
	h.markReady(t, setup.ID, setup.Revision)
	archived, err := h.service.ArchiveSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "restore-archive",
	})
	if err != nil || archived.Status != domain.SetupStatusArchived {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	file, err := h.roots.OpenLibrary(object.Key, unix.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("M"), 0); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	restored, err := h.service.RestoreSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "restore-changed",
	})
	if err != nil || restored.Status != domain.SetupStatusAttention || len(restored.NotReadyReasons) == 0 {
		t.Fatalf("restore changed = %+v, %v", restored, err)
	}

	attention := h.createSetup(t, "Уже требует внимания", "restore-attention-create")
	h.attachProgram(t, attention.ID, "attention.ngc", []byte("G1 X2\n"), true)
	if _, err := h.db.SQL().ExecContext(ctx, `
		UPDATE setups SET status = 'attention', attention_reason = 'external sentinel reason'
		 WHERE id = ?`, attention.ID); err != nil {
		t.Fatal(err)
	}
	attentionArchived, err := h.service.ArchiveSetup(ctx, attention.ID, ArchiveInput{
		ExpectedRevision: attention.Revision, IdempotencyKey: "restore-attention-archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	attentionRestored, err := h.service.RestoreSetup(ctx, attention.ID, ArchiveInput{
		ExpectedRevision: attentionArchived.Revision, IdempotencyKey: "restore-attention-restore",
	})
	if err != nil || attentionRestored.Status != domain.SetupStatusAttention {
		t.Fatalf("restore prior attention = %+v, %v", attentionRestored, err)
	}
	foundReason := false
	for _, reason := range attentionRestored.NotReadyReasons {
		foundReason = foundReason || reason == "external sentinel reason"
	}
	if !foundReason {
		t.Fatalf("restored attention reason = %v", attentionRestored.NotReadyReasons)
	}
}

func TestRestoreSetupUsesPersistentProgressJob(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Job restore", "restore-job-create")
	content := []byte("G1 X10\nG1 X20\n")
	h.attachProgram(t, setup.ID, "restore.ngc", content, true)
	archived, err := h.service.ArchiveSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "restore-job-archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := ArchiveInput{ExpectedRevision: archived.Revision, IdempotencyKey: "restore-job-start"}
	job, err := h.service.RestoreSetupJob(ctx, setup.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := h.service.RestoreSetupJob(ctx, setup.ID, input)
	if err != nil || replayed.ID != job.ID {
		t.Fatalf("restore job replay = %+v, %v", replayed, err)
	}
	terminal, err := h.service.waitForJob(ctx, job.ID)
	if err != nil || terminal.State != domain.JobStateSucceeded || terminal.Progress.TotalBytes != int64(len(content)) {
		t.Fatalf("restore terminal job = %+v, %v", terminal, err)
	}
	restored, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || restored.Status != domain.SetupStatusDraft {
		t.Fatalf("restored setup = %+v, %v", restored, err)
	}
}

func TestPermanentDeleteConfirmationAndSharedObjectSafety(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	first := h.createSetup(t, "Удалить A", "delete-first")
	_, object, objectID := h.attachProgram(t, first.ID, "shared.ngc", []byte("G0 X0\n"), true)
	second := h.createSetup(t, "Оставить B", "delete-second")
	h.attachExistingProgram(t, second.ID, objectID, object, "shared-copy.ngc", true)
	archived, err := h.service.ArchiveSetup(ctx, first.ID, ArchiveInput{
		ExpectedRevision: first.Revision, IdempotencyKey: "delete-archive-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := h.service.CreateDeletePlan(ctx, first.ID, archived.Revision, "delete-plan-shared")
	if err != nil {
		t.Fatal(err)
	}
	replayedPlan, err := h.service.CreateDeletePlan(ctx, first.ID, archived.Revision, "delete-plan-shared")
	if err != nil || replayedPlan.ConfirmationToken != plan.ConfirmationToken ||
		!replayedPlan.ExpiresAt.Equal(plan.ExpiresAt) {
		t.Fatalf("delete plan replay = %+v, %v; original=%+v", replayedPlan, err, plan)
	}
	if plan.ProgramCount != 1 || plan.HasSetupSheet || plan.UniqueBytes != 0 || !domain.IsValidID(plan.ConfirmationToken) {
		t.Fatalf("shared delete plan = %+v", plan)
	}
	wrong := PermanentDeleteInput{
		ExpectedRevision: archived.Revision, ExactName: "не то имя",
		ConfirmationToken: plan.ConfirmationToken, IdempotencyKey: "delete-wrong-name",
	}
	if err := h.service.PermanentDeleteSetup(ctx, first.ID, wrong); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("wrong-name deletion = %v", err)
	}
	correct := PermanentDeleteInput{
		ExpectedRevision: archived.Revision, ExactName: first.Name,
		ConfirmationToken: plan.ConfirmationToken, IdempotencyKey: "delete-correct",
	}
	if err := h.service.PermanentDeleteSetup(ctx, first.ID, correct); err != nil {
		t.Fatal(err)
	}
	if err := h.service.PermanentDeleteSetup(ctx, first.ID, correct); err != nil {
		t.Fatalf("delete replay after aggregate removal: %v", err)
	}
	if _, err := h.service.GetSetup(ctx, first.ID); !domain.IsErrorCode(err, domain.CodeSetupNotFound) {
		t.Fatalf("deleted setup lookup = %v", err)
	}
	if _, err := h.store.OpenObject(object.Key, object.SHA256, object.Version); err != nil {
		t.Fatalf("shared physical object was removed: %v", err)
	}
	var refs int
	if err := h.db.SQL().QueryRowContext(ctx,
		"SELECT ref_count FROM storage_objects WHERE id = ?", objectID,
	).Scan(&refs); err != nil || refs != 1 {
		t.Fatalf("shared ref count = %d, %v", refs, err)
	}
	if setup, err := h.service.GetSetup(ctx, second.ID); err != nil || len(setup.Artifacts) != 1 {
		t.Fatalf("surviving setup = %+v, %v", setup, err)
	}

	unique := h.createSetup(t, "Уникальный", "delete-unique")
	_, uniqueObject, uniqueObjectID := h.attachProgram(t, unique.ID, "unique.ngc", []byte("G2 X2\n"), true)
	uniqueArchived, err := h.service.ArchiveSetup(ctx, unique.ID, ArchiveInput{
		ExpectedRevision: unique.Revision, IdempotencyKey: "delete-archive-unique",
	})
	if err != nil {
		t.Fatal(err)
	}
	uniquePlan, err := h.service.CreateDeletePlan(ctx, unique.ID, uniqueArchived.Revision, "delete-plan-unique")
	if err != nil || uniquePlan.UniqueBytes != uniqueObject.Size {
		t.Fatalf("unique delete plan = %+v, %v", uniquePlan, err)
	}
	if err := h.service.PermanentDeleteSetup(ctx, unique.ID, PermanentDeleteInput{
		ExpectedRevision: uniqueArchived.Revision, ExactName: unique.Name,
		ConfirmationToken: uniquePlan.ConfirmationToken, IdempotencyKey: "delete-unique-correct",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx,
		"SELECT ref_count FROM storage_objects WHERE id = ?", uniqueObjectID,
	).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("unique storage candidate refs = %d, %v", refs, err)
	}
	if _, err := h.store.OpenObject(uniqueObject.Key, uniqueObject.SHA256, ""); err != nil {
		t.Fatalf("permanent delete unlinked outside centralized GC: %v", err)
	}
	gc, err := h.service.GarbageCollect(ctx)
	if err != nil || gc.ObjectsRemoved != 1 || gc.BytesRemoved != uniqueObject.Size {
		t.Fatalf("centralized GC = %+v, %v", gc, err)
	}
	if err := h.db.SQL().QueryRowContext(ctx,
		"SELECT ref_count FROM storage_objects WHERE id = ?", uniqueObjectID,
	).Scan(&refs); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unique storage metadata still exists after GC: %v", err)
	}
	if _, err := h.store.OpenObject(uniqueObject.Key, uniqueObject.SHA256, ""); err == nil {
		t.Fatal("unique physical object still opens after centralized GC")
	}
}

func TestDeleteConfirmationExpiresAndArchiveTokensAreInvalidated(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) {
		options.DeleteConfirmationTTL = 2 * time.Second
	})
	ctx := context.Background()
	setup := h.createSetup(t, "Срок", "expiry-create")
	archived, err := h.service.ArchiveSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "expiry-archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := h.service.CreateDeletePlan(ctx, setup.ID, archived.Revision, "delete-plan-expired")
	if err != nil {
		t.Fatal(err)
	}
	h.clockNS.Add(int64(3 * time.Second))
	if err := h.service.PermanentDeleteSetup(ctx, setup.ID, PermanentDeleteInput{
		ExpectedRevision: archived.Revision, ExactName: setup.Name,
		ConfirmationToken: plan.ConfirmationToken, IdempotencyKey: "expiry-delete",
	}); !domain.IsErrorCode(err, domain.CodeConfirmationExpired) {
		t.Fatalf("expired confirmation error = %v", err)
	}

	secondPlan, err := h.service.CreateDeletePlan(ctx, setup.ID, archived.Revision, "delete-plan-second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RestoreSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: archived.Revision, IdempotencyKey: "expiry-restore",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ArchiveSetup(ctx, setup.ID, ArchiveInput{
		ExpectedRevision: archived.Revision, IdempotencyKey: "expiry-rearchive",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.PermanentDeleteSetup(ctx, setup.ID, PermanentDeleteInput{
		ExpectedRevision: archived.Revision, ExactName: setup.Name,
		ConfirmationToken: secondPlan.ConfirmationToken, IdempotencyKey: "invalidated-delete",
	}); !domain.IsErrorCode(err, domain.CodeConfirmationExpired) {
		t.Fatalf("invalidated confirmation error = %v", err)
	}
}

func TestConcurrentMetadataMutationHasSingleWinner(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	setup := h.createSetup(t, "Гонка", "race-create")
	inputs := []UpdateSetupInput{
		{ExpectedRevision: setup.Revision, Name: "Победитель A", IdempotencyKey: "race-a"},
		{ExpectedRevision: setup.Revision, Name: "Победитель B", IdempotencyKey: "race-b"},
	}
	var wait sync.WaitGroup
	wait.Add(len(inputs))
	errorsByIndex := make([]error, len(inputs))
	for index := range inputs {
		go func(index int) {
			defer wait.Done()
			_, errorsByIndex[index] = h.service.UpdateSetup(context.Background(), setup.ID, inputs[index])
		}(index)
	}
	wait.Wait()
	succeeded, conflicted := 0, 0
	for _, err := range errorsByIndex {
		switch {
		case err == nil:
			succeeded++
		case domain.IsErrorCode(err, domain.CodeRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent update error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent results success/conflict = %d/%d", succeeded, conflicted)
	}
}
