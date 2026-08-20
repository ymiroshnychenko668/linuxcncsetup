package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type scanner interface{ Scan(...any) error }

const setupSelect = `
	SELECT s.id, s.library_id, s.name, s.description, s.status, s.revision,
	       s.source, s.source_setup_id, s.archived_from_status,
	       (SELECT i.id FROM import_sessions i WHERE i.setup_id = s.id ORDER BY i.created_at DESC LIMIT 1),
	       s.attention_reason, s.created_at, s.updated_at
	  FROM setups s
	 WHERE s.library_id = ? AND s.id = ?`

func (s *Service) loadSetup(ctx context.Context, q queryer, setupID string, withArtifacts bool) (*domain.Setup, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	setup, attention, err := scanSetup(q.QueryRowContext(ctx, setupSelect, s.libraryID, setupID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.CodeSetupNotFound, "setup was not found")
	}
	if err != nil {
		return nil, databaseError(err)
	}
	if withArtifacts {
		artifacts, err := s.loadArtifacts(ctx, q, setupID)
		if err != nil {
			return nil, err
		}
		setup.Artifacts = make([]domain.Artifact, len(artifacts))
		for index := range artifacts {
			setup.Artifacts[index] = artifacts[index].Artifact
		}
	}
	setup.NotReadyReasons = s.notReadyReasons(setup, attention)
	return setup, nil
}

func scanSetup(row scanner) (*domain.Setup, string, error) {
	var setup domain.Setup
	var status, source string
	var sourceID, archivedFrom, importID, attention sql.NullString
	var created, updated string
	if err := row.Scan(
		&setup.ID, &setup.LibraryID, &setup.Name, &setup.Description, &status, &setup.Revision,
		&source, &sourceID, &archivedFrom, &importID, &attention, &created, &updated,
	); err != nil {
		return nil, "", err
	}
	setup.Status = domain.SetupStatus(status)
	setup.Source = domain.SetupSource(source)
	setup.SourceSetupID = sourceID.String
	setup.ImportSessionID = importID.String
	if archivedFrom.Valid {
		value := domain.SetupStatus(archivedFrom.String)
		setup.ArchivedFromStatus = &value
	}
	var err error
	if setup.CreatedAt, err = parseTimestamp(created); err != nil {
		return nil, "", err
	}
	if setup.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return nil, "", err
	}
	return &setup, attention.String, nil
}

type artifactRecord struct {
	domain.Artifact
	StorageKey string
}

func (s *Service) loadArtifacts(ctx context.Context, q queryer, setupID string) ([]artifactRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT a.id, a.setup_id, a.role, a.display_name, o.media_type,
		       o.byte_size, o.sha256, a.object_version, a.position, a.is_primary,
		       a.storage_object_id, o.storage_key, a.created_at, a.updated_at
		  FROM setup_artifacts a
		  JOIN storage_objects o ON o.id = a.storage_object_id
		 WHERE a.setup_id = ?
		 ORDER BY CASE a.role WHEN 'program' THEN 0 ELSE 1 END, a.position, a.id`, setupID)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := make([]artifactRecord, 0)
	for rows.Next() {
		var record artifactRecord
		var role, sha, created, updated string
		var primary int
		if err := rows.Scan(&record.ID, &record.SetupID, &role, &record.DisplayName,
			&record.MediaType, &record.ByteSize, &sha, &record.Version, &record.Position,
			&primary, &record.StorageObjectID, &record.StorageKey, &created, &updated); err != nil {
			return nil, databaseError(err)
		}
		record.Role = domain.ArtifactRole(role)
		record.Primary = primary != 0
		record.State = domain.ArtifactStateAvailable
		record.SHA256 = sha
		if record.CreatedAt, err = parseTimestamp(created); err != nil {
			return nil, databaseError(err)
		}
		if record.UpdatedAt, err = parseTimestamp(updated); err != nil {
			return nil, databaseError(err)
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}

func (s *Service) loadArtifact(ctx context.Context, q queryer, setupID, artifactID string) (*artifactRecord, error) {
	if err := domain.ValidateID(artifactID); err != nil {
		return nil, err
	}
	rows, err := s.loadArtifacts(ctx, q, setupID)
	if err != nil {
		return nil, err
	}
	for index := range rows {
		if rows[index].ID == artifactID {
			return &rows[index], nil
		}
	}
	return nil, domain.NewError(domain.CodeArtifactNotFound, "artifact was not found")
}

func (s *Service) notReadyReasons(setup *domain.Setup, attention string) []string {
	if setup.Status == domain.SetupStatusReady {
		return nil
	}
	if setup.Status == domain.SetupStatusArchived {
		return []string{"setup is archived"}
	}
	result := make([]string, 0, 3)
	programs, hasSheet := 0, false
	for _, artifact := range setup.Artifacts {
		if artifact.Role == domain.ArtifactRoleProgram {
			programs++
		} else if artifact.Role == domain.ArtifactRoleSetupSheet {
			hasSheet = true
		}
	}
	if programs == 0 {
		result = append(result, "add at least one G-code program")
	}
	if s.requireSetupSheetForReady && !hasSheet {
		result = append(result, "add a setup sheet")
	}
	if setup.Status == domain.SetupStatusAttention {
		if attention == "" {
			attention = "managed content needs attention"
		}
		result = append(result, attention)
	} else {
		result = append(result, "validate this revision")
	}
	return result
}

func (s *Service) appendAudit(ctx context.Context, tx *sql.Tx, operation domain.AuditOperation,
	setupID, artifactID, jobID string, before, after domain.Revision, result domain.AuditResult,
	errorCode domain.ErrorCode, details any,
) error {
	id, err := domain.NewAuditEventID()
	if err != nil {
		return err
	}
	payload := []byte("{}")
	if details != nil {
		payload, err = json.Marshal(details)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events(id, library_id, operation, setup_id, artifact_id, job_id,
		                         from_revision, to_revision, result, error_code, details_json)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
		        NULLIF(?, 0), NULLIF(?, 0), ?, NULLIF(?, ''), ?)`,
		id, s.libraryID, operation, setupID, artifactID, jobID, before, after, result, errorCode, string(payload))
	return databaseError(err)
}

func (s *Service) appendJournal(ctx context.Context, tx *sql.Tx, operation domain.AuditOperation,
	setupID, artifactID, objectID, importID, jobID string, expected domain.Revision, details any,
) (string, error) {
	id, err := domain.NewOperationID()
	if err != nil {
		return "", err
	}
	payload := []byte("{}")
	if details != nil {
		payload, err = json.Marshal(details)
		if err != nil {
			return "", err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO operation_journal(id, library_id, operation, setup_id, artifact_id,
		                              storage_object_id, import_session_id, job_id,
		                              expected_revision, state, details_json)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
		        NULLIF(?, ''), NULLIF(?, 0), 'intent', ?)`,
		id, s.libraryID, operation, setupID, artifactID, objectID, importID, jobID, expected, string(payload))
	if err != nil {
		return "", databaseError(err)
	}
	return id, nil
}

func completeJournal(ctx context.Context, tx *sql.Tx, operationID string, target domain.Revision) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE operation_journal
		   SET state = 'completed', target_revision = NULLIF(?, 0),
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		       completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND state IN ('intent', 'storage_applied', 'db_applied')`, target, operationID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return databaseError(fmt.Errorf("operation journal transition failed"))
	}
	return nil
}
