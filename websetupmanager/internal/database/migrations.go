package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// migrationFiles is intentionally private: schema changes must always pass
// checksum/history validation rather than being executed ad hoc.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationFilename = regexp.MustCompile(`^([0-9]+)_([a-z0-9_]+)\.sql$`)

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected migration directory %q", entry.Name())
		}
		matches := migrationFilename.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		contents, err := migrationFiles.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read embedded migration %q: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(contents)
		migrations = append(migrations, migration{
			version:  version,
			name:     matches[2],
			checksum: hex.EncodeToString(digest[:]),
			sql:      string(contents),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	if len(migrations) == 0 {
		return nil, errors.New("no embedded database migrations")
	}
	for i, item := range migrations {
		want := int64(i + 1)
		if item.version != want {
			return nil, fmt.Errorf("embedded migration sequence has version %d, expected %d", item.version, want)
		}
	}
	return migrations, nil
}

type appliedMigration struct {
	version  int64
	checksum string
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	tableExists, err := migrationTableExists(ctx, db)
	if err != nil {
		return err
	}
	if !tableExists {
		empty, err := databaseHasNoUserSchema(ctx, db)
		if err != nil {
			return err
		}
		if !empty {
			return ErrUnmanagedDatabase
		}
	}

	applied, err := readAppliedMigrations(ctx, db, tableExists)
	if err != nil {
		return err
	}
	if err := validateMigrationHistory(applied, migrations); err != nil {
		return err
	}

	for _, item := range migrations[len(applied):] {
		if err := applyMigration(ctx, db, item); err != nil {
			return fmt.Errorf("apply database migration %d (%s): %w", item.version, item.name, err)
		}
	}
	return nil
}

func migrationTableExists(ctx context.Context, db *sql.DB) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_schema
			 WHERE type = 'table' AND name = 'schema_migrations'
		)`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("inspect database migration table: %w", err)
	}
	return exists == 1, nil
}

func databaseHasNoUserSchema(ctx context.Context, db *sql.DB) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_schema
			 WHERE name NOT LIKE 'sqlite_%'
			   AND type IN ('table', 'view', 'trigger', 'index')
		)`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("inspect existing database schema: %w", err)
	}
	return exists == 0, nil
}

func readAppliedMigrations(ctx context.Context, db *sql.DB, tableExists bool) ([]appliedMigration, error) {
	if !tableExists {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT version, checksum
		  FROM schema_migrations
		 ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read database migration history: %w", err)
	}
	defer rows.Close()

	var applied []appliedMigration
	for rows.Next() {
		var item appliedMigration
		if err := rows.Scan(&item.version, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan database migration history: %w", err)
		}
		applied = append(applied, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read database migration history: %w", err)
	}
	return applied, nil
}

func validateMigrationHistory(applied []appliedMigration, embedded []migration) error {
	if len(applied) > len(embedded) {
		return ErrSchemaNewer
	}
	for i, item := range applied {
		wantVersion := int64(i + 1)
		if item.version > int64(len(embedded)) {
			return ErrSchemaNewer
		}
		if item.version != wantVersion {
			return fmt.Errorf("%w: expected version %d, found %d", ErrMigrationHistory, wantVersion, item.version)
		}
		if !strings.EqualFold(item.checksum, embedded[i].checksum) {
			return fmt.Errorf("%w at version %d", ErrMigrationChecksum, item.version)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, item migration) (finalErr error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, item.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations(version, name, checksum)
		VALUES (?, ?, ?)`, item.version, item.name, item.checksum); err != nil {
		return err
	}
	return tx.Commit()
}
