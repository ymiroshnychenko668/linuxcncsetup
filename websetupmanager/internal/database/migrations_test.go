package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestMigrationChecksumMismatchStopsStartup(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	db, err := Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(
		"UPDATE schema_migrations SET checksum = ? WHERE version = 1",
		strings.Repeat("0", 64),
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(ctx, stateDir)
	if !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("Open error = %v, want ErrMigrationChecksum", err)
	}
}

func TestNewerSchemaStopsStartupWithoutDowngrade(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	db, err := Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`
		INSERT INTO schema_migrations(version, name, checksum)
		VALUES (999, 'future', ?)`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(ctx, stateDir)
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("Open error = %v, want ErrSchemaNewer", err)
	}
}

func TestMigrationIsTransactional(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	bad := migration{
		version:  1,
		name:     "broken",
		checksum: strings.Repeat("a", 64),
		sql: `
			CREATE TABLE schema_migrations (
				version INTEGER PRIMARY KEY,
				name TEXT NOT NULL,
				checksum TEXT NOT NULL,
				applied_at TEXT
			);
			CREATE TABLE should_rollback (id INTEGER);
			THIS IS NOT SQL;`,
	}
	if err := applyMigration(context.Background(), db, bad); err == nil {
		t.Fatal("applyMigration unexpectedly succeeded")
	}
	var count int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_schema
		 WHERE name IN ('schema_migrations', 'should_rollback')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed migration left %d schema objects", count)
	}
}

func TestUnmanagedDatabaseIsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	raw, err := sql.Open("sqlite", sqliteDSN(stateDir+"/"+defaultFilename, defaultBusyTimeout))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("CREATE TABLE user_data(value TEXT); INSERT INTO user_data VALUES ('keep')"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(ctx, stateDir)
	if !errors.Is(err, ErrUnmanagedDatabase) {
		t.Fatalf("Open error = %v, want ErrUnmanagedDatabase", err)
	}
	raw, err = sql.Open("sqlite", sqliteDSN(stateDir+"/"+defaultFilename, defaultBusyTimeout))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var value string
	if err := raw.QueryRow("SELECT value FROM user_data").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "keep" {
		t.Fatalf("user data = %q", value)
	}
}

func TestPendingMigrationRequiresBackupBeforeSchemaChange(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	firstChecksum := strings.Repeat("a", 64)
	secondChecksum := strings.Repeat("b", 64)
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT
		);
		CREATE TABLE existing_data (value TEXT NOT NULL);
		INSERT INTO existing_data(value) VALUES ('preserved');
		INSERT INTO schema_migrations(version, name, checksum)
		VALUES (1, 'initial', ?);`, firstChecksum); err != nil {
		t.Fatal(err)
	}
	migrations := []migration{
		{version: 1, name: "initial", checksum: firstChecksum},
		{version: 2, name: "upgrade", checksum: secondChecksum, sql: "CREATE TABLE upgraded_data (value TEXT);"},
	}

	backupCalled := false
	err = applyMigrationSet(ctx, db, migrations, func(ctx context.Context, fromVersion, toVersion int64) error {
		backupCalled = true
		if fromVersion != 1 || toVersion != 2 {
			t.Fatalf("backup versions = %d to %d, want 1 to 2", fromVersion, toVersion)
		}
		var exists int
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name = 'upgraded_data')`).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			t.Fatal("schema changed before pre-migration backup")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !backupCalled {
		t.Fatal("pending migration did not request a backup")
	}
	var version int64
	if err := db.QueryRow("SELECT max(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
}

func TestBackupFailurePreventsMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	firstChecksum := strings.Repeat("a", 64)
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT
		);
		INSERT INTO schema_migrations(version, name, checksum)
		VALUES (1, 'initial', ?);`, firstChecksum); err != nil {
		t.Fatal(err)
	}
	migrations := []migration{
		{version: 1, name: "initial", checksum: firstChecksum},
		{version: 2, name: "upgrade", checksum: strings.Repeat("b", 64), sql: "CREATE TABLE must_not_exist (value TEXT);"},
	}
	backupFailure := errors.New("backup unavailable")
	err = applyMigrationSet(ctx, db, migrations, func(context.Context, int64, int64) error {
		return backupFailure
	})
	if !errors.Is(err, backupFailure) {
		t.Fatalf("migration error = %v, want backup failure", err)
	}
	var exists int
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name = 'must_not_exist')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 0 {
		t.Fatal("migration ran after backup failure")
	}
}
