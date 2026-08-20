package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
	"golang.org/x/net/html"
)

const (
	mediaTypeGCode = "text/x-gcode; charset=utf-8"
	mediaTypePDF   = "application/pdf"
	mediaTypeHTML  = "text/html; charset=utf-8"
	// Total HTML size is governed only by configured/storage limits. This
	// structural limit bounds one tokenizer token so every accepted sheet can
	// later be sanitized with bounded memory.
	MaxHTMLSetupSheetTokenBytes = 1 << 20
)

type preparedArtifact struct {
	Role                 domain.ArtifactRole
	DisplayName          string
	NormalizedName       string
	MediaType            string
	Object               *storage.Object
	StorageObjectID      string
	ReservationJournalID string
}

type storageCandidate struct {
	ID     string
	Key    string
	SHA256 string
}

// setupMutationFinalizer is an internal transaction hook used by durable
// upload jobs. It lets the aggregate mutation, terminal job snapshot and the
// outer run-upload idempotency result become visible in one SQLite commit.
type setupMutationFinalizer func(context.Context, *sql.Tx, *domain.Setup) error

// AddPrograms publishes all supplied programs as one setup-composition
// mutation. A failed upload or revision conflict leaves the setup untouched.
func (s *Service) AddPrograms(ctx context.Context, setupID string, input AddProgramsInput) (*domain.Setup, error) {
	return s.AddProgramsStream(ctx, setupID, AddProgramsStreamInput{
		ExpectedRevision: input.ExpectedRevision,
		IdempotencyKey:   input.IdempotencyKey,
		Source: func(yield func(UploadArtifactInput) error) error {
			for _, program := range input.Programs {
				if err := yield(program); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func (s *Service) AddProgramsStream(ctx context.Context, setupID string, input AddProgramsStreamInput) (*domain.Setup, error) {
	if input.Source == nil {
		return nil, domain.NewError(domain.CodeInvalidContent, "program upload source is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	var result *domain.Setup
	var cleanupItems []preparedArtifact
	var claim idempotencyClaim
	claimActive := false
	err := s.withSetupLock(setupID, func() error {
		setup, err := s.loadSetup(ctx, s.db, setupID, true)
		if err != nil {
			return err
		}

		newNames := make(map[string]struct{})
		programCount := 0
		for _, artifact := range setup.Artifacts {
			if artifact.Role == domain.ArtifactRoleProgram {
				programCount++
			}
		}

		prepared := make([]preparedArtifact, 0, 4)
		cleanupItems = prepared
		cleanup := func() {}
		var yieldedErr error
		sourceErr := input.Source(func(program UploadArtifactInput) error {
			if yieldedErr != nil {
				return yieldedErr
			}
			yieldedErr = func() error {
				if program.Role != "" && program.Role != domain.ArtifactRoleProgram {
					return domain.NewError(domain.CodeUnsupportedFileType, "only G-code programs may be added here")
				}
				program.Role = domain.ArtifactRoleProgram
				name, nameErr := domain.NormalizeArtifactName(program.DisplayName)
				if nameErr != nil {
					return nameErr
				}
				key, keyErr := domain.ArtifactNameKey(name)
				if keyErr != nil {
					return keyErr
				}
				if _, duplicate := newNames[key]; duplicate {
					return domain.NewError(domain.CodeNameConflict, "program names in one operation must be unique")
				}
				newNames[key] = struct{}{}
				item, prepareErr := s.prepareArtifact(ctx, program)
				if prepareErr != nil {
					return prepareErr
				}
				if persistErr := s.persistPreparedObject(ctx, item); persistErr != nil {
					prepared = append(prepared, *item)
					cleanupItems = prepared
					return persistErr
				}
				prepared = append(prepared, *item)
				cleanupItems = prepared
				return nil
			}()
			return yieldedErr
		})
		if sourceErr == nil {
			sourceErr = yieldedErr
		}
		if sourceErr != nil {
			return sourceErr
		}
		if len(prepared) == 0 {
			return domain.NewError(domain.CodeInvalidContent, "at least one program is required")
		}

		request := make([]map[string]any, 0, len(prepared))
		for _, item := range prepared {
			request = append(request, map[string]any{
				"name": item.DisplayName, "role": item.Role,
				"size": item.Object.Size, "sha256": item.Object.SHA256,
			})
		}
		hash, err := idempotencyRequestHash("addPrograms:"+setupID, map[string]any{
			"expectedRevision": input.ExpectedRevision, "programs": request,
		})
		if err != nil {
			cleanup()
			return err
		}
		claim, err = s.claimIdempotency(ctx, input.IdempotencyKey, "addPrograms:"+setupID, hash)
		if err != nil {
			return err
		}
		var replay domain.Setup
		if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
			return replayErr
		} else if ok {
			result = &replay
			return nil
		}
		claimActive = true

		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			cleanup()
			return databaseError(err)
		}
		defer tx.Rollback()
		current, err := s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			cleanup()
			return err
		}
		nextStatus, nextRevision, err := domain.NextMutation(current.Status, current.Revision, input.ExpectedRevision)
		if err != nil {
			cleanup()
			return err
		}
		var maximumPosition int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) FROM setup_artifacts WHERE setup_id = ? AND role = 'program'`, setupID).Scan(&maximumPosition); err != nil {
			cleanup()
			return databaseError(err)
		}
		journalID, err := s.appendJournal(ctx, tx, domain.AuditOperationAddPrograms, setupID, "", "", "", "", input.ExpectedRevision,
			map[string]any{"count": len(prepared)})
		if err != nil {
			cleanup()
			return err
		}
		for index := range prepared {
			if err := s.ensurePreparedObjectTx(ctx, tx, &prepared[index]); err != nil {
				cleanup()
				return err
			}
			artifactID, idErr := domain.NewArtifactID()
			if idErr != nil {
				cleanup()
				return idErr
			}
			primary := programCount == 0 && len(prepared) == 1
			if err := insertArtifactTx(ctx, tx, setupID, artifactID, &prepared[index], maximumPosition+index+1, primary); err != nil {
				cleanup()
				return err
			}
		}
		if err := updateSetupForMutation(ctx, tx, setupID, input.ExpectedRevision, nextStatus, nextRevision); err != nil {
			cleanup()
			return err
		}
		if err := completeJournal(ctx, tx, journalID, nextRevision); err != nil {
			cleanup()
			return err
		}
		if err := s.appendAudit(ctx, tx, domain.AuditOperationAddPrograms, setupID, "", "", input.ExpectedRevision,
			nextRevision, domain.AuditResultSucceeded, "", map[string]any{"count": len(prepared)}); err != nil {
			cleanup()
			return err
		}
		result, err = s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			cleanup()
			return err
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			cleanup()
			return err
		}
		if input.finalizeTx != nil {
			if err := input.finalizeTx(ctx, tx, result); err != nil {
				cleanup()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			cleanup()
			return databaseError(err)
		}
		return nil
	})
	s.cleanupPreparedObjects(context.Background(), cleanupItems)
	if err != nil && claimActive {
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if finishErr := s.finishIdempotency(finishCtx, claim, 0, nil, err); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, err
}

// ReplaceArtifact atomically replaces one program or setup sheet while
// retaining the logical artifact identity, position and primary flag.
func (s *Service) ReplaceArtifact(ctx context.Context, setupID, artifactID string, input ReplaceArtifactInput) (*domain.Setup, error) {
	return s.replaceArtifact(ctx, setupID, artifactID, input, false)
}

func (s *Service) replaceArtifact(ctx context.Context, setupID, artifactID string, input ReplaceArtifactInput, allowSheetRename bool) (*domain.Setup, error) {
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	var result *domain.Setup
	var cleanupItems []preparedArtifact
	var claim idempotencyClaim
	claimActive := false
	err := s.withSetupLock(setupID, func() error {
		_, err := s.loadSetup(ctx, s.db, setupID, true)
		if err != nil {
			return err
		}
		record, err := s.loadArtifact(ctx, s.db, setupID, artifactID)
		if err != nil {
			return err
		}
		displayName := record.DisplayName
		if record.Role == domain.ArtifactRoleSetupSheet && allowSheetRename && input.DisplayName != "" {
			displayName = input.DisplayName
		} else if input.DisplayName != "" {
			normalized, normalizeErr := domain.NormalizeArtifactName(input.DisplayName)
			if normalizeErr != nil {
				return normalizeErr
			}
			if normalized != displayName {
				return domain.NewError(domain.CodeInvalidContent, "program replacement preserves the display name")
			}
		}
		prepared, err := s.prepareArtifact(ctx, UploadArtifactInput{
			Role: record.Role, DisplayName: displayName, Content: input.Content, ExpectedSize: input.ExpectedSize,
		})
		if err != nil {
			return err
		}
		cleanupItems = []preparedArtifact{*prepared}
		cleanup := func() {}
		if err := s.persistPreparedObject(ctx, prepared); err != nil {
			cleanup()
			return err
		}
		operation := domain.AuditOperationReplaceProgram
		operationName := "replaceProgram:"
		if record.Role == domain.ArtifactRoleSetupSheet {
			operation = domain.AuditOperationSetupSheet
			operationName = "replaceSetupSheet:"
		}
		hash, err := idempotencyRequestHash(operationName+artifactID, map[string]any{
			"expectedRevision": input.ExpectedRevision, "expectedVersion": input.ExpectedVersion,
			"name": prepared.DisplayName, "size": prepared.Object.Size, "sha256": prepared.Object.SHA256,
		})
		if err != nil {
			cleanup()
			return err
		}
		claim, err = s.claimIdempotency(ctx, input.IdempotencyKey, operationName+artifactID, hash)
		if err != nil {
			return err
		}
		var replay domain.Setup
		if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
			return replayErr
		} else if ok {
			result = &replay
			return nil
		}
		claimActive = true
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			cleanup()
			return databaseError(err)
		}
		defer tx.Rollback()
		current, err := s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			cleanup()
			return err
		}
		nextStatus, nextRevision, err := domain.NextMutation(current.Status, current.Revision, input.ExpectedRevision)
		if err != nil {
			cleanup()
			return err
		}
		currentArtifact, err := s.loadArtifact(ctx, tx, setupID, artifactID)
		if err != nil {
			cleanup()
			return err
		}
		if currentArtifact.Version != input.ExpectedVersion || currentArtifact.Role != record.Role {
			cleanup()
			return domain.NewError(domain.CodeArtifactChanged, "artifact version has changed")
		}
		if inspectErr := s.inspectArtifactForMutation(currentArtifact); inspectErr != nil {
			cleanup()
			if domain.IsErrorCode(inspectErr, domain.CodeArtifactChanged) {
				return s.commitArtifactAttentionTx(ctx, tx, setupID, inspectErr)
			}
			return inspectErr
		}
		if err := s.ensurePreparedObjectTx(ctx, tx, prepared); err != nil {
			cleanup()
			return err
		}
		journalID, err := s.appendJournal(ctx, tx, operation, setupID, artifactID, prepared.StorageObjectID, "", "",
			input.ExpectedRevision, map[string]any{"oldObjectId": currentArtifact.StorageObjectID})
		if err != nil {
			cleanup()
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE setup_artifacts
			   SET display_name = ?, normalized_name = ?, storage_object_id = ?,
			       identity_device = ?, identity_inode = ?, identity_size = ?,
			       identity_mtime_ns = ?, identity_ctime_ns = ?, object_version = ?,
			       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND setup_id = ? AND object_version = ?`,
			prepared.DisplayName, prepared.NormalizedName, prepared.StorageObjectID,
			int64(prepared.Object.Identity.Device), int64(prepared.Object.Identity.Inode), prepared.Object.Size,
			prepared.Object.Identity.ModTimeNS, prepared.Object.Identity.ChangeTimeNS, prepared.Object.Version,
			artifactID, setupID, input.ExpectedVersion)
		if err != nil {
			cleanup()
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return domain.NewError(domain.CodeNameConflict, "an artifact with this name already exists in the setup")
			}
			return databaseError(err)
		}
		if err := updateSetupForMutation(ctx, tx, setupID, input.ExpectedRevision, nextStatus, nextRevision); err != nil {
			cleanup()
			return err
		}
		if err := completeJournal(ctx, tx, journalID, nextRevision); err != nil {
			cleanup()
			return err
		}
		if err := s.appendAudit(ctx, tx, operation, setupID, artifactID, "", input.ExpectedRevision, nextRevision,
			domain.AuditResultSucceeded, "", nil); err != nil {
			cleanup()
			return err
		}
		result, err = s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			cleanup()
			return err
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			cleanup()
			return err
		}
		if input.finalizeTx != nil {
			if err := input.finalizeTx(ctx, tx, result); err != nil {
				cleanup()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			cleanup()
			return databaseError(err)
		}
		// The old object became a candidate only after the successful commit.
		s.cleanupStorageCandidates(context.Background(), []storageCandidate{{
			ID: currentArtifact.StorageObjectID, Key: currentArtifact.StorageKey, SHA256: currentArtifact.SHA256,
		}})
		return nil
	})
	s.cleanupPreparedObjects(context.Background(), cleanupItems)
	if err != nil && claimActive {
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if finishErr := s.finishIdempotency(finishCtx, claim, 0, nil, err); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, err
}

func (s *Service) RenameArtifact(ctx context.Context, setupID, artifactID string, input RenameArtifactInput) (*domain.Setup, error) {
	name, err := domain.NormalizeArtifactName(input.DisplayName)
	if err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	var result *domain.Setup
	err = s.withSetupLock(setupID, func() error {
		tx, beginErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if beginErr != nil {
			return databaseError(beginErr)
		}
		defer tx.Rollback()
		hash, hashErr := idempotencyRequestHash("renameArtifact:"+artifactID, input)
		if hashErr != nil {
			return hashErr
		}
		claim, claimErr := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "renameArtifact:"+artifactID, hash)
		if claimErr != nil {
			return claimErr
		}
		var replay domain.Setup
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
		setup, loadErr := s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, loadErr)
		}
		nextStatus, nextRevision, transitionErr := domain.NextMutation(setup.Status, setup.Revision, input.ExpectedRevision)
		if transitionErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, transitionErr)
		}
		artifact, loadErr := s.loadArtifact(ctx, tx, setupID, artifactID)
		if loadErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, loadErr)
		}
		if input.ExpectedVersion == "" || artifact.Version != input.ExpectedVersion {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeArtifactChanged, "artifact version has changed"))
		}
		if inspectErr := s.inspectArtifactForMutation(artifact); inspectErr != nil {
			if domain.IsErrorCode(inspectErr, domain.CodeArtifactChanged) {
				return s.finishArtifactMutationAttention(ctx, tx, claim, setupID, inspectErr)
			}
			return finishLifecycleFailure(ctx, tx, claim, inspectErr)
		}
		if artifact.Role == domain.ArtifactRoleProgram {
			if extensionErr := s.gcode.ValidateExtension(name); extensionErr != nil {
				return finishLifecycleFailure(ctx, tx, claim, extensionErr)
			}
		} else if media, mediaErr := setupSheetMediaType(name); mediaErr != nil || media != artifact.MediaType {
			if mediaErr != nil {
				return finishLifecycleFailure(ctx, tx, claim, mediaErr)
			}
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeUnsupportedFileType, "setup sheet extension does not match its content type"))
		}
		normalizedName, keyErr := domain.ArtifactNameKey(name)
		if keyErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, keyErr)
		}
		journalID, journalErr := s.appendJournal(ctx, tx, domain.AuditOperationRenameProgram, setupID, artifactID,
			artifact.StorageObjectID, "", "", input.ExpectedRevision, nil)
		if journalErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, journalErr)
		}
		resultSQL, updateErr := tx.ExecContext(ctx, `
			UPDATE setup_artifacts SET display_name = ?, normalized_name = ?,
			       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND setup_id = ? AND object_version = ?`, name, normalizedName, artifactID, setupID, input.ExpectedVersion)
		if updateErr != nil {
			if strings.Contains(strings.ToLower(updateErr.Error()), "unique") {
				return finishLifecycleFailure(ctx, tx, claim,
					domain.NewError(domain.CodeNameConflict, "an artifact with this name already exists in the setup"))
			}
			return finishLifecycleFailure(ctx, tx, claim, databaseError(updateErr))
		}
		if changed, rowsErr := resultSQL.RowsAffected(); rowsErr != nil || changed != 1 {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeArtifactChanged, "artifact version has changed"))
		}
		if updateErr := updateSetupForMutation(ctx, tx, setupID, input.ExpectedRevision, nextStatus, nextRevision); updateErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, updateErr)
		}
		if journalErr := completeJournal(ctx, tx, journalID, nextRevision); journalErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, journalErr)
		}
		operation := domain.AuditOperationRenameProgram
		if artifact.Role == domain.ArtifactRoleSetupSheet {
			operation = domain.AuditOperationSetupSheet
		}
		if auditErr := s.appendAudit(ctx, tx, operation, setupID, artifactID, "", input.ExpectedRevision,
			nextRevision, domain.AuditResultSucceeded, "", nil); auditErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, auditErr)
		}
		result, loadErr = s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return finishLifecycleFailure(ctx, tx, claim, loadErr)
		}
		if finishErr := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); finishErr != nil {
			return finishErr
		}
		return databaseError(tx.Commit())
	})
	return result, err
}

