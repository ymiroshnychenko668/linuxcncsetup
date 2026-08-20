package httpapi

import (
	"errors"
	"net/http"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func writeDomainError(w http.ResponseWriter, requestID string, err error) {
	var coded *domain.Error
	if !errors.As(err, &coded) || coded == nil {
		writeError(w, http.StatusInternalServerError, requestID, "INTERNAL_ERROR", "The request could not be completed.", nil, true)
		return
	}
	status, retryable := domainErrorStatus(coded.Code)
	details := any(coded.Details)
	if len(coded.Details) == 0 {
		details = nil
	}
	writeError(w, status, requestID, string(coded.Code), coded.Message, details, coded.Retryable || retryable)
}

func domainErrorStatus(code domain.ErrorCode) (int, bool) {
	switch code {
	case domain.CodeSetupNotFound, domain.CodeArtifactNotFound, domain.CodeJobNotFound,
		domain.CodeImportNotFound, domain.CodeValidationNotFound:
		return http.StatusNotFound, false
	case domain.CodeRevisionConflict, domain.CodeArtifactChanged, domain.CodeInvalidSetupState,
		domain.CodeSetupNotReady, domain.CodeCurrentSetupConflict, domain.CodeNameConflict,
		domain.CodeIdempotencyConflict, domain.CodeJobCancelled, domain.CodeUploadJobRequired:
		return http.StatusConflict, false
	case domain.CodeUnsupportedFileType:
		return http.StatusUnsupportedMediaType, false
	case domain.CodeFileTooLarge, domain.CodeImportTooLarge:
		return http.StatusRequestEntityTooLarge, false
	case domain.CodeInsufficientStorage:
		return http.StatusInsufficientStorage, true
	case domain.CodeStorageUnavailable, domain.CodeDatabaseUnavailable:
		return http.StatusServiceUnavailable, true
	case domain.CodeConfirmationExpired:
		return http.StatusGone, false
	case domain.CodeInvalidID, domain.CodeInvalidName, domain.CodeInvalidRevision,
		domain.CodeInvalidContent, domain.CodeUploadIncomplete, domain.CodeConfirmationInvalid,
		domain.CodeInvalidRange:
		return http.StatusBadRequest, false
	default:
		return http.StatusInternalServerError, true
	}
}
