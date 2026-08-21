package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

type CreateCatalogFolderInput struct {
	ParentFolderID  string
	Name            string
	IdempotencyKey  string
	legacySourceKey string
}

type UpdateCatalogFolderInput struct {
	ExpectedRevision domain.Revision
	Name             *string
	ParentFolderID   *string
	IdempotencyKey   string
}

type CreateCatalogSetupInput struct {
	FolderID        string
	Name            string
	Description     string
	IdempotencyKey  string
	legacySourceKey string
	legacySetupID   string
}

type UpdateCatalogSetupInput struct {
	ExpectedRevision domain.Revision
	Name             *string
	Description      *string
	FolderID         *string
	IdempotencyKey   string
}

type PutCatalogFileInput struct {
	ExpectedRevision    domain.Revision
	ExpectedFileVersion string
	CreateOnly          bool
	DisplayName         string
	Content             io.Reader
	ExpectedSize        int64
	ExpectedSHA256      string
	IdempotencyKey      string
	legacySourceKey     string
	legacyArtifactID    string
	legacyObjectID      string
}

type catalogOperationDetails struct {
	SetupID          string              `json:"setupId,omitempty"`
	FileID           string              `json:"fileId,omitempty"`
	FolderID         string              `json:"folderId,omitempty"`
	ParentFolderID   string              `json:"parentFolderId,omitempty"`
	Role             domain.ArtifactRole `json:"role,omitempty"`
	SourcePath       string              `json:"sourcePath,omitempty"`
	TargetPath       string              `json:"targetPath"`
	TemporaryPath    string              `json:"temporaryPath,omitempty"`
	DisplayName      string              `json:"displayName,omitempty"`
	MediaType        string              `json:"mediaType,omitempty"`
	ExpectedSHA256   string              `json:"expectedSha256,omitempty"`
	ResultSHA256     string              `json:"resultSha256,omitempty"`
	ExpectedVersion  string              `json:"expectedVersion,omitempty"`
	ResultVersion    string              `json:"resultVersion,omitempty"`
	ExpectedRevision domain.Revision     `json:"expectedRevision,omitempty"`
	ByteSize         int64               `json:"byteSize,omitempty"`
	ExpectedDevice   uint64              `json:"expectedDevice,omitempty"`
	ExpectedInode    uint64              `json:"expectedInode,omitempty"`
	ResultDevice     uint64              `json:"resultDevice,omitempty"`
	ResultInode      uint64              `json:"resultInode,omitempty"`
	ExpectedSize     int64               `json:"expectedSize,omitempty"`
	ResultSize       int64               `json:"resultSize,omitempty"`
	IdempotencyKey   string              `json:"idempotencyKey,omitempty"`
	RequestOperation string              `json:"requestOperation,omitempty"`
	RequestHash      string              `json:"requestHash,omitempty"`
}

func (s *Service) catalogAvailable() error {
	if s == nil || s.catalog == nil {
		return domain.NewError(domain.CodeStorageUnavailable, "LinuxCNC program catalog is unavailable")
	}
	return nil
}

func (s *Service) ensureCatalogState(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_state(library_id) VALUES (?)
		ON CONFLICT(library_id) DO NOTHING`, s.libraryID)
	return databaseError(err)
}

func bumpCatalogGeneration(ctx context.Context, tx *sql.Tx, libraryID string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE catalog_state
		   SET generation = generation + 1,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE library_id = ?`, libraryID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return databaseError(errors.New("catalog state is unavailable"))
	}
	return nil
}

func (s *Service) GetCatalogTree(ctx context.Context) (*domain.CatalogTree, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	if err := s.ensureCatalogState(ctx); err != nil {
		return nil, err
	}
	result := &domain.CatalogTree{
		Destination: domain.CatalogDestination{RootLabel: s.catalogRootLabel, RootDisplay: s.catalogRootDisplay},
		Folders:     []domain.CatalogFolder{}, Setups: []domain.CatalogSetup{},
	}
	var generation int64
	if err := s.db.QueryRowContext(ctx, `SELECT generation FROM catalog_state WHERE library_id = ?`, s.libraryID).Scan(&generation); err != nil {
		return nil, databaseError(err)
	}
	result.Generation = strconv.FormatInt(generation, 10)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(parent_id, ''), name, relative_path, revision, created_at, updated_at
		  FROM catalog_folders WHERE library_id = ?
		 ORDER BY path_key, id`, s.libraryID)
	if err != nil {
		return nil, databaseError(err)
	}
	for rows.Next() {
		folder, scanErr := scanCatalogFolder(rows)
		if scanErr != nil {
			rows.Close()
			return nil, databaseError(scanErr)
		}
		result.Folders = append(result.Folders, *folder)
	}
	if err := rows.Close(); err != nil {
		return nil, databaseError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	rows, err = s.db.QueryContext(ctx, `
		SELECT id, COALESCE(folder_id, ''), name, description, revision, created_at, updated_at
		  FROM catalog_setups WHERE library_id = ?
		 ORDER BY COALESCE(folder_id, ''), name_key, id`, s.libraryID)
	if err != nil {
		return nil, databaseError(err)
	}
	for rows.Next() {
		setup, scanErr := scanCatalogSetup(rows)
		if scanErr != nil {
			rows.Close()
			return nil, databaseError(scanErr)
		}
		files, loadErr := s.loadCatalogFiles(ctx, s.db, setup.ID)
		if loadErr != nil {
			rows.Close()
			return nil, loadErr
		}
		attachCatalogFiles(setup, files)
		result.Setups = append(result.Setups, *setup)
	}
	if err := rows.Close(); err != nil {
		return nil, databaseError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}

func (s *Service) GetCatalogSetup(ctx context.Context, setupID string) (*domain.CatalogSetup, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	return s.loadCatalogSetup(ctx, s.db, setupID, true)
}

func (s *Service) loadCatalogSetup(ctx context.Context, q queryer, setupID string, withFiles bool) (*domain.CatalogSetup, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	setup, err := scanCatalogSetup(q.QueryRowContext(ctx, `
		SELECT id, COALESCE(folder_id, ''), name, description, revision, created_at, updated_at
		  FROM catalog_setups WHERE library_id = ? AND id = ?`, s.libraryID, setupID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.CodeSetupNotFound, "setup was not found")
	}
	if err != nil {
		return nil, databaseError(err)
	}
	if withFiles {
		files, err := s.loadCatalogFiles(ctx, q, setupID)
		if err != nil {
			return nil, err
		}
		attachCatalogFiles(setup, files)
	}
	return setup, nil
}

func (s *Service) loadCatalogFolder(ctx context.Context, q queryer, folderID string) (*domain.CatalogFolder, error) {
	if err := domain.ValidateID(folderID); err != nil {
		return nil, err
	}
	folder, err := scanCatalogFolder(q.QueryRowContext(ctx, `
		SELECT id, COALESCE(parent_id, ''), name, relative_path, revision, created_at, updated_at
		  FROM catalog_folders WHERE library_id = ? AND id = ?`, s.libraryID, folderID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.CodeNameConflict, "catalog folder was not found")
	}
	if err != nil {
		return nil, databaseError(err)
	}
	return folder, nil
}

func (s *Service) loadCatalogFiles(ctx context.Context, q queryer, setupID string) ([]domain.CatalogFile, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, role, display_name, relative_path, media_type, byte_size, sha256,
		       object_version, identity_device, identity_inode, created_at, updated_at
		  FROM catalog_files WHERE setup_id = ? ORDER BY role`, setupID)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	files := []domain.CatalogFile{}
	for rows.Next() {
		var file domain.CatalogFile
		var role, created, updated string
		if err := rows.Scan(&file.ID, &role, &file.DisplayName, &file.RelativePath, &file.MediaType,
			&file.ByteSize, &file.SHA256, &file.Version, &file.IdentityDevice, &file.IdentityInode,
			&created, &updated); err != nil {
			return nil, databaseError(err)
		}
		file.Role = domain.ArtifactRole(role)
		var err error
		if file.CreatedAt, err = parseTimestamp(created); err != nil {
			return nil, databaseError(err)
		}
		if file.UpdatedAt, err = parseTimestamp(updated); err != nil {
			return nil, databaseError(err)
		}
		files = append(files, file)
	}
	return files, databaseError(rows.Err())
}

func attachCatalogFiles(setup *domain.CatalogSetup, files []domain.CatalogFile) {
	for index := range files {
		file := files[index]
		if file.Role == domain.ArtifactRoleProgram {
			setup.Program = &file
			setup.ProgramRelativePath = file.RelativePath
		} else if file.Role == domain.ArtifactRoleSetupSheet {
			setup.SetupSheet = &file
			setup.SetupSheetRelativePath = file.RelativePath
		}
	}
}