func (s *Service) DeleteArtifact(ctx context.Context, setupID, artifactID string, input DeleteArtifactInput) (*domain.Setup, error) {
	return s.deleteArtifact(ctx, setupID, artifactID, input)
}

func (s *Service) deleteArtifact(ctx context.Context, setupID, artifactID string, input DeleteArtifactInput) (*domain.Setup, error) {
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	var result *domain.Setup
	err := s.withSetupLock(setupID, func() error {
		tx, beginErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if beginErr != nil {
			return databaseError(beginErr)
		}
		defer tx.Rollback()
		hash, hashErr := idempotencyRequestHash("deleteArtifact:"+artifactID, input)
		if hashErr != nil {
			return hashErr
		}
		claim, claimErr := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "deleteArtifact:"+artifactID, hash)
		if claimErr != nil {
			return claimErr
		}
		var replay domain.Setup
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
		setup, loadErr := s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return fail(loadErr)
		}
		nextStatus, nextRevision, transitionErr := domain.NextMutation(setup.Status, setup.Revision, input.ExpectedRevision)
		if transitionErr != nil {
			return fail(transitionErr)
		}
		artifact, loadErr := s.loadArtifact(ctx, tx, setupID, artifactID)
		if loadErr != nil {
			return fail(loadErr)
		}
		if input.ExpectedVersion == "" || artifact.Version != input.ExpectedVersion {
			return fail(domain.NewError(domain.CodeArtifactChanged, "artifact version has changed"))
		}
		if inspectErr := s.inspectArtifactForMutation(artifact); inspectErr != nil {
			if domain.IsErrorCode(inspectErr, domain.CodeArtifactChanged) {
				return s.finishArtifactMutationAttention(ctx, tx, claim, setupID, inspectErr)
			}
			return fail(inspectErr)
		}
		programCount := 0
		for _, candidate := range setup.Artifacts {
			if candidate.Role == domain.ArtifactRoleProgram {
				programCount++
			}
		}
		if artifact.Role == domain.ArtifactRoleProgram && programCount == 1 && !input.ConfirmDeleteLastProgram {
			return fail(domain.NewError(domain.CodeInvalidContent, "deleting the last program requires explicit confirmation"))
		}
		var replacementPrimary *artifactRecord
		hasReplacement := input.ReplacementPrimaryArtifactID != ""
		if artifact.Role != domain.ArtifactRoleProgram || !artifact.Primary {
			if hasReplacement || input.LeavePrimaryUnassigned {
				return fail(domain.NewError(domain.CodeInvalidContent, "primary deletion choice is only valid when deleting the primary program"))
			}
		} else {
			if hasReplacement == input.LeavePrimaryUnassigned {
				return fail(domain.NewError(domain.CodeInvalidContent, "choose a replacement primary or explicitly leave it unassigned"))
			}
			if hasReplacement {
				if input.ReplacementPrimaryArtifactID == artifactID {
					return fail(domain.NewError(domain.CodeInvalidContent, "replacement primary must be a different program"))
				}
				if err := domain.ValidateID(input.ReplacementPrimaryArtifactID); err != nil {
					return fail(err)
				}
				replacementPrimary, loadErr = s.loadArtifact(ctx, tx, setupID, input.ReplacementPrimaryArtifactID)
				if loadErr != nil {
					return fail(loadErr)
				}
				if replacementPrimary.Role != domain.ArtifactRoleProgram {
					return fail(domain.NewError(domain.CodeInvalidContent, "replacement primary must be another G-code program"))
				}
			}
		}
		operation := domain.AuditOperationDeleteProgram
		if artifact.Role == domain.ArtifactRoleSetupSheet {
			operation = domain.AuditOperationSetupSheet
		}
		journalID, journalErr := s.appendJournal(ctx, tx, operation, setupID, artifactID, artifact.StorageObjectID,
			"", "", input.ExpectedRevision, nil)
		if journalErr != nil {
			return fail(journalErr)
		}
		deleted, deleteErr := tx.ExecContext(ctx, `DELETE FROM setup_artifacts WHERE id = ? AND setup_id = ? AND object_version = ?`,
			artifactID, setupID, input.ExpectedVersion)
		if deleteErr != nil {
			return fail(databaseError(deleteErr))
		}
		if changed, rowsErr := deleted.RowsAffected(); rowsErr != nil || changed != 1 {
			return fail(domain.NewError(domain.CodeArtifactChanged, "artifact version has changed"))
		}
		if replacementPrimary != nil {
			updated, updateErr := tx.ExecContext(ctx, `
				UPDATE setup_artifacts SET is_primary = 1,
				       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				 WHERE id = ? AND setup_id = ? AND role = 'program'`, replacementPrimary.ID, setupID)
			if updateErr != nil {
				return fail(databaseError(updateErr))
			}
			if changed, rowsErr := updated.RowsAffected(); rowsErr != nil || changed != 1 {
				return fail(domain.NewError(domain.CodeRevisionConflict, "replacement primary changed"))
			}
		} else if artifact.Role == domain.ArtifactRoleProgram && !artifact.Primary && programCount == 2 {
			// If deletion leaves one program and no explicit primary-deletion
			// choice was made, preserve the single-program primary invariant.
			if _, updateErr := tx.ExecContext(ctx, `
				UPDATE setup_artifacts SET is_primary = 1,
				       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				 WHERE setup_id = ? AND role = 'program'
				   AND NOT EXISTS (SELECT 1 FROM setup_artifacts WHERE setup_id = ? AND role = 'program' AND is_primary = 1)`,
				setupID, setupID); updateErr != nil {
				return fail(databaseError(updateErr))
			}
		}
		if updateErr := updateSetupForMutation(ctx, tx, setupID, input.ExpectedRevision, nextStatus, nextRevision); updateErr != nil {
			return fail(updateErr)
		}
		if journalErr := completeJournal(ctx, tx, journalID, nextRevision); journalErr != nil {
			return fail(journalErr)
		}
		if auditErr := s.appendAudit(ctx, tx, operation, setupID, artifactID, "", input.ExpectedRevision,
			nextRevision, domain.AuditResultSucceeded, "", nil); auditErr != nil {
			return fail(auditErr)
		}
		result, loadErr = s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return fail(loadErr)
		}
		if finishErr := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); finishErr != nil {
			return finishErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return databaseError(commitErr)
		}
		s.cleanupStorageCandidates(context.Background(), []storageCandidate{{
			ID: artifact.StorageObjectID, Key: artifact.StorageKey, SHA256: artifact.SHA256,
		}})
		return nil
	})
	return result, err
}

