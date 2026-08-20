// Package service implements the setup-manager use cases over SQLite and the
// root-anchored immutable object store. Public values intentionally contain no
// host paths or storage keys.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

const (
	defaultRecentLimit     = 30
	defaultIdempotencyTTL  = 24 * time.Hour
	defaultDeleteTTL       = 5 * time.Minute
	defaultImportExpiry    = 24 * time.Hour
	defaultMaxParallelJobs = 2
	defaultContentReaders  = 4
	maxDescriptionBytes    = 32 << 10
	maxIdempotencyKeyBytes = 128
	defaultSetupPageSize   = 50
	maximumSetupPageSize   = 200
)

type Options struct {
	Database                  *database.DB
	Objects                   *storage.Store
	LibraryID                 string
	GCodeExtensions           []string
	RequireSetupSheetForReady bool
	RecentLimit               int
	MaxParallelHeavyJobs      int
	ImportTotalLimit          int64
	IdempotencyTTL            time.Duration
	DeleteConfirmationTTL     time.Duration
	ImportSessionExpiry       time.Duration
	Logger                    *slog.Logger
}

type Service struct {
	db                        *sql.DB
	objects                   *storage.Store
	libraryID                 string
	gcode                     *domain.GCodeValidator
	requireSetupSheetForReady bool
	recentLimit               int
	importTotalLimit          int64
	idempotencyTTL            time.Duration
	deleteConfirmationTTL     time.Duration
	importSessionExpiry       time.Duration
	heavy                     chan struct{}
	content                   chan struct{}

	locksMu   sync.Mutex
	locks     map[string]*keyedMutex
	jobsMu    sync.Mutex
	jobs      map[string]context.CancelFunc
	jobsWG    sync.WaitGroup
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
	now       func() time.Time
	logger    *slog.Logger
}

type keyedMutex struct {
	mutex sync.Mutex
	refs  int
}

func New(options Options) (*Service, error) {
	if options.Database == nil || options.Objects == nil || strings.TrimSpace(options.LibraryID) == "" {
		return nil, errors.New("database, object store and library ID are required")
	}
	validator, err := domain.NewGCodeValidator(options.GCodeExtensions)
	if err != nil {
		return nil, err
	}
	if options.RecentLimit == 0 {
		options.RecentLimit = defaultRecentLimit
	}
	if options.MaxParallelHeavyJobs == 0 {
		options.MaxParallelHeavyJobs = defaultMaxParallelJobs
	}
	if options.IdempotencyTTL == 0 {
		options.IdempotencyTTL = defaultIdempotencyTTL
	}
	if options.DeleteConfirmationTTL == 0 {
		options.DeleteConfirmationTTL = defaultDeleteTTL
	}
	if options.ImportSessionExpiry == 0 {
		options.ImportSessionExpiry = defaultImportExpiry
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.DiscardHandler)
	}
	if options.RecentLimit < 1 || options.RecentLimit > 1000 ||
		options.MaxParallelHeavyJobs < 1 || options.MaxParallelHeavyJobs > 16 ||
		options.ImportTotalLimit < 0 || options.IdempotencyTTL <= 0 ||
		options.DeleteConfirmationTTL <= 0 || options.ImportSessionExpiry <= 0 {
		return nil, errors.New("invalid service limits")
	}
	return &Service{
		db: options.Database.SQL(), objects: options.Objects, libraryID: options.LibraryID,
		gcode: validator, requireSetupSheetForReady: options.RequireSetupSheetForReady,
		recentLimit: options.RecentLimit, importTotalLimit: options.ImportTotalLimit,
		idempotencyTTL:        options.IdempotencyTTL,
		deleteConfirmationTTL: options.DeleteConfirmationTTL,
		importSessionExpiry:   options.ImportSessionExpiry,
		heavy:                 make(chan struct{}, options.MaxParallelHeavyJobs), locks: make(map[string]*keyedMutex),
		content: make(chan struct{}, defaultContentReaders),
		jobs:    make(map[string]context.CancelFunc), closed: make(chan struct{}), now: time.Now,
		logger: options.Logger,
	}, nil
}

func (s *Service) Close() {
	_ = s.CloseContext(context.Background())
}

// CloseContext stops admission and synchronously terminalizes durable staging
// imports. The caller owns the shutdown deadline; after this method and Wait
// return, no service goroutine can access the database.
func (s *Service) CloseContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.closed)
		s.jobsMu.Lock()
		for _, cancel := range s.jobs {
			cancel()
		}
		// The map is intentionally retained until workers unregister. Clearing
		// it here would make a concurrent DELETE lose its cancellation handle.
		s.jobsMu.Unlock()
		started := time.Now()
		s.closeErr = s.cancelStagingImports(ctx)
		if s.closeErr != nil {
			s.logger.Error("staging import shutdown failed", "operation", "cancelStagingImports", "duration_ms", time.Since(started).Milliseconds(), "bytes", 0, "result", "failed", "error_code", safeErrorCode(s.closeErr))
		} else {
			s.logger.Info("staging imports terminalized", "operation", "cancelStagingImports", "duration_ms", time.Since(started).Milliseconds(), "bytes", 0, "result", "succeeded", "error_code", "")
		}
	})
	return s.closeErr
}

