package domain

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable, machine-readable API error code.
type ErrorCode string

// The first block is the complete minimum set mandated by API-001. Additional
// validation codes make failures at the domain boundary equally stable.
const (
	CodeSetupNotFound        ErrorCode = "SETUP_NOT_FOUND"
	CodeArtifactNotFound     ErrorCode = "ARTIFACT_NOT_FOUND"
	CodeRevisionConflict     ErrorCode = "REVISION_CONFLICT"
	CodeArtifactChanged      ErrorCode = "ARTIFACT_CHANGED"
	CodeInvalidSetupState    ErrorCode = "INVALID_SETUP_STATE"
	CodeSetupNotReady        ErrorCode = "SETUP_NOT_READY"
	CodeCurrentSetupConflict ErrorCode = "CURRENT_SETUP_CONFLICT"
	CodeNameConflict         ErrorCode = "NAME_CONFLICT"
	CodeUnsupportedFileType  ErrorCode = "UNSUPPORTED_FILE_TYPE"
	CodeInvalidContent       ErrorCode = "INVALID_CONTENT"
	CodeFileTooLarge         ErrorCode = "FILE_TOO_LARGE"
	CodeImportTooLarge       ErrorCode = "IMPORT_TOO_LARGE"
	CodeInsufficientStorage  ErrorCode = "INSUFFICIENT_STORAGE"
	CodeStorageUnavailable   ErrorCode = "STORAGE_UNAVAILABLE"
	CodeUploadIncomplete     ErrorCode = "UPLOAD_INCOMPLETE"
	CodeJobCancelled         ErrorCode = "JOB_CANCELLED"
	CodeConfirmationExpired  ErrorCode = "CONFIRMATION_EXPIRED"
	CodeDatabaseUnavailable  ErrorCode = "DATABASE_UNAVAILABLE"

	CodeInvalidID       ErrorCode = "INVALID_ID"
	CodeInvalidName     ErrorCode = "INVALID_NAME"
	CodeInvalidRevision ErrorCode = "INVALID_REVISION"
)

var requiredErrorCodes = [...]ErrorCode{
	CodeSetupNotFound,
	CodeArtifactNotFound,
	CodeRevisionConflict,
	CodeArtifactChanged,
	CodeInvalidSetupState,
	CodeSetupNotReady,
	CodeCurrentSetupConflict,
	CodeNameConflict,
	CodeUnsupportedFileType,
	CodeInvalidContent,
	CodeFileTooLarge,
	CodeImportTooLarge,
	CodeInsufficientStorage,
	CodeStorageUnavailable,
	CodeUploadIncomplete,
	CodeJobCancelled,
	CodeConfirmationExpired,
	CodeDatabaseUnavailable,
}

// RequiredErrorCodes returns an independent copy of the requirements-mandated
// minimum. Callers cannot mutate the package's canonical list.
func RequiredErrorCodes() []ErrorCode {
	result := make([]ErrorCode, len(requiredErrorCodes))
	copy(result, requiredErrorCodes[:])
	return result
}

// Error is safe for serialization. Cause is retained for errors.Is/errors.As
// and diagnostics but is deliberately excluded from JSON.
type Error struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"retryable,omitempty"`
	Cause     error          `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

func WrapError(code ErrorCode, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

// ErrorCodeOf extracts the stable code through an arbitrary wrapping chain.
func ErrorCodeOf(err error) (ErrorCode, bool) {
	var coded *Error
	if !errors.As(err, &coded) || coded == nil {
		return "", false
	}
	return coded.Code, true
}

func IsErrorCode(err error, code ErrorCode) bool {
	actual, ok := ErrorCodeOf(err)
	return ok && actual == code
}
