package service

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

// DuplicateSetup enqueues creation of an independent draft aggregate. The
// duplicate gets new entity IDs while immutable storage objects may be shared
// through reference-counted database relationships.
func (s *Service) DuplicateSetup(ctx context.Context, setupID string, input DuplicateInput) (*domain.Job, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	name, err := domain.NormalizeSetupName(input.Name)
	if err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	hash, err := idempotencyRequestHash("duplicateSetup", struct {
		SetupID  string          `json:"setupId"`
		Revision domain.Revision `json:"revision"`
		Name     string          `json:"name"`
	}{setupID, input.ExpectedRevision, name})
	if err != nil {
		return nil, err
	}

	var job *domain.Job
	err = s.withSetupLock(setupID, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "duplicateSetup", hash)
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
			return finishDuplicateClaimFailure(ctx, tx, claim, err)
		}
		if err := domain.CheckExpectedRevision(setup.Revision, input.ExpectedRevision); err != nil {
			return finishDuplicateClaimFailure(ctx, tx, claim, err)
		}
		artifacts, err := s.loadArtifacts(ctx, tx, setupID)
		if err != nil {
			return finishDuplicateClaimFailure(ctx, tx, claim, err)
		}
		var totalBytes int64
		for _, artifact := range artifacts {
			if artifact.ByteSize > 0 {
				if totalBytes > int64(^uint64(0)>>1)-artifact.ByteSize {
					return finishDuplicateClaimFailure(ctx, tx, claim,
						domain.NewError(domain.CodeInvalidContent, "duplicate byte total exceeds supported range"))
				}
				totalBytes += artifact.ByteSize
			}
		}
		job, err = s.insertJobTx(ctx, tx, domain.JobKindDuplicate, setupID, "", &totalBytes)
		if err != nil {
			return finishDuplicateClaimFailure(ctx, tx, claim, err)
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 202, job, nil); err != nil {
			return err
		}
		return databaseError(tx.Commit())
	})
	if err != nil || job == nil {
		return job, err
	}
	s.launchJob(job.ID, func(jobCtx context.Context, progress func(domain.JobProgress) error) (any, error) {
		result, workErr := s.executeDuplicate(jobCtx, job.ID, setupID, input.ExpectedRevision, name, progress)
		if workErr != nil {
			_ = s.recordDuplicateFailure(context.Background(), job.ID, setupID, input.ExpectedRevision, workErr)
		}
		return result, workErr
	})
	return job, nil
}

func finishDuplicateClaimFailure(ctx context.Context, tx *sql.Tx, claim idempotencyClaim, operationErr error) error {
	if finishErr := finishIdempotencyTx(ctx, tx, claim, 0, nil, operationErr); finishErr != nil {
		_ = tx.Rollback()
		return finishErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return databaseError(commitErr)
	}
	return operationErr
}

type verifiedDuplicateArtifact struct {
	record artifactRecord
	object *storage.Object
}

