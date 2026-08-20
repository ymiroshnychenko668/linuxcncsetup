package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

type checkStub struct{ err error }

func (c checkStub) Check(context.Context) error { return c.err }

func newTestServer(t *testing.T, databaseErr, storageErr error) (*Server, *bytes.Buffer) {
	t.Helper()
	files := fstest.MapFS{
		"index.html":             &fstest.MapFile{Data: []byte("<!doctype html><h1>Setups</h1>")},
		"assets/app-deadbeef.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	}
	logs := &bytes.Buffer{}
	server, err := New(Config{
		ListenAddress: "127.0.0.1:8080", LibraryID: strings.Repeat("a", 32),
		LibraryAlias: "Сетапы", GCodeExtensions: []string{".ngc"},
	}, checkStub{databaseErr}, checkStub{storageErr}, files,
		slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return server, logs
}

func TestHealthReadinessAndSafeErrors(t *testing.T) {
	server, logs := newTestServer(t, nil, errors.New("/secret/database path"))
	response := request(server, http.MethodGet, "/healthz")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health = %d %s", response.Code, response.Body.String())
	}
	response = request(server, http.MethodGet, "/readyz")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "STORAGE_UNAVAILABLE") {
		t.Fatalf("ready = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "/secret/") || strings.Contains(logs.String(), "/secret/") {
		t.Fatalf("physical path leaked: body=%s logs=%s", response.Body.String(), logs.String())
	}
	server.BeginShutdown()
	response = request(server, http.MethodGet, "/healthz")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("shutdown health = %d", response.Code)
	}
}

