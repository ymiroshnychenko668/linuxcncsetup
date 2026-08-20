package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestP0EnumValuesAndValidity(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		valid  func(string) bool
	}{
		{"setup status", []string{"draft", "ready", "attention", "archived"}, func(v string) bool { return SetupStatus(v).Valid() }},
		{"setup source", []string{"created", "imported", "duplicated"}, func(v string) bool { return SetupSource(v).Valid() }},
		{"artifact role", []string{"program", "setup_sheet"}, func(v string) bool { return ArtifactRole(v).Valid() }},
		{"artifact state", []string{"available", "missing", "changed", "corrupt", "unavailable"}, func(v string) bool { return ArtifactState(v).Valid() }},
		{"validation state", []string{"queued", "running", "succeeded", "failed", "cancelled", "conflict"}, func(v string) bool { return ValidationState(v).Valid() }},
		{"validation severity", []string{"error", "warning"}, func(v string) bool { return ValidationSeverity(v).Valid() }},
		{"job kind", []string{"import", "addPrograms", "replaceProgram", "validate", "duplicate", "permanentDelete", "gcodeSearch", "reconcile"}, func(v string) bool { return JobKind(v).Valid() }},
		{"job state", []string{"queued", "running", "cancelling", "succeeded", "failed", "cancelled", "conflict"}, func(v string) bool { return JobState(v).Valid() }},
		{"import state", []string{"staging", "committing", "succeeded", "draft_saved", "failed", "cancelled", "conflict"}, func(v string) bool { return ImportState(v).Valid() }},
		{"import artifact state", []string{"pending", "uploading", "staged", "excluded", "published", "failed"}, func(v string) bool { return ImportArtifactState(v).Valid() }},
		{"audit result", []string{"succeeded", "failed", "cancelled", "conflict"}, func(v string) bool { return AuditResult(v).Valid() }},
		{"journal state", []string{"intent", "storage_applied", "db_applied", "completed", "failed", "conflict"}, func(v string) bool { return JournalState(v).Valid() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, value := range test.values {
				if !test.valid(value) {
					t.Errorf("documented value %q is invalid", value)
				}
			}
			for _, invalid := range []string{"", "unknown", "READY", "../ready"} {
				if test.valid(invalid) {
					t.Errorf("unknown value %q is valid", invalid)
				}
			}
		})
	}
	operations := []AuditOperation{
		AuditOperationCreate, AuditOperationImport, AuditOperationValidate,
		AuditOperationSelectCurrent, AuditOperationClearCurrent, AuditOperationAddPrograms,
		AuditOperationReplaceProgram, AuditOperationRenameProgram, AuditOperationSetPrimary,
		AuditOperationDeleteProgram, AuditOperationSetupSheet, AuditOperationDuplicate,
		AuditOperationArchive, AuditOperationRestore, AuditOperationPermanentDelete,
	}
	for _, operation := range operations {
		if !operation.Valid() {
			t.Errorf("documented audit operation %q is invalid", operation)
		}
	}
	if AuditOperation("unknown").Valid() {
		t.Fatal("unknown audit operation is valid")
	}
}

