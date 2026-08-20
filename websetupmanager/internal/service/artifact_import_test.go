//go:build linux

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func TestImportThreeProgramsAndPDFPublishesExactlyOneSetup(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	start := StartImportInput{Name: "Корпус A", Description: "Установка 20", IdempotencyKey: "import-three-start"}
	session, err := h.service.StartImport(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	replayedSession, err := h.service.StartImport(ctx, start)
	if err != nil || replayedSession.ID != session.ID {
		t.Fatalf("start replay = %+v, %v", replayedSession, err)
	}

	contents := []struct {
		role    domain.ArtifactRole
		name    string
		content []byte
	}{
		{domain.ArtifactRoleProgram, "10-face.ngc", []byte("G90\nG0 X0 Y0\n")},
		{domain.ArtifactRoleProgram, "20-bore.NGC", []byte("G1 X2 F100\n")},
		{domain.ArtifactRoleProgram, "30-finish.nc", []byte("M3 S1000\nG1 X3\n")},
		{domain.ArtifactRoleSetupSheet, "setup.pdf", []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")},
	}
	importIDs := make([]string, 0, len(contents))
	for index, item := range contents {
		artifact, uploadErr := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
			Role: item.role, DisplayName: item.name, Content: bytes.NewReader(item.content),
			ExpectedSize: int64(len(item.content)), IdempotencyKey: "import-three-upload-" + string(rune('a'+index)),
		})
		if uploadErr != nil {
			t.Fatalf("upload %s: %v", item.name, uploadErr)
		}
		if artifact.State != domain.ImportArtifactStaged || artifact.ByteSize != int64(len(item.content)) {
			t.Fatalf("staged artifact = %+v", artifact)
		}
		if index == 0 {
			replay, replayErr := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
				Role: item.role, DisplayName: item.name, Content: bytes.NewReader(item.content),
				ExpectedSize: int64(len(item.content)), IdempotencyKey: "import-three-upload-a",
			})
			if replayErr != nil || replay.ID != artifact.ID {
				t.Fatalf("upload replay = %+v, %v", replay, replayErr)
			}
		}
		importIDs = append(importIDs, artifact.ID)
	}
	var setupsBefore int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM setups").Scan(&setupsBefore); err != nil {
		t.Fatal(err)
	}
	if setupsBefore != 0 {
		t.Fatalf("staging import exposed %d setups", setupsBefore)
	}

	commit := CommitImportInput{
		ExpectedArtifactIDs: importIDs, PrimaryArtifactID: importIDs[1], IdempotencyKey: "import-three-commit",
	}
	setup, err := h.service.CommitImport(ctx, session.ID, commit)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Status != domain.SetupStatusDraft || setup.Revision != domain.InitialRevision ||
		setup.Source != domain.SetupSourceImported || len(setup.Artifacts) != 4 {
		t.Fatalf("imported setup = %+v", setup)
	}
	programs, sheets, primaries := 0, 0, 0
	for _, artifact := range setup.Artifacts {
		switch artifact.Role {
		case domain.ArtifactRoleProgram:
			programs++
			if artifact.Primary {
				primaries++
				if artifact.DisplayName != "20-bore.NGC" {
					t.Fatalf("unexpected primary: %+v", artifact)
				}
			}
		case domain.ArtifactRoleSetupSheet:
			sheets++
		}
	}
	if programs != 3 || sheets != 1 || primaries != 1 {
		t.Fatalf("composition programs=%d sheets=%d primaries=%d", programs, sheets, primaries)
	}
	if replay, replayErr := h.service.CommitImport(ctx, session.ID, commit); replayErr != nil || replay.ID != setup.ID {
		t.Fatalf("commit replay = %+v, %v", replay, replayErr)
	}
	finished, err := h.service.GetImport(ctx, session.ID)
	if err != nil || finished.State != domain.ImportStateSucceeded || finished.SetupID != setup.ID {
		t.Fatalf("finished session = %+v, %v", finished, err)
	}
	var setupCount, succeededJobs, refCount, importObjectReferences, activeJournals int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM setups").Scan(&setupCount); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE import_session_id = ? AND state = 'succeeded'", session.ID).Scan(&succeededJobs); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT COALESCE(SUM(ref_count), 0) FROM storage_objects").Scan(&refCount); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM import_artifacts WHERE storage_object_id IS NOT NULL").Scan(&importObjectReferences); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM operation_journal WHERE state IN ('intent','storage_applied','db_applied')").Scan(&activeJournals); err != nil {
		t.Fatal(err)
	}
	if setupCount != 1 || succeededJobs != 1 || refCount != 4 || importObjectReferences != 0 || activeJournals != 0 {
		t.Fatalf("setupCount=%d succeededJobs=%d refCount=%d importRefs=%d activeJournals=%d",
			setupCount, succeededJobs, refCount, importObjectReferences, activeJournals)
	}
	payload, err := json.Marshal(setup)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("objects/")) || bytes.Contains(payload, []byte("storage")) {
		t.Fatalf("public setup leaked storage metadata: %s", payload)
	}
}

