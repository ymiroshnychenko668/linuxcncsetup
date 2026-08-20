package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

const (
	defaultJobListLimit = 50
	maximumJobListLimit = 200
)

type jobWork func(context.Context, func(domain.JobProgress) error) (any, error)

// GetJob returns a persisted job. Terminal jobs are immutable and therefore
// remain stable across polling and process restarts.
func (s *Service) GetJob(ctx context.Context, jobID string) (*domain.Job, error) {
	if err := domain.ValidateID(jobID); err != nil {
		return nil, err
	}
	job, err := scanJob(s.db.QueryRowContext(ctx, jobSelect+` AND j.id = ?`, s.libraryID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.CodeJobNotFound, "job was not found")
	}
	if err != nil {
		return nil, databaseError(err)
	}
	return job, nil
}

func (s *Service) getJobTx(ctx context.Context, tx *sql.Tx, jobID string) (*domain.Job, error) {
	if tx == nil {
		return nil, databaseError(sql.ErrTxDone)
	}
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` AND j.id = ?`, s.libraryID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.CodeJobNotFound, "job was not found")
	}
	if err != nil {
		return nil, databaseError(err)
	}
	return job, nil
}

// ListJobs returns the newest bounded set of jobs for the active library.
func (s *Service) ListJobs(ctx context.Context, limit int) ([]domain.Job, error) {
	if limit == 0 {
		limit = defaultJobListLimit
	}
	if limit < 1 || limit > maximumJobListLimit {
		return nil, domain.NewError(domain.CodeInvalidContent, "job list limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, jobSelect+`
		 ORDER BY CASE WHEN j.state IN ('queued', 'running', 'cancelling') THEN 0 ELSE 1 END,
		          j.created_at DESC, j.id DESC
		 LIMIT ?`, s.libraryID, limit)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := make([]domain.Job, 0, limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, databaseError(err)
		}
		result = append(result, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}

// ListActiveJobsForSetup is the bounded reload-recovery view. It deliberately
// does not share the terminal-history page: the newest active job can never
// disappear merely because terminal jobs filled that page.
func (s *Service) ListActiveJobsForSetup(ctx context.Context, setupID string) ([]domain.Job, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, jobSelect+`
		 AND j.setup_id = ? AND j.state IN ('queued', 'running', 'cancelling')
		 ORDER BY j.created_at DESC, j.id DESC LIMIT 1`, s.libraryID, setupID)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, databaseError(err)
		}
		result = append(result, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}

// CancelJob requests cooperative cancellation. A job that has already
// reached a terminal state is returned unchanged; cancellation never masks a
// successfully committed operation.
func (s *Service) CancelJob(ctx context.Context, jobID, idempotencyKey string) (*domain.Job, error) {
	if err := domain.ValidateID(jobID); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	operation := "cancelJob:" + jobID
	hash, err := idempotencyRequestHash(operation, map[string]string{"jobId": jobID})
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, idempotencyKey, operation, hash)
	if err != nil {
		return nil, err
	}
	var replay domain.Job
	if replayed, replayErr := claim.replayInto(&replay); replayErr != nil {
		return nil, replayErr
	} else if replayed {
		return &replay, nil
	}
	result, operationErr := s.cancelJob(ctx, jobID)
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if finishErr := s.finishIdempotency(finishCtx, claim, 200, result, operationErr); finishErr != nil {
		return nil, finishErr
	}
	return result, operationErr
}

func (s *Service) cancelJob(ctx context.Context, jobID string) (*domain.Job, error) {
	before, err := s.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if before.State.Terminal() {
		return before, nil
	}
	if before.Kind == domain.JobKindImport {
		var importSessionID sql.NullString
		if err := s.db.QueryRowContext(ctx, `
			SELECT import_session_id FROM jobs
			 WHERE library_id = ? AND id = ?`, s.libraryID, jobID).Scan(&importSessionID); err != nil {
			return nil, databaseError(err)
		}
		if importSessionID.Valid && importSessionID.String != "" {
			_, cancelErr := s.cancelImport(ctx, importSessionID.String, nil, "", "")
			if cancelErr == nil {
				return s.GetJob(ctx, jobID)
			}
			if !domain.IsErrorCode(cancelErr, domain.CodeInvalidSetupState) {
				return nil, cancelErr
			}
		}
	}
	// A prepared upload can remain queued indefinitely while the browser is
	// waiting for confirmation or is reloaded. It has no worker to observe a
	// cancelling state, so cancellation terminalizes it atomically here.
	if before.State == domain.JobStateQueued {
		if before.Kind == domain.JobKindValidate {
			var linked validationResultJSON
			if json.Unmarshal(before.Result, &linked) == nil && linked.ValidationRunID != "" {
				if err := s.cancelQueuedValidation(ctx, before.ID, before.SetupID, linked.ValidationRunID); err != nil {
					return nil, err
				}
				after, err := s.GetJob(ctx, jobID)
				if err != nil {
					return nil, err
				}
				if after.State.Terminal() {
					s.signalJobCancellation(jobID)
					return after, nil
				}
			}
		}
		now := sqlTimestamp(s.now())
		result, updateErr := s.db.ExecContext(ctx, `
			UPDATE jobs SET cancel_requested = 1, state = 'cancelled',
			       error_code = ?, updated_at = ?, finished_at = ?
			 WHERE library_id = ? AND id = ? AND state = 'queued'`,
			domain.CodeJobCancelled, now, now, s.libraryID, jobID)
		if updateErr != nil {
			return nil, databaseError(updateErr)
		}
		if changed, rowsErr := result.RowsAffected(); rowsErr != nil {
			return nil, databaseError(rowsErr)
		} else if changed != 0 {
			s.signalJobCancellation(jobID)
			return s.GetJob(ctx, jobID)
		}
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		   SET cancel_requested = 1,
		       state = 'cancelling',
		       updated_at = ?
		 WHERE library_id = ? AND id = ?
		   AND state IN ('queued', 'running')`, sqlTimestamp(s.now()), s.libraryID, jobID)
	if err != nil {
		return nil, databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, databaseError(err)
	}
	if changed != 0 {
		if before.State == domain.JobStateQueued && before.Kind == domain.JobKindValidate {
			var linked validationResultJSON
			if json.Unmarshal(before.Result, &linked) == nil && linked.ValidationRunID != "" {
				_ = s.cancelQueuedValidation(ctx, before.ID, before.SetupID, linked.ValidationRunID)
			}
		}
		s.signalJobCancellation(jobID)
	}
	return s.GetJob(ctx, jobID)
}

func (s *Service) signalJobCancellation(jobID string) {
	s.jobsMu.Lock()
	cancel := s.jobs[jobID]
	s.jobsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

const jobSelect = `
	SELECT j.id, j.kind, j.setup_id, j.state, j.bytes_done, j.bytes_total,
	       j.error_code, j.result_json, j.created_at, j.started_at, j.finished_at
	  FROM jobs j
	 WHERE j.library_id = ?`

func scanJob(row scanner) (*domain.Job, error) {
	var job domain.Job
	var kind, state, created, result string
	var setupID, errorCode, started, finished sql.NullString
	var bytesTotal sql.NullInt64
	if err := row.Scan(
		&job.ID, &kind, &setupID, &state, &job.Progress.CompletedBytes, &bytesTotal,
		&errorCode, &result, &created, &started, &finished,
	); err != nil {
		return nil, err
	}
	job.Kind = domain.JobKind(kind)
	job.State = domain.JobState(state)
	if !job.Kind.Valid() || !job.State.Valid() || !json.Valid([]byte(result)) {
		return nil, fmt.Errorf("invalid persisted job")
	}
	job.SetupID = setupID.String
	job.ErrorCode = domain.ErrorCode(errorCode.String)
	job.Result = json.RawMessage(result)
	var upload uploadJobEnvelope
	if json.Unmarshal(job.Result, &upload) == nil && (upload.Upload != nil || upload.Setup != nil || upload.Progress.TotalItems > 0) {
		job.Progress.CompletedItems = upload.Progress.CompletedItems
		job.Progress.TotalItems = upload.Progress.TotalItems
		if job.Progress.TotalBytes == 0 {
			job.Progress.TotalBytes = upload.Progress.TotalBytes
		}
	}
	if bytesTotal.Valid {
		job.Progress.TotalBytes = bytesTotal.Int64
	}
	var err error
	if job.CreatedAt, err = parseTimestamp(created); err != nil {
		return nil, err
	}
	if job.StartedAt, err = parseNullableTimestamp(started); err != nil {
		return nil, err
	}
	if job.CompletedAt, err = parseNullableTimestamp(finished); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Service) insertJobTx(
	ctx context.Context,
	tx *sql.Tx,
	kind domain.JobKind,
	setupID, importSessionID string,
	totalBytes *int64,
) (*domain.Job, error) {
	if tx == nil {
		return nil, databaseError(sql.ErrTxDone)
	}
	if !kind.Valid() {
		return nil, domain.NewError(domain.CodeInvalidContent, "job kind is invalid")
	}
	if setupID != "" {
		if err := domain.ValidateID(setupID); err != nil {
			return nil, err
		}
		var activeJobID string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM jobs
			 WHERE library_id = ? AND setup_id = ?
			   AND state IN ('queued', 'running', 'cancelling')
			 ORDER BY created_at ASC, id ASC LIMIT 1`, s.libraryID, setupID).Scan(&activeJobID)
		if err == nil {
			busy := domain.NewError(domain.CodeInvalidSetupState, "another setup operation is already active")
			busy.Details = map[string]any{"jobId": activeJobID}
			return nil, busy
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, databaseError(err)
		}
	}
	if importSessionID != "" {
		if err := domain.ValidateID(importSessionID); err != nil {
			return nil, err
		}
	}
	if totalBytes != nil && *totalBytes < 0 {
		return nil, domain.NewError(domain.CodeInvalidContent, "job byte total is invalid")
	}
	jobID, err := domain.NewJobID()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	var bytes any
	progress := domain.JobProgress{}
	if totalBytes != nil {
		bytes = *totalBytes
		progress.TotalBytes = *totalBytes
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO jobs(
			id, library_id, kind, setup_id, import_session_id, state,
			progress, bytes_done, bytes_total, result_json, created_at, updated_at
		) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 'queued',
		          0, 0, ?, '{}', ?, ?)`,
		jobID, s.libraryID, kind, setupID, importSessionID, bytes,
		sqlTimestamp(now), sqlTimestamp(now))
	if err != nil {
		return nil, databaseError(err)
	}
	return &domain.Job{
		ID: jobID, Kind: kind, SetupID: setupID, State: domain.JobStateQueued,
		Progress: progress, Result: json.RawMessage("{}"), CreatedAt: now,
	}, nil
}

// launchJob registers cancellation before starting the goroutine, so a job
// returned to an API caller can always be cancelled without a registration
// race.
func (s *Service) launchJob(jobID string, work jobWork) {
	ctx, cancel := context.WithCancel(context.Background())
	s.jobsMu.Lock()
	select {
	case <-s.closed:
		s.jobsMu.Unlock()
		cancel()
		_ = s.finishJob(context.Background(), jobID, nil, context.Canceled)
		return
	default:
	}
	s.jobs[jobID] = cancel
	s.jobsWG.Add(1)
	s.jobsMu.Unlock()
	go func() {
		defer s.jobsWG.Done()
		s.runJob(ctx, jobID, work)
	}()
}

func (s *Service) runJob(ctx context.Context, jobID string, work jobWork) {
	started := time.Now()
	defer func() {
		s.jobsMu.Lock()
		delete(s.jobs, jobID)
		s.jobsMu.Unlock()
		s.logJobResult(jobID, started)
	}()
	release, err := s.acquireHeavy(ctx)
	if err != nil {
		_ = s.finishJob(context.Background(), jobID, nil, err)
		return
	}
	defer release()
	if err := s.markJobRunning(ctx, jobID); err != nil {
		_ = s.finishJob(context.Background(), jobID, nil, err)
		return
	}
	result, err := work(ctx, func(progress domain.JobProgress) error {
		return s.updateJobProgress(ctx, jobID, progress)
	})
	_ = s.finishJob(context.Background(), jobID, result, err)
}

func (s *Service) logJobResult(jobID string, started time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		s.logger.Error("job result unavailable",
			"job_id", jobID,
			"setup_id", "",
			"import_session_id", "",
			"operation", "job",
			"duration_ms", time.Since(started).Milliseconds(),
			"bytes", 0,
			"result", "unknown",
			"error_code", domain.CodeDatabaseUnavailable,
		)
		return
	}
	if started.IsZero() {
		started = job.CreatedAt
		if job.StartedAt != nil {
			started = *job.StartedAt
		}
	}
	var importSessionID string
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(import_session_id, '') FROM jobs WHERE library_id = ? AND id = ?`, s.libraryID, jobID).Scan(&importSessionID)
	result := "failed"
	if job.State == domain.JobStateSucceeded {
		result = "succeeded"
	} else if job.State == domain.JobStateCancelled {
		result = "cancelled"
	} else if job.State == domain.JobStateConflict {
		result = "conflict"
	}
	s.logger.Info("job completed",
		"job_id", job.ID,
		"setup_id", job.SetupID,
		"import_session_id", importSessionID,
		"kind", job.Kind,
		"operation", job.Kind,
		"duration_ms", time.Since(started).Milliseconds(),
		"bytes", job.Progress.CompletedBytes,
		"result", result,
		"error_code", job.ErrorCode,
	)
}

func (s *Service) markJobRunning(ctx context.Context, jobID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := sqlTimestamp(s.now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		   SET state = 'running', started_at = ?, updated_at = ?
		 WHERE library_id = ? AND id = ? AND state = 'queued' AND cancel_requested = 0`,
		now, now, s.libraryID, jobID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed != 1 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		job, loadErr := s.GetJob(ctx, jobID)
		if loadErr != nil {
			return loadErr
		}
		if job.State == domain.JobStateCancelling || job.State == domain.JobStateCancelled {
			return context.Canceled
		}
		return domain.NewError(domain.CodeInvalidSetupState, "job cannot start in its current state")
	}
	return nil
}

