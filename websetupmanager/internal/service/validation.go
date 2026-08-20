package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

type validationResultJSON struct {
	ValidationRunID string                   `json:"validationRunId,omitempty"`
	Issues          []domain.ValidationIssue `json:"issues,omitempty"`
}

// ValidateSetup enqueues a revision-bound validation. The returned persistent
// job can be polled or cancelled; its terminal result contains ValidationRun.
func (s *Service) ValidateSetup(ctx context.Context, setupID string, input ValidateInput) (*domain.Job, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	hash, err := idempotencyRequestHash("validateSetup", struct {
		SetupID  string          `json:"setupId"`
		Revision domain.Revision `json:"revision"`
	}{setupID, input.ExpectedRevision})
	if err != nil {
		return nil, err
	}

	var job *domain.Job
	var validationID string
	err = s.withSetupLock(setupID, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "validateSetup", hash)
		if err != nil {
			return err
		}
		var replay domain.Job
		if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
			if err := tx.Commit(); err != nil {
				return databaseError(err)
			}
			if replayErr != nil {
				return replayErr
			}
			current, err := s.GetJob(ctx, replay.ID)
			if err != nil {
				return err
			}
			job = current
			return nil
		}

		setup, err := s.loadSetup(ctx, tx, setupID, false)
		if err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, err)
		}
		if err := domain.CheckExpectedRevision(setup.Revision, input.ExpectedRevision); err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, err)
		}
		if setup.Status == domain.SetupStatusArchived {
			return finishValidationClaimFailure(ctx, tx, claim,
				domain.NewError(domain.CodeInvalidSetupState, "archived setup cannot be validated"))
		}
		artifacts, err := s.loadArtifacts(ctx, tx, setupID)
		if err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, err)
		}
		totalBytes, err := validationWorkBytes(artifacts)
		if err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, err)
		}
		validationID, err = domain.NewValidationRunID()
		if err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, err)
		}
		now := sqlTimestamp(s.now())
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO validation_runs(id, setup_id, revision, state, result_json, created_at)
			VALUES (?, ?, ?, 'queued', ?, ?)`, validationID, setupID, input.ExpectedRevision,
			`{"issues":[]}`, now); err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, databaseError(err))
		}
		job, err = s.insertJobTx(ctx, tx, domain.JobKindValidate, setupID, "", &totalBytes)
		if err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, err)
		}
		queuedResult, err := json.Marshal(validationResultJSON{ValidationRunID: validationID})
		if err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET result_json = ? WHERE id = ?`, string(queuedResult), job.ID); err != nil {
			return finishValidationClaimFailure(ctx, tx, claim, databaseError(err))
		}
		job.Result = queuedResult
		if err := finishIdempotencyTx(ctx, tx, claim, 202, job, nil); err != nil {
			return err
		}
		return databaseError(tx.Commit())
	})
	if err != nil || job == nil || validationID == "" {
		return job, err
	}
	s.launchJob(job.ID, func(jobCtx context.Context, progress func(domain.JobProgress) error) (any, error) {
		return s.executeValidation(jobCtx, job.ID, setupID, validationID, input.ExpectedRevision, progress)
	})
	return job, nil
}

func finishValidationClaimFailure(ctx context.Context, tx *sql.Tx, claim idempotencyClaim, operationErr error) error {
	if finishErr := finishIdempotencyTx(ctx, tx, claim, 0, nil, operationErr); finishErr != nil {
		_ = tx.Rollback()
		return finishErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return databaseError(commitErr)
	}
	return operationErr
}

// GetValidationRun returns one run only when it belongs to the requested
// setup, preventing cross-aggregate ID confusion.
func (s *Service) GetValidationRun(ctx context.Context, setupID, runID string) (*domain.ValidationRun, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if err := domain.ValidateID(runID); err != nil {
		return nil, err
	}
	run, err := scanValidationRun(s.db.QueryRowContext(ctx, `
		SELECT v.id, v.setup_id, v.revision, v.state, v.result_json,
		       v.created_at, v.started_at, v.finished_at
		  FROM validation_runs v
		  JOIN setups owner
		    ON owner.id = v.setup_id AND owner.library_id = ?
		 WHERE v.id = ? AND v.setup_id = ?`, s.libraryID, runID, setupID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.CodeValidationNotFound, "validation run was not found")
	}
	if err != nil {
		return nil, databaseError(err)
	}
	return run, nil
}