func (s *Service) SetPrimaryProgram(ctx context.Context, setupID, artifactID string, input SetPrimaryInput) (*domain.Setup, error) {
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	var result *domain.Setup
	err := s.withSetupLock(setupID, func() error {
		tx, beginErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if beginErr != nil {
			return databaseError(beginErr)
		}
		defer tx.Rollback()
		hash, hashErr := idempotencyRequestHash("setPrimary:"+artifactID, input)
		if hashErr != nil {
			return hashErr
		}
		claim, claimErr := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "setPrimary:"+artifactID, hash)
		if claimErr != nil {
			return claimErr
		}
		var replay domain.Setup
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
		setup, loadErr := s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return fail(loadErr)
		}
		if _, _, transitionErr := domain.NextMutation(setup.Status, setup.Revision, input.ExpectedRevision); transitionErr != nil {
			return fail(transitionErr)
		}
		artifact, loadErr := s.loadArtifact(ctx, tx, setupID, artifactID)
		if loadErr != nil {
			return fail(loadErr)
		}
		if artifact.Role != domain.ArtifactRoleProgram {
			return fail(domain.NewError(domain.CodeInvalidContent, "only a G-code program can be primary"))
		}
		if input.ExpectedVersion == "" || artifact.Version != input.ExpectedVersion {
			return fail(domain.NewError(domain.CodeArtifactChanged, "artifact version has changed"))
		}
		if inspectErr := s.inspectArtifactForMutation(artifact); inspectErr != nil {
			if domain.IsErrorCode(inspectErr, domain.CodeArtifactChanged) {
				return s.finishArtifactMutationAttention(ctx, tx, claim, setupID, inspectErr)
			}
			return fail(inspectErr)
		}
		if artifact.Primary {
			result = setup
			if finishErr := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); finishErr != nil {
				return finishErr
			}
			return databaseError(tx.Commit())
		}
		nextStatus, nextRevision, transitionErr := domain.NextMutation(setup.Status, setup.Revision, input.ExpectedRevision)
		if transitionErr != nil {
			return fail(transitionErr)
		}
		journalID, journalErr := s.appendJournal(ctx, tx, domain.AuditOperationSetPrimary, setupID, artifactID,
			artifact.StorageObjectID, "", "", input.ExpectedRevision, nil)
		if journalErr != nil {
			return fail(journalErr)
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE setup_artifacts SET is_primary = 0 WHERE setup_id = ? AND role = 'program'`, setupID); updateErr != nil {
			return fail(databaseError(updateErr))
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE setup_artifacts SET is_primary = 1 WHERE id = ? AND setup_id = ?`, artifactID, setupID); updateErr != nil {
			return fail(databaseError(updateErr))
		}
		if updateErr := updateSetupForMutation(ctx, tx, setupID, input.ExpectedRevision, nextStatus, nextRevision); updateErr != nil {
			return fail(updateErr)
		}
		if journalErr := completeJournal(ctx, tx, journalID, nextRevision); journalErr != nil {
			return fail(journalErr)
		}
		if auditErr := s.appendAudit(ctx, tx, domain.AuditOperationSetPrimary, setupID, artifactID, "", input.ExpectedRevision,
			nextRevision, domain.AuditResultSucceeded, "", nil); auditErr != nil {
			return fail(auditErr)
		}
		result, loadErr = s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return fail(loadErr)
		}
		if finishErr := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); finishErr != nil {
			return finishErr
		}
		return databaseError(tx.Commit())
	})
	return result, err
}

