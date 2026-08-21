package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/auth"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/config"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
)

func TestComposeReadinessRunsInOrderAndPropagatesFirstFailure(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("probe"), "same-context")
	wantErr := errors.New("catalog root unavailable")
	calls := make([]string, 0, 3)
	check := func(name string, result error) func(context.Context) error {
		return func(received context.Context) error {
			if received.Value(contextKey("probe")) != "same-context" {
				t.Fatal("readiness check did not receive the caller context")
			}
			calls = append(calls, name)
			return result
		}
	}
	probe := composeReadiness(
		check("legacy-roots", nil),
		check("catalog-root", wantErr),
		check("active-ini", nil),
	)
	if err := probe.Check(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("readiness error = %v, want %v", err, wantErr)
	}
	if got := strings.Join(calls, ","); got != "legacy-roots,catalog-root" {
		t.Fatalf("readiness order = %q", got)
	}
}

func TestComposeReadinessSucceedsOnlyAfterEveryDependency(t *testing.T) {
	calls := 0
	probe := composeReadiness(
		func(context.Context) error { calls++; return nil },
		func(context.Context) error { calls++; return nil },
		func(context.Context) error { calls++; return nil },
	)
	if err := probe.Check(context.Background()); err != nil {
		t.Fatalf("readiness = %v", err)
	}
	if calls != 3 {
		t.Fatalf("readiness checks = %d, want 3", calls)
	}
}

func TestComposeReadinessFailsClosedForCancellationAndMissingDependency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	probe := composeReadiness(func(context.Context) error { calls++; return nil })
	if err := probe.Check(ctx); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancelled readiness = %v, calls = %d", err, calls)
	}

	probe = composeReadiness(nil)
	if err := probe.Check(context.Background()); err == nil {
		t.Fatal("missing readiness dependency was accepted")
	}
	probe = composeReadiness()
	if err := probe.Check(context.Background()); err == nil {
		t.Fatal("empty readiness dependency list was accepted")
	}
}

func TestRecoverBeforeListenUsesSafeFixedOrder(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("startup"), "same-context")
	calls := make([]string, 0, 5)
	step := func(name string) func(context.Context) error {
		return func(received context.Context) error {
			if received.Value(contextKey("startup")) != "same-context" {
				t.Fatal("startup step did not receive the caller context")
			}
			calls = append(calls, name)
			return nil
		}
	}
	err := recoverBeforeListen(ctx, startupRecoveryDependencies{
		recoverLegacyOperations:  step("legacy-operations"),
		recoverLegacyImports:     step("legacy-imports"),
		inspectLegacyContent:     step("legacy-inspection"),
		recoverCatalogOperations: step("catalog-operations"),
		migrateLegacyCatalog:     step("catalog-migration"),
	})
	if err != nil {
		t.Fatalf("startup recovery = %v", err)
	}
	if got := strings.Join(calls, ","); got != "legacy-operations,legacy-imports,legacy-inspection,catalog-operations,catalog-migration" {
		t.Fatalf("startup recovery order = %q", got)
	}
}

func TestRecoverBeforeListenStopsAtFirstFailure(t *testing.T) {
	wantErr := errors.New("catalog journal is inconsistent")
	calls := make([]string, 0, 5)
	step := func(name string, result error) func(context.Context) error {
		return func(context.Context) error {
			calls = append(calls, name)
			return result
		}
	}
	err := recoverBeforeListen(context.Background(), startupRecoveryDependencies{
		recoverLegacyOperations:  step("legacy-operations", nil),
		recoverLegacyImports:     step("legacy-imports", nil),
		inspectLegacyContent:     step("legacy-inspection", nil),
		recoverCatalogOperations: step("catalog-operations", wantErr),
		migrateLegacyCatalog:     step("catalog-migration", nil),
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("startup recovery error = %v, want %v", err, wantErr)
	}
	if got := strings.Join(calls, ","); got != "legacy-operations,legacy-imports,legacy-inspection,catalog-operations" {
		t.Fatalf("startup recovery after failure = %q", got)
	}
}

func TestRecoverBeforeListenPropagatesMigrationFailure(t *testing.T) {
	wantErr := errors.New("legacy entries require manual review")
	completed := 0
	success := func(context.Context) error { completed++; return nil }
	err := recoverBeforeListen(context.Background(), startupRecoveryDependencies{
		recoverLegacyOperations:  success,
		recoverLegacyImports:     success,
		inspectLegacyContent:     success,
		recoverCatalogOperations: success,
		migrateLegacyCatalog: func(context.Context) error {
			completed++
			return wantErr
		},
	})
	if !errors.Is(err, wantErr) || completed != 5 {
		t.Fatalf("migration failure = %v, completed steps = %d", err, completed)
	}
}

func TestRecoverBeforeListenFailsClosedForCancellationAndMissingStep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	first := func(context.Context) error {
		calls++
		cancel()
		return nil
	}
	err := recoverBeforeListen(ctx, startupRecoveryDependencies{
		recoverLegacyOperations: first,
		recoverLegacyImports:    func(context.Context) error { calls++; return nil },
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled startup recovery = %v, calls = %d", err, calls)
	}

	err = recoverBeforeListen(context.Background(), startupRecoveryDependencies{})
	if err == nil {
		t.Fatal("missing startup recovery step was accepted")
	}
}

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