func scanValidationRun(row scanner) (*domain.ValidationRun, error) {
	var run domain.ValidationRun
	var state, payload, created string
	var started, finished sql.NullString
	if err := row.Scan(&run.ID, &run.SetupID, &run.Revision, &state, &payload, &created, &started, &finished); err != nil {
		return nil, err
	}
	run.State = domain.ValidationState(state)
	if !run.State.Valid() {
		return nil, errors.New("invalid persisted validation state")
	}
	var result validationResultJSON
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return nil, err
	}
	run.Issues = result.Issues
	if run.Issues == nil {
		run.Issues = []domain.ValidationIssue{}
	}
	var err error
	if run.CreatedAt, err = parseTimestamp(created); err != nil {
		return nil, err
	}
	if run.StartedAt, err = parseNullableTimestamp(started); err != nil {
		return nil, err
	}
	if run.CompletedAt, err = parseNullableTimestamp(finished); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Service) executeValidation(
	ctx context.Context,
	jobID, setupID, runID string,
	revision domain.Revision,
	updateProgress func(domain.JobProgress) error,
) (*domain.ValidationRun, error) {
	if err := s.markValidationRunning(ctx, setupID, runID); err != nil {
		return nil, err
	}
	var result *domain.ValidationRun
	err := s.withSetupLock(setupID, func() error {
		setup, err := s.loadSetup(ctx, s.db, setupID, false)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(setup.Revision, revision); err != nil {
			return s.finishValidationConflict(ctx, jobID, setupID, runID, revision, err)
		}
		if setup.Status == domain.SetupStatusArchived {
			return domain.NewError(domain.CodeInvalidSetupState, "archived setup cannot be validated")
		}
		artifacts, err := s.loadArtifacts(ctx, s.db, setupID)
		if err != nil {
			return err
		}
		issues, attention, err := s.validateArtifacts(ctx, artifacts, updateProgress)
		if err != nil {
			return err
		}
		result, err = s.commitValidationResult(ctx, setup, jobID, runID, revision, issues, attention)
		return err
	})
	if err != nil {
		_ = s.markValidationAborted(context.Background(), jobID, setupID, runID, err)
		return nil, err
	}
	return result, nil
}