// PutSetupSheet adds the single setup sheet or replaces it atomically. For a
// replacement ExpectedVersion is mandatory; for an addition it must be empty.
func (s *Service) PutSetupSheet(ctx context.Context, setupID string, input ReplaceArtifactInput) (*domain.Setup, error) {
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	prepared, err := s.prepareArtifact(ctx, UploadArtifactInput{
		Role: domain.ArtifactRoleSetupSheet, DisplayName: input.DisplayName,
		Content: input.Content, ExpectedSize: input.ExpectedSize,
	})
	if err != nil {
		return nil, err
	}
	cleanup := func() { s.cleanupPreparedObjects(context.Background(), []preparedArtifact{*prepared}) }
	if err := s.persistPreparedObject(ctx, prepared); err != nil {
		cleanup()
		return nil, err
	}
	operation := "putSetupSheet:" + setupID
	hash, err := idempotencyRequestHash(operation, map[string]any{
		"expectedRevision": input.ExpectedRevision, "expectedVersion": input.ExpectedVersion,
		"name": prepared.DisplayName, "size": prepared.Object.Size, "sha256": prepared.Object.SHA256,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, operation, hash)
	if err != nil {
		cleanup()
		return nil, err
	}
	var replay domain.Setup
	if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
		cleanup()
		return nil, replayErr
	} else if ok {
		cleanup()
		return &replay, nil
	}
	var result *domain.Setup
	var oldCandidate storageCandidate
	operationErr := s.withSetupLock(setupID, func() error {
		tx, beginErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if beginErr != nil {
			return databaseError(beginErr)
		}
		defer tx.Rollback()
		current, loadErr := s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return loadErr
		}
		nextStatus, nextRevision, transitionErr := domain.NextMutation(current.Status, current.Revision, input.ExpectedRevision)
		if transitionErr != nil {
			return transitionErr
		}
		var existing *artifactRecord
		records, loadErr := s.loadArtifacts(ctx, tx, setupID)
		if loadErr != nil {
			return loadErr
		}
		for index := range records {
			if records[index].Role == domain.ArtifactRoleSetupSheet {
				existing = &records[index]
				break
			}
		}
		if existing == nil && input.ExpectedVersion != "" {
			return domain.NewError(domain.CodeArtifactChanged, "setup sheet was removed or replaced")
		}
		if existing != nil && (input.ExpectedVersion == "" || existing.Version != input.ExpectedVersion) {
			return domain.NewError(domain.CodeArtifactChanged, "setup sheet version has changed")
		}
		if existing != nil {
			if inspectErr := s.inspectArtifactForMutation(existing); inspectErr != nil {
				if domain.IsErrorCode(inspectErr, domain.CodeArtifactChanged) {
					return s.commitArtifactAttentionTx(ctx, tx, setupID, inspectErr)
				}
				return inspectErr
			}
		}
		if objectErr := s.ensurePreparedObjectTx(ctx, tx, prepared); objectErr != nil {
			return objectErr
		}
		artifactID := ""
		if existing == nil {
			var idErr error
			artifactID, idErr = domain.NewArtifactID()
			if idErr != nil {
				return idErr
			}
		} else {
			artifactID = existing.ID
			oldCandidate = storageCandidate{ID: existing.StorageObjectID, Key: existing.StorageKey, SHA256: existing.SHA256}
		}
		journalID, journalErr := s.appendJournal(ctx, tx, domain.AuditOperationSetupSheet, setupID, artifactID,
			prepared.StorageObjectID, "", "", input.ExpectedRevision, nil)
		if journalErr != nil {
			return journalErr
		}
		if existing == nil {
			if insertErr := insertArtifactTx(ctx, tx, setupID, artifactID, prepared, 0, false); insertErr != nil {
				return insertErr
			}
		} else {
			updated, updateErr := tx.ExecContext(ctx, `
				UPDATE setup_artifacts
				   SET display_name = ?, normalized_name = ?, storage_object_id = ?,
				       identity_device = ?, identity_inode = ?, identity_size = ?,
				       identity_mtime_ns = ?, identity_ctime_ns = ?, object_version = ?,
				       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				 WHERE id = ? AND setup_id = ? AND object_version = ?`,
				prepared.DisplayName, prepared.NormalizedName, prepared.StorageObjectID,
				int64(prepared.Object.Identity.Device), int64(prepared.Object.Identity.Inode), prepared.Object.Size,
				prepared.Object.Identity.ModTimeNS, prepared.Object.Identity.ChangeTimeNS, prepared.Object.Version,
				artifactID, setupID, input.ExpectedVersion)
			if updateErr != nil {
				if strings.Contains(strings.ToLower(updateErr.Error()), "unique") {
					return domain.NewError(domain.CodeNameConflict, "an artifact with this name already exists in the setup")
				}
				return databaseError(updateErr)
			}
			if changed, rowsErr := updated.RowsAffected(); rowsErr != nil || changed != 1 {
				return domain.NewError(domain.CodeArtifactChanged, "setup sheet version has changed")
			}
		}
		if updateErr := updateSetupForMutation(ctx, tx, setupID, input.ExpectedRevision, nextStatus, nextRevision); updateErr != nil {
			return updateErr
		}
		if journalErr := completeJournal(ctx, tx, journalID, nextRevision); journalErr != nil {
			return journalErr
		}
		if auditErr := s.appendAudit(ctx, tx, domain.AuditOperationSetupSheet, setupID, artifactID, "", input.ExpectedRevision,
			nextRevision, domain.AuditResultSucceeded, "", nil); auditErr != nil {
			return auditErr
		}
		result, loadErr = s.loadSetup(ctx, tx, setupID, true)
		if loadErr != nil {
			return loadErr
		}
		if finishErr := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); finishErr != nil {
			return finishErr
		}
		if input.finalizeTx != nil {
			if finishErr := input.finalizeTx(ctx, tx, result); finishErr != nil {
				return finishErr
			}
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return databaseError(commitErr)
		}
		return nil
	})
	cleanup()
	if operationErr != nil {
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if finishErr := s.finishIdempotency(finishCtx, claim, 0, nil, operationErr); finishErr != nil {
			return nil, finishErr
		}
		return nil, operationErr
	}
	if oldCandidate.ID != "" {
		s.cleanupStorageCandidates(context.Background(), []storageCandidate{oldCandidate})
	}
	return result, nil
}