func (s *Service) updateJobProgress(ctx context.Context, jobID string, progress domain.JobProgress) error {
	if progress.CompletedBytes < 0 || progress.TotalBytes < 0 || progress.CompletedItems < 0 || progress.TotalItems < 0 ||
		(progress.TotalBytes > 0 && progress.CompletedBytes > progress.TotalBytes) ||
		(progress.TotalItems > 0 && progress.CompletedItems > progress.TotalItems) {
		return domain.NewError(domain.CodeInvalidContent, "job progress is invalid")
	}
	fraction := 0.0
	if progress.TotalBytes > 0 {
		fraction = float64(progress.CompletedBytes) / float64(progress.TotalBytes)
	} else if progress.TotalItems > 0 {
		fraction = float64(progress.CompletedItems) / float64(progress.TotalItems)
	}
	fraction = math.Max(0, math.Min(1, fraction))
	var total any
	if progress.TotalBytes > 0 {
		total = progress.TotalBytes
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		   SET progress = max(progress, ?),
		       bytes_done = max(bytes_done, ?),
		       bytes_total = COALESCE(bytes_total, ?),
		       updated_at = ?
		 WHERE library_id = ? AND id = ? AND state = 'running'`,
		fraction, progress.CompletedBytes, total, sqlTimestamp(s.now()), s.libraryID, jobID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed != 1 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return context.Canceled
	}
	return nil
}

func (s *Service) finishJob(ctx context.Context, jobID string, value any, workErr error) error {
	state, code, payload, progress, err := terminalJobValues(value, workErr)
	if err != nil {
		// A background job whose result cannot be represented still needs a
		// durable terminal state. Atomic domain callers use finishJobTx below,
		// which instead propagates this error and rolls the domain transaction back.
		state = domain.JobStateFailed
		code = domain.CodeInvalidContent
		payload = []byte("{}")
		progress = 0
	}
	now := sqlTimestamp(s.now())
	result, err := s.db.ExecContext(ctx, terminalJobUpdateSQL,
		state, progress, progress, code, string(payload), now, now,
		s.libraryID, jobID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed == 0 {
		// A terminal row is intentionally immutable. Treat observing it as a
		// successful idempotent finalization rather than overwriting it.
		job, loadErr := s.GetJob(ctx, jobID)
		if loadErr != nil {
			return loadErr
		}
		if !job.State.Terminal() {
			return databaseError(fmt.Errorf("job did not reach a terminal state"))
		}
	}
	return nil
}

// finishJobTx makes the terminal job snapshot part of the domain commit. A
// zero-row update is an error here: committing an aggregate after another
// transaction terminalized its job would make the two durable outcomes
// contradict each other.
func (s *Service) finishJobTx(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	value any,
	workErr error,
) error {
	if tx == nil {
		return databaseError(sql.ErrTxDone)
	}
	state, code, payload, progress, err := terminalJobValues(value, workErr)
	if err != nil {
		return err
	}
	now := sqlTimestamp(s.now())
	result, err := tx.ExecContext(ctx, terminalJobUpdateSQL,
		state, progress, progress, code, string(payload), now, now, s.libraryID, jobID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed != 1 {
		return databaseError(fmt.Errorf("job is no longer active"))
	}
	return nil
}

const terminalJobUpdateSQL = `
	UPDATE jobs
	   SET state = ?, progress = max(progress, ?),
	       bytes_done = CASE WHEN ? = 1 THEN COALESCE(bytes_total, bytes_done) ELSE bytes_done END,
	       error_code = NULLIF(?, ''),
	       result_json = ?, updated_at = ?, finished_at = ?
	 WHERE library_id = ? AND id = ?
	   AND state IN ('queued', 'running', 'cancelling')`

func terminalJobValues(value any, workErr error) (
	domain.JobState,
	domain.ErrorCode,
	[]byte,
	float64,
	error,
) {
	state := domain.JobStateSucceeded
	var code domain.ErrorCode
	if workErr != nil {
		state = domain.JobStateFailed
		if errors.Is(workErr, context.Canceled) || domain.IsErrorCode(workErr, domain.CodeJobCancelled) {
			state = domain.JobStateCancelled
			code = domain.CodeJobCancelled
		} else if value, ok := domain.ErrorCodeOf(workErr); ok {
			code = value
			if idempotencyErrorIsConflict(code) {
				state = domain.JobStateConflict
			}
		} else {
			code = domain.CodeDatabaseUnavailable
		}
	}
	payload := []byte("{}")
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", "", nil, 0,
				domain.WrapError(domain.CodeInvalidContent, "job result cannot be encoded", err)
		}
		payload = encoded
	}
	progress := 0.0
	if state == domain.JobStateSucceeded {
		progress = 1
	}
	return state, code, payload, progress, nil
}

// waitForJob is intentionally test-only in behavior but useful to internal
// maintenance callers that need a bounded synchronous barrier.
func (s *Service) waitForJob(ctx context.Context, jobID string) (*domain.Job, error) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := s.GetJob(ctx, jobID)
		if err != nil || job.State.Terminal() {
			return job, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