func (s *Service) markValidationRunning(ctx context.Context, setupID, runID string) error {
	now := sqlTimestamp(s.now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE validation_runs
		   SET state = 'running', started_at = ?
		 WHERE id = ? AND setup_id = ? AND state = 'queued'`, now, runID, setupID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return databaseError(errors.New("validation run cannot start"))
	}
	return nil
}

func (s *Service) validateArtifacts(
	ctx context.Context,
	artifacts []artifactRecord,
	updateProgress func(domain.JobProgress) error,
) ([]domain.ValidationIssue, bool, error) {
	issues := make([]domain.ValidationIssue, 0)
	programs := 0
	hasSheet := false
	seenNames := make(map[string]struct{}, len(artifacts))
	totalBytes, err := validationWorkBytes(artifacts)
	if err != nil {
		return nil, false, err
	}
	for _, artifact := range artifacts {
		if artifact.Role == domain.ArtifactRoleProgram {
			programs++
		} else if artifact.Role == domain.ArtifactRoleSetupSheet {
			hasSheet = true
		}
		normalized, err := domain.ArtifactNameKey(artifact.DisplayName)
		if err != nil {
			issues = append(issues, validationIssue("INVALID_NAME", artifact.ID,
				"artifact name is invalid", "rename the artifact"))
			continue
		}
		if _, duplicate := seenNames[normalized]; duplicate {
			issues = append(issues, validationIssue("DUPLICATE_NAME", artifact.ID,
				"artifact name conflicts with another setup artifact", "rename the artifact"))
		}
		seenNames[normalized] = struct{}{}
	}
	if programs == 0 {
		issues = append(issues, validationIssue("MISSING_PROGRAM", "",
			"setup has no G-code program", "add at least one G-code program"))
	}
	if s.requireSetupSheetForReady && !hasSheet {
		issues = append(issues, validationIssue("MISSING_SETUP_SHEET", "",
			"setup sheet is required", "add a PDF or HTML setup sheet"))
	}

	attention := false
	var completed int64
	reporter := newJobProgressReporter(updateProgress, totalBytes, int64(len(artifacts)))
	for index, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return nil, attention, err
		}
		objectStart := completed
		var progressErr error
		object, err := s.objects.VerifyObjectWithProgress(ctx, artifact.StorageKey, artifact.SHA256, artifact.Version, func(objectBytes int64) error {
			progressErr = reporter.report(objectStart+objectBytes, int64(index), false)
			return progressErr
		})
		if progressErr != nil {
			return nil, attention, progressErr
		}
		completed = objectStart + artifact.ByteSize
		if err != nil || object.Size != artifact.ByteSize {
			attention = true
			issues = append(issues, validationIssue("ARTIFACT_CHANGED", artifact.ID,
				"artifact is missing, changed, or unavailable", "replace or remove the artifact"))
			if artifact.Role == domain.ArtifactRoleProgram {
				completed += artifact.ByteSize
			}
			if reportErr := reporter.report(completed, int64(index+1), true); reportErr != nil {
				return nil, attention, reportErr
			}
			continue
		}
		if artifact.Role == domain.ArtifactRoleProgram {
			if reportErr := reporter.report(completed, int64(index), true); reportErr != nil {
				return nil, attention, reportErr
			}
			validationStart := completed
			file, openErr := s.objects.OpenObject(artifact.StorageKey, artifact.SHA256, artifact.Version)
			if openErr != nil {
				attention = true
				issues = append(issues, validationIssue("ARTIFACT_CHANGED", artifact.ID,
					"program is unavailable", "replace or remove the program"))
			} else {
				counted := &progressReader{reader: file, report: func(readBytes int64) error {
					progressErr = reporter.report(validationStart+readBytes, int64(index), false)
					return progressErr
				}}
				info, validationErr := s.gcode.Validate(ctx, artifact.Role, artifact.DisplayName, counted)
				closeErr := file.Close()
				if progressErr != nil {
					return nil, attention, progressErr
				}
				if validationErr != nil {
					code := "INVALID_GCODE"
					if domain.IsErrorCode(validationErr, domain.CodeUnsupportedFileType) {
						code = "UNSUPPORTED_FILE_TYPE"
					}
					issues = append(issues, validationIssue(code, artifact.ID,
						"program is not supported text G-code", "replace the program with supported G-code"))
				} else if closeErr != nil {
					attention = true
					issues = append(issues, validationIssue("ARTIFACT_UNAVAILABLE", artifact.ID,
						"program could not be read completely", "check or replace the program"))
				} else if info.Empty {
					issues = append(issues, domain.ValidationIssue{
						Code: "EMPTY_PROGRAM", Severity: domain.ValidationSeverityWarning,
						Message: "program is empty", ArtifactID: artifact.ID,
						Action: "confirm the program or replace it",
					})
				}
			}
			completed = validationStart + artifact.ByteSize
		} else if artifact.Role == domain.ArtifactRoleSetupSheet {
			if artifact.MediaType != "application/pdf" && !strings.HasPrefix(artifact.MediaType, "text/html") {
				issues = append(issues, validationIssue("UNSUPPORTED_FILE_TYPE", artifact.ID,
					"setup sheet type is unsupported", "replace it with PDF or standalone HTML"))
			}
		} else {
			issues = append(issues, validationIssue("UNSUPPORTED_FILE_TYPE", artifact.ID,
				"artifact role is unsupported", "remove the artifact"))
		}
		if _, verifyErr := s.objects.InspectObject(artifact.StorageKey, artifact.SHA256, artifact.Version); verifyErr != nil {
			attention = true
			issues = append(issues, validationIssue("ARTIFACT_CHANGED", artifact.ID,
				"artifact changed during validation", "refresh and validate again"))
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE storage_objects SET last_verified_at = ? WHERE id = ?`,
			sqlTimestamp(s.now()), artifact.StorageObjectID)
		if reportErr := reporter.report(completed, int64(index+1), true); reportErr != nil {
			return nil, attention, reportErr
		}
	}
	return issues, attention, nil
}