func scanCatalogFolder(row scanner) (*domain.CatalogFolder, error) {
	var folder domain.CatalogFolder
	var created, updated string
	if err := row.Scan(&folder.ID, &folder.ParentFolderID, &folder.Name, &folder.RelativePath,
		&folder.Revision, &created, &updated); err != nil {
		return nil, err
	}
	var err error
	if folder.CreatedAt, err = parseTimestamp(created); err != nil {
		return nil, err
	}
	if folder.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return nil, err
	}
	return &folder, nil
}

func scanCatalogSetup(row scanner) (*domain.CatalogSetup, error) {
	var setup domain.CatalogSetup
	var created, updated string
	if err := row.Scan(&setup.ID, &setup.FolderID, &setup.Name, &setup.Description,
		&setup.Revision, &created, &updated); err != nil {
		return nil, err
	}
	var err error
	if setup.CreatedAt, err = parseTimestamp(created); err != nil {
		return nil, err
	}
	if setup.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return nil, err
	}
	return &setup, nil
}

func (s *Service) catalogFolderPath(ctx context.Context, q queryer, folderID string) (string, error) {
	if folderID == "" {
		return "", nil
	}
	folder, err := s.loadCatalogFolder(ctx, q, folderID)
	if err != nil {
		return "", err
	}
	return folder.RelativePath, nil
}

func catalogRelative(folderPath, basename string) string {
	if folderPath == "" {
		return basename
	}
	return folderPath + "/" + basename
}

func catalogPathKey(relative string) (string, error) {
	components := strings.Split(relative, "/")
	keys := make([]string, len(components))
	for index, component := range components {
		key, err := domain.ArtifactNameKey(component)
		if err != nil {
			return "", err
		}
		keys[index] = key
	}
	return strings.Join(keys, "/"), nil
}

func catalogNameConflict(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return domain.NewError(domain.CodeNameConflict, "an item with this name already exists in the folder")
	}
	return databaseError(err)
}

func (s *Service) finishCatalogClaim(claim idempotencyClaim, status int, result any, operationErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.finishIdempotency(ctx, claim, status, result, operationErr)
}

