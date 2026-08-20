package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

const interruptedOperationErrorCode = "PROCESS_INTERRUPTED"

// OperationRecoveryResult reports durable journal decisions made after the
// managed object store became available. It intentionally contains counts,
// never physical storage identifiers or paths.
type OperationRecoveryResult struct {
	Examined   int `json:"examined"`
	Completed  int `json:"completed"`
	Conflicted int `json:"conflicted"`
}

type interruptedOperation struct {
	ID              string
	Operation       string
	SetupID         string
	ArtifactID      string
	StorageObjectID string
	ImportSessionID string
	JobID           string
	Details         []byte
}

type recoveryObject struct {
	Key              string
	SHA256           string
	Size             int64
	Version          string
	SetupReferences  int
	ImportReferences int
	ImportEvidence   bool
}

type journalRecoveryDetails struct {
	Kind             string `json:"kind"`
	ImportArtifactID string `json:"importArtifactId"`
}

// RecoverOperations reconciles journals that were active when the previous
// process stopped. Normal domain mutations and their journal transition share
// one SQLite transaction, so an active normal mutation is necessarily
// inconclusive and becomes conflict. The only independently durable step is a
// managed-object publication reservation; it is completed only when the object
// hashes correctly and a matching logical adoption is already durable.
//
// The operation is idempotent: only active journal states are enumerated and
// every transition has an active-state predicate.
func (s *Service) RecoverOperations(ctx context.Context) (*OperationRecoveryResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation, COALESCE(setup_id, ''), COALESCE(artifact_id, ''),
		       COALESCE(storage_object_id, ''), COALESCE(import_session_id, ''),
		       COALESCE(job_id, ''), details_json
		  FROM operation_journal
		 WHERE library_id = ? AND state IN ('intent', 'storage_applied', 'db_applied')
		 ORDER BY id`, s.libraryID)
	if err != nil {
		return nil, databaseError(err)
	}
	operations := make([]interruptedOperation, 0)
	for rows.Next() {
		var operation interruptedOperation
		if err := rows.Scan(&operation.ID, &operation.Operation, &operation.SetupID,
			&operation.ArtifactID, &operation.StorageObjectID, &operation.ImportSessionID,
			&operation.JobID, &operation.Details); err != nil {
			rows.Close()
			return nil, databaseError(err)
		}
		operations = append(operations, operation)
	}
	if err := rows.Close(); err != nil {
		return nil, databaseError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}

	result := &OperationRecoveryResult{}
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result.Examined++
		complete, err := s.recoverOperation(ctx, operation)
		if err != nil {
			return nil, err
		}
		state := domain.JournalStateConflict
		if complete {
			state = domain.JournalStateCompleted
		}
		changed, err := s.finishRecoveredOperation(ctx, operation.ID, state)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		if complete {
			result.Completed++
		} else {
			result.Conflicted++
		}
	}
	return result, nil
}

func (s *Service) recoverOperation(ctx context.Context, operation interruptedOperation) (bool, error) {
	if operation.StorageObjectID == "" {
		// Read all optional logical links before resolving the journal. This also
		// makes database corruption/unavailability a startup failure rather than
		// silently labelling an unexamined mutation as a conflict.
		if err := s.observeInterruptedLinks(ctx, operation); err != nil {
			return false, err
		}
		return false, nil
	}

	object, err := s.loadRecoveryObject(ctx, s.db, operation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, databaseError(err)
	}
	if object.SHA256 == "" {
		return false, nil
	}

	release, err := s.acquireHeavy(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	var details journalRecoveryDetails
	if err := json.Unmarshal(operation.Details, &details); err != nil {
		return false, nil
	}
	complete := false
	err = s.withSetupLock("object-key:"+object.SHA256, func() error {
		current, verifyErr := s.objects.VerifyObject(ctx, object.Key, object.SHA256, object.Version)
		if verifyErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(verifyErr, fs.ErrNotExist) || errors.Is(verifyErr, storage.ErrInvalidObject) ||
				errors.Is(verifyErr, storage.ErrObjectChanged) {
				return nil
			}
			return storageError(verifyErr)
		}
		if current == nil || current.Size != object.Size ||
			(object.Version != "" && current.Version != object.Version) {
			return nil
		}
		// Re-read logical evidence after hashing. The journal itself prevents GC,
		// while the object-key lock serializes publication and collection.
		latest, loadErr := s.loadRecoveryObject(ctx, s.db, operation)
		if errors.Is(loadErr, sql.ErrNoRows) {
			return nil
		}
		if loadErr != nil {
			return databaseError(loadErr)
		}
		if latest.Key != object.Key || latest.SHA256 != object.SHA256 ||
			latest.Size != object.Size || latest.Version == "" || latest.Version != current.Version {
			return nil
		}
		complete = recoveryHasCompletionEvidence(operation.Operation, details, latest)
		if _, updateErr := s.db.ExecContext(ctx, `
			UPDATE storage_objects SET last_verified_at = ?
			 WHERE id = ? AND library_id = ? AND storage_key = ? AND sha256 = ? AND byte_size = ?`,
			sqlTimestamp(s.now()), operation.StorageObjectID, s.libraryID,
			latest.Key, latest.SHA256, latest.Size); updateErr != nil {
			return databaseError(updateErr)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return complete, nil
}

func recoveryHasCompletionEvidence(operation string, details journalRecoveryDetails, object recoveryObject) bool {
	if operation != string(domain.AuditOperationImport) {
		return false
	}
	adopted := object.SetupReferences > 0 || object.ImportReferences > 0
	switch {
	case details.Kind == "storageAdoption":
		return adopted
	case details.ImportArtifactID != "":
		return object.ImportEvidence
	default:
		// A valid referenced object is not enough to infer that an arbitrary
		// replace/delete mutation committed; those journals remain conflicts.
		return false
	}
}

type recoveryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Service) loadRecoveryObject(ctx context.Context, query recoveryQueryer,
	operation interruptedOperation,
) (recoveryObject, error) {
	var object recoveryObject
	var sha sql.NullString
	var setupVersions, importVersions string
	err := query.QueryRowContext(ctx, `
		SELECT o.storage_key, o.sha256, o.byte_size,
		       COALESCE((SELECT group_concat(DISTINCT object_version)
		                   FROM setup_artifacts WHERE storage_object_id = o.id), ''),
		       COALESCE((SELECT group_concat(DISTINCT object_version)
		                   FROM import_artifacts WHERE storage_object_id = o.id
		                    AND state IN ('staged', 'published')), ''),
		       (SELECT count(*) FROM setup_artifacts WHERE storage_object_id = o.id),
		       (SELECT count(*) FROM import_artifacts WHERE storage_object_id = o.id
		         AND state IN ('staged', 'published')),
		       CASE WHEN ? = '' THEN 0 ELSE EXISTS (
		           SELECT 1 FROM import_artifacts a
		            WHERE a.id = ?
		              AND (? = '' OR a.import_session_id = ?)
		              AND ((a.storage_object_id = o.id AND a.state IN ('staged', 'published'))
		                   OR EXISTS (SELECT 1 FROM setup_artifacts sa
		                               WHERE sa.id = a.artifact_id AND sa.storage_object_id = o.id))
		       ) END
		  FROM storage_objects o
		 WHERE o.id = ? AND o.library_id = ?`, recoveryImportArtifactID(operation.Details),
		recoveryImportArtifactID(operation.Details), operation.ImportSessionID,
		operation.ImportSessionID, operation.StorageObjectID, s.libraryID).
		Scan(&object.Key, &sha, &object.Size, &setupVersions, &importVersions,
			&object.SetupReferences, &object.ImportReferences, &object.ImportEvidence)
	if err != nil {
		return recoveryObject{}, err
	}
	if sha.Valid {
		object.SHA256 = sha.String
	}
	versions := make(map[string]struct{})
	for _, list := range []string{setupVersions, importVersions} {
		for _, version := range strings.Split(list, ",") {
			if version != "" {
				versions[version] = struct{}{}
			}
		}
	}
	if len(versions) == 1 {
		for version := range versions {
			object.Version = version
		}
	}
	return object, nil
}

func recoveryImportArtifactID(details []byte) string {
	var decoded journalRecoveryDetails
	if json.Unmarshal(details, &decoded) != nil {
		return ""
	}
	return decoded.ImportArtifactID
}

func (s *Service) observeInterruptedLinks(ctx context.Context, operation interruptedOperation) error {
	var setupRevision, artifactReferences, importReferences, jobReferences int64
	return databaseError(s.db.QueryRowContext(ctx, `
		SELECT CASE WHEN ? = '' THEN 0 ELSE COALESCE((SELECT revision FROM setups
		                                              WHERE id = ? AND library_id = ?), 0) END,
		       CASE WHEN ? = '' THEN 0 ELSE (SELECT count(*) FROM setup_artifacts WHERE id = ?) END,
		       CASE WHEN ? = '' THEN 0 ELSE (SELECT count(*) FROM import_sessions
		                                      WHERE id = ? AND library_id = ?) END,
		       CASE WHEN ? = '' THEN 0 ELSE (SELECT count(*) FROM jobs
		                                      WHERE id = ? AND library_id = ?) END`,
		operation.SetupID, operation.SetupID, s.libraryID,
		operation.ArtifactID, operation.ArtifactID,
		operation.ImportSessionID, operation.ImportSessionID, s.libraryID,
		operation.JobID, operation.JobID, s.libraryID).
		Scan(&setupRevision, &artifactReferences, &importReferences, &jobReferences))
}

func (s *Service) finishRecoveredOperation(ctx context.Context, operationID string,
	state domain.JournalState,
) (bool, error) {
	errorCode := ""
	if state == domain.JournalStateConflict {
		errorCode = interruptedOperationErrorCode
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE operation_journal
		   SET state = ?, error_code = NULLIF(?, ''),
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND library_id = ?
		   AND state IN ('intent', 'storage_applied', 'db_applied')`,
		state, errorCode, operationID, s.libraryID)
	if err != nil {
		return false, databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, databaseError(err)
	}
	return changed == 1, nil
}