func (s *Service) DeleteSetupSheet(ctx context.Context, setupID string, input DeleteArtifactInput) (*domain.Setup, error) {
	setup, err := s.loadSetup(ctx, s.db, setupID, true)
	if err != nil {
		return nil, err
	}
	for _, artifact := range setup.Artifacts {
		if artifact.Role == domain.ArtifactRoleSetupSheet {
			return s.deleteArtifact(ctx, setupID, artifact.ID, input)
		}
	}
	return nil, domain.NewError(domain.CodeArtifactNotFound, "setup sheet was not found")
}

func (s *Service) prepareArtifact(ctx context.Context, input UploadArtifactInput) (*preparedArtifact, error) {
	if input.Content == nil || !input.Role.Valid() {
		return nil, domain.NewError(domain.CodeInvalidContent, "artifact upload is incomplete")
	}
	name, err := domain.NormalizeArtifactName(input.DisplayName)
	if err != nil {
		return nil, err
	}
	normalizedName, err := domain.ArtifactNameKey(name)
	if err != nil {
		return nil, err
	}
	mediaType := mediaTypeGCode
	if input.Role == domain.ArtifactRoleProgram {
		if err := s.gcode.ValidateExtension(name); err != nil {
			return nil, err
		}
	} else {
		mediaType, err = setupSheetMediaType(name)
		if err != nil {
			return nil, err
		}
	}
	release, err := s.acquireHeavy(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeJobCancelled, "artifact upload was cancelled", err)
	}
	defer release()
	staged, err := s.objects.Stage(ctx, input.Content, input.ExpectedSize)
	if err != nil {
		if errors.Is(err, errImportLimitExceeded) {
			return nil, domain.WrapError(domain.CodeImportTooLarge, "import exceeds the configured total limit", err)
		}
		if errors.Is(err, storage.ErrInvalidObject) {
			return nil, domain.WrapError(domain.CodeUploadIncomplete, "artifact upload did not match its declared size", err)
		}
		return nil, storageError(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = s.objects.Discard(staged)
		}
	}()
	file, err := s.objects.OpenStaged(staged)
	if err != nil {
		return nil, storageError(err)
	}
	if input.Role == domain.ArtifactRoleProgram {
		_, err = s.gcode.Validate(ctx, input.Role, name, file)
	} else {
		err = validateSetupSheetContent(ctx, mediaType, file)
	}
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, storageError(closeErr)
	}
	key, err := s.objects.ObjectKeyForSHA(staged.SHA256)
	if err != nil {
		return nil, storageError(err)
	}
	prepared := &preparedArtifact{
		Role: input.Role, DisplayName: name, NormalizedName: normalizedName,
		MediaType: mediaType, Object: &storage.Object{Key: key, Size: staged.Size, SHA256: staged.SHA256},
	}
	err = s.withSetupLock("object-key:"+staged.SHA256, func() error {
		if err := s.reservePreparedPublication(ctx, prepared); err != nil {
			return err
		}
		object, err := s.objects.Publish(ctx, staged)
		if err != nil {
			return storageError(err)
		}
		keep = true
		prepared.Object = object
		return s.markPublicationStored(ctx, prepared.ReservationJournalID)
	})
	if err != nil {
		s.abandonPreparedReservations(context.Background(), []preparedArtifact{*prepared}, err)
		s.cleanupStorageCandidates(context.Background(), []storageCandidate{{
			ID: prepared.StorageObjectID, Key: key, SHA256: staged.SHA256,
		}})
		return nil, err
	}
	return prepared, nil
}

