//go:build linux

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func TestDeterministicPostClaimFailuresAreTerminalAndHashBound(t *testing.T) {
	t.Run("prepare upload job", func(t *testing.T) {
		h := newLifecycleTestHarness(t, nil)
		ctx := context.Background()
		missingID := mustNewSetupID(t)
		const key = "terminal-prepare-upload"
		input := PrepareUploadJobInput{
			Operation: UploadJobAddPrograms, ExpectedRevision: domain.InitialRevision,
			Items: []UploadJobItem{{DisplayName: "missing.ngc", Size: 8}}, IdempotencyKey: key,
		}
		call := func(value PrepareUploadJobInput) error {
			_, err := h.service.PrepareUploadJob(ctx, missingID, value)
			return err
		}
		assertStableFailure(t, call(input), domain.CodeSetupNotFound)
		assertStableFailure(t, call(input), domain.CodeSetupNotFound)
		changed := input
		changed.Items = []UploadJobItem{{DisplayName: "missing.ngc", Size: 9}}
		assertStableFailure(t, call(changed), domain.CodeIdempotencyConflict)
		assertTerminalClaim(t, h, key, domain.CodeSetupNotFound)
		assertTableCount(t, h, "jobs", 0)
	})

	t.Run("touch recent setup", func(t *testing.T) {
		h := newLifecycleTestHarness(t, nil)
		ctx := context.Background()
		missingID := mustNewSetupID(t)
		const key = "terminal-touch-recent"
		call := func(artifactID string) error {
			return h.service.TouchRecentSetup(ctx, missingID, artifactID, 0, key)
		}
		assertStableFailure(t, call(""), domain.CodeSetupNotFound)
		assertStableFailure(t, call(""), domain.CodeSetupNotFound)
		assertStableFailure(t, call(mustNewArtifactID(t)), domain.CodeIdempotencyConflict)
		assertTerminalClaim(t, h, key, domain.CodeSetupNotFound)
		assertTableCount(t, h, "recent_setups", 0)
	})

	t.Run("put UI state", func(t *testing.T) {
		h := newLifecycleTestHarness(t, nil)
		ctx := context.Background()
		const key = "terminal-put-ui-state"
		state := UIState{
			ClientID: "terminal-browser", Screen: "setup", SelectedSetupID: mustNewSetupID(t),
			Filters: json.RawMessage(`{"status":"attention"}`), View: json.RawMessage(`{"tab":"programs"}`),
		}
		call := func(value UIState) error {
			_, err := h.service.PutUIState(ctx, value, key)
			return err
		}
		assertStableFailure(t, call(state), domain.CodeSetupNotFound)
		assertStableFailure(t, call(state), domain.CodeSetupNotFound)
		changed := state
		changed.Screen = "library"
		assertStableFailure(t, call(changed), domain.CodeIdempotencyConflict)
		assertTerminalClaim(t, h, key, domain.CodeSetupNotFound)
		assertTableCount(t, h, "ui_state", 0)
	})

	t.Run("create delete plan", func(t *testing.T) {
		h := newLifecycleTestHarness(t, nil)
		ctx := context.Background()
		setup := h.createSetup(t, "Не архивирован", "terminal-delete-plan-setup")
		const key = "terminal-delete-plan"
		call := func(revision domain.Revision) error {
			_, err := h.service.CreateDeletePlan(ctx, setup.ID, revision, key)
			return err
		}
		assertStableFailure(t, call(setup.Revision), domain.CodeInvalidSetupState)
		assertStableFailure(t, call(setup.Revision), domain.CodeInvalidSetupState)
		assertStableFailure(t, call(setup.Revision+1), domain.CodeIdempotencyConflict)
		assertTerminalClaim(t, h, key, domain.CodeInvalidSetupState)
		assertTableCount(t, h, "delete_confirmations", 0)
	})

	t.Run("recent delete rolls back before terminal failure", func(t *testing.T) {
		h := newLifecycleTestHarness(t, nil)
		ctx := context.Background()
		first := h.createSetup(t, "Недавний", "terminal-recent-first")
		second := h.createSetup(t, "Другой", "terminal-recent-second")
		if err := h.service.TouchRecentSetup(ctx, first.ID, "", 0, "terminal-recent-seed"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.SQL().ExecContext(ctx, `
			CREATE TRIGGER terminal_recent_delete_failure
			BEFORE DELETE ON recent_setups
			BEGIN SELECT RAISE(ABORT, 'deterministic recent delete failure'); END`); err != nil {
			t.Fatal(err)
		}
		const key = "terminal-delete-recent"
		call := func(setupID string) error {
			return h.service.DeleteRecentSetup(ctx, setupID, key)
		}
		assertStableFailure(t, call(first.ID), domain.CodeDatabaseUnavailable)
		if _, err := h.db.SQL().ExecContext(ctx, `DROP TRIGGER terminal_recent_delete_failure`); err != nil {
			t.Fatal(err)
		}
		assertStableFailure(t, call(first.ID), domain.CodeDatabaseUnavailable)
		assertStableFailure(t, call(second.ID), domain.CodeIdempotencyConflict)
		assertTerminalClaim(t, h, key, domain.CodeDatabaseUnavailable)
		recent, err := h.service.ListRecentSetups(ctx)
		if err != nil || len(recent) != 1 || recent[0].SetupID != first.ID {
			t.Fatalf("failed delete changed recent state: %+v, %v", recent, err)
		}
	})

	t.Run("run missing upload job", func(t *testing.T) {
		h := newLifecycleTestHarness(t, nil)
		ctx := context.Background()
		jobID, err := domain.NewJobID()
		if err != nil {
			t.Fatal(err)
		}
		const callerKey = "terminal-run-upload"
		call := func() error {
			_, err := h.service.RunUploadJob(ctx, jobID, RunUploadJobInput{IdempotencyKey: callerKey})
			return err
		}
		assertStableFailure(t, call(), domain.CodeJobNotFound)
		assertStableFailure(t, call(), domain.CodeJobNotFound)
		operation := "runUploadJob:" + jobID
		scoped := sha256.Sum256([]byte(operation + "\x00" + callerKey))
		assertTerminalClaim(t, h, hex.EncodeToString(scoped[:]), domain.CodeJobNotFound)
	})
}

func assertStableFailure(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if !domain.IsErrorCode(err, code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func assertTerminalClaim(t *testing.T, h *lifecycleTestHarness, key string, code domain.ErrorCode) {
	t.Helper()
	var state string
	var persistedCode string
	if err := h.db.SQL().QueryRowContext(context.Background(), `
		SELECT state, error_code
		  FROM idempotency_requests
		 WHERE library_id = ? AND key = ?`, h.service.libraryID, key).Scan(&state, &persistedCode); err != nil {
		t.Fatal(err)
	}
	if (state != idempotencyStateFailed && state != idempotencyStateConflict) || persistedCode != string(code) {
		t.Fatalf("claim %q = state %q code %q, want terminal/%s", key, state, persistedCode, code)
	}
}

func assertTableCount(t *testing.T, h *lifecycleTestHarness, table string, expected int) {
	t.Helper()
	allowed := map[string]bool{
		"delete_confirmations": true,
		"jobs":                 true,
		"recent_setups":        true,
		"ui_state":             true,
	}
	if !allowed[table] {
		t.Fatalf("test attempted an unapproved table name %q", table)
	}
	var count int
	if err := h.db.SQL().QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("%s count = %d, want %d", table, count, expected)
	}
}

func mustNewSetupID(t *testing.T) string {
	t.Helper()
	id, err := domain.NewSetupID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustNewArtifactID(t *testing.T) string {
	t.Helper()
	id, err := domain.NewArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