func TestTerminalStatesAreStable(t *testing.T) {
	for _, state := range []JobState{JobStateQueued, JobStateRunning, JobStateCancelling} {
		if state.Terminal() {
			t.Errorf("job state %q is terminal", state)
		}
	}
	for _, state := range []JobState{JobStateSucceeded, JobStateFailed, JobStateCancelled, JobStateConflict} {
		if !state.Terminal() {
			t.Errorf("job state %q is not terminal", state)
		}
	}
	for _, state := range []ValidationState{ValidationStateQueued, ValidationStateRunning} {
		if state.Terminal() {
			t.Errorf("validation state %q is terminal", state)
		}
	}
	for _, state := range []ValidationState{ValidationStateSucceeded, ValidationStateFailed, ValidationStateCancelled, ValidationStateConflict} {
		if !state.Terminal() {
			t.Errorf("validation state %q is not terminal", state)
		}
	}
	for _, state := range []ImportState{ImportStateStaging, ImportStateCommitting} {
		if state.Terminal() {
			t.Errorf("import state %q is terminal", state)
		}
	}
	for _, state := range []ImportState{ImportStateSucceeded, ImportStateDraftSaved, ImportStateFailed, ImportStateCancelled, ImportStateConflict} {
		if !state.Terminal() {
			t.Errorf("import state %q is not terminal", state)
		}
	}
	for _, state := range []JournalState{JournalStateIntent, JournalStateStorageApplied, JournalStateDatabaseApplied} {
		if state.Terminal() {
			t.Errorf("journal state %q is terminal", state)
		}
	}
	for _, state := range []JournalState{JournalStateCompleted, JournalStateFailed, JournalStateConflict} {
		if !state.Terminal() {
			t.Errorf("journal state %q is not terminal", state)
		}
	}
}

func TestAllDomainJSONFieldsUseCamelCase(t *testing.T) {
	types := []any{
		Setup{}, SetupSummary{}, Artifact{}, StorageObject{}, ValidationIssue{}, ValidationRun{},
		CurrentSetup{}, RecentSetup{}, JobProgress{}, Job{}, ImportArtifact{}, ImportSession{},
		AuditEvent{}, OperationJournal{}, Error{}, GCodeContentInfo{},
	}
	for _, value := range types {
		typeOf := reflect.TypeOf(value)
		t.Run(typeOf.Name(), func(t *testing.T) {
			for index := range typeOf.NumField() {
				field := typeOf.Field(index)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					t.Errorf("field %s has no explicit JSON name", field.Name)
					continue
				}
				first, _ := firstRune(name)
				if strings.Contains(name, "_") || !unicode.IsLower(first) {
					t.Errorf("field %s JSON name %q is not camelCase", field.Name, name)
				}
			}
		})
	}
}

func TestInternalStorageAndIdempotencyValuesNeverMarshal(t *testing.T) {
	now := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	values := []any{
		Artifact{ID: "artifact-safe", SHA256: "artifact-sha-secret", StorageObjectID: "storage-secret", CreatedAt: now, UpdatedAt: now},
		StorageObject{ID: "object-secret", StorageKey: "/absolute/private/storage-key", SHA256: "object-sha-secret", CreatedAt: now},
		ImportSession{ID: "import-safe", IdempotencyKey: "idempotency-secret", CreatedAt: now, UpdatedAt: now},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		text := string(encoded)
		for _, forbidden := range []string{
			"storage-secret", "artifact-sha-secret", "object-secret", "object-sha-secret",
			"/absolute/private", "storage-key", "idempotency-secret", "StorageKey", "storageKey",
			"storageObjectId", "sha256", "idempotencyKey",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%T leaked %q: %s", value, forbidden, text)
			}
		}
	}
}

func TestSetupDTOUsesStableEntityIDsAndCamelCase(t *testing.T) {
	now := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	setup := Setup{
		ID: "setup-id", LibraryID: "library-id", Name: "Part", Status: SetupStatusDraft,
		Revision: 1, Source: SetupSourceCreated, Artifacts: []Artifact{{
			ID: "artifact-id", SetupID: "setup-id", Role: ArtifactRoleProgram,
			DisplayName: "main.ngc", State: ArtifactStateAvailable,
		}}, CreatedAt: now, UpdatedAt: now,
	}
	encoded, err := json.Marshal(setup)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{`"setupId":"setup-id"`, `"libraryId":"library-id"`, `"artifactId":"artifact-id"`, `"displayName":"main.ngc"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("setup JSON lacks %s: %s", expected, text)
		}
	}
	for _, forbidden := range []string{"setup_id", "artifact_id", "library_id", "display_name"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("setup JSON contains snake_case %q: %s", forbidden, text)
		}
	}
}

func firstRune(value string) (rune, int) {
	for _, r := range value {
		return r, 1
	}
	return 0, 0
}