func setupSheetMediaType(displayName string) (string, error) {
	extension := strings.ToLower(path.Ext(displayName))
	switch extension {
	case ".pdf":
		return mediaTypePDF, nil
	case ".html", ".htm":
		return mediaTypeHTML, nil
	default:
		return "", domain.NewError(domain.CodeUnsupportedFileType, "setup sheet must be PDF or standalone HTML")
	}
}

func validateSetupSheetContent(ctx context.Context, mediaType string, reader io.ReadSeeker) error {
	if mediaType == mediaTypePDF {
		signature := make([]byte, 5)
		if _, err := io.ReadFull(reader, signature); err != nil || !bytes.Equal(signature, []byte("%PDF-")) {
			return domain.NewError(domain.CodeInvalidContent, "setup sheet does not have a valid PDF signature")
		}
		return nil
	}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	prefix := make([]rune, 0, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return domain.WrapError(domain.CodeJobCancelled, "setup sheet validation was cancelled", err)
		}
		r, size, err := buffered.ReadRune()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return domain.WrapError(domain.CodeUploadIncomplete, "setup sheet could not be read completely", err)
		}
		if r == utf8.RuneError && size == 1 {
			return domain.NewError(domain.CodeInvalidContent, "HTML setup sheet is not valid UTF-8")
		}
		if r == 0 {
			return domain.NewError(domain.CodeInvalidContent, "HTML setup sheet contains NUL bytes")
		}
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return domain.NewError(domain.CodeInvalidContent, "HTML setup sheet contains invalid control characters")
		}
		if len(prefix) < cap(prefix) {
			prefix = append(prefix, unicode.ToLower(r))
		}
	}
	trimmed := strings.TrimSpace(string(prefix))
	if !strings.HasPrefix(trimmed, "<!doctype html") && !strings.HasPrefix(trimmed, "<html") {
		return domain.NewError(domain.CodeInvalidContent, "setup sheet is not a standalone HTML document")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return domain.WrapError(domain.CodeUploadIncomplete, "setup sheet could not be checked completely", err)
	}
	tokenizer := html.NewTokenizer(reader)
	tokenizer.SetMaxBuf(MaxHTMLSetupSheetTokenBytes)
	for {
		if err := ctx.Err(); err != nil {
			return domain.WrapError(domain.CodeJobCancelled, "setup sheet validation was cancelled", err)
		}
		if tokenizer.Next() != html.ErrorToken {
			continue
		}
		if err := tokenizer.Err(); err != nil && !errors.Is(err, io.EOF) {
			return domain.NewError(domain.CodeInvalidContent, "HTML setup sheet contains an unsupported oversized token")
		}
		return nil
	}
}

