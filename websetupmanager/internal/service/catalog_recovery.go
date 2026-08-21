package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

type catalogRecoveryRow struct {
	ID, Operation, State, Target, Temporary, ExpectedVersion, ResultVersion string
	Details                                                                 catalogOperationDetails
}

// RecoverCatalogOperations reconciles every filesystem operation whose final
// cleanup was interrupted. It must run after CatalogStore is opened and before
// readiness is published.
func (s *Service) RecoverCatalogOperations(ctx context.Context) error {
	if err := s.catalogAvailable(); err != nil {
		return err
	}
	return s.withSetupLock("catalog-tree", func() error {
		rows, err := s.db.QueryContext(ctx, `
			SELECT id, operation, state, target_path, COALESCE(temporary_path, ''),
			       COALESCE(expected_version, ''), COALESCE(result_version, ''), details_json
			  FROM catalog_operations
			 WHERE library_id = ? AND state IN ('intent', 'storage_applied', 'db_applied')
			 ORDER BY created_at, id`, s.libraryID)
		if err != nil {
			return databaseError(err)
		}
		defer rows.Close()
		operations := []catalogRecoveryRow{}
		for rows.Next() {
			var operation catalogRecoveryRow
			var details string
			if err := rows.Scan(&operation.ID, &operation.Operation, &operation.State, &operation.Target,
				&operation.Temporary, &operation.ExpectedVersion, &operation.ResultVersion, &details); err != nil {
				return databaseError(err)
			}
			if err := json.Unmarshal([]byte(details), &operation.Details); err != nil ||
				operation.Details.TargetPath != operation.Target || operation.Details.TemporaryPath != operation.Temporary {
				return domain.NewError(domain.CodeStorageUnavailable, "catalog operation journal is invalid")
			}
			operations = append(operations, operation)
		}
		if err := rows.Err(); err != nil {
			return databaseError(err)
		}
		if err := rows.Close(); err != nil {
			return databaseError(err)
		}
		for _, operation := range operations {
			if err := s.recoverCatalogOperation(ctx, operation); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) recoverCatalogOperation(ctx context.Context, operation catalogRecoveryRow) error {
	committed := operation.State == "db_applied"
	var result any
	var err error
	switch operation.Operation {
	case "publish":
		var object *storage.Object
		object, err = s.catalog.RecoverPublication(ctx, operation.Target, operation.Temporary,
			operation.Details.ExpectedSHA256, operation.Details.ResultSHA256,
			operation.ExpectedVersion, operation.ResultVersion,
			operation.Details.ExpectedDevice, operation.Details.ExpectedInode,
			operation.Details.ResultDevice, operation.Details.ResultInode,
			operation.Details.ExpectedSize, operation.Details.ResultSize, committed)
		if err == nil && object != nil {
			s.refreshCatalogFileIdentity(ctx, operation.Details.SetupID, operation.Target, object)
		}
		if committed && err == nil {
			result, err = s.loadCatalogSetup(ctx, s.db, operation.Details.SetupID, true)
		}
	case "delete":
		var object *storage.Object
		object, err = s.catalog.RecoverQuarantine(ctx, operation.Target, operation.Temporary,
			operation.Details.ExpectedSHA256, operation.Details.ExpectedDevice,
			operation.Details.ExpectedInode, operation.Details.ExpectedSize, committed)
		if err == nil && object != nil {
			s.refreshCatalogFileIdentity(ctx, operation.Details.SetupID, operation.Target, object)
		}
		if committed && err == nil && operation.Details.RequestOperation != "catalogDeleteSetup:"+operation.Details.SetupID {
			result, err = s.loadCatalogSetup(ctx, s.db, operation.Details.SetupID, true)
		}
	case "folder_delete":
		err = s.catalog.RecoverFolderQuarantine(operation.Target, operation.Temporary,
			operation.Details.ExpectedDevice, operation.Details.ExpectedInode, operation.Details.ExpectedSize, committed)
	case "folder_create":
		err = s.recoverCatalogFolderCreate(ctx, operation, committed)
		if committed && err == nil {
			result, err = s.loadCatalogFolder(ctx, s.db, operation.Details.FolderID)
		}
	case "move":
		err = s.recoverCatalogMove(ctx, operation, committed)
		if committed && err == nil {
			if operation.Details.FileID != "" {
				result, err = s.loadCatalogSetup(ctx, s.db, operation.Details.SetupID, true)
			} else {
				result, err = s.loadCatalogFolder(ctx, s.db, operation.Details.FolderID)
			}
		}
	default:
		return domain.NewError(domain.CodeStorageUnavailable, "catalog operation journal has an unknown operation")
	}
	if err != nil {
		return storageError(err)
	}
	terminal := "failed"
	var operationErr error = domain.NewError(domain.CodeStorageUnavailable, "catalog mutation was interrupted before commit")
	status := 0
	if committed {
		terminal, operationErr, status = "completed", nil, 200
		if operation.Operation == "folder_create" {
			status = 201
		}
		if operation.Operation == "folder_delete" || operation.Details.RequestOperation == "catalogDeleteSetup:"+operation.Details.SetupID {
			status = 204
		}
	}
	if err := s.finishRecoveredCatalogClaim(ctx, operation.Details, status, result, operationErr); err != nil {
		return err
	}
	return s.finishCatalogOperationRow(ctx, operation.ID, terminal)
}

func (s *Service) recoverCatalogFolderCreate(ctx context.Context, operation catalogRecoveryRow, committed bool) error {
	if committed {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM catalog_folders WHERE library_id = ? AND id = ? AND relative_path = ?`,
			s.libraryID, operation.Details.FolderID, operation.Target).Scan(&count); err != nil {
			return databaseError(err)
		}
		if count != 1 {
			return domain.NewError(domain.CodeDatabaseUnavailable, "committed catalog folder is missing")
		}
	}
	_, err := s.catalog.RecoverFolderCreate(operation.Target, operation.Temporary, operation.ResultVersion,
		operation.Details.ResultDevice, operation.Details.ResultInode, operation.Details.ResultSize, committed)
	return err
}

func (s *Service) recoverCatalogMove(ctx context.Context, operation catalogRecoveryRow, committed bool) error {
	from, to := operation.Details.SourcePath, operation.Target
	fromVersion, toVersion := operation.ExpectedVersion, operation.ResultVersion
	if !committed {
		from, to = operation.Target, operation.Details.SourcePath
		fromVersion, toVersion = operation.ResultVersion, operation.ExpectedVersion
	}
	matchesIdentity := func(object *storage.Object) bool {
		return object != nil && operation.Details.ExpectedDevice != 0 && operation.Details.ExpectedInode != 0 &&
			object.Identity.Device == operation.Details.ExpectedDevice && object.Identity.Inode == operation.Details.ExpectedInode &&
			object.Size == operation.Details.ExpectedSize
	}
	if operation.Details.FileID != "" {
		sha := operation.Details.ResultSHA256
		if sha == "" {
			sha = operation.Details.ExpectedSHA256
		}
		if object, inspectErr := s.catalog.Inspect(to, sha, toVersion); inspectErr == nil && matchesIdentity(object) {
			s.refreshCatalogFileIdentity(ctx, operation.Details.SetupID, to, object)
			return nil
		}
		object, inspectErr := s.catalog.Inspect(from, sha, fromVersion)
		if inspectErr != nil || !matchesIdentity(object) {
			if inspectErr == nil {
				inspectErr = storage.ErrObjectChanged
			}
			return inspectErr
		}
		moved, err := s.catalog.MoveExpected(ctx, from, to, sha, object.Version)
		if err != nil {
			return err
		}
		s.refreshCatalogFileIdentity(ctx, operation.Details.SetupID, to, moved)
		return nil
	}
	if object, inspectErr := s.catalog.InspectFolder(to, toVersion); inspectErr == nil && matchesIdentity(object) {
		return nil
	}
	object, err := s.catalog.InspectFolder(from, fromVersion)
	if err != nil || !matchesIdentity(object) {
		if err == nil {
			err = storage.ErrObjectChanged
		}
		return err
	}
	_, err = s.catalog.MoveExpected(ctx, from, to, "", object.Version)
	return err
}

func (s *Service) finishRecoveredCatalogClaim(ctx context.Context, details catalogOperationDetails,
	status int, result any, operationErr error,
) error {
	if details.IdempotencyKey == "" {
		return nil
	}
	var state, operation, requestHash string
	err := s.db.QueryRowContext(ctx, `SELECT state, operation, request_hash FROM idempotency_requests WHERE library_id = ? AND key = ?`,
		s.libraryID, details.IdempotencyKey).Scan(&state, &operation, &requestHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return databaseError(err)
	}
	if operation != details.RequestOperation || requestHash != details.RequestHash {
		return domain.NewError(domain.CodeDatabaseUnavailable, "catalog idempotency journal does not match")
	}
	if state != idempotencyStateInProgress {
		return nil
	}
	if operationErr != nil {
		resultSQL, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_requests
			WHERE library_id = ? AND key = ? AND operation = ? AND request_hash = ? AND state = 'in_progress'`,
			s.libraryID, details.IdempotencyKey, operation, requestHash)
		if err != nil {
			return databaseError(err)
		}
		changed, err := resultSQL.RowsAffected()
		if err != nil || changed != 1 {
			return databaseError(errors.New("catalog recovery idempotency claim changed"))
		}
		return nil
	}
	claim := idempotencyClaim{Key: details.IdempotencyKey, Operation: operation,
		RequestHash: requestHash, libraryID: s.libraryID}
	return s.finishIdempotency(ctx, claim, status, result, operationErr)
}
