package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

const (
	maximumClientIDBytes = 128
	maximumScreenBytes   = 64
	maximumUIJSONBytes   = 32 << 10
	defaultAuditLimit    = 100
	maximumAuditLimit    = 500
)

// GetCurrentSetup returns the exact revision explicitly selected by the
// operator. A missing selection is represented as (nil, nil).
func (s *Service) GetCurrentSetup(ctx context.Context) (*domain.CurrentSetup, error) {
	var current domain.CurrentSetup
	var selected string
	err := s.db.QueryRowContext(ctx, `
		SELECT library_id, setup_id, revision_selected, selected_at
		  FROM current_setup
		 WHERE library_id = ?`, s.libraryID).Scan(
		&current.LibraryID, &current.SetupID, &current.RevisionSelected, &selected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, databaseError(err)
	}
	current.SelectedAt, err = parseTimestamp(selected)
	if err != nil {
		return nil, databaseError(err)
	}
	return &current, nil
}

// SetCurrentSetup pins a ready setup at the exact revision confirmed by the
// operator. It only updates metadata: it never copies, launches, or executes
// managed content.
func (s *Service) SetCurrentSetup(ctx context.Context, input SetCurrentInput) (*domain.CurrentSetup, error) {
	if err := domain.ValidateID(input.SetupID); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if !input.Confirmed {
		return nil, domain.NewError(domain.CodeInvalidContent, "current setup selection requires confirmation")
	}
	if input.ExpectedPreviousSetupID == "" {
		if input.ExpectedPreviousRevision != 0 {
			return nil, domain.NewError(domain.CodeInvalidRevision, "previous current revision requires a setup")
		}
	} else {
		if err := domain.ValidateID(input.ExpectedPreviousSetupID); err != nil {
			return nil, err
		}
		if !input.ExpectedPreviousRevision.Valid() {
			return nil, domain.NewError(domain.CodeInvalidRevision, "previous current revision is required")
		}
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	hash, err := idempotencyRequestHash("selectCurrent", struct {
		SetupID                  string          `json:"setupId"`
		ExpectedRevision         domain.Revision `json:"expectedRevision"`
		ExpectedPreviousSetupID  string          `json:"expectedPreviousSetupId"`
		ExpectedPreviousRevision domain.Revision `json:"expectedPreviousRevision"`
		Confirmed                bool            `json:"confirmed"`
	}{input.SetupID, input.ExpectedRevision, input.ExpectedPreviousSetupID, input.ExpectedPreviousRevision, input.Confirmed})
	if err != nil {
		return nil, databaseError(err)
	}

	var result *domain.CurrentSetup
	err = s.withSetupLock(input.SetupID, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "selectCurrent", hash)
		if err != nil {
			return err
		}
		var replay domain.CurrentSetup
		if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
			if err := tx.Commit(); err != nil {
				return databaseError(err)
			}
			if replayErr != nil {
				return replayErr
			}
			result = &replay
			return nil
		}
		if err := beginLifecycleMutation(ctx, tx); err != nil {
			return err
		}

		setup, err := s.loadSetup(ctx, tx, input.SetupID, false)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := domain.CheckExpectedRevision(setup.Revision, input.ExpectedRevision); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if setup.Status != domain.SetupStatusReady {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeSetupNotReady, "only a ready setup revision can be selected"))
		}
		var previousID string
		var previousRevision domain.Revision
		previousErr := tx.QueryRowContext(ctx,
			"SELECT setup_id, revision_selected FROM current_setup WHERE library_id = ?", s.libraryID,
		).Scan(&previousID, &previousRevision)
		if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
			return finishLifecycleFailure(ctx, tx, claim, databaseError(previousErr))
		}
		if errors.Is(previousErr, sql.ErrNoRows) {
			if input.ExpectedPreviousSetupID != "" {
				return finishLifecycleFailure(ctx, tx, claim,
					domain.NewError(domain.CodeCurrentSetupConflict, "current setup selection changed"))
			}
		} else if input.ExpectedPreviousSetupID == "" || previousID != input.ExpectedPreviousSetupID || previousRevision != input.ExpectedPreviousRevision {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeCurrentSetupConflict, "current setup selection changed"))
		}
		journalID, err := s.appendJournal(ctx, tx, domain.AuditOperationSelectCurrent,
			input.SetupID, "", "", "", "", input.ExpectedRevision,
			map[string]any{"previousSetupId": previousID})
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO current_setup(library_id, setup_id, revision_selected, selected_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(library_id) DO UPDATE SET
				setup_id = excluded.setup_id,
				revision_selected = excluded.revision_selected,
				selected_at = excluded.selected_at`,
			s.libraryID, input.SetupID, input.ExpectedRevision, sqlTimestamp(now)); err != nil {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.WrapError(domain.CodeCurrentSetupConflict, "current setup selection changed", err))
		}
		if err := completeJournal(ctx, tx, journalID, input.ExpectedRevision); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := s.appendAudit(ctx, tx, domain.AuditOperationSelectCurrent, input.SetupID,
			"", "", input.ExpectedRevision, input.ExpectedRevision,
			domain.AuditResultSucceeded, "", map[string]any{"previousSetupId": previousID}); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		result = &domain.CurrentSetup{
			LibraryID: s.libraryID, SetupID: input.SetupID,
			RevisionSelected: input.ExpectedRevision, SelectedAt: now,
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return err
		}
		return databaseError(tx.Commit())
	})
	return result, err
}

// ClearCurrentSetup explicitly removes the exact selection the operator saw.
// Binding both stable ID and selected revision prevents a stale dialog from
// removing a newer selection made by another window.
func (s *Service) ClearCurrentSetup(ctx context.Context, input ClearCurrentInput) error {
	if err := domain.ValidateID(input.ExpectedSetupID); err != nil {
		return err
	}
	if !input.ExpectedRevision.Valid() {
		return domain.NewError(domain.CodeInvalidRevision, "expected selected revision is required")
	}
	if !input.Confirmed {
		return domain.NewError(domain.CodeInvalidContent, "clearing current setup requires confirmation")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return err
	}
	hash, err := idempotencyRequestHash("clearCurrent", struct {
		ExpectedSetupID  string          `json:"expectedSetupId"`
		ExpectedRevision domain.Revision `json:"expectedRevision"`
		Confirmed        bool            `json:"confirmed"`
	}{input.ExpectedSetupID, input.ExpectedRevision, input.Confirmed})
	if err != nil {
		return databaseError(err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "clearCurrent", hash)
	if err != nil {
		return err
	}
	var replay map[string]any
	if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		return replayErr
	}
	if err := beginLifecycleMutation(ctx, tx); err != nil {
		return err
	}
	var setupID string
	var revision domain.Revision
	err = tx.QueryRowContext(ctx, `
		SELECT setup_id, revision_selected FROM current_setup WHERE library_id = ?`,
		s.libraryID).Scan(&setupID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return finishLifecycleFailure(ctx, tx, claim,
			domain.NewError(domain.CodeCurrentSetupConflict, "current setup selection changed"))
	}
	if err != nil {
		return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
	}
	if setupID != input.ExpectedSetupID || revision != input.ExpectedRevision {
		return finishLifecycleFailure(ctx, tx, claim,
			domain.NewError(domain.CodeCurrentSetupConflict, "current setup selection changed"))
	}
	journalID, err := s.appendJournal(ctx, tx, domain.AuditOperationClearCurrent,
		setupID, "", "", "", "", revision, nil)
	if err != nil {
		return finishLifecycleFailure(ctx, tx, claim, err)
	}
	deleteResult, err := tx.ExecContext(ctx,
		"DELETE FROM current_setup WHERE library_id = ? AND setup_id = ? AND revision_selected = ?",
		s.libraryID, setupID, revision,
	)
	if err != nil {
		return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
	}
	deleted, err := deleteResult.RowsAffected()
	if err != nil {
		return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
	}
	if deleted != 1 {
		return finishLifecycleFailure(ctx, tx, claim,
			domain.NewError(domain.CodeCurrentSetupConflict, "current setup selection changed"))
	}
	if err := completeJournal(ctx, tx, journalID, revision); err != nil {
		return finishLifecycleFailure(ctx, tx, claim, err)
	}
	if err := s.appendAudit(ctx, tx, domain.AuditOperationClearCurrent, setupID, "", "",
		revision, revision, domain.AuditResultSucceeded, "", nil); err != nil {
		return finishLifecycleFailure(ctx, tx, claim, err)
	}
	result := map[string]any{"cleared": true, "setupId": setupID}
	if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
		return err
	}
	return databaseError(tx.Commit())
}

// TouchRecentSetup records an opened card or successfully displayed program
// without changing the setup itself. The UPSERT deduplicates by stable ID and
// the same transaction prunes the configured tail.
func (s *Service) TouchRecentSetup(ctx context.Context, setupID, artifactID string, line int64, idempotencyKey string) error {
	if line < 0 || line > 0 && artifactID == "" {
		return domain.NewError(domain.CodeInvalidContent, "recent preview line is invalid")
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return err
	}
	operation := "touchRecentSetup:" + setupID
	hash, err := idempotencyRequestHash(operation, struct {
		ArtifactID string `json:"artifactId"`
		Line       int64  `json:"line"`
	}{artifactID, line})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, idempotencyKey, operation, hash)
	if err != nil {
		return err
	}
	if replay, replayErr := claim.replayInto(nil); replayErr != nil {
		return replayErr
	} else if replay {
		return databaseError(tx.Commit())
	}
	if err := beginLifecycleMutation(ctx, tx); err != nil {
		return err
	}
	fail := func(operationErr error) error {
		return finishLifecycleFailure(ctx, tx, claim, operationErr)
	}
	if _, err := s.loadSetup(ctx, tx, setupID, false); err != nil {
		return fail(err)
	}
	if artifactID != "" {
		if _, err := s.loadArtifact(ctx, tx, setupID, artifactID); err != nil {
			return fail(err)
		}
	}
	now := sqlTimestamp(s.now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO recent_setups(
			library_id, setup_id, last_artifact_id, last_line, last_opened_at
		) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, 0), ?)
		ON CONFLICT(library_id, setup_id) DO UPDATE SET
			last_artifact_id = CASE
				WHEN excluded.last_artifact_id IS NULL THEN recent_setups.last_artifact_id
				ELSE excluded.last_artifact_id END,
			last_line = CASE
				WHEN excluded.last_artifact_id IS NULL THEN recent_setups.last_line
				ELSE excluded.last_line END,
			last_opened_at = excluded.last_opened_at`,
		s.libraryID, setupID, artifactID, line, now); err != nil {
		return fail(databaseError(err))
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM recent_setups
		 WHERE library_id = ? AND setup_id IN (
			SELECT setup_id FROM recent_setups
			 WHERE library_id = ?
			 ORDER BY last_opened_at DESC, setup_id DESC
			 LIMIT -1 OFFSET ?
		 )`, s.libraryID, s.libraryID, s.recentLimit); err != nil {
		return fail(databaseError(err))
	}
	if err := finishIdempotencyTx(ctx, tx, claim, 204, map[string]any{"updated": true}, nil); err != nil {
		return err
	}
	return databaseError(tx.Commit())
}

func (s *Service) ListRecentSetups(ctx context.Context) ([]domain.RecentSetup, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.library_id, r.setup_id, s.name, s.status,
		       r.last_artifact_id, r.last_line, r.last_opened_at
		  FROM recent_setups r
		  JOIN setups s ON s.id = r.setup_id AND s.library_id = r.library_id
		 WHERE r.library_id = ?
		 ORDER BY r.last_opened_at DESC, r.setup_id DESC
		 LIMIT ?`, s.libraryID, s.recentLimit)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := make([]domain.RecentSetup, 0)
	for rows.Next() {
		var recent domain.RecentSetup
		var artifact sql.NullString
		var status string
		var line sql.NullInt64
		var opened string
		if err := rows.Scan(&recent.LibraryID, &recent.SetupID, &recent.SetupName, &status, &artifact, &line, &opened); err != nil {
			return nil, databaseError(err)
		}
		recent.SetupStatus = domain.SetupStatus(status)
		if !recent.SetupStatus.Valid() {
			return nil, databaseError(errors.New("invalid recent setup status"))
		}
		recent.LastArtifactID = artifact.String
		if line.Valid {
			recent.LastLine = line.Int64
		}
		if recent.LastOpenedAt, err = parseTimestamp(opened); err != nil {
			return nil, databaseError(err)
		}
		result = append(result, recent)
	}
	return result, databaseError(rows.Err())
}