func TestPreparedUploadJobPublishesAtomicallyAndReportsItems(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Prepared upload", "prepared-upload-create")
	first := []byte("G90\nG0 X0\n")
	second := []byte("G91\nG1 X1\n")
	total := int64(len(first) + len(second))
	job, err := h.service.PrepareUploadJob(ctx, setup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: setup.Revision,
		Items:          []UploadJobItem{{DisplayName: "one.ngc", Size: int64(len(first))}, {DisplayName: "two.ngc", Size: int64(len(second))}},
		IdempotencyKey: "prepare-two-programs",
	})
	if err != nil || job.State != domain.JobStateQueued || job.Progress.TotalBytes != total || job.Progress.TotalItems != 2 {
		t.Fatalf("prepared job = %+v, %v", job, err)
	}
	active, err := h.service.ListActiveJobsForSetup(ctx, setup.ID)
	if err != nil || len(active) != 1 || active[0].ID != job.ID {
		t.Fatalf("active setup jobs = %+v, %v", active, err)
	}
	replay, err := h.service.PrepareUploadJob(ctx, setup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: setup.Revision,
		Items:          []UploadJobItem{{DisplayName: "one.ngc", Size: int64(len(first))}, {DisplayName: "two.ngc", Size: int64(len(second))}},
		IdempotencyKey: "prepare-two-programs",
	})
	if err != nil || replay.ID != job.ID {
		t.Fatalf("prepare replay = %+v, %v", replay, err)
	}
	index := 0
	terminal, err := h.service.RunUploadJob(ctx, job.ID, RunUploadJobInput{IdempotencyKey: "prepare-two-programs", Source: func(yield func(UploadArtifactInput) error) error {
		for _, content := range [][]byte{first, second} {
			if err := yield(UploadArtifactInput{Content: bytes.NewReader(content)}); err != nil {
				return err
			}
			index++
		}
		return nil
	}})
	if err != nil || terminal.State != domain.JobStateSucceeded || terminal.Progress.CompletedBytes != total || terminal.Progress.CompletedItems != 2 {
		t.Fatalf("terminal upload job = %+v, %v", terminal, err)
	}
	runReplay, err := h.service.RunUploadJob(ctx, job.ID, RunUploadJobInput{IdempotencyKey: "prepare-two-programs", Source: func(yield func(UploadArtifactInput) error) error {
		for _, content := range [][]byte{first, second} {
			if err := yield(UploadArtifactInput{Content: bytes.NewReader(content)}); err != nil {
				return err
			}
		}
		return nil
	}})
	if err != nil || runReplay.ID != job.ID || runReplay.State != domain.JobStateSucceeded {
		t.Fatalf("run replay = %+v, %v", runReplay, err)
	}
	altered := append([]byte(nil), second...)
	altered[0] = 'M'
	_, err = h.service.RunUploadJob(ctx, job.ID, RunUploadJobInput{IdempotencyKey: "prepare-two-programs", Source: func(yield func(UploadArtifactInput) error) error {
		for _, content := range [][]byte{first, altered} {
			if err := yield(UploadArtifactInput{Content: bytes.NewReader(content)}); err != nil {
				return err
			}
		}
		return nil
	}})
	if !domain.IsErrorCode(err, domain.CodeIdempotencyConflict) {
		t.Fatalf("altered upload replay error = %v", err)
	}
	changed, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || changed.Revision != setup.Revision+1 || len(changed.Artifacts) != 2 || index != 2 {
		t.Fatalf("changed setup = %+v, index=%d, err=%v", changed, index, err)
	}
	afterRevisionChange, err := h.service.PrepareUploadJob(ctx, setup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: setup.Revision,
		Items:          []UploadJobItem{{DisplayName: "one.ngc", Size: int64(len(first))}, {DisplayName: "two.ngc", Size: int64(len(second))}},
		IdempotencyKey: "prepare-two-programs",
	})
	if err != nil || afterRevisionChange.ID != job.ID || afterRevisionChange.State != domain.JobStateSucceeded {
		t.Fatalf("prepare replay after revision change = %+v, %v", afterRevisionChange, err)
	}
}

func TestPreparedUploadJobCancellationLeavesRevisionUntouched(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	setup := h.createSetup(t, "Cancelled upload", "cancelled-upload-create")
	job, err := h.service.PrepareUploadJob(ctx, setup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: setup.Revision,
		Items: []UploadJobItem{{DisplayName: "slow.ngc", Size: 12}}, IdempotencyKey: "prepare-cancel-upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := &blockingUploadReader{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan *domain.Job, 1)
	go func() {
		result, _ := h.service.RunUploadJob(ctx, job.ID, RunUploadJobInput{IdempotencyKey: "run-cancel-upload", Source: func(yield func(UploadArtifactInput) error) error {
			return yield(UploadArtifactInput{Content: reader})
		}})
		done <- result
	}()
	<-reader.started
	if _, err := h.service.CancelJob(ctx, job.ID, "cancel-prepared-upload"); err != nil {
		t.Fatal(err)
	}
	close(reader.release)
	terminal := <-done
	if terminal == nil || terminal.State != domain.JobStateCancelled {
		t.Fatalf("cancelled upload = %+v", terminal)
	}
	unchanged, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || unchanged.Revision != setup.Revision || len(unchanged.Artifacts) != 0 {
		t.Fatalf("partial revision was published: %+v, %v", unchanged, err)
	}
}

func TestPreparedUploadJobExpiresWhenContentNeverStarts(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.IdempotencyTTL = time.Second })
	ctx := context.Background()
	setup := h.createSetup(t, "Expired upload", "expired-upload-create")
	job, err := h.service.PrepareUploadJob(ctx, setup.ID, PrepareUploadJobInput{
		Operation: UploadJobAddPrograms, ExpectedRevision: setup.Revision,
		Items: []UploadJobItem{{DisplayName: "never.ngc", Size: 8}}, IdempotencyKey: "prepare-expired-upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.clockNS.Add(int64(2 * time.Second))
	cleanup, err := h.service.CleanupExpired(ctx)
	if err != nil || cleanup.UploadJobs != 1 {
		t.Fatalf("cleanup = %+v, %v", cleanup, err)
	}
	terminal, err := h.service.GetJob(ctx, job.ID)
	if err != nil || terminal.State != domain.JobStateCancelled {
		t.Fatalf("expired job = %+v, %v", terminal, err)
	}
}

type blockingUploadReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingUploadReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, context.Canceled
}