func (s *Service) CreateCatalogFolder(ctx context.Context, input CreateCatalogFolderInput) (*domain.CatalogFolder, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	name, err := domain.NormalizeArtifactName(input.Name)
	if err != nil {
		return nil, err
	}
	nameKey, err := domain.ArtifactNameKey(name)
	if err != nil {
		return nil, err
	}
	if input.ParentFolderID == "" && strings.EqualFold(name, "ngcgui_lib") {
		return nil, domain.NewError(domain.CodeNameConflict, "ngcgui_lib is reserved by LinuxCNC")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := s.ensureCatalogState(ctx); err != nil {
		return nil, err
	}
	requestHash, err := idempotencyRequestHash("catalogCreateFolder", struct {
		ParentFolderID  string `json:"parentFolderId"`
		Name            string `json:"name"`
		LegacySourceKey string `json:"legacySourceKey,omitempty"`
	}{input.ParentFolderID, name, input.legacySourceKey})
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, "catalogCreateFolder", requestHash)
	if err != nil {
		return nil, err
	}
	var replay domain.CatalogFolder
	if ok, replayErr := claim.replayInto(&replay); ok || replayErr != nil {
		if replayErr != nil {
			return nil, replayErr
		}
		return &replay, nil
	}
	var result *domain.CatalogFolder
	dbCommitted := false
	operationID := ""
	err = s.withSetupLock("catalog-tree", func() error {
		parentPath, err := s.catalogFolderPath(ctx, s.db, input.ParentFolderID)
		if err != nil {
			return err
		}
		relative := catalogRelative(parentPath, name)
		pathKey, err := catalogPathKey(relative)
		if err != nil {
			return err
		}
		folderID, err := domain.NewFolderID()
		if err != nil {
			return err
		}
		operationID, err = domain.NewOperationID()
		if err != nil {
			return err
		}
		temporary := catalogFolderCreateTemp(relative, operationID)
		details := catalogOperationDetails{FolderID: folderID, ParentFolderID: input.ParentFolderID,
			DisplayName: name, TargetPath: relative, TemporaryPath: temporary,
			IdempotencyKey: input.IdempotencyKey, RequestOperation: "catalogCreateFolder", RequestHash: requestHash}
		if err := s.insertCatalogOperation(ctx, operationID, "folder_create", details); err != nil {
			return err
		}
		createdObject, err := s.catalog.CreateFolderPrepared(relative, operationID, func(prepared *storage.Object) error {
			details.ResultDevice, details.ResultInode = prepared.Identity.Device, prepared.Identity.Inode
			details.ResultSize = prepared.Size
			payload, marshalErr := json.Marshal(details)
			if marshalErr != nil {
				return marshalErr
			}
			result, updateErr := s.db.ExecContext(ctx, `UPDATE catalog_operations SET details_json = ?,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'intent'`,
				string(payload), operationID)
			if updateErr != nil {
				return databaseError(updateErr)
			}
			changed, rowsErr := result.RowsAffected()
			if rowsErr != nil || changed != 1 {
				return databaseError(errors.New("catalog folder journal changed"))
			}
			return nil
		})
		if err != nil {
			return storageError(err)
		}
		details.ResultDevice, details.ResultInode, details.ByteSize = createdObject.Identity.Device,
			createdObject.Identity.Inode, createdObject.Size
		details.ResultSize = createdObject.Size
		details.ResultVersion = createdObject.Version
		payload, err := json.Marshal(details)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE catalog_operations SET details_json = ? WHERE id = ? AND state = 'intent'`, string(payload), operationID); err != nil {
			return databaseError(err)
		}
		removeOnFailure := true
		defer func() {
			if removeOnFailure {
				if _, recoveryErr := s.catalog.RecoverFolderCreate(relative, temporary, createdObject.Version,
					createdObject.Identity.Device, createdObject.Identity.Inode, createdObject.Size, false); recoveryErr == nil {
					_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
				}
			}
		}()
		if _, err := s.db.ExecContext(ctx, `UPDATE catalog_operations SET state = 'storage_applied', result_version = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'intent'`, createdObject.Version, operationID); err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		now := sqlTimestamp(s.now())
		_, err = tx.ExecContext(ctx, `
			INSERT INTO catalog_folders(id, library_id, parent_id, name, name_key, relative_path, path_key,
			                            legacy_source_key, revision, created_at, updated_at)
			VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), 1, ?, ?)`, folderID, s.libraryID,
			input.ParentFolderID, name, nameKey, relative, pathKey, input.legacySourceKey, now, now)
		if err != nil {
			return catalogNameConflict(err)
		}
		if err := bumpCatalogGeneration(ctx, tx, s.libraryID); err != nil {
			return err
		}
		if err := s.appendCatalogAudit(ctx, tx, "catalogCreateFolder", "", "", 0, 1,
			map[string]any{"folderId": folderID}); err != nil {
			return err
		}
		result, err = s.loadCatalogFolder(ctx, tx, folderID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, operationID); err != nil {
			return databaseError(err)
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 201, result, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		removeOnFailure = false
		_ = s.finishCatalogOperationRow(context.Background(), operationID, "completed")
		return nil
	})
	if err != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, err); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, err
}

func (s *Service) CreateCatalogSetup(ctx context.Context, input CreateCatalogSetupInput) (*domain.CatalogSetup, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	name, err := domain.NormalizeSetupName(input.Name)
	if err != nil {
		return nil, err
	}
	nameKey, err := domain.SetupNameKey(name)
	if err != nil {
		return nil, err
	}
	if err := validateDescription(input.Description); err != nil {
		return nil, err
	}
	if (input.legacySourceKey == "") != (input.legacySetupID == "") {
		return nil, domain.NewError(domain.CodeInvalidContent, "legacy setup provenance is incomplete")
	}
	if input.legacySetupID != "" {
		if err := domain.ValidateID(input.legacySetupID); err != nil {
			return nil, domain.NewError(domain.CodeInvalidContent, "legacy setup provenance is invalid")
		}
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := s.ensureCatalogState(ctx); err != nil {
		return nil, err
	}
	requestHash, err := idempotencyRequestHash("catalogCreateSetup", struct {
		FolderID        string `json:"folderId"`
		Name            string `json:"name"`
		Description     string `json:"description"`
		LegacySourceKey string `json:"legacySourceKey,omitempty"`
		LegacySetupID   string `json:"legacySetupId,omitempty"`
	}{input.FolderID, name, input.Description, input.legacySourceKey, input.legacySetupID})
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, "catalogCreateSetup", requestHash)
	if err != nil {
		return nil, err
	}
	var replay domain.CatalogSetup
	if ok, replayErr := claim.replayInto(&replay); ok || replayErr != nil {
		if replayErr != nil {
			return nil, replayErr
		}
		return &replay, nil
	}
	var result *domain.CatalogSetup
	dbCommitted := false
	err = s.withSetupLock("catalog-tree", func() error {
		if _, err := s.catalogFolderPath(ctx, s.db, input.FolderID); err != nil {
			return err
		}
		setupID, err := domain.NewSetupID()
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		now := sqlTimestamp(s.now())
		_, err = tx.ExecContext(ctx, `
			INSERT INTO catalog_setups(id, library_id, folder_id, name, name_key, description, legacy_setup_id,
			                           legacy_source_key, revision, created_at, updated_at)
			VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 1, ?, ?)`,
			setupID, s.libraryID, input.FolderID, name, nameKey, input.Description,
			input.legacySetupID, input.legacySourceKey, now, now)
		if err != nil {
			return catalogNameConflict(err)
		}
		if input.legacySourceKey != "" {
			linked, linkErr := tx.ExecContext(ctx, `UPDATE catalog_legacy_migrations
				SET catalog_setup_id = ?, state = 'publishing', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				WHERE source_key = ? AND library_id = ? AND legacy_setup_id = ?
				  AND COALESCE(target_folder_id, '') = ? AND target_name = ?
				  AND catalog_setup_id IS NULL AND state IN ('pending','publishing')`,
				setupID, input.legacySourceKey, s.libraryID, input.legacySetupID, input.FolderID, name)
			if linkErr != nil {
				return databaseError(linkErr)
			}
			changed, rowsErr := linked.RowsAffected()
			if rowsErr != nil || changed != 1 {
				return databaseError(errors.New("legacy setup mapping changed"))
			}
		}
		if err := bumpCatalogGeneration(ctx, tx, s.libraryID); err != nil {
			return err
		}
		if err := s.appendCatalogAudit(ctx, tx, "catalogCreateSetup", setupID, "", 0, 1, nil); err != nil {
			return err
		}
		result, err = s.loadCatalogSetup(ctx, tx, setupID, true)
		if err != nil {
			return err
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 201, result, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		return nil
	})
	if err != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, err); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, err
}

func (s *Service) UpdateCatalogFolder(ctx context.Context, folderID string, input UpdateCatalogFolderInput) (*domain.CatalogFolder, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() || input.Name == nil && input.ParentFolderID == nil {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision and a folder change are required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := domain.ValidateID(folderID); err != nil {
		return nil, err
	}
	if input.Name != nil {
		normalized, err := domain.NormalizeArtifactName(*input.Name)
		if err != nil {
			return nil, err
		}
		input.Name = &normalized
	}
	if input.ParentFolderID != nil && *input.ParentFolderID != "" {
		if err := domain.ValidateID(*input.ParentFolderID); err != nil {
			return nil, err
		}
	}
	operationName := "catalogUpdateFolder:" + folderID
	requestHash, err := idempotencyRequestHash(operationName, input)
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, operationName, requestHash)
	if err != nil {
		return nil, err
	}
	var replay domain.CatalogFolder
	if ok, replayErr := claim.replayInto(&replay); ok || replayErr != nil {
		if replayErr != nil {
			return nil, replayErr
		}
		return &replay, nil
	}
	var result *domain.CatalogFolder
	dbCommitted := false
	err = s.withSetupLock("catalog-tree", func() error {
		current, err := s.loadCatalogFolder(ctx, s.db, folderID)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(current.Revision, input.ExpectedRevision); err != nil {
			return err
		}
		name := current.Name
		if input.Name != nil {
			name, err = domain.NormalizeArtifactName(*input.Name)
			if err != nil {
				return err
			}
		}
		parentID := current.ParentFolderID
		if input.ParentFolderID != nil {
			parentID = *input.ParentFolderID
		}
		if parentID == folderID {
			return domain.NewError(domain.CodeNameConflict, "a folder cannot contain itself")
		}
		parentPath, err := s.catalogFolderPath(ctx, s.db, parentID)
		if err != nil {
			return err
		}
		if parentPath == current.RelativePath || strings.HasPrefix(parentPath, current.RelativePath+"/") {
			return domain.NewError(domain.CodeNameConflict, "a folder cannot move into its descendant")
		}
		newRelative := catalogRelative(parentPath, name)
		nameKey, err := domain.ArtifactNameKey(name)
		if err != nil {
			return err
		}
		newPathKey, err := catalogPathKey(newRelative)
		if err != nil {
			return err
		}
		moved := newRelative != current.RelativePath
		operationID := ""
		var movedObject *storage.Object
		if moved {
			expectedObject, err := s.catalog.InspectFolder(current.RelativePath, "")
			if err != nil {
				return storageError(err)
			}
			operationID, err = domain.NewOperationID()
			if err != nil {
				return err
			}
			details := catalogOperationDetails{FolderID: folderID, SourcePath: current.RelativePath,
				TargetPath: newRelative, ExpectedVersion: expectedObject.Version, ExpectedRevision: input.ExpectedRevision,
				ExpectedDevice: expectedObject.Identity.Device, ExpectedInode: expectedObject.Identity.Inode,
				ExpectedSize: expectedObject.Size, ResultSize: expectedObject.Size, ByteSize: expectedObject.Size,
				IdempotencyKey: input.IdempotencyKey, RequestOperation: operationName, RequestHash: requestHash}
			if err := s.insertCatalogOperation(ctx, operationID, "move", details); err != nil {
				return err
			}
			movedObject, err = s.catalog.MoveExpected(ctx, current.RelativePath, newRelative, "", expectedObject.Version)
			if err != nil {
				if movedObject == nil {
					_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
				}
				return storageError(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE catalog_operations SET state = 'storage_applied', result_version = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'intent'`, movedObject.Version, operationID); err != nil {
				return databaseError(err)
			}
		}
		rollbackMove := moved
		defer func() {
			if rollbackMove {
				if _, rollbackErr := s.catalog.MoveExpected(context.Background(), newRelative, current.RelativePath, "", movedObject.Version); rollbackErr == nil {
					_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
				}
			}
		}()
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		now := sqlTimestamp(s.now())
		oldPrefix := current.RelativePath + "/"
		newPrefix := newRelative + "/"
		if moved {
			if _, err := tx.ExecContext(ctx, `
				UPDATE catalog_setups SET revision = revision + 1, updated_at = ?
				 WHERE library_id = ? AND (folder_id = ? OR folder_id IN (
				       SELECT id FROM catalog_folders WHERE library_id = ?
				        AND substr(relative_path, 1, length(?)) = ?
				 ))`, now, s.libraryID, folderID, s.libraryID, oldPrefix, oldPrefix); err != nil {
				return databaseError(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE catalog_folders
				   SET relative_path = ? || substr(relative_path, length(?) + 1),
				       path_key = wsm_casefold(? || substr(relative_path, length(?) + 1)),
				       revision = revision + 1, updated_at = ?
				 WHERE library_id = ? AND substr(relative_path, 1, length(?)) = ?`, newPrefix, oldPrefix,
				newPrefix, oldPrefix, now, s.libraryID, oldPrefix, oldPrefix); err != nil {
				return catalogNameConflict(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE catalog_files
				   SET relative_path = ? || substr(relative_path, length(?) + 1),
				       path_key = wsm_casefold(? || substr(relative_path, length(?) + 1)), updated_at = ?
				 WHERE library_id = ? AND substr(relative_path, 1, length(?)) = ?`, newPrefix, oldPrefix,
				newPrefix, oldPrefix, now, s.libraryID, oldPrefix, oldPrefix); err != nil {
				return catalogNameConflict(err)
			}
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE catalog_folders
			   SET parent_id = NULLIF(?, ''), name = ?, name_key = ?, relative_path = ?, path_key = ?,
			       revision = revision + 1, updated_at = ?
			 WHERE library_id = ? AND id = ? AND revision = ?`, parentID, name, nameKey,
			newRelative, newPathKey, now, s.libraryID, folderID, input.ExpectedRevision)
		if err != nil {
			return catalogNameConflict(err)
		}
		changed, err := updated.RowsAffected()
		if err != nil || changed != 1 {
			return domain.NewError(domain.CodeRevisionConflict, "folder revision has changed")
		}
		if err := bumpCatalogGeneration(ctx, tx, s.libraryID); err != nil {
			return err
		}
		if err := s.appendCatalogAudit(ctx, tx, "catalogUpdateFolder", "", "", current.Revision,
			current.Revision+1, map[string]any{"folderId": folderID, "moved": moved}); err != nil {
			return err
		}
		result, err = s.loadCatalogFolder(ctx, tx, folderID)
		if err != nil {
			return err
		}
		if moved {
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, operationID); err != nil {
				return databaseError(err)
			}
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		rollbackMove = false
		if moved {
			_ = s.finishCatalogOperationRow(context.Background(), operationID, "completed")
		}
		return nil
	})
	if err != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, err); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, err
}

func (s *Service) DeleteCatalogFolder(ctx context.Context, folderID string, expected domain.Revision, idempotencyKey string) error {
	if err := s.catalogAvailable(); err != nil {
		return err
	}
	if !expected.Valid() {
		return domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return err
	}
	if err := domain.ValidateID(folderID); err != nil {
		return err
	}
	operationName := "catalogDeleteFolder:" + folderID
	requestHash, err := idempotencyRequestHash(operationName, struct {
		Expected domain.Revision `json:"expectedRevision"`
	}{expected})
	if err != nil {
		return err
	}
	claim, err := s.claimIdempotency(ctx, idempotencyKey, operationName, requestHash)
	if err != nil {
		return err
	}
	if ok, replayErr := claim.replayInto(nil); ok || replayErr != nil {
		return replayErr
	}
	dbCommitted := false
	operationErr := s.withSetupLock("catalog-tree", func() error {
		folder, err := s.loadCatalogFolder(ctx, s.db, folderID)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(folder.Revision, expected); err != nil {
			return err
		}
		var children int
		if err := s.db.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM catalog_folders WHERE parent_id = ?) +
			       (SELECT count(*) FROM catalog_setups WHERE folder_id = ?)`, folderID, folderID).Scan(&children); err != nil {
			return databaseError(err)
		}
		if children != 0 {
			return domain.NewError(domain.CodeInvalidContent, "folder is not empty")
		}
		expectedObject, err := s.catalog.InspectFolder(folder.RelativePath, "")
		if err != nil {
			return storageError(err)
		}
		operationID, err := domain.NewOperationID()
		if err != nil {
			return err
		}
		temporary := ".wsm-trash-" + operationID + "-dir"
		details := catalogOperationDetails{FolderID: folderID, TargetPath: folder.RelativePath,
			TemporaryPath: temporary, ExpectedVersion: expectedObject.Version, ExpectedRevision: expected,
			ExpectedDevice: expectedObject.Identity.Device, ExpectedInode: expectedObject.Identity.Inode,
			ExpectedSize: expectedObject.Size, ByteSize: expectedObject.Size,
			IdempotencyKey: idempotencyKey, RequestOperation: operationName, RequestHash: requestHash}
		if err := s.insertCatalogOperation(ctx, operationID, "folder_delete", details); err != nil {
			return err
		}
		quarantine, err := s.catalog.QuarantineEmptyFolder(folder.RelativePath, expectedObject.Version, operationID)
		if err != nil {
			if quarantine != nil && quarantine.Restore() == nil {
				_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
			}
			return storageError(err)
		}
		rollbackStorage := true
		defer func() {
			if rollbackStorage && quarantine.Restore() == nil {
				_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
			}
		}()
		if err := s.finishCatalogOperationRow(ctx, operationID, "storage_applied"); err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		deleted, err := tx.ExecContext(ctx, `DELETE FROM catalog_folders WHERE library_id = ? AND id = ? AND revision = ?`, s.libraryID, folderID, expected)
		if err == nil {
			var changed int64
			changed, err = deleted.RowsAffected()
			if changed != 1 && err == nil {
				err = domain.NewError(domain.CodeRevisionConflict, "folder revision has changed")
			}
		}
		if err == nil {
			err = bumpCatalogGeneration(ctx, tx, s.libraryID)
		}
		if err == nil {
			err = s.appendCatalogAudit(ctx, tx, "catalogDeleteFolder", "", "", expected, 0,
				map[string]any{"folderId": folderID})
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, operationID)
		}
		if err == nil {
			err = finishIdempotencyTx(ctx, tx, claim, 204, nil, nil)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		rollbackStorage = false
		if quarantine.Discard() == nil {
			_ = s.finishCatalogOperationRow(context.Background(), operationID, "completed")
		}
		return nil
	})
	if operationErr != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, operationErr); finishErr != nil {
			return finishErr
		}
	}
	return operationErr
}