func (s *Service) DeleteRecentSetup(ctx context.Context, setupID, idempotencyKey string) error {
	if err := domain.ValidateID(setupID); err != nil {
		return err
	}
	return s.mutateRecentSetups(ctx, idempotencyKey, "deleteRecentSetup:"+setupID,
		map[string]string{"setupId": setupID}, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				"DELETE FROM recent_setups WHERE library_id = ? AND setup_id = ?", s.libraryID, setupID)
			return databaseError(err)
		})
}

func (s *Service) ClearRecentSetups(ctx context.Context, idempotencyKey string) error {
	return s.mutateRecentSetups(ctx, idempotencyKey, "clearRecentSetups", map[string]any{}, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM recent_setups WHERE library_id = ?", s.libraryID)
		return databaseError(err)
	})
}

func (s *Service) mutateRecentSetups(
	ctx context.Context, idempotencyKey, operation string, request any, mutate func(*sql.Tx) error,
) error {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return err
	}
	hash, err := idempotencyRequestHash(operation, request)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, idempotencyKey, operation, hash)
	if err != nil {
		return err
	}
	if replay, replayErr := claim.replayInto(nil); replayErr != nil {
		return replayErr
	} else if replay {
		return databaseError(tx.Commit())
	}
	if err := beginLifecycleMutation(ctx, tx); err != nil {
		return err
	}
	if err := mutate(tx); err != nil {
		return finishLifecycleFailure(ctx, tx, claim, err)
	}
	if err := finishIdempotencyTx(ctx, tx, claim, 204, map[string]any{"updated": true}, nil); err != nil {
		return err
	}
	return databaseError(tx.Commit())
}

