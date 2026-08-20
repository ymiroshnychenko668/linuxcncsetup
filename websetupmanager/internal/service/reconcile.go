package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

type ReconcileResult struct {
	SetupsChecked     int `json:"setupsChecked"`
	ArtifactsChecked  int `json:"artifactsChecked"`
	SetupsAttention   int `json:"setupsAttention"`
	SetupsRecovered   int `json:"setupsRecovered"`
	ArchivedAttention int `json:"archivedAttention"`
}

type GarbageCollectionResult struct {
	ObjectsExamined  int   `json:"objectsExamined"`
	ObjectsRemoved   int   `json:"objectsRemoved"`
	ObjectsProtected int   `json:"objectsProtected"`
	BytesRemoved     int64 `json:"bytesRemoved"`
}

type CleanupResult struct {
	IdempotencyRequests int64 `json:"idempotencyRequests"`
	DeleteConfirmations int64 `json:"deleteConfirmations"`
	ImportSessions      int64 `json:"importSessions"`
	UploadJobs          int64 `json:"uploadJobs"`
	StagingCleaned      bool  `json:"stagingCleaned"`
}

type reconcileCacheEntry struct {
	object *storage.Object
	err    error
}

// Reconcile verifies every logical artifact through the root-anchored store.
// External disappearance/replacement marks non-archived setups attention;
// a fully verified repaired attention setup returns to draft and requires
// validation. The identity-only startup pass never clears attention.
func (s *Service) Reconcile(ctx context.Context) (*ReconcileResult, error) {
	return s.reconcile(ctx, true)
}

// InspectManagedContent performs the startup-safe identity pass. It detects
// disappearance, inode replacement, size/mtime/ctime changes and special-file
// substitution without reading every byte of a multi-gigabyte library. The
// periodic Reconcile pass performs the full SHA-256 verification after the
// listener is available.
func (s *Service) InspectManagedContent(ctx context.Context) (*ReconcileResult, error) {
	return s.reconcile(ctx, false)
}

