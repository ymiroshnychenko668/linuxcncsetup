//go:build linux

package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

const crashLifecycleHelperEnv = "WEB_SETUP_MANAGER_TEST_CRASH_LIFECYCLE_HELPER"

func TestReservationAndGarbageCollectionRaceNeverDeletesAdoptedObject(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Reservation race", "reservation-race-create")

	for iteration := 0; iteration < 20; iteration++ {
		content := []byte(fmt.Sprintf("G0 X%d\n", iteration))
		prepared, err := h.service.prepareArtifact(ctx, UploadArtifactInput{
			Role: domain.ArtifactRoleProgram, DisplayName: fmt.Sprintf("race-%02d.ngc", iteration),
			Content: bytes.NewReader(content), ExpectedSize: int64(len(content)),
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errors := make(chan error, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		go func() {
			ready.Done()
			<-start
			_, collectErr := h.service.GarbageCollect(ctx)
			errors <- collectErr
		}()
		go func(position int) {
			ready.Done()
			<-start
			tx, beginErr := h.db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
			if beginErr != nil {
				errors <- beginErr
				return
			}
			defer tx.Rollback()
			if ensureErr := h.service.ensurePreparedObjectTx(ctx, tx, prepared); ensureErr != nil {
				errors <- ensureErr
				return
			}
			artifactID, idErr := domain.NewArtifactID()
			if idErr != nil {
				errors <- idErr
				return
			}
			if insertErr := insertArtifactTx(ctx, tx, setup.ID, artifactID, prepared, position, false); insertErr != nil {
				errors <- insertErr
				return
			}
			errors <- tx.Commit()
		}(iteration)
		ready.Wait()
		close(start)
		for range 2 {
			if err := <-errors; err != nil {
				t.Fatalf("iteration %d race: %v", iteration, err)
			}
		}

		file, err := h.store.OpenObject(prepared.Object.Key, prepared.Object.SHA256, prepared.Object.Version)
		if err != nil {
			t.Fatalf("iteration %d adopted object missing: %v", iteration, err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		var refs int64
		var journalState string
		if err := h.db.SQL().QueryRowContext(ctx, `SELECT ref_count FROM storage_objects WHERE id = ?`,
			prepared.StorageObjectID).Scan(&refs); err != nil {
			t.Fatal(err)
		}
		if err := h.db.SQL().QueryRowContext(ctx, `SELECT state FROM operation_journal WHERE id = ?`,
			prepared.ReservationJournalID).Scan(&journalState); err != nil {
			t.Fatal(err)
		}
		if refs != 1 || journalState != string(domain.JournalStateCompleted) {
			t.Fatalf("iteration %d refs/state = %d/%s", iteration, refs, journalState)
		}
	}
}

func TestRecoverOperationsCompletesOnlyVerifiedAdoptionsAndCollectsConflicts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	libraryDir := filepath.Join(root, "library")
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(libraryDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	roots, db, store, manager := openRestartAcceptanceStack(t, ctx, libraryDir, stateDir)
	t.Cleanup(func() {
		manager.Close()
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
		if err := roots.Close(); err != nil {
			t.Errorf("close roots: %v", err)
		}
	})

	setup := createRecoveryFixtureSetup(t, ctx, manager, "Verified adoption")
	adopted := prepareRecoveryFixture(t, ctx, manager, "adopted.ngc", "G0 X1\n")
	tx, err := db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	artifactID := mustDomainID(t, domain.NewArtifactID)
	if err := insertArtifactTx(ctx, tx, setup.ID, artifactID, adopted, 0, true); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	orphan := prepareRecoveryFixture(t, ctx, manager, "orphan.ngc", "G0 X2\n")
	corrupt := prepareRecoveryFixture(t, ctx, manager, "corrupt.ngc", "G0 X3\n")
	if err := os.WriteFile(filepath.Join(libraryDir, filepath.FromSlash(corrupt.Object.Key)),
		[]byte("G0 BAD\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	uploaded := prepareRecoveryFixture(t, ctx, manager, "upload.ngc", "G0 X4\n")
	importID := mustDomainID(t, domain.NewImportID)
	importArtifactID := mustDomainID(t, domain.NewArtifactID)
	journalID := mustDomainID(t, domain.NewOperationID)
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO import_sessions(
			id, library_id, idempotency_key, setup_name, state, expires_at
		) VALUES (?, ?, 'recovery-upload', 'Recovered upload', 'staging',
		          strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+1 day'))`,
		importID, roots.LibraryID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO import_artifacts(
			id, import_session_id, role, display_name, normalized_name, staging_key,
			media_type, byte_size, sha256, object_version, state, storage_object_id
		) VALUES (?, ?, 'program', 'upload.ngc', 'upload.ngc', ?, 'text/x-gcode',
		          ?, ?, ?, 'staged', ?)`, importArtifactID, importID, uploaded.Object.Key,
		uploaded.Object.Size, uploaded.Object.SHA256, uploaded.Object.Version,
		uploaded.StorageObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO operation_journal(
			id, library_id, operation, storage_object_id, import_session_id,
			state, details_json
		) VALUES (?, ?, 'import', ?, ?, 'db_applied', json_object('importArtifactId', ?))`,
		journalID, roots.LibraryID(), uploaded.StorageObjectID, importID,
		importArtifactID); err != nil {
		t.Fatal(err)
	}

	recovered, err := manager.RecoverOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Examined != 5 || recovered.Completed != 3 || recovered.Conflicted != 2 {
		t.Fatalf("RecoverOperations = %+v, want 5/3/2", recovered)
	}
	for _, fixture := range []struct {
		id    string
		state domain.JournalState
		error string
	}{
		{adopted.ReservationJournalID, domain.JournalStateCompleted, ""},
		{uploaded.ReservationJournalID, domain.JournalStateCompleted, ""},
		{journalID, domain.JournalStateCompleted, ""},
		{orphan.ReservationJournalID, domain.JournalStateConflict, interruptedOperationErrorCode},
		{corrupt.ReservationJournalID, domain.JournalStateConflict, interruptedOperationErrorCode},
	} {
		var state string
		var errorCode sql.NullString
		if err := db.SQL().QueryRowContext(ctx, `
			SELECT state, error_code FROM operation_journal WHERE id = ?`, fixture.id).
			Scan(&state, &errorCode); err != nil {
			t.Fatal(err)
		}
		if state != string(fixture.state) || errorCode.String != fixture.error || errorCode.Valid != (fixture.error != "") {
			t.Errorf("journal %s state/error = %s/%v, want %s/%q",
				fixture.id, state, errorCode, fixture.state, fixture.error)
		}
	}
	second, err := manager.RecoverOperations(ctx)
	if err != nil || *second != (OperationRecoveryResult{}) {
		t.Fatalf("second RecoverOperations = %+v, %v", second, err)
	}
	collection, err := manager.GarbageCollect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if collection.ObjectsRemoved != 2 {
		t.Fatalf("GarbageCollect = %+v, want two conflicted objects removed", collection)
	}
	objects, err := store.ListObjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("managed objects after recovery = %d, want two referenced objects", len(objects))
	}
}

func createRecoveryFixtureSetup(t *testing.T, ctx context.Context, manager *Service, name string) *domain.Setup {
	t.Helper()
	setup, err := manager.CreateSetup(ctx, CreateSetupInput{Name: name, IdempotencyKey: "recovery-fixture-setup"})
	if err != nil {
		t.Fatal(err)
	}
	return setup
}

func prepareRecoveryFixture(t *testing.T, ctx context.Context, manager *Service,
	name, content string,
) *preparedArtifact {
	t.Helper()
	prepared, err := manager.prepareArtifact(ctx, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: name,
		Content: strings.NewReader(content), ExpectedSize: int64(len(content)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestRestartReconcilesInterruptedImportReplaceAndDuplicate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	libraryDir := filepath.Join(root, "library")
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(libraryDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	roots, db, store, manager := openRestartAcceptanceStack(t, ctx, libraryDir, stateDir)
	source, err := manager.CreateSetup(ctx, CreateSetupInput{Name: "Stable source", IdempotencyKey: "restart-source"})
	if err != nil {
		t.Fatal(err)
	}
	source, err = manager.AddPrograms(ctx, source.ID, AddProgramsInput{
		ExpectedRevision: source.Revision, IdempotencyKey: "restart-source-program",
		Programs: []UploadArtifactInput{{
			DisplayName: "stable.ngc", Content: strings.NewReader("G0 X0\n"), ExpectedSize: 6,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stableArtifact := source.Artifacts[0]

	importObjectID := publishOrphanFixture(t, ctx, manager, db, store, []byte("G0 X10\n"), "text/x-gcode")
	replaceObjectID := publishOrphanFixture(t, ctx, manager, db, store, []byte("G0 X20\n"), "text/x-gcode")
	importID := mustDomainID(t, domain.NewImportID)
	importJobID := mustDomainID(t, domain.NewJobID)
	replaceJobID := mustDomainID(t, domain.NewJobID)
	duplicateJobID := mustDomainID(t, domain.NewJobID)
	fixtures := []struct {
		journalID string
		operation domain.AuditOperation
		state     domain.JournalState
		setupID   string
		artifact  string
		objectID  string
		importID  string
		jobID     string
	}{
		{mustDomainID(t, domain.NewOperationID), domain.AuditOperationImport, domain.JournalStateStorageApplied,
			"", "", importObjectID, importID, importJobID},
		{mustDomainID(t, domain.NewOperationID), domain.AuditOperationReplaceProgram, domain.JournalStateDatabaseApplied,
			source.ID, stableArtifact.ID, replaceObjectID, "", replaceJobID},
		{mustDomainID(t, domain.NewOperationID), domain.AuditOperationDuplicate, domain.JournalStateIntent,
			source.ID, "", "", "", duplicateJobID},
	}
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO import_sessions(
			id, library_id, idempotency_key, setup_name, state, expires_at
		) VALUES (?, ?, 'restart-import-key', 'Interrupted import', 'committing', ?)`,
		importID, roots.LibraryID(), expires); err != nil {
		t.Fatal(err)
	}
	for _, job := range []struct {
		id       string
		kind     domain.JobKind
		setupID  string
		importID string
	}{
		{importJobID, domain.JobKindImport, "", importID},
		{replaceJobID, domain.JobKindReplaceProgram, source.ID, ""},
		{duplicateJobID, domain.JobKindDuplicate, source.ID, ""},
	} {
		if _, err := db.SQL().ExecContext(ctx, `
			INSERT INTO jobs(id, library_id, kind, setup_id, import_session_id, state, started_at)
			VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 'running',
			        strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
			job.id, roots.LibraryID(), job.kind, job.setupID, job.importID); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range fixtures {
		if _, err := db.SQL().ExecContext(ctx, `
			INSERT INTO operation_journal(
				id, library_id, operation, setup_id, artifact_id, storage_object_id,
				import_session_id, job_id, expected_revision, state
			) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
			          NULLIF(?, ''), NULLIF(?, ''), ?, ?)`, fixture.journalID, roots.LibraryID(),
			fixture.operation, fixture.setupID, fixture.artifact, fixture.objectID,
			fixture.importID, fixture.jobID, source.Revision, fixture.state); err != nil {
			t.Fatal(err)
		}
	}

	manager.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := roots.Close(); err != nil {
		t.Fatal(err)
	}

	restartedRoots, restartedDB, restartedStore, restartedManager := openRestartAcceptanceStack(t, ctx, libraryDir, stateDir)
	t.Cleanup(func() {
		restartedManager.Close()
		if err := restartedDB.Close(); err != nil {
			t.Errorf("close restarted database: %v", err)
		}
		if err := restartedRoots.Close(); err != nil {
			t.Errorf("close restarted roots: %v", err)
		}
	})

	loaded, err := restartedManager.GetSetup(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != source.Revision || len(loaded.Artifacts) != 1 ||
		loaded.Artifacts[0].ID != stableArtifact.ID || loaded.Artifacts[0].Version != stableArtifact.Version {
		t.Fatalf("restart exposed a partial source revision: %+v", loaded)
	}
	var setupCount int
	if err := restartedDB.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setups`).Scan(&setupCount); err != nil {
		t.Fatal(err)
	}
	if setupCount != 1 {
		t.Fatalf("restart exposed %d setups, want only stable source", setupCount)
	}
	for _, fixture := range fixtures {
		var state, errorCode string
		if err := restartedDB.SQL().QueryRowContext(ctx, `
			SELECT state, error_code FROM operation_journal WHERE id = ?`, fixture.journalID).
			Scan(&state, &errorCode); err != nil {
			t.Fatal(err)
		}
		if state != string(domain.JournalStateConflict) || errorCode != "PROCESS_INTERRUPTED" {
			t.Errorf("%s journal state/error = %s/%s", fixture.operation, state, errorCode)
		}
		var jobState, jobError string
		if err := restartedDB.SQL().QueryRowContext(ctx, `SELECT state, error_code FROM jobs WHERE id = ?`, fixture.jobID).
			Scan(&jobState, &jobError); err != nil {
			t.Fatal(err)
		}
		if jobState != string(domain.JobStateFailed) || jobError != "PROCESS_INTERRUPTED" {
			t.Errorf("%s job state/error = %s/%s", fixture.operation, jobState, jobError)
		}
	}
	var importState, importError string
	if err := restartedDB.SQL().QueryRowContext(ctx, `SELECT state, error_code FROM import_sessions WHERE id = ?`, importID).
		Scan(&importState, &importError); err != nil {
		t.Fatal(err)
	}
	if importState != string(domain.ImportStateConflict) || importError != "PROCESS_INTERRUPTED" {
		t.Fatalf("import recovery state/error = %s/%s", importState, importError)
	}

	collection, err := restartedManager.GarbageCollect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if collection.ObjectsRemoved != 2 {
		t.Fatalf("post-recovery collection = %+v", collection)
	}
	objects, err := restartedStore.ListObjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 {
		t.Fatalf("objects after recovery/GC = %d, want stable referenced object only", len(objects))
	}
}

func TestProcessKillRollsBackArchiveDeleteAndCurrentSelection(t *testing.T) {
	if os.Getenv(crashLifecycleHelperEnv) == "1" {
		runCrashLifecycleHelper(t)
		return
	}
	ctx := context.Background()
	root := t.TempDir()
	libraryDir := filepath.Join(root, "library")
	stateDir := filepath.Join(root, "state")
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
		t.Fatal(err)
	}
	if err := db.EnsureLibrary(ctx, roots.LibraryID(), roots.LibraryFingerprint()); err != nil {
		t.Fatal(err)
	}
	archiveID := mustDomainID(t, domain.NewSetupID)
	currentID := mustDomainID(t, domain.NewSetupID)
	deleteID := mustDomainID(t, domain.NewSetupID)
	for _, item := range []struct{ id, name string }{
		{archiveID, "Archive survives"}, {currentID, "Current remains clear"}, {deleteID, "Delete survives"},
	} {
		if _, err := db.SQL().ExecContext(ctx,
			`INSERT INTO setups(id, library_id, name, status, revision) VALUES (?, ?, ?, 'draft', 1)`,
			item.id, roots.LibraryID(), item.name); err != nil {
			t.Fatal(err)
		}
	}
	// The current-setup guard only permits a validated ready revision. The
	// child transaction must get past that guard before it is killed so this is
	// a real rollback test rather than a fixture-validation failure.
	if _, err := db.SQL().ExecContext(ctx, `
		UPDATE setups SET status = 'ready', ready_revision = revision WHERE id = ?`, currentID); err != nil {
		t.Fatal(err)
	}
	libraryID := roots.LibraryID()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := roots.Close(); err != nil {
		t.Fatal(err)
	}

	processContext, cancelProcess := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelProcess()
	command := exec.CommandContext(processContext, os.Args[0], "-test.run=^TestProcessKillRollsBackArchiveDeleteAndCurrentSelection$")
	command.Env = append(os.Environ(),
		crashLifecycleHelperEnv+"=1",
		"WEB_SETUP_MANAGER_TEST_CRASH_STATE="+stateDir,
		"WEB_SETUP_MANAGER_TEST_CRASH_LIBRARY_ID="+libraryID,
		"WEB_SETUP_MANAGER_TEST_CRASH_ARCHIVE_ID="+archiveID,
		"WEB_SETUP_MANAGER_TEST_CRASH_CURRENT_ID="+currentID,
		"WEB_SETUP_MANAGER_TEST_CRASH_DELETE_ID="+deleteID,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	scanned := scanner.Scan()
	marker := scanner.Text()
	if !scanned || marker != "transaction-ready" {
		_ = command.Process.Kill()
		_ = command.Wait()
		remainder, _ := io.ReadAll(stdout)
		t.Fatalf("crash helper did not enter transaction: scanned=%v marker=%q scan=%v stdout=%q stderr=%s",
			scanned, marker, scanner.Err(), string(remainder), stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("crash helper exited without kill status")
	}

	restarted, err := database.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var archiveStatus string
	if err := restarted.SQL().QueryRowContext(ctx, `SELECT status FROM setups WHERE id = ?`, archiveID).Scan(&archiveStatus); err != nil {
		t.Fatal(err)
	}
	var currentCount, deleteCount, journalCount int
	if err := restarted.SQL().QueryRowContext(ctx, `SELECT count(*) FROM current_setup`).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if err := restarted.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setups WHERE id = ?`, deleteID).Scan(&deleteCount); err != nil {
		t.Fatal(err)
	}
	if err := restarted.SQL().QueryRowContext(ctx, `
		SELECT count(*) FROM operation_journal
		 WHERE id IN ('11111111111111111111111111111111',
		              '22222222222222222222222222222222',
		              '33333333333333333333333333333333')`).Scan(&journalCount); err != nil {
		t.Fatal(err)
	}
	if archiveStatus != string(domain.SetupStatusDraft) || currentCount != 0 || deleteCount != 1 || journalCount != 0 {
		t.Fatalf("post-kill state archive=%s current=%d delete=%d journals=%d",
			archiveStatus, currentCount, deleteCount, journalCount)
	}
}

func runCrashLifecycleHelper(t *testing.T) {
	t.Helper()
	stateDir := os.Getenv("WEB_SETUP_MANAGER_TEST_CRASH_STATE")
	libraryID := os.Getenv("WEB_SETUP_MANAGER_TEST_CRASH_LIBRARY_ID")
	archiveID := os.Getenv("WEB_SETUP_MANAGER_TEST_CRASH_ARCHIVE_ID")
	currentID := os.Getenv("WEB_SETUP_MANAGER_TEST_CRASH_CURRENT_ID")
	deleteID := os.Getenv("WEB_SETUP_MANAGER_TEST_CRASH_DELETE_ID")
	db, err := database.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.SQL().BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO operation_journal(id, library_id, operation, setup_id, state)
		  VALUES ('11111111111111111111111111111111', ?, 'archive', ?, 'intent')`, []any{libraryID, archiveID}},
		{`UPDATE setups SET status = 'archived', archived_from_status = 'draft' WHERE id = ?`, []any{archiveID}},
		{`INSERT INTO operation_journal(id, library_id, operation, setup_id, state)
		  VALUES ('22222222222222222222222222222222', ?, 'selectCurrent', ?, 'intent')`, []any{libraryID, currentID}},
		{`INSERT INTO current_setup(library_id, setup_id, revision_selected) VALUES (?, ?, 1)`, []any{libraryID, currentID}},
		{`INSERT INTO operation_journal(id, library_id, operation, setup_id, state)
		  VALUES ('33333333333333333333333333333333', ?, 'permanentDelete', ?, 'intent')`, []any{libraryID, deleteID}},
		{`DELETE FROM setups WHERE id = ?`, []any{deleteID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fmt.Fprintln(os.Stdout, "transaction-ready"); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Minute)
	}
}

func openRestartAcceptanceStack(
	t *testing.T, ctx context.Context, libraryDir, stateDir string,
) (*storage.Roots, *database.DB, *storage.Store, *Service) {
	t.Helper()
	roots, err := storage.NewRoots(libraryDir, stateDir, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, stateDir)
	if err != nil {
		_ = roots.Close()
		t.Fatal(err)
	}
	if err := db.EnsureLibrary(ctx, roots.LibraryID(), roots.LibraryFingerprint()); err != nil {
		_ = db.Close()
		_ = roots.Close()
		t.Fatal(err)
	}
	store, err := storage.NewStore(roots, storage.StoreOptions{})
	if err != nil {
		_ = db.Close()
		_ = roots.Close()
		t.Fatal(err)
	}
	manager, err := New(Options{
		Database: db, Objects: store, LibraryID: roots.LibraryID(),
		GCodeExtensions: []string{".ngc", ".nc"},
	})
	if err != nil {
		_ = db.Close()
		_ = roots.Close()
		t.Fatal(err)
	}
	if _, err := manager.RecoverOperations(ctx); err != nil {
		manager.Close()
		_ = db.Close()
		_ = roots.Close()
		t.Fatal(err)
	}
	return roots, db, store, manager
}

func publishOrphanFixture(
	t *testing.T, ctx context.Context, manager *Service, db *database.DB, store *storage.Store,
	contents []byte, mediaType string,
) string {
	t.Helper()
	staged, err := store.Stage(ctx, bytes.NewReader(contents), int64(len(contents)))
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Publish(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	objectID, err := domain.NewStorageObjectID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO storage_objects(id, library_id, storage_key, media_type, byte_size, sha256)
		VALUES (?, ?, ?, ?, ?, ?)`, objectID, manager.libraryID, object.Key, mediaType, object.Size, object.SHA256); err != nil {
		t.Fatal(err)
	}
	return objectID
}

func mustDomainID(t *testing.T, generate func() (string, error)) string {
	t.Helper()
	value, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