func (s *Service) UpdateCatalogSetup(ctx context.Context, setupID string, input UpdateCatalogSetupInput) (*domain.CatalogSetup, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() || input.Name == nil && input.Description == nil && input.FolderID == nil {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision and a setup change are required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if input.Name != nil {
		normalized, err := domain.NormalizeSetupName(*input.Name)
		if err != nil {
			return nil, err
		}
		input.Name = &normalized
	}
	if input.Description != nil {
		if err := validateDescription(*input.Description); err != nil {
			return nil, err
		}
	}
	if input.FolderID != nil && *input.FolderID != "" {
		if err := domain.ValidateID(*input.FolderID); err != nil {
			return nil, err
		}
	}
	operationName := "catalogUpdateSetup:" + setupID
	requestHash, err := idempotencyRequestHash(operationName, input)
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, operationName, requestHash)
	if err != nil {
		return nil, err
	}
	var replay domain.CatalogSetup
	if ok, replayErr := claim.replayInto(&replay); ok || replayErr != nil {
		if replayErr != nil {
			return nil, replayErr
		}
		return &replay, nil
	}
	var result *domain.CatalogSetup
	dbCommitted := false
	err = s.withSetupLock("catalog-tree", func() error {
		current, err := s.loadCatalogSetup(ctx, s.db, setupID, true)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(current.Revision, input.ExpectedRevision); err != nil {
			return err
		}
		name, description, folderID := current.Name, current.Description, current.FolderID
		if input.Name != nil {
			name, err = domain.NormalizeSetupName(*input.Name)
			if err != nil {
				return err
			}
		}
		if input.Description != nil {
			description = *input.Description
			if err := validateDescription(description); err != nil {
				return err
			}
		}
		if input.FolderID != nil {
			folderID = *input.FolderID
		}
		folderPath, err := s.catalogFolderPath(ctx, s.db, folderID)
		if err != nil {
			return err
		}
		nameKey, err := domain.SetupNameKey(name)
		if err != nil {
			return err
		}
		type move struct {
			oldPath, newPath, sha, version, fileID, operationID string
			role                                                domain.ArtifactRole
			device, inode                                       uint64
			size                                                int64
			object                                              *storage.Object
		}
		moves := []move{}
		if folderID != current.FolderID {
			for _, file := range []*domain.CatalogFile{current.Program, current.SetupSheet} {
				if file != nil {
					moves = append(moves, move{oldPath: file.RelativePath, newPath: catalogRelative(folderPath, file.DisplayName),
						sha: file.SHA256, version: file.Version, fileID: file.ID, role: file.Role,
						device: file.IdentityDevice, inode: file.IdentityInode, size: file.ByteSize})
				}
			}
		}
		moved := 0
		rollbackMoves := true
		defer func() {
			if rollbackMoves {
				for index := moved - 1; index >= 0; index-- {
					rolled, rollbackErr := s.catalog.MoveExpected(context.Background(), moves[index].newPath, moves[index].oldPath,
						moves[index].sha, moves[index].object.Version)
					if rollbackErr == nil && rolled != nil {
						s.refreshCatalogFileIdentity(context.Background(), setupID, moves[index].oldPath, rolled)
						_ = s.finishCatalogOperationRow(context.Background(), moves[index].operationID, "failed")
					}
				}
			}
		}()
		for index := range moves {
			item := &moves[index]
			item.operationID, err = domain.NewOperationID()
			if err != nil {
				return err
			}
			details := catalogOperationDetails{SetupID: setupID, FileID: item.fileID, Role: item.role,
				SourcePath: item.oldPath, TargetPath: item.newPath, ExpectedSHA256: item.sha,
				ResultSHA256: item.sha, ExpectedVersion: item.version, ExpectedRevision: input.ExpectedRevision,
				IdempotencyKey: input.IdempotencyKey, RequestOperation: operationName, RequestHash: requestHash}
			details.ExpectedDevice, details.ExpectedInode, details.ByteSize = item.device, item.inode, item.size
			details.ExpectedSize, details.ResultSize = item.size, item.size
			if err := s.insertCatalogOperation(ctx, item.operationID, "move", details); err != nil {
				return err
			}
			object, moveErr := s.catalog.MoveExpected(ctx, item.oldPath, item.newPath, item.sha, item.version)
			if moveErr != nil {
				if object == nil {
					_ = s.finishCatalogOperationRow(context.Background(), item.operationID, "failed")
				}
				return storageError(moveErr)
			}
			moves[moved].object = object
			moved++
			if _, err := s.db.ExecContext(ctx, `UPDATE catalog_operations SET state = 'storage_applied', result_version = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'intent'`, object.Version, item.operationID); err != nil {
				return databaseError(err)
			}
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		now := sqlTimestamp(s.now())
		for _, item := range moves {
			key, keyErr := catalogPathKey(item.newPath)
			if keyErr != nil {
				return keyErr
			}
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_files SET relative_path = ?, path_key = ?, object_version = ?,
				identity_device = ?, identity_inode = ?, identity_size = ?, identity_mtime_ns = ?, identity_ctime_ns = ?, updated_at = ?
				WHERE setup_id = ? AND relative_path = ?`, item.newPath, key, item.object.Version,
				int64(item.object.Identity.Device), int64(item.object.Identity.Inode), item.object.Size,
				item.object.Identity.ModTimeNS, item.object.Identity.ChangeTimeNS, now, setupID, item.oldPath); err != nil {
				return catalogNameConflict(err)
			}
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE catalog_setups SET folder_id = NULLIF(?, ''), name = ?, name_key = ?, description = ?,
			       revision = revision + 1, updated_at = ?
			 WHERE library_id = ? AND id = ? AND revision = ?`, folderID, name, nameKey, description,
			now, s.libraryID, setupID, input.ExpectedRevision)
		if err != nil {
			return catalogNameConflict(err)
		}
		changed, err := updated.RowsAffected()
		if err != nil || changed != 1 {
			return domain.NewError(domain.CodeRevisionConflict, "setup revision has changed")
		}
		if err := bumpCatalogGeneration(ctx, tx, s.libraryID); err != nil {
			return err
		}
		if err := s.appendCatalogAudit(ctx, tx, "catalogUpdateSetup", setupID, "", current.Revision,
			current.Revision+1, map[string]any{"moved": len(moves) > 0}); err != nil {
			return err
		}
		for _, item := range moves {
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, item.operationID); err != nil {
				return databaseError(err)
			}
		}
		result, err = s.loadCatalogSetup(ctx, tx, setupID, true)
		if err != nil {
			return err
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		rollbackMoves = false
		for _, item := range moves {
			_ = s.finishCatalogOperationRow(context.Background(), item.operationID, "completed")
		}
		return nil
	})
	if err != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, err); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, err
}