func (s *Service) ensurePreparedObjectTx(ctx context.Context, tx *sql.Tx, item *preparedArtifact) error {
	if err := s.reservePreparedObjectTx(ctx, tx, item); err != nil {
		return err
	}
	if item.ReservationJournalID == "" {
		return nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE operation_journal
		   SET state = 'completed', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND library_id = ? AND storage_object_id = ? AND state = 'storage_applied'`,
		item.ReservationJournalID, s.libraryID, item.StorageObjectID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return databaseError(fmt.Errorf("storage adoption reservation is no longer active"))
	}
	return nil
}

func (s *Service) reservePreparedObjectTx(ctx context.Context, tx *sql.Tx, item *preparedArtifact) error {
	if item == nil || item.Object == nil {
		return domain.NewError(domain.CodeStorageUnavailable, "managed object metadata is unavailable")
	}
	var objectID, mediaType, sha string
	var size int64
	err := tx.QueryRowContext(ctx, `
		SELECT id, media_type, byte_size, sha256
		  FROM storage_objects WHERE library_id = ? AND storage_key = ?`, s.libraryID, item.Object.Key).
		Scan(&objectID, &mediaType, &size, &sha)
	if err == nil {
		if mediaType != item.MediaType || size != item.Object.Size || sha != item.Object.SHA256 {
			return domain.NewError(domain.CodeArtifactChanged, "immutable object metadata conflicts with managed storage")
		}
		item.StorageObjectID = objectID
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return databaseError(err)
	}
	objectID, err = domain.NewStorageObjectID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO storage_objects(id, library_id, storage_key, media_type, byte_size, sha256, last_verified_at)
		VALUES (?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`, objectID, s.libraryID,
		item.Object.Key, item.MediaType, item.Object.Size, item.Object.SHA256)
	if err != nil {
		return databaseError(err)
	}
	item.StorageObjectID = objectID
	return nil
}

func (s *Service) reservePreparedPublication(ctx context.Context, item *preparedArtifact) (finalErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()
	if err := s.reservePreparedObjectTx(ctx, tx, item); err != nil {
		return err
	}
	reservationID, err := s.appendJournal(ctx, tx, domain.AuditOperationImport, "", "", item.StorageObjectID,
		"", "", 0, map[string]any{
			"kind": "storageAdoption", "role": item.Role, "byteSize": item.Object.Size,
		})
	if err != nil {
		return err
	}
	item.ReservationJournalID = reservationID
	if err := tx.Commit(); err != nil {
		return databaseError(err)
	}
	return nil
}

func (s *Service) markPublicationStored(ctx context.Context, reservationID string) error {
	if reservationID == "" {
		return domain.NewError(domain.CodeStorageUnavailable, "storage adoption reservation is unavailable")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE operation_journal
		   SET state = 'storage_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND library_id = ? AND state = 'intent'`, reservationID, s.libraryID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return databaseError(fmt.Errorf("storage adoption reservation transition failed"))
	}
	return nil
}

func (s *Service) abandonPreparedReservations(ctx context.Context, items []preparedArtifact, operationErr error) {
	code := safeErrorCode(operationErr)
	for _, item := range items {
		if item.ReservationJournalID == "" {
			continue
		}
		_, _ = s.db.ExecContext(ctx, `
			UPDATE operation_journal
			   SET state = 'failed', error_code = ?,
			       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
			       completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			 WHERE id = ? AND library_id = ? AND state IN ('intent', 'storage_applied', 'db_applied')`,
			code, item.ReservationJournalID, s.libraryID)
	}
}

func (s *Service) persistPreparedObject(ctx context.Context, item *preparedArtifact) error {
	if item == nil || item.Object == nil {
		return domain.NewError(domain.CodeStorageUnavailable, "managed object metadata is unavailable")
	}
	return s.withSetupLock("object-key:"+item.Object.SHA256, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		if err := s.reservePreparedObjectTx(ctx, tx, item); err != nil {
			return err
		}
		return databaseError(tx.Commit())
	})
}

func insertArtifactTx(ctx context.Context, tx *sql.Tx, setupID, artifactID string, item *preparedArtifact, position int, primary bool) error {
	primaryValue := 0
	if primary {
		primaryValue = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO setup_artifacts(id, setup_id, role, display_name, normalized_name, storage_object_id,
		                            position, is_primary, identity_device, identity_inode, identity_size,
		                            identity_mtime_ns, identity_ctime_ns, object_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactID, setupID, item.Role, item.DisplayName, item.NormalizedName, item.StorageObjectID,
		position, primaryValue, int64(item.Object.Identity.Device), int64(item.Object.Identity.Inode), item.Object.Size,
		item.Object.Identity.ModTimeNS, item.Object.Identity.ChangeTimeNS, item.Object.Version)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.NewError(domain.CodeNameConflict, "an artifact with this name already exists in the setup")
		}
		return databaseError(err)
	}
	return nil
}