func TestStorageAdoptionReservationBlocksGarbageCollection(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	content := []byte("G0 X0\n")
	prepared, err := h.service.prepareArtifact(ctx, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "reserved.ngc",
		Content: bytes.NewReader(content), ExpectedSize: int64(len(content)),
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := storageCandidate{ID: prepared.StorageObjectID, Key: prepared.Object.Key, SHA256: prepared.Object.SHA256}
	h.service.cleanupStorageCandidates(ctx, []storageCandidate{candidate})
	if file, err := h.store.OpenObject(prepared.Object.Key, prepared.Object.SHA256, prepared.Object.Version); err != nil {
		t.Fatalf("active reservation did not protect object: %v", err)
	} else {
		file.Close()
	}
	var state string
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT state FROM operation_journal WHERE id = ?", prepared.ReservationJournalID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.JournalStateStorageApplied) {
		t.Fatalf("reservation state = %q", state)
	}
	h.service.cleanupPreparedObjects(ctx, []preparedArtifact{*prepared})
	if _, err := h.store.OpenObject(prepared.Object.Key, prepared.Object.SHA256, prepared.Object.Version); err == nil {
		t.Fatal("abandoned unreferenced object was not collected")
	}
	var objects int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM storage_objects WHERE id = ?", prepared.StorageObjectID).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if objects != 0 {
		t.Fatalf("abandoned storage row count = %d", objects)
	}
}

func TestGetImportReturnsImportNotFound(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	missingID, err := domain.NewImportID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.GetImport(context.Background(), missingID); !domain.IsErrorCode(err, domain.CodeImportNotFound) {
		t.Fatalf("missing import error = %v", err)
	}
}

func TestImportRejectsSecondSetupSheetUntilFirstIsExcluded(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Sheet role", IdempotencyKey: "sheet-role-start"})
	if err != nil {
		t.Fatal(err)
	}
	firstContent := []byte("%PDF-1.4\n%%EOF\n")
	first, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleSetupSheet, DisplayName: "first.pdf", Content: bytes.NewReader(firstContent),
		ExpectedSize: int64(len(firstContent)), IdempotencyKey: "sheet-role-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondContent := []byte("<!doctype html><html><body>Second</body></html>")
	if _, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleSetupSheet, DisplayName: "second.html", Content: bytes.NewReader(secondContent),
		ExpectedSize: int64(len(secondContent)), IdempotencyKey: "sheet-role-conflict",
	}); !domain.IsErrorCode(err, domain.CodeNameConflict) {
		t.Fatalf("second setup sheet error = %v", err)
	}
	if _, err := h.service.ExcludeImportArtifact(ctx, session.ID, first.ID, "sheet-role-exclude"); err != nil {
		t.Fatal(err)
	}
	second, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleSetupSheet, DisplayName: "second.html", Content: bytes.NewReader(secondContent),
		ExpectedSize: int64(len(secondContent)), IdempotencyKey: "sheet-role-second",
	})
	if err != nil || second.State != domain.ImportArtifactStaged {
		t.Fatalf("replacement sheet = %+v, %v", second, err)
	}
}

func TestImportFailureCanSaveExplicitPartialDraft(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Частичный", IdempotencyKey: "partial-start"})
	if err != nil {
		t.Fatal(err)
	}
	programBytes := []byte("G0 X0\n")
	program, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "usable.ngc", Content: bytes.NewReader(programBytes),
		ExpectedSize: int64(len(programBytes)), IdempotencyKey: "partial-program",
	})
	if err != nil {
		t.Fatal(err)
	}
	badPDF := []byte("this is not a PDF")
	if _, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleSetupSheet, DisplayName: "broken.pdf", Content: bytes.NewReader(badPDF),
		ExpectedSize: int64(len(badPDF)), IdempotencyKey: "partial-broken-sheet",
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("invalid PDF error = %v", err)
	}
	if _, err := h.service.CommitImport(ctx, session.ID, CommitImportInput{
		ExpectedArtifactIDs: []string{program.ID}, IdempotencyKey: "partial-not-explicit",
	}); !domain.IsErrorCode(err, domain.CodeUploadIncomplete) {
		t.Fatalf("non-partial commit error = %v", err)
	}
	setup, err := h.service.CommitImport(ctx, session.ID, CommitImportInput{
		ExpectedArtifactIDs: []string{program.ID}, SavePartialDraft: true, IdempotencyKey: "partial-explicit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if setup.Status != domain.SetupStatusDraft || len(setup.Artifacts) != 1 || !setup.Artifacts[0].Primary {
		t.Fatalf("partial draft = %+v", setup)
	}
	finished, err := h.service.GetImport(ctx, session.ID)
	if err != nil || finished.State != domain.ImportStateDraftSaved {
		t.Fatalf("partial session = %+v, %v", finished, err)
	}
}

func TestCommitImportVerificationSemaphoreSerializesTwoSessions(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.MaxParallelHeavyJobs = 1 })
	ctx := context.Background()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- h.service.withImportVerificationSlot(ctx, func() error {
			close(firstEntered)
			if got := len(h.service.heavy); got != 1 {
				return errors.New("first import verification did not own exactly one heavy-work slot")
			}
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first import session did not enter verification")
	}

	secondStarted := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- h.service.withImportVerificationSlot(ctx, func() error {
			close(secondEntered)
			if got := len(h.service.heavy); got != 1 {
				return errors.New("second import verification did not own exactly one heavy-work slot")
			}
			<-releaseSecond
			return nil
		})
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		t.Fatal("two import sessions verified concurrently with MaxParallelHeavyJobs=1")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first import verification did not release its slot")
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second import session did not acquire the released verification slot")
	}
	close(releaseSecond)
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second import verification did not finish")
	}
}

