// Package domain defines the stable Web Setup Manager entities and value
// types. It deliberately contains no filesystem paths: the HTTP and frontend
// layers may safely serialize these types without exposing managed storage.
package domain

import (
	"encoding/json"
	"time"
)

// SetupStatus describes the lifecycle of a setup.
type SetupStatus string

const (
	SetupStatusDraft     SetupStatus = "draft"
	SetupStatusReady     SetupStatus = "ready"
	SetupStatusAttention SetupStatus = "attention"
	SetupStatusArchived  SetupStatus = "archived"
)

// Valid reports whether the status is part of the P0 setup state machine.
func (s SetupStatus) Valid() bool {
	switch s {
	case SetupStatusDraft, SetupStatusReady, SetupStatusAttention, SetupStatusArchived:
		return true
	default:
		return false
	}
}

// SetupSource identifies how a setup first entered the library.
type SetupSource string

const (
	SetupSourceCreated    SetupSource = "created"
	SetupSourceImported   SetupSource = "imported"
	SetupSourceDuplicated SetupSource = "duplicated"
)

func (s SetupSource) Valid() bool {
	switch s {
	case SetupSourceCreated, SetupSourceImported, SetupSourceDuplicated:
		return true
	default:
		return false
	}
}

// ArtifactRole is the domain role of an artifact. Arbitrary attachments are
// intentionally absent from P0.
type ArtifactRole string

const (
	ArtifactRoleProgram    ArtifactRole = "program"
	ArtifactRoleSetupSheet ArtifactRole = "setup_sheet"
)

func (r ArtifactRole) Valid() bool {
	return r == ArtifactRoleProgram || r == ArtifactRoleSetupSheet
}

// ArtifactState records whether the immutable object still matches the
// identity observed by the application.
type ArtifactState string

const (
	ArtifactStateAvailable   ArtifactState = "available"
	ArtifactStateMissing     ArtifactState = "missing"
	ArtifactStateChanged     ArtifactState = "changed"
	ArtifactStateCorrupt     ArtifactState = "corrupt"
	ArtifactStateUnavailable ArtifactState = "unavailable"
)

func (s ArtifactState) Valid() bool {
	switch s {
	case ArtifactStateAvailable, ArtifactStateMissing, ArtifactStateChanged,
		ArtifactStateCorrupt, ArtifactStateUnavailable:
		return true
	default:
		return false
	}
}

// Setup is the aggregate root. Artifacts belong to exactly one setup even
// when their immutable storage object is shared by a duplicate.
type Setup struct {
	ID                 string       `json:"setupId"`
	LibraryID          string       `json:"libraryId"`
	Name               string       `json:"name"`
	Description        string       `json:"description,omitempty"`
	Status             SetupStatus  `json:"status"`
	ArchivedFromStatus *SetupStatus `json:"archivedFromStatus,omitempty"`
	Revision           Revision     `json:"revision"`
	Source             SetupSource  `json:"source"`
	SourceSetupID      string       `json:"sourceSetupId,omitempty"`
	ImportSessionID    string       `json:"importSessionId,omitempty"`
	Artifacts          []Artifact   `json:"artifacts"`
	NotReadyReasons    []string     `json:"notReadyReasons,omitempty"`
	CreatedAt          time.Time    `json:"createdAt"`
	UpdatedAt          time.Time    `json:"updatedAt"`
}