func (s *Service) executeDuplicate(
	ctx context.Context,
	jobID, sourceID string,
	expected domain.Revision,
	name string,
	updateProgress func(domain.JobProgress) error,
) (*domain.Setup, error) {
	var duplicated *domain.Setup
	err := s.withSetupLock(sourceID, func() error {
		source, err := s.loadSetup(ctx, s.db, sourceID, false)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(source.Revision, expected); err != nil {
			return err
		}
		artifacts, err := s.loadArtifacts(ctx, s.db, sourceID)
		if err != nil {
			return err
		}
		verified := make([]verifiedDuplicateArtifact, 0, len(artifacts))
		var totalBytes, completedBytes int64
		for _, artifact := range artifacts {
			if artifact.ByteSize > 0 {
				if totalBytes > int64(^uint64(0)>>1)-artifact.ByteSize {
					return domain.NewError(domain.CodeInvalidContent, "duplicate byte total exceeds supported range")
				}
				totalBytes += artifact.ByteSize
			}
		}
		reporter := newJobProgressReporter(updateProgress, totalBytes, int64(len(artifacts)))
		for index, artifact := range artifacts {
			if err := ctx.Err(); err != nil {
				return err
			}
			objectStart := completedBytes
			var progressErr error
			object, err := s.objects.VerifyObjectWithProgress(ctx, artifact.StorageKey, artifact.SHA256, artifact.Version, func(objectBytes int64) error {
				progressErr = reporter.report(objectStart+objectBytes, int64(index), false)
				return progressErr
			})
			if progressErr != nil {
				return progressErr
			}
			if err != nil || object.Size != artifact.ByteSize {
				s.markContentAttention(context.Background(), sourceID, err)
				if err == nil {
					err = storage.ErrObjectChanged
				}
				return duplicateStorageError(err)
			}
			verified = append(verified, verifiedDuplicateArtifact{record: artifact, object: object})
			completedBytes += artifact.ByteSize
			if err := reporter.report(completedBytes, int64(index+1), true); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		duplicated, err = s.commitDuplicate(ctx, jobID, source, expected, name, verified)
		return err
	})
	return duplicated, err
}

func (s *Service) commitDuplicate(
	ctx context.Context,
	jobID string,
	source *domain.Setup,
	expected domain.Revision,
	name string,
	artifacts []verifiedDuplicateArtifact,
) (_ *domain.Setup, finalErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()
	current, err := s.loadSetup(ctx, tx, source.ID, false)
	if err != nil {
		return nil, err
	}
	if err := domain.CheckExpectedRevision(current.Revision, expected); err != nil {
		return nil, err
	}
	targetID, err := domain.NewSetupID()
	if err != nil {
		return nil, err
	}
	now := sqlTimestamp(s.now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO setups(
			id, library_id, name, description, status, revision, source,
			source_setup_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'draft', ?, 'duplicated', ?, ?, ?)`,
		targetID, s.libraryID, name, current.Description, domain.InitialRevision,
		current.ID, now, now); err != nil {
		return nil, databaseError(err)
	}
	journalID, err := s.appendJournal(ctx, tx, domain.AuditOperationDuplicate,
		targetID, "", "", "", jobID, expected,
		map[string]any{"sourceSetupId": source.ID, "artifactCount": len(artifacts)})
	if err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		artifactID, err := domain.NewArtifactID()
		if err != nil {
			return nil, err
		}
		normalized, err := domain.ArtifactNameKey(artifact.record.DisplayName)
		if err != nil {
			return nil, err
		}
		primary := 0
		if artifact.record.Primary {
			primary = 1
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO setup_artifacts(
				id, setup_id, role, display_name, normalized_name, storage_object_id,
				position, is_primary, identity_device, identity_inode, identity_size,
				identity_mtime_ns, identity_ctime_ns, object_version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			artifactID, targetID, artifact.record.Role, artifact.record.DisplayName, normalized,
			artifact.record.StorageObjectID, artifact.record.Position, primary,
			int64(artifact.object.Identity.Device), int64(artifact.object.Identity.Inode), artifact.object.Size,
			artifact.object.Identity.ModTimeNS, artifact.object.Identity.ChangeTimeNS, artifact.object.Version,
			now, now)
		if err != nil {
			return nil, databaseError(err)
		}
	}
	if err := completeJournal(ctx, tx, journalID, domain.InitialRevision); err != nil {
		return nil, err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditOperationDuplicate, targetID, "", jobID,
		expected, domain.InitialRevision, domain.AuditResultSucceeded, "",
		map[string]any{"sourceSetupId": source.ID}); err != nil {
		return nil, err
	}
	duplicated, err := s.loadSetup(ctx, tx, targetID, true)
	if err != nil {
		return nil, err
	}
	if err := s.finishJobTx(ctx, tx, jobID, duplicated, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return duplicated, nil
}

func duplicateStorageError(err error) error {
	if errors.Is(err, storage.ErrObjectChanged) || errors.Is(err, storage.ErrInvalidObject) || errors.Is(err, fs.ErrNotExist) {
		return domain.WrapError(domain.CodeArtifactChanged, "source artifact changed during duplication", err)
	}
	return storageError(err)
}

func (s *Service) recordDuplicateFailure(
	ctx context.Context,
	jobID, sourceID string,
	expected domain.Revision,
	cause error,
) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	actual := expected
	_ = tx.QueryRowContext(ctx, `
		SELECT revision FROM setups WHERE id = ? AND library_id = ?`, sourceID, s.libraryID).Scan(&actual)
	result := domain.AuditResultFailed
	code := safeErrorCode(cause)
	if errors.Is(cause, context.Canceled) || domain.IsErrorCode(cause, domain.CodeJobCancelled) {
		result = domain.AuditResultCancelled
		code = domain.CodeJobCancelled
	} else if idempotencyErrorIsConflict(code) {
		result = domain.AuditResultConflict
	}
	if err := s.appendAudit(ctx, tx, domain.AuditOperationDuplicate, sourceID, "", jobID,
		expected, actual, result, code, nil); err != nil {
		return err
	}
	return databaseError(tx.Commit())
}
