package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRequiredErrorCodesAreCompleteUniqueAndStable(t *testing.T) {
	want := []ErrorCode{
		"SETUP_NOT_FOUND",
		"ARTIFACT_NOT_FOUND",
		"REVISION_CONFLICT",
		"ARTIFACT_CHANGED",
		"INVALID_SETUP_STATE",
		"SETUP_NOT_READY",
		"CURRENT_SETUP_CONFLICT",
		"NAME_CONFLICT",
		"UNSUPPORTED_FILE_TYPE",
		"INVALID_CONTENT",
		"FILE_TOO_LARGE",
		"IMPORT_TOO_LARGE",
		"INSUFFICIENT_STORAGE",
		"STORAGE_UNAVAILABLE",
		"UPLOAD_INCOMPLETE",
		"JOB_CANCELLED",
		"CONFIRMATION_EXPIRED",
		"DATABASE_UNAVAILABLE",
	}
	got := RequiredErrorCodes()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("required codes = %#v, want %#v", got, want)
	}
	seen := make(map[ErrorCode]struct{}, len(got))
	for _, code := range got {
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("duplicate error code %q", code)
		}
		seen[code] = struct{}{}
	}
	got[0] = "MUTATED"
	if RequiredErrorCodes()[0] != CodeSetupNotFound {
		t.Fatal("RequiredErrorCodes exposed mutable package state")
	}
}

func TestCodedErrorWrapAndExtraction(t *testing.T) {
	cause := errors.New("private database detail")
	coded := WrapError(CodeDatabaseUnavailable, "database is unavailable", cause)
	wrapped := fmt.Errorf("request failed: %w", coded)
	if !errors.Is(wrapped, cause) {
		t.Fatal("coded error did not retain its cause")
	}
	if code, ok := ErrorCodeOf(wrapped); !ok || code != CodeDatabaseUnavailable {
		t.Fatalf("ErrorCodeOf = %q, %v", code, ok)
	}
	if !IsErrorCode(wrapped, CodeDatabaseUnavailable) || IsErrorCode(wrapped, CodeStorageUnavailable) {
		t.Fatal("IsErrorCode returned an incorrect result")
	}
	if _, ok := ErrorCodeOf(errors.New("plain")); ok {
		t.Fatal("plain error was treated as coded")
	}
}

func TestCodedErrorJSONIsSafeAndCamelCase(t *testing.T) {
	coded := &Error{
		Code: CodeStorageUnavailable, Message: "storage is unavailable",
		RequestID: "request-1", Details: map[string]any{"safeId": "entity-1"},
		Retryable: true, Cause: errors.New("/private/storage/key: SQL failure"),
	}
	encoded, err := json.Marshal(coded)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"private", "storage/key", "SQL failure", "request_id", "Cause", "cause"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("serialized error leaked %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{`"code"`, `"message"`, `"requestId"`, `"details"`, `"retryable"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("serialized error is missing %s: %s", required, text)
		}
	}
}

func TestNilCodedErrorIsSafe(t *testing.T) {
	var coded *Error
	if coded.Error() != "<nil>" || coded.Unwrap() != nil {
		t.Fatalf("nil coded error behavior is unsafe: %q, %v", coded.Error(), coded.Unwrap())
	}
}