func catalogFileForRole(setup *domain.CatalogSetup, role domain.ArtifactRole) *domain.CatalogFile {
	if role == domain.ArtifactRoleProgram {
		return setup.Program
	}
	return setup.SetupSheet
}

func (s *Service) catalogFileMediaType(role domain.ArtifactRole, name string) (string, error) {
	if role == domain.ArtifactRoleProgram {
		if _, ok := s.catalogProgramExtensions[strings.ToLower(path.Ext(name))]; !ok {
			return "", domain.NewError(domain.CodeUnsupportedFileType, "program extension is not enabled by the active LinuxCNC policy")
		}
		return mediaTypeGCode, nil
	}
	if role != domain.ArtifactRoleSetupSheet {
		return "", domain.NewError(domain.CodeInvalidContent, "catalog file role is invalid")
	}
	return setupSheetMediaType(name)
}

func catalogOperationTemp(target, operationID string) string {
	directory := path.Dir(target)
	name := ".wsm-upload-" + operationID
	if directory == "." {
		return name
	}
	return directory + "/" + name
}

func catalogFolderCreateTemp(target, operationID string) string {
	directory := path.Dir(target)
	name := ".wsm-create-" + operationID + "-dir"
	if directory == "." {
		return name
	}
	return directory + "/" + name
}

func (s *Service) insertCatalogOperation(ctx context.Context, operationID, operation string, details catalogOperationDetails) error {
	payload, err := json.Marshal(details)
	if err != nil {
		return domain.WrapError(domain.CodeInvalidContent, "catalog operation cannot be encoded", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO catalog_operations(id, library_id, setup_id, file_id, operation, target_path,
		                               temporary_path, expected_version, expected_revision,
		                               idempotency_key, request_hash, state, details_json)
		VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''),
		        NULLIF(?, 0), NULLIF(?, ''), NULLIF(?, ''), 'intent', ?)`,
		operationID, s.libraryID, details.SetupID, details.FileID, operation, details.TargetPath,
		details.TemporaryPath, details.ExpectedVersion, details.ExpectedRevision,
		details.IdempotencyKey, details.RequestHash, string(payload))
	return databaseError(err)
}

func (s *Service) finishCatalogOperationRow(ctx context.Context, operationID, state string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE catalog_operations SET state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       completed_at = CASE WHEN ? IN ('completed','failed') THEN strftime('%Y-%m-%dT%H:%M:%fZ', 'now') ELSE completed_at END
		 WHERE id = ? AND library_id = ?`, state, state, operationID, s.libraryID)
	return databaseError(err)
}

func (s *Service) appendCatalogAudit(ctx context.Context, tx *sql.Tx, operation string, setupID, fileID string,
	before, after domain.Revision, details any,
) error {
	return s.appendAudit(ctx, tx, domain.AuditOperation(operation), setupID, fileID, "", before, after,
		domain.AuditResultSucceeded, "", details)
}