func (s *Service) GetUIState(ctx context.Context, clientID string) (*UIState, error) {
	if err := validateClientID(clientID); err != nil {
		return nil, err
	}
	state := &UIState{ClientID: clientID, Screen: "library", Filters: json.RawMessage("{}"), View: json.RawMessage("{}")}
	var selectedSetup, selectedArtifact sql.NullString
	var filters, view, updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT screen, selected_setup_id, selected_artifact_id,
		       filters_json, view_json, updated_at
		  FROM ui_state
		 WHERE library_id = ? AND client_id = ?`, s.libraryID, clientID).Scan(
		&state.Screen, &selectedSetup, &selectedArtifact,
		&filters, &view, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return nil, databaseError(err)
	}
	state.SelectedSetupID = selectedSetup.String
	state.SelectedArtifactID = selectedArtifact.String
	state.Filters = json.RawMessage(filters)
	state.View = json.RawMessage(view)
	state.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return nil, databaseError(err)
	}
	return state, nil
}

func (s *Service) PutUIState(ctx context.Context, state UIState, idempotencyKey string) (*UIState, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	if err := validateClientID(state.ClientID); err != nil {
		return nil, err
	}
	if err := validateScreen(state.Screen); err != nil {
		return nil, err
	}
	filters, err := normalizeUIJSON(state.Filters)
	if err != nil {
		return nil, err
	}
	view, err := normalizeUIJSON(state.View)
	if err != nil {
		return nil, err
	}
	if state.SelectedArtifactID != "" && state.SelectedSetupID == "" {
		return nil, domain.NewError(domain.CodeInvalidContent, "selected artifact requires a selected setup")
	}
	operation := "putUIState:" + state.ClientID
	hash, err := idempotencyRequestHash(operation, struct {
		Screen             string          `json:"screen"`
		SelectedSetupID    string          `json:"selectedSetupId"`
		SelectedArtifactID string          `json:"selectedArtifactId"`
		Filters            json.RawMessage `json:"filters"`
		View               json.RawMessage `json:"view"`
	}{state.Screen, state.SelectedSetupID, state.SelectedArtifactID, filters, view})
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, idempotencyKey, operation, hash)
	if err != nil {
		return nil, err
	}
	var replay UIState
	if replayed, replayErr := claim.replayInto(&replay); replayErr != nil {
		return nil, replayErr
	} else if replayed {
		if err := tx.Commit(); err != nil {
			return nil, databaseError(err)
		}
		return &replay, nil
	}
	if err := beginLifecycleMutation(ctx, tx); err != nil {
		return nil, err
	}
	fail := func(operationErr error) (*UIState, error) {
		return nil, finishLifecycleFailure(ctx, tx, claim, operationErr)
	}
	if state.SelectedSetupID != "" {
		if _, err := s.loadSetup(ctx, tx, state.SelectedSetupID, false); err != nil {
			return fail(err)
		}
	}
	if state.SelectedArtifactID != "" {
		if _, err := s.loadArtifact(ctx, tx, state.SelectedSetupID, state.SelectedArtifactID); err != nil {
			return fail(err)
		}
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ui_state(
			library_id, client_id, screen, selected_setup_id,
			selected_artifact_id, filters_json, view_json, updated_at
		) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT(library_id, client_id) DO UPDATE SET
			screen = excluded.screen,
			selected_setup_id = excluded.selected_setup_id,
			selected_artifact_id = excluded.selected_artifact_id,
			filters_json = excluded.filters_json,
			view_json = excluded.view_json,
			updated_at = excluded.updated_at`,
		s.libraryID, state.ClientID, state.Screen, state.SelectedSetupID,
		state.SelectedArtifactID, string(filters), string(view), sqlTimestamp(now)); err != nil {
		return fail(databaseError(err))
	}
	state.Filters = filters
	state.View = view
	state.UpdatedAt = now
	if err := finishIdempotencyTx(ctx, tx, claim, 200, &state, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return &state, nil
}

