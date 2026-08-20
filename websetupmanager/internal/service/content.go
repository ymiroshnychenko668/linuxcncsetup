package service

import (
	"context"
	"errors"
	"io"
	"io/fs"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

const MaxContentRangeBytes int64 = 4 << 20

func artifactETag(version string) string { return `"` + version + `"` }

// InspectArtifactContent resolves IDs through SQLite and verifies the expected
// inode version. The response contains no storage key or host path.
func (s *Service) InspectArtifactContent(ctx context.Context, setupID, artifactID string) (*ContentMetadata, error) {
	record, err := s.contentRecord(ctx, setupID, artifactID)
	if err != nil {
		return nil, err
	}
	object, err := s.objects.InspectObject(record.StorageKey, record.SHA256, record.Version)
	if err != nil {
		s.markContentAttention(ctx, setupID, err)
		return nil, contentStorageError(err)
	}
	return &ContentMetadata{
		ArtifactID: record.ID, SetupID: setupID, MediaType: record.MediaType,
		ByteSize: object.Size, Version: record.Version, ETag: artifactETag(record.Version),
	}, nil
}

// ReadArtifactRange returns at most MaxContentRangeBytes bytes and checks the
// opaque version both before and after the read.
func (s *Service) ReadArtifactRange(ctx context.Context, setupID, artifactID, expectedVersion string, offset, length int64) (*ContentRange, error) {
	if offset < 0 || length < 1 || length > MaxContentRangeBytes {
		return nil, domain.NewError(domain.CodeInvalidContent, "requested content range is invalid")
	}
	record, err := s.contentRecord(ctx, setupID, artifactID)
	if err != nil {
		return nil, err
	}
	if expectedVersion != "" && expectedVersion != record.Version {
		return nil, domain.NewError(domain.CodeArtifactChanged, "artifact version no longer matches")
	}
	release, err := s.acquireContent(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer release()
	buffer := make([]byte, length)
	count, total, err := s.objects.ReadObjectRange(ctx, record.StorageKey, record.SHA256, record.Version, offset, buffer)
	if errors.Is(err, io.EOF) {
		return nil, domain.NewError(domain.CodeInvalidRange, "requested range starts beyond the artifact")
	}
	if errors.Is(err, context.Canceled) {
		return nil, storageError(err)
	}
	if err != nil {
		s.markContentAttention(ctx, setupID, err)
		return nil, contentStorageError(err)
	}
	buffer = buffer[:count]
	return &ContentRange{ContentMetadata: ContentMetadata{
		ArtifactID: record.ID, SetupID: setupID, MediaType: record.MediaType,
		ByteSize: total, Version: record.Version, ETag: artifactETag(record.Version),
	}, Offset: offset, Data: buffer}, nil
}

// ReadArtifactAll builds one caller-bounded, version-consistent snapshot. The
// production viewers stream content; this helper remains useful to bounded
// internal callers and tests.
func (s *Service) ReadArtifactAll(ctx context.Context, setupID, artifactID string, maximum int64) (*ContentRange, error) {
	metadata, err := s.InspectArtifactContent(ctx, setupID, artifactID)
	if err != nil {
		return nil, err
	}
	if maximum < 0 || metadata.ByteSize > maximum {
		return nil, domain.NewError(domain.CodeFileTooLarge, "artifact is too large for this viewer")
	}
	result := &ContentRange{ContentMetadata: *metadata, Data: make([]byte, 0, metadata.ByteSize)}
	for offset := int64(0); offset < metadata.ByteSize; {
		length := min(MaxContentRangeBytes, metadata.ByteSize-offset)
		block, err := s.ReadArtifactRange(ctx, setupID, artifactID, metadata.Version, offset, length)
		if err != nil {
			return nil, err
		}
		result.Data = append(result.Data, block.Data...)
		offset += int64(len(block.Data))
		if len(block.Data) == 0 {
			return nil, domain.NewError(domain.CodeUploadIncomplete, "artifact could not be read completely")
		}
	}
	return result, nil
}

func (s *Service) contentRecord(ctx context.Context, setupID, artifactID string) (*artifactRecord, error) {
	if _, err := s.loadSetup(ctx, s.db, setupID, false); err != nil {
		return nil, err
	}
	return s.loadArtifact(ctx, s.db, setupID, artifactID)
}

func contentStorageError(err error) error {
	if errors.Is(err, storage.ErrObjectChanged) || errors.Is(err, storage.ErrInvalidObject) || errors.Is(err, fs.ErrNotExist) {
		return domain.WrapError(domain.CodeArtifactChanged, "artifact content changed or is unavailable", err)
	}
	return storageError(err)
}

func (s *Service) markContentAttention(ctx context.Context, setupID string, cause error) {
	reason := "managed artifact identity changed"
	if errors.Is(cause, fs.ErrNotExist) {
		reason = "managed artifact is missing"
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE setups
		   SET status = CASE WHEN status = 'archived' THEN status ELSE 'attention' END,
		       attention_reason = CASE WHEN status = 'archived' THEN attention_reason ELSE ? END,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND library_id = ?`, reason, setupID, s.libraryID)
}