func (s *Service) PutCatalogFile(ctx context.Context, setupID string, role domain.ArtifactRole, input PutCatalogFileInput) (*domain.CatalogSetup, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() || input.Content == nil {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision and file content are required")
	}
	name, err := domain.NormalizeArtifactName(input.DisplayName)
	if err != nil {
		return nil, err
	}
	mediaType, err := s.catalogFileMediaType(role, name)
	if err != nil {
		return nil, err
	}
	if input.CreateOnly && input.ExpectedFileVersion != "" {
		return nil, domain.NewError(domain.CodePreconditionRequired, "create and replacement preconditions are mutually exclusive")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	legacyProvenanceCount := 0
	for _, value := range []string{input.legacySourceKey, input.legacyArtifactID, input.legacyObjectID} {
		if value != "" {
			legacyProvenanceCount++
		}
	}
	if legacyProvenanceCount != 0 && legacyProvenanceCount != 3 {
		return nil, domain.NewError(domain.CodeInvalidContent, "legacy file provenance is incomplete")
	}
	if legacyProvenanceCount != 0 {
		if !input.CreateOnly || input.ExpectedFileVersion != "" {
			return nil, domain.NewError(domain.CodeInvalidContent, "legacy migration can only create a catalog file")
		}
		if err := domain.ValidateID(input.legacyArtifactID); err != nil {
			return nil, domain.NewError(domain.CodeInvalidContent, "legacy artifact provenance is invalid")
		}
		if err := domain.ValidateID(input.legacyObjectID); err != nil {
			return nil, domain.NewError(domain.CodeInvalidContent, "legacy object provenance is invalid")
		}
	}
	releaseHeavy, err := s.acquireHeavy(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer releaseHeavy()
	staged, err := s.objects.Stage(ctx, input.Content, input.ExpectedSize)
	if err != nil {
		return nil, storageError(err)
	}
	discardStaged := true
	defer func() {
		if discardStaged {
			_ = s.objects.Discard(staged)
		}
	}()
	if input.ExpectedSHA256 != "" && input.ExpectedSHA256 != staged.SHA256 {
		return nil, domain.NewError(domain.CodeArtifactChanged, "source content digest changed before publication")
	}
	stagedFile, err := s.objects.OpenStaged(staged)
	if err != nil {
		return nil, storageError(err)
	}
	if role == domain.ArtifactRoleSetupSheet {
		err = validateSetupSheetContent(ctx, mediaType, stagedFile)
	} else {
		_, err = s.gcode.Validate(ctx, role, name, stagedFile)
	}
	closeErr := stagedFile.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, storageError(closeErr)
	}
	operationName := "catalogPutProgram"
	if role == domain.ArtifactRoleSetupSheet {
		operationName = "catalogPutSetupSheet"
	}
	requestHash, err := idempotencyRequestHash(operationName, struct {
		SetupID             string          `json:"setupId"`
		ExpectedRevision    domain.Revision `json:"expectedRevision"`
		ExpectedFileVersion string          `json:"expectedFileVersion,omitempty"`
		CreateOnly          bool            `json:"createOnly"`
		DisplayName         string          `json:"displayName"`
		Size                int64           `json:"size"`
		SHA256              string          `json:"sha256"`
		ExpectedSHA256      string          `json:"expectedSha256,omitempty"`
		LegacySourceKey     string          `json:"legacySourceKey,omitempty"`
		LegacyArtifactID    string          `json:"legacyArtifactId,omitempty"`
		LegacyObjectID      string          `json:"legacyObjectId,omitempty"`
	}{setupID, input.ExpectedRevision, input.ExpectedFileVersion, input.CreateOnly, name, staged.Size, staged.SHA256,
		input.ExpectedSHA256, input.legacySourceKey, input.legacyArtifactID, input.legacyObjectID})
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, input.IdempotencyKey, operationName, requestHash)
	if err != nil {
		return nil, err
	}
	var replay domain.CatalogSetup
	if ok, replayErr := claim.replayInto(&replay); ok || replayErr != nil {
		if replayErr != nil {
			return nil, replayErr
		}
		return &replay, nil
	}
	var result *domain.CatalogSetup
	dbCommitted := false
	operationErr := s.withSetupLock("catalog-tree", func() error {
		setup, err := s.loadCatalogSetup(ctx, s.db, setupID, true)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(setup.Revision, input.ExpectedRevision); err != nil {
			return err
		}
		existing := catalogFileForRole(setup, role)
		if existing == nil {
			if !input.CreateOnly || input.ExpectedFileVersion != "" {
				return domain.NewError(domain.CodePreconditionRequired, "file creation requires If-None-Match")
			}
		} else {
			if input.CreateOnly || input.ExpectedFileVersion == "" {
				return domain.NewError(domain.CodePreconditionRequired, "file replacement requires If-Match")
			}
			if input.ExpectedFileVersion != existing.Version &&
				!s.allowRecoveredCatalogFilePrecondition(ctx, existing, input.ExpectedFileVersion,
					input.IdempotencyKey, operationName, requestHash) {
				return domain.NewError(domain.CodeArtifactChanged, "catalog file version no longer matches")
			}
		}
		folderPath, err := s.catalogFolderPath(ctx, s.db, setup.FolderID)
		if err != nil {
			return err
		}
		target := catalogRelative(folderPath, name)
		pathKey, err := catalogPathKey(target)
		if err != nil {
			return err
		}
		fileID := ""
		expectedSHA, expectedVersion := "", ""
		if existing != nil {
			fileID, expectedSHA, expectedVersion = existing.ID, existing.SHA256, existing.Version
		} else if fileID, err = domain.NewArtifactID(); err != nil {
			return err
		}
		renamedReplacement := existing != nil && existing.RelativePath != target
		publishExpectedSHA, publishExpectedVersion := expectedSHA, expectedVersion
		if renamedReplacement {
			publishExpectedSHA, publishExpectedVersion = "", ""
		}
		operationID, err := domain.NewOperationID()
		if err != nil {
			return err
		}
		temporary := catalogOperationTemp(target, operationID)
		details := catalogOperationDetails{SetupID: setupID, FileID: fileID, Role: role, TargetPath: target,
			TemporaryPath: temporary, DisplayName: name, MediaType: mediaType, ExpectedSHA256: publishExpectedSHA,
			ResultSHA256: staged.SHA256, ExpectedVersion: publishExpectedVersion, ExpectedRevision: input.ExpectedRevision,
			ByteSize: staged.Size, IdempotencyKey: input.IdempotencyKey, RequestOperation: operationName, RequestHash: requestHash}
		if existing != nil && !renamedReplacement {
			details.ExpectedDevice, details.ExpectedInode = existing.IdentityDevice, existing.IdentityInode
			details.ExpectedSize = existing.ByteSize
		}
		details.ResultSize = staged.Size
		if err := s.insertCatalogOperation(ctx, operationID, "publish", details); err != nil {
			return err
		}
		deleteOperationID := ""
		var deleteDetails catalogOperationDetails
		if renamedReplacement {
			deleteOperationID, err = domain.NewOperationID()
			if err != nil {
				return err
			}
			deleteDetails = catalogOperationDetails{SetupID: setupID, FileID: existing.ID, Role: role,
				TargetPath: existing.RelativePath, TemporaryPath: ".wsm-trash-" + deleteOperationID + "-a",
				ExpectedSHA256: existing.SHA256, ExpectedVersion: existing.Version,
				ExpectedRevision: input.ExpectedRevision, ExpectedDevice: existing.IdentityDevice,
				ExpectedInode: existing.IdentityInode, ExpectedSize: existing.ByteSize,
				IdempotencyKey: input.IdempotencyKey, RequestOperation: operationName, RequestHash: requestHash}
			if err := s.insertCatalogOperation(ctx, deleteOperationID, "delete", deleteDetails); err != nil {
				return err
			}
		}
		journalPrepared := false
		publication, err := s.catalog.PublishPrepared(ctx, staged, target, publishExpectedSHA, publishExpectedVersion, operationID,
			func(prepared *storage.Object) error {
				details.ResultDevice, details.ResultInode = prepared.Identity.Device, prepared.Identity.Inode
				details.ResultSize = prepared.Size
				payload, marshalErr := json.Marshal(details)
				if marshalErr != nil {
					return marshalErr
				}
				result, updateErr := s.db.ExecContext(ctx, `UPDATE catalog_operations SET details_json = ?,
					updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'intent'`,
					string(payload), operationID)
				if updateErr != nil {
					return databaseError(updateErr)
				}
				changed, rowsErr := result.RowsAffected()
				if rowsErr != nil || changed != 1 {
					return databaseError(errors.New("catalog publication journal changed"))
				}
				journalPrepared = true
				return nil
			})
		if err != nil {
			if !journalPrepared {
				_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
			}
			return storageError(err)
		}
		discardStaged = false
		details.ResultDevice, details.ResultInode = publication.Object.Identity.Device, publication.Object.Identity.Inode
		details.ResultVersion = publication.Object.Version
		payload, err := json.Marshal(details)
		if err != nil {
			return err
		}
		rollbackPublication := true
		var oldQuarantine *storage.CatalogQuarantine
		rollbackOld := false
		defer func() {
			if rollbackOld && oldQuarantine.Restore() == nil {
				s.refreshCatalogFileIdentity(context.Background(), setupID, existing.RelativePath, oldQuarantine.Restored)
				_ = s.finishCatalogOperationRow(context.Background(), deleteOperationID, "failed")
			}
			if rollbackPublication {
				if rollbackErr := publication.Rollback(); rollbackErr == nil && publication.Restored != nil && existing != nil {
					s.refreshCatalogFileIdentity(context.Background(), setupID, target, publication.Restored)
				}
			}
		}()
		if _, err := s.db.ExecContext(ctx, `UPDATE catalog_operations SET state = 'storage_applied', result_version = ?, details_json = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'intent'`, publication.Object.Version, string(payload), operationID); err != nil {
			return databaseError(err)
		}
		if renamedReplacement {
			oldQuarantine, err = s.catalog.Quarantine(existing.RelativePath, existing.SHA256, existing.Version, deleteOperationID, 0)
			if err != nil {
				return storageError(err)
			}
			rollbackOld = true
			if err := s.finishCatalogOperationRow(ctx, deleteOperationID, "storage_applied"); err != nil {
				return err
			}
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		now := sqlTimestamp(s.now())
		object := publication.Object
		if existing == nil {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO catalog_files(id, library_id, setup_id, role, display_name, relative_path, path_key,
				                          media_type, byte_size, sha256, object_version, identity_device,
				                          identity_inode, identity_size, identity_mtime_ns, identity_ctime_ns,
				                          legacy_storage_object_id, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, fileID, s.libraryID,
				setupID, role, name, target, pathKey, mediaType, object.Size, object.SHA256, object.Version,
				int64(object.Identity.Device), int64(object.Identity.Inode), object.Size, object.Identity.ModTimeNS,
				object.Identity.ChangeTimeNS, input.legacyObjectID, now, now)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE catalog_files SET display_name = ?, relative_path = ?, path_key = ?, media_type = ?, byte_size = ?, sha256 = ?, object_version = ?,
				       identity_device = ?, identity_inode = ?, identity_size = ?, identity_mtime_ns = ?,
				       identity_ctime_ns = ?, updated_at = ?
				 WHERE id = ? AND setup_id = ? AND object_version = ?`, name, target, pathKey, mediaType, object.Size, object.SHA256,
				object.Version, int64(object.Identity.Device), int64(object.Identity.Inode), object.Size,
				object.Identity.ModTimeNS, object.Identity.ChangeTimeNS, now, fileID, setupID, expectedVersion)
		}
		if err != nil {
			return catalogNameConflict(err)
		}
		if input.legacySourceKey != "" {
			linked, linkErr := tx.ExecContext(ctx, `UPDATE catalog_legacy_file_manifest
				SET catalog_file_id = ?, outcome = 'copied', error_code = NULL,
				    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				WHERE source_key = ? AND role = ? AND legacy_artifact_id = ?
				  AND target_relative_path = ? AND byte_size = ? AND sha256 = ?
				  AND catalog_file_id IS NULL AND outcome = 'pending'`,
				fileID, input.legacySourceKey, role, input.legacyArtifactID, target, object.Size, object.SHA256)
			if linkErr != nil {
				return databaseError(linkErr)
			}
			changed, rowsErr := linked.RowsAffected()
			if rowsErr != nil || changed != 1 {
				return databaseError(errors.New("legacy file manifest changed"))
			}
		}
		updated, err := tx.ExecContext(ctx, `UPDATE catalog_setups SET revision = revision + 1, updated_at = ? WHERE library_id = ? AND id = ? AND revision = ?`, now, s.libraryID, setupID, input.ExpectedRevision)
		if err != nil {
			return databaseError(err)
		}
		changed, err := updated.RowsAffected()
		if err != nil || changed != 1 {
			return domain.NewError(domain.CodeRevisionConflict, "setup revision has changed")
		}
		if err := bumpCatalogGeneration(ctx, tx, s.libraryID); err != nil {
			return err
		}
		if err := s.appendCatalogAudit(ctx, tx, operationName, setupID, fileID, input.ExpectedRevision,
			input.ExpectedRevision+1, map[string]any{"role": role, "replacement": existing != nil}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, operationID); err != nil {
			return databaseError(err)
		}
		if renamedReplacement {
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, deleteOperationID); err != nil {
				return databaseError(err)
			}
		}
		result, err = s.loadCatalogSetup(ctx, tx, setupID, true)
		if err != nil {
			return err
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		rollbackPublication = false
		rollbackOld = false
		if err := publication.Commit(); err != nil {
			return nil
		}
		_ = s.finishCatalogOperationRow(ctx, operationID, "completed")
		if renamedReplacement && oldQuarantine.Discard() == nil {
			_ = s.finishCatalogOperationRow(ctx, deleteOperationID, "completed")
		}
		return nil
	})
	if operationErr != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, operationErr); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, operationErr
}

func (s *Service) DeleteCatalogFile(ctx context.Context, setupID string, role domain.ArtifactRole,
	expected domain.Revision, expectedFileVersion, idempotencyKey string,
) (*domain.CatalogSetup, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	if role != domain.ArtifactRoleProgram && role != domain.ArtifactRoleSetupSheet {
		return nil, domain.NewError(domain.CodeInvalidContent, "catalog file role is invalid")
	}
	if !expected.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if expectedFileVersion == "" {
		return nil, domain.NewError(domain.CodePreconditionRequired, "file deletion requires If-Match")
	}
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	operationName := "catalogDeleteFile:" + setupID + ":" + string(role)
	requestHash, err := idempotencyRequestHash(operationName, struct {
		Expected            domain.Revision `json:"expectedRevision"`
		ExpectedFileVersion string          `json:"expectedFileVersion"`
	}{expected, expectedFileVersion})
	if err != nil {
		return nil, err
	}
	claim, err := s.claimIdempotency(ctx, idempotencyKey, operationName, requestHash)
	if err != nil {
		return nil, err
	}
	var replay domain.CatalogSetup
	if ok, replayErr := claim.replayInto(&replay); ok || replayErr != nil {
		if replayErr != nil {
			return nil, replayErr
		}
		return &replay, nil
	}
	var result *domain.CatalogSetup
	dbCommitted := false
	operationErr := s.withSetupLock("catalog-tree", func() error {
		setup, err := s.loadCatalogSetup(ctx, s.db, setupID, true)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(setup.Revision, expected); err != nil {
			return err
		}
		file := catalogFileForRole(setup, role)
		if file == nil {
			return domain.NewError(domain.CodeArtifactNotFound, "setup component was not found")
		}
		if file.Version != expectedFileVersion &&
			!s.allowRecoveredCatalogFilePrecondition(ctx, file, expectedFileVersion, idempotencyKey, operationName, requestHash) {
			return domain.NewError(domain.CodeArtifactChanged, "catalog file version no longer matches")
		}
		operationID, err := domain.NewOperationID()
		if err != nil {
			return err
		}
		temporary := ".wsm-trash-" + operationID + "-a"
		details := catalogOperationDetails{SetupID: setupID, FileID: file.ID, Role: role,
			TargetPath: file.RelativePath, TemporaryPath: temporary, ExpectedSHA256: file.SHA256,
			ExpectedVersion: file.Version, ExpectedRevision: expected, IdempotencyKey: idempotencyKey, RequestOperation: operationName,
			RequestHash: requestHash}
		details.ExpectedDevice, details.ExpectedInode, details.ByteSize = file.IdentityDevice, file.IdentityInode, file.ByteSize
		details.ExpectedSize = file.ByteSize
		if err := s.insertCatalogOperation(ctx, operationID, "delete", details); err != nil {
			return err
		}
		quarantine, err := s.catalog.Quarantine(file.RelativePath, file.SHA256, file.Version, operationID, 0)
		if err != nil {
			if quarantine != nil && quarantine.Restore() == nil {
				s.refreshCatalogFileIdentity(context.Background(), setupID, file.RelativePath, quarantine.Restored)
				_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
			}
			return storageError(err)
		}
		rollbackStorage := true
		defer func() {
			if rollbackStorage && quarantine.Restore() == nil {
				s.refreshCatalogFileIdentity(context.Background(), setupID, file.RelativePath, quarantine.Restored)
				_ = s.finishCatalogOperationRow(context.Background(), operationID, "failed")
			}
		}()
		if err := s.finishCatalogOperationRow(ctx, operationID, "storage_applied"); err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		deleted, err := tx.ExecContext(ctx, `DELETE FROM catalog_files WHERE id = ? AND setup_id = ? AND object_version = ?`,
			file.ID, setupID, file.Version)
		if err != nil {
			return databaseError(err)
		}
		changed, err := deleted.RowsAffected()
		if err != nil || changed != 1 {
			return domain.NewError(domain.CodeArtifactChanged, "setup component changed")
		}
		now := sqlTimestamp(s.now())
		updated, err := tx.ExecContext(ctx, `UPDATE catalog_setups SET revision = revision + 1, updated_at = ? WHERE library_id = ? AND id = ? AND revision = ?`,
			now, s.libraryID, setupID, expected)
		if err != nil {
			return databaseError(err)
		}
		changed, err = updated.RowsAffected()
		if err != nil || changed != 1 {
			return domain.NewError(domain.CodeRevisionConflict, "setup revision has changed")
		}
		if err := bumpCatalogGeneration(ctx, tx, s.libraryID); err != nil {
			return err
		}
		if err := s.appendCatalogAudit(ctx, tx, operationName, setupID, file.ID, expected, expected+1,
			map[string]any{"role": role}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, operationID); err != nil {
			return databaseError(err)
		}
		result, err = s.loadCatalogSetup(ctx, tx, setupID, true)
		if err != nil {
			return err
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		rollbackStorage = false
		if quarantine.Discard() == nil {
			_ = s.finishCatalogOperationRow(context.Background(), operationID, "completed")
		}
		return nil
	})
	if operationErr != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, operationErr); finishErr != nil {
			return nil, finishErr
		}
	}
	return result, operationErr
}

func (s *Service) DeleteCatalogSetup(ctx context.Context, setupID string, expected domain.Revision, idempotencyKey string) error {
	if err := s.catalogAvailable(); err != nil {
		return err
	}
	if !expected.Valid() {
		return domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := domain.ValidateID(setupID); err != nil {
		return err
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return err
	}
	operationName := "catalogDeleteSetup:" + setupID
	requestHash, err := idempotencyRequestHash(operationName, struct {
		Expected domain.Revision `json:"expectedRevision"`
	}{expected})
	if err != nil {
		return err
	}
	claim, err := s.claimIdempotency(ctx, idempotencyKey, operationName, requestHash)
	if err != nil {
		return err
	}
	if ok, replayErr := claim.replayInto(nil); ok || replayErr != nil {
		return replayErr
	}
	dbCommitted := false
	operationErr := s.withSetupLock("catalog-tree", func() error {
		setup, err := s.loadCatalogSetup(ctx, s.db, setupID, true)
		if err != nil {
			return err
		}
		if err := domain.CheckExpectedRevision(setup.Revision, expected); err != nil {
			return err
		}
		type removal struct {
			file         *domain.CatalogFile
			operationID  string
			quarantine   *storage.CatalogQuarantine
			storageState bool
		}
		removals := []removal{}
		for _, file := range []*domain.CatalogFile{setup.Program, setup.SetupSheet} {
			if file != nil {
				removals = append(removals, removal{file: file})
			}
		}
		applied := 0
		rollbackStorage := true
		defer func() {
			if rollbackStorage {
				for index := applied - 1; index >= 0; index-- {
					item := &removals[index]
					if item.quarantine.Restore() == nil {
						s.refreshCatalogFileIdentity(context.Background(), setupID, item.file.RelativePath, item.quarantine.Restored)
						_ = s.finishCatalogOperationRow(context.Background(), item.operationID, "failed")
					}
				}
			}
		}()
		for index := range removals {
			item := &removals[index]
			item.operationID, err = domain.NewOperationID()
			if err != nil {
				return err
			}
			details := catalogOperationDetails{SetupID: setupID, FileID: item.file.ID, Role: item.file.Role,
				TargetPath: item.file.RelativePath, TemporaryPath: ".wsm-trash-" + item.operationID + "-a",
				ExpectedSHA256: item.file.SHA256, ExpectedVersion: item.file.Version, ExpectedRevision: expected,
				IdempotencyKey: idempotencyKey, RequestOperation: operationName, RequestHash: requestHash}
			details.ExpectedDevice, details.ExpectedInode, details.ByteSize = item.file.IdentityDevice, item.file.IdentityInode, item.file.ByteSize
			details.ExpectedSize = item.file.ByteSize
			if err := s.insertCatalogOperation(ctx, item.operationID, "delete", details); err != nil {
				return err
			}
			item.quarantine, err = s.catalog.Quarantine(item.file.RelativePath, item.file.SHA256, item.file.Version, item.operationID, 0)
			if err != nil {
				if item.quarantine != nil && item.quarantine.Restore() == nil {
					s.refreshCatalogFileIdentity(context.Background(), setupID, item.file.RelativePath, item.quarantine.Restored)
					_ = s.finishCatalogOperationRow(context.Background(), item.operationID, "failed")
				}
				return storageError(err)
			}
			applied++
			if err := s.finishCatalogOperationRow(ctx, item.operationID, "storage_applied"); err != nil {
				return err
			}
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		deleted, err := tx.ExecContext(ctx, `DELETE FROM catalog_setups WHERE library_id = ? AND id = ? AND revision = ?`,
			s.libraryID, setupID, expected)
		if err != nil {
			return databaseError(err)
		}
		changed, err := deleted.RowsAffected()
		if err != nil || changed != 1 {
			return domain.NewError(domain.CodeRevisionConflict, "setup revision has changed")
		}
		if err := bumpCatalogGeneration(ctx, tx, s.libraryID); err != nil {
			return err
		}
		if err := s.appendCatalogAudit(ctx, tx, operationName, setupID, "", expected, 0, nil); err != nil {
			return err
		}
		for _, item := range removals {
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_operations SET state = 'db_applied', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND state = 'storage_applied'`, item.operationID); err != nil {
				return databaseError(err)
			}
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 204, nil, nil); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return databaseError(err)
		}
		dbCommitted = true
		rollbackStorage = false
		for _, item := range removals {
			if item.quarantine.Discard() == nil {
				_ = s.finishCatalogOperationRow(context.Background(), item.operationID, "completed")
			}
		}
		return nil
	})
	if operationErr != nil && !dbCommitted {
		if finishErr := s.finishCatalogClaim(claim, 0, nil, operationErr); finishErr != nil {
			return finishErr
		}
	}
	return operationErr
}

