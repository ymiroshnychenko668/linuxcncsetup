package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupRecoveryMakesInterruptedWorkTerminal(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	db, err := Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureLibrary(ctx, "library", "fingerprint"); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO setups(id, library_id, name) VALUES ('setup', 'library', 'Setup')`,
		`INSERT INTO jobs(id, library_id, kind, setup_id, state)
		 VALUES ('job', 'library', 'import', 'setup', 'running')`,
		`INSERT INTO operation_journal(
			id, library_id, operation, setup_id, job_id, state
		 ) VALUES ('journal', 'library', 'import', 'setup', 'job', 'storage_applied')`,
		`INSERT INTO validation_runs(id, setup_id, revision, state)
		 VALUES ('validation', 'setup', 1, 'running')`,
		`INSERT INTO import_sessions(
			id, library_id, idempotency_key, setup_name, state, expires_at
		 ) VALUES ('import', 'library', 'import-key', 'Setup', 'committing',
		           strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+1 day'))`,
		`INSERT INTO idempotency_requests(
			library_id, key, operation, request_hash, state, expires_at
		 ) VALUES ('library', 'request-key', 'create', '` + strings.Repeat("a", 64) + `',
		           'in_progress', strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+1 day'))`,
	}
	for _, statement := range statements {
		if _, err := db.SQL().ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture statement failed: %v\n%s", err, statement)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	assertStateAndError := func(table, idColumn, id, wantState string) {
		t.Helper()
		var state, errorCode string
		query := fmt.Sprintf("SELECT state, error_code FROM %s WHERE %s = ?", table, idColumn)
		if err := db.SQL().QueryRowContext(ctx, query, id).Scan(&state, &errorCode); err != nil {
			t.Fatal(err)
		}
		if state != wantState || errorCode != interruptedErrorCode {
			t.Errorf("%s state/error = %q/%q, want %q/%q",
				table, state, errorCode, wantState, interruptedErrorCode)
		}
	}
	assertStateAndError("jobs", "id", "job", "failed")
	assertStateAndError("import_sessions", "id", "import", "conflict")
	assertStateAndError("idempotency_requests", "key", "request-key", "conflict")
	var journalState string
	var journalError sql.NullString
	if err := db.SQL().QueryRowContext(ctx, `
		SELECT state, error_code FROM operation_journal WHERE id = 'journal'`,
	).Scan(&journalState, &journalError); err != nil {
		t.Fatal(err)
	}
	if journalState != "storage_applied" || journalError.Valid {
		t.Errorf("operation journal state/error = %q/%v, want active without error", journalState, journalError)
	}

	var validationState string
	var validationFinished sql.NullString
	if err := db.SQL().QueryRowContext(ctx, `
		SELECT state, finished_at FROM validation_runs WHERE id = 'validation'`,
	).Scan(&validationState, &validationFinished); err != nil {
		t.Fatal(err)
	}
	if validationState != "failed" || !validationFinished.Valid {
		t.Errorf("validation state/finished = %q/%v", validationState, validationFinished)
	}

	result, err := db.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result != (RecoveryResult{}) {
		t.Fatalf("second recovery changed rows: %+v", result)
	}
}

