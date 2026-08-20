package database

import (
	"context"
	"database/sql"
	"fmt"
)

const interruptedErrorCode = "PROCESS_INTERRUPTED"

// RecoveryResult reports rows moved to stable terminal states during startup.
type RecoveryResult struct {
	Jobs              int64
	Journals          int64
	ValidationRuns    int64
	ImportSessions    int64
	IdempotencyClaims int64
}

// RecoverInterrupted atomically makes records that cannot still be running
// after a process restart terminal. Journals become conflict (rather than
// completed) so higher layers never mistake a partial filesystem/SQLite
// operation for a published mutation. It is safe and idempotent to call more
// than once; Open invokes it at startup.
func (d *DB) RecoverInterrupted(ctx context.Context) (_ RecoveryResult, finalErr error) {
	if d == nil || d.sql == nil {
		return RecoveryResult{}, sql.ErrConnDone
	}
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return RecoveryResult{}, err
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()

	var result RecoveryResult
	result.Jobs, err = execCount(ctx, tx, `
		UPDATE jobs
		   SET state = 'failed',
		       error_code = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE state IN ('queued', 'running', 'cancelling')`, interruptedErrorCode)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("recover jobs: %w", err)
	}

	result.Journals, err = execCount(ctx, tx, `
		UPDATE operation_journal
		   SET state = 'conflict',
		       error_code = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE state IN ('intent', 'storage_applied', 'db_applied')`, interruptedErrorCode)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("recover operation journal: %w", err)
	}

	result.ValidationRuns, err = execCount(ctx, tx, `
		UPDATE validation_runs
		   SET state = 'failed',
		       finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE state IN ('queued', 'running')`)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("recover validation runs: %w", err)
	}

	result.ImportSessions, err = execCount(ctx, tx, `
		UPDATE import_sessions
		   SET state = 'conflict',
		       error_code = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE state = 'committing'`, interruptedErrorCode)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("recover import sessions: %w", err)
	}

	result.IdempotencyClaims, err = execCount(ctx, tx, `
		UPDATE idempotency_requests
		   SET state = 'conflict', error_code = ?
		 WHERE state = 'in_progress'`, interruptedErrorCode)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("recover idempotency requests: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return RecoveryResult{}, err
	}
	return result, nil
}

func execCount(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