func validationWorkBytes(artifacts []artifactRecord) (int64, error) {
	var total int64
	for _, artifact := range artifacts {
		passes := 1
		if artifact.Role == domain.ArtifactRoleProgram {
			passes = 2
		}
		for range passes {
			if artifact.ByteSize > 0 && total > int64(^uint64(0)>>1)-artifact.ByteSize {
				return 0, domain.NewError(domain.CodeInvalidContent, "validation byte total exceeds supported range")
			}
			total += artifact.ByteSize
		}
	}
	return total, nil
}

func validationIssue(code, artifactID, message, action string) domain.ValidationIssue {
	return domain.ValidationIssue{
		Code: code, Severity: domain.ValidationSeverityError, Message: message,
		ArtifactID: artifactID, Action: action,
	}
}

func (s *Service) commitValidationResult(
	ctx context.Context,
	setup *domain.Setup,
	jobID, runID string,
	revision domain.Revision,
	issues []domain.ValidationIssue,
	attention bool,
) (_ *domain.ValidationRun, finalErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()
	current, err := s.loadSetup(ctx, tx, setup.ID, false)
	if err != nil {
		return nil, err
	}
	if err := domain.CheckExpectedRevision(current.Revision, revision); err != nil {
		return nil, s.finishValidationConflictTx(ctx, tx, current, jobID, runID, revision, err)
	}
	blocking := false
	for _, issue := range issues {
		if issue.Severity == domain.ValidationSeverityError {
			blocking = true
			break
		}
	}
	status := domain.SetupStatusReady
	var readyRevision any = revision
	attentionReason := ""
	validationState := domain.ValidationStateSucceeded
	if blocking {
		status = domain.SetupStatusDraft
		readyRevision = nil
		validationState = domain.ValidationStateFailed
		if attention {
			status = domain.SetupStatusAttention
			attentionReason = "one or more managed artifacts changed or became unavailable"
		}
	}
	payload, err := json.Marshal(validationResultJSON{Issues: issues})
	if err != nil {
		return nil, databaseError(err)
	}
	now := sqlTimestamp(s.now())
	result, err := tx.ExecContext(ctx, `
		UPDATE setups
		   SET status = ?, ready_revision = ?, attention_reason = NULLIF(?, ''), updated_at = ?
		 WHERE library_id = ? AND id = ? AND revision = ? AND status <> 'archived'`,
		status, readyRevision, attentionReason, now, s.libraryID, setup.ID, revision)
	if err != nil {
		return nil, databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, s.finishValidationConflictTx(ctx, tx, current, jobID, runID, revision,
			domain.NewError(domain.CodeRevisionConflict, "setup changed during validation"))
	}
	validationUpdate, err := tx.ExecContext(ctx, `
		UPDATE validation_runs
		   SET state = ?, result_json = ?, finished_at = ?
		 WHERE id = ? AND setup_id = ? AND revision = ? AND state = 'running'`,
		validationState, string(payload), now, runID, setup.ID, revision)
	if err != nil {
		return nil, databaseError(err)
	}
	if changed, rowsErr := validationUpdate.RowsAffected(); rowsErr != nil || changed != 1 {
		return nil, databaseError(errors.New("validation run is no longer active"))
	}
	completed, err := scanValidationRun(tx.QueryRowContext(ctx, `
		SELECT v.id, v.setup_id, v.revision, v.state, v.result_json,
		       v.created_at, v.started_at, v.finished_at
		  FROM validation_runs v
		  JOIN setups owner
		    ON owner.id = v.setup_id AND owner.library_id = ?
		 WHERE v.id = ? AND v.setup_id = ?`, s.libraryID, runID, setup.ID))
	if err != nil {
		return nil, databaseError(err)
	}
	if err := s.appendAudit(ctx, tx, domain.AuditOperationValidate, setup.ID, "", jobID,
		revision, revision, domain.AuditResultSucceeded, "", map[string]any{"passed": !blocking}); err != nil {
		return nil, err
	}
	// A validation run with blocking issues is terminal "failed", but the job
	// itself succeeded: it durably produced that complete validation result.
	if err := s.finishJobTx(ctx, tx, jobID, completed, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return completed, nil
}

func (s *Service) finishValidationConflict(ctx context.Context, jobID, setupID, runID string, revision domain.Revision, conflict error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	setup, err := s.loadSetup(ctx, tx, setupID, false)
	if err != nil {
		return err
	}
	return s.finishValidationConflictTx(ctx, tx, setup, jobID, runID, revision, conflict)
}

func (s *Service) finishValidationConflictTx(
	ctx context.Context,
	tx *sql.Tx,
	setup *domain.Setup,
	jobID, runID string,
	revision domain.Revision,
	conflict error,
) error {
	now := sqlTimestamp(s.now())
	payload, _ := json.Marshal(validationResultJSON{Issues: []domain.ValidationIssue{
		validationIssue("REVISION_CONFLICT", "", "setup changed during validation", "refresh and validate again"),
	}})
	validationUpdate, err := tx.ExecContext(ctx, `
		UPDATE validation_runs
		   SET state = 'conflict', result_json = ?, finished_at = ?
		 WHERE id = ? AND setup_id = ? AND state IN ('queued', 'running')`,
		string(payload), now, runID, setup.ID)
	if err != nil {
		return databaseError(err)
	}
	if changed, rowsErr := validationUpdate.RowsAffected(); rowsErr != nil || changed != 1 {
		return databaseError(errors.New("validation run is no longer active"))
	}
	if err := s.appendAudit(ctx, tx, domain.AuditOperationValidate, setup.ID, "", jobID,
		revision, setup.Revision, domain.AuditResultConflict, domain.CodeRevisionConflict, nil); err != nil {
		return err
	}
	if err := s.finishJobTx(ctx, tx, jobID, nil, conflict); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return databaseError(err)
	}
	return conflict
}

