package service

import (
	"encoding/json"
	"io"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

// CreateSetupInput creates the aggregate only; programs and the optional
// setup sheet are added through explicit domain operations.
type CreateSetupInput struct {
	Name           string
	Description    string
	IdempotencyKey string
}

type UpdateSetupInput struct {
	ExpectedRevision domain.Revision
	Name             string
	Description      string
	IdempotencyKey   string
}

type ListSetupsOptions struct {
	Query         string
	Statuses      []domain.SetupStatus
	HasSetupSheet *bool
	Current       *bool
	Sort          string
	Cursor        string
	Limit         int
}

type SetupPage struct {
	Items      []domain.SetupSummary `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

type UIState struct {
	ClientID           string          `json:"clientId"`
	Screen             string          `json:"screen"`
	SelectedSetupID    string          `json:"selectedSetupId,omitempty"`
	SelectedArtifactID string          `json:"selectedArtifactId,omitempty"`
	Filters            json.RawMessage `json:"filters"`
	View               json.RawMessage `json:"view"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

type SetCurrentInput struct {
	SetupID                  string
	ExpectedRevision         domain.Revision
	ExpectedPreviousSetupID  string
	ExpectedPreviousRevision domain.Revision
	Confirmed                bool
	IdempotencyKey           string
}

type ClearCurrentInput struct {
	ExpectedSetupID  string
	ExpectedRevision domain.Revision
	Confirmed        bool
	IdempotencyKey   string
}

type StartImportInput struct {
	Name           string
	Description    string
	IdempotencyKey string
}

type UploadArtifactInput struct {
	Role           domain.ArtifactRole
	DisplayName    string
	Content        io.Reader
	ExpectedSize   int64
	IdempotencyKey string
}

type CommitImportInput struct {
	ExpectedArtifactIDs []string
	PrimaryArtifactID   string
	SavePartialDraft    bool
	IdempotencyKey      string
}

type AddProgramsInput struct {
	ExpectedRevision domain.Revision
	Programs         []UploadArtifactInput
	IdempotencyKey   string
}

// ProgramUploadSource synchronously yields one streaming upload at a time.
// A multipart HTTP adapter can call yield before advancing to the next part,
// so no complete file or multi-file request is buffered in memory.
type ProgramUploadSource func(yield func(UploadArtifactInput) error) error

type AddProgramsStreamInput struct {
	ExpectedRevision domain.Revision
	IdempotencyKey   string
	Source           ProgramUploadSource
	finalizeTx       setupMutationFinalizer
}

// UploadJobOperation is a deliberately small setup-manager upload surface.
// It cannot name host paths or arbitrary storage locations.
type UploadJobOperation string

const (
	UploadJobAddPrograms    UploadJobOperation = "addPrograms"
	UploadJobReplaceProgram UploadJobOperation = "replaceProgram"
	UploadJobPutSetupSheet  UploadJobOperation = "putSetupSheet"
)

type UploadJobItem struct {
	DisplayName string `json:"displayName"`
	Size        int64  `json:"size"`
}

// PrepareUploadJobInput binds the immutable mutation preconditions before a
// potentially long request body is accepted. The returned durable job ID is
// therefore available to clients before transfer starts.
type PrepareUploadJobInput struct {
	Operation        UploadJobOperation
	ExpectedRevision domain.Revision
	ArtifactID       string
	ExpectedVersion  string
	Items            []UploadJobItem
	IdempotencyKey   string
}

// RunUploadJobInput supplies content for an already prepared job. Add-program
// jobs use Source; single-file replace/sheet jobs use Content.
type RunUploadJobInput struct {
	Content        io.Reader
	Source         ProgramUploadSource
	IdempotencyKey string
}

type ReplaceArtifactInput struct {
	ExpectedRevision domain.Revision
	ExpectedVersion  string
	DisplayName      string
	Content          io.Reader
	ExpectedSize     int64
	IdempotencyKey   string
	finalizeTx       setupMutationFinalizer
}

type RenameArtifactInput struct {
	ExpectedRevision domain.Revision
	ExpectedVersion  string
	DisplayName      string
	IdempotencyKey   string
}

type DeleteArtifactInput struct {
	ExpectedRevision             domain.Revision
	ExpectedVersion              string
	ReplacementPrimaryArtifactID string
	LeavePrimaryUnassigned       bool
	ConfirmDeleteLastProgram     bool
	IdempotencyKey               string
}

type SetPrimaryInput struct {
	ExpectedRevision domain.Revision
	ExpectedVersion  string
	IdempotencyKey   string
}

type ValidateInput struct {
	ExpectedRevision domain.Revision
	IdempotencyKey   string
}

type DuplicateInput struct {
	ExpectedRevision domain.Revision
	Name             string
	IdempotencyKey   string
}

type ArchiveInput struct {
	ExpectedRevision domain.Revision
	IdempotencyKey   string
}

type DeletePlan struct {
	ConfirmationToken string          `json:"confirmationToken"`
	SetupID           string          `json:"setupId"`
	Revision          domain.Revision `json:"revision"`
	ExactName         string          `json:"exactName"`
	ProgramCount      int             `json:"programCount"`
	HasSetupSheet     bool            `json:"hasSetupSheet"`
	UniqueBytes       int64           `json:"uniqueBytes"`
	ExpiresAt         time.Time       `json:"expiresAt"`
}

type PermanentDeleteInput struct {
	ExpectedRevision  domain.Revision
	ExactName         string
	ConfirmationToken string
	IdempotencyKey    string
}

type ContentDescriptor struct {
	Artifact domain.Artifact
	Reader   io.ReadSeekCloser
	ETag     string
}

type ContentMetadata struct {
	ArtifactID string `json:"artifactId"`
	SetupID    string `json:"setupId"`
	MediaType  string `json:"mediaType"`
	ByteSize   int64  `json:"byteSize"`
	Version    string `json:"version"`
	ETag       string `json:"etag"`
}

type ContentRange struct {
	ContentMetadata
	Offset int64  `json:"offset"`
	Data   []byte `json:"-"`
}
