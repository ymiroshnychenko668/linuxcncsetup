package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

const (
	uploadProgressByteStep = int64(1 << 20)
	uploadProgressInterval = 250 * time.Millisecond
)

type uploadJobDescriptor struct {
	Operation        UploadJobOperation `json:"operation"`
	ExpectedRevision domain.Revision    `json:"expectedRevision"`
	ArtifactID       string             `json:"artifactId,omitempty"`
	ExpectedVersion  string             `json:"expectedVersion,omitempty"`
	Items            []UploadJobItem    `json:"items"`
}

type uploadJobEnvelope struct {
	Upload   *uploadJobDescriptor `json:"upload,omitempty"`
	Progress domain.JobProgress   `json:"uploadProgress"`
	Digests  []string             `json:"contentDigests,omitempty"`
	Setup    *domain.Setup        `json:"setup,omitempty"`
}

// PrepareUploadJob persists the exact aggregate/version/name/size contract
// before the HTTP request body starts. Replaying the same idempotency key
// returns the same job, while a changed contract conflicts.
func (s *Service) PrepareUploadJob(ctx context.Context, setupID string, input PrepareUploadJobInput) (*domain.Job, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	descriptor := uploadJobDescriptor{
		Operation: input.Operation, ExpectedRevision: input.ExpectedRevision,
		ArtifactID: input.ArtifactID, ExpectedVersion: input.ExpectedVersion,
	}
	kind := domain.JobKindAddPrograms
	seen := make(map[string]struct{})
	var total int64
	for _, item := range input.Items {
		name, normalizeErr := domain.NormalizeArtifactName(item.DisplayName)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		if item.Size < 0 || item.Size > math.MaxInt64-total {
			return nil, domain.NewError(domain.CodeInvalidContent, "upload size is invalid")
		}
		key, keyErr := domain.ArtifactNameKey(name)
		if keyErr != nil {
			return nil, keyErr
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, domain.NewError(domain.CodeNameConflict, "upload names must be unique")
		}
		seen[key] = struct{}{}
		total += item.Size
		descriptor.Items = append(descriptor.Items, UploadJobItem{DisplayName: name, Size: item.Size})
	}
	if len(descriptor.Items) == 0 {
		return nil, domain.NewError(domain.CodeInvalidContent, "at least one upload item is required")
	}

	switch input.Operation {
	case UploadJobAddPrograms:
		for _, item := range descriptor.Items {
			if err := s.gcode.ValidateExtension(item.DisplayName); err != nil {
				return nil, err
			}
		}
	case UploadJobReplaceProgram:
		kind = domain.JobKindReplaceProgram
		if len(descriptor.Items) != 1 || input.ArtifactID == "" {
			return nil, domain.NewError(domain.CodeInvalidContent, "program replacement requires one artifact")
		}
		if err := domain.ValidateID(input.ArtifactID); err != nil {
			return nil, err
		}
		if input.ExpectedVersion == "" {
			return nil, domain.NewError(domain.CodeArtifactChanged, "program version is required")
		}
	case UploadJobPutSetupSheet:
		kind = domain.JobKindUpdateSetupSheet
		if len(descriptor.Items) != 1 {
			return nil, domain.NewError(domain.CodeInvalidContent, "setup sheet upload requires one artifact")
		}
		if _, mediaErr := setupSheetMediaType(descriptor.Items[0].DisplayName); mediaErr != nil {
			return nil, mediaErr
		}
	default:
		return nil, domain.NewError(domain.CodeInvalidContent, "upload operation is invalid")
	}

	operation := "prepareUploadJob:" + setupID
	hash, err := idempotencyRequestHash(operation, descriptor)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, operation, hash)
	if err != nil {
		return nil, err
	}
	var replay domain.Job
	if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
		return nil, replayErr
	} else if ok {
		if err := tx.Commit(); err != nil {
			return nil, databaseError(err)
		}
		return s.GetJob(ctx, replay.ID)
	}
	if err := beginLifecycleMutation(ctx, tx); err != nil {
		return nil, err
	}
	fail := func(operationErr error) (*domain.Job, error) {
		return nil, finishLifecycleFailure(ctx, tx, claim, operationErr)
	}
	// Mutable aggregate/version preconditions are intentionally checked only
	// after idempotency replay. A retry must return its original durable job
	// even when that job has already advanced the setup revision.
	setup, err := s.loadSetup(ctx, tx, setupID, true)
	if err != nil {
		return fail(err)
	}
	if _, _, err := domain.NextMutation(setup.Status, setup.Revision, input.ExpectedRevision); err != nil {
		return fail(err)
	}
	switch descriptor.Operation {
	case UploadJobReplaceProgram:
		record, loadErr := s.loadArtifact(ctx, tx, setupID, descriptor.ArtifactID)
		if loadErr != nil {
			return fail(loadErr)
		}
		if record.Role != domain.ArtifactRoleProgram || record.Version != descriptor.ExpectedVersion {
			return fail(domain.NewError(domain.CodeArtifactChanged, "program version has changed"))
		}
		if descriptor.Items[0].DisplayName != record.DisplayName {
			return fail(domain.NewError(domain.CodeInvalidContent, "program replacement preserves the display name"))
		}
	case UploadJobPutSetupSheet:
		var existing *domain.Artifact
		for index := range setup.Artifacts {
			if setup.Artifacts[index].Role == domain.ArtifactRoleSetupSheet {
				existing = &setup.Artifacts[index]
				break
			}
		}
		if existing == nil && descriptor.ExpectedVersion != "" || existing != nil && (descriptor.ExpectedVersion == "" || existing.Version != descriptor.ExpectedVersion) {
			return fail(domain.NewError(domain.CodeArtifactChanged, "setup sheet version has changed"))
		}
	}
	job, err := s.insertJobTx(ctx, tx, kind, setupID, "", &total)
	if err != nil {
		return fail(err)
	}
	job.Progress.TotalBytes = total
	job.Progress.TotalItems = int64(len(descriptor.Items))
	envelope := uploadJobEnvelope{Upload: &descriptor, Progress: job.Progress}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET result_json = ? WHERE id = ?`, string(payload), job.ID); err != nil {
		return fail(databaseError(err))
	}
	job.Result = payload
	if err := finishIdempotencyTx(ctx, tx, claim, 201, job, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return job, nil
}

// RunUploadJob consumes the body for one prepared job and returns its durable
// terminal snapshot. Cancellation or request abort never publishes a partial
// setup revision.
func (s *Service) RunUploadJob(ctx context.Context, jobID string, input RunUploadJobInput) (*domain.Job, error) {
	if err := domain.ValidateID(jobID); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	operation := "runUploadJob:" + jobID
	hash, err := idempotencyRequestHash(operation, map[string]string{"jobId": jobID})
	if err != nil {
		return nil, err
	}
	// Idempotency keys are library-global in the schema. Scope the caller key
	// deterministically to this run transition so the same key can safely be
	// used for prepare and run, and retried after a lost response.
	scopedKeyBytes := sha256.Sum256([]byte(operation + "\x00" + input.IdempotencyKey))
	scopedKey := hex.EncodeToString(scopedKeyBytes[:])
	claim, err := s.claimIdempotency(ctx, scopedKey, operation, hash)
	if err != nil {
		return nil, err
	}
	var replay domain.Job
	if ok, replayErr := claim.replayInto(&replay); replayErr != nil {
		return nil, replayErr
	} else if ok {
		current, loadErr := s.GetJob(ctx, replay.ID)
		if loadErr != nil {
			return nil, loadErr
		}
		if verifyErr := verifyUploadReplay(ctx, current.Result, input); verifyErr != nil {
			return nil, verifyErr
		}
		return current, nil
	}
	finishClaim := func(result *domain.Job, operationErr error) error {
		finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.finishIdempotency(finishCtx, claim, 200, result, operationErr)
	}
	finishFailure := func(operationErr error) error {
		if finishErr := finishClaim(nil, operationErr); finishErr != nil {
			return finishErr
		}
		return operationErr
	}
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		return nil, finishFailure(err)
	}
	if job.State.Terminal() {
		if verifyErr := verifyUploadReplay(ctx, job.Result, input); verifyErr != nil {
			return nil, finishFailure(verifyErr)
		}
		return s.finalizeUploadRun(jobID, claim, uploadJobEnvelope{}, nil, nil)
	}
	if job.State != domain.JobStateQueued {
		operationErr := domain.NewError(domain.CodeUploadIncomplete, "upload job already has a writer")
		return nil, finishFailure(operationErr)
	}
	var envelope uploadJobEnvelope
	if err := json.Unmarshal(job.Result, &envelope); err != nil || envelope.Upload == nil {
		operationErr := domain.NewError(domain.CodeInvalidContent, "job is not an upload job")
		return nil, finishFailure(operationErr)
	}
	operationCtx, cancel, err := s.beginInlineJob(ctx, jobID)
	if err != nil {
		return nil, finishFailure(err)
	}
	defer s.endInlineJob(jobID, cancel)
	started := time.Now()
	defer s.logJobResult(jobID, started)

	if err := s.markJobRunning(operationCtx, jobID); err != nil {
		return s.finalizeUploadRun(jobID, claim, envelope, nil, err)
	}
	reporter := &uploadProgressReporter{service: s, jobID: jobID, envelope: &envelope, lastAt: time.Now()}
	var setup *domain.Setup
	descriptor := envelope.Upload
	workErr := func() error {
		switch descriptor.Operation {
		case UploadJobAddPrograms:
			if input.Source == nil {
				return domain.NewError(domain.CodeInvalidContent, "multipart upload content is required")
			}
			index := 0
			returnValue, mutationErr := s.AddProgramsStream(operationCtx, job.SetupID, AddProgramsStreamInput{
				ExpectedRevision: descriptor.ExpectedRevision,
				IdempotencyKey:   "upload-" + jobID,
				finalizeTx: func(finalizeCtx context.Context, tx *sql.Tx, completed *domain.Setup) error {
					return s.finalizeUploadJobTx(finalizeCtx, tx, jobID, claim, envelope, completed, envelope.Digests)
				},
				Source: func(yield func(UploadArtifactInput) error) error {
					return input.Source(func(item UploadArtifactInput) error {
						if index >= len(descriptor.Items) {
							return domain.NewError(domain.CodeInvalidContent, "upload has more items than prepared")
						}
						bound := descriptor.Items[index]
						item.Role = domain.ArtifactRoleProgram
						item.DisplayName = bound.DisplayName
						item.ExpectedSize = bound.Size
						hasher := sha256.New()
						item.Content = io.TeeReader(&uploadProgressReader{source: item.Content, report: reporter.addBytes}, hasher)
						if err := yield(item); err != nil {
							return err
						}
						envelope.Digests = append(envelope.Digests, hex.EncodeToString(hasher.Sum(nil)))
						index++
						return reporter.itemDone(operationCtx)
					})
				},
			})
			if mutationErr == nil && index != len(descriptor.Items) {
				return domain.NewError(domain.CodeUploadIncomplete, "upload has fewer items than prepared")
			}
			setup = returnValue
			return mutationErr
		case UploadJobReplaceProgram, UploadJobPutSetupSheet:
			if input.Content == nil {
				return domain.NewError(domain.CodeInvalidContent, "upload content is required")
			}
			item := descriptor.Items[0]
			hasher := sha256.New()
			content := io.TeeReader(&uploadProgressReader{source: input.Content, report: reporter.addBytes}, hasher)
			finalizeTx := func(finalizeCtx context.Context, tx *sql.Tx, completed *domain.Setup) error {
				digest := hex.EncodeToString(hasher.Sum(nil))
				return s.finalizeUploadJobTx(finalizeCtx, tx, jobID, claim, envelope, completed, []string{digest})
			}
			var mutationErr error
			if descriptor.Operation == UploadJobReplaceProgram {
				setup, mutationErr = s.ReplaceArtifact(operationCtx, job.SetupID, descriptor.ArtifactID, ReplaceArtifactInput{
					ExpectedRevision: descriptor.ExpectedRevision, ExpectedVersion: descriptor.ExpectedVersion,
					DisplayName: item.DisplayName, Content: content, ExpectedSize: item.Size,
					IdempotencyKey: "upload-" + jobID, finalizeTx: finalizeTx,
				})
			} else {
				setup, mutationErr = s.PutSetupSheet(operationCtx, job.SetupID, ReplaceArtifactInput{
					ExpectedRevision: descriptor.ExpectedRevision, ExpectedVersion: descriptor.ExpectedVersion,
					DisplayName: item.DisplayName, Content: content, ExpectedSize: item.Size,
					IdempotencyKey: "upload-" + jobID, finalizeTx: finalizeTx,
				})
			}
			envelope.Digests = append(envelope.Digests, hex.EncodeToString(hasher.Sum(nil)))
			return mutationErr
		default:
			return domain.NewError(domain.CodeInvalidContent, "upload operation is invalid")
		}
	}()
	if workErr != nil {
		reporter.force(context.Background())
		return s.finalizeUploadRun(jobID, claim, envelope, setup, workErr)
	}
	terminal, err := s.GetJob(context.Background(), jobID)
	if err != nil {
		return nil, err
	}
	if terminal.State != domain.JobStateSucceeded {
		return nil, databaseError(errors.New("upload mutation committed without a succeeded terminal job"))
	}
	return terminal, nil
}

func (s *Service) finalizeUploadJobTx(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	claim idempotencyClaim,
	envelope uploadJobEnvelope,
	setup *domain.Setup,
	digests []string,
) error {
	if envelope.Upload == nil || len(digests) != len(envelope.Upload.Items) {
		return domain.NewError(domain.CodeUploadIncomplete, "upload completion evidence is incomplete")
	}
	completed := envelope
	completed.Digests = append([]string(nil), digests...)
	completed.Setup = setup
	completed.Progress.CompletedBytes = completed.Progress.TotalBytes
	completed.Progress.CompletedItems = completed.Progress.TotalItems
	if err := s.finishJobTx(ctx, tx, jobID, completed, nil); err != nil {
		return err
	}
	terminal, err := s.getJobTx(ctx, tx, jobID)
	if err != nil {
		return err
	}
	return finishIdempotencyTx(ctx, tx, claim, 200, terminal, nil)
}

// finalizeUploadRun is the error/terminal counterpart of
// finalizeUploadJobTx. The job transition and the outer run-upload claim are
// committed together, so a crash cannot leave a stable failed/cancelled job
// paired with an abandoned in-progress idempotency request.
func (s *Service) finalizeUploadRun(
	jobID string,
	claim idempotencyClaim,
	envelope uploadJobEnvelope,
	setup *domain.Setup,
	workErr error,
) (_ *domain.Job, finalErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()
	terminal, err := s.getJobTx(ctx, tx, jobID)
	if err != nil {
		return nil, err
	}
	if !terminal.State.Terminal() {
		if workErr == nil {
			envelope.Setup = setup
		}
		if err := s.finishJobTx(ctx, tx, jobID, envelope, workErr); err != nil {
			return nil, err
		}
		terminal, err = s.getJobTx(ctx, tx, jobID)
		if err != nil {
			return nil, err
		}
	}
	if err := finishIdempotencyTx(ctx, tx, claim, 200, terminal, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return terminal, nil
}

func (s *Service) beginInlineJob(parent context.Context, jobID string) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(parent)
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	select {
	case <-s.closed:
		cancel()
		return nil, nil, domain.NewError(domain.CodeJobCancelled, "service is shutting down")
	default:
	}
	if _, exists := s.jobs[jobID]; exists {
		cancel()
		return nil, nil, domain.NewError(domain.CodeUploadIncomplete, "upload job already has a writer")
	}
	s.jobs[jobID] = cancel
	s.jobsWG.Add(1)
	return ctx, cancel, nil
}

func (s *Service) endInlineJob(jobID string, cancel context.CancelFunc) {
	cancel()
	s.jobsMu.Lock()
	delete(s.jobs, jobID)
	s.jobsMu.Unlock()
	s.jobsWG.Done()
}

func verifyUploadReplay(ctx context.Context, payload json.RawMessage, input RunUploadJobInput) error {
	var envelope uploadJobEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Upload == nil || len(envelope.Digests) != len(envelope.Upload.Items) {
		return domain.NewError(domain.CodeIdempotencyConflict, "the original upload content cannot be verified for replay")
	}
	verifyOne := func(index int, reader io.Reader) error {
		if reader == nil || index >= len(envelope.Upload.Items) {
			return domain.NewError(domain.CodeIdempotencyConflict, "upload replay content differs from the original request")
		}
		hasher := sha256.New()
		count, err := io.Copy(hasher, io.LimitReader(reader, envelope.Upload.Items[index].Size+1))
		if err != nil {
			return domain.WrapError(domain.CodeUploadIncomplete, "upload replay could not be read", err)
		}
		if err := ctx.Err(); err != nil {
			return domain.WrapError(domain.CodeJobCancelled, "upload replay was cancelled", err)
		}
		if count != envelope.Upload.Items[index].Size || hex.EncodeToString(hasher.Sum(nil)) != envelope.Digests[index] {
			return domain.NewError(domain.CodeIdempotencyConflict, "upload replay content differs from the original request")
		}
		return nil
	}
	if envelope.Upload.Operation != UploadJobAddPrograms {
		return verifyOne(0, input.Content)
	}
	if input.Source == nil {
		return domain.NewError(domain.CodeIdempotencyConflict, "upload replay content differs from the original request")
	}
	index := 0
	err := input.Source(func(item UploadArtifactInput) error {
		if err := verifyOne(index, item.Content); err != nil {
			return err
		}
		index++
		return nil
	})
	if err != nil {
		return err
	}
	if index != len(envelope.Upload.Items) {
		return domain.NewError(domain.CodeIdempotencyConflict, "upload replay content differs from the original request")
	}
	return nil
}

type uploadProgressReporter struct {
	service   *Service
	jobID     string
	envelope  *uploadJobEnvelope
	lastBytes int64
	lastAt    time.Time
}

func (r *uploadProgressReporter) addBytes(ctx context.Context, count int64) error {
	if count < 0 || r.envelope.Progress.CompletedBytes > math.MaxInt64-count {
		return domain.NewError(domain.CodeInvalidContent, "upload progress overflow")
	}
	r.envelope.Progress.CompletedBytes += count
	if r.envelope.Progress.CompletedBytes-r.lastBytes >= uploadProgressByteStep || time.Since(r.lastAt) >= uploadProgressInterval {
		return r.persist(ctx)
	}
	return nil
}

func (r *uploadProgressReporter) itemDone(ctx context.Context) error {
	r.envelope.Progress.CompletedItems++
	return r.persist(ctx)
}

func (r *uploadProgressReporter) force(ctx context.Context) { _ = r.persist(ctx) }

func (r *uploadProgressReporter) persist(ctx context.Context) error {
	payload, err := json.Marshal(r.envelope)
	if err != nil {
		return err
	}
	p := r.envelope.Progress
	fraction := float64(0)
	if p.TotalBytes > 0 {
		fraction = float64(p.CompletedBytes) / float64(p.TotalBytes)
	} else if p.TotalItems > 0 {
		fraction = float64(p.CompletedItems) / float64(p.TotalItems)
	}
	fraction = math.Max(0, math.Min(1, fraction))
	result, err := r.service.db.ExecContext(ctx, `
		UPDATE jobs SET progress = max(progress, ?), bytes_done = max(bytes_done, ?),
		       result_json = ?, updated_at = ?
		 WHERE library_id = ? AND id = ? AND state = 'running'`,
		fraction, p.CompletedBytes, string(payload), sqlTimestamp(r.service.now()), r.service.libraryID, r.jobID)
	if err != nil {
		return databaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if changed != 1 {
		return context.Canceled
	}
	r.lastBytes = p.CompletedBytes
	r.lastAt = time.Now()
	return nil
}

type uploadProgressReader struct {
	source io.Reader
	report func(context.Context, int64) error
}

func (r *uploadProgressReader) Read(buffer []byte) (int, error) {
	if r.source == nil {
		return 0, errors.New("upload source is nil")
	}
	count, err := r.source.Read(buffer)
	if count > 0 {
		if reportErr := r.report(context.Background(), int64(count)); reportErr != nil {
			return count, fmt.Errorf("upload progress: %w", reportErr)
		}
	}
	return count, err
}