// cancelStagingImports gives idle import sessions a durable terminal state.
// Active request readers observe s.closed and abort before publication; the
// setup composition remains unchanged because commit is transactional.
func (s *Service) cancelStagingImports(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM import_sessions
		 WHERE library_id = ? AND state = 'staging' ORDER BY id`, s.libraryID)
	if err != nil {
		return databaseError(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return databaseError(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return databaseError(err)
	}
	for _, id := range ids {
		if _, err := s.cancelImport(ctx, id, nil, "", ""); err != nil && !domain.IsErrorCode(err, domain.CodeInvalidSetupState) {
			return err
		}
	}
	return nil
}

// Wait blocks until every asynchronous job goroutine has observed shutdown
// and completed its durable terminal-state update. Call Close before Wait so
// no new goroutine can be registered concurrently with the barrier.
func (s *Service) Wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		s.jobsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) withSetupLock(setupID string, operation func() error) error {
	s.locksMu.Lock()
	entry := s.locks[setupID]
	if entry == nil {
		entry = &keyedMutex{}
		s.locks[setupID] = entry
	}
	entry.refs++
	s.locksMu.Unlock()
	entry.mutex.Lock()
	defer func() {
		entry.mutex.Unlock()
		s.locksMu.Lock()
		entry.refs--
		if entry.refs == 0 && s.locks[setupID] == entry {
			delete(s.locks, setupID)
		}
		s.locksMu.Unlock()
	}()
	return operation()
}

// acquireHeavy applies the configured process-wide budget to work whose
// duration or I/O grows with artifact size. The returned release function
// must be called exactly once after a successful acquisition.
func (s *Service) acquireHeavy(ctx context.Context) (func(), error) {
	select {
	case s.heavy <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.heavy }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, context.Canceled
	}
}

// acquireContent isolates latency-sensitive bounded Range reads from long
// upload/hash/duplicate jobs. A saturated heavy-work pool must never make the
// first G-code viewport wait for a multi-gigabyte operation to finish.
func (s *Service) acquireContent(ctx context.Context) (func(), error) {
	select {
	case s.content <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.content }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, context.Canceled
	}
}

func validateDescription(value string) error {
	if len(value) > maxDescriptionBytes {
		return domain.NewError(domain.CodeInvalidContent, "description is too long")
	}
	if strings.ContainsRune(value, 0) {
		return domain.NewError(domain.CodeInvalidContent, "description contains an invalid character")
	}
	return nil
}

func validateIdempotencyKey(value string) error {
	if len(value) < 1 || len(value) > maxIdempotencyKeyBytes || strings.TrimSpace(value) != value {
		return domain.NewError(domain.CodeInvalidContent, "Idempotency-Key is required and invalid")
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return domain.NewError(domain.CodeInvalidContent, "Idempotency-Key is required and invalid")
		}
	}
	return nil
}

func databaseError(err error) error {
	if err == nil {
		return nil
	}
	var coded *domain.Error
	if errors.As(err, &coded) {
		return err
	}
	return domain.WrapError(domain.CodeDatabaseUnavailable, "setup database is unavailable", err)
}

func storageError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrTooLarge):
		return domain.WrapError(domain.CodeFileTooLarge, "artifact exceeds the configured limit", err)
	case errors.Is(err, storage.ErrInsufficientStorage):
		return domain.WrapError(domain.CodeInsufficientStorage, "managed storage has insufficient free space", err)
	case errors.Is(err, storage.ErrObjectChanged):
		return domain.WrapError(domain.CodeArtifactChanged, "artifact content changed", err)
	case errors.Is(err, context.Canceled):
		return domain.WrapError(domain.CodeJobCancelled, "operation was cancelled", err)
	default:
		return domain.WrapError(domain.CodeStorageUnavailable, "managed storage is unavailable", err)
	}
}

// SQLite's strftime('%f') emits exactly three fractional digits. Use the same
// fixed-width representation for application timestamps so TEXT ordering and
// expiry comparisons remain chronological (RFC3339Nano's variable width is
// not lexicographically sortable for values such as .010Z and .012Z).
func sqlTimestamp(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse database timestamp: %w", err)
	}
	return parsed, nil
}

func parseNullableTimestamp(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTimestamp(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
