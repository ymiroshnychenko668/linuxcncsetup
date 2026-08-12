package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/auth"
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/config"
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/httpapi"
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/sessions"
)

func main() {
	logger := log.New(os.Stderr, "remoteterminal: ", log.LstdFlags|log.LUTC)
	if err := run(logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) error {
	settings, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := settings.ValidateFiles(); err != nil {
		return fmt.Errorf("validate deployment: %w", err)
	}
	if err := validateRuntimeUser(settings.AllowedUser); err != nil {
		return err
	}

	manager := sessions.NewManager(sessions.Config{
		TmuxBinary:   settings.TmuxBinary,
		TtydBinary:   settings.TtydBinary,
		RuntimeDir:   settings.RuntimeDir,
		MaxSessions:  settings.MaxSessions,
		StartTimeout: settings.TerminalTimeout,
	})
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	err = manager.Initialize(startupContext)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("initialize terminal manager: %w", err)
	}

	authStore := auth.NewStore(settings.IdleTimeout, settings.AbsoluteTimeout, settings.SessionCapacity)
	handler, err := httpapi.New(httpapi.Config{
		AllowedUser:     settings.AllowedUser,
		MachineName:     settings.MachineName,
		WebDir:          settings.WebDir,
		AbsoluteTimeout: settings.AbsoluteTimeout,
		AuthConcurrency: settings.AuthConcurrency,
	}, auth.NewPAMAuthenticator(settings.PAMService), authStore,
		auth.NewThrottler(settings.LoginAttempts, settings.LoginWindow), manager, logger)
	if err != nil {
		return fmt.Errorf("initialize HTTP handler: %w", err)
	}

	server := &http.Server{
		Addr:              settings.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          logger,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Printf("listening on https://%s", settings.ListenAddress)
		serverErrors <- server.ListenAndServeTLS(settings.TLSCertFile, settings.TLSKeyFile)
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			shutdownContext, cancel := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
			_ = manager.Shutdown(shutdownContext)
			cancel()
			return fmt.Errorf("serve HTTPS: %w", err)
		}
		return nil
	case <-signalContext.Done():
		logger.Print("shutting down")
	}

	handler.BeginShutdown()
	// Stopping ttyd closes its proxied WebSockets; net/http Shutdown otherwise
	// cannot account for hijacked WebSocket connections.
	terminalContext, cancelTerminals := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	terminalErr := manager.Shutdown(terminalContext)
	cancelTerminals()

	httpContext, cancelHTTP := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	httpErr := server.Shutdown(httpContext)
	cancelHTTP()
	if httpErr != nil {
		return fmt.Errorf("shutdown HTTPS server: %w", httpErr)
	}
	if terminalErr != nil && !errors.Is(terminalErr, context.Canceled) && !errors.Is(terminalErr, context.DeadlineExceeded) {
		return fmt.Errorf("shutdown terminal processes: %w", terminalErr)
	}
	return nil
}

func validateRuntimeUser(allowed string) error {
	if os.Geteuid() == 0 {
		return errors.New("refusing to run the remote terminal service as root")
	}
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("determine service account: %w", err)
	}
	if current.Username != allowed {
		return fmt.Errorf("service account %q does not match configured account %q", current.Username, allowed)
	}
	if home := os.Getenv("HOME"); home == "" || !filepath.IsAbs(home) {
		return errors.New("service HOME must be an absolute path")
	}
	return nil
}
