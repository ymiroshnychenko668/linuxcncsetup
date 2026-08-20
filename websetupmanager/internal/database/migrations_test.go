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
