package main

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/auth"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/config"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
)

func TestNewWebServerAppliesResourceTimeouts(t *testing.T) {
	settings := config.Config{
		ListenAddress: "127.0.0.1:8080", ReadHeaderTimeout: config.DefaultReadHeaderTimeout,
		ReadTimeout: config.DefaultReadTimeout, IdleTimeout: config.DefaultIdleTimeout,
		MaxHeaderBytes: config.DefaultMaxHeaderBytes,
	}
	server := newWebServer(settings, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if server.Addr != settings.ListenAddress || server.ReadHeaderTimeout != settings.ReadHeaderTimeout ||
		server.ReadTimeout != 0 || server.IdleTimeout != settings.IdleTimeout ||
		server.MaxHeaderBytes != settings.MaxHeaderBytes {
		t.Fatalf("unexpected server config: %+v", server)
	}
}

func TestAuthenticationScopeBindsSecurityRelevantDeploymentState(t *testing.T) {
	settings := config.Config{
		ListenAddress: "192.0.2.10:443", AllowedUser: "operator",
		PAMService: "websetupmanager",
	}
	baseline := authenticationScope(settings, "library-a")
	if len(baseline) != 64 || baseline != authenticationScope(settings, "library-a") {
		t.Fatalf("authentication scope is not a stable SHA-256 digest: %q", baseline)
	}
	for name, changed := range map[string]config.Config{
		"listener": func() config.Config { value := settings; value.ListenAddress = "192.0.2.11:443"; return value }(),
		"account":  func() config.Config { value := settings; value.AllowedUser = "other"; return value }(),
		"pam":      func() config.Config { value := settings; value.PAMService = "other"; return value }(),
		"proxy":    func() config.Config { value := settings; value.TrustedTLSProxy = true; return value }(),
	} {
		if authenticationScope(changed, "library-a") == baseline {
			t.Fatalf("%s change retained remembered-session scope", name)
		}
	}
	if authenticationScope(settings, "library-b") == baseline {
		t.Fatal("library change retained remembered-session scope")
	}
}

func TestRemoteAuthenticationFailsClosedWithoutPAMBuild(t *testing.T) {
	if auth.PAMAvailable() {
		t.Skip("PAM-tagged build exercises the enabled runtime path")
	}
	err := validateAuthenticationRuntime(config.Config{RemoteAccess: true, AllowedUser: "operator"})
	var classified *operationalError
	if !errors.As(err, &classified) || classified.code != "AUTHENTICATION_UNAVAILABLE" {
		t.Fatalf("non-PAM remote runtime error = %#v", err)
	}
	if err := validateAuthenticationRuntime(config.Config{}); err != nil {
		t.Fatalf("local runtime unexpectedly requires PAM: %v", err)
	}
}

type fakeServingServer struct {
	httpCalls int
	tlsCalls  int
	certFile  string
	keyFile   string
}

func (f *fakeServingServer) Serve(net.Listener) error {
	f.httpCalls++
	return http.ErrServerClosed
}

func (f *fakeServingServer) ServeTLS(_ net.Listener, certFile, keyFile string) error {
	f.tlsCalls++
	f.certFile = certFile
	f.keyFile = keyFile
	return http.ErrServerClosed
}

func TestServeUsesTLSOnlyWhenConfigured(t *testing.T) {
	server := &fakeServingServer{}
	if err := serve(server, nil, config.Config{}); err != http.ErrServerClosed || server.httpCalls != 1 || server.tlsCalls != 0 {
		t.Fatalf("plain serve = %v, %+v", err, server)
	}
	server = &fakeServingServer{}
	settings := config.Config{TLSCertFile: "/configured/cert", TLSKeyFile: "/configured/key"}
	if err := serve(server, nil, settings); err != http.ErrServerClosed || server.httpCalls != 0 || server.tlsCalls != 1 ||
		server.certFile != "" || server.keyFile != "" {
		t.Fatalf("TLS serve = %v, %+v", err, server)
	}
	certificate := &tls.Certificate{Certificate: [][]byte{{1, 2, 3}}}
	tlsServer := newWebServer(settings, http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)), certificate)
	if tlsServer.TLSConfig == nil || tlsServer.TLSConfig.MinVersion != tls.VersionTLS13 ||
		len(tlsServer.TLSConfig.Certificates) != 1 {
		t.Fatalf("in-memory TLS configuration = %+v", tlsServer.TLSConfig)
	}
}

func TestDatabaseStartupErrorsHaveSafeStableClassifications(t *testing.T) {
	for _, test := range []struct {
		cause error
		code  string
	}{
		{database.ErrAlreadyRunning, "ALREADY_RUNNING"},
		{database.ErrIntegrityCheck, "DB_INTEGRITY_FAILED"},
		{database.ErrMigrationChecksum, "DB_MIGRATION_FAILED"},
		{database.ErrLibraryFingerprintMismatch, "LIBRARY_MISMATCH"},
		{database.ErrInvalidStateDir, "STATE_STORAGE_INVALID"},
		{errors.New("driver detail /secret/path"), "DATABASE_STARTUP_FAILED"},
	} {
		err := classifyDatabaseStartupError(test.cause)
		var classified *operationalError
		if !errors.As(err, &classified) || classified.code != test.code {
			t.Fatalf("classification(%v) = %#v", test.cause, err)
		}
		if classified.Error() == "" || classified.Error() == test.cause.Error() {
			t.Fatalf("unsafe classification message %q", classified.Error())
		}
	}
}