func TestOnlineBackupIsConsistentAndNeverOverwrites(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.EnsureLibrary(ctx, "library", "fingerprint"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO setups(id, library_id, name) VALUES ('setup', 'library', 'Backup me')`,
	); err != nil {
		t.Fatal(err)
	}

	const backupName = "before-migration.sqlite3"
	if err := db.Backup(ctx, backupName); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	backupPath := filepath.Join(db.stateDir, backupName)
	stat, err := os.Lstat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0o111 != 0 {
		t.Fatalf("backup mode = %v, want non-executable regular file", stat.Mode())
	}

	backupDB, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	var setupName string
	if err := backupDB.QueryRowContext(ctx,
		"SELECT name FROM setups WHERE id = 'setup'",
	).Scan(&setupName); err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if setupName != "Backup me" {
		t.Fatalf("backup setup name = %q", setupName)
	}
	var check string
	if err := backupDB.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		t.Fatal(err)
	}
	if check != "ok" {
		t.Fatalf("backup quick_check = %q", check)
	}

	if err := db.Backup(ctx, backupName); !errors.Is(err, ErrInvalidBackupDestination) {
		t.Fatalf("overwrite error = %v, want ErrInvalidBackupDestination", err)
	}
	if err := db.Backup(ctx, "../outside.sqlite3"); !errors.Is(err, ErrInvalidBackupDestination) {
		t.Fatalf("traversal error = %v, want ErrInvalidBackupDestination", err)
	}
	entries, err := os.ReadDir(db.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".sqlite-backup-") {
			t.Errorf("backup staging entry remains: %s", entry.Name())
		}
	}
}

func TestOpenCleansInterruptedBackupTempsWithoutFollowingLinks(t *testing.T) {
	stateDir := t.TempDir()
	stale := backupTempPrefix + strings.Repeat("a", 32) + backupTempSuffix
	if err := os.WriteFile(filepath.Join(stateDir, stale), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := backupTempPrefix + strings.Repeat("b", 32) + backupTempSuffix + "-journal"
	if err := os.WriteFile(filepath.Join(stateDir, journal), []byte("partial journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := backupTempPrefix + strings.Repeat("c", 32) + backupTempSuffix
	if err := os.Symlink(sentinel, filepath.Join(stateDir, linked)); err != nil {
		t.Fatal(err)
	}
	unrelated := "operator-backup.tmp"
	if err := os.WriteFile(filepath.Join(stateDir, unrelated), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{stale, journal, linked} {
		if _, err := os.Lstat(filepath.Join(stateDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("backup staging entry %q remains: %v", name, err)
		}
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("external sentinel = %q, %v", contents, err)
	}
	contents, err = os.ReadFile(filepath.Join(stateDir, unrelated))
	if err != nil || string(contents) != "preserve" {
		t.Fatalf("unrelated state file = %q, %v", contents, err)
	}
}

func TestSchemaEnforcesArtifactInvariantsAndGCGuard(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.EnsureLibrary(ctx, "library", "fingerprint"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO setups(id, library_id, name) VALUES ('setup', 'library', 'Fixture')`,
	); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"object-1", "object-2", "object-3", "object-4"} {
		if _, err := db.SQL().ExecContext(ctx, `
			INSERT INTO storage_objects(
				id, library_id, storage_key, media_type, byte_size
			) VALUES (?, 'library', ?, 'text/x-gcode', 1)`, id, id+".blob"); err != nil {
			t.Fatal(err)
		}
	}
	insertArtifact := func(id, role, name, normalized, object string, primary int) error {
		_, err := db.SQL().ExecContext(ctx, `
			INSERT INTO setup_artifacts(
				id, setup_id, role, display_name, normalized_name,
				storage_object_id, is_primary, identity_device, identity_inode,
				identity_size, identity_mtime_ns, identity_ctime_ns, object_version
			) VALUES (?, 'setup', ?, ?, ?, ?, ?, 1, 1, 1, 1, 1, 'version')`,
			id, role, name, normalized, object, primary)
		return err
	}
	if err := insertArtifact("program-1", "program", "MAIN.NGC", "main.ngc", "object-1", 1); err != nil {
		t.Fatal(err)
	}
	var refs int
	if err := db.SQL().QueryRowContext(ctx,
		"SELECT ref_count FROM storage_objects WHERE id = 'object-1'",
	).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("ref_count after insert = %d", refs)
	}
	if err := insertArtifact("program-2", "program", "other.ngc", "other.ngc", "object-2", 1); err == nil {
		t.Fatal("second primary program unexpectedly succeeded")
	}
	if err := insertArtifact("program-3", "program", "main.ngc", "main.ngc", "object-2", 0); err == nil {
		t.Fatal("normalized duplicate name unexpectedly succeeded")
	}
	if err := insertArtifact("sheet-1", "setup_sheet", "sheet.pdf", "sheet.pdf", "object-3", 0); err != nil {
		t.Fatal(err)
	}
	if err := insertArtifact("sheet-2", "setup_sheet", "other.pdf", "other.pdf", "object-4", 0); err == nil {
		t.Fatal("second setup sheet unexpectedly succeeded")
	}

	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO operation_journal(
			id, library_id, operation, storage_object_id, state
		) VALUES ('journal', 'library', 'gc-test', 'object-2', 'intent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		"DELETE FROM storage_objects WHERE id = 'object-2'",
	); err == nil {
		t.Fatal("GC deleted object held by unfinished journal")
	}
	if _, err := db.SQL().ExecContext(ctx,
		"UPDATE operation_journal SET state = 'completed' WHERE id = 'journal'",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		"DELETE FROM storage_objects WHERE id = 'object-2'",
	); err != nil {
		t.Fatalf("delete unreferenced object after journal completion: %v", err)
	}

	if _, err := db.SQL().ExecContext(ctx,
		"DELETE FROM setup_artifacts WHERE id = 'program-1'",
	); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRowContext(ctx,
		"SELECT ref_count FROM storage_objects WHERE id = 'object-1'",
	).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Fatalf("ref_count after artifact delete = %d", refs)
	}
}
