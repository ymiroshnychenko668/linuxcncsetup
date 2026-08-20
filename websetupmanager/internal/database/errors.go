package database

import "errors"

var (
	// ErrAlreadyRunning means another process owns the state-directory lock.
	ErrAlreadyRunning = errors.New("web setup manager is already using this state directory")
	// ErrInvalidStateDir means the state directory or one of its control files
	// failed a no-follow/type check.
	ErrInvalidStateDir = errors.New("invalid state directory")
	// ErrUnmanagedDatabase prevents migrations from being applied over a
	// non-empty SQLite database that has no Web Setup Manager migration history.
	ErrUnmanagedDatabase = errors.New("database has no recognized migration history")
	// ErrMigrationChecksum means an embedded migration differs from the one
	// recorded when the database was created or upgraded.
	ErrMigrationChecksum = errors.New("database migration checksum mismatch")
	// ErrSchemaNewer means the database was written by a newer application and
	// cannot safely be downgraded.
	ErrSchemaNewer = errors.New("database schema is newer than this application")
	// ErrMigrationHistory means migration versions are missing or malformed.
	ErrMigrationHistory = errors.New("invalid database migration history")
	// ErrIntegrityCheck means SQLite quick_check did not report "ok".
	ErrIntegrityCheck = errors.New("database integrity check failed")
	// ErrLibraryFingerprintMismatch prevents state belonging to one managed
	// library from being silently reused for another library.
	ErrLibraryFingerprintMismatch = errors.New("library fingerprint does not match stored state")
	// ErrInvalidBackupDestination means the requested backup is not a new,
	// direct child of the state directory.
	ErrInvalidBackupDestination = errors.New("invalid database backup destination")
)
