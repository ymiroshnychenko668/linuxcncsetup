package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
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

func request(server http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1:8080"+target, nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

var _ fs.FS = fstest.MapFS{}
