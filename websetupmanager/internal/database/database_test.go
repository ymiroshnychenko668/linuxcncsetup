package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/libraryidentity"
	"golang.org/x/sys/unix"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func TestOpenMigratesFullInitialSchema(t *testing.T) {
	db := openTestDB(t)
	rows, err := db.SQL().Query(`
		SELECT name
		  FROM sqlite_schema
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		 ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"audit_events",
		"current_setup",
		"delete_confirmations",
		"idempotency_requests",
		"import_artifacts",
		"import_sessions",
		"jobs",
		"library_instances",
		"operation_journal",
		"recent_setups",
		"schema_migrations",
		"setup_artifacts",
		"setups",
		"storage_objects",
		"ui_state",
		"validation_runs",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tables = %q, want %q", got, want)
	}

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	var checksum string
	if err := db.SQL().QueryRow(
		"SELECT version, checksum FROM schema_migrations",
	).Scan(&version, &checksum); err != nil {
		t.Fatal(err)
	}
	if version != migrations[0].version || checksum != migrations[0].checksum {
		t.Fatalf("migration record = (%d, %q), want (%d, %q)",
			version, checksum, migrations[0].version, migrations[0].checksum)
	}
}

func TestPragmasApplyToEveryConnection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	connections := make([]*sql.Conn, 0, defaultConnections)
	for range defaultConnections {
		conn, err := db.SQL().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()

	for i, conn := range connections {
		var foreignKeys, busyTimeout, synchronous int
		var journalMode string
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("connection %d synchronous: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatalf("connection %d journal_mode: %v", i, err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 || journalMode != "wal" {
			t.Errorf("connection %d pragmas = fk:%d busy:%d sync:%d journal:%q",
				i, foreignKeys, busyTimeout, synchronous, journalMode)
		}
	}
	// Release every reserved connection before asking the pool to run the
	// integrity check. Keeping all MaxOpenConns checked out would deadlock.
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatalf("close reserved connection: %v", err)
		}
	}
	connections = nil
	if err := db.QuickCheck(ctx); err != nil {
		t.Fatalf("QuickCheck: %v", err)
	}
}

func TestProcessLockIsExclusiveAndReleased(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	first, err := Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, stateDir)
	if second != nil {
		_ = second.Close()
		t.Fatal("second Open unexpectedly succeeded")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Open error = %v, want ErrAlreadyRunning", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, stateDir)
	if err != nil {
		t.Fatalf("Open after release: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionPoolRemainsAnchoredAfterStateDirectoryReplacement(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	stateDir := filepath.Join(parent, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	savedDir := filepath.Join(parent, "original-state")
	if err := os.Rename(stateDir, savedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	connections := make([]*sql.Conn, 0, defaultConnections)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range defaultConnections {
		connection, err := db.SQL().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	for index, connection := range connections {
		var migrations int
		if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
			t.Fatalf("connection %d escaped held state root: %v", index, err)
		}
		if migrations == 0 {
			t.Fatalf("connection %d opened an unmigrated database", index)
		}
	}
	if _, err := os.Lstat(filepath.Join(stateDir, defaultFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement state directory received a database entry: %v", err)
	}
}

func TestOpenWithOptionsRejectsReplacedExpectedStateDirectory(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	stateDir := filepath.Join(parent, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var original unix.Stat_t
	if err := unix.Stat(stateDir, &original); err != nil {
		t.Fatal(err)
	}
	expected := &StateDirectoryIdentity{Device: uint64(original.Dev), Inode: original.Ino}

	savedDir := filepath.Join(parent, "original-state")
	if err := os.Rename(stateDir, savedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	db, err := OpenWithOptions(ctx, Options{StateDir: stateDir, ExpectedStateIdentity: expected})
	if db != nil {
		_ = db.Close()
		t.Fatal("OpenWithOptions unexpectedly accepted a replaced state directory")
	}
	if !errors.Is(err, ErrInvalidStateDir) {
		t.Fatalf("OpenWithOptions error = %v, want ErrInvalidStateDir", err)
	}
	for _, name := range []string{processLockName, defaultFilename} {
		if _, statErr := os.Lstat(filepath.Join(stateDir, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("replacement state directory received %q: %v", name, statErr)
		}
	}
}

func TestOpenWithOptionsAcceptsExpectedStateDirectory(t *testing.T) {
	stateDir := t.TempDir()
	var stat unix.Stat_t
	if err := unix.Stat(stateDir, &stat); err != nil {
		t.Fatal(err)
	}
	db, err := OpenWithOptions(context.Background(), Options{
		StateDir: stateDir,
		ExpectedStateIdentity: &StateDirectoryIdentity{
			Device: uint64(stat.Dev),
			Inode:  stat.Ino,
		},
	})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessLockRejectsSymlinkAndSpecialFile(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		stateDir := t.TempDir()
		sentinel := filepath.Join(t.TempDir(), "sentinel")
		if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, filepath.Join(stateDir, processLockName)); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), stateDir)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
		contents, err := os.ReadFile(sentinel)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != "unchanged" {
			t.Fatalf("sentinel changed to %q", contents)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		stateDir := t.TempDir()
		if err := unix.Mkfifo(filepath.Join(stateDir, processLockName), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), stateDir)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
	})

	t.Run("shared writable", func(t *testing.T) {
		stateDir := t.TempDir()
		lockPath := filepath.Join(stateDir, processLockName)
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, 0o666); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), stateDir)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
	})
}

func TestOpenRejectsSymlinkStateDirectoryAndDatabase(t *testing.T) {
	t.Run("state directory", func(t *testing.T) {
		realDir := t.TempDir()
		link := filepath.Join(t.TempDir(), "state-link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), link)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
	})

	t.Run("database", func(t *testing.T) {
		stateDir := t.TempDir()
		sentinel := filepath.Join(t.TempDir(), "sentinel.sqlite3")
		if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, filepath.Join(stateDir, defaultFilename)); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), stateDir)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
	})

	t.Run("hard-linked database", func(t *testing.T) {
		stateDir := t.TempDir()
		sentinel := filepath.Join(t.TempDir(), "sentinel.sqlite3")
		if err := os.WriteFile(sentinel, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(sentinel, filepath.Join(stateDir, defaultFilename)); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), stateDir)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
		contents, err := os.ReadFile(sentinel)
		if err != nil {
			t.Fatal(err)
		}
		if len(contents) != 0 {
			t.Fatalf("external hard-link sentinel was modified: %q", contents)
		}
	})

	t.Run("symlink WAL", func(t *testing.T) {
		stateDir := t.TempDir()
		sentinel := filepath.Join(t.TempDir(), "sentinel.wal")
		if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, filepath.Join(stateDir, defaultFilename+"-wal")); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), stateDir)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
		contents, err := os.ReadFile(sentinel)
		if err != nil || string(contents) != "unchanged" {
			t.Fatalf("external WAL sentinel = %q, %v", contents, err)
		}
	})

	t.Run("shared writable database", func(t *testing.T) {
		stateDir := t.TempDir()
		databasePath := filepath.Join(stateDir, defaultFilename)
		if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(databasePath, 0o666); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), stateDir)
		if !errors.Is(err, ErrInvalidStateDir) {
			t.Fatalf("Open error = %v, want ErrInvalidStateDir", err)
		}
	})
}

func TestEnsureLibraryIsStable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.EnsureLibrary(ctx, "library-a", "fingerprint-a"); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureLibrary(ctx, "library-a", "fingerprint-a"); err != nil {
		t.Fatalf("idempotent EnsureLibrary: %v", err)
	}
	if err := db.EnsureLibrary(ctx, "library-a", "fingerprint-b"); !errors.Is(err, ErrLibraryFingerprintMismatch) {
		t.Fatalf("mismatched fingerprint error = %v", err)
	}
	if err := db.EnsureLibrary(ctx, "library-b", "fingerprint-a"); !errors.Is(err, ErrLibraryFingerprintMismatch) {
		t.Fatalf("reused fingerprint error = %v", err)
	}
}

func TestEnsureLibraryMigratesOnlyToPortableMarkerFingerprint(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const id = "0123456789abcdef0123456789abcdef"
	if err := db.EnsureLibrary(ctx, id, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	portable := libraryidentity.Fingerprint(id)
	if err := db.EnsureLibrary(ctx, id, portable); err != nil {
		t.Fatalf("portable fingerprint migration: %v", err)
	}
	if err := db.EnsureLibrary(ctx, id, portable); err != nil {
		t.Fatalf("portable fingerprint replay: %v", err)
	}
}
