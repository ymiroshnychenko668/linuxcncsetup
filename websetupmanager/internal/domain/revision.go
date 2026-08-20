package domain

import "math"

// Revision is monotonic and bounded by SQLite's signed INTEGER range.
type Revision int64

const InitialRevision Revision = 1

func (r Revision) Valid() bool {
	return r >= InitialRevision
}

func NextRevision(current Revision) (Revision, error) {
	if !current.Valid() || current == Revision(math.MaxInt64) {
		return 0, NewError(CodeInvalidRevision, "revision cannot be advanced")
	}
	return current + 1, nil
}

// CheckExpectedRevision implements optimistic concurrency without exposing
// any internal database state.
func CheckExpectedRevision(actual, expected Revision) error {
	if !actual.Valid() || !expected.Valid() {
		return NewError(CodeInvalidRevision, "revision is not valid")
	}
	if actual != expected {
		return &Error{
			Code:    CodeRevisionConflict,
			Message: "setup revision has changed",
			Details: map[string]any{
				"expectedRevision": expected,
				"actualRevision":   actual,
			},
		}
	}
	return nil
}

// NextMutation returns the state and revision produced by an atomic successful
// metadata or composition mutation. Ready setups become draft and must be
// validated again. Attention is sticky across user mutations: only a storage
// verification boundary may prove that its external cause has been repaired.
// Archived setups cannot be mutated.
func NextMutation(status SetupStatus, actual, expected Revision) (SetupStatus, Revision, error) {
	if err := CheckExpectedRevision(actual, expected); err != nil {
		return status, actual, err
	}
	if !status.Valid() || status == SetupStatusArchived {
		return status, actual, NewError(CodeInvalidSetupState, "setup cannot be changed in its current state")
	}
	next, err := NextRevision(actual)
	if err != nil {
		return status, actual, err
	}
	if status == SetupStatusAttention {
		return SetupStatusAttention, next, nil
	}
	return SetupStatusDraft, next, nil
}

// CompleteValidation applies a result only to the exact revision that was
// validated. Validation changes status, not the composition revision.
func CompleteValidation(
	status SetupStatus,
	actual Revision,
	validated Revision,
	passed bool,
) (SetupStatus, error) {
	if err := CheckExpectedRevision(actual, validated); err != nil {
		return status, err
	}
	if !status.Valid() || status == SetupStatusArchived {
		return status, NewError(CodeInvalidSetupState, "setup cannot be validated in its current state")
	}
	if passed {
		return SetupStatusReady, nil
	}
	if status == SetupStatusAttention {
		return SetupStatusAttention, nil
	}
	return SetupStatusDraft, nil
}

// MarkAttention records external disappearance, replacement, or corruption.
// Archived setups retain their archived state and will reconcile on restore.
func MarkAttention(status SetupStatus) (SetupStatus, error) {
	if !status.Valid() {
		return status, NewError(CodeInvalidSetupState, "setup state is not valid")
	}
	if status == SetupStatusArchived {
		return status, nil
	}
	return SetupStatusAttention, nil
}

// ArchiveStatus preserves the previous state for a later safe restore.
func ArchiveStatus(status SetupStatus) (SetupStatus, SetupStatus, error) {
	if !status.Valid() || status == SetupStatusArchived {
		return status, "", NewError(CodeInvalidSetupState, "setup cannot be archived in its current state")
	}
	return SetupStatusArchived, status, nil
}

// RestoreStatus returns the preserved state, or attention when reconciliation
// detects an external content change while the setup was archived.
func RestoreStatus(status, archivedFrom SetupStatus, contentChanged bool) (SetupStatus, error) {
	if status != SetupStatusArchived || !archivedFrom.Valid() || archivedFrom == SetupStatusArchived {
		return status, NewError(CodeInvalidSetupState, "setup cannot be restored in its current state")
	}
	if contentChanged {
		return SetupStatusAttention, nil
	}
	return archivedFrom, nil
}