func (s *Service) catalogContentFile(ctx context.Context, setupID string, role domain.ArtifactRole) (*domain.CatalogFile, error) {
	setup, err := s.loadCatalogSetup(ctx, s.db, setupID, true)
	if err != nil {
		return nil, err
	}
	file := catalogFileForRole(setup, role)
	if file == nil {
		return nil, domain.NewError(domain.CodeArtifactNotFound, "setup component was not found")
	}
	return file, nil
}

func (s *Service) InspectCatalogContent(ctx context.Context, setupID string, role domain.ArtifactRole) (*ContentMetadata, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	file, err := s.catalogContentFile(ctx, setupID, role)
	if err != nil {
		return nil, err
	}
	object, err := s.catalog.InspectMetadata(file.RelativePath, file.SHA256, file.Version)
	if err != nil {
		return nil, contentStorageError(err)
	}
	if object.Size != file.ByteSize || object.Identity.Device != file.IdentityDevice ||
		object.Identity.Inode != file.IdentityInode {
		return nil, contentStorageError(storage.ErrObjectChanged)
	}
	return &ContentMetadata{ArtifactID: file.ID, SetupID: setupID, MediaType: file.MediaType,
		ByteSize: object.Size, Version: file.Version, ETag: artifactETag(file.Version)}, nil
}