func TestCancelImportWithFreshKeyReturnsPersistedCancellationAndSurvivesShutdown(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Cancel replay", IdempotencyKey: "cancel-fresh-start"})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("G0 X0\n")
	if _, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "cancelled.ngc", Content: bytes.NewReader(content),
		ExpectedSize: int64(len(content)), IdempotencyKey: "cancel-fresh-upload",
	}); err != nil {
		t.Fatal(err)
	}
	first, err := h.service.CancelImport(ctx, session.ID, "cancel-fresh-first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.CancelImport(ctx, session.ID, "cancel-fresh-second")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.State != domain.ImportStateCancelled || second.ErrorCode != domain.CodeJobCancelled ||
		second.Bytes != 0 || !second.UpdatedAt.Equal(first.UpdatedAt) || len(second.Artifacts) != len(first.Artifacts) {
		t.Fatalf("fresh-key cancellation changed persisted terminal session: first=%+v second=%+v", first, second)
	}
	if replay, replayErr := h.service.CancelImport(ctx, session.ID, "cancel-fresh-second"); replayErr != nil ||
		replay.ID != second.ID || !replay.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatalf("fresh-key replay = %+v, %v", replay, replayErr)
	}
	var completedClaims int
	if err := h.db.SQL().QueryRowContext(ctx, `
		SELECT count(*) FROM idempotency_requests
		 WHERE key IN ('cancel-fresh-first', 'cancel-fresh-second') AND state = 'completed'`).Scan(&completedClaims); err != nil {
		t.Fatal(err)
	}
	if completedClaims != 2 {
		t.Fatalf("completed cancellation claims = %d, want 2", completedClaims)
	}

	shutdownCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := h.service.CloseContext(shutdownCtx); err != nil {
		t.Fatalf("shutdown after persisted cancellation: %v", err)
	}
	if err := h.service.Wait(shutdownCtx); err != nil {
		t.Fatalf("wait after persisted cancellation: %v", err)
	}
	persisted, err := h.service.GetImport(ctx, session.ID)
	if err != nil || persisted.State != domain.ImportStateCancelled || !persisted.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("persisted session after shutdown = %+v, %v", persisted, err)
	}
}

func TestCancelImportRejectsPublishedTerminalSessions(t *testing.T) {
	for _, test := range []struct {
		name      string
		partial   bool
		expected  domain.ImportState
		keySuffix string
	}{
		{name: "succeeded", expected: domain.ImportStateSucceeded, keySuffix: "succeeded"},
		{name: "draft saved", partial: true, expected: domain.ImportStateDraftSaved, keySuffix: "draft"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newLifecycleTestHarness(t, nil)
			ctx := context.Background()
			session, err := h.service.StartImport(ctx, StartImportInput{
				Name: "Published terminal", IdempotencyKey: "cancel-terminal-start-" + test.keySuffix,
			})
			if err != nil {
				t.Fatal(err)
			}
			content := []byte("G1 X1\n")
			artifact, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
				Role: domain.ArtifactRoleProgram, DisplayName: "terminal.ngc", Content: bytes.NewReader(content),
				ExpectedSize: int64(len(content)), IdempotencyKey: "cancel-terminal-upload-" + test.keySuffix,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.CommitImport(ctx, session.ID, CommitImportInput{
				ExpectedArtifactIDs: []string{artifact.ID}, SavePartialDraft: test.partial,
				IdempotencyKey: "cancel-terminal-commit-" + test.keySuffix,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.CancelImport(ctx, session.ID, "cancel-terminal-cancel-"+test.keySuffix); !domain.IsErrorCode(err, domain.CodeInvalidSetupState) {
				t.Fatalf("cancel published terminal error = %v", err)
			}
			persisted, err := h.service.GetImport(ctx, session.ID)
			if err != nil || persisted.State != test.expected || persisted.SetupID == "" {
				t.Fatalf("published terminal session = %+v, %v", persisted, err)
			}
		})
	}
}

func TestCancelImportInterruptsStreamingAndPublishesNoSetup(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Отмена", IdempotencyKey: "cancel-start"})
	if err != nil {
		t.Fatal(err)
	}
	reader := newPausedGCodeReader(4 << 20)
	uploadDone := make(chan error, 1)
	go func() {
		_, uploadErr := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
			Role: domain.ArtifactRoleProgram, DisplayName: "huge.ngc", Content: reader,
			ExpectedSize: -1, IdempotencyKey: "cancel-upload",
		})
		uploadDone <- uploadErr
	}()
	select {
	case <-reader.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach the cancellation checkpoint")
	}
	cancelled, err := h.service.CancelImport(ctx, session.ID, "cancel-session")
	if err != nil {
		t.Fatal(err)
	}
	close(reader.release)
	select {
	case uploadErr := <-uploadDone:
		if !domain.IsErrorCode(uploadErr, domain.CodeJobCancelled) {
			t.Fatalf("upload cancellation error = %v", uploadErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled upload did not stop")
	}
	if cancelled.State != domain.ImportStateCancelled || cancelled.SetupID != "" || cancelled.Bytes != 0 {
		t.Fatalf("cancelled session = %+v", cancelled)
	}
	if replay, replayErr := h.service.CancelImport(ctx, session.ID, "cancel-session"); replayErr != nil || replay.State != domain.ImportStateCancelled {
		t.Fatalf("cancel replay = %+v, %v", replay, replayErr)
	}
	var setups, objects, activeJobs int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM setups").Scan(&setups); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM storage_objects").Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE import_session_id = ? AND state NOT IN ('failed','cancelled','succeeded','conflict')", session.ID).Scan(&activeJobs); err != nil {
		t.Fatal(err)
	}
	if setups != 0 || objects != 0 || activeJobs != 0 {
		t.Fatalf("after cancel setups=%d objects=%d activeJobs=%d", setups, objects, activeJobs)
	}
}

