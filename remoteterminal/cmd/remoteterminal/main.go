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
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/codeservers"
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
	codeServerManager := codeservers.NewManager(codeservers.Config{
		TmuxBinary:       settings.TmuxBinary,
		CodeServerBinary: settings.CodeServerBinary,
		RuntimeDir:       settings.RuntimeDir,
		StateDir:         settings.StateDir,
		HomeDir:          os.Getenv("HOME"),
		MaxServers:       settings.MaxCodeServers,
		StartTimeout:     settings.CodeServerTimeout,
	})
	codeStartupContext, cancelCodeStartup := context.WithTimeout(context.Background(), settings.CodeServerTimeout+5*time.Second)
	err = codeServerManager.Initialize(codeStartupContext)
	cancelCodeStartup()
	if err != nil {
		return fmt.Errorf("initialize code-server manager: %w", err)
	}

	authStore, err := auth.NewPersistentStore(
		settings.IdleTimeout,
		settings.AbsoluteTimeout,
		settings.SessionCapacity,
		filepath.Join(settings.StateDir, "auth", "remembered-sessions.json"),
		settings.AllowedUser,
		string(settings.Transport),
	)
	if err != nil {
		return fmt.Errorf("initialize authentication store: %w", err)
	}
	handler, err := httpapi.New(httpapi.Config{
		AllowedUser:     settings.AllowedUser,
		MachineName:     settings.MachineName,
		WebDir:          settings.WebDir,
		AbsoluteTimeout: settings.AbsoluteTimeout,
		RememberTimeout: settings.RememberTimeout,
		AuthConcurrency: settings.AuthConcurrency,
		InsecureHTTP:    settings.Transport == config.TransportHTTP,
	}, auth.NewPAMAuthenticator(settings.PAMService), authStore,
		auth.NewThrottler(settings.LoginAttempts, settings.LoginWindow), manager, codeServerManager, logger)
	if err != nil {
		return fmt.Errorf("initialize HTTP handler: %w", err)
	}

	server := newWebServer(settings, handler, logger)

	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s://%s", settings.Transport, settings.ListenAddress)
		serverErrors <- serveConfigured(server, settings)
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			shutdownContext, cancel := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
			_ = shutdownManagers(shutdownContext, manager, codeServerManager)
			cancel()
			return fmt.Errorf("serve %s: %w", settings.Transport, err)
		}
		return nil
	case <-signalContext.Done():
		logger.Print("shutting down")
	}

	handler.BeginShutdown()
	// Stopping ttyd and code-server closes their proxied WebSockets; net/http
	// Shutdown otherwise cannot account for hijacked WebSocket connections.
	processContext, cancelProcesses := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	processErr := shutdownManagers(processContext, manager, codeServerManager)
	cancelProcesses()

	httpContext, cancelHTTP := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	httpErr := server.Shutdown(httpContext)
	cancelHTTP()
	if httpErr != nil {
		return fmt.Errorf("shutdown web server: %w", httpErr)
	}
	if processErr != nil && !errors.Is(processErr, context.Canceled) && !errors.Is(processErr, context.DeadlineExceeded) {
		return fmt.Errorf("shutdown managed processes: %w", processErr)
	}
	return nil
}

func newWebServer(settings config.Config, handler http.Handler, logger *log.Logger) *http.Server {
	server := &http.Server{
		Addr:              settings.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          logger,
	}
	if settings.Transport != config.TransportHTTP {
		server.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
	return server
}

type servingServer interface {
	ListenAndServe() error
	ListenAndServeTLS(certFile, keyFile string) error
}

func serveConfigured(server servingServer, settings config.Config) error {
	if settings.Transport == config.TransportHTTP {
		return server.ListenAndServe()
	}
	return server.ListenAndServeTLS(settings.TLSCertFile, settings.TLSKeyFile)
}

type processManager interface {
	Shutdown(context.Context) error
}

func shutdownManagers(ctx context.Context, managers ...processManager) error {
	errorsChannel := make(chan error, len(managers))
	for _, manager := range managers {
		manager := manager
		go func() { errorsChannel <- manager.Shutdown(ctx) }()
	}
	var result error
	for range managers {
		if err := <-errorsChannel; err != nil && result == nil {
			result = err
		}
	}
	return result
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