func (s *Service) markValidationAborted(ctx context.Context, jobID, setupID, runID string, cause error) error {
	state := domain.ValidationStateFailed
	code := "VALIDATION_FAILED"
	message := "validation could not be completed"
	if errors.Is(cause, context.Canceled) || domain.IsErrorCode(cause, domain.CodeJobCancelled) {
		state = domain.ValidationStateCancelled
		code = "VALIDATION_CANCELLED"
		message = "validation was cancelled"
	}
	payload, _ := json.Marshal(validationResultJSON{Issues: []domain.ValidationIssue{
		validationIssue(code, "", message, "run validation again"),
	}})
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	var revision domain.Revision
	if err := tx.QueryRowContext(ctx, `
		SELECT revision FROM validation_runs WHERE id = ? AND setup_id = ?`, runID, setupID).Scan(&revision); err != nil {
		return databaseError(err)
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE validation_runs
		   SET state = ?, result_json = ?, finished_at = ?
		 WHERE id = ? AND setup_id = ? AND state IN ('queued', 'running')`,
		state, string(payload), sqlTimestamp(s.now()), runID, setupID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed == 0 {
		return databaseError(tx.Commit())
	}
	auditResult := domain.AuditResultFailed
	errorCode := safeErrorCode(cause)
	if state == domain.ValidationStateCancelled {
		auditResult = domain.AuditResultCancelled
		errorCode = domain.CodeJobCancelled
	}
	if err := s.finishJobTx(ctx, tx, jobID, nil, cause); err != nil {
		return err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditOperationValidate, setupID, "", jobID,
		revision, revision, auditResult, errorCode, nil); err != nil {
		return err
	}
	return databaseError(tx.Commit())
}

func (s *Service) cancelQueuedValidation(ctx context.Context, jobID, setupID, runID string) error {
	payload, _ := json.Marshal(validationResultJSON{Issues: []domain.ValidationIssue{
		validationIssue("VALIDATION_CANCELLED", "", "validation was cancelled", "run validation again"),
	}})
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	var revision domain.Revision
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM validation_runs WHERE id = ? AND setup_id = ?`,
		runID, setupID).Scan(&revision); err != nil {
		return databaseError(err)
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE validation_runs
		   SET state = 'cancelled', result_json = ?, finished_at = ?
		 WHERE id = ? AND setup_id = ? AND state = 'queued'`,
		string(payload), sqlTimestamp(s.now()), runID, setupID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed != 0 {
		if err := s.finishJobTx(ctx, tx, jobID, nil, context.Canceled); err != nil {
			return err
		}
		if err := s.appendAudit(ctx, tx, domain.AuditOperationValidate, setupID, "", jobID,
			revision, revision, domain.AuditResultCancelled, domain.CodeJobCancelled, nil); err != nil {
			return err
		}
	}
	return databaseError(tx.Commit())
}