// SetupSummary is the bounded representation used in the setup library.
type SetupSummary struct {
	ID              string      `json:"setupId"`
	Name            string      `json:"name"`
	Description     string      `json:"description,omitempty"`
	Status          SetupStatus `json:"status"`
	Revision        Revision    `json:"revision"`
	ProgramCount    int         `json:"programCount"`
	HasSetupSheet   bool        `json:"hasSetupSheet"`
	IsCurrent       bool        `json:"isCurrent"`
	NotReadyReasons []string    `json:"notReadyReasons,omitempty"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
	LastOpenedAt    *time.Time  `json:"lastOpenedAt,omitempty"`
}

// Artifact is a logical setup member. StorageObjectID is an internal
// relationship and must never be serialized to an API consumer.
type Artifact struct {
	ID              string        `json:"artifactId"`
	SetupID         string        `json:"setupId"`
	Role            ArtifactRole  `json:"role"`
	DisplayName     string        `json:"displayName"`
	MediaType       string        `json:"mediaType"`
	ByteSize        int64         `json:"byteSize"`
	SHA256          string        `json:"-"`
	Version         string        `json:"version"`
	Position        int           `json:"position"`
	Primary         bool          `json:"primary"`
	State           ArtifactState `json:"state"`
	StorageObjectID string        `json:"-"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

// StorageObject is immutable published content. StorageKey describes the
// physical layout and is intentionally impossible to expose through JSON.
type StorageObject struct {
	ID         string    `json:"-"`
	StorageKey string    `json:"-"`
	MediaType  string    `json:"mediaType"`
	ByteSize   int64     `json:"byteSize"`
	SHA256     string    `json:"-"`
	RefCount   int64     `json:"refCount"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ValidationState is the lifecycle of a validation run.
type ValidationState string

const (
	ValidationStateQueued    ValidationState = "queued"
	ValidationStateRunning   ValidationState = "running"
	ValidationStateSucceeded ValidationState = "succeeded"
	ValidationStateFailed    ValidationState = "failed"
	ValidationStateCancelled ValidationState = "cancelled"
	ValidationStateConflict  ValidationState = "conflict"

	// ValidationStatePassed is a semantic alias for callers that deal in
	// readiness results. The persisted and serialized value is "succeeded".
	ValidationStatePassed = ValidationStateSucceeded
)

func (s ValidationState) Valid() bool {
	switch s {
	case ValidationStateQueued, ValidationStateRunning, ValidationStateSucceeded,
		ValidationStateFailed, ValidationStateCancelled, ValidationStateConflict:
		return true
	default:
		return false
	}
}

// Terminal reports whether a validation result can no longer change.
func (s ValidationState) Terminal() bool {
	switch s {
	case ValidationStateSucceeded, ValidationStateFailed, ValidationStateCancelled, ValidationStateConflict:
		return true
	default:
		return false
	}
}

type ValidationSeverity string

const (
	ValidationSeverityError   ValidationSeverity = "error"
	ValidationSeverityWarning ValidationSeverity = "warning"
)

func (s ValidationSeverity) Valid() bool {
	return s == ValidationSeverityError || s == ValidationSeverityWarning
}

// ValidationIssue is actionable and may point at one artifact or at the setup
// as a whole when ArtifactID is empty.
type ValidationIssue struct {
	Code       string             `json:"code"`
	Severity   ValidationSeverity `json:"severity"`
	Message    string             `json:"message"`
	ArtifactID string             `json:"artifactId,omitempty"`
	Action     string             `json:"action,omitempty"`
}

type ValidationRun struct {
	ID          string            `json:"validationRunId"`
	SetupID     string            `json:"setupId"`
	Revision    Revision          `json:"revision"`
	State       ValidationState   `json:"state"`
	Issues      []ValidationIssue `json:"issues"`
	StartedAt   *time.Time        `json:"startedAt,omitempty"`
	CompletedAt *time.Time        `json:"completedAt,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// CurrentSetup pins the exact revision explicitly selected by the operator.
// It is a reference only and carries no execution semantics.
type CurrentSetup struct {
	LibraryID        string    `json:"libraryId"`
	SetupID          string    `json:"setupId"`
	RevisionSelected Revision  `json:"revisionSelected"`
	SelectedAt       time.Time `json:"selectedAt"`
}

type RecentSetup struct {
	LibraryID      string      `json:"libraryId"`
	SetupID        string      `json:"setupId"`
	SetupName      string      `json:"setupName"`
	SetupStatus    SetupStatus `json:"setupStatus"`
	LastArtifactID string      `json:"lastArtifactId,omitempty"`
	LastLine       int64       `json:"lastLine,omitempty"`
	LastOpenedAt   time.Time   `json:"lastOpenedAt"`
}

type JobKind string

const (
	JobKindImport           JobKind = "import"
	JobKindAddPrograms      JobKind = "addPrograms"
	JobKindReplaceProgram   JobKind = "replaceProgram"
	JobKindUpdateSetupSheet JobKind = "updateSetupSheet"
	JobKindValidate         JobKind = "validate"
	JobKindDuplicate        JobKind = "duplicate"
	JobKindRestore          JobKind = "restore"
	JobKindPermanentDelete  JobKind = "permanentDelete"
	JobKindGCodeSearch      JobKind = "gcodeSearch"
	JobKindReconcile        JobKind = "reconcile"
)

func (k JobKind) Valid() bool {
	switch k {
	case JobKindImport, JobKindAddPrograms, JobKindReplaceProgram, JobKindUpdateSetupSheet, JobKindValidate,
		JobKindDuplicate, JobKindRestore, JobKindPermanentDelete, JobKindGCodeSearch, JobKindReconcile:
		return true
	default:
		return false
	}
}

// JobState includes the four stable terminal states required by API-007.
type JobState string

const (
	JobStateQueued     JobState = "queued"
	JobStateRunning    JobState = "running"
	JobStateCancelling JobState = "cancelling"
	JobStateSucceeded  JobState = "succeeded"
	JobStateFailed     JobState = "failed"
	JobStateCancelled  JobState = "cancelled"
	JobStateConflict   JobState = "conflict"
)

func (s JobState) Valid() bool {
	switch s {
	case JobStateQueued, JobStateRunning, JobStateCancelling, JobStateSucceeded, JobStateFailed,
		JobStateCancelled, JobStateConflict:
		return true
	default:
		return false
	}
}

func (s JobState) Terminal() bool {
	switch s {
	case JobStateSucceeded, JobStateFailed, JobStateCancelled, JobStateConflict:
		return true
	default:
		return false
	}
}

type JobProgress struct {
	CompletedBytes int64 `json:"completedBytes"`
	TotalBytes     int64 `json:"totalBytes,omitempty"`
	CompletedItems int64 `json:"completedItems"`
	TotalItems     int64 `json:"totalItems,omitempty"`
}

type Job struct {
	ID          string          `json:"jobId"`
	Kind        JobKind         `json:"kind"`
	SetupID     string          `json:"setupId,omitempty"`
	State       JobState        `json:"state"`
	Progress    JobProgress     `json:"progress"`
	ErrorCode   ErrorCode       `json:"errorCode,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	StartedAt   *time.Time      `json:"startedAt,omitempty"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
}

type ImportState string

const (
	ImportStateStaging    ImportState = "staging"
	ImportStateCommitting ImportState = "committing"
	ImportStateSucceeded  ImportState = "succeeded"
	ImportStateDraftSaved ImportState = "draft_saved"
	ImportStateFailed     ImportState = "failed"
	ImportStateCancelled  ImportState = "cancelled"
	ImportStateConflict   ImportState = "conflict"
)

func (s ImportState) Valid() bool {
	switch s {
	case ImportStateStaging, ImportStateCommitting, ImportStateSucceeded, ImportStateDraftSaved,
		ImportStateFailed, ImportStateCancelled, ImportStateConflict:
		return true
	default:
		return false
	}
}

func (s ImportState) Terminal() bool {
	switch s {
	case ImportStateSucceeded, ImportStateDraftSaved, ImportStateFailed, ImportStateCancelled, ImportStateConflict:
		return true
	default:
		return false
	}
}

type ImportArtifactState string

const (
	ImportArtifactPending   ImportArtifactState = "pending"
	ImportArtifactUploading ImportArtifactState = "uploading"
	ImportArtifactStaged    ImportArtifactState = "staged"
	ImportArtifactExcluded  ImportArtifactState = "excluded"
	ImportArtifactPublished ImportArtifactState = "published"
	ImportArtifactFailed    ImportArtifactState = "failed"
)

func (s ImportArtifactState) Valid() bool {
	switch s {
	case ImportArtifactPending, ImportArtifactUploading, ImportArtifactStaged,
		ImportArtifactExcluded, ImportArtifactPublished, ImportArtifactFailed:
		return true
	default:
		return false
	}
}

type ImportArtifact struct {
	ID          string              `json:"importArtifactId"`
	ArtifactID  string              `json:"artifactId,omitempty"`
	Role        ArtifactRole        `json:"role"`
	DisplayName string              `json:"displayName"`
	ByteSize    int64               `json:"byteSize"`
	Bytes       int64               `json:"bytes"`
	State       ImportArtifactState `json:"state"`
	ErrorCode   ErrorCode           `json:"errorCode,omitempty"`
}

type ImportSession struct {
	ID             string           `json:"importSessionId"`
	JobID          string           `json:"jobId,omitempty"`
	IdempotencyKey string           `json:"-"`
	Name           string           `json:"name"`
	Description    string           `json:"description,omitempty"`
	State          ImportState      `json:"state"`
	Artifacts      []ImportArtifact `json:"artifacts"`
	Bytes          int64            `json:"bytes"`
	SetupID        string           `json:"setupId,omitempty"`
	ErrorCode      ErrorCode        `json:"errorCode,omitempty"`
	ExpiresAt      time.Time        `json:"expiresAt"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}

type AuditOperation string

const (
	AuditOperationCreate          AuditOperation = "create"
	AuditOperationImport          AuditOperation = "import"
	AuditOperationValidate        AuditOperation = "validate"
	AuditOperationSelectCurrent   AuditOperation = "selectCurrent"
	AuditOperationClearCurrent    AuditOperation = "clearCurrent"
	AuditOperationAddPrograms     AuditOperation = "addPrograms"
	AuditOperationReplaceProgram  AuditOperation = "replaceProgram"
	AuditOperationRenameProgram   AuditOperation = "renameProgram"
	AuditOperationSetPrimary      AuditOperation = "setPrimaryProgram"
	AuditOperationDeleteProgram   AuditOperation = "deleteProgram"
	AuditOperationSetupSheet      AuditOperation = "updateSetupSheet"
	AuditOperationDuplicate       AuditOperation = "duplicate"
	AuditOperationArchive         AuditOperation = "archive"
	AuditOperationRestore         AuditOperation = "restore"
	AuditOperationPermanentDelete AuditOperation = "permanentDelete"
	AuditOperationReconcile       AuditOperation = "reconcile"
)

func (o AuditOperation) Valid() bool {
	switch o {
	case AuditOperationCreate, AuditOperationImport, AuditOperationValidate,
		AuditOperationSelectCurrent, AuditOperationClearCurrent, AuditOperationAddPrograms,
		AuditOperationReplaceProgram, AuditOperationRenameProgram, AuditOperationSetPrimary,
		AuditOperationDeleteProgram, AuditOperationSetupSheet, AuditOperationDuplicate,
		AuditOperationArchive, AuditOperationRestore, AuditOperationPermanentDelete,
		AuditOperationReconcile:
		return true
	default:
		return false
	}
}

type AuditResult string

const (
	AuditResultSucceeded AuditResult = "succeeded"
	AuditResultFailed    AuditResult = "failed"
	AuditResultCancelled AuditResult = "cancelled"
	AuditResultConflict  AuditResult = "conflict"
)

func (r AuditResult) Valid() bool {
	switch r {
	case AuditResultSucceeded, AuditResultFailed, AuditResultCancelled, AuditResultConflict:
		return true
	default:
		return false
	}
}

type AuditEvent struct {
	ID             string         `json:"auditEventId"`
	Operation      AuditOperation `json:"operation"`
	LibraryID      string         `json:"libraryId"`
	SetupID        string         `json:"setupId,omitempty"`
	ArtifactID     string         `json:"artifactId,omitempty"`
	JobID          string         `json:"jobId,omitempty"`
	RevisionBefore Revision       `json:"revisionBefore,omitempty"`
	RevisionAfter  Revision       `json:"revisionAfter,omitempty"`
	Result         AuditResult    `json:"result"`
	ErrorCode      ErrorCode      `json:"errorCode,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
}

type JournalState string

const (
	JournalStateIntent          JournalState = "intent"
	JournalStateStorageApplied  JournalState = "storage_applied"
	JournalStateDatabaseApplied JournalState = "db_applied"
	JournalStateCompleted       JournalState = "completed"
	JournalStateFailed          JournalState = "failed"
	JournalStateConflict        JournalState = "conflict"
)

func (s JournalState) Valid() bool {
	switch s {
	case JournalStateIntent, JournalStateStorageApplied, JournalStateDatabaseApplied,
		JournalStateCompleted, JournalStateFailed, JournalStateConflict:
		return true
	default:
		return false
	}
}

func (s JournalState) Terminal() bool {
	return s == JournalStateCompleted || s == JournalStateFailed || s == JournalStateConflict
}

// OperationJournal is persisted crash-recovery state. Payload is bounded
// operation metadata, never artifact content or a physical path.
type OperationJournal struct {
	ID               string          `json:"operationId"`
	Operation        AuditOperation  `json:"operation"`
	LibraryID        string          `json:"libraryId"`
	SetupID          string          `json:"setupId,omitempty"`
	ArtifactID       string          `json:"artifactId,omitempty"`
	ExpectedRevision Revision        `json:"expectedRevision,omitempty"`
	ResultRevision   Revision        `json:"resultRevision,omitempty"`
	State            JournalState    `json:"state"`
	ErrorCode        ErrorCode       `json:"errorCode,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}
