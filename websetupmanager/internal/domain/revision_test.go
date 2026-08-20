package domain

import (
	"math"
	"testing"
)

func TestRevisionValidationAndAdvance(t *testing.T) {
	if !InitialRevision.Valid() || Revision(0).Valid() || Revision(-1).Valid() {
		t.Fatal("revision validity is incorrect")
	}
	next, err := NextRevision(41)
	if err != nil || next != 42 {
		t.Fatalf("NextRevision = %d, %v", next, err)
	}
	for _, invalid := range []Revision{0, -1, Revision(math.MaxInt64)} {
		if _, err := NextRevision(invalid); !IsErrorCode(err, CodeInvalidRevision) {
			t.Errorf("NextRevision(%d) error = %v", invalid, err)
		}
	}
}

func TestExpectedRevisionConflictIsStableAndActionable(t *testing.T) {
	if err := CheckExpectedRevision(3, 3); err != nil {
		t.Fatal(err)
	}
	err := CheckExpectedRevision(4, 3)
	if !IsErrorCode(err, CodeRevisionConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	coded := err.(*Error)
	if coded.Details["expectedRevision"] != Revision(3) || coded.Details["actualRevision"] != Revision(4) {
		t.Fatalf("conflict details = %#v", coded.Details)
	}
	for _, revisions := range [][2]Revision{{0, 1}, {1, 0}, {-1, 1}} {
		if err := CheckExpectedRevision(revisions[0], revisions[1]); !IsErrorCode(err, CodeInvalidRevision) {
			t.Errorf("CheckExpectedRevision%v error = %v", revisions, err)
		}
	}
}

func TestNextMutationIncrementsAndPreservesAttention(t *testing.T) {
	for _, status := range []SetupStatus{SetupStatusDraft, SetupStatusReady, SetupStatusAttention} {
		t.Run(string(status), func(t *testing.T) {
			nextStatus, nextRevision, err := NextMutation(status, 7, 7)
			if err != nil {
				t.Fatal(err)
			}
			expectedStatus := SetupStatusDraft
			if status == SetupStatusAttention {
				expectedStatus = SetupStatusAttention
			}
			if nextStatus != expectedStatus || nextRevision != 8 {
				t.Fatalf("transition = %s/%d", nextStatus, nextRevision)
			}
		})
	}
	status, revision, err := NextMutation(SetupStatusReady, 8, 7)
	if !IsErrorCode(err, CodeRevisionConflict) || status != SetupStatusReady || revision != 8 {
		t.Fatalf("conflict changed setup: %s/%d, %v", status, revision, err)
	}
	for _, status := range []SetupStatus{SetupStatusArchived, "unknown"} {
		nextStatus, nextRevision, err := NextMutation(status, 5, 5)
		if !IsErrorCode(err, CodeInvalidSetupState) || nextStatus != status || nextRevision != 5 {
			t.Fatalf("invalid state transition = %s/%d, %v", nextStatus, nextRevision, err)
		}
	}
}

func TestCompleteValidationIsRevisionBound(t *testing.T) {
	status, err := CompleteValidation(SetupStatusDraft, 4, 4, true)
	if err != nil || status != SetupStatusReady {
		t.Fatalf("passed validation = %s, %v", status, err)
	}
	status, err = CompleteValidation(SetupStatusReady, 4, 4, false)
	if err != nil || status != SetupStatusDraft {
		t.Fatalf("failed validation = %s, %v", status, err)
	}
	status, err = CompleteValidation(SetupStatusAttention, 4, 4, false)
	if err != nil || status != SetupStatusAttention {
		t.Fatalf("attention validation = %s, %v", status, err)
	}
	status, err = CompleteValidation(SetupStatusDraft, 5, 4, true)
	if !IsErrorCode(err, CodeRevisionConflict) || status != SetupStatusDraft {
		t.Fatalf("stale validation = %s, %v", status, err)
	}
	status, err = CompleteValidation(SetupStatusArchived, 4, 4, true)
	if !IsErrorCode(err, CodeInvalidSetupState) || status != SetupStatusArchived {
		t.Fatalf("archived validation = %s, %v", status, err)
	}
}

func TestAttentionArchiveAndRestoreTransitions(t *testing.T) {
	for _, initial := range []SetupStatus{SetupStatusDraft, SetupStatusReady, SetupStatusAttention} {
		attention, err := MarkAttention(initial)
		if err != nil || attention != SetupStatusAttention {
			t.Fatalf("MarkAttention(%s) = %s, %v", initial, attention, err)
		}
		archived, previous, err := ArchiveStatus(initial)
		if err != nil || archived != SetupStatusArchived || previous != initial {
			t.Fatalf("ArchiveStatus(%s) = %s/%s, %v", initial, archived, previous, err)
		}
		restored, err := RestoreStatus(archived, previous, false)
		if err != nil || restored != initial {
			t.Fatalf("RestoreStatus(%s) = %s, %v", initial, restored, err)
		}
		restored, err = RestoreStatus(archived, previous, true)
		if err != nil || restored != SetupStatusAttention {
			t.Fatalf("changed RestoreStatus(%s) = %s, %v", initial, restored, err)
		}
	}
	status, err := MarkAttention(SetupStatusArchived)
	if err != nil || status != SetupStatusArchived {
		t.Fatalf("archived MarkAttention = %s, %v", status, err)
	}
	if _, _, err := ArchiveStatus(SetupStatusArchived); !IsErrorCode(err, CodeInvalidSetupState) {
		t.Fatalf("double archive error = %v", err)
	}
	for _, test := range []struct {
		status SetupStatus
		from   SetupStatus
	}{
		{SetupStatusDraft, SetupStatusReady},
		{SetupStatusArchived, SetupStatusArchived},
		{SetupStatusArchived, "unknown"},
	} {
		if _, err := RestoreStatus(test.status, test.from, false); !IsErrorCode(err, CodeInvalidSetupState) {
			t.Errorf("RestoreStatus(%s, %s) error = %v", test.status, test.from, err)
		}
	}
}