func TestImportCancellationMonitorStopsStorageWork(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Monitor", IdempotencyKey: "monitor-start"})
	if err != nil {
		t.Fatal(err)
	}
	operationCtx, stop := h.service.monitorImportContext(ctx, session.ID)
	defer stop()
	if _, err := h.service.CancelImport(ctx, session.ID, "monitor-cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-operationCtx.Done():
		if !errors.Is(operationCtx.Err(), context.Canceled) {
			t.Fatalf("monitored context error = %v", operationCtx.Err())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("persisted cancellation did not stop monitored storage work")
	}
}

func TestArtifactMutationAtomicityRevisionVersionAndSetupSheet(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Операции", "artifact-create")
	added, err := h.service.AddPrograms(ctx, setup.ID, AddProgramsInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "artifact-add-two",
		Programs: []UploadArtifactInput{
			{DisplayName: "first.ngc", Content: strings.NewReader("G0 X0\n"), ExpectedSize: 6},
			{DisplayName: "second.nc", Content: strings.NewReader("G1 X1\n"), ExpectedSize: 6},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Revision != setup.Revision+1 || len(added.Artifacts) != 2 || added.Artifacts[0].Primary || added.Artifacts[1].Primary {
		t.Fatalf("added programs = %+v", added)
	}
	first := added.Artifacts[0]
	selected, err := h.service.SetPrimaryProgram(ctx, setup.ID, first.ID, SetPrimaryInput{
		ExpectedRevision: added.Revision, ExpectedVersion: first.Version, IdempotencyKey: "artifact-primary",
	})
	if err != nil || !selected.Artifacts[0].Primary {
		t.Fatalf("set primary = %+v, %v", selected, err)
	}
	h.markReady(t, setup.ID, selected.Revision)

	failedReader := io.MultiReader(strings.NewReader("G1 "), errorReader{err: errors.New("connection dropped")})
	if _, err := h.service.ReplaceArtifact(ctx, setup.ID, first.ID, ReplaceArtifactInput{
		ExpectedRevision: selected.Revision, ExpectedVersion: first.Version,
		Content: failedReader, ExpectedSize: -1, IdempotencyKey: "artifact-replace-broken",
	}); err == nil {
		t.Fatal("interrupted replacement unexpectedly succeeded")
	}
	unchanged, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != selected.Revision || unchanged.Status != domain.SetupStatusReady ||
		unchanged.Artifacts[0].Version != first.Version || unchanged.Artifacts[0].ID != first.ID {
		t.Fatalf("interrupted replacement changed aggregate: %+v", unchanged)
	}

	replacement := []byte("G2 X20\n")
	replaced, err := h.service.ReplaceArtifact(ctx, setup.ID, first.ID, ReplaceArtifactInput{
		ExpectedRevision: selected.Revision, ExpectedVersion: first.Version,
		Content: bytes.NewReader(replacement), ExpectedSize: int64(len(replacement)), IdempotencyKey: "artifact-replace-ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedFirst := findArtifact(t, replaced, first.ID)
	if replaced.Revision != selected.Revision+1 || replaced.Status != domain.SetupStatusDraft ||
		updatedFirst.ID != first.ID || updatedFirst.DisplayName != first.DisplayName || !updatedFirst.Primary || updatedFirst.Version == first.Version {
		t.Fatalf("replaced setup = %+v", replaced)
	}
	if replay, replayErr := h.service.ReplaceArtifact(ctx, setup.ID, first.ID, ReplaceArtifactInput{
		ExpectedRevision: selected.Revision, ExpectedVersion: first.Version,
		Content: bytes.NewReader(replacement), ExpectedSize: int64(len(replacement)), IdempotencyKey: "artifact-replace-ok",
	}); replayErr != nil || replay.Revision != replaced.Revision {
		t.Fatalf("replace replay = %+v, %v", replay, replayErr)
	}
	content, err := h.service.ReadArtifactAll(ctx, setup.ID, first.ID, 1024)
	if err != nil || !bytes.Equal(content.Data, replacement) {
		t.Fatalf("replacement content = %q, %v", content.Data, err)
	}

	renamed, err := h.service.RenameArtifact(ctx, setup.ID, first.ID, RenameArtifactInput{
		ExpectedRevision: replaced.Revision, ExpectedVersion: updatedFirst.Version,
		DisplayName: "first-renamed.ngc", IdempotencyKey: "artifact-rename",
	})
	if err != nil || findArtifact(t, renamed, first.ID).DisplayName != "first-renamed.ngc" {
		t.Fatalf("renamed setup = %+v, %v", renamed, err)
	}
	html := []byte("<!doctype html><html><body><h1>Setup</h1></body></html>")
	withSheet, err := h.service.PutSetupSheet(ctx, setup.ID, ReplaceArtifactInput{
		ExpectedRevision: renamed.Revision, DisplayName: "instructions.html",
		Content: bytes.NewReader(html), ExpectedSize: int64(len(html)), IdempotencyKey: "artifact-sheet-add",
	})
	if err != nil {
		t.Fatal(err)
	}
	sheet := findRole(t, withSheet, domain.ArtifactRoleSetupSheet)
	if replay, replayErr := h.service.PutSetupSheet(ctx, setup.ID, ReplaceArtifactInput{
		ExpectedRevision: renamed.Revision, DisplayName: "instructions.html",
		Content: bytes.NewReader(html), ExpectedSize: int64(len(html)), IdempotencyKey: "artifact-sheet-add",
	}); replayErr != nil || replay.Revision != withSheet.Revision {
		t.Fatalf("sheet replay = %+v, %v", replay, replayErr)
	}
	pdf := []byte("%PDF-1.4\n%%EOF\n")
	withPDF, err := h.service.PutSetupSheet(ctx, setup.ID, ReplaceArtifactInput{
		ExpectedRevision: withSheet.Revision, ExpectedVersion: sheet.Version, DisplayName: "instructions.pdf",
		Content: bytes.NewReader(pdf), ExpectedSize: int64(len(pdf)), IdempotencyKey: "artifact-sheet-replace",
	})
	if err != nil {
		t.Fatal(err)
	}
	replacedSheet := findRole(t, withPDF, domain.ArtifactRoleSetupSheet)
	if replacedSheet.ID != sheet.ID || replacedSheet.MediaType != mediaTypePDF || replacedSheet.Version == sheet.Version {
		t.Fatalf("replaced sheet = %+v", replacedSheet)
	}
	withoutSheet, err := h.service.DeleteSetupSheet(ctx, setup.ID, DeleteArtifactInput{
		ExpectedRevision: withPDF.Revision, ExpectedVersion: replacedSheet.Version, IdempotencyKey: "artifact-sheet-delete",
	})
	if err != nil || hasRole(withoutSheet, domain.ArtifactRoleSetupSheet) {
		t.Fatalf("delete sheet = %+v, %v", withoutSheet, err)
	}
	currentFirst := findArtifact(t, withoutSheet, first.ID)
	if _, err := h.service.DeleteArtifact(ctx, setup.ID, first.ID, DeleteArtifactInput{
		ExpectedRevision: withoutSheet.Revision, ExpectedVersion: currentFirst.Version, IdempotencyKey: "artifact-delete-primary-no-choice",
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("primary delete without explicit choice error = %v", err)
	}
	remainingID := withoutSheet.Artifacts[1].ID
	withoutPrimary, err := h.service.DeleteArtifact(ctx, setup.ID, first.ID, DeleteArtifactInput{
		ExpectedRevision: withoutSheet.Revision, ExpectedVersion: currentFirst.Version,
		ReplacementPrimaryArtifactID: remainingID, IdempotencyKey: "artifact-delete-primary",
	})
	if err != nil || len(withoutPrimary.Artifacts) != 1 || !withoutPrimary.Artifacts[0].Primary || withoutPrimary.Artifacts[0].ID != remainingID {
		t.Fatalf("delete primary = %+v, %v", withoutPrimary, err)
	}
}

func TestArtifactStaleRevisionDoesNotLeavePublishedReference(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Конфликт", "conflict-create")
	first, err := h.service.AddPrograms(ctx, setup.ID, AddProgramsInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "conflict-first",
		Programs: []UploadArtifactInput{{DisplayName: "main.ngc", Content: strings.NewReader("G0\n"), ExpectedSize: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.AddPrograms(ctx, setup.ID, AddProgramsInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "conflict-stale",
		Programs: []UploadArtifactInput{{DisplayName: "stale.ngc", Content: strings.NewReader("G1\n"), ExpectedSize: 3}},
	}); !domain.IsErrorCode(err, domain.CodeRevisionConflict) {
		t.Fatalf("stale add error = %v", err)
	}
	if _, err := h.service.AddPrograms(ctx, setup.ID, AddProgramsInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "conflict-stale",
		Programs: []UploadArtifactInput{{DisplayName: "stale.ngc", Content: strings.NewReader("G1\n"), ExpectedSize: 3}},
	}); !domain.IsErrorCode(err, domain.CodeRevisionConflict) {
		t.Fatalf("stale add replay error = %v", err)
	}
	loaded, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || loaded.Revision != first.Revision || len(loaded.Artifacts) != 1 {
		t.Fatalf("setup after stale add = %+v, %v", loaded, err)
	}
	var objects, refs int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*), COALESCE(SUM(ref_count), 0) FROM storage_objects").Scan(&objects, &refs); err != nil {
		t.Fatal(err)
	}
	if objects != 1 || refs != 1 {
		t.Fatalf("stale mutation left objects=%d refs=%d", objects, refs)
	}
}

func TestAddProgramsRejectsWholeBatchWhenOneUploadIsInvalid(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Atomic batch", "batch-create")
	if _, err := h.service.AddPrograms(ctx, setup.ID, AddProgramsInput{
		ExpectedRevision: setup.Revision, IdempotencyKey: "batch-add",
		Programs: []UploadArtifactInput{
			{DisplayName: "good.ngc", Content: strings.NewReader("G0 X0\n"), ExpectedSize: 6},
			{DisplayName: "bad.ngc", Content: bytes.NewReader([]byte{'G', 0, '1'}), ExpectedSize: 3},
		},
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("atomic batch error = %v", err)
	}
	loaded, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil || loaded.Revision != setup.Revision || len(loaded.Artifacts) != 0 {
		t.Fatalf("setup after rejected batch = %+v, %v", loaded, err)
	}
	var objects, activeJournals int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM storage_objects").Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM operation_journal WHERE state IN ('intent','storage_applied','db_applied')").Scan(&activeJournals); err != nil {
		t.Fatal(err)
	}
	if objects != 0 || activeJournals != 0 {
		t.Fatalf("rejected batch left objects=%d activeJournals=%d", objects, activeJournals)
	}
}

func TestAddProgramsStreamConsumesPartsSequentiallyAndRollsBackSourceFailure(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Streaming batch", "streaming-batch-create")
	firstExhausted := false
	added, err := h.service.AddProgramsStream(ctx, setup.ID, AddProgramsStreamInput{
		ExpectedRevision: setup.Revision,
		IdempotencyKey:   "streaming-batch-add",
		Source: func(yield func(UploadArtifactInput) error) error {
			first := &eofTrackingReader{source: strings.NewReader("G0 X0\n"), exhausted: &firstExhausted}
			if err := yield(UploadArtifactInput{
				DisplayName: "first-stream.ngc", Content: first, ExpectedSize: 6,
			}); err != nil {
				return err
			}
			if !firstExhausted {
				return errors.New("source advanced before the previous part was consumed")
			}
			return yield(UploadArtifactInput{
				DisplayName: "second-stream.ngc", Content: strings.NewReader("G1 X1\n"), ExpectedSize: 6,
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !firstExhausted || added.Revision != setup.Revision+1 || len(added.Artifacts) != 2 {
		t.Fatalf("streamed add = %+v, firstExhausted=%v", added, firstExhausted)
	}

	var objectsBefore int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM storage_objects").Scan(&objectsBefore); err != nil {
		t.Fatal(err)
	}
	sourceFailure := errors.New("multipart stream stopped")
	if _, err := h.service.AddProgramsStream(ctx, setup.ID, AddProgramsStreamInput{
		ExpectedRevision: added.Revision,
		IdempotencyKey:   "streaming-batch-source-failure",
		Source: func(yield func(UploadArtifactInput) error) error {
			if err := yield(UploadArtifactInput{
				DisplayName: "never-published.ngc", Content: strings.NewReader("G2 X2\n"), ExpectedSize: 6,
			}); err != nil {
				return err
			}
			return sourceFailure
		},
	}); !errors.Is(err, sourceFailure) {
		t.Fatalf("source failure = %v", err)
	}
	unchanged, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	var objectsAfter, activeJournals int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM storage_objects").Scan(&objectsAfter); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM operation_journal WHERE state IN ('intent','storage_applied','db_applied')").Scan(&activeJournals); err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != added.Revision || len(unchanged.Artifacts) != 2 || objectsAfter != objectsBefore || activeJournals != 0 {
		t.Fatalf("failed stream changed setup=%+v objects=%d/%d activeJournals=%d", unchanged, objectsAfter, objectsBefore, activeJournals)
	}
}

func TestPrepareArtifactHonorsHeavyWorkLimitBeforeReading(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.MaxParallelHeavyJobs = 1 })
	ctx := context.Background()
	entered := make(chan struct{})
	releaseReader := make(chan struct{})
	type prepareResult struct {
		artifact *preparedArtifact
		err      error
	}
	firstDone := make(chan prepareResult, 1)
	go func() {
		artifact, err := h.service.prepareArtifact(ctx, UploadArtifactInput{
			Role: domain.ArtifactRoleProgram, DisplayName: "first-heavy.ngc",
			Content:      &gateReader{source: strings.NewReader("G0 X0\n"), entered: entered, release: releaseReader},
			ExpectedSize: 6,
		})
		firstDone <- prepareResult{artifact: artifact, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first upload did not acquire the heavy-work slot")
	}
	readerReleased := false
	defer func() {
		if !readerReleased {
			close(releaseReader)
		}
	}()

	secondRead := false
	limitedCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err := h.service.prepareArtifact(limitedCtx, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "second-heavy.ngc",
		Content:      &readTrackingReader{source: strings.NewReader("G1 X1\n"), read: &secondRead},
		ExpectedSize: 6,
	})
	cancel()
	if !domain.IsErrorCode(err, domain.CodeJobCancelled) || secondRead {
		t.Fatalf("limited upload error=%v contentRead=%v", err, secondRead)
	}
	close(releaseReader)
	readerReleased = true
	select {
	case first := <-firstDone:
		if first.err != nil {
			t.Fatal(first.err)
		}
		h.service.cleanupPreparedObjects(ctx, []preparedArtifact{*first.artifact})
	case <-time.After(5 * time.Second):
		t.Fatal("first upload did not finish after releasing its reader")
	}
}

func TestDeleteLastPrimaryRequiresExplicitLeaveUnassigned(t *testing.T) {
	h := newLifecycleTestHarness(t, nil)
	ctx := context.Background()
	setup := h.createSetup(t, "Primary choice", "primary-choice-create")
	added, err := h.service.AddPrograms(ctx, setup.ID, AddProgramsInput{
		ExpectedRevision: setup.Revision,
		IdempotencyKey:   "primary-choice-add",
		Programs: []UploadArtifactInput{{
			DisplayName: "only.ngc", Content: strings.NewReader("G0\n"), ExpectedSize: 3,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := added.Artifacts[0]
	if !primary.Primary {
		t.Fatalf("single added program is not primary: %+v", primary)
	}
	if _, err := h.service.DeleteArtifact(ctx, setup.ID, primary.ID, DeleteArtifactInput{
		ExpectedRevision: added.Revision, ExpectedVersion: primary.Version,
		IdempotencyKey: "primary-choice-missing",
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("implicit primary deletion error = %v", err)
	}
	if _, err := h.service.DeleteArtifact(ctx, setup.ID, primary.ID, DeleteArtifactInput{
		ExpectedRevision: added.Revision, ExpectedVersion: primary.Version,
		IdempotencyKey: "primary-choice-missing",
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("implicit primary deletion replay error = %v", err)
	}
	var failedClaimState string
	if err := h.db.SQL().QueryRowContext(ctx, `
		SELECT state FROM idempotency_requests WHERE library_id = ? AND key = ?`,
		h.roots.LibraryID(), "primary-choice-missing").Scan(&failedClaimState); err != nil {
		t.Fatal(err)
	}
	if failedClaimState != "failed" {
		t.Fatalf("failed delete idempotency state = %q", failedClaimState)
	}
	if _, err := h.service.DeleteArtifact(ctx, setup.ID, primary.ID, DeleteArtifactInput{
		ExpectedRevision: added.Revision, ExpectedVersion: primary.Version,
		LeavePrimaryUnassigned: true, IdempotencyKey: "primary-choice-missing-last-confirmation",
	}); !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("last program deletion without confirmation error = %v", err)
	}
	withoutPrograms, err := h.service.DeleteArtifact(ctx, setup.ID, primary.ID, DeleteArtifactInput{
		ExpectedRevision: added.Revision, ExpectedVersion: primary.Version,
		LeavePrimaryUnassigned: true, ConfirmDeleteLastProgram: true,
		IdempotencyKey: "primary-choice-explicit-empty",
	})
	if err != nil {
		t.Fatal(err)
	}
	if withoutPrograms.Revision != added.Revision+1 || len(withoutPrograms.Artifacts) != 0 {
		t.Fatalf("explicit unassigned primary deletion = %+v", withoutPrograms)
	}
}

func TestImportLimitDuplicateNamesExcludeAndRecovery(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.ImportTotalLimit = 12 })
	ctx := context.Background()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Лимит", IdempotencyKey: "limit-start"})
	if err != nil {
		t.Fatal(err)
	}
	firstBytes := []byte("G0 X0\n")
	first, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "Case.NGC", Content: bytes.NewReader(firstBytes),
		ExpectedSize: int64(len(firstBytes)), IdempotencyKey: "limit-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "case.ngc", Content: strings.NewReader("G1\n"),
		ExpectedSize: 3, IdempotencyKey: "limit-duplicate",
	}); !domain.IsErrorCode(err, domain.CodeNameConflict) {
		t.Fatalf("semantic duplicate error = %v", err)
	}
	if _, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "large.ngc", Content: strings.NewReader("G123456789\n"),
		ExpectedSize: 11, IdempotencyKey: "limit-large",
	}); !domain.IsErrorCode(err, domain.CodeImportTooLarge) {
		t.Fatalf("import limit error = %v", err)
	}
	excluded, err := h.service.ExcludeImportArtifact(ctx, session.ID, first.ID, "limit-exclude")
	if err != nil || excluded.Bytes != 0 || excluded.Artifacts[0].State != domain.ImportArtifactExcluded {
		t.Fatalf("exclude = %+v, %v", excluded, err)
	}
	if _, err := h.db.SQL().ExecContext(ctx, `
		UPDATE import_artifacts SET state = 'uploading', error_code = NULL WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().ExecContext(ctx, `
		UPDATE jobs SET state = 'failed', error_code = 'PROCESS_INTERRUPTED', finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE import_session_id = ?`, session.ID); err != nil {
		t.Fatal(err)
	}
	count, err := h.service.RecoverImports(ctx)
	if err != nil || count != 1 {
		t.Fatalf("RecoverImports = %d, %v", count, err)
	}
	recovered, err := h.service.GetImport(ctx, session.ID)
	if err != nil || recovered.Artifacts[0].State != domain.ImportArtifactFailed || recovered.Artifacts[0].ErrorCode != domain.CodeUploadIncomplete {
		t.Fatalf("recovered import = %+v, %v", recovered, err)
	}
}

func TestExpiredImportCleanupDetachesAndCollectsObjects(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.ImportSessionExpiry = time.Second })
	ctx := context.Background()
	session, err := h.service.StartImport(ctx, StartImportInput{Name: "Expired", IdempotencyKey: "expired-start"})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("G0 X0\n")
	if _, err := h.service.UploadImportArtifact(ctx, session.ID, UploadArtifactInput{
		Role: domain.ArtifactRoleProgram, DisplayName: "expired.ngc", Content: bytes.NewReader(content),
		ExpectedSize: int64(len(content)), IdempotencyKey: "expired-upload",
	}); err != nil {
		t.Fatal(err)
	}
	h.clockNS.Add(int64(2 * time.Second))
	cleaned, err := h.service.CleanupExpiredImports(ctx)
	if err != nil || cleaned != 1 {
		t.Fatalf("CleanupExpiredImports = %d, %v", cleaned, err)
	}
	loaded, err := h.service.GetImport(ctx, session.ID)
	if err != nil || loaded.State != domain.ImportStateCancelled || loaded.Bytes != 0 {
		t.Fatalf("expired session = %+v, %v", loaded, err)
	}
	var objects, setups int
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM storage_objects").Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM setups").Scan(&setups); err != nil {
		t.Fatal(err)
	}
	if objects != 0 || setups != 0 {
		t.Fatalf("expired cleanup objects=%d setups=%d", objects, setups)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type eofTrackingReader struct {
	source    io.Reader
	exhausted *bool
}

func (r *eofTrackingReader) Read(buffer []byte) (int, error) {
	count, err := r.source.Read(buffer)
	if errors.Is(err, io.EOF) {
		*r.exhausted = true
	}
	return count, err
}

type gateReader struct {
	source  io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gateReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.source.Read(buffer)
}

type readTrackingReader struct {
	source io.Reader
	read   *bool
}

func (r *readTrackingReader) Read(buffer []byte) (int, error) {
	*r.read = true
	return r.source.Read(buffer)
}

type pausedGCodeReader struct {
	remaining int
	emitted   int
	reached   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newPausedGCodeReader(size int) *pausedGCodeReader {
	return &pausedGCodeReader{remaining: size, reached: make(chan struct{}), release: make(chan struct{})}
}

func (r *pausedGCodeReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if r.emitted >= 1<<20 {
		r.once.Do(func() { close(r.reached) })
		<-r.release
	}
	count := len(buffer)
	if count > r.remaining {
		count = r.remaining
	}
	pattern := []byte("G1 X1\n")
	for index := 0; index < count; index++ {
		buffer[index] = pattern[(r.emitted+index)%len(pattern)]
	}
	r.remaining -= count
	r.emitted += count
	return count, nil
}

func findArtifact(t *testing.T, setup *domain.Setup, artifactID string) domain.Artifact {
	t.Helper()
	for _, artifact := range setup.Artifacts {
		if artifact.ID == artifactID {
			return artifact
		}
	}
	t.Fatalf("artifact %s not found in %+v", artifactID, setup.Artifacts)
	return domain.Artifact{}
}

func findRole(t *testing.T, setup *domain.Setup, role domain.ArtifactRole) domain.Artifact {
	t.Helper()
	for _, artifact := range setup.Artifacts {
		if artifact.Role == role {
			return artifact
		}
	}
	t.Fatalf("role %s not found in %+v", role, setup.Artifacts)
	return domain.Artifact{}
}

func hasRole(setup *domain.Setup, role domain.ArtifactRole) bool {
	for _, artifact := range setup.Artifacts {
		if artifact.Role == role {
			return true
		}
	}
	return false
}
