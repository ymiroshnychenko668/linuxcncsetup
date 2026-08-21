package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

type legacyCatalogArtifact struct {
	ID, Role, DisplayName, ObjectID, StorageKey, MediaType, SHA256, Version string
	ByteSize                                                                int64
}

// MigrateLegacyCatalog is an idempotent 0/1/N split migrator. Legacy rows and
// immutable objects remain untouched; every copied file is recorded in the
// manifest. A result requiring operator judgment is persisted as manual_review
// and returned as an error so startup never silently loses work.
func (s *Service) MigrateLegacyCatalog(ctx context.Context) error {
	if err := s.catalogAvailable(); err != nil {
		return err
	}
	if err := s.ensureCatalogState(ctx); err != nil {
		return err
	}
	return s.withSetupLock("catalog-legacy-migration", func() error {
		var migrationState string
		if err := s.db.QueryRowContext(ctx, `SELECT legacy_migration_state FROM catalog_state WHERE library_id = ?`, s.libraryID).Scan(&migrationState); err != nil {
			return databaseError(err)
		}
		switch migrationState {
		case "completed":
			return nil
		case "manual_review", "failed":
			return domain.NewError(domain.CodeInvalidContent, "legacy catalog migration requires manual review")
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE catalog_state SET legacy_migration_state = 'running', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE library_id = ? AND legacy_migration_state IN ('pending','running')`, s.libraryID); err != nil {
			return databaseError(err)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, name, description FROM setups WHERE library_id = ? ORDER BY created_at, id`, s.libraryID)
		if err != nil {
			return databaseError(err)
		}
		type legacySetup struct{ id, name, description string }
		legacySetups := []legacySetup{}
		for rows.Next() {
			var setup legacySetup
			if err := rows.Scan(&setup.id, &setup.name, &setup.description); err != nil {
				rows.Close()
				return databaseError(err)
			}
			legacySetups = append(legacySetups, setup)
		}
		if err := rows.Close(); err != nil {
			return databaseError(err)
		}
		if err := rows.Err(); err != nil {
			return databaseError(err)
		}
		if len(legacySetups) == 0 {
			_, err := s.db.ExecContext(ctx, `UPDATE catalog_state SET legacy_migration_state = 'completed', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE library_id = ?`, s.libraryID)
			return databaseError(err)
		}
		root, err := s.ensureLegacyCatalogFolder(ctx, "", "Импортировано", "legacy-folder-root")
		if err != nil {
			return s.failLegacyCatalogMigration(ctx, "", err)
		}
		for _, legacy := range legacySetups {
			if err := ctx.Err(); err != nil {
				return err
			}
			folderName := legacy.name
			if _, err := domain.NormalizeArtifactName(folderName); err != nil {
				folderName = "Legacy-" + legacy.id[:min(8, len(legacy.id))]
			}
			folder, err := s.ensureLegacyCatalogFolder(ctx, root.ID, folderName, "legacy-folder:"+legacy.id)
			if err != nil {
				return s.failLegacyCatalogMigration(ctx, legacy.id, err)
			}
			artifacts, err := s.loadLegacyCatalogArtifacts(ctx, legacy.id)
			if err != nil {
				return s.failLegacyCatalogMigration(ctx, legacy.id, err)
			}
			programs := []legacyCatalogArtifact{}
			var sheet *legacyCatalogArtifact
			for index := range artifacts {
				switch artifacts[index].Role {
				case string(domain.ArtifactRoleProgram):
					programs = append(programs, artifacts[index])
				case string(domain.ArtifactRoleSetupSheet):
					if sheet != nil {
						return s.failLegacyCatalogMigration(ctx, legacy.id,
							domain.NewError(domain.CodeInvalidContent, "legacy setup has multiple setup sheets"))
					}
					sheet = &artifacts[index]
				}
			}
			targets := programs
			if len(targets) == 0 {
				targets = []legacyCatalogArtifact{{}}
			}
			usedSetupNames := make(map[string]int, len(targets))
			for index, program := range targets {
				sourceKey := "legacy:" + legacy.id + ":"
				if program.ID == "" {
					sourceKey += "empty"
				} else {
					sourceKey += program.ID
				}
				setupName := legacy.name
				if program.DisplayName != "" {
					setupName = strings.TrimSuffix(program.DisplayName, path.Ext(program.DisplayName))
				}
				setupName, err = domain.NormalizeSetupName(setupName)
				if err != nil {
					setupName = "Legacy-" + legacy.id[:min(8, len(legacy.id))] + fmt.Sprintf("-%d", index+1)
				}
				baseName := setupName
				for suffix := 1; ; suffix++ {
					setupNameKey, keyErr := domain.SetupNameKey(setupName)
					if keyErr != nil {
						return s.failLegacyCatalogMigration(ctx, legacy.id, keyErr)
					}
					if usedSetupNames[setupNameKey] == 0 {
						usedSetupNames[setupNameKey] = 1
						break
					}
					setupName = fmt.Sprintf("%s (%d)", baseName, suffix+1)
				}
				completed, err := s.legacyMappingCompleted(ctx, sourceKey, setupName, program, sheet)
				if err != nil {
					return s.failLegacyCatalogMigration(ctx, legacy.id, err)
				}
				if completed {
					continue
				}
				if _, err := s.db.ExecContext(ctx, `INSERT INTO catalog_legacy_migrations(
					source_key, library_id, legacy_setup_id, legacy_program_artifact_id, target_folder_id, target_name, state)
					VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, 'pending')
					ON CONFLICT(source_key) DO UPDATE SET target_folder_id = excluded.target_folder_id,
					 target_name = excluded.target_name, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
					sourceKey, s.libraryID, legacy.id, program.ID, folder.ID, setupName); err != nil {
					return s.failLegacyCatalogMigration(ctx, legacy.id, databaseError(err))
				}
				catalogSetup, err := s.CreateCatalogSetup(ctx, CreateCatalogSetupInput{FolderID: folder.ID,
					Name: setupName, Description: legacy.description, IdempotencyKey: "legacy-setup:" + sourceKey})
				if err != nil {
					return s.failLegacyMapping(ctx, sourceKey, err)
				}
				if _, err := s.db.ExecContext(ctx, `UPDATE catalog_setups SET legacy_setup_id = ? WHERE id = ? AND library_id = ?`, legacy.id, catalogSetup.ID, s.libraryID); err != nil {
					return s.failLegacyMapping(ctx, sourceKey, databaseError(err))
				}
				if _, err := s.db.ExecContext(ctx, `UPDATE catalog_legacy_migrations SET catalog_setup_id = ?, state = 'publishing', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE source_key = ?`, catalogSetup.ID, sourceKey); err != nil {
					return s.failLegacyMapping(ctx, sourceKey, databaseError(err))
				}
				if program.ID != "" {
					catalogSetup, err = s.copyLegacyCatalogArtifact(ctx, sourceKey, catalogSetup, program,
						domain.ArtifactRoleProgram, program.DisplayName, "legacy-file:"+sourceKey+":program")
					if err != nil {
						return s.failLegacyMapping(ctx, sourceKey, err)
					}
				}
				if sheet != nil {
					sheetName := sheet.DisplayName
					if len(targets) > 1 {
						sheetName = legacySheetName(setupName, *sheet)
					}
					catalogSetup, err = s.copyLegacyCatalogArtifact(ctx, sourceKey, catalogSetup, *sheet,
						domain.ArtifactRoleSetupSheet, sheetName, "legacy-file:"+sourceKey+":sheet")
					if err != nil {
						return s.failLegacyMapping(ctx, sourceKey, err)
					}
				}
				if _, err := s.db.ExecContext(ctx, `UPDATE catalog_legacy_migrations SET state = 'completed', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE source_key = ?`, sourceKey); err != nil {
					return databaseError(err)
				}
			}
		}
		_, err = s.db.ExecContext(ctx, `UPDATE catalog_state SET legacy_migration_state = 'completed', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE library_id = ?`, s.libraryID)
		return databaseError(err)
	})
}

func (s *Service) ensureLegacyCatalogFolder(ctx context.Context, parentID, name, key string) (*domain.CatalogFolder, error) {
	nameKey, err := domain.ArtifactNameKey(name)
	if err != nil {
		return nil, err
	}
	folder, err := scanCatalogFolder(s.db.QueryRowContext(ctx, `SELECT id, COALESCE(parent_id, ''), name, relative_path, revision, created_at, updated_at
		FROM catalog_folders WHERE library_id = ? AND legacy_source_key = ?`, s.libraryID, key))
	if err == nil {
		actualNameKey, keyErr := domain.ArtifactNameKey(folder.Name)
		if keyErr != nil || folder.ParentFolderID != parentID || actualNameKey != nameKey {
			return nil, domain.NewError(domain.CodeNameConflict, "legacy folder provenance does not match its target")
		}
		return folder, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, databaseError(err)
	}
	folder, err = s.CreateCatalogFolder(ctx, CreateCatalogFolderInput{ParentFolderID: parentID, Name: name, IdempotencyKey: key})
	if err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE catalog_folders SET legacy_source_key = ?
		WHERE library_id = ? AND id = ? AND legacy_source_key IS NULL`, key, s.libraryID, folder.ID)
	if err != nil {
		return nil, databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, databaseError(errors.New("legacy folder provenance changed"))
	}
	return folder, nil
}

func (s *Service) loadLegacyCatalogArtifacts(ctx context.Context, setupID string) ([]legacyCatalogArtifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT artifact.id, artifact.role, artifact.display_name,
		object.id, object.storage_key, object.media_type, object.byte_size, COALESCE(object.sha256, ''), artifact.object_version
		FROM setup_artifacts artifact JOIN storage_objects object ON object.id = artifact.storage_object_id
		WHERE artifact.setup_id = ? ORDER BY artifact.role, artifact.position, artifact.id`, setupID)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := []legacyCatalogArtifact{}
	for rows.Next() {
		var artifact legacyCatalogArtifact
		if err := rows.Scan(&artifact.ID, &artifact.Role, &artifact.DisplayName, &artifact.ObjectID,
			&artifact.StorageKey, &artifact.MediaType, &artifact.ByteSize, &artifact.SHA256, &artifact.Version); err != nil {
			return nil, databaseError(err)
		}
		if artifact.SHA256 == "" {
			return nil, domain.NewError(domain.CodeInvalidContent, "legacy artifact has no verified digest")
		}
		result = append(result, artifact)
	}
	return result, databaseError(rows.Err())
}

func (s *Service) copyLegacyCatalogArtifact(ctx context.Context, sourceKey string, setup *domain.CatalogSetup,
	artifact legacyCatalogArtifact, role domain.ArtifactRole, displayName, idempotencyKey string,
) (*domain.CatalogSetup, error) {
	manifestID, err := domain.NewOperationID()
	if err != nil {
		return nil, err
	}
	folderPath, err := s.catalogFolderPath(ctx, s.db, setup.FolderID)
	if err != nil {
		return nil, err
	}
	target := catalogRelative(folderPath, displayName)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO catalog_legacy_file_manifest(
		id, source_key, legacy_artifact_id, role, target_relative_path, byte_size, sha256, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending')
		ON CONFLICT(source_key, role) DO NOTHING`, manifestID, sourceKey, artifact.ID, role,
		target, artifact.ByteSize, artifact.SHA256); err != nil {
		return nil, databaseError(err)
	}
	var recordedArtifactID, recordedTarget, recordedSHA, recordedOutcome, recordedFileID string
	var recordedSize int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(legacy_artifact_id, ''), target_relative_path,
		byte_size, sha256, outcome, COALESCE(catalog_file_id, '')
		FROM catalog_legacy_file_manifest WHERE source_key = ? AND role = ?`, sourceKey, role).Scan(
		&recordedArtifactID, &recordedTarget, &recordedSize, &recordedSHA, &recordedOutcome, &recordedFileID); err != nil {
		return nil, databaseError(err)
	}
	if recordedArtifactID != artifact.ID || recordedTarget != target || recordedSize != artifact.ByteSize ||
		recordedSHA != artifact.SHA256 || recordedOutcome != "pending" && recordedOutcome != "copied" ||
		recordedOutcome == "pending" && recordedFileID != "" {
		return nil, domain.NewError(domain.CodeInvalidContent, "legacy migration manifest conflicts with its source")
	}
	file, err := s.objects.OpenObject(artifact.StorageKey, artifact.SHA256, artifact.Version)
	if err != nil {
		return nil, storageError(err)
	}
	updated, putErr := s.PutCatalogFile(ctx, setup.ID, role, PutCatalogFileInput{ExpectedRevision: setup.Revision,
		CreateOnly: true, DisplayName: displayName, Content: file, ExpectedSize: artifact.ByteSize,
		ExpectedSHA256: artifact.SHA256, IdempotencyKey: idempotencyKey})
	closeErr := file.Close()
	if putErr != nil {
		return nil, putErr
	}
	if closeErr != nil {
		return nil, storageError(closeErr)
	}
	copied := catalogFileForRole(updated, role)
	if copied == nil {
		return nil, domain.NewError(domain.CodeDatabaseUnavailable, "migrated catalog file is missing")
	}
	if copied.SHA256 != artifact.SHA256 || copied.ByteSize != artifact.ByteSize {
		return nil, domain.NewError(domain.CodeInvalidContent, "migrated catalog file does not match its source")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_files SET legacy_storage_object_id = ?
		WHERE library_id = ? AND id = ?`, artifact.ObjectID, s.libraryID, copied.ID); err != nil {
		return nil, databaseError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_legacy_file_manifest SET catalog_file_id = ?, outcome = 'copied',
		updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE source_key = ? AND role = ?`,
		copied.ID, sourceKey, role); err != nil {
		return nil, databaseError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return updated, nil
}

func (s *Service) legacyMappingCompleted(ctx context.Context, sourceKey, expectedName string, program legacyCatalogArtifact,
	sheet *legacyCatalogArtifact,
) (bool, error) {
	var state, setupID, targetName string
	err := s.db.QueryRowContext(ctx, `SELECT state, COALESCE(catalog_setup_id, ''), target_name
		FROM catalog_legacy_migrations WHERE source_key = ? AND library_id = ?`, sourceKey, s.libraryID).Scan(&state, &setupID, &targetName)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, databaseError(err)
	}
	if state != "completed" {
		return false, nil
	}
	if setupID == "" || targetName != expectedName {
		return false, domain.NewError(domain.CodeDatabaseUnavailable, "completed legacy mapping target changed")
	}
	expectedManifests := 0
	if program.ID != "" {
		expectedManifests++
		if err := s.verifyLegacyCatalogManifest(ctx, sourceKey, setupID, domain.ArtifactRoleProgram, program); err != nil {
			return false, err
		}
	}
	if sheet != nil {
		expectedManifests++
		if err := s.verifyLegacyCatalogManifest(ctx, sourceKey, setupID, domain.ArtifactRoleSetupSheet, *sheet); err != nil {
			return false, err
		}
	}
	var actualManifests int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM catalog_legacy_file_manifest WHERE source_key = ?`, sourceKey).Scan(&actualManifests); err != nil {
		return false, databaseError(err)
	}
	if actualManifests != expectedManifests {
		return false, domain.NewError(domain.CodeDatabaseUnavailable, "legacy migration manifest is incomplete")
	}
	if _, err := s.loadCatalogSetup(ctx, s.db, setupID, true); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) verifyLegacyCatalogManifest(ctx context.Context, sourceKey, setupID string, role domain.ArtifactRole,
	artifact legacyCatalogArtifact,
) error {
	var legacyArtifactID, catalogFileID, targetPath, manifestSHA, outcome string
	var manifestSize int64
	var fileSetupID, fileRole, filePath, fileSHA, fileVersion, legacyObjectID string
	var fileSize, identityDevice, identityInode, identitySize int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(manifest.legacy_artifact_id, ''),
		COALESCE(manifest.catalog_file_id, ''), manifest.target_relative_path, manifest.byte_size,
		manifest.sha256, manifest.outcome, COALESCE(file.setup_id, ''), COALESCE(file.role, ''),
		COALESCE(file.relative_path, ''), COALESCE(file.byte_size, -1), COALESCE(file.sha256, ''),
		COALESCE(file.object_version, ''), COALESCE(file.identity_device, 0), COALESCE(file.identity_inode, 0),
		COALESCE(file.identity_size, -1), COALESCE(file.legacy_storage_object_id, '')
		FROM catalog_legacy_file_manifest manifest
		LEFT JOIN catalog_files file ON file.id = manifest.catalog_file_id
		WHERE manifest.source_key = ? AND manifest.role = ?`, sourceKey, role).Scan(
		&legacyArtifactID, &catalogFileID, &targetPath, &manifestSize, &manifestSHA, &outcome,
		&fileSetupID, &fileRole, &filePath, &fileSize, &fileSHA, &fileVersion,
		&identityDevice, &identityInode, &identitySize, &legacyObjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeDatabaseUnavailable, "legacy migration manifest is missing")
	}
	if err != nil {
		return databaseError(err)
	}
	if outcome != "copied" || legacyArtifactID != artifact.ID || catalogFileID == "" ||
		manifestSize != artifact.ByteSize || manifestSHA != artifact.SHA256 || fileSetupID != setupID ||
		fileRole != string(role) || filePath != targetPath || fileSize != artifact.ByteSize ||
		fileSHA != artifact.SHA256 || legacyObjectID != artifact.ObjectID || identitySize != artifact.ByteSize ||
		identityDevice <= 0 || identityInode <= 0 || fileVersion == "" {
		return domain.NewError(domain.CodeDatabaseUnavailable, "legacy migration manifest does not match its source")
	}
	object, err := s.catalog.Inspect(filePath, fileSHA, fileVersion)
	if err != nil {
		return storageError(err)
	}
	if int64(object.Identity.Device) != identityDevice || int64(object.Identity.Inode) != identityInode ||
		object.Size != identitySize {
		return domain.NewError(domain.CodeInvalidContent, "migrated catalog file identity changed")
	}
	return nil
}

func (s *Service) failLegacyMapping(ctx context.Context, sourceKey string, cause error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE catalog_legacy_migrations SET state = 'manual_review', error_code = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE source_key = ?`, "MIGRATION_REVIEW_REQUIRED", sourceKey)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE catalog_legacy_file_manifest SET outcome = 'manual_review', error_code = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE source_key = ? AND outcome = 'pending'`,
			"MIGRATION_REVIEW_REQUIRED", sourceKey)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		cause = errors.Join(cause, databaseError(err))
	}
	return s.failLegacyCatalogMigration(ctx, "", cause)
}

func (s *Service) failLegacyCatalogMigration(ctx context.Context, legacySetupID string, cause error) error {
	_, _ = s.db.ExecContext(ctx, `UPDATE catalog_state SET legacy_migration_state = 'manual_review',
		updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE library_id = ?`, s.libraryID)
	return domain.WrapError(domain.CodeInvalidContent, "legacy catalog migration requires manual review", cause)
}

func legacySheetName(setupName string, sheet legacyCatalogArtifact) string {
	extension := path.Ext(sheet.DisplayName)
	base := strings.TrimSuffix(sheet.DisplayName, extension)
	candidate := setupName + "-" + base + extension
	if normalized, err := domain.NormalizeArtifactName(candidate); err == nil {
		return normalized
	}
	return "setup-" + sheet.ID[:min(8, len(sheet.ID))] + extension
}