func updateSetupForMutation(ctx context.Context, tx *sql.Tx, setupID string, expected domain.Revision,
	status domain.SetupStatus, revision domain.Revision,
) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE setups
		   SET status = ?, revision = ?, ready_revision = NULL,
		       attention_reason = CASE WHEN ? = 'attention' THEN attention_reason ELSE NULL END,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND revision = ? AND status <> 'archived'`, status, revision, status, setupID, expected)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed != 1 {
		return domain.NewError(domain.CodeRevisionConflict, "setup revision has changed")
	}
	return nil
}

// inspectArtifactForMutation binds the API's opaque expectedVersion to the
// physical immutable object immediately before a metadata/reference mutation.
// A database token alone is insufficient when an operator or attacker has
// replaced the directory entry outside the service.
func (s *Service) inspectArtifactForMutation(record *artifactRecord) error {
	if record == nil {
		return domain.NewError(domain.CodeArtifactChanged, "artifact content changed or is unavailable")
	}
	object, err := s.objects.InspectObject(record.StorageKey, record.SHA256, record.Version)
	if err == nil && object != nil && object.Size == record.ByteSize {
		return nil
	}
	if err == nil {
		err = storage.ErrObjectChanged
	}
	if errors.Is(err, storage.ErrObjectChanged) || errors.Is(err, storage.ErrInvalidObject) || errors.Is(err, fs.ErrNotExist) {
		return domain.WrapError(domain.CodeArtifactChanged, "artifact content changed or is unavailable", err)
	}
	return storageError(err)
}

func artifactMutationAttentionReason(cause error) string {
	if errors.Is(cause, fs.ErrNotExist) {
		return "managed artifact is missing"
	}
	return "managed artifact identity changed"
}

func (s *Service) markArtifactAttentionTx(ctx context.Context, tx *sql.Tx, setupID string, cause error) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE setups
		   SET status = CASE WHEN status = 'archived' THEN status ELSE 'attention' END,
		       ready_revision = CASE WHEN status = 'archived' THEN ready_revision ELSE NULL END,
		       attention_reason = CASE WHEN status = 'archived' THEN attention_reason ELSE ? END,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND library_id = ?`, artifactMutationAttentionReason(cause), setupID, s.libraryID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed != 1 {
		return domain.NewError(domain.CodeSetupNotFound, "setup was not found")
	}
	return nil
}

// commitArtifactAttentionTx is used by streaming replacements, whose
// idempotency claim is deliberately outside the composition transaction.
// No setup revision or artifact reference has been changed when it is called.
func (s *Service) commitArtifactAttentionTx(
	ctx context.Context,
	tx *sql.Tx,
	setupID string,
	operationErr error,
) error {
	if err := s.markArtifactAttentionTx(ctx, tx, setupID, operationErr); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return databaseError(err)
	}
	return operationErr
}

// finishArtifactMutationAttention rolls the logical mutation back to its
// savepoint, persists only attention plus the stable idempotent failure, then
// commits. Thus an external change cannot advance revision or alter refs.
func (s *Service) finishArtifactMutationAttention(
	ctx context.Context,
	tx *sql.Tx,
	claim idempotencyClaim,
	setupID string,
	operationErr error,
) error {
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT lifecycle_mutation"); err != nil {
		_ = tx.Rollback()
		return databaseError(err)
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT lifecycle_mutation"); err != nil {
		_ = tx.Rollback()
		return databaseError(err)
	}
	if err := s.markArtifactAttentionTx(ctx, tx, setupID, operationErr); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := finishIdempotencyTx(ctx, tx, claim, 0, nil, operationErr); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return databaseError(err)
	}
	return operationErr
}

func (s *Service) cleanupPreparedObjects(ctx context.Context, items []preparedArtifact) {
	s.abandonPreparedReservations(ctx, items, domain.NewError(domain.CodeJobCancelled, "storage adoption was not completed"))
	candidates := make([]storageCandidate, 0, len(items))
	for _, item := range items {
		if item.StorageObjectID != "" && item.Object != nil {
			candidates = append(candidates, storageCandidate{ID: item.StorageObjectID, Key: item.Object.Key, SHA256: item.Object.SHA256})
		}
	}
	s.cleanupStorageCandidates(ctx, candidates)
}

// cleanupStorageCandidates performs ref-safe garbage collection. Physical
// removal happens while SQLite holds an immediate write transaction and while
// the in-process object lock prevents a concurrent publication from adopting
// the same object between the reference check and unlink.
func (s *Service) cleanupStorageCandidates(ctx context.Context, candidates []storageCandidate) {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.Key == "" || candidate.SHA256 == "" {
			continue
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			continue
		}
		seen[candidate.ID] = struct{}{}
		_ = s.withSetupLock("object-key:"+candidate.SHA256, func() error {
			tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
			if err != nil {
				return err
			}
			defer tx.Rollback()
			var key, sha string
			var references int64
			err = tx.QueryRowContext(ctx, `
				SELECT storage_key, sha256, ref_count +
				       (SELECT COUNT(*) FROM import_artifacts WHERE storage_object_id = o.id) +
				       (SELECT COUNT(*) FROM operation_journal WHERE storage_object_id = o.id
				          AND state IN ('intent', 'storage_applied', 'db_applied'))
				  FROM storage_objects o WHERE id = ? AND library_id = ?`, candidate.ID, s.libraryID).
				Scan(&key, &sha, &references)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil || references != 0 || key != candidate.Key || sha != candidate.SHA256 {
				return err
			}
			if err := s.objects.RemoveObject(key, sha); err != nil {
				return storageError(err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM storage_objects WHERE id = ? AND ref_count = 0`, candidate.ID); err != nil {
				return err
			}
			return tx.Commit()
		})
	}
}

// ensure imports remain deterministic if callers supply artifact IDs as a
// set rather than in upload order.
func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func safeErrorCode(err error) domain.ErrorCode {
	if code, ok := domain.ErrorCodeOf(err); ok {
		return code
	}
	return domain.CodeDatabaseUnavailable
}

func rowsAffectedExactlyOne(result sql.Result, err error) error {
	if err != nil {
		return databaseError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if count != 1 {
		return fmt.Errorf("expected one affected row, got %d", count)
	}
	return nil
}
