package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	webassets "github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/auth"
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
	if err := validateAuthenticationRuntime(settings); err != nil {
		return err
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
	var authentication httpapi.AuthDependencies
	if settings.RemoteAccess {
		sessions, err := auth.NewPersistentStore(
			db.SQL(), settings.AuthIdleTimeout, settings.AuthAbsoluteTimeout,
			settings.AuthSessionCapacity, settings.AllowedUser,
			authenticationScope(settings, roots.LibraryID()),
		)
		if err != nil {
			return newOperationalError("AUTHENTICATION_UNAVAILABLE", "the authentication session store could not be initialized", err)
		}
		defer sessions.Close()
		authentication = httpapi.AuthDependencies{
			Authenticator: auth.NewPAMAuthenticator(settings.PAMService),
			Sessions:      sessions,
			Throttler:     auth.NewThrottler(settings.LoginAttempts, settings.LoginWindow),
		}
	}
	objects, err := storage.NewStore(roots, storage.StoreOptions{UploadLimit: settings.ArtifactUploadLimit})
	if err != nil {
		return errors.New("managed object store initialization failed")
	}
	if err := objects.CleanupStaging(); err != nil {
		return errors.New("staging recovery failed")
	}
	catalog, err := storage.NewCatalogStore(settings.ProgramRoot, objects, settings.ArtifactFileMode)
	if err != nil {
		return errors.New("LinuxCNC program catalog initialization failed")
	}
	defer catalog.Close()
	application, err := service.New(service.Options{
		Database: db, Objects: objects, Catalog: catalog, LibraryID: roots.LibraryID(),
		CatalogRootLabel:          settings.LibraryAlias,
		CatalogRootDisplay:        settings.ProgramRootDisplay,
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
	recoverErr := recoverBeforeListen(signalContext, startupRecoveryDependencies{
		recoverLegacyOperations: func(ctx context.Context) error {
			_, err := application.RecoverOperations(ctx)
			return err
		},
		recoverLegacyImports: func(ctx context.Context) error {
			_, err := application.RecoverImports(ctx)
			return err
		},
		inspectLegacyContent: func(ctx context.Context) error {
			// Every legacy reference is identity-checked before it can become a
			// migration source. Missing/replaced bytes therefore cannot briefly
			// appear as valid catalog content after a restart.
			_, err := application.InspectManagedContent(ctx)
			return err
		},
		recoverCatalogOperations: application.RecoverCatalogOperations,
		migrateLegacyCatalog:     application.MigrateLegacyCatalog,
	})
	if recoverErr != nil {
		return newOperationalError("STATE_RECOVERY_FAILED", "durable setup state recovery or migration failed", recoverErr)
	}

	frontend, err := webassets.FS()
	if err != nil {
		return errors.New("embedded frontend initialization failed")
	}
	storageReadiness := composeReadiness(
		func(context.Context) error { return roots.Check() },
		func(context.Context) error { return catalog.Check() },
		func(context.Context) error { return settings.ValidateFiles() },
	)
	handler, err := httpapi.NewWithServiceAuthenticated(httpapi.Config{
		ListenAddress: settings.ListenAddress, LibraryID: roots.LibraryID(), LibraryAlias: settings.LibraryAlias,
		GCodeExtensions: settings.GCodeExtensions, RequireSetupSheetForReady: settings.RequireSetupSheetForReady,
		RequestReadIdleTimeout:   settings.ReadTimeout,
		ResponseWriteIdleTimeout: settings.ReadTimeout,
		RemoteAccess:             settings.RemoteAccess,
		AllowedUser:              settings.AllowedUser,
		AuthRememberTimeout:      settings.AuthRememberTimeout,
		AuthConcurrency:          settings.AuthConcurrency,
		RemoteAuthToken:          settings.RemoteAuthToken,
		// The old setup/validation API is intentionally unavailable in the
		// production catalog application. It remains opt-in for compatibility
		// tests and controlled offline migration tooling only.
		EnableLegacyAPI: false,
	}, httpapi.CheckFunc(db.Ping), storageReadiness, application, frontend, authentication, logger)
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

// composeReadiness returns one fail-closed readiness probe. Checks run in the
// supplied order and the first failure is returned unchanged so callers can
// preserve cancellation and dependency error identity. A missing check is a
// configuration failure rather than an implicit success.
func composeReadiness(checks ...func(context.Context) error) httpapi.CheckFunc {
	return func(ctx context.Context) error {
		if len(checks) == 0 {
			return errors.New("readiness dependency is unavailable")
		}
		for _, check := range checks {
			if err := ctx.Err(); err != nil {
				return err
			}
			if check == nil {
				return errors.New("readiness dependency is unavailable")
			}
			if err := check(ctx); err != nil {
				return err
			}
		}
		return nil
	}
}

type startupRecoveryDependencies struct {
	recoverLegacyOperations  func(context.Context) error
	recoverLegacyImports     func(context.Context) error
	inspectLegacyContent     func(context.Context) error
	recoverCatalogOperations func(context.Context) error
	migrateLegacyCatalog     func(context.Context) error
}

// recoverBeforeListen fixes every durable source and destination state before
// migration, and finishes migration before the caller may create a listener.
// The fixed ordering prevents migration from reading an interrupted legacy
// object or writing into a catalog whose own journal has not been reconciled.
func recoverBeforeListen(ctx context.Context, dependencies startupRecoveryDependencies) error {
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "legacy operation recovery", run: dependencies.recoverLegacyOperations},
		{name: "legacy import recovery", run: dependencies.recoverLegacyImports},
		{name: "legacy content inspection", run: dependencies.inspectLegacyContent},
		{name: "catalog operation recovery", run: dependencies.recoverCatalogOperations},
		{name: "legacy catalog migration", run: dependencies.migrateLegacyCatalog},
	}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if step.run == nil {
			return errors.New("startup recovery dependency is unavailable")
		}
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("%s failed: %w", step.name, err)
		}
	}
	return nil
}

func validateAuthenticationRuntime(settings config.Config) error {
	if !settings.RemoteAccess {
		return nil
	}
	if !auth.PAMAvailable() {
		return newOperationalError("AUTHENTICATION_UNAVAILABLE", "remote access requires a production build with PAM support", auth.ErrUnavailable)
	}
	configuredAccount, err := user.Lookup(settings.AllowedUser)
	if err != nil {
		return newOperationalError("AUTHENTICATION_UNAVAILABLE", "the configured authentication account is unavailable", err)
	}
	uid, err := strconv.ParseUint(configuredAccount.Uid, 10, 32)
	if err != nil || int(uid) != os.Geteuid() {
		return newOperationalError("AUTHENTICATION_ACCOUNT_MISMATCH", "the service must run as the configured authentication account", err)
	}
	return nil
}

func authenticationScope(settings config.Config, libraryID string) string {
	// Remembered browser sessions are invalidated if the managed library,
	// listener, PAM policy or transport termination mode changes. Store only a
	// digest so the durable session table remains a small opaque security index.
	material := strings.Join([]string{
		"websetupmanager-auth-v1", libraryID, settings.ListenAddress,
		settings.AllowedUser, settings.PAMService, strconv.FormatBool(settings.TrustedTLSProxy),
	}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
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
