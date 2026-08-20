package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	webassets "github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/config"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/httpapi"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("startup failed", "result", "STARTUP_FAILED", "message", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the service refuses to run with root privileges")
	}
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
	roots, err := storage.NewRoots(settings.LibraryDir, settings.StateDir, settings.ArtifactFileMode)
	if err != nil {
		return errors.New("managed storage initialization failed")
	}
	defer roots.Close()
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	db, err := database.Open(startupContext, settings.StateDir)
	if err == nil {
		err = db.EnsureLibrary(startupContext, roots.LibraryID(), roots.LibraryFingerprint())
	}
	cancelStartup()
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		return errors.New("local database initialization failed")
	}
	defer db.Close()
	objects, err := storage.NewStore(roots, storage.StoreOptions{UploadLimit: settings.ArtifactUploadLimit})
	if err != nil {
		return errors.New("managed object store initialization failed")
	}
	if err := objects.CleanupStaging(); err != nil {
		return errors.New("staging recovery failed")
	}

	frontend, err := webassets.FS()
	if err != nil {
		return errors.New("embedded frontend initialization failed")
	}
	handler, err := httpapi.New(httpapi.Config{
		ListenAddress: settings.ListenAddress, LibraryID: roots.LibraryID(), LibraryAlias: settings.LibraryAlias,
		GCodeExtensions: settings.GCodeExtensions, RequireSetupSheetForReady: settings.RequireSetupSheetForReady,
		RemoteAccess: settings.RemoteAccess, RemoteAuthToken: settings.RemoteAuthToken,
	}, httpapi.CheckFunc(db.Ping), httpapi.CheckFunc(func(context.Context) error { return roots.Check() }), frontend, logger)
	if err != nil {
		return errors.New("HTTP application initialization failed")
	}
	server := newWebServer(settings, handler, logger)

	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("service listening", "listen_address", settings.ListenAddress, "library_id", roots.LibraryID())
		serverErrors <- serve(server, settings)
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server stopped unexpectedly")
	case <-signalContext.Done():
		logger.Info("service shutting down", "result", "signal")
	}

	handler.BeginShutdown()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		return errors.New("graceful shutdown timed out")
	}
	return nil
}

func newWebServer(settings config.Config, handler http.Handler, logger *slog.Logger) *http.Server {
	server := &http.Server{
		Addr:              settings.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: settings.ReadHeaderTimeout,
		ReadTimeout:       settings.ReadTimeout,
		IdleTimeout:       settings.IdleTimeout,
		MaxHeaderBytes:    settings.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	if settings.TLSCertFile != "" {
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	}
	return server
}

type servingServer interface {
	ListenAndServe() error
	ListenAndServeTLS(certFile, keyFile string) error
}

func serve(server servingServer, settings config.Config) error {
	if settings.TLSCertFile != "" {
		return server.ListenAndServeTLS(settings.TLSCertFile, settings.TLSKeyFile)
	}
	return server.ListenAndServe()
}
