//go:build linux

package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"golang.org/x/sys/unix"
)

func TestIdempotencyClaimReplayConflictAndExpiry(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) {
		options.IdempotencyTTL = 2 * time.Second
	})
	ctx := context.Background()
	hash, err := idempotencyRequestHash("testOperation", struct {
		Name string `json:"name"`
	}{"same"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := h.service.claimIdempotency(ctx, "test-idempotency", "testOperation", hash)
	if err != nil || claim.Replayed {
		t.Fatalf("initial claim = %+v, %v", claim, err)
	}
	_, err = h.service.claimIdempotency(ctx, "test-idempotency", "testOperation", hash)
	var coded *domain.Error
	if !errors.As(err, &coded) || coded.Code != domain.CodeIdempotencyConflict || !coded.Retryable {
		t.Fatalf("in-progress replay error = %#v", err)
	}

	type response struct {
		ID string `json:"id"`
	}
	want := response{ID: "stable-result"}
	if err := h.service.finishIdempotency(ctx, claim, 201, want, nil); err != nil {
		t.Fatal(err)
	}
	replay, err := h.service.claimIdempotency(ctx, "test-idempotency", "testOperation", hash)
	if err != nil || !replay.Replayed || replay.ResponseStatus != 201 {
		t.Fatalf("terminal replay = %+v, %v", replay, err)
	}
	var got response
	if replayed, err := replay.replayInto(&got); !replayed || err != nil || got != want {
		t.Fatalf("replayInto = %v, %+v, %v", replayed, got, err)
	}
	differentHash, err := idempotencyRequestHash("testOperation", struct {
		Name string `json:"name"`
	}{"different"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.claimIdempotency(ctx, "test-idempotency", "testOperation", differentHash); !domain.IsErrorCode(err, domain.CodeIdempotencyConflict) {
		t.Fatalf("different payload error = %v", err)
	}

	h.clockNS.Add(int64(3 * time.Second))
	reclaimed, err := h.service.claimIdempotency(ctx, "test-idempotency", "testOperation", differentHash)
	if err != nil || reclaimed.Replayed {
		t.Fatalf("expired claim was not reclaimed: %+v, %v", reclaimed, err)
	}
	terminalErr := domain.NewError(domain.CodeRevisionConflict, "revision changed")
	if err := h.service.finishIdempotency(ctx, reclaimed, 0, nil, terminalErr); err != nil {
		t.Fatal(err)
	}
	failedReplay, err := h.service.claimIdempotency(ctx, "test-idempotency", "testOperation", differentHash)
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := failedReplay.replayInto(nil); !replayed || !domain.IsErrorCode(err, domain.CodeRevisionConflict) {
		t.Fatalf("failed replay = %v, %v", replayed, err)
	}
	var storedHash, resultJSON string
	if err := h.db.SQL().QueryRowContext(ctx, `
		SELECT request_hash, result_json FROM idempotency_requests
		 WHERE library_id = ? AND key = ?`, h.service.libraryID, "test-idempotency").
		Scan(&storedHash, &resultJSON); err != nil {
		t.Fatal(err)
	}
	if len(storedHash) != 64 || !json.Valid([]byte(resultJSON)) || bytes.Contains([]byte(resultJSON), []byte("revision changed")) {
		t.Fatalf("unsafe idempotency persistence: hash=%q result=%q", storedHash, resultJSON)
	}
}

func TestPersistentJobsProgressCancellationAndTerminalStability(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.MaxParallelHeavyJobs = 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := h.db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	total := int64(100)
	job, err := h.service.insertJobTx(ctx, tx, domain.JobKindReconcile, "", "", &total)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	h.service.launchJob(job.ID, func(jobCtx context.Context, progress func(domain.JobProgress) error) (any, error) {
		close(started)
		if err := progress(domain.JobProgress{CompletedBytes: 25, TotalBytes: 100}); err != nil {
			return nil, err
		}
		<-jobCtx.Done()
		return nil, jobCtx.Err()
	})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelling, err := h.service.CancelJob(ctx, job.ID, "cancel-running-job")
	if err != nil || (cancelling.State != domain.JobStateCancelling && cancelling.State != domain.JobStateCancelled) {
		t.Fatalf("CancelJob = %+v, %v", cancelling, err)
	}
	terminal, err := h.service.waitForJob(ctx, job.ID)
	if err != nil || terminal.State != domain.JobStateCancelled || terminal.ErrorCode != domain.CodeJobCancelled || terminal.Progress.CompletedBytes != 25 {
		t.Fatalf("cancelled terminal = %+v, %v", terminal, err)
	}
	repeated, err := h.service.CancelJob(ctx, job.ID, "cancel-running-job")
	if err != nil || repeated.State != cancelling.State || string(repeated.Result) != string(cancelling.Result) {
		t.Fatalf("terminal cancellation replay = %+v, %v", repeated, err)
	}

	tx, err = h.db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	success, err := h.service.insertJobTx(ctx, tx, domain.JobKindReconcile, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h.service.launchJob(success.ID, func(context.Context, func(domain.JobProgress) error) (any, error) {
		return map[string]string{"result": "stable"}, nil
	})
	succeeded, err := h.service.waitForJob(ctx, success.ID)
	if err != nil || succeeded.State != domain.JobStateSucceeded || string(succeeded.Result) != `{"result":"stable"}` {
		t.Fatalf("succeeded job = %+v, %v", succeeded, err)
	}
	afterCancel, err := h.service.CancelJob(ctx, success.ID, "cancel-success-job")
	if err != nil || afterCancel.State != domain.JobStateSucceeded || string(afterCancel.Result) != string(succeeded.Result) {
		t.Fatalf("cancel after terminal = %+v, %v", afterCancel, err)
	}
	jobs, err := h.service.ListJobs(ctx, 10)
	if err != nil || len(jobs) != 2 || jobs[0].ID != success.ID {
		t.Fatalf("ListJobs = %+v, %v", jobs, err)
	}
	if _, err := h.service.GetJob(ctx, mustID(t)); !domain.IsErrorCode(err, domain.CodeJobNotFound) {
		t.Fatalf("missing job error = %v", err)
	}
	importSession, err := h.service.StartImport(ctx, StartImportInput{
		Name: "Cancelable import", IdempotencyKey: "jobs-cancel-import",
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = h.service.ListJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var importJob *domain.Job
	for index := range jobs {
		if jobs[index].Kind == domain.JobKindImport {
			importJob = &jobs[index]
			break
		}
	}
	if importJob == nil {
		t.Fatal("persistent import job was not listed")
	}
	cancelledImportJob, err := h.service.CancelJob(ctx, importJob.ID, "cancel-import-job")
	if err != nil || cancelledImportJob.State != domain.JobStateCancelled {
		t.Fatalf("CancelJob(import) = %+v, %v", cancelledImportJob, err)
	}
	cancelledImport, err := h.service.GetImport(ctx, importSession.ID)
	if err != nil || cancelledImport.State != domain.ImportStateCancelled {
		t.Fatalf("cancelled import session = %+v, %v", cancelledImport, err)
	}

	tx, err = h.db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := h.service.insertJobTx(ctx, tx, domain.JobKindDuplicate, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := h.service.GetJob(ctx, interrupted.ID)
	if err != nil || recovered.State != domain.JobStateFailed || recovered.ErrorCode != "PROCESS_INTERRUPTED" {
		t.Fatalf("recovered persistent job = %+v, %v", recovered, err)
	}
	afterRecoveryCancel, err := h.service.CancelJob(ctx, interrupted.ID, "cancel-recovered-job")
	if err != nil || afterRecoveryCancel.State != domain.JobStateFailed || afterRecoveryCancel.ErrorCode != recovered.ErrorCode {
		t.Fatalf("recovered terminal stability = %+v, %v", afterRecoveryCancel, err)
	}
}

func TestSetupAllowsOnlyOneActivePersistentJob(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Single active job", "single-active-create")

	tx, err := h.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.service.insertJobTx(ctx, tx, domain.JobKindValidate, setup.ID, "", nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = h.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.insertJobTx(ctx, tx, domain.JobKindDuplicate, setup.ID, "", nil)
	_ = tx.Rollback()
	if !domain.IsErrorCode(err, domain.CodeInvalidSetupState) {
		t.Fatalf("second active job error = %v", err)
	}

	if err := h.service.finishJob(ctx, first.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	tx, err = h.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.insertJobTx(ctx, tx, domain.JobKindDuplicate, setup.ID, "", nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("terminal job prevented a new setup operation")
	}
}

func TestServiceShutdownWaitsForJobTerminalization(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := h.db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.service.insertJobTx(ctx, tx, domain.JobKindReconcile, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	h.service.launchJob(job.ID, func(jobCtx context.Context, _ func(domain.JobProgress) error) (any, error) {
		close(started)
		<-jobCtx.Done()
		return nil, jobCtx.Err()
	})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	h.service.Close()
	if err := h.service.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	terminal, err := h.service.GetJob(ctx, job.ID)
	if err != nil || terminal.State != domain.JobStateCancelled || terminal.CompletedAt == nil {
		t.Fatalf("shutdown terminal job = %+v, %v", terminal, err)
	}
}

func TestServiceShutdownTerminalizesIdleStagingImport(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Idle staging", IdempotencyKey: "idle-staging-start"})
	if err != nil || session.JobID == "" {
		t.Fatalf("StartImport = %+v, %v", session, err)
	}
	h.service.Close()
	if err := h.service.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	terminal, err := h.service.GetImport(ctx, session.ID)
	if err != nil || terminal.State != domain.ImportStateCancelled {
		t.Fatalf("terminal import = %+v, %v", terminal, err)
	}
	job, err := h.service.GetJob(ctx, session.JobID)
	if err != nil || job.State != domain.JobStateCancelled {
		t.Fatalf("terminal import job = %+v, %v", job, err)
	}
}

func TestValidationReadyRequiredSheetExternalChangeAndStaleRevision(t *testing.T) {
	t.Run("ready and external change", func(t *testing.T) {
		h := newLifecycleTestHarness(t, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		setup := h.createSetup(t, "Проверка", "validation-create")
		_, object, _ := h.attachProgram(t, setup.ID, "main.ngc", []byte("G0 X0\nM2\n"), true)
		job, err := h.service.ValidateSetup(ctx, setup.ID, ValidateInput{
			ExpectedRevision: setup.Revision, IdempotencyKey: "validation-ready",
		})
		if err != nil {
			t.Fatal(err)
		}
		if job.Progress.TotalBytes != object.Size*2 {
			t.Fatalf("validation total bytes = %d, want %d", job.Progress.TotalBytes, object.Size*2)
		}
		var queued validationResultJSON
		if err := json.Unmarshal(job.Result, &queued); err != nil || queued.ValidationRunID == "" {
			t.Fatalf("queued validation result = %q, %v", job.Result, err)
		}
		terminal, err := h.service.waitForJob(ctx, job.ID)
		if err != nil || terminal.State != domain.JobStateSucceeded {
			t.Fatalf("validation job = %+v, %v", terminal, err)
		}
		if terminal.Progress.CompletedBytes != terminal.Progress.TotalBytes {
			t.Fatalf("validation progress = %+v", terminal.Progress)
		}
		run, err := h.service.GetValidationRun(ctx, setup.ID, queued.ValidationRunID)
		if err != nil || run.State != domain.ValidationStateSucceeded || len(run.Issues) != 0 {
			t.Fatalf("validation run = %+v, %v", run, err)
		}
		ready, err := h.service.GetSetup(ctx, setup.ID)
		if err != nil || ready.Status != domain.SetupStatusReady || ready.Revision != setup.Revision {
			t.Fatalf("ready setup = %+v, %v", ready, err)
		}
		replay, err := h.service.ValidateSetup(ctx, setup.ID, ValidateInput{
			ExpectedRevision: setup.Revision, IdempotencyKey: "validation-ready",
		})
		if err != nil || replay.ID != job.ID || replay.State != domain.JobStateSucceeded {
			t.Fatalf("validation replay = %+v, %v", replay, err)
		}

		file, err := h.roots.OpenLibrary(object.Key, unix.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte("M"), 0); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		changedJob, err := h.service.ValidateSetup(ctx, setup.ID, ValidateInput{
			ExpectedRevision: setup.Revision, IdempotencyKey: "validation-changed",
		})
		if err != nil {
			t.Fatal(err)
		}
		changedTerminal, err := h.service.waitForJob(ctx, changedJob.ID)
		if err != nil || changedTerminal.State != domain.JobStateSucceeded {
			t.Fatalf("changed validation job = %+v, %v", changedTerminal, err)
		}
		attention, err := h.service.GetSetup(ctx, setup.ID)
		if err != nil || attention.Status != domain.SetupStatusAttention {
			t.Fatalf("attention setup = %+v, %v", attention, err)
		}
	})

	t.Run("required sheet blocks ready", func(t *testing.T) {
		h := newLifecycleTestHarness(t, func(options *Options) { options.RequireSetupSheetForReady = true })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		setup := h.createSetup(t, "Нужен документ", "required-sheet-create")
		h.attachProgram(t, setup.ID, "main.ngc", []byte("G1 X1\n"), true)
		job, err := h.service.ValidateSetup(ctx, setup.ID, ValidateInput{
			ExpectedRevision: setup.Revision, IdempotencyKey: "required-sheet-validation",
		})
		if err != nil {
			t.Fatal(err)
		}
		terminal, err := h.service.waitForJob(ctx, job.ID)
		if err != nil || terminal.State != domain.JobStateSucceeded {
			t.Fatalf("required-sheet job = %+v, %v", terminal, err)
		}
		var run domain.ValidationRun
		if err := json.Unmarshal(terminal.Result, &run); err != nil || run.State != domain.ValidationStateFailed || !hasValidationIssue(run.Issues, "MISSING_SETUP_SHEET") {
			t.Fatalf("required-sheet run = %+v, %v", run, err)
		}
		got, _ := h.service.GetSetup(ctx, setup.ID)
		if got.Status != domain.SetupStatusDraft {
			t.Fatalf("required-sheet setup status = %s", got.Status)
		}
	})

	t.Run("stale queued validation conflicts", func(t *testing.T) {
		h := newLifecycleTestHarness(t, func(options *Options) { options.MaxParallelHeavyJobs = 1 })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		blocker, release := startBlockingTestJob(t, h, ctx)
		setup := h.createSetup(t, "Устаревшая проверка", "stale-validation-create")
		h.attachProgram(t, setup.ID, "main.ngc", []byte("G1 X1\n"), true)
		job, err := h.service.ValidateSetup(ctx, setup.ID, ValidateInput{
			ExpectedRevision: setup.Revision, IdempotencyKey: "stale-validation",
		})
		if err != nil {
			t.Fatal(err)
		}
		updated, err := h.service.UpdateSetup(ctx, setup.ID, UpdateSetupInput{
			ExpectedRevision: setup.Revision, Name: "Изменено", IdempotencyKey: "stale-validation-mutation",
		})
		if err != nil || updated.Revision == setup.Revision {
			t.Fatalf("concurrent mutation = %+v, %v", updated, err)
		}
		close(release)
		if _, err := h.service.waitForJob(ctx, blocker.ID); err != nil {
			t.Fatal(err)
		}
		terminal, err := h.service.waitForJob(ctx, job.ID)
		if err != nil || terminal.State != domain.JobStateConflict || terminal.ErrorCode != domain.CodeRevisionConflict {
			t.Fatalf("stale validation terminal = %+v, %v", terminal, err)
		}
	})

	t.Run("queued validation cancellation is terminal", func(t *testing.T) {
		h := newLifecycleTestHarness(t, func(options *Options) { options.MaxParallelHeavyJobs = 1 })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		blocker, release := startBlockingTestJob(t, h, ctx)
		setup := h.createSetup(t, "Отмена проверки", "cancel-validation-create")
		h.attachProgram(t, setup.ID, "main.ngc", []byte("G1 X1\n"), true)
		job, err := h.service.ValidateSetup(ctx, setup.ID, ValidateInput{
			ExpectedRevision: setup.Revision, IdempotencyKey: "cancel-validation",
		})
		if err != nil {
			t.Fatal(err)
		}
		var linked validationResultJSON
		if err := json.Unmarshal(job.Result, &linked); err != nil || linked.ValidationRunID == "" {
			t.Fatalf("queued validation link = %q, %v", job.Result, err)
		}
		if _, err := h.service.CancelJob(ctx, job.ID, "cancel-validation-job"); err != nil {
			t.Fatal(err)
		}
		close(release)
		if _, err := h.service.waitForJob(ctx, blocker.ID); err != nil {
			t.Fatal(err)
		}
		terminal, err := h.service.waitForJob(ctx, job.ID)
		if err != nil || terminal.State != domain.JobStateCancelled {
			t.Fatalf("cancelled validation job = %+v, %v", terminal, err)
		}
		run, err := h.service.GetValidationRun(ctx, setup.ID, linked.ValidationRunID)
		if err != nil || run.State != domain.ValidationStateCancelled {
			t.Fatalf("cancelled validation run = %+v, %v", run, err)
		}
	})
}

func TestDuplicateUsesNewEntityIDsAndIndependentMutations(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := h.createSetup(t, "Источник", "duplicate-source")
	sourceArtifactID, sourceObject, objectID := h.attachProgram(t, source.ID, "main.ngc", []byte("G0 X0\n"), true)
	h.markReady(t, source.ID, source.Revision)
	input := DuplicateInput{ExpectedRevision: source.Revision, Name: "Копия", IdempotencyKey: "duplicate-job"}
	job, err := h.service.DuplicateSetup(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if job.Progress.TotalBytes != sourceObject.Size {
		t.Fatalf("duplicate total bytes = %d, want %d", job.Progress.TotalBytes, sourceObject.Size)
	}
	terminal, err := h.service.waitForJob(ctx, job.ID)
	if err != nil || terminal.State != domain.JobStateSucceeded {
		t.Fatalf("duplicate job = %+v, %v", terminal, err)
	}
	if terminal.Progress.CompletedBytes != terminal.Progress.TotalBytes {
		t.Fatalf("duplicate progress = %+v", terminal.Progress)
	}
	var duplicate domain.Setup
	if err := json.Unmarshal(terminal.Result, &duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ID == source.ID || duplicate.Status != domain.SetupStatusDraft || duplicate.Source != domain.SetupSourceDuplicated ||
		duplicate.SourceSetupID != source.ID || len(duplicate.Artifacts) != 1 || duplicate.Artifacts[0].ID == sourceArtifactID {
		t.Fatalf("duplicate aggregate = %+v", duplicate)
	}
	var refs int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT ref_count FROM storage_objects WHERE id = ?", objectID).Scan(&refs); err != nil || refs != 2 {
		t.Fatalf("shared immutable refs = %d, %v", refs, err)
	}
	replay, err := h.service.DuplicateSetup(ctx, source.ID, input)
	if err != nil || replay.ID != job.ID || replay.State != domain.JobStateSucceeded {
		t.Fatalf("duplicate replay = %+v, %v", replay, err)
	}

	replacement := []byte("G1 X99\n")
	mutated, err := h.service.ReplaceArtifact(ctx, duplicate.ID, duplicate.Artifacts[0].ID, ReplaceArtifactInput{
		ExpectedRevision: duplicate.Revision, ExpectedVersion: duplicate.Artifacts[0].Version,
		DisplayName: duplicate.Artifacts[0].DisplayName, Content: bytes.NewReader(replacement),
		ExpectedSize: int64(len(replacement)), IdempotencyKey: "duplicate-replace",
	})
	if err != nil || mutated.Artifacts[0].ID != duplicate.Artifacts[0].ID {
		t.Fatalf("mutate duplicate = %+v, %v", mutated, err)
	}
	sourceAfter, err := h.service.GetSetup(ctx, source.ID)
	if err != nil || sourceAfter.Artifacts[0].Version != sourceObject.Version || sourceAfter.Artifacts[0].ID != sourceArtifactID {
		t.Fatalf("source after duplicate mutation = %+v, %v", sourceAfter, err)
	}
}

func TestReconcileGarbageCollectionAndExpiredCleanup(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) {
		options.IdempotencyTTL = time.Second
		options.DeleteConfirmationTTL = time.Second
	})
	ctx := context.Background()
	changedSetup := h.createSetup(t, "Внешнее изменение", "reconcile-changed")
	changedArtifactID, _, _ := h.attachProgram(t, changedSetup.ID, "changed.ngc", []byte("G1 X1\n"), true)
	h.markReady(t, changedSetup.ID, changedSetup.Revision)
	changedRecord, err := h.service.loadArtifact(ctx, h.db.SQL(), changedSetup.ID, changedArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the directory entry so identity detection is deterministic even
	// on filesystems with coarse mtime/ctime granularity.
	if err := replaceManagedObjectIdentity(h, changedRecord, []byte("G1 X1\n")); err != nil {
		t.Fatal(err)
	}
	repaired := h.createSetup(t, "Исправленный", "reconcile-repaired")
	h.attachProgram(t, repaired.ID, "valid.ngc", []byte("G2 X2\n"), true)
	if _, err := h.db.SQL().ExecContext(ctx, `
		UPDATE setups SET status = 'attention', attention_reason = 'old external issue'
		 WHERE id = ?`, repaired.ID); err != nil {
		t.Fatal(err)
	}
	reconcile, err := h.service.InspectManagedContent(ctx)
	if err != nil || reconcile.SetupsAttention < 1 || reconcile.SetupsRecovered != 0 {
		t.Fatalf("startup identity reconciliation = %+v, %v", reconcile, err)
	}
	changed, _ := h.service.GetSetup(ctx, changedSetup.ID)
	stillAttention, _ := h.service.GetSetup(ctx, repaired.ID)
	if changed.Status != domain.SetupStatusAttention || stillAttention.Status != domain.SetupStatusAttention {
		t.Fatalf("identity-pass states changed=%s repaired=%s", changed.Status, stillAttention.Status)
	}
	verified, err := h.service.Reconcile(ctx)
	if err != nil || verified.SetupsRecovered < 1 {
		t.Fatalf("full reconciliation = %+v, %v", verified, err)
	}
	recovered, _ := h.service.GetSetup(ctx, repaired.ID)
	if recovered.Status != domain.SetupStatusDraft {
		t.Fatalf("fully reconciled state = %s", recovered.Status)
	}

	orphanContent := []byte("G3 X3\n")
	staged, err := h.store.Stage(ctx, bytes.NewReader(orphanContent), int64(len(orphanContent)))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := h.store.Publish(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	orphanID, _ := domain.NewStorageObjectID()
	if _, err := h.db.SQL().ExecContext(ctx, `
		INSERT INTO storage_objects(id, library_id, storage_key, media_type, byte_size, sha256)
		VALUES (?, ?, ?, 'text/x-gcode', ?, ?)`, orphanID, h.service.libraryID, orphan.Key, orphan.Size, orphan.SHA256); err != nil {
		t.Fatal(err)
	}
	journalID, _ := domain.NewOperationID()
	if _, err := h.db.SQL().ExecContext(ctx, `
		INSERT INTO operation_journal(id, library_id, operation, storage_object_id, state)
		VALUES (?, ?, 'testGC', ?, 'intent')`, journalID, h.service.libraryID, orphanID); err != nil {
		t.Fatal(err)
	}
	firstGC, err := h.service.GarbageCollect(ctx)
	if err != nil || firstGC.ObjectsProtected != 1 || firstGC.ObjectsRemoved != 0 {
		t.Fatalf("protected GC = %+v, %v", firstGC, err)
	}
	if _, err := h.store.OpenObject(orphan.Key, orphan.SHA256, orphan.Version); err != nil {
		t.Fatalf("protected object removed: %v", err)
	}
	if _, err := h.db.SQL().ExecContext(ctx, `UPDATE operation_journal SET state = 'completed' WHERE id = ?`, journalID); err != nil {
		t.Fatal(err)
	}
	secondGC, err := h.service.GarbageCollect(ctx)
	if err != nil || secondGC.ObjectsRemoved != 1 || secondGC.BytesRemoved != orphan.Size {
		t.Fatalf("unreferenced GC = %+v, %v", secondGC, err)
	}
	if _, err := h.store.OpenObject(orphan.Key, orphan.SHA256, ""); err == nil {
		t.Fatal("garbage-collected object still opens")
	}

	hash, _ := idempotencyRequestHash("cleanup", map[string]string{"value": "one"})
	claim, err := h.service.claimIdempotency(ctx, "cleanup-idempotency", "cleanup", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.finishIdempotency(ctx, claim, 200, map[string]bool{"ok": true}, nil); err != nil {
		t.Fatal(err)
	}
	archivable := h.createSetup(t, "Cleanup archive", "cleanup-archive-create")
	archived, err := h.service.ArchiveSetup(ctx, archivable.ID, ArchiveInput{
		ExpectedRevision: archivable.Revision, IdempotencyKey: "cleanup-archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.CreateDeletePlan(ctx, archived.ID, archived.Revision, "maintenance-delete-plan"); err != nil {
		t.Fatal(err)
	}
	importID, _ := domain.NewImportID()
	if _, err := h.db.SQL().ExecContext(ctx, `
		INSERT INTO import_sessions(
			id, library_id, idempotency_key, setup_name, state, expires_at
		) VALUES (?, ?, ?, 'Expired import', 'staging', ?)`,
		importID, h.service.libraryID, "expired-import-key", sqlTimestamp(h.service.now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	h.clockNS.Add(int64(2 * time.Second))
	cleanup, err := h.service.CleanupExpired(ctx)
	if err != nil || cleanup.IdempotencyRequests < 1 || cleanup.DeleteConfirmations < 1 || cleanup.ImportSessions != 1 {
		t.Fatalf("CleanupExpired = %+v, %v", cleanup, err)
	}
	var importState string
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT state FROM import_sessions WHERE id = ?", importID).Scan(&importState); err != nil || importState != "cancelled" {
		t.Fatalf("expired import state = %q, %v", importState, err)
	}
	var count int
	if err := h.db.SQL().QueryRowContext(ctx, `
		SELECT count(*) FROM idempotency_requests WHERE library_id = ? AND key = ?`,
		h.service.libraryID, "cleanup-idempotency").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired idempotency count = %d, %v", count, err)
	}
}

func TestFullReconcileHashesContentBeyondTheStartupIdentityPass(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Полная сверка", "full-reconcile-create")
	artifactID, object, _ := h.attachProgram(t, setup.ID, "hash.ngc", []byte("G1 X1\n"), true)
	h.markReady(t, setup.ID, setup.Revision)
	file, err := h.roots.OpenLibrary(object.Key, unix.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("M"), 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := h.store.InspectObject(object.Key, object.SHA256, "")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate identity metadata already refreshed without accepting the new
	// bytes. The startup pass is intentionally O(files), while the background
	// full pass must still compare SHA-256 and detect this mismatch.
	if _, err := h.db.SQL().ExecContext(ctx, `
		UPDATE setup_artifacts
		   SET identity_device = ?, identity_inode = ?, identity_size = ?,
		       identity_mtime_ns = ?, identity_ctime_ns = ?, object_version = ?
		 WHERE id = ?`, int64(current.Identity.Device), int64(current.Identity.Inode), current.Size,
		current.Identity.ModTimeNS, current.Identity.ChangeTimeNS, current.Version, artifactID); err != nil {
		t.Fatal(err)
	}
	if result, err := h.service.InspectManagedContent(ctx); err != nil || result.SetupsAttention != 0 {
		t.Fatalf("identity-only pass = %+v, %v", result, err)
	}
	if result, err := h.service.Reconcile(ctx); err != nil || result.SetupsAttention != 1 {
		t.Fatalf("full hash pass = %+v, %v", result, err)
	}
	loaded, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || loaded.Status != domain.SetupStatusAttention {
		t.Fatalf("fully reconciled setup = %+v, %v", loaded, err)
	}
}

func startBlockingTestJob(t *testing.T, h *lifecycleTestHarness, ctx context.Context) (*domain.Job, chan struct{}) {
	t.Helper()
	tx, err := h.db.SQL().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.service.insertJobTx(ctx, tx, domain.JobKindReconcile, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	h.service.launchJob(job.ID, func(jobCtx context.Context, _ func(domain.JobProgress) error) (any, error) {
		close(started)
		select {
		case <-release:
			return map[string]bool{"released": true}, nil
		case <-jobCtx.Done():
			return nil, jobCtx.Err()
		}
	})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return job, release
}

func hasValidationIssue(issues []domain.ValidationIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
