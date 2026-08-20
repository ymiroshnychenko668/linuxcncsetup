package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	webassets "github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/config"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/httpapi"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

func main() {
	started := time.Now()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		errorCode := "STARTUP_OR_SHUTDOWN_FAILED"
		var classified *operationalError
		if errors.As(err, &classified) {
			errorCode = classified.code
		}
		logger.Error("service failed", "operation", "lifecycle", "duration_ms", time.Since(started).Milliseconds(),
			"bytes", 0, "result", "failed", "error_code", errorCode, "message", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the service refuses to run with root privileges")
	}
	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	settings, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration is invalid: %w", err)
	}
	if err := settings.ValidateRoots(); err != nil {
		return fmt.Errorf("storage configuration is invalid: %w", err)
	}
	if err := settings.ValidateFiles(); err != nil {
		return fmt.Errorf("deployment configuration is invalid: %w", err)
	}
	tlsCertificate, err := settings.LoadTLSCertificate()
	if err != nil {
		return fmt.Errorf("deployment configuration is invalid: %w", err)
	}
	roots, err := storage.NewRoots(settings.LibraryDir, settings.StateDir, settings.ArtifactFileMode)
	if err != nil {
		return errors.New("managed storage initialization failed")
	}
	defer roots.Close()
	stateIdentity, err := roots.StateIdentity()
	if err != nil {
		return errors.New("managed state identity verification failed")
	}
	db, err := database.OpenWithOptions(signalContext, database.Options{
		StateDir: settings.StateDir,
		ExpectedStateIdentity: &database.StateDirectoryIdentity{
			Device: stateIdentity.Device,
			Inode:  stateIdentity.Inode,
		},
	})
	if err == nil {
		err = db.EnsureLibrary(signalContext, roots.LibraryID(), roots.LibraryFingerprint())
	}
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		return classifyDatabaseStartupError(err)
	}
	defer db.Close()
	objects, err := storage.NewStore(roots, storage.StoreOptions{UploadLimit: settings.ArtifactUploadLimit})
	if err != nil {
		return errors.New("managed object store initialization failed")
	}
	if err := objects.CleanupStaging(); err != nil {
		return errors.New("staging recovery failed")
	}
	application, err := service.New(service.Options{
		Database: db, Objects: objects, LibraryID: roots.LibraryID(),
		GCodeExtensions:           settings.GCodeExtensions,
		RequireSetupSheetForReady: settings.RequireSetupSheetForReady,
		RecentLimit:               settings.RecentSetupsLimit,
		MaxParallelHeavyJobs:      settings.MaxParallelHeavyJobs,
		ImportTotalLimit:          settings.ImportTotalLimit,
		IdempotencyTTL:            settings.IdempotencyTTL,
		DeleteConfirmationTTL:     settings.DeleteConfirmationTTL,
		ImportSessionExpiry:       settings.ImportSessionExpiry,
		Logger:                    logger,
	})
	if err != nil {
		return errors.New("setup service initialization failed")
	}
	defer application.Close()
	_, recoverErr := application.RecoverOperations(signalContext)
	if recoverErr == nil {
		_, recoverErr = application.RecoverImports(signalContext)
	}
	if recoverErr == nil {
		// Reconcile every durable reference before ready-state so a restart
		// cannot briefly expose a missing or externally replaced artifact as
		// ready/current until the periodic maintenance pass catches up.
		_, recoverErr = application.InspectManagedContent(signalContext)
	}
	if recoverErr != nil {
		return errors.New("setup operation recovery failed")
	}

	frontend, err := webassets.FS()
	if err != nil {
		return errors.New("embedded frontend initialization failed")
	}
	handler, err := httpapi.NewWithService(httpapi.Config{
		ListenAddress: settings.ListenAddress, LibraryID: roots.LibraryID(), LibraryAlias: settings.LibraryAlias,
		GCodeExtensions: settings.GCodeExtensions, RequireSetupSheetForReady: settings.RequireSetupSheetForReady,
		RequestReadIdleTimeout:   settings.ReadTimeout,
		ResponseWriteIdleTimeout: settings.ReadTimeout,
		RemoteAccess:             settings.RemoteAccess, RemoteAuthToken: settings.RemoteAuthToken,
	}, httpapi.CheckFunc(db.Ping), httpapi.CheckFunc(func(context.Context) error { return roots.Check() }), application, frontend, logger)
	if err != nil {
		return errors.New("HTTP application initialization failed")
	}
	server := newWebServer(settings, handler, logger, tlsCertificate)
	listener, err := net.Listen("tcp", settings.ListenAddress)
	if err != nil {
		return newOperationalError("HTTP_LISTEN_FAILED", "HTTP listen address is unavailable or already in use", err)
	}
	defer listener.Close()
	logger.Info("service listening", "operation", "listen", "duration_ms", 0, "bytes", 0,
		"result", "succeeded", "error_code", "", "listen_address", settings.ListenAddress, "library_id", roots.LibraryID())

	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		runMaintenance(signalContext, application, settings.ReconcileInterval, logger)
	}()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- serve(server, listener, settings)
	}()

	var serveErr error
	shutdownStarted := time.Now()
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = errors.New("HTTP server stopped unexpectedly")
		}
		// Ensure maintenance observes cancellation on listener failure too.
		stopSignals()
	case <-signalContext.Done():
		shutdownStarted = time.Now()
		logger.Info("service shutting down", "operation", "shutdown", "duration_ms", 0, "bytes", 0,
			"result", "signal", "error_code", "")
	}

	handler.BeginShutdown()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	defer cancelShutdown()
	shutdownResults := make(chan error, 1)
	go func() { shutdownResults <- server.Shutdown(shutdownContext) }()
	closeErr := application.CloseContext(shutdownContext)
	jobsErr := application.Wait(shutdownContext)
	shutdownErr := <-shutdownResults
	maintenanceErr := error(nil)
	select {
	case <-maintenanceDone:
	case <-shutdownContext.Done():
		maintenanceErr = shutdownContext.Err()
	}
	if closeErr != nil || shutdownErr != nil || jobsErr != nil || maintenanceErr != nil {
		return errors.New("graceful shutdown timed out")
	}
	logger.Info("service stopped", "operation", "shutdown", "duration_ms", time.Since(shutdownStarted).Milliseconds(),
		"bytes", 0, "result", "succeeded", "error_code", "")
	return serveErr
}