func (s *Service) ReadCatalogRange(ctx context.Context, setupID string, role domain.ArtifactRole, expectedVersion string, offset, length int64) (*ContentRange, error) {
	if err := s.catalogAvailable(); err != nil {
		return nil, err
	}
	if offset < 0 || length < 1 || length > MaxContentRangeBytes {
		return nil, domain.NewError(domain.CodeInvalidContent, "requested content range is invalid")
	}
	file, err := s.catalogContentFile(ctx, setupID, role)
	if err != nil {
		return nil, err
	}
	if expectedVersion != "" && expectedVersion != file.Version {
		return nil, domain.NewError(domain.CodeArtifactChanged, "catalog file version no longer matches")
	}
	releaseContent, err := s.acquireContent(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer releaseContent()
	buffer := make([]byte, length)
	count, total, err := s.catalog.ReadRange(ctx, file.RelativePath, file.SHA256, file.Version, offset, buffer)
	if errors.Is(err, io.EOF) {
		return nil, domain.NewError(domain.CodeInvalidRange, "requested range starts beyond the file")
	}
	if err != nil {
		return nil, contentStorageError(err)
	}
	return &ContentRange{ContentMetadata: ContentMetadata{ArtifactID: file.ID, SetupID: setupID,
		MediaType: file.MediaType, ByteSize: total, Version: file.Version, ETag: artifactETag(file.Version)},
		Offset: offset, Data: buffer[:count]}, nil
}

func (s *Service) refreshCatalogFileIdentity(ctx context.Context, setupID, relative string, object *storage.Object) {
	if object == nil {
		return
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE catalog_files SET object_version = ?, identity_device = ?, identity_inode = ?, identity_size = ?,
		       identity_mtime_ns = ?, identity_ctime_ns = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE setup_id = ? AND relative_path = ?`, object.Version, int64(object.Identity.Device),
		int64(object.Identity.Inode), object.Size, object.Identity.ModTimeNS, object.Identity.ChangeTimeNS,
		setupID, relative)
}

// allowRecoveredCatalogFilePrecondition recognizes only the exact request
// whose interrupted filesystem mutation was durably rolled back. Internal
// rename/restore changes ctime (and therefore Version) even though the same
// inode and bytes were restored. The failed journal row provides the narrow
// alias needed for a same-key retry without accepting a generally stale ETag.
func (s *Service) allowRecoveredCatalogFilePrecondition(ctx context.Context, file *domain.CatalogFile,
	expectedVersion, idempotencyKey, requestOperation, requestHash string,
) bool {
	if file == nil || expectedVersion == "" || idempotencyKey == "" || requestHash == "" {
		return false
	}
	object, err := s.catalog.InspectMetadata(file.RelativePath, file.SHA256, file.Version)
	if err != nil || object.Identity.Device != file.IdentityDevice || object.Identity.Inode != file.IdentityInode ||
		object.Size != file.ByteSize {
		return false
	}
	rows, err := s.db.QueryContext(ctx, `SELECT details_json FROM catalog_operations
		WHERE library_id = ? AND state = 'failed' AND idempotency_key = ? AND request_hash = ?
		ORDER BY completed_at DESC, id DESC`, s.libraryID, idempotencyKey, requestHash)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		var details catalogOperationDetails
		if rows.Scan(&payload) != nil || json.Unmarshal([]byte(payload), &details) != nil {
			return false
		}
		if details.RequestOperation == requestOperation && details.FileID == file.ID &&
			details.ExpectedVersion == expectedVersion && details.ExpectedSHA256 == file.SHA256 &&
			details.ExpectedDevice == file.IdentityDevice && details.ExpectedInode == file.IdentityInode &&
			details.ExpectedSize == file.ByteSize {
			return true
		}
	}
	return false
}
