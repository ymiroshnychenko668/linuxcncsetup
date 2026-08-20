package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// EnsureLibrary records the stable identity of a managed library. Reopening
// the same ID with a different fingerprint, or the same fingerprint under a
// different ID, is rejected instead of mixing library-scoped state.
func (d *DB) EnsureLibrary(ctx context.Context, id, fingerprint string) (finalErr error) {
	if d == nil || d.sql == nil {
		return sql.ErrConnDone
	}
	if !validLibraryIdentity(id) || !validLibraryIdentity(fingerprint) {
		return errors.New("library ID and fingerprint must be non-empty bounded strings")
	}

	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin library identity transaction: %w", err)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()

	var stored string
	err = tx.QueryRowContext(ctx,
		"SELECT fingerprint FROM library_instances WHERE id = ?", id,
	).Scan(&stored)
	switch {
	case err == nil:
		if stored != fingerprint {
			return ErrLibraryFingerprintMismatch
		}
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read library identity: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO library_instances(id, fingerprint)
		VALUES (?, ?)`, id, fingerprint); err != nil {
		// A unique fingerprint owned by another ID is a library mismatch,
		// not an internal SQL error. Avoid depending on driver error text.
		var otherID string
		lookupErr := tx.QueryRowContext(ctx,
			"SELECT id FROM library_instances WHERE fingerprint = ?", fingerprint,
		).Scan(&otherID)
		if lookupErr == nil && otherID != id {
			return ErrLibraryFingerprintMismatch
		}
		return fmt.Errorf("store library identity: %w", err)
	}
	return tx.Commit()
}

func validLibraryIdentity(value string) bool {
	return value != "" && len(value) <= 1024 &&
		strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}
