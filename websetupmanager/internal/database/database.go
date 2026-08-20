package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const (
	defaultFilename    = "websetupmanager.sqlite3"
	defaultBusyTimeout = 5 * time.Second
	defaultConnections = 4
)

// Options controls opening the local SQLite database. StateDir must already
// exist and must not be a symbolic link. Filename, when set, is a basename;
// paths are deliberately rejected so callers cannot escape StateDir.
type Options struct {
	StateDir              string
	Filename              string
	BusyTimeout           time.Duration
	MaxOpenConns          int
	ExpectedStateIdentity *StateDirectoryIdentity
}

// StateDirectoryIdentity pins database startup to a state directory already
// opened and validated by the storage layer. It prevents a pathname rename or
// replacement from splitting staged content and SQLite across two roots.
type StateDirectoryIdentity struct {
	Device uint64
	Inode  uint64
}

// DB owns both the database/sql pool and the single-process state lock.
// Closing DB releases both resources.
type DB struct {
	sql      *sql.DB
	lock     *processLock
	stateDir string

	closeOnce sync.Once
	closeErr  error
}

// Open opens the default database in stateDir, verifies its integrity,
// applies embedded migrations and reconciles interrupted terminal records.
func Open(ctx context.Context, stateDir string) (*DB, error) {
	return OpenWithOptions(ctx, Options{StateDir: stateDir})
}

// OpenWithOptions is Open with explicit tuning intended mainly for tests and
// deployment configuration.
func OpenWithOptions(ctx context.Context, opts Options) (_ *DB, finalErr error) {
	if strings.TrimSpace(opts.StateDir) == "" {
		return nil, fmt.Errorf("%w: state directory is empty", ErrInvalidStateDir)
	}
	filename := opts.Filename
	if filename == "" {
		filename = defaultFilename
	}
	if !validBasename(filename) {
		return nil, fmt.Errorf("%w: database filename must be a basename", ErrInvalidStateDir)
	}
	if opts.BusyTimeout == 0 {
		opts.BusyTimeout = defaultBusyTimeout
	}
	if opts.BusyTimeout < 0 || opts.BusyTimeout > time.Hour {
		return nil, fmt.Errorf("invalid SQLite busy timeout")
	}
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = defaultConnections
	}
	if opts.MaxOpenConns < 1 {
		return nil, fmt.Errorf("invalid SQLite connection limit")
	}

	stateDir, err := filepath.Abs(opts.StateDir)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve directory: %v", ErrInvalidStateDir, err)
	}
	lock, err := acquireProcessLock(stateDir, opts.ExpectedStateIdentity)
	if err != nil {
		return nil, err
	}
	defer func() {
		if finalErr != nil {
			_ = lock.Close()
		}
	}()
	if err := cleanupBackupTemps(lock.dirFD); err != nil {
		return nil, fmt.Errorf("%w: clean interrupted database backup", ErrInvalidStateDir)
	}

	if err := inspectDatabaseFile(lock.dirFD, filename); err != nil {
		return nil, err
	}

	// Keep every lazily opened pool connection anchored to the directory that
	// was inspected and locked above. Reusing the configured pathname here
	// would let a rename/replacement make one pool span two physical databases.
	dsn := sqliteDSN(fmt.Sprintf("/proc/self/fd/%d/%s", lock.dirFD, filename), opts.BusyTimeout)
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	defer func() {
		if finalErr != nil {
			_ = sqldb.Close()
		}
	}()

	// Keep startup on one connection. WAL is persistent, while all
	// connection-local pragmas are also present in the DSN below.
	sqldb.SetMaxOpenConns(1)
	if err := sqldb.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect to SQLite database: %w", err)
	}
	if err := configurePersistentPragmas(ctx, sqldb); err != nil {
		return nil, err
	}
	if err := quickCheck(ctx, sqldb); err != nil {
		return nil, err
	}
	startupDB := &DB{sql: sqldb, lock: lock, stateDir: stateDir}
	if err := applyMigrations(ctx, sqldb, func(ctx context.Context, fromVersion, toVersion int64) error {
		backupName, err := migrationBackupName(fromVersion, toVersion)
		if err != nil {
			return err
		}
		return startupDB.Backup(ctx, backupName)
	}); err != nil {
		return nil, err
	}
	if err := quickCheck(ctx, sqldb); err != nil {
		return nil, err
	}

	db := &DB{sql: sqldb, lock: lock, stateDir: stateDir}
	if _, err := db.RecoverInterrupted(ctx); err != nil {
		return nil, fmt.Errorf("recover interrupted database operations: %w", err)
	}
	sqldb.SetMaxOpenConns(opts.MaxOpenConns)
	sqldb.SetMaxIdleConns(opts.MaxOpenConns)
	return db, nil
}

func validBasename(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsRune(name, 0) && filepath.Base(name) == name &&
		!strings.ContainsAny(name, `/\\`)
}

func inspectDatabaseFile(dirFD int, filename string) error {
	for _, name := range []string{filename, filename + "-wal", filename + "-shm", filename + "-journal"} {
		var stat unix.Stat_t
		err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: inspect database control file: %v", ErrInvalidStateDir, err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o022 != 0 {
			return fmt.Errorf("%w: database control file is not a private regular file", ErrInvalidStateDir)
		}
	}
	return nil
}

func sqliteDSN(path string, busyTimeout time.Duration) string {
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	return u.String()
}

func configurePersistentPragmas(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return fmt.Errorf("enable SQLite WAL: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("enable SQLite WAL: unexpected journal mode %q", mode)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return fmt.Errorf("enable SQLite full synchronization: %w", err)
	}
	return nil
}

func quickCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIntegrityCheck, err)
	}
	defer rows.Close()

	seen := false
	for rows.Next() {
		seen = true
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("%w: read result: %v", ErrIntegrityCheck, err)
		}
		if result != "ok" {
			return fmt.Errorf("%w: SQLite reported a consistency error", ErrIntegrityCheck)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrIntegrityCheck, err)
	}
	if !seen {
		return fmt.Errorf("%w: SQLite returned no result", ErrIntegrityCheck)
	}
	return nil
}

// SQL returns the database/sql handle for repository-layer transactions. The
// handle remains owned by DB and must not be closed by the caller.
func (d *DB) SQL() *sql.DB {
	return d.sql
}

// Ping verifies that SQLite is reachable through the configured pool.
func (d *DB) Ping(ctx context.Context) error {
	if d == nil || d.sql == nil {
		return sql.ErrConnDone
	}
	return d.sql.PingContext(ctx)
}

// QuickCheck runs SQLite's bounded startup integrity check on demand.
func (d *DB) QuickCheck(ctx context.Context) error {
	if d == nil || d.sql == nil {
		return sql.ErrConnDone
	}
	return quickCheck(ctx, d.sql)
}

// Close closes SQLite before releasing the process lock. It is idempotent.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		var errs []error
		if d.sql != nil {
			errs = append(errs, d.sql.Close())
		}
		errs = append(errs, d.lock.Close())
		d.closeErr = errors.Join(errs...)
	})
	return d.closeErr
}