func TestCapabilitiesAreDomainOnlyAndSecured(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	response := request(server, http.MethodGet, "/api/v1/capabilities")
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"libraryAlias":"Сетапы"`) ||
		!strings.Contains(body, `"fileBrowser":false`) || strings.Contains(body, "storageKey") || strings.Contains(body, "Dir") {
		t.Fatalf("capabilities = %d %s", response.Code, body)
	}
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Request-ID"} {
		if response.Header().Get(name) == "" {
			t.Fatalf("missing security header %s", name)
		}
	}
	response = request(server, http.MethodPost, "/api/v1/capabilities")
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("capabilities POST = %d Allow=%q", response.Code, response.Header().Get("Allow"))
	}
}

func TestStaticSPAAndNoFilesystemAPI(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	response := request(server, http.MethodGet, "/setups/opaque-id")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Setups") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SPA = %d %s", response.Code, response.Body.String())
	}
	response = request(server, http.MethodGet, "/assets/app-deadbeef.js")
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d Cache-Control=%q", response.Code, response.Header().Get("Cache-Control"))
	}
	for _, attack := range []string{"/fs", "/fs/etc/passwd", "/api/v1/fs", "/api/v1/content?path=/etc/passwd"} {
		response = request(server, http.MethodGet, attack)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "passwd") {
			t.Fatalf("attack %q = %d %s", attack, response.Code, response.Body.String())
		}
	}
}

func TestMutationAuthorizationChecksHostOriginAndCSRF(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/setups", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "http://localhost:8080")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	if !server.authorizeMutation(request) {
		t.Fatal("valid same-origin mutation rejected")
	}
	for name, mutate := range map[string]func(*http.Request){
		"host":   func(r *http.Request) { r.Host = "evil.example" },
		"origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"csrf":   func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") },
	} {
		copyRequest := request.Clone(request.Context())
		copyRequest.Header = request.Header.Clone()
		mutate(copyRequest)
		if server.authorizeMutation(copyRequest) {
			t.Fatalf("%s attack accepted", name)
		}
	}
}

func TestMutationAuthorizationAcceptsBrowserDefaultPortAuthority(t *testing.T) {
	files := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("app")}}
	server, err := New(Config{
		ListenAddress: "127.0.0.1:080", LibraryID: "id", LibraryAlias: "Setups",
	}, checkStub{}, checkStub{}, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/setups", nil)
	request.Host = "127.0.0.1"
	request.Header.Set("Origin", "http://localhost")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	if !server.authorizeMutation(request) {
		t.Fatal("default-port browser authority was rejected")
	}
}

func TestRemoteAPIRequiresConstantTimeBearerAuthentication(t *testing.T) {
	files := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("app")}}
	token := strings.Repeat("s", 40)
	server, err := New(Config{
		ListenAddress: "0.0.0.0:8443", LibraryID: "id", LibraryAlias: "Setups",
		RemoteAccess: true, RemoteAuthToken: token,
	}, checkStub{}, checkStub{}, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := request(server, http.MethodGet, "/api/v1/capabilities")
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthenticated remote = %d", response.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated remote = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteMutationBindsOriginToValidatedRequestHost(t *testing.T) {
	files := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("app")}}
	server, err := New(Config{
		ListenAddress: "0.0.0.0:8443", LibraryID: "id", LibraryAlias: "Setups",
		RemoteAccess: true, RemoteAuthToken: strings.Repeat("s", 40),
	}, checkStub{}, checkStub{}, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/setups", nil)
	valid.Host = "machine.example:8443"
	valid.Header.Set("Origin", "https://machine.example:8443")
	valid.Header.Set("X-CSRF-Token", server.csrfToken)
	if !server.authorizeMutation(valid) {
		t.Fatal("valid authenticated-origin authority was rejected")
	}
	wrongOrigin := valid.Clone(valid.Context())
	wrongOrigin.Header = valid.Header.Clone()
	wrongOrigin.Header.Set("Origin", "https://evil.example:8443")
	if server.authorizeMutation(wrongOrigin) {
		t.Fatal("cross-origin remote mutation was accepted")
	}
	invalidHost := valid.Clone(valid.Context())
	invalidHost.Header = valid.Header.Clone()
	invalidHost.Host = "machine.example:8443/invalid"
	if server.authorizeMutation(invalidHost) {
		t.Fatal("invalid remote Host header was accepted")
	}
}

func TestHeadAndMethodContracts(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	response := request(server, http.MethodHead, "/api/v1/capabilities")
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD capabilities = %d body=%q", response.Code, response.Body.String())
	}
	response = request(server, http.MethodDelete, "/unknown-ui-route")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE UI = %d", response.Code)
	}
}

func TestSafeRouteContextIdentifiesLongUploadOperations(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, test := range []struct {
		path, route, setupID, jobID string
	}{
		{"/api/v1/jobs/" + id + "/upload", "job-upload", "", id},
		{"/api/v1/setups/" + id + "/upload-jobs", "setup-upload-jobs", id, ""},
		{"/api/v1/setups/name-check", "setup-name-check", "", ""},
		{"/api/v1/setup-imports/preflight", "setup-import-preflight", "", ""},
	} {
		route, setupID, _, _, jobID := safeRouteContext(test.path)
		if route != test.route || setupID != test.setupID || jobID != test.jobID {
			t.Fatalf("safe route %q = route=%q setup=%q job=%q", test.path, route, setupID, jobID)
		}
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.readDeadlines = append(r.readDeadlines, deadline)
	return nil
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.writeDeadlines = append(r.writeDeadlines, deadline)
	return nil
}

func TestStreamingBodyUsesSlidingReadIdleDeadline(t *testing.T) {
	underlying := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	recorder := &statusRecorder{ResponseWriter: underlying, status: http.StatusOK}
	timeout := 3 * time.Second
	started := time.Now()
	body := &idleDeadlineReadCloser{
		ReadCloser: io.NopCloser(strings.NewReader("streaming upload")),
		controller: http.NewResponseController(recorder),
		timeout:    timeout,
	}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if len(underlying.readDeadlines) < 1 {
		t.Fatal("request body read did not set a connection deadline")
	}
	for _, deadline := range underlying.readDeadlines {
		if deadline.Before(started.Add(timeout)) || deadline.After(time.Now().Add(timeout+time.Second)) {
			t.Fatalf("unexpected sliding deadline %s", deadline)
		}
	}
	recorder.writeIdleTimeout = timeout
	if _, err := recorder.Write([]byte("streaming response")); err != nil {
		t.Fatal(err)
	}
	if len(underlying.writeDeadlines) < 1 {
		t.Fatal("response write did not set a connection deadline")
	}
}

func TestBeginShutdownCancelsTrackedOperation(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/setups", nil)
	tracked, done := server.trackRequest(request)
	defer done()
	server.BeginShutdown()
	select {
	case <-tracked.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("tracked mutation context was not cancelled")
	}
	server.requestsMu.Lock()
	active := len(server.requests)
	server.requestsMu.Unlock()
	if active != 1 {
		t.Fatalf("tracked operation removed before handler cleanup: %d", active)
	}
	done()
	server.requestsMu.Lock()
	active = len(server.requests)
	server.requestsMu.Unlock()
	if active != 0 {
		t.Fatalf("tracked operation leaked after handler cleanup: %d", active)
	}
}

func TestBeginShutdownClosesTrackedBlockingRequestBody(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	body := newBlockingReadCloser()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/setups", body)
	tracked, done := server.trackRequest(request)
	defer done()
	readResult := make(chan error, 1)
	go func() {
		_, err := tracked.Body.Read(make([]byte, 1))
		readResult <- err
	}()

	server.BeginShutdown()
	select {
	case err := <-readResult:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("blocking body read error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not unblock the active request body read")
	}
	select {
	case <-tracked.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the active request context")
	}
}

func request(server http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1:8080"+target, nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

var _ fs.FS = fstest.MapFS{}