func (s *Service) reconcile(ctx context.Context, fullHash bool) (*ReconcileResult, error) {
	release, err := s.acquireHeavy(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer release()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM setups WHERE library_id = ? ORDER BY id`, s.libraryID)
	if err != nil {
		return nil, databaseError(err)
	}
	setupIDs := make([]string, 0)
	for rows.Next() {
		var setupID string
		if err := rows.Scan(&setupID); err != nil {
			rows.Close()
			return nil, databaseError(err)
		}
		setupIDs = append(setupIDs, setupID)
	}
	if err := rows.Close(); err != nil {
		return nil, databaseError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}

	result := &ReconcileResult{}
	cache := make(map[string]reconcileCacheEntry)
	for _, setupID := range setupIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := s.withSetupLock(setupID, func() error {
			setup, err := s.loadSetup(ctx, s.db, setupID, false)
			if err != nil {
				return err
			}
			artifacts, err := s.loadArtifacts(ctx, s.db, setupID)
			if err != nil {
				return err
			}
			changed := false
			for _, artifact := range artifacts {
				if err := ctx.Err(); err != nil {
					return err
				}
				result.ArtifactsChecked++
				// Resolve the current physical identity without accepting it. A
				// different identity is always a state change; a full digest pass may
				// safely rebind identical bytes (notably after a cold-copy restore),
				// but the setup remains attention and requires validation.
				current, inspectErr := s.objects.InspectObject(
					artifact.StorageKey, artifact.SHA256, "")
				identityChanged := inspectErr == nil && current.Version != artifact.Version
				verified := reconcileCacheEntry{object: current, err: inspectErr}
				if inspectErr == nil && fullHash {
					cacheKey := artifact.StorageObjectID + ":" + current.Version
					var exists bool
					verified, exists = cache[cacheKey]
					if !exists {
						verified.object, verified.err = s.objects.VerifyObject(
							ctx, artifact.StorageKey, artifact.SHA256, current.Version)
						cache[cacheKey] = verified
					}
				}
				if verified.err != nil || verified.object == nil || verified.object.Size != artifact.ByteSize {
					changed = true
					continue
				}
				if identityChanged {
					changed = true
					if !fullHash {
						continue
					}
					result, updateErr := s.db.ExecContext(ctx, `
						UPDATE setup_artifacts
						   SET identity_device = ?, identity_inode = ?, identity_size = ?,
						       identity_mtime_ns = ?, identity_ctime_ns = ?, object_version = ?,
						       updated_at = ?
						 WHERE id = ? AND setup_id = ? AND object_version = ?`,
						int64(verified.object.Identity.Device), int64(verified.object.Identity.Inode),
						verified.object.Size, verified.object.Identity.ModTimeNS,
						verified.object.Identity.ChangeTimeNS, verified.object.Version,
						sqlTimestamp(s.now()), artifact.ID, setupID, artifact.Version)
					if updateErr != nil {
						return databaseError(updateErr)
					}
					updated, updateErr := result.RowsAffected()
					if updateErr != nil {
						return databaseError(updateErr)
					}
					if updated != 1 {
						continue
					}
				}
				if _, err := s.db.ExecContext(ctx, `
					UPDATE storage_objects SET last_verified_at = ?
					 WHERE id = ? AND library_id = ?`,
					sqlTimestamp(s.now()), artifact.StorageObjectID, s.libraryID); err != nil {
					return databaseError(err)
				}
			}
			result.SetupsChecked++
			now := sqlTimestamp(s.now())
			if changed {
				if setup.Status == domain.SetupStatusArchived {
					_, err = s.db.ExecContext(ctx, `
						UPDATE setups SET attention_reason = ?, updated_at = ?
						 WHERE id = ? AND library_id = ? AND status = 'archived'`,
						"managed content changed while setup was archived", now, setupID, s.libraryID)
					result.ArchivedAttention++
				} else {
					_, err = s.db.ExecContext(ctx, `
						UPDATE setups
						   SET status = 'attention', ready_revision = NULL,
						       attention_reason = ?, updated_at = ?
						 WHERE id = ? AND library_id = ? AND status <> 'archived'`,
						"one or more managed artifacts changed or became unavailable", now,
						setupID, s.libraryID)
					result.SetupsAttention++
				}
			} else if fullHash && setup.Status == domain.SetupStatusAttention {
				_, err = s.db.ExecContext(ctx, `
					UPDATE setups
					   SET status = 'draft', ready_revision = NULL,
					       attention_reason = NULL, updated_at = ?
					 WHERE id = ? AND library_id = ? AND status = 'attention'`,
					now, setupID, s.libraryID)
				result.SetupsRecovered++
			} else if fullHash && setup.Status == domain.SetupStatusArchived && setup.ArchivedFromStatus != nil &&
				*setup.ArchivedFromStatus == domain.SetupStatusAttention {
				_, err = s.db.ExecContext(ctx, `
					UPDATE setups
					   SET archived_from_status = 'draft', attention_reason = NULL, updated_at = ?
					 WHERE id = ? AND library_id = ? AND status = 'archived'
					   AND archived_from_status = 'attention'`, now, setupID, s.libraryID)
			}
			return databaseError(err)
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GarbageCollect removes only database-known immutable objects with no setup,
// import, or unfinished-journal reference. The SQLite write transaction and
// per-object lock stay held across unlink, closing the adoption race.
func (s *Service) GarbageCollect(ctx context.Context) (*GarbageCollectionResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, storage_key, sha256, byte_size
		  FROM storage_objects
		 WHERE library_id = ? AND ref_count = 0
		 ORDER BY id`, s.libraryID)
	if err != nil {
		return nil, databaseError(err)
	}
	candidates := make([]storageCandidate, 0)
	sizes := make(map[string]int64)
	for rows.Next() {
		var candidate storageCandidate
		var sha sql.NullString
		var size int64
		if err := rows.Scan(&candidate.ID, &candidate.Key, &sha, &size); err != nil {
			rows.Close()
			return nil, databaseError(err)
		}
		candidate.SHA256 = sha.String
		candidates = append(candidates, candidate)
		sizes[candidate.ID] = size
	}
	if err := rows.Close(); err != nil {
		return nil, databaseError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}

	result := &GarbageCollectionResult{ObjectsExamined: len(candidates)}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := s.withSetupLock("object-key:"+candidate.SHA256, func() error {
			tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
			if err != nil {
				return databaseError(err)
			}
			defer tx.Rollback()
			var key string
			var sha sql.NullString
			var references int64
			err = tx.QueryRowContext(ctx, `
				SELECT storage_key, sha256,
				       ref_count +
				       (SELECT count(*) FROM import_artifacts WHERE storage_object_id = o.id) +
				       (SELECT count(*) FROM operation_journal WHERE storage_object_id = o.id
				          AND state IN ('intent', 'storage_applied', 'db_applied'))
				  FROM storage_objects o
				 WHERE id = ? AND library_id = ?`, candidate.ID, s.libraryID).
				Scan(&key, &sha, &references)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return databaseError(err)
			}
			if references != 0 || key != candidate.Key || !sha.Valid || sha.String != candidate.SHA256 {
				result.ObjectsProtected++
				return nil
			}
			if err := s.objects.RemoveObject(key, sha.String); err != nil {
				return storageError(err)
			}
			deleted, err := tx.ExecContext(ctx, `
				DELETE FROM storage_objects
				 WHERE id = ? AND library_id = ? AND ref_count = 0
				   AND NOT EXISTS (
				       SELECT 1 FROM operation_journal
				        WHERE storage_object_id = ?
				          AND state IN ('intent', 'storage_applied', 'db_applied'))`,
				candidate.ID, s.libraryID, candidate.ID)
			if err != nil {
				return databaseError(err)
			}
			changed, err := deleted.RowsAffected()
			if err != nil {
				return databaseError(err)
			}
			if changed != 1 {
				return databaseError(errors.New("garbage collection reference changed"))
			}
			if err := tx.Commit(); err != nil {
				return databaseError(err)
			}
			result.ObjectsRemoved++
			result.BytesRemoved += sizes[candidate.ID]
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// CleanupExpired removes bounded replay/confirmation records and terminalizes
// abandoned import sessions. Physical staging cleanup is a startup operation:
// doing it here could race a new upload between the active-session check and
// directory cleanup.
func (s *Service) CleanupExpired(ctx context.Context) (*CleanupResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	now := sqlTimestamp(s.now())
	result := &CleanupResult{}
	expiredUploads, err := tx.ExecContext(ctx, `
		UPDATE jobs SET state = 'cancelled', cancel_requested = 1, error_code = ?,
		       updated_at = ?, finished_at = ?
		 WHERE library_id = ? AND state = 'queued'
		   AND kind IN (?, ?, ?)
		   AND julianday(created_at) <= julianday(?)`,
		domain.CodeJobCancelled, now, now, s.libraryID,
		domain.JobKindAddPrograms, domain.JobKindReplaceProgram, domain.JobKindUpdateSetupSheet,
		sqlTimestamp(s.now().Add(-s.idempotencyTTL)))
	if err != nil {
		return nil, databaseError(err)
	}
	result.UploadJobs, err = expiredUploads.RowsAffected()
	if err != nil {
		return nil, databaseError(err)
	}
	deleted, err := tx.ExecContext(ctx, `
		DELETE FROM idempotency_requests
		 WHERE library_id = ? AND state <> 'in_progress'
		   AND julianday(expires_at) <= julianday(?)`, s.libraryID, now)
	if err != nil {
		return nil, databaseError(err)
	}
	result.IdempotencyRequests, err = deleted.RowsAffected()
	if err != nil {
		return nil, databaseError(err)
	}
	deleted, err = tx.ExecContext(ctx, `
		DELETE FROM delete_confirmations
		 WHERE library_id = ?
		   AND (consumed_at IS NOT NULL OR julianday(expires_at) <= julianday(?))`, s.libraryID, now)
	if err != nil {
		return nil, databaseError(err)
	}
	result.DeleteConfirmations, err = deleted.RowsAffected()
	if err != nil {
		return nil, databaseError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	imports, err := s.CleanupExpiredImports(ctx)
	if err != nil {
		return nil, err
	}
	result.ImportSessions = int64(imports)
	return result, nil
}