func (s *Service) ListAuditEvents(ctx context.Context, setupID string, limit int) ([]domain.AuditEvent, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = defaultAuditLimit
	}
	if limit < 1 || limit > maximumAuditLimit {
		return nil, domain.NewError(domain.CodeInvalidContent, "audit history limit is invalid")
	}
	if _, err := s.loadSetup(ctx, s.db, setupID, false); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation, library_id, setup_id, artifact_id, job_id,
		       from_revision, to_revision, result, error_code, occurred_at
		  FROM audit_events
		 WHERE library_id = ? AND setup_id = ?
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT ?`, s.libraryID, setupID, limit)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := make([]domain.AuditEvent, 0)
	for rows.Next() {
		var event domain.AuditEvent
		var operation, auditResult, occurred string
		var artifactID, jobID, errorCode sql.NullString
		var before, after sql.NullInt64
		if err := rows.Scan(&event.ID, &operation, &event.LibraryID, &event.SetupID,
			&artifactID, &jobID, &before, &after, &auditResult, &errorCode, &occurred); err != nil {
			return nil, databaseError(err)
		}
		event.Operation = domain.AuditOperation(operation)
		event.ArtifactID = artifactID.String
		event.JobID = jobID.String
		if before.Valid {
			event.RevisionBefore = domain.Revision(before.Int64)
		}
		if after.Valid {
			event.RevisionAfter = domain.Revision(after.Int64)
		}
		event.Result = domain.AuditResult(auditResult)
		event.ErrorCode = domain.ErrorCode(errorCode.String)
		if event.CreatedAt, err = parseTimestamp(occurred); err != nil {
			return nil, databaseError(err)
		}
		result = append(result, event)
	}
	return result, databaseError(rows.Err())
}

func (s *Service) ArchiveSetup(ctx context.Context, setupID string, input ArchiveInput) (*domain.Setup, error) {
	return s.changeArchiveState(ctx, setupID, input, false, "", nil)
}

// RestoreSetupJob creates a durable, cancellable job before full object
// verification starts. Restoring a setup with multi-gigabyte programs is a
// long operation even though its final catalog mutation is small.
func (s *Service) RestoreSetupJob(ctx context.Context, setupID string, input ArchiveInput) (*domain.Job, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	hash, err := idempotencyRequestHash("restoreSetupJob", struct {
		SetupID          string          `json:"setupId"`
		ExpectedRevision domain.Revision `json:"expectedRevision"`
	}{setupID, input.ExpectedRevision})
	if err != nil {
		return nil, err
	}

	var job *domain.Job
	created := false
	err = s.withSetupLock(setupID, func() error {
		tx, beginErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if beginErr != nil {
			return databaseError(beginErr)
		}
		defer tx.Rollback()
		claim, claimErr := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "restoreSetupJob", hash)
		if claimErr != nil {
			return claimErr
		}
		var replay domain.Job
		if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
			if commitErr := tx.Commit(); commitErr != nil {
				return databaseError(commitErr)
			}
			if replayErr != nil {
				return replayErr
			}
			current, loadErr := s.GetJob(ctx, replay.ID)
			if loadErr != nil {
				return loadErr
			}
			job = current
			return nil
		}
		setup, loadErr := s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return finishRestoreClaimFailure(ctx, tx, claim, loadErr)
		}
		if loadErr = domain.CheckExpectedRevision(setup.Revision, input.ExpectedRevision); loadErr != nil {
			return finishRestoreClaimFailure(ctx, tx, claim, loadErr)
		}
		if setup.ArchivedFromStatus == nil {
			return finishRestoreClaimFailure(ctx, tx, claim,
				domain.NewError(domain.CodeInvalidSetupState, "setup cannot be restored in its current state"))
		}
		records, loadErr := s.loadArtifacts(ctx, tx, setupID)
		if loadErr != nil {
			return finishRestoreClaimFailure(ctx, tx, claim, loadErr)
		}
		var totalBytes int64
		for _, record := range records {
			if record.ByteSize > 0 && totalBytes > int64(^uint64(0)>>1)-record.ByteSize {
				return finishRestoreClaimFailure(ctx, tx, claim,
					domain.NewError(domain.CodeInvalidContent, "restore byte total exceeds supported range"))
			}
			totalBytes += record.ByteSize
		}
		job, loadErr = s.insertJobTx(ctx, tx, domain.JobKindRestore, setupID, "", &totalBytes)
		if loadErr != nil {
			return finishRestoreClaimFailure(ctx, tx, claim, loadErr)
		}
		created = true
		if finishErr := finishIdempotencyTx(ctx, tx, claim, 202, job, nil); finishErr != nil {
			return finishErr
		}
		return databaseError(tx.Commit())
	})
	if err != nil || job == nil || !created {
		return job, err
	}
	s.launchJob(job.ID, func(jobCtx context.Context, progress func(domain.JobProgress) error) (any, error) {
		return s.changeArchiveState(jobCtx, setupID, ArchiveInput{
			ExpectedRevision: input.ExpectedRevision,
			IdempotencyKey:   "restore-job-" + job.ID,
		}, true, job.ID, progress)
	})
	return job, nil
}

func finishRestoreClaimFailure(ctx context.Context, tx *sql.Tx, claim idempotencyClaim, operationErr error) error {
	if finishErr := finishIdempotencyTx(ctx, tx, claim, 0, nil, operationErr); finishErr != nil {
		_ = tx.Rollback()
		return finishErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return databaseError(commitErr)
	}
	return operationErr
}

func (s *Service) RestoreSetup(ctx context.Context, setupID string, input ArchiveInput) (*domain.Setup, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	release, err := s.acquireHeavy(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer release()
	return s.changeArchiveState(ctx, setupID, input, true, "", nil)
}

func (s *Service) changeArchiveState(
	ctx context.Context,
	setupID string,
	input ArchiveInput,
	restore bool,
	jobID string,
	updateProgress func(domain.JobProgress) error,
) (*domain.Setup, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	operationName := "archiveSetup"
	auditOperation := domain.AuditOperationArchive
	if restore {
		operationName = "restoreSetup"
		auditOperation = domain.AuditOperationRestore
	}
	hash, err := idempotencyRequestHash(operationName, struct {
		SetupID          string          `json:"setupId"`
		ExpectedRevision domain.Revision `json:"expectedRevision"`
	}{setupID, input.ExpectedRevision})
	if err != nil {
		return nil, databaseError(err)
	}
	var result *domain.Setup
	err = s.withSetupLock(setupID, func() error {
		var verifiedRecords []artifactRecord
		contentChanged := false
		if restore {
			// Hash immutable objects outside a SQLite write transaction. The setup
			// lock freezes domain mutations; the revision and exact artifact
			// snapshot are checked again after the short write transaction begins.
			before, loadErr := s.loadSetup(ctx, s.db, setupID, true)
			if loadErr != nil {
				return loadErr
			}
			if loadErr = domain.CheckExpectedRevision(before.Revision, input.ExpectedRevision); loadErr != nil {
				return loadErr
			}
			if before.ArchivedFromStatus == nil {
				return domain.NewError(domain.CodeInvalidSetupState, "setup cannot be restored in its current state")
			}
			verifiedRecords, loadErr = s.loadArtifacts(ctx, s.db, setupID)
			if loadErr != nil {
				return loadErr
			}
			var totalBytes, completedBytes int64
			for _, record := range verifiedRecords {
				totalBytes += record.ByteSize
			}
			lastReportedBytes := int64(0)
			lastReportedAt := time.Now()
			for index, record := range verifiedRecords {
				objectStart := completedBytes
				_, inspectErr := s.objects.VerifyObjectWithProgress(ctx, record.StorageKey, record.SHA256, record.Version, func(objectBytes int64) error {
					if updateProgress == nil {
						return nil
					}
					currentBytes := objectStart + objectBytes
					if currentBytes != totalBytes && currentBytes-lastReportedBytes < 1<<20 && time.Since(lastReportedAt) < 250*time.Millisecond {
						return nil
					}
					if progressErr := updateProgress(domain.JobProgress{
						CompletedBytes: currentBytes, TotalBytes: totalBytes,
						CompletedItems: int64(index), TotalItems: int64(len(verifiedRecords)),
					}); progressErr != nil {
						return progressErr
					}
					lastReportedBytes, lastReportedAt = currentBytes, time.Now()
					return nil
				})
				if inspectErr != nil {
					if ctx.Err() != nil {
						return storageError(ctx.Err())
					}
					mapped := contentStorageError(inspectErr)
					if !domain.IsErrorCode(mapped, domain.CodeArtifactChanged) {
						return mapped
					}
					contentChanged = true
					break
				}
				completedBytes += record.ByteSize
				if updateProgress != nil {
					if progressErr := updateProgress(domain.JobProgress{
						CompletedBytes: completedBytes, TotalBytes: totalBytes,
						CompletedItems: int64(index + 1), TotalItems: int64(len(verifiedRecords)),
					}); progressErr != nil {
						return progressErr
					}
					lastReportedBytes, lastReportedAt = completedBytes, time.Now()
				}
			}
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, operationName, hash)
		if err != nil {
			return err
		}
		var replay domain.Setup
		if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
			if err := tx.Commit(); err != nil {
				return databaseError(err)
			}
			if replayErr != nil {
				return replayErr
			}
			result = &replay
			return nil
		}
		if err := beginLifecycleMutation(ctx, tx); err != nil {
			return err
		}
		setup, err := s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := domain.CheckExpectedRevision(setup.Revision, input.ExpectedRevision); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}

		targetStatus := domain.SetupStatusArchived
		archivedFrom := setup.Status
		attentionReason := ""
		if restore {
			if setup.ArchivedFromStatus == nil {
				return finishLifecycleFailure(ctx, tx, claim,
					domain.NewError(domain.CodeInvalidSetupState, "setup cannot be restored in its current state"))
			}
			var archivedAttention sql.NullString
			if err := tx.QueryRowContext(ctx,
				"SELECT attention_reason FROM setups WHERE library_id = ? AND id = ?",
				s.libraryID, setupID).Scan(&archivedAttention); err != nil {
				return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
			}
			records, loadErr := s.loadArtifacts(ctx, tx, setupID)
			if loadErr != nil {
				return finishLifecycleFailure(ctx, tx, claim, loadErr)
			}
			if !sameArtifactSnapshot(records, verifiedRecords) {
				return finishLifecycleFailure(ctx, tx, claim,
					domain.NewError(domain.CodeRevisionConflict, "setup artifacts changed during restore"))
			}
			targetStatus, err = domain.RestoreStatus(setup.Status, *setup.ArchivedFromStatus, contentChanged)
			if err != nil {
				return finishLifecycleFailure(ctx, tx, claim, err)
			}
			if contentChanged {
				attentionReason = "managed content changed while setup was archived"
			} else if targetStatus == domain.SetupStatusAttention {
				attentionReason = archivedAttention.String
			}
		} else {
			var previous domain.SetupStatus
			targetStatus, previous, err = domain.ArchiveStatus(setup.Status)
			if err != nil {
				return finishLifecycleFailure(ctx, tx, claim, err)
			}
			archivedFrom = previous
			var current int
			if err := tx.QueryRowContext(ctx,
				"SELECT count(*) FROM current_setup WHERE library_id = ? AND setup_id = ?",
				s.libraryID, setupID).Scan(&current); err != nil {
				return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
			}
			if current != 0 {
				return finishLifecycleFailure(ctx, tx, claim,
					domain.NewError(domain.CodeCurrentSetupConflict, "current setup must be cleared before archiving"))
			}
		}

		journalID, err := s.appendJournal(ctx, tx, auditOperation, setupID, "", "", "", jobID,
			input.ExpectedRevision, map[string]any{"restoring": restore})
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		now := sqlTimestamp(s.now())
		if restore {
			readyRevision := any(nil)
			if targetStatus == domain.SetupStatusReady {
				readyRevision = setup.Revision
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE setups
				   SET status = ?, archived_from_status = NULL, archived_at = NULL,
				       attention_reason = NULLIF(?, ''), ready_revision = ?, updated_at = ?
				 WHERE library_id = ? AND id = ? AND revision = ? AND status = 'archived'`,
				targetStatus, attentionReason, readyRevision, now,
				s.libraryID, setupID, input.ExpectedRevision); err != nil {
				return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
				UPDATE setups
				   SET status = 'archived', archived_from_status = ?, archived_at = ?, updated_at = ?
				 WHERE library_id = ? AND id = ? AND revision = ? AND status <> 'archived'`,
				archivedFrom, now, now, s.libraryID, setupID, input.ExpectedRevision); err != nil {
				return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
			}
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM delete_confirmations WHERE library_id = ? AND setup_id = ?", s.libraryID, setupID,
		); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
		}
		if err := completeJournal(ctx, tx, journalID, setup.Revision); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := s.appendAudit(ctx, tx, auditOperation, setupID, "", jobID,
			setup.Revision, setup.Revision, domain.AuditResultSucceeded, "", nil); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		result, err = s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return err
		}
		if jobID != "" {
			if err := s.finishJobTx(ctx, tx, jobID, result, nil); err != nil {
				return err
			}
		}
		return databaseError(tx.Commit())
	})
	return result, err
}

func sameArtifactSnapshot(current, verified []artifactRecord) bool {
	if len(current) != len(verified) {
		return false
	}
	for index := range current {
		left, right := current[index], verified[index]
		if left.ID != right.ID || left.SetupID != right.SetupID || left.Role != right.Role ||
			left.DisplayName != right.DisplayName || left.MediaType != right.MediaType ||
			left.ByteSize != right.ByteSize || left.SHA256 != right.SHA256 || left.Version != right.Version ||
			left.Position != right.Position || left.Primary != right.Primary ||
			left.StorageObjectID != right.StorageObjectID || left.StorageKey != right.StorageKey {
			return false
		}
	}
	return true
}

func (s *Service) CreateDeletePlan(ctx context.Context, setupID string, expected domain.Revision, idempotencyKey string) (*DeletePlan, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if !expected.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	operation := "createDeletePlan:" + setupID
	hash, err := idempotencyRequestHash(operation, map[string]any{"expectedRevision": expected})
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, idempotencyKey, operation, hash)
	if err != nil {
		return nil, err
	}
	var replay DeletePlan
	if replayed, replayErr := claim.replayInto(&replay); replayErr != nil {
		return nil, replayErr
	} else if replayed {
		if err := tx.Commit(); err != nil {
			return nil, databaseError(err)
		}
		return &replay, nil
	}
	if err := beginLifecycleMutation(ctx, tx); err != nil {
		return nil, err
	}
	fail := func(operationErr error) (*DeletePlan, error) {
		return nil, finishLifecycleFailure(ctx, tx, claim, operationErr)
	}
	setup, err := s.loadSetup(ctx, tx, setupID, false)
	if err != nil {
		return fail(err)
	}
	if err := domain.CheckExpectedRevision(setup.Revision, expected); err != nil {
		return fail(err)
	}
	if setup.Status != domain.SetupStatusArchived {
		return fail(domain.NewError(domain.CodeInvalidSetupState, "only an archived setup can be permanently deleted"))
	}
	programCount, hasSheet, uniqueBytes, err := s.deletePlanFacts(ctx, tx, setupID)
	if err != nil {
		return fail(err)
	}
	token, err := domain.NewID()
	if err != nil {
		return fail(databaseError(err))
	}
	tokenHash := sha256.Sum256([]byte(token))
	expires := s.now().UTC().Add(s.deleteConfirmationTTL)
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM delete_confirmations WHERE library_id = ? AND expires_at <= ?`,
		s.libraryID, sqlTimestamp(s.now())); err != nil {
		return fail(databaseError(err))
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO delete_confirmations(
			token_hash, library_id, setup_id, revision, exact_name,
			program_count, has_setup_sheet, unique_bytes, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hex.EncodeToString(tokenHash[:]), s.libraryID, setupID, expected, setup.Name,
		programCount, boolInteger(hasSheet), uniqueBytes, sqlTimestamp(expires)); err != nil {
		return fail(databaseError(err))
	}
	plan := &DeletePlan{
		ConfirmationToken: token, SetupID: setupID, Revision: expected,
		ExactName: setup.Name, ProgramCount: programCount,
		HasSetupSheet: hasSheet, UniqueBytes: uniqueBytes, ExpiresAt: expires,
	}
	if err := finishIdempotencyTx(ctx, tx, claim, 200, plan, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return plan, nil
}

// PermanentDeleteSetup removes only an archived aggregate after a one-use,
// short-lived confirmation bound to its exact name and revision. Unshared
// objects become ref-count-zero candidates only after the aggregate transaction
// commits; the centralized GarbageCollect operation performs physical unlink.
func (s *Service) PermanentDeleteSetup(ctx context.Context, setupID string, input PermanentDeleteInput) error {
	if err := domain.ValidateID(setupID); err != nil {
		return err
	}
	if !input.ExpectedRevision.Valid() {
		return domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return err
	}
	if !domain.IsValidID(input.ConfirmationToken) {
		return domain.NewError(domain.CodeInvalidContent, "delete confirmation token is invalid")
	}
	hash, err := idempotencyRequestHash("permanentDelete", struct {
		SetupID          string          `json:"setupId"`
		ExpectedRevision domain.Revision `json:"expectedRevision"`
		ExactName        string          `json:"exactName"`
		Token            string          `json:"confirmationToken"`
	}{setupID, input.ExpectedRevision, input.ExactName, input.ConfirmationToken})
	if err != nil {
		return databaseError(err)
	}
	err = s.withSetupLock(setupID, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "permanentDelete", hash)
		if err != nil {
			return err
		}
		var replay map[string]any
		if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
			if err := tx.Commit(); err != nil {
				return databaseError(err)
			}
			return replayErr
		}
		if err := beginLifecycleMutation(ctx, tx); err != nil {
			return err
		}
		setup, err := s.loadSetup(ctx, tx, setupID, false)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := domain.CheckExpectedRevision(setup.Revision, input.ExpectedRevision); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if setup.Status != domain.SetupStatusArchived {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeInvalidSetupState, "only an archived setup can be permanently deleted"))
		}
		if subtle.ConstantTimeCompare([]byte(setup.Name), []byte(input.ExactName)) != 1 {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeInvalidContent, "exact setup name confirmation does not match"))
		}
		tokenHash := sha256.Sum256([]byte(input.ConfirmationToken))
		var revision domain.Revision
		var exactName, expires string
		var programs, sheet int
		var uniqueBytes int64
		err = tx.QueryRowContext(ctx, `
			SELECT revision, exact_name, program_count, has_setup_sheet,
			       unique_bytes, expires_at
			  FROM delete_confirmations
			 WHERE token_hash = ? AND library_id = ? AND setup_id = ?
			   AND consumed_at IS NULL`,
			hex.EncodeToString(tokenHash[:]), s.libraryID, setupID).Scan(
			&revision, &exactName, &programs, &sheet, &uniqueBytes, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeConfirmationExpired, "delete confirmation is missing or expired"))
		}
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
		}
		expiresAt, err := parseTimestamp(expires)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
		}
		if !s.now().Before(expiresAt) || revision != input.ExpectedRevision ||
			subtle.ConstantTimeCompare([]byte(exactName), []byte(input.ExactName)) != 1 {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeConfirmationExpired, "delete confirmation is missing or expired"))
		}
		actualPrograms, actualSheet, actualBytes, err := s.deletePlanFacts(ctx, tx, setupID)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if actualPrograms != programs || boolInteger(actualSheet) != sheet || actualBytes != uniqueBytes {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeRevisionConflict, "setup composition changed after delete confirmation"))
		}
		journalID, err := s.appendJournal(ctx, tx, domain.AuditOperationPermanentDelete,
			setupID, "", "", "", "", input.ExpectedRevision,
			map[string]any{"programCount": programs, "hasSetupSheet": actualSheet})
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := s.appendAudit(ctx, tx, domain.AuditOperationPermanentDelete, setupID,
			"", "", input.ExpectedRevision, input.ExpectedRevision,
			domain.AuditResultSucceeded, "", map[string]any{"uniqueBytes": uniqueBytes}); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM setups WHERE library_id = ? AND id = ?",
			s.libraryID, setupID); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
		}
		if err := completeJournal(ctx, tx, journalID, input.ExpectedRevision); err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		response := map[string]any{"deleted": true, "setupId": setupID}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, response, nil); err != nil {
			return err
		}
		return databaseError(tx.Commit())
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) deletePlanFacts(ctx context.Context, q queryer, setupID string) (int, bool, int64, error) {
	var programs, sheet int
	if err := q.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE role = 'program'),
		       count(*) FILTER (WHERE role = 'setup_sheet')
		  FROM setup_artifacts WHERE setup_id = ?`, setupID).Scan(&programs, &sheet); err != nil {
		return 0, false, 0, databaseError(err)
	}
	var uniqueBytes int64
	if err := q.QueryRowContext(ctx, `
		WITH setup_refs AS (
			SELECT o.id, o.byte_size, o.ref_count, count(a.id) AS local_refs
			  FROM storage_objects o
			  JOIN setup_artifacts a ON a.storage_object_id = o.id
			 WHERE a.setup_id = ?
			 GROUP BY o.id, o.byte_size, o.ref_count
		)
		SELECT coalesce(sum(CASE
		       WHEN ref_count = local_refs
		        AND NOT EXISTS (
		            SELECT 1 FROM import_artifacts i
		             WHERE i.storage_object_id = setup_refs.id)
		        AND NOT EXISTS (
		            SELECT 1 FROM operation_journal j
		             WHERE j.storage_object_id = setup_refs.id
		               AND j.state IN ('intent', 'storage_applied', 'db_applied'))
		       THEN byte_size ELSE 0 END), 0)
		  FROM setup_refs`, setupID).Scan(&uniqueBytes); err != nil {
		return 0, false, 0, databaseError(err)
	}
	return programs, sheet != 0, uniqueBytes, nil
}

func validateClientID(value string) error {
	if len(value) < 1 || len(value) > maximumClientIDBytes || strings.TrimSpace(value) != value {
		return domain.NewError(domain.CodeInvalidContent, "UI client ID is invalid")
	}
	for _, character := range value {
		if character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || strings.ContainsRune("._:-", character)) {
			return domain.NewError(domain.CodeInvalidContent, "UI client ID is invalid")
		}
	}
	return nil
}

func validateScreen(value string) error {
	if len(value) < 1 || len(value) > maximumScreenBytes || strings.TrimSpace(value) != value {
		return domain.NewError(domain.CodeInvalidContent, "UI screen is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return domain.NewError(domain.CodeInvalidContent, "UI screen is invalid")
		}
	}
	return nil
}

func normalizeUIJSON(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage("{}"), nil
	}
	if len(value) > maximumUIJSONBytes {
		return nil, domain.NewError(domain.CodeInvalidContent, "UI state JSON is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, domain.NewError(domain.CodeInvalidContent, "UI state JSON must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, domain.NewError(domain.CodeInvalidContent, "UI state JSON must contain one object")
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, domain.NewError(domain.CodeInvalidContent, "UI state JSON is invalid")
	}
	return normalized, nil
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
