package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

var errImportLimitExceeded = errors.New("import session byte limit exceeded")

type importArtifactRecord struct {
	domain.ImportArtifact
	NormalizedName  string
	StagingKey      string
	MediaType       string
	SHA256          string
	Version         string
	StorageObjectID string
	StorageKey      string
}

type importUploadReservation struct {
	ArtifactID   string
	JobID        string
	Existing     bool
	BytesAlready int64
	BytesAllowed int64
}

func (s *Service) StartImport(ctx context.Context, input StartImportInput) (*domain.ImportSession, error) {
	name, err := domain.NormalizeSetupName(input.Name)
	if err != nil {
		return nil, err
	}
	if err := validateDescription(input.Description); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	hash, err := idempotencyRequestHash("startImport", map[string]any{"name": name, "description": input.Description})
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "startImport", hash)
	if err != nil {
		return nil, err
	}
	var replay domain.ImportSession
	if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
		return nil, replayErr
	} else if ok {
		if err := tx.Commit(); err != nil {
			return nil, databaseError(err)
		}
		return &replay, nil
	}
	if savepointErr := beginLifecycleMutation(ctx, tx); savepointErr != nil {
		return nil, savepointErr
	}
	fail := func(operationErr error) (*domain.ImportSession, error) {
		return nil, finishLifecycleFailure(ctx, tx, claim, operationErr)
	}

	importID, err := domain.NewImportID()
	if err != nil {
		return fail(err)
	}
	jobID, err := domain.NewJobID()
	if err != nil {
		return fail(err)
	}
	expires := s.now().UTC().Add(s.importSessionExpiry)
	var byteLimit any
	if s.importTotalLimit > 0 {
		byteLimit = s.importTotalLimit
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO import_sessions(id, library_id, idempotency_key, setup_name, setup_description,
		                            state, bytes_received, byte_limit, expires_at)
		VALUES (?, ?, ?, ?, ?, 'staging', 0, ?, ?)`, importID, s.libraryID, input.IdempotencyKey,
		name, input.Description, byteLimit, sqlTimestamp(expires))
	if err != nil {
		return fail(databaseError(err))
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO jobs(id, library_id, kind, import_session_id, state, progress, bytes_done, started_at)
		VALUES (?, ?, ?, ?, 'running', 0, 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
		jobID, s.libraryID, domain.JobKindImport, importID)
	if err != nil {
		return fail(databaseError(err))
	}
	result, err := s.loadImport(ctx, tx, importID)
	if err != nil {
		return fail(err)
	}
	if err := finishIdempotencyTx(ctx, tx, claim, 201, result, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}

func (s *Service) GetImport(ctx context.Context, sessionID string) (*domain.ImportSession, error) {
	return s.loadImport(ctx, s.db, sessionID)
}

func (s *Service) UploadImportArtifact(ctx context.Context, sessionID string, input UploadArtifactInput) (*domain.ImportArtifact, error) {
	if err := domain.ValidateID(sessionID); err != nil {
		return nil, err
	}
	if !input.Role.Valid() || input.Content == nil {
		return nil, domain.NewError(domain.CodeInvalidContent, "artifact role and content are required")
	}
	name, err := domain.NormalizeArtifactName(input.DisplayName)
	if err != nil {
		return nil, err
	}
	normalizedName, err := domain.ArtifactNameKey(name)
	if err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	input.DisplayName = name

	reservation, err := s.reserveImportUpload(ctx, sessionID, input, normalizedName)
	if err != nil {
		return nil, err
	}
	reader := io.Reader(&importCancellationReader{ctx: ctx, service: s, sessionID: sessionID, source: input.Content})
	if reservation.BytesAllowed >= 0 {
		reader = &importLimitReader{source: reader, remaining: reservation.BytesAllowed}
	}
	input.Content = reader
	prepared, err := s.prepareArtifact(ctx, input)
	if err != nil {
		if !reservation.Existing {
			s.recordImportUploadFailure(context.Background(), sessionID, reservation.ArtifactID, err)
		}
		return nil, err
	}
	cleanup := func() { s.cleanupPreparedObjects(context.Background(), []preparedArtifact{*prepared}) }
	if err := s.persistPreparedObject(ctx, prepared); err != nil {
		cleanup()
		if !reservation.Existing {
			s.recordImportUploadFailure(context.Background(), sessionID, reservation.ArtifactID, err)
		}
		return nil, err
	}
	operationName := "uploadImportArtifact:" + sessionID
	hash, err := idempotencyRequestHash(operationName, map[string]any{
		"role": prepared.Role, "name": prepared.DisplayName, "size": prepared.Object.Size,
		"sha256": prepared.Object.SHA256,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, operationName, hash)
	if err != nil {
		cleanup()
		return nil, err
	}
	var replay domain.ImportArtifact
	if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
		cleanup()
		return nil, replayErr
	} else if ok {
		cleanup()
		return &replay, nil
	}

	var result *domain.ImportArtifact
	operationErr := s.withSetupLock("import:"+sessionID, func() error {
		tx, beginErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if beginErr != nil {
			return databaseError(beginErr)
		}
		defer tx.Rollback()
		session, loadErr := s.loadImport(ctx, tx, sessionID)
		if loadErr != nil {
			return loadErr
		}
		if session.State != domain.ImportStateStaging {
			return domain.NewError(domain.CodeInvalidSetupState, "import session no longer accepts uploads")
		}
		if reservation.Existing {
			return domain.NewError(domain.CodeNameConflict, "an artifact with this name already exists in the import")
		}
		var state string
		if queryErr := tx.QueryRowContext(ctx, `
			SELECT state FROM import_artifacts WHERE id = ? AND import_session_id = ?`,
			reservation.ArtifactID, sessionID).Scan(&state); queryErr != nil {
			return databaseError(queryErr)
		}
		if state != string(domain.ImportArtifactUploading) {
			return domain.NewError(domain.CodeJobCancelled, "artifact upload was cancelled")
		}
		var activeBytes int64
		if queryErr := tx.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(byte_size), 0) FROM import_artifacts
			 WHERE import_session_id = ? AND state IN ('staged', 'published')`, sessionID).Scan(&activeBytes); queryErr != nil {
			return databaseError(queryErr)
		}
		if s.importTotalLimit > 0 && (prepared.Object.Size > s.importTotalLimit-activeBytes) {
			operationErr := domain.NewError(domain.CodeImportTooLarge, "import exceeds the configured total limit")
			if _, updateErr := tx.ExecContext(ctx, `
				UPDATE import_artifacts SET state = 'failed', error_code = ?,
				       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
				operationErr.Code, reservation.ArtifactID); updateErr != nil {
				return databaseError(updateErr)
			}
			return operationErr
		}
		if objectErr := s.ensurePreparedObjectTx(ctx, tx, prepared); objectErr != nil {
			return objectErr
		}
		journalID, journalErr := s.appendJournal(ctx, tx, domain.AuditOperationImport, "", "", prepared.StorageObjectID,
			sessionID, reservation.JobID, 0, map[string]any{"importArtifactId": reservation.ArtifactID})
		if journalErr != nil {
			return journalErr
		}
		updated, updateErr := tx.ExecContext(ctx, `
			UPDATE import_artifacts
			   SET staging_key = ?, media_type = ?, byte_size = ?, sha256 = ?, object_version = ?,
			       state = 'staged', storage_object_id = ?, error_code = NULL,
			       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND import_session_id = ? AND state = 'uploading'`,
			prepared.Object.Key, prepared.MediaType, prepared.Object.Size, prepared.Object.SHA256,
			prepared.Object.Version, prepared.StorageObjectID, reservation.ArtifactID, sessionID)
		if updateErr != nil {
			return databaseError(updateErr)
		}
		if changed, rowsErr := updated.RowsAffected(); rowsErr != nil || changed != 1 {
			return domain.NewError(domain.CodeJobCancelled, "artifact upload was cancelled")
		}
		newBytes := activeBytes + prepared.Object.Size
		if _, updateErr := tx.ExecContext(ctx, `
			UPDATE import_sessions SET bytes_received = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND state = 'staging'`, newBytes, sessionID); updateErr != nil {
			return databaseError(updateErr)
		}
		if _, updateErr := tx.ExecContext(ctx, `
			UPDATE jobs SET bytes_done = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND state = 'running'`, newBytes, reservation.JobID); updateErr != nil {
			return databaseError(updateErr)
		}
		if journalErr := completeJournal(ctx, tx, journalID, 0); journalErr != nil {
			return journalErr
		}
		record, loadErr := s.loadImportArtifact(ctx, tx, sessionID, reservation.ArtifactID)
		if loadErr != nil {
			return loadErr
		}
		result = &record.ImportArtifact
		if finishErr := finishIdempotencyTx(ctx, tx, claim, 201, result, nil); finishErr != nil {
			return finishErr
		}
		return databaseError(tx.Commit())
	})
	if operationErr != nil {
		cleanup()
		if !reservation.Existing {
			s.recordImportUploadFailure(context.Background(), sessionID, reservation.ArtifactID, operationErr)
		}
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if finishErr := s.finishIdempotency(finishCtx, claim, 0, nil, operationErr); finishErr != nil {
			return nil, finishErr
		}
		return nil, operationErr
	}
	return result, nil
}

func (s *Service) ExcludeImportArtifact(ctx context.Context, sessionID, importArtifactID, idempotencyKey string) (*domain.ImportSession, error) {
	if err := domain.ValidateID(sessionID); err != nil {
		return nil, err
	}
	if err := domain.ValidateID(importArtifactID); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	operation := "excludeImportArtifact:" + importArtifactID
	hash, err := idempotencyRequestHash(operation, map[string]string{"sessionId": sessionID})
	if err != nil {
		return nil, err
	}
	var result *domain.ImportSession
	var candidate storageCandidate
	err = s.withSetupLock("import:"+sessionID, func() error {
		tx, beginErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if beginErr != nil {
			return databaseError(beginErr)
		}
		defer tx.Rollback()
		claim, claimErr := s.claimIdempotencyTx(ctx, tx, idempotencyKey, operation, hash)
		if claimErr != nil {
			return claimErr
		}
		var replay domain.ImportSession
		if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
			return replayErr
		} else if ok {
			if commitErr := tx.Commit(); commitErr != nil {
				return databaseError(commitErr)
			}
			result = &replay
			return nil
		}
		if savepointErr := beginLifecycleMutation(ctx, tx); savepointErr != nil {
			return savepointErr
		}
		fail := func(operationErr error) error {
			return finishLifecycleFailure(ctx, tx, claim, operationErr)
		}
		session, loadErr := s.loadImport(ctx, tx, sessionID)
		if loadErr != nil {
			return fail(loadErr)
		}
		if session.State != domain.ImportStateStaging {
			return fail(domain.NewError(domain.CodeInvalidSetupState, "import session is not staging"))
		}
		record, loadErr := s.loadImportArtifact(ctx, tx, sessionID, importArtifactID)
		if loadErr != nil {
			return fail(loadErr)
		}
		candidate = storageCandidate{ID: record.StorageObjectID, Key: record.StorageKey, SHA256: record.SHA256}
		if _, updateErr := tx.ExecContext(ctx, `
			UPDATE import_artifacts SET state = 'excluded', storage_object_id = NULL,
			       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND import_session_id = ?`, importArtifactID, sessionID); updateErr != nil {
			return fail(databaseError(updateErr))
		}
		if updateErr := s.recalculateImportBytes(ctx, tx, sessionID); updateErr != nil {
			return fail(updateErr)
		}
		result, loadErr = s.loadImport(ctx, tx, sessionID)
		if loadErr != nil {
			return fail(loadErr)
		}
		if finishErr := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); finishErr != nil {
			return finishErr
		}
		return databaseError(tx.Commit())
	})
	if err == nil {
		s.cleanupStorageCandidates(context.Background(), []storageCandidate{candidate})
	}
	return result, err
}

func (s *Service) CommitImport(ctx context.Context, sessionID string, input CommitImportInput) (*domain.Setup, error) {
	if err := domain.ValidateID(sessionID); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	expectedIDs := sortedStrings(input.ExpectedArtifactIDs)
	for index, id := range expectedIDs {
		if err := domain.ValidateID(id); err != nil {
			return nil, err
		}
		if index > 0 && id == expectedIDs[index-1] {
			return nil, domain.NewError(domain.CodeInvalidContent, "expected import artifact IDs contain duplicates")
		}
	}
	if input.PrimaryArtifactID != "" {
		if err := domain.ValidateID(input.PrimaryArtifactID); err != nil {
			return nil, err
		}
	}
	operation := "commitImport:" + sessionID
	hash, err := idempotencyRequestHash(operation, map[string]any{
		"artifactIds": expectedIDs, "primaryArtifactId": input.PrimaryArtifactID,
		"savePartialDraft": input.SavePartialDraft,
	})
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, operation, hash)
	if err != nil {
		return nil, err
	}
	var replay domain.Setup
	if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
		return nil, replayErr
	} else if ok {
		return &replay, nil
	}

	var result *domain.Setup
	operationErr := s.withSetupLock("import:"+sessionID, func() error {
		session, err := s.loadImport(ctx, s.db, sessionID)
		if err != nil {
			return err
		}
		if session.State != domain.ImportStateStaging {
			return domain.NewError(domain.CodeInvalidSetupState, "import session was already finalized")
		}
		operationCtx, stopMonitoring := s.monitorImportContext(ctx, sessionID)
		defer stopMonitoring()
		allRecords, err := s.loadImportArtifactRecords(ctx, s.db, sessionID)
		if err != nil {
			return err
		}
		active := make([]importArtifactRecord, 0, len(allRecords))
		incomplete := false
		for _, record := range allRecords {
			switch record.State {
			case domain.ImportArtifactStaged:
				active = append(active, record)
			case domain.ImportArtifactExcluded:
			default:
				incomplete = true
			}
		}
		activeIDs := make([]string, len(active))
		for index := range active {
			activeIDs[index] = active[index].ID
		}
		if !sameStrings(sortedStrings(activeIDs), expectedIDs) {
			return domain.NewError(domain.CodeUploadIncomplete, "confirmed artifact set does not match staged artifacts")
		}
		if incomplete && !input.SavePartialDraft {
			return domain.NewError(domain.CodeUploadIncomplete, "one or more import artifacts are incomplete")
		}

		programCount := 0
		primaryFound := false
		prepared := make(map[string]preparedArtifact, len(active))
		if err := s.withImportVerificationSlot(operationCtx, func() error {
			for _, record := range active {
				if record.Role == domain.ArtifactRoleProgram {
					programCount++
				}
				if record.ID == input.PrimaryArtifactID {
					if record.Role != domain.ArtifactRoleProgram {
						return domain.NewError(domain.CodeInvalidContent, "primary import artifact must be a G-code program")
					}
					primaryFound = true
				}
				object, verifyErr := s.objects.VerifyObject(operationCtx, record.StorageKey, record.SHA256, record.Version)
				if verifyErr != nil {
					return storageError(verifyErr)
				}
				prepared[record.ID] = preparedArtifact{
					Role: record.Role, DisplayName: record.DisplayName, NormalizedName: record.NormalizedName,
					MediaType: record.MediaType, Object: object, StorageObjectID: record.StorageObjectID,
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if !input.SavePartialDraft && programCount == 0 {
			return domain.NewError(domain.CodeUploadIncomplete, "a completed import requires at least one G-code program")
		}
		if input.PrimaryArtifactID != "" && !primaryFound {
			return domain.NewError(domain.CodeUploadIncomplete, "primary artifact is not in the confirmed import set")
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		currentSession, err := s.loadImport(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if currentSession.State != domain.ImportStateStaging || currentSession.SetupID != "" {
			return domain.NewError(domain.CodeInvalidSetupState, "import session was already finalized")
		}
		currentRecords, err := s.loadImportArtifactRecords(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		currentActive := make([]string, 0, len(active))
		for _, record := range currentRecords {
			if record.State == domain.ImportArtifactStaged {
				currentActive = append(currentActive, record.ID)
				before, ok := prepared[record.ID]
				if !ok || before.Object.Version != record.Version || before.StorageObjectID != record.StorageObjectID {
					return domain.NewError(domain.CodeArtifactChanged, "staged artifact changed before import commit")
				}
			}
		}
		if !sameStrings(sortedStrings(currentActive), expectedIDs) {
			return domain.NewError(domain.CodeUploadIncomplete, "staged artifact set changed before import commit")
		}
		jobID, err := s.ensureImportJobTx(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		setupID, err := domain.NewSetupID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO setups(id, library_id, name, description, status, revision, source)
			VALUES (?, ?, ?, ?, 'draft', 1, 'imported')`, setupID, s.libraryID, session.Name, session.Description); err != nil {
			return databaseError(err)
		}
		journalID, err := s.appendJournal(ctx, tx, domain.AuditOperationImport, setupID, "", "", sessionID, jobID, 0,
			map[string]any{"artifactCount": len(active), "partialDraft": input.SavePartialDraft})
		if err != nil {
			return err
		}
		programPosition := 0
		for _, record := range currentRecords {
			item, ok := prepared[record.ID]
			if !ok || record.State != domain.ImportArtifactStaged {
				continue
			}
			artifactID, idErr := domain.NewArtifactID()
			if idErr != nil {
				return idErr
			}
			position := 0
			primary := false
			if record.Role == domain.ArtifactRoleProgram {
				position = programPosition
				programPosition++
				primary = record.ID == input.PrimaryArtifactID || programCount == 1
			}
			if err := insertArtifactTx(ctx, tx, setupID, artifactID, &item, position, primary); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE import_artifacts SET state = 'published', artifact_id = ?, storage_object_id = NULL,
				       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'staged'`,
				artifactID, record.ID); err != nil {
				return databaseError(err)
			}
		}
		state := domain.ImportStateSucceeded
		if input.SavePartialDraft {
			state = domain.ImportStateDraftSaved
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE import_sessions SET state = ?, setup_id = ?, error_code = NULL,
			       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
			       finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND state = 'staging'`, state, setupID, sessionID); err != nil {
			return databaseError(err)
		}
		result, err = s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			return err
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE jobs SET setup_id = ?, state = 'succeeded', progress = 1,
			       result_json = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
			       finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND state = 'running'`, setupID, string(resultJSON), jobID); err != nil {
			return databaseError(err)
		}
		if err := completeJournal(ctx, tx, journalID, domain.InitialRevision); err != nil {
			return err
		}
		if err := s.appendAudit(ctx, tx, domain.AuditOperationImport, setupID, "", jobID, 0,
			domain.InitialRevision, domain.AuditResultSucceeded, "", map[string]any{"artifactCount": len(active)}); err != nil {
			return err
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 201, result, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		s.logJobResult(jobID, time.Time{})
		return nil
	})
	if operationErr != nil {
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if finishErr := s.finishIdempotency(finishCtx, claim, 0, nil, operationErr); finishErr != nil {
			return nil, finishErr
		}
		return nil, operationErr
	}
	return result, nil
}

// withImportVerificationSlot keeps commit-time full-object hashing inside the
// same process-wide heavy-work budget as uploads, validation and reconciliation.
// The slot covers only verification and is released before the database
// publication transaction, avoiding nested acquisition by later mutations.
func (s *Service) withImportVerificationSlot(ctx context.Context, verify func() error) error {
	release, err := s.acquireHeavy(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeJobCancelled, "import verification was cancelled", err)
	}
	defer release()
	return verify()
}

func (s *Service) CancelImport(ctx context.Context, sessionID, idempotencyKey string) (*domain.ImportSession, error) {
	if err := domain.ValidateID(sessionID); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	operation := "cancelImport:" + sessionID
	hash, err := idempotencyRequestHash(operation, map[string]string{"sessionId": sessionID})
	if err != nil {
		return nil, err
	}
	return s.cancelImport(ctx, sessionID, &idempotencyKey, operation, hash)
}

func (s *Service) cancelImport(ctx context.Context, sessionID string, idempotencyKey *string, operation, hash string) (*domain.ImportSession, error) {
	var result *domain.ImportSession
	var candidates []storageCandidate
	var jobIDs []string
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	var claim idempotencyClaim
	if idempotencyKey != nil {
		claim, err = s.claimIdempotencyTx(ctx, tx, *idempotencyKey, operation, hash)
		if err != nil {
			return nil, err
		}
		var replay domain.ImportSession
		if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
			return nil, replayErr
		} else if ok {
			if err := tx.Commit(); err != nil {
				return nil, databaseError(err)
			}
			return &replay, nil
		}
	}
	fail := func(operationErr error) error { return operationErr }
	if idempotencyKey != nil {
		if savepointErr := beginLifecycleMutation(ctx, tx); savepointErr != nil {
			return nil, savepointErr
		}
		fail = func(operationErr error) error {
			return finishLifecycleFailure(ctx, tx, claim, operationErr)
		}
	}
	session, err := s.loadImport(ctx, tx, sessionID)
	if err != nil {
		return nil, fail(err)
	}
	if session.State == domain.ImportStateCancelled {
		result = session
		if idempotencyKey != nil {
			if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, databaseError(err)
		}
		return result, nil
	}
	if session.State != domain.ImportStateStaging {
		operationErr := domain.NewError(domain.CodeInvalidSetupState, "import session was already finalized")
		return nil, fail(operationErr)
	}
	records, err := s.loadImportArtifactRecords(ctx, tx, sessionID)
	if err != nil {
		return nil, fail(err)
	}
	for _, record := range records {
		if record.StorageObjectID != "" {
			candidates = append(candidates, storageCandidate{ID: record.StorageObjectID, Key: record.StorageKey, SHA256: record.SHA256})
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM jobs WHERE import_session_id = ? AND state IN ('queued', 'running', 'cancelling')`, sessionID)
	if err != nil {
		return nil, fail(databaseError(err))
	}
	for rows.Next() {
		var jobID string
		if err := rows.Scan(&jobID); err != nil {
			rows.Close()
			return nil, fail(databaseError(err))
		}
		jobIDs = append(jobIDs, jobID)
	}
	if err := rows.Close(); err != nil {
		return nil, fail(databaseError(err))
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET cancel_requested = 1, state = 'cancelled', error_code = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE import_session_id = ? AND state IN ('queued', 'running', 'cancelling')`,
		domain.CodeJobCancelled, sessionID); err != nil {
		return nil, fail(databaseError(err))
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE import_artifacts SET state = 'excluded', storage_object_id = NULL,
		       error_code = CASE WHEN state = 'uploading' THEN ? ELSE error_code END,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE import_session_id = ? AND state <> 'published'`, domain.CodeJobCancelled, sessionID); err != nil {
		return nil, fail(databaseError(err))
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE import_sessions SET state = 'cancelled', bytes_received = 0, error_code = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND state = 'staging'`, domain.CodeJobCancelled, sessionID); err != nil {
		return nil, fail(databaseError(err))
	}
	result, err = s.loadImport(ctx, tx, sessionID)
	if err != nil {
		return nil, fail(err)
	}
	if idempotencyKey != nil {
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	for _, jobID := range jobIDs {
		s.logJobResult(jobID, time.Time{})
	}
	s.cleanupStorageCandidates(context.Background(), candidates)
	return result, nil
}

// RecoverImports repairs upload rows that cannot still have a writer after a
// process restart and detaches objects from failed terminal sessions so the
// ref-safe collector can reclaim them.
func (s *Service) RecoverImports(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, databaseError(err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE import_artifacts
		   SET state = 'failed', error_code = 'UPLOAD_INCOMPLETE',
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE state = 'uploading'
		   AND import_session_id IN (SELECT id FROM import_sessions WHERE state = 'staging')
		   AND NOT EXISTS (
		       SELECT 1 FROM jobs j WHERE j.import_session_id = import_artifacts.import_session_id
		        AND j.state IN ('queued', 'running', 'cancelling'))`)
	if err != nil {
		return 0, databaseError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, databaseError(err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT o.id, o.storage_key, COALESCE(o.sha256, '')
		  FROM import_artifacts a
		  JOIN import_sessions i ON i.id = a.import_session_id
		  JOIN storage_objects o ON o.id = a.storage_object_id
		 WHERE i.library_id = ? AND i.setup_id IS NULL
		   AND i.state IN ('failed', 'cancelled', 'conflict')`, s.libraryID)
	if err != nil {
		return 0, databaseError(err)
	}
	var candidates []storageCandidate
	for rows.Next() {
		var candidate storageCandidate
		if err := rows.Scan(&candidate.ID, &candidate.Key, &candidate.SHA256); err != nil {
			rows.Close()
			return 0, databaseError(err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return 0, databaseError(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE import_artifacts
		   SET state = 'excluded', storage_object_id = NULL,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE storage_object_id IS NOT NULL AND import_session_id IN (
		       SELECT id FROM import_sessions
		        WHERE library_id = ? AND setup_id IS NULL
		          AND state IN ('failed', 'cancelled', 'conflict'))`, s.libraryID); err != nil {
		return 0, databaseError(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE import_sessions SET bytes_received = 0,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE library_id = ? AND setup_id IS NULL
		   AND state IN ('failed', 'cancelled', 'conflict')`, s.libraryID); err != nil {
		return 0, databaseError(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, databaseError(err)
	}
	s.cleanupStorageCandidates(context.Background(), candidates)
	return count, nil
}

func (s *Service) CleanupExpiredImports(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM import_sessions
		 WHERE library_id = ? AND state = 'staging' AND expires_at <= ? ORDER BY id`,
		s.libraryID, sqlTimestamp(s.now().UTC()))
	if err != nil {
		return 0, databaseError(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, databaseError(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, databaseError(err)
	}
	cleaned := 0
	for _, id := range ids {
		if _, err := s.cancelImport(ctx, id, nil, "", ""); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	return cleaned, nil
}

func (s *Service) reserveImportUpload(ctx context.Context, sessionID string, input UploadArtifactInput, normalizedName string) (*importUploadReservation, error) {
	var reservation *importUploadReservation
	err := s.withSetupLock("import:"+sessionID, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		session, err := s.loadImport(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if session.State != domain.ImportStateStaging {
			return domain.NewError(domain.CodeInvalidSetupState, "import session does not accept uploads")
		}
		if !s.now().UTC().Before(session.ExpiresAt) {
			return domain.NewError(domain.CodeJobCancelled, "import session expired")
		}
		if s.importTotalLimit > 0 && input.ExpectedSize >= 0 && input.ExpectedSize > s.importTotalLimit-session.Bytes {
			return domain.NewError(domain.CodeImportTooLarge, "import exceeds the configured total limit")
		}
		jobID, err := s.ensureImportJobTx(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		var recordID, state string
		err = tx.QueryRowContext(ctx, `
			SELECT id, state FROM import_artifacts WHERE import_session_id = ? AND normalized_name = ?`,
			sessionID, normalizedName).Scan(&recordID, &state)
		existing := false
		switch {
		case errors.Is(err, sql.ErrNoRows):
			recordID, err = domain.NewArtifactID()
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO import_artifacts(id, import_session_id, role, display_name, normalized_name,
				                             staging_key, state)
				VALUES (?, ?, ?, ?, ?, ?, 'uploading')`, recordID, sessionID, input.Role,
				input.DisplayName, normalizedName, "pending:"+recordID)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					return domain.NewError(domain.CodeNameConflict, "the import already contains an artifact with this name or role")
				}
				return databaseError(err)
			}
		case err != nil:
			return databaseError(err)
		case state == string(domain.ImportArtifactStaged) || state == string(domain.ImportArtifactPublished):
			existing = true
		case state == string(domain.ImportArtifactUploading):
			return domain.NewError(domain.CodeUploadIncomplete, "an upload for this artifact is already in progress")
		default:
			_, err = tx.ExecContext(ctx, `
				UPDATE import_artifacts
				   SET role = ?, display_name = ?, staging_key = ?, media_type = NULL,
				       byte_size = 0, sha256 = NULL, object_version = NULL, state = 'uploading',
				       storage_object_id = NULL, artifact_id = NULL, error_code = NULL,
				       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				 WHERE id = ?`, input.Role, input.DisplayName, "pending:"+recordID, recordID)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					return domain.NewError(domain.CodeNameConflict, "the import already contains an artifact with this name or role")
				}
				return databaseError(err)
			}
		}
		allowed := int64(-1)
		if s.importTotalLimit > 0 {
			allowed = s.importTotalLimit - session.Bytes
		}
		reservation = &importUploadReservation{
			ArtifactID: recordID, JobID: jobID, Existing: existing,
			BytesAlready: session.Bytes, BytesAllowed: allowed,
		}
		return databaseError(tx.Commit())
	})
	return reservation, err
}

func (s *Service) recordImportUploadFailure(ctx context.Context, sessionID, artifactID string, operationErr error) {
	code := safeErrorCode(operationErr)
	_, _ = s.db.ExecContext(ctx, `
		UPDATE import_artifacts
		   SET state = 'failed', error_code = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND import_session_id = ? AND state = 'uploading'`, code, artifactID, sessionID)
}

func (s *Service) recalculateImportBytes(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var bytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(byte_size), 0) FROM import_artifacts
		 WHERE import_session_id = ? AND state IN ('staged', 'published')`, sessionID).Scan(&bytes); err != nil {
		return databaseError(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE import_sessions SET bytes_received = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		bytes, sessionID); err != nil {
		return databaseError(err)
	}
	return nil
}

func (s *Service) ensureImportJobTx(ctx context.Context, tx *sql.Tx, sessionID string) (string, error) {
	var jobID string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM jobs WHERE import_session_id = ? AND state IN ('queued', 'running', 'cancelling')
		 ORDER BY created_at DESC, id DESC LIMIT 1`, sessionID).Scan(&jobID)
	if err == nil {
		return jobID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", databaseError(err)
	}
	jobID, err = domain.NewJobID()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jobs(id, library_id, kind, import_session_id, state, progress, bytes_done, started_at)
		VALUES (?, ?, ?, ?, 'running', 0, 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
		jobID, s.libraryID, domain.JobKindImport, sessionID); err != nil {
		return "", databaseError(err)
	}
	return jobID, nil
}

func (s *Service) importCancelled(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var state string
	var cancelRequested int
	err := s.db.QueryRowContext(ctx, `
		SELECT i.state, COALESCE(MAX(j.cancel_requested), 0)
		  FROM import_sessions i LEFT JOIN jobs j ON j.import_session_id = i.id
		 WHERE i.id = ? AND i.library_id = ? GROUP BY i.id`, sessionID, s.libraryID).Scan(&state, &cancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return context.Canceled
	}
	if err != nil {
		return err
	}
	if state != string(domain.ImportStateStaging) || cancelRequested != 0 {
		return context.Canceled
	}
	return nil
}

func (s *Service) monitorImportContext(parent context.Context, sessionID string) (context.Context, context.CancelFunc) {
	operationCtx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-operationCtx.Done():
				return
			case <-s.closed:
				cancel()
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(context.Background(), time.Second)
				err := s.importCancelled(checkCtx, sessionID)
				stop()
				if errors.Is(err, context.Canceled) {
					cancel()
					return
				}
			}
		}
	}()
	return operationCtx, cancel
}

type importCancellationReader struct {
	ctx        context.Context
	service    *Service
	sessionID  string
	source     io.Reader
	untilCheck int64
}

func (r *importCancellationReader) Read(buffer []byte) (int, error) {
	if r.untilCheck <= 0 {
		if err := r.service.importCancelled(r.ctx, r.sessionID); err != nil {
			return 0, err
		}
		r.untilCheck = 1 << 20
	}
	if int64(len(buffer)) > r.untilCheck {
		buffer = buffer[:r.untilCheck]
	}
	count, err := r.source.Read(buffer)
	r.untilCheck -= int64(count)
	return count, err
}

type importLimitReader struct {
	source    io.Reader
	remaining int64
}

func (r *importLimitReader) Read(buffer []byte) (int, error) {
	if r.remaining < 0 {
		return r.source.Read(buffer)
	}
	maximum := int64(len(buffer))
	if maximum > r.remaining+1 {
		maximum = r.remaining + 1
	}
	count, err := r.source.Read(buffer[:maximum])
	if int64(count) > r.remaining {
		r.remaining = 0
		return count, errImportLimitExceeded
	}
	r.remaining -= int64(count)
	return count, err
}

func (s *Service) loadImport(ctx context.Context, q queryer, sessionID string) (*domain.ImportSession, error) {
	if err := domain.ValidateID(sessionID); err != nil {
		return nil, err
	}
	var result domain.ImportSession
	var state, errorCode, expires, created, updated string
	var setupID sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT id, idempotency_key, setup_name, setup_description, state, bytes_received,
		       COALESCE(setup_id, ''), COALESCE(error_code, ''), expires_at, created_at, updated_at,
		       COALESCE((SELECT id FROM jobs WHERE import_session_id = import_sessions.id
		                 ORDER BY created_at DESC, id DESC LIMIT 1), '')
		  FROM import_sessions WHERE id = ? AND library_id = ?`, sessionID, s.libraryID).Scan(
		&result.ID, &result.IdempotencyKey, &result.Name, &result.Description, &state, &result.Bytes,
		&setupID, &errorCode, &expires, &created, &updated, &result.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.CodeImportNotFound, "import session was not found")
	}
	if err != nil {
		return nil, databaseError(err)
	}
	result.State = domain.ImportState(state)
	result.SetupID = setupID.String
	result.ErrorCode = domain.ErrorCode(errorCode)
	if result.ExpiresAt, err = parseTimestamp(expires); err != nil {
		return nil, databaseError(err)
	}
	if result.CreatedAt, err = parseTimestamp(created); err != nil {
		return nil, databaseError(err)
	}
	if result.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return nil, databaseError(err)
	}
	records, err := s.loadImportArtifactRecords(ctx, q, sessionID)
	if err != nil {
		return nil, err
	}
	result.Artifacts = make([]domain.ImportArtifact, len(records))
	for index := range records {
		result.Artifacts[index] = records[index].ImportArtifact
	}
	return &result, nil
}

func (s *Service) loadImportArtifact(ctx context.Context, q queryer, sessionID, artifactID string) (*importArtifactRecord, error) {
	records, err := s.loadImportArtifactRecords(ctx, q, sessionID)
	if err != nil {
		return nil, err
	}
	for index := range records {
		if records[index].ID == artifactID {
			return &records[index], nil
		}
	}
	return nil, domain.NewError(domain.CodeArtifactNotFound, "import artifact was not found")
}

func (s *Service) loadImportArtifactRecords(ctx context.Context, q queryer, sessionID string) ([]importArtifactRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT i.id, COALESCE(i.artifact_id, ''), i.role, i.display_name, i.normalized_name,
		       i.byte_size, i.state, COALESCE(i.error_code, ''), i.staging_key,
		       COALESCE(i.media_type, ''), COALESCE(i.sha256, ''), COALESCE(i.object_version, ''),
		       COALESCE(i.storage_object_id, ''), COALESCE(o.storage_key, '')
		  FROM import_artifacts i LEFT JOIN storage_objects o ON o.id = i.storage_object_id
		 WHERE i.import_session_id = ? ORDER BY i.created_at, i.id`, sessionID)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := make([]importArtifactRecord, 0)
	for rows.Next() {
		var record importArtifactRecord
		var role, state, errorCode string
		if err := rows.Scan(&record.ID, &record.ArtifactID, &role, &record.DisplayName, &record.NormalizedName,
			&record.ByteSize, &state, &errorCode, &record.StagingKey, &record.MediaType, &record.SHA256,
			&record.Version, &record.StorageObjectID, &record.StorageKey); err != nil {
			return nil, databaseError(err)
		}
		record.Role = domain.ArtifactRole(role)
		record.State = domain.ImportArtifactState(state)
		record.ErrorCode = domain.ErrorCode(errorCode)
		record.Bytes = record.ByteSize
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}
