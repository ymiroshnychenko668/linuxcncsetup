//go:build linux

package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

func TestCommittedJobResultsAndUploadClaimsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	libraryDir := filepath.Join(root, "library")
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(libraryDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	stack := openAtomicJobStack(t, libraryDir, stateDir)
	manager := stack.manager

	create := func(name, key string) *domain.Setup {
		t.Helper()
		setup, err := manager.CreateSetup(ctx, CreateSetupInput{Name: name, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		return setup
	}
	addProgram := func(setup *domain.Setup, name, key string, contents []byte) *domain.Setup {
		t.Helper()
		updated, err := manager.AddPrograms(ctx, setup.ID, AddProgramsInput{
			ExpectedRevision: setup.Revision,
			IdempotencyKey:   key,
			Programs: []UploadArtifactInput{{
				Role: domain.ArtifactRoleProgram, DisplayName: name,
				Content: bytes.NewReader(contents), ExpectedSize: int64(len(contents)),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return updated
	}

	uploadContents := []byte("G0 X1\nM2\n")
	uploadSetup := create("Atomic upload", "atomic-upload-create")
	uploadJob, err := manager.PrepareUploadJob(ctx, uploadSetup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: uploadSetup.Revision,
		Items:          []UploadJobItem{{DisplayName: "atomic.ngc", Size: int64(len(uploadContents))}},
		IdempotencyKey: "atomic-upload-prepare",
	})
	if err != nil {
		t.Fatal(err)
	}
	uploadRunKey := "atomic-upload-run"
	uploadJob, err = manager.RunUploadJob(ctx, uploadJob.ID, RunUploadJobInput{
		IdempotencyKey: uploadRunKey,
		Source:         singleProgramSource("atomic.ngc", bytes.NewReader(uploadContents)),
	})
	assertTerminalJob(t, uploadJob, err, domain.JobStateSucceeded, "")
	if uploadJob.Progress.CompletedBytes != int64(len(uploadContents)) || uploadJob.Progress.CompletedItems != 1 {
		t.Fatalf("upload progress = %+v", uploadJob.Progress)
	}

	replaceSetup := addProgram(create("Atomic replace", "atomic-replace-create"),
		"replace.ngc", "atomic-replace-program", []byte("G0 X0\n"))
	replacedArtifactID := replaceSetup.Artifacts[0].ID
	replacedArtifactName := replaceSetup.Artifacts[0].DisplayName
	replacedArtifactPrimary := replaceSetup.Artifacts[0].Primary
	replacedArtifactOldVersion := replaceSetup.Artifacts[0].Version
	replaceRevisionBefore := replaceSetup.Revision
	replaceContents := []byte("G1 X123\n")
	replaceJob, err := manager.PrepareUploadJob(ctx, replaceSetup.ID, PrepareUploadJobInput{
		Operation: UploadJobReplaceProgram, ExpectedRevision: replaceSetup.Revision,
		ArtifactID: replaceSetup.Artifacts[0].ID, ExpectedVersion: replaceSetup.Artifacts[0].Version,
		Items:          []UploadJobItem{{DisplayName: replaceSetup.Artifacts[0].DisplayName, Size: int64(len(replaceContents))}},
		IdempotencyKey: "atomic-replace-prepare",
	})
	if err != nil {
		t.Fatal(err)
	}
	replaceRunKey := "atomic-replace-run"
	replaceJob, err = manager.RunUploadJob(ctx, replaceJob.ID, RunUploadJobInput{
		IdempotencyKey: replaceRunKey, Content: bytes.NewReader(replaceContents),
	})
	assertTerminalJob(t, replaceJob, err, domain.JobStateSucceeded, "")
	if replaceJob.Progress.CompletedBytes != int64(len(replaceContents)) || replaceJob.Progress.CompletedItems != 1 {
		t.Fatalf("replace progress = %+v", replaceJob.Progress)
	}

	sheetSetup := create("Atomic setup sheet", "atomic-sheet-create")
	sheetContents := []byte("%PDF-1.4\n%%EOF\n")
	sheetJob, err := manager.PrepareUploadJob(ctx, sheetSetup.ID, PrepareUploadJobInput{
		Operation: UploadJobPutSetupSheet, ExpectedRevision: sheetSetup.Revision,
		Items:          []UploadJobItem{{DisplayName: "setup.pdf", Size: int64(len(sheetContents))}},
		IdempotencyKey: "atomic-sheet-prepare",
	})
	if err != nil {
		t.Fatal(err)
	}
	sheetRunKey := "atomic-sheet-run"
	sheetJob, err = manager.RunUploadJob(ctx, sheetJob.ID, RunUploadJobInput{
		IdempotencyKey: sheetRunKey, Content: bytes.NewReader(sheetContents),
	})
	assertTerminalJob(t, sheetJob, err, domain.JobStateSucceeded, "")
	if sheetJob.Progress.CompletedBytes != int64(len(sheetContents)) || sheetJob.Progress.CompletedItems != 1 {
		t.Fatalf("setup-sheet progress = %+v", sheetJob.Progress)
	}

	failedContents := []byte("G1 X2\n")
	failedSetup := create("Atomic failed upload", "atomic-failed-create")
	failedJob, err := manager.PrepareUploadJob(ctx, failedSetup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: failedSetup.Revision,
		Items:          []UploadJobItem{{DisplayName: "failed.ngc", Size: int64(len(failedContents) + 1)}},
		IdempotencyKey: "atomic-failed-prepare",
	})
	if err != nil {
		t.Fatal(err)
	}
	failedRunKey := "atomic-failed-run"
	failedJob, err = manager.RunUploadJob(ctx, failedJob.ID, RunUploadJobInput{
		IdempotencyKey: failedRunKey,
		Source:         singleProgramSource("failed.ngc", bytes.NewReader(failedContents)),
	})
	assertTerminalJob(t, failedJob, err, domain.JobStateFailed, domain.CodeUploadIncomplete)
	var failedEnvelope uploadJobEnvelope
	if err := json.Unmarshal(failedJob.Result, &failedEnvelope); err != nil {
		t.Fatal(err)
	}
	if failedEnvelope.Setup != nil {
		t.Fatalf("failed upload exposed a non-durable setup result: %+v", failedEnvelope.Setup)
	}

	conflictContents := []byte("G1 X777\n")
	conflictSetup := create("Atomic upload conflict", "atomic-conflict-create")
	conflictJob, err := manager.PrepareUploadJob(ctx, conflictSetup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: conflictSetup.Revision,
		Items:          []UploadJobItem{{DisplayName: "conflict.ngc", Size: int64(len(conflictContents))}},
		IdempotencyKey: "atomic-conflict-prepare",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateSetup(ctx, conflictSetup.ID, UpdateSetupInput{
		ExpectedRevision: conflictSetup.Revision, Name: conflictSetup.Name,
		Description: "advance the revision after upload preparation", IdempotencyKey: "atomic-conflict-advance",
	}); err != nil {
		t.Fatal(err)
	}
	conflictRunKey := "atomic-conflict-run"
	conflictJob, err = manager.RunUploadJob(ctx, conflictJob.ID, RunUploadJobInput{
		IdempotencyKey: conflictRunKey,
		Source:         singleProgramSource("conflict.ngc", bytes.NewReader(conflictContents)),
	})
	assertTerminalJob(t, conflictJob, err, domain.JobStateConflict, domain.CodeRevisionConflict)
	var conflictEnvelope uploadJobEnvelope
	if err := json.Unmarshal(conflictJob.Result, &conflictEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(conflictEnvelope.Digests) != 1 || conflictEnvelope.Setup != nil {
		t.Fatalf("full-body conflict evidence = %+v", conflictEnvelope)
	}

	cancelledContents := bytes.Repeat([]byte("G1 X123456789\n"), 1<<15)
	cancelledSetup := create("Atomic cancelled upload", "atomic-cancelled-create")
	cancelledJob, err := manager.PrepareUploadJob(ctx, cancelledSetup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: cancelledSetup.Revision,
		Items:          []UploadJobItem{{DisplayName: "cancelled.ngc", Size: int64(len(cancelledContents))}},
		IdempotencyKey: "atomic-cancelled-prepare",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelledRunKey := "atomic-cancelled-run"
	cancelCtx, cancel := context.WithCancel(ctx)
	cancelledJob, err = manager.RunUploadJob(cancelCtx, cancelledJob.ID, RunUploadJobInput{
		IdempotencyKey: cancelledRunKey,
		Source: singleProgramSource("cancelled.ngc", &cancelAfterFirstRead{
			reader: bytes.NewReader(cancelledContents), cancel: cancel,
		}),
	})
	assertTerminalJob(t, cancelledJob, err, domain.JobStateCancelled, domain.CodeJobCancelled)

	validationSetup := create("Atomic validation", "atomic-validation-create")
	validationID, err := domain.NewValidationRunID()
	if err != nil {
		t.Fatal(err)
	}
	validationJob := insertRunningJob(t, manager, domain.JobKindValidate, validationSetup.ID, 0, func(tx *sql.Tx, job *domain.Job) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO validation_runs(id, setup_id, revision, state, result_json)
			VALUES (?, ?, ?, 'queued', '{"issues":[]}')`, validationID, validationSetup.ID, validationSetup.Revision); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(validationResultJSON{ValidationRunID: validationID})
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET result_json = ? WHERE id = ?`, string(payload), job.ID); err != nil {
			t.Fatal(err)
		}
	})
	validationResult, err := manager.executeValidation(ctx, validationJob.ID, validationSetup.ID,
		validationID, validationSetup.Revision, func(domain.JobProgress) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if validationResult.State != domain.ValidationStateFailed {
		t.Fatalf("blocking validation run state = %s, want failed", validationResult.State)
	}
	validationJob, err = manager.GetJob(ctx, validationJob.ID)
	assertTerminalJob(t, validationJob, err, domain.JobStateSucceeded, "")

	queuedValidationSetup := create("Atomic queued validation cancellation", "atomic-queued-validation-create")
	queuedValidationID, err := domain.NewValidationRunID()
	if err != nil {
		t.Fatal(err)
	}
	queuedValidationJob := insertQueuedValidationJob(t, manager, queuedValidationSetup, queuedValidationID)
	if err := manager.cancelQueuedValidation(ctx, queuedValidationJob.ID, queuedValidationSetup.ID, queuedValidationID); err != nil {
		t.Fatal(err)
	}
	queuedValidationJob, err = manager.GetJob(ctx, queuedValidationJob.ID)
	assertTerminalJob(t, queuedValidationJob, err, domain.JobStateCancelled, domain.CodeJobCancelled)

	duplicateSource := addProgram(create("Atomic duplicate source", "atomic-duplicate-create"),
		"source.ngc", "atomic-duplicate-program", []byte("G0 X3\n"))
	duplicateBytes := duplicateSource.Artifacts[0].ByteSize
	duplicateJob := insertRunningJob(t, manager, domain.JobKindDuplicate, duplicateSource.ID, duplicateBytes, nil)
	duplicated, err := manager.executeDuplicate(ctx, duplicateJob.ID, duplicateSource.ID,
		duplicateSource.Revision, "Atomic duplicate copy", func(domain.JobProgress) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	duplicateJob, err = manager.GetJob(ctx, duplicateJob.ID)
	assertTerminalJob(t, duplicateJob, err, domain.JobStateSucceeded, "")

	restoreSetup := addProgram(create("Atomic restore", "atomic-restore-create"),
		"restore.ngc", "atomic-restore-program", []byte("G0 X4\n"))
	archived, err := manager.ArchiveSetup(ctx, restoreSetup.ID, ArchiveInput{
		ExpectedRevision: restoreSetup.Revision, IdempotencyKey: "atomic-restore-archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	restoreJob := insertRunningJob(t, manager, domain.JobKindRestore, archived.ID,
		archived.Artifacts[0].ByteSize, nil)
	restored, err := manager.changeArchiveState(ctx, archived.ID, ArchiveInput{
		ExpectedRevision: archived.Revision, IdempotencyKey: "restore-job-" + restoreJob.ID,
	}, true, restoreJob.ID, func(domain.JobProgress) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	restoreJob, err = manager.GetJob(ctx, restoreJob.ID)
	assertTerminalJob(t, restoreJob, err, domain.JobStateSucceeded, "")
	var restoreAuditJob string
	if err := stack.db.SQL().QueryRowContext(ctx, `
		SELECT COALESCE(job_id, '') FROM audit_events
		 WHERE library_id = ? AND setup_id = ? AND operation = 'restore'
		 ORDER BY occurred_at DESC, id DESC LIMIT 1`, manager.libraryID, restored.ID).Scan(&restoreAuditJob); err != nil {
		t.Fatal(err)
	}
	if restoreAuditJob != restoreJob.ID {
		t.Fatalf("restore audit job_id = %q, want %q", restoreAuditJob, restoreJob.ID)
	}
	var restoreJournalJob string
	if err := stack.db.SQL().QueryRowContext(ctx, `
		SELECT COALESCE(job_id, '') FROM operation_journal
		 WHERE library_id = ? AND setup_id = ? AND operation = 'restore'
		 ORDER BY created_at DESC, id DESC LIMIT 1`, manager.libraryID, restored.ID).Scan(&restoreJournalJob); err != nil {
		t.Fatal(err)
	}
	if restoreJournalJob != restoreJob.ID {
		t.Fatalf("restore journal job_id = %q, want %q", restoreJournalJob, restoreJob.ID)
	}

	for _, job := range []*domain.Job{uploadJob, replaceJob, sheetJob, failedJob, conflictJob, cancelledJob} {
		var state string
		if err := stack.db.SQL().QueryRowContext(ctx, `
			SELECT state FROM idempotency_requests
			 WHERE library_id = ? AND operation = ?`, manager.libraryID, "runUploadJob:"+job.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != idempotencyStateCompleted {
			t.Fatalf("run claim for %s = %s, want completed", job.ID, state)
		}
	}

	jobExpectations := []struct {
		id    string
		state domain.JobState
		code  domain.ErrorCode
	}{
		{uploadJob.ID, domain.JobStateSucceeded, ""},
		{replaceJob.ID, domain.JobStateSucceeded, ""},
		{sheetJob.ID, domain.JobStateSucceeded, ""},
		{failedJob.ID, domain.JobStateFailed, domain.CodeUploadIncomplete},
		{conflictJob.ID, domain.JobStateConflict, domain.CodeRevisionConflict},
		{cancelledJob.ID, domain.JobStateCancelled, domain.CodeJobCancelled},
		{validationJob.ID, domain.JobStateSucceeded, ""},
		{queuedValidationJob.ID, domain.JobStateCancelled, domain.CodeJobCancelled},
		{duplicateJob.ID, domain.JobStateSucceeded, ""},
		{restoreJob.ID, domain.JobStateSucceeded, ""},
	}
	stack.close(t)

	stack = openAtomicJobStack(t, libraryDir, stateDir)
	defer stack.close(t)
	manager = stack.manager
	for _, expectation := range jobExpectations {
		job, err := manager.GetJob(ctx, expectation.id)
		assertTerminalJob(t, job, err, expectation.state, expectation.code)
	}
	replacedAfterRestart, err := manager.GetSetup(ctx, replaceSetup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacedAfterRestart.Revision != replaceRevisionBefore+1 || len(replacedAfterRestart.Artifacts) != 1 {
		t.Fatalf("replace aggregate after restart = %+v", replacedAfterRestart)
	}
	replacedArtifact := replacedAfterRestart.Artifacts[0]
	if replacedArtifact.ID != replacedArtifactID || replacedArtifact.DisplayName != replacedArtifactName ||
		replacedArtifact.Primary != replacedArtifactPrimary || replacedArtifact.Version == replacedArtifactOldVersion {
		t.Fatalf("replace identity invariants after restart = %+v", replacedArtifact)
	}
	replacedContent, err := manager.ReadArtifactAll(ctx, replacedAfterRestart.ID, replacedArtifact.ID, int64(len(replaceContents)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replacedContent.Data, replaceContents) {
		t.Fatalf("replace content after restart = %q", replacedContent.Data)
	}
	replayedReplace, err := manager.RunUploadJob(ctx, replaceJob.ID, RunUploadJobInput{
		IdempotencyKey: replaceRunKey, Content: bytes.NewReader(replaceContents),
	})
	assertTerminalJob(t, replayedReplace, err, domain.JobStateSucceeded, "")
	if replayedReplace.ID != replaceJob.ID {
		t.Fatalf("replace replay job = %s, want %s", replayedReplace.ID, replaceJob.ID)
	}
	alteredReplace := append([]byte(nil), replaceContents...)
	alteredReplace[len(alteredReplace)-2] = '9'
	if _, err := manager.RunUploadJob(ctx, replaceJob.ID, RunUploadJobInput{
		IdempotencyKey: replaceRunKey, Content: bytes.NewReader(alteredReplace),
	}); !domain.IsErrorCode(err, domain.CodeIdempotencyConflict) {
		t.Fatalf("altered replace replay error = %v", err)
	}
	replacedAfterReplay, err := manager.GetSetup(ctx, replaceSetup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacedAfterReplay.Revision != replaceRevisionBefore+1 {
		t.Fatalf("replace replay advanced revision to %d", replacedAfterReplay.Revision)
	}

	replayed, err := manager.RunUploadJob(ctx, uploadJob.ID, RunUploadJobInput{
		IdempotencyKey: uploadRunKey,
		Source:         singleProgramSource("atomic.ngc", bytes.NewReader(uploadContents)),
	})
	assertTerminalJob(t, replayed, err, domain.JobStateSucceeded, "")
	altered := append([]byte(nil), uploadContents...)
	altered[len(altered)-2] = '9'
	if _, err := manager.RunUploadJob(ctx, uploadJob.ID, RunUploadJobInput{
		IdempotencyKey: uploadRunKey,
		Source:         singleProgramSource("atomic.ngc", bytes.NewReader(altered)),
	}); !domain.IsErrorCode(err, domain.CodeIdempotencyConflict) {
		t.Fatalf("altered successful upload replay error = %v", err)
	}
	if _, err := manager.RunUploadJob(ctx, failedJob.ID, RunUploadJobInput{
		IdempotencyKey: failedRunKey,
		Source:         singleProgramSource("failed.ngc", bytes.NewReader([]byte("different"))),
	}); !domain.IsErrorCode(err, domain.CodeIdempotencyConflict) {
		t.Fatalf("incomplete failed upload replay error = %v", err)
	}
	replayedConflict, err := manager.RunUploadJob(ctx, conflictJob.ID, RunUploadJobInput{
		IdempotencyKey: conflictRunKey,
		Source:         singleProgramSource("conflict.ngc", bytes.NewReader(conflictContents)),
	})
	assertTerminalJob(t, replayedConflict, err, domain.JobStateConflict, domain.CodeRevisionConflict)
	alteredConflict := append([]byte(nil), conflictContents...)
	alteredConflict[len(alteredConflict)-2] = '8'
	if _, err := manager.RunUploadJob(ctx, conflictJob.ID, RunUploadJobInput{
		IdempotencyKey: conflictRunKey,
		Source:         singleProgramSource("conflict.ngc", bytes.NewReader(alteredConflict)),
	}); !domain.IsErrorCode(err, domain.CodeIdempotencyConflict) {
		t.Fatalf("altered conflict replay error = %v", err)
	}
	if _, err := manager.RunUploadJob(ctx, cancelledJob.ID, RunUploadJobInput{
		IdempotencyKey: cancelledRunKey,
		Source:         singleProgramSource("cancelled.ngc", bytes.NewReader([]byte("different"))),
	}); !domain.IsErrorCode(err, domain.CodeIdempotencyConflict) {
		t.Fatalf("incomplete cancelled upload replay error = %v", err)
	}

	var duplicateResult domain.Setup
	if err := json.Unmarshal(duplicateJob.Result, &duplicateResult); err != nil {
		t.Fatal(err)
	}
	if duplicateResult.ID != duplicated.ID {
		t.Fatalf("duplicate result setup = %q, want %q", duplicateResult.ID, duplicated.ID)
	}
}

func TestAtomicJobMarshalFailureRollsBackDomainTransaction(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Before marshal failure", "marshal-failure-create")
	job := insertRunningJob(t, h.service, domain.JobKindDuplicate, setup.ID, 0, nil)

	tx, err := h.service.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE setups SET name = 'Must roll back' WHERE id = ?`, setup.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.finishJobTx(ctx, tx, job.ID, make(chan struct{}), nil); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("finishJobTx marshal error = %v", err)
	}
	if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
		t.Fatal(err)
	}

	reloaded, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Name != setup.Name {
		t.Fatalf("domain mutation committed after marshal failure: %q", reloaded.Name)
	}
	reloadedJob, err := h.service.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedJob.State != domain.JobStateRunning {
		t.Fatalf("job state after rolled-back marshal failure = %s", reloadedJob.State)
	}
}

func TestCancelQueuedValidationSignalsWorkerWaitingForHeavySlot(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) {
		options.MaxParallelHeavyJobs = 1
	})
	ctx := context.Background()
	release, err := h.service.acquireHeavy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	setup := h.createSetup(t, "Queued validation cancellation", "queued-cancel-create")
	job, err := h.service.ValidateSetup(ctx, setup.ID, ValidateInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "queued-cancel-validate",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := h.service.CancelJob(ctx, job.ID, "queued-cancel-job")
	assertTerminalJob(t, cancelled, err, domain.JobStateCancelled, domain.CodeJobCancelled)

	waitCtx, waitCancel := context.WithTimeout(ctx, time.Second)
	defer waitCancel()
	if err := h.service.Wait(waitCtx); err != nil {
		t.Fatalf("worker remained blocked after queued validation cancellation: %v", err)
	}
	release()
	released = true
}

type atomicJobStack struct {
	roots   *storage.Roots
	db      *database.DB
	manager *Service
}

func openAtomicJobStack(t *testing.T, libraryDir, stateDir string) *atomicJobStack {
	t.Helper()
	ctx := context.Background()
	roots, err := storage.NewRoots(libraryDir, stateDir, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, stateDir)
	if err != nil {
		_ = roots.Close()
		t.Fatal(err)
	}
	if err := db.EnsureLibrary(ctx, roots.LibraryID(), roots.LibraryFingerprint()); err != nil {
		_ = db.Close()
		_ = roots.Close()
		t.Fatal(err)
	}
	store, err := storage.NewStore(roots, storage.StoreOptions{})
	if err != nil {
		_ = db.Close()
		_ = roots.Close()
		t.Fatal(err)
	}
	manager, err := New(Options{
		Database: db, Objects: store, LibraryID: roots.LibraryID(),
		GCodeExtensions: []string{".ngc", ".nc"},
	})
	if err != nil {
		_ = db.Close()
		_ = roots.Close()
		t.Fatal(err)
	}
	return &atomicJobStack{roots: roots, db: db, manager: manager}
}

func (s *atomicJobStack) close(t *testing.T) {
	t.Helper()
	if s.manager != nil {
		s.manager.Close()
		s.manager = nil
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		s.db = nil
	}
	if s.roots != nil {
		if err := s.roots.Close(); err != nil {
			t.Fatal(err)
		}
		s.roots = nil
	}
}

func insertRunningJob(
	t *testing.T,
	manager *Service,
	kind domain.JobKind,
	setupID string,
	totalBytes int64,
	prepare func(*sql.Tx, *domain.Job),
) *domain.Job {
	t.Helper()
	ctx := context.Background()
	tx, err := manager.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	job, err := manager.insertJobTx(ctx, tx, kind, setupID, "", &totalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		prepare(tx, job)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := manager.markJobRunning(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	return job
}

func insertQueuedValidationJob(
	t *testing.T,
	manager *Service,
	setup *domain.Setup,
	runID string,
) *domain.Job {
	t.Helper()
	ctx := context.Background()
	tx, err := manager.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO validation_runs(id, setup_id, revision, state, result_json)
		VALUES (?, ?, ?, 'queued', '{"issues":[]}')`, runID, setup.ID, setup.Revision); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	job, err := manager.insertJobTx(ctx, tx, domain.JobKindValidate, setup.ID, "", &zero)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(validationResultJSON{ValidationRunID: runID})
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET result_json = ? WHERE id = ?`, string(payload), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return job
}

func singleProgramSource(name string, reader interface{ Read([]byte) (int, error) }) ProgramUploadSource {
	return func(yield func(UploadArtifactInput) error) error {
		return yield(UploadArtifactInput{DisplayName: name, Content: reader})
	}
}

type cancelAfterFirstRead struct {
	reader interface{ Read([]byte) (int, error) }
	cancel context.CancelFunc
	done   bool
}

func (r *cancelAfterFirstRead) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if count > 0 && !r.done {
		r.done = true
		r.cancel()
	}
	return count, err
}

func assertTerminalJob(
	t *testing.T,
	job *domain.Job,
	err error,
	wantState domain.JobState,
	wantCode domain.ErrorCode,
) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.State != wantState || job.ErrorCode != wantCode || !job.State.Terminal() {
		t.Fatalf("job = %+v, want terminal %s/%s", job, wantState, wantCode)
	}
}