type operationalError struct {
	code    string
	message string
	cause   error
}

func (e *operationalError) Error() string { return e.message }
func (e *operationalError) Unwrap() error { return e.cause }

func newOperationalError(code, message string, cause error) error {
	return &operationalError{code: code, message: message, cause: cause}
}

func classifyDatabaseStartupError(err error) error {
	switch {
	case errors.Is(err, database.ErrAlreadyRunning):
		return newOperationalError("ALREADY_RUNNING", "another Web Setup Manager process is already using the state directory", err)
	case errors.Is(err, database.ErrLibraryFingerprintMismatch):
		return newOperationalError("LIBRARY_MISMATCH", "the state database belongs to a different managed library", err)
	case errors.Is(err, database.ErrIntegrityCheck):
		return newOperationalError("DB_INTEGRITY_FAILED", "the local database failed its integrity check", err)
	case errors.Is(err, database.ErrMigrationChecksum), errors.Is(err, database.ErrMigrationHistory),
		errors.Is(err, database.ErrSchemaNewer), errors.Is(err, database.ErrUnmanagedDatabase):
		return newOperationalError("DB_MIGRATION_FAILED", "the local database migration history is incompatible", err)
	case errors.Is(err, database.ErrInvalidStateDir):
		return newOperationalError("STATE_STORAGE_INVALID", "the state directory or database control files are unsafe", err)
	default:
		return newOperationalError("DATABASE_STARTUP_FAILED", "the local database could not be initialized", err)
	}
}

func runMaintenance(ctx context.Context, application *service.Service, interval time.Duration, logger *slog.Logger) {
	identityTicker := time.NewTicker(interval)
	fullTicker := time.NewTicker(24 * time.Hour)
	defer identityTicker.Stop()
	defer fullTicker.Stop()
	run := func(full bool) {
		started := time.Now()
		operation := "inspectManagedContent"
		var result *service.ReconcileResult
		var err error
		if full {
			operation = "reconcile"
			result, err = application.Reconcile(ctx)
		} else {
			result, err = application.InspectManagedContent(ctx)
		}
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("managed content reconciliation failed",
					"operation", operation, "duration_ms", time.Since(started).Milliseconds(),
					"bytes", 0, "result", "failed", "error_code", "RECONCILE_FAILED")
			}
			return
		}
		_, cleanupErr := application.CleanupExpired(ctx)
		_, gcErr := application.GarbageCollect(ctx)
		if cleanupErr != nil || gcErr != nil {
			if ctx.Err() == nil {
				logger.Warn("managed content maintenance failed",
					"operation", "maintenance", "duration_ms", time.Since(started).Milliseconds(),
					"bytes", 0, "result", "failed", "error_code", "MAINTENANCE_FAILED")
			}
			return
		}
		logger.Info("managed content reconciled", "operation", operation, "result", "succeeded",
			"setups_checked", result.SetupsChecked, "artifacts_checked", result.ArtifactsChecked,
			"duration_ms", time.Since(started).Milliseconds(), "bytes", 0, "error_code", "")
	}
	// One full background scrub follows listener startup. Frequent maintenance
	// is identity-only; re-hashing every 10 GiB object each minute would violate
	// the idle CPU/I/O budget. A full repair-capable scrub repeats daily.
	run(true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-identityTicker.C:
			run(false)
		case <-fullTicker.C:
			run(true)
		}
	}
}

func newWebServer(settings config.Config, handler http.Handler, logger *slog.Logger, certificate *tls.Certificate) *http.Server {
	server := &http.Server{
		Addr:              settings.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: settings.ReadHeaderTimeout,
		// Streaming uploads use a per-read idle deadline in the application
		// handler. A server-wide ReadTimeout is an absolute wall-clock limit and
		// would incorrectly reject large uploads that are making steady progress.
		ReadTimeout:    0,
		IdleTimeout:    settings.IdleTimeout,
		MaxHeaderBytes: settings.MaxHeaderBytes,
		ErrorLog:       slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	if certificate != nil {
		server.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{*certificate},
		}
	}
	return server
}

type servingServer interface {
	Serve(net.Listener) error
	ServeTLS(net.Listener, string, string) error
}

func serve(server servingServer, listener net.Listener, settings config.Config) error {
	if settings.TLSCertFile != "" {
		// TLS material was opened, identity-checked and parsed once during
		// startup. Empty filenames tell net/http to use TLSConfig.Certificates
		// without reopening mutable configured paths.
		return server.ServeTLS(listener, "", "")
	}
	return server.Serve(listener)
}
