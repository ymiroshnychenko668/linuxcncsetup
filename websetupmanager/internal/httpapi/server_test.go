package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/auth"
	appdb "github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
)

type checkStub struct{ err error }

func (c checkStub) Check(context.Context) error { return c.err }

type fakeAuthenticator struct {
	mu        sync.Mutex
	users     []string
	passwords []string
	err       error
	entered   chan struct{}
	release   chan struct{}
}

func (f *fakeAuthenticator) Authenticate(ctx context.Context, username, password string) error {
	f.mu.Lock()
	f.users = append(f.users, username)
	f.passwords = append(f.passwords, password)
	entered, release, result := f.entered, f.release, f.err
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return result
}

func (f *fakeAuthenticator) calls() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.users...), append([]string(nil), f.passwords...)
}

func newRemoteTestServer(t *testing.T, token string, authenticator *fakeAuthenticator, concurrency, attempts int) *Server {
	t.Helper()
	sessions := auth.NewStore(30*time.Minute, 12*time.Hour, 32)
	t.Cleanup(func() { _ = sessions.Close() })
	return newRemoteTestServerWithStore(t, token, authenticator, concurrency, attempts, sessions)
}

func newRemoteTestServerWithStore(t *testing.T, token string, authenticator *fakeAuthenticator, concurrency, attempts int, sessions *auth.Store) *Server {
	t.Helper()
	files := fstest.MapFS{
		"index.html":             &fstest.MapFile{Data: []byte("<!doctype html><h1>Setups</h1>")},
		"assets/app-deadbeef.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	}
	if authenticator == nil {
		authenticator = &fakeAuthenticator{}
	}
	if concurrency == 0 {
		concurrency = 2
	}
	if attempts == 0 {
		attempts = 5
	}
	server, err := NewAuthenticated(Config{
		ListenAddress: "0.0.0.0:8443", LibraryID: "id", LibraryAlias: "Setups",
		RemoteAccess: true, AllowedUser: "operator", AuthRememberTimeout: 30 * 24 * time.Hour,
		AuthConcurrency: concurrency, RemoteAuthToken: token,
	}, checkStub{}, checkStub{}, files, AuthDependencies{
		Authenticator: authenticator, Sessions: sessions,
		Throttler: auth.NewThrottler(attempts, 10*time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

type authenticationResult struct {
	cookie        *http.Cookie
	cookies       []*http.Cookie
	csrf          string
	authenticated bool
	loginRequired bool
	username      string
	status        int
	body          string
}

func remoteLogin(t *testing.T, server http.Handler, username, password string, remember bool) authenticationResult {
	return remoteLoginWithCookie(t, server, username, password, remember, nil)
}

func remoteLoginWithCookie(t *testing.T, server http.Handler, username, password string, remember bool, cookie *http.Cookie) authenticationResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"username": username, "password": password, "rememberMe": remember,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/auth/login", bytes.NewReader(body))
	request.Host = "machine.example:8443"
	request.Header.Set("Origin", "https://machine.example:8443")
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return decodeAuthenticationResult(t, response)
}

func decodeAuthenticationResult(t *testing.T, response *httptest.ResponseRecorder) authenticationResult {
	t.Helper()
	result := authenticationResult{status: response.Code, body: response.Body.String()}
	var payload struct {
		Authenticated bool   `json:"authenticated"`
		LoginRequired bool   `json:"loginRequired"`
		CSRF          string `json:"csrfToken"`
		User          *struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if response.Body.Len() != 0 && json.Unmarshal(response.Body.Bytes(), &payload) == nil {
		result.authenticated = payload.Authenticated
		result.loginRequired = payload.LoginRequired
		result.csrf = payload.CSRF
		if payload.User != nil {
			result.username = payload.User.Username
		}
	}
	result.cookies = response.Result().Cookies()
	for _, cookie := range result.cookies {
		if cookie.Name == remoteSessionCookieName {
			result.cookie = cookie
		}
	}
	return result
}

func remoteRequest(server http.Handler, method, target string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://machine.example:8443"+target, nil)
	request.Host = "machine.example:8443"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if isMutation(method) {
		request.Header.Set("Origin", "https://machine.example:8443")
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func activateRemoteSession(t *testing.T, server http.Handler, result authenticationResult) authenticationResult {
	t.Helper()
	if result.status != http.StatusOK || result.cookie == nil || result.csrf == "" {
		t.Fatalf("cannot activate invalid login result: %+v", result)
	}
	request := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/auth/activate", nil)
	request.Host = "machine.example:8443"
	request.AddCookie(result.cookie)
	request.Header.Set("Origin", "https://machine.example:8443")
	request.Header.Set("X-CSRF-Token", result.csrf)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("activate remote session = %d %s", response.Code, response.Body.String())
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("session activation emitted Set-Cookie: %+v", cookies)
	}
	return result
}

func activatedRemoteRequest(server http.Handler, method, target string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://machine.example:8443"+target, nil)
	request.Host = "machine.example:8443"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if isMutation(method) {
		request.Header.Set("Origin", "https://machine.example:8443")
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertRemoteSessionCookieCleared(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cleared cookie count = %d, cookies=%+v", len(cookies), cookies)
	}
	session := cookies[0]
	if session == nil || session.Value != "" || session.MaxAge != -1 || !session.Secure || !session.HttpOnly ||
		session.Name != remoteSessionCookieName || session.Path != "/" || session.Domain != "" ||
		session.SameSite != http.SameSiteStrictMode || session.Expires.IsZero() {
		t.Fatalf("cleared session cookie = %+v", session)
	}
}

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
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "frame-src 'self' blob:") {
		t.Fatalf("application CSP must allow only the intended same-origin/sanitized-blob frames: %q", policy)
	}
	if policy := response.Header().Get("Content-Security-Policy"); strings.Contains(policy, "'"+sanitizedHTMLStyleHash+"'") || !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("application CSP must permit the trusted React shell's dynamic geometry styles: %q", policy)
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

func TestRemoteStaticIsPublicAndProtectedAPIUsesBearerWithoutBasic(t *testing.T) {
	token := strings.Repeat("s", 40)
	server := newRemoteTestServer(t, token, nil, 0, 0)
	for _, target := range []string{"/", "/setups/example", "/assets/app-deadbeef.js", "/healthz", "/readyz"} {
		response := remoteRequest(server, http.MethodGet, target, nil, "")
		if response.Code != http.StatusOK {
			t.Fatalf("public remote route %q = %d %s", target, response.Code, response.Body.String())
		}
	}

	response := remoteRequest(server, http.MethodGet, "/api/v1/capabilities", nil, "")
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Header().Get("WWW-Authenticate"), "Basic") ||
		response.Header().Get("WWW-Authenticate") != `Bearer realm="web-setup-manager"` {
		t.Fatalf("unauthenticated remote = %d challenge=%q", response.Code, response.Header().Values("WWW-Authenticate"))
	}

	basic := httptest.NewRequest(http.MethodGet, "https://machine.example:8443/api/v1/capabilities", nil)
	basic.SetBasicAuth("websetup", token)
	basicResponse := httptest.NewRecorder()
	server.ServeHTTP(basicResponse, basic)
	if basicResponse.Code != http.StatusUnauthorized || strings.Contains(strings.Join(basicResponse.Header().Values("WWW-Authenticate"), " "), "Basic") {
		t.Fatalf("Basic authentication remained enabled: %d %#v", basicResponse.Code, basicResponse.Header().Values("WWW-Authenticate"))
	}

	bearer := httptest.NewRequest(http.MethodGet, "https://machine.example:8443/api/v1/capabilities", nil)
	bearer.Header.Set("Authorization", "Bearer "+token)
	bearerResponse := httptest.NewRecorder()
	server.ServeHTTP(bearerResponse, bearer)
	if bearerResponse.Code != http.StatusOK || !strings.Contains(bearerResponse.Body.String(), server.csrfToken) {
		t.Fatalf("Bearer authentication = %d %s", bearerResponse.Code, bearerResponse.Body.String())
	}
}

func TestRemoteWithoutOptionalBearerHasNoBearerCredentialPath(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	response := remoteRequest(server, http.MethodGet, "/api/v1/capabilities", nil, "")
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("optional-Bearer 401 = %d challenge=%q", response.Code, response.Header().Get("WWW-Authenticate"))
	}
	request := httptest.NewRequest(http.MethodGet, "https://machine.example:8443/api/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer ")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("empty Bearer accepted: %d", recorder.Code)
	}
}

func TestAuthenticationSessionSupportsLocalAndRemoteGuestModes(t *testing.T) {
	local, _ := newTestServer(t, nil, nil)
	response := request(local, http.MethodGet, "/api/v1/auth/session")
	result := decodeAuthenticationResult(t, response)
	if result.status != http.StatusOK || !result.authenticated || result.loginRequired || result.username != "" || result.csrf != local.csrfToken {
		t.Fatalf("local auth session = %+v", result)
	}

	remote := newRemoteTestServer(t, "", nil, 0, 0)
	response = remoteRequest(remote, http.MethodGet, "/api/v1/auth/session", nil, "")
	result = decodeAuthenticationResult(t, response)
	if result.status != http.StatusOK || result.authenticated || !result.loginRequired || result.username != "" || result.csrf != "" {
		t.Fatalf("remote guest session = %+v", result)
	}
}

func TestRemoteLoginCreatesSecureOpaqueSessionAndCapabilitiesUseItsCSRF(t *testing.T) {
	authenticator := &fakeAuthenticator{}
	server := newRemoteTestServer(t, "", authenticator, 0, 0)
	result := remoteLogin(t, server, "operator", "correct-password", false)
	if result.status != http.StatusOK || !result.authenticated || !result.loginRequired || result.username != "operator" || result.csrf == "" {
		t.Fatalf("login = %+v", result)
	}
	if result.cookie == nil || result.cookie.Name != remoteSessionCookieName || result.cookie.Value == "" ||
		!result.cookie.Secure || !result.cookie.HttpOnly || result.cookie.Path != "/" || result.cookie.Domain != "" ||
		result.cookie.SameSite != http.SameSiteStrictMode || result.cookie.MaxAge != 0 || !result.cookie.Expires.IsZero() ||
		result.cookie.Value == result.csrf {
		t.Fatalf("session cookie/CSRF contract = cookie=%+v csrf=%q", result.cookie, result.csrf)
	}
	if len(result.cookies) != 1 {
		t.Fatalf("login must set only the provisional HttpOnly session cookie: %+v", result.cookies)
	}
	users, passwords := authenticator.calls()
	if len(users) != 1 || users[0] != "operator" || passwords[0] != "correct-password" {
		t.Fatalf("PAM calls = users %#v passwords %#v", users, passwords)
	}

	result = activateRemoteSession(t, server, result)
	sessionResponse := activatedRemoteRequest(server, http.MethodGet, "/api/v1/auth/session", result.cookie, result.csrf)
	session := decodeAuthenticationResult(t, sessionResponse)
	if !session.authenticated || session.csrf != result.csrf || session.username != "operator" {
		t.Fatalf("restored session = %+v", session)
	}
	capabilities := activatedRemoteRequest(server, http.MethodGet, "/api/v1/capabilities", result.cookie, result.csrf)
	if capabilities.Code != http.StatusOK || !strings.Contains(capabilities.Body.String(), result.csrf) ||
		strings.Contains(capabilities.Body.String(), server.csrfToken) {
		t.Fatalf("session capabilities leaked/replaced CSRF: %d %s", capabilities.Code, capabilities.Body.String())
	}
}

func TestProvisionalRemoteSessionRequiresExplicitActivation(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	login := remoteLogin(t, server, "operator", "secret", false)
	if login.status != http.StatusOK || login.cookie == nil || login.csrf == "" {
		t.Fatalf("login = %+v", login)
	}
	if _, session, ok := server.browserSession(requestWithCookie(login.cookie)); !ok || session.Activated {
		t.Fatal("provisional server session was not created")
	}

	response := remoteRequest(server, http.MethodGet, "/api/v1/auth/session", login.cookie, "")
	result := decodeAuthenticationResult(t, response)
	if result.status != http.StatusOK || result.authenticated || !result.loginRequired || result.username != "" || result.csrf != "" {
		t.Fatalf("provisional session probe = %+v", result)
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("provisional session probe mutated cookies: %+v", cookies)
	}
	if _, session, ok := server.browserSession(requestWithCookie(login.cookie)); !ok || session.Activated {
		t.Fatal("provisional session probe deleted the server session")
	}

	if unauthorized := remoteRequest(server, http.MethodGet, "/api/v1/capabilities", login.cookie, ""); unauthorized.Code != http.StatusUnauthorized || len(unauthorized.Result().Cookies()) != 0 {
		t.Fatalf("provisional session reached protected API: %d cookies=%+v body=%s", unauthorized.Code, unauthorized.Result().Cookies(), unauthorized.Body.String())
	}
	login = activateRemoteSession(t, server, login)
	if _, session, ok := server.browserSession(requestWithCookie(login.cookie)); !ok || !session.Activated {
		t.Fatal("exact activation did not promote the server session")
	}
	after := activatedRemoteRequest(server, http.MethodGet, "/api/v1/auth/session", login.cookie, login.csrf)
	activated := decodeAuthenticationResult(t, after)
	if !activated.authenticated || activated.username != "operator" || activated.csrf != login.csrf {
		t.Fatalf("activated session = %+v", activated)
	}
}

func TestRemoteSessionActivationRequiresExactOriginAndCSRF(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	login := remoteLogin(t, server, "operator", "secret", false)
	for _, test := range []struct {
		name      string
		origin    string
		csrf      string
		errorCode string
	}{
		{name: "missing origin", csrf: login.csrf, errorCode: "ORIGIN_REJECTED"},
		{name: "HTTP origin", origin: "http://machine.example:8443", csrf: login.csrf, errorCode: "ORIGIN_REJECTED"},
		{name: "wrong CSRF", origin: "https://machine.example:8443", csrf: "wrong", errorCode: "CSRF_REJECTED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/auth/activate", nil)
			request.Host = "machine.example:8443"
			request.AddCookie(login.cookie)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			request.Header.Set("X-CSRF-Token", test.csrf)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), test.errorCode) {
				t.Fatalf("activation rejection = %d %s", response.Code, response.Body.String())
			}
			if cookies := response.Result().Cookies(); len(cookies) != 0 {
				t.Fatalf("rejected activation mutated browser cookies: %+v", cookies)
			}
			if _, session, ok := server.browserSession(requestWithCookie(login.cookie)); !ok || session.Activated {
				t.Fatalf("rejected activation changed the provisional session: %+v, %v", session, ok)
			}
		})
	}
	_ = activateRemoteSession(t, server, login)
}

func TestStaleActivationCannotActivateOrDamageNewerSession(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	stale := remoteLogin(t, server, "operator", "first-password", false)
	current := remoteLogin(t, server, "operator", "second-password", false)
	if stale.cookie == nil || current.cookie == nil || stale.cookie.Value == current.cookie.Value || stale.csrf == current.csrf {
		t.Fatalf("independent login sessions = stale=%+v current=%+v", stale, current)
	}

	request := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/auth/activate", nil)
	request.Host = "machine.example:8443"
	request.AddCookie(current.cookie)
	request.Header.Set("Origin", "https://machine.example:8443")
	request.Header.Set("X-CSRF-Token", stale.csrf)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "CSRF_REJECTED") || len(response.Result().Cookies()) != 0 {
		t.Fatalf("stale activation = %d cookies=%+v body=%s", response.Code, response.Result().Cookies(), response.Body.String())
	}
	if _, session, ok := server.browserSession(requestWithCookie(current.cookie)); !ok || session.Activated {
		t.Fatalf("stale CSRF activated or deleted current session: %+v, %v", session, ok)
	}
	if probe := remoteRequest(server, http.MethodGet, "/api/v1/auth/session", current.cookie, ""); decodeAuthenticationResult(t, probe).authenticated {
		t.Fatal("current provisional session authenticated after stale activation")
	}

	current = activateRemoteSession(t, server, current)
	if capabilities := activatedRemoteRequest(server, http.MethodGet, "/api/v1/capabilities", current.cookie, current.csrf); capabilities.Code != http.StatusOK {
		t.Fatalf("current session was damaged by stale activation: %d %s", capabilities.Code, capabilities.Body.String())
	}
	if _, session, ok := server.browserSession(requestWithCookie(stale.cookie)); !ok || session.Activated {
		t.Fatalf("stale session changed unexpectedly: %+v, %v", session, ok)
	}
}

func TestRememberedLoginSetsPersistentStrictCookie(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	before := time.Now().Add(30 * 24 * time.Hour)
	result := remoteLogin(t, server, "operator", "correct-password", true)
	if result.status != http.StatusOK || result.cookie == nil || result.cookie.MaxAge <= 0 || result.cookie.Expires.IsZero() {
		t.Fatalf("remembered cookie = %+v response=%s", result.cookie, result.body)
	}
	if delta := result.cookie.Expires.Sub(before); delta < -2*time.Second || delta > 2*time.Second {
		t.Fatalf("remembered expiry = %s want near %s", result.cookie.Expires, before)
	}
	_ = activateRemoteSession(t, server, result)
}

func TestRememberedSessionActivationSurvivesStoreAndServerRestart(t *testing.T) {
	db, err := appdb.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const scope = "https://machine.example:8443/library/id"
	firstStore, err := auth.NewPersistentStore(db.SQL(), 30*time.Minute, 12*time.Hour, 32, "operator", scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	firstServer := newRemoteTestServerWithStore(t, "", nil, 0, 0, firstStore)
	login := activateRemoteSession(t, firstServer, remoteLogin(t, firstServer, "operator", "secret", true))
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	restoredStore, err := auth.NewPersistentStore(db.SQL(), 30*time.Minute, 12*time.Hour, 32, "operator", scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoredStore.Close() })
	restoredServer := newRemoteTestServerWithStore(t, "", nil, 0, 0, restoredStore)
	response := activatedRemoteRequest(restoredServer, http.MethodGet, "/api/v1/auth/session", login.cookie, login.csrf)
	restored := decodeAuthenticationResult(t, response)
	if !restored.authenticated || restored.username != "operator" || restored.csrf != login.csrf {
		t.Fatalf("remembered activation after restart = %+v", restored)
	}
	if _, session, ok := restoredServer.browserSession(requestWithCookie(login.cookie)); !ok || !session.Activated || !session.Remembered {
		t.Fatalf("restored server session = %+v, %v", session, ok)
	}
}

func TestSuccessfulReloginRotatesExistingBrowserSession(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	first := activateRemoteSession(t, server, remoteLogin(t, server, "operator", "first-password", true))
	second := remoteLoginWithCookie(t, server, "operator", "second-password", false, first.cookie)
	if first.status != http.StatusOK || second.status != http.StatusOK || second.cookie == nil ||
		second.cookie.Value == first.cookie.Value || second.csrf == first.csrf {
		t.Fatalf("relogin did not rotate credentials: first=%+v second=%+v", first, second)
	}
	second = activateRemoteSession(t, server, second)
	if stale := activatedRemoteRequest(server, http.MethodGet, "/api/v1/capabilities", first.cookie, first.csrf); stale.Code != http.StatusUnauthorized {
		t.Fatalf("old browser session survived relogin: %d", stale.Code)
	}
	if current := activatedRemoteRequest(server, http.MethodGet, "/api/v1/capabilities", second.cookie, second.csrf); current.Code != http.StatusOK {
		t.Fatalf("replacement browser session is unusable: %d %s", current.Code, current.Body.String())
	}
}

func TestRemoteLoginRequiresExactHTTPSOriginBeforePAM(t *testing.T) {
	authenticator := &fakeAuthenticator{}
	server := newRemoteTestServer(t, "", authenticator, 0, 0)
	body := `{"username":"operator","password":"secret","rememberMe":false}`
	for _, test := range []struct {
		name   string
		host   string
		origin []string
	}{
		{name: "missing", host: "machine.example:8443"},
		{name: "plain HTTP", host: "machine.example:8443", origin: []string{"http://machine.example:8443"}},
		{name: "wrong host", host: "machine.example:8443", origin: []string{"https://evil.example:8443"}},
		{name: "multiple", host: "machine.example:8443", origin: []string{"https://machine.example:8443", "https://machine.example:8443"}},
		{name: "origin path", host: "machine.example:8443", origin: []string{"https://machine.example:8443/path"}},
		{name: "invalid host", host: "machine.example:8443/invalid", origin: []string{"https://machine.example:8443"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/auth/login", strings.NewReader(body))
			request.Host = test.host
			request.Header.Set("Content-Type", "application/json")
			for _, origin := range test.origin {
				request.Header.Add("Origin", origin)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ORIGIN_REJECTED") {
				t.Fatalf("origin rejection = %d %s", response.Code, response.Body.String())
			}
		})
	}
	users, _ := authenticator.calls()
	if len(users) != 0 {
		t.Fatalf("rejected origins reached PAM: %#v", users)
	}
}

func TestUnknownUsernameStillAuthenticatesOnlyConfiguredPAMAccount(t *testing.T) {
	authenticator := &fakeAuthenticator{}
	server := newRemoteTestServer(t, "", authenticator, 0, 0)
	wrongUser := remoteLogin(t, server, "somebody-else", "guess", false)
	if wrongUser.status != http.StatusUnauthorized || !strings.Contains(wrongUser.body, `"code":"AUTHENTICATION_FAILED"`) {
		t.Fatalf("wrong user = %d %s", wrongUser.status, wrongUser.body)
	}
	users, passwords := authenticator.calls()
	if len(users) != 1 || users[0] != "operator" || passwords[0] != "guess" {
		t.Fatalf("wrong username probed PAM accounts: %#v %#v", users, passwords)
	}
}

func TestRemoteLoginThrottlingAndConcurrencyAreBounded(t *testing.T) {
	t.Run("attempts", func(t *testing.T) {
		authenticator := &fakeAuthenticator{err: auth.ErrInvalidCredentials}
		server := newRemoteTestServer(t, "", authenticator, 1, 2)
		for index := 0; index < 2; index++ {
			if result := remoteLogin(t, server, "operator", "wrong", false); result.status != http.StatusUnauthorized {
				t.Fatalf("failed login %d = %d %s", index, result.status, result.body)
			}
		}
		if result := remoteLogin(t, server, "operator", "wrong", false); result.status != http.StatusTooManyRequests {
			t.Fatalf("rate-limited login = %d %s", result.status, result.body)
		}
		users, _ := authenticator.calls()
		if len(users) != 2 {
			t.Fatalf("rate-limited login reached PAM: %d calls", len(users))
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		authenticator := &fakeAuthenticator{entered: make(chan struct{}, 1), release: make(chan struct{})}
		server := newRemoteTestServer(t, "", authenticator, 1, 10)
		firstDone := make(chan authenticationResult, 1)
		go func() { firstDone <- remoteLogin(t, server, "operator", "secret", false) }()
		select {
		case <-authenticator.entered:
		case <-time.After(time.Second):
			t.Fatal("first login did not enter PAM")
		}
		second := remoteLogin(t, server, "operator", "secret", false)
		if second.status != http.StatusTooManyRequests {
			t.Fatalf("concurrent login = %d %s", second.status, second.body)
		}
		close(authenticator.release)
		if first := <-firstDone; first.status != http.StatusOK {
			t.Fatalf("first login = %d %s", first.status, first.body)
		}
	})
}

func TestSessionMutationUsesExactOriginAndPerSessionCSRF(t *testing.T) {
	server := newRemoteTestServer(t, strings.Repeat("b", 40), nil, 0, 0)
	login := remoteLogin(t, server, "operator", "secret", false)
	if login.status != http.StatusOK {
		t.Fatalf("login = %d %s", login.status, login.body)
	}
	_, session, ok := server.browserSession(requestWithCookie(login.cookie))
	if !ok {
		t.Fatal("created browser session was not retrievable")
	}
	base := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/setups", nil)
	base.Host = "machine.example:8443"
	base.Header.Set("Origin", "https://machine.example:8443")
	base.Header.Set("X-CSRF-Token", login.csrf)
	base = base.WithContext(withRequestPrincipal(base.Context(), requestPrincipal{kind: principalSession, session: session}))
	if !server.authorizeMutation(base) {
		t.Fatal("valid session mutation rejected")
	}
	for name, mutate := range map[string]func(*http.Request){
		"missing origin": func(r *http.Request) { r.Header.Del("Origin") },
		"HTTP origin":    func(r *http.Request) { r.Header.Set("Origin", "http://machine.example:8443") },
		"wrong origin":   func(r *http.Request) { r.Header.Set("Origin", "https://evil.example:8443") },
		"wrong CSRF":     func(r *http.Request) { r.Header.Set("X-CSRF-Token", server.csrfToken) },
		"invalid host":   func(r *http.Request) { r.Host = "machine.example/invalid" },
	} {
		copyRequest := base.Clone(base.Context())
		copyRequest.Header = base.Header.Clone()
		mutate(copyRequest)
		if server.authorizeMutation(copyRequest) {
			t.Fatalf("session mutation accepted %s", name)
		}
		response := httptest.NewRecorder()
		if requireMutation(server, response, copyRequest, "request-id") {
			t.Fatalf("requireMutation accepted %s", name)
		}
		wantCode := "ORIGIN_REJECTED"
		if name == "wrong CSRF" {
			wantCode = "CSRF_REJECTED"
		}
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), wantCode) {
			t.Fatalf("session rejection %s = %d %s, want %s", name, response.Code, response.Body.String(), wantCode)
		}
	}

	bearer := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/setups", nil)
	bearer.Host = "machine.example:8443"
	bearer.Header.Set("X-CSRF-Token", server.csrfToken)
	bearer = bearer.WithContext(withRequestPrincipal(bearer.Context(), requestPrincipal{kind: principalBearer}))
	if !server.authorizeMutation(bearer) {
		t.Fatal("Bearer mutation did not preserve global CSRF semantics")
	}
}

func TestLogoutRequiresSessionOriginAndCSRFThenRevokesCookie(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	login := activateRemoteSession(t, server, remoteLogin(t, server, "operator", "secret", false))
	for _, test := range []struct {
		name      string
		origin    string
		csrf      string
		errorCode string
	}{
		{name: "missing origin", csrf: login.csrf, errorCode: "ORIGIN_REJECTED"},
		{name: "HTTP origin", origin: "http://machine.example:8443", csrf: login.csrf, errorCode: "ORIGIN_REJECTED"},
		{name: "wrong CSRF", origin: "https://machine.example:8443", csrf: "wrong", errorCode: "CSRF_REJECTED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/auth/logout", nil)
			request.Host = "machine.example:8443"
			request.AddCookie(login.cookie)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			request.Header.Set("X-CSRF-Token", test.csrf)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), test.errorCode) {
				t.Fatalf("logout rejection = %d %s", response.Code, response.Body.String())
			}
			if cookies := response.Result().Cookies(); len(cookies) != 0 {
				t.Fatalf("rejected logout mutated browser cookies: %+v", cookies)
			}
			if stillValid := activatedRemoteRequest(server, http.MethodGet, "/api/v1/capabilities", login.cookie, login.csrf); stillValid.Code != http.StatusOK {
				t.Fatalf("rejected logout revoked session: %d", stillValid.Code)
			}
		})
	}

	response := activatedRemoteRequest(server, http.MethodPost, "/api/v1/auth/logout", login.cookie, login.csrf)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", response.Code, response.Body.String())
	}
	assertRemoteSessionCookieCleared(t, response)
	if after := activatedRemoteRequest(server, http.MethodGet, "/api/v1/capabilities", login.cookie, login.csrf); after.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out cookie remained valid: %d", after.Code)
	}
}

func TestStaleSessionRevocationNeverMutatesBrowserCookie(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	login := activateRemoteSession(t, server, remoteLogin(t, server, "operator", "secret", false))

	rejectedRequest := httptest.NewRequest(http.MethodPost, "https://machine.example:8443/api/v1/auth/revoke-stale", nil)
	rejectedRequest.Host = "machine.example:8443"
	rejectedRequest.AddCookie(login.cookie)
	rejectedRequest.Header.Set("Origin", "https://machine.example:8443")
	rejectedRequest.Header.Set("X-CSRF-Token", "wrong")
	rejected := httptest.NewRecorder()
	server.ServeHTTP(rejected, rejectedRequest)
	if rejected.Code != http.StatusForbidden || len(rejected.Result().Cookies()) != 0 {
		t.Fatalf("rejected stale revoke = %d cookies=%+v body=%s", rejected.Code, rejected.Result().Cookies(), rejected.Body.String())
	}
	response := activatedRemoteRequest(server, http.MethodPost, "/api/v1/auth/revoke-stale", login.cookie, login.csrf)
	if response.Code != http.StatusNoContent || len(response.Result().Cookies()) != 0 {
		t.Fatalf("stale revoke = %d cookies=%+v body=%s", response.Code, response.Result().Cookies(), response.Body.String())
	}
	if after := activatedRemoteRequest(server, http.MethodGet, "/api/v1/capabilities", login.cookie, login.csrf); after.Code != http.StatusUnauthorized {
		t.Fatalf("stale-revoked session remained valid: %d", after.Code)
	}
}

func TestExplicitInvalidAuthorizationDoesNotFallBackToValidCookie(t *testing.T) {
	server := newRemoteTestServer(t, strings.Repeat("b", 40), nil, 0, 0)
	login := activateRemoteSession(t, server, remoteLogin(t, server, "operator", "secret", false))
	request := httptest.NewRequest(http.MethodGet, "https://machine.example:8443/api/v1/capabilities", nil)
	request.AddCookie(login.cookie)
	request.Header.Set("Authorization", "Bearer wrong")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid explicit Authorization fell back to cookie: %d", response.Code)
	}
}

func TestRemoteConstructorFailsClosedWithoutAuthenticationDependencies(t *testing.T) {
	files := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("app")}}
	_, err := New(Config{
		ListenAddress: "0.0.0.0:8443", RemoteAccess: true, AllowedUser: "operator",
		AuthRememberTimeout: time.Hour, AuthConcurrency: 1,
	}, checkStub{}, checkStub{}, files, nil)
	if err == nil {
		t.Fatal("remote server without PAM/session dependencies was constructed")
	}
}

func TestAuthenticationEndpointMethodContracts(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	for _, test := range []struct {
		method, target, allow string
	}{
		{http.MethodGet, "/api/v1/auth/login", http.MethodPost},
		{http.MethodPost, "/api/v1/auth/session", http.MethodGet},
		{http.MethodGet, "/api/v1/auth/activate", http.MethodPost},
		{http.MethodGet, "/api/v1/auth/logout", http.MethodPost},
		{http.MethodGet, "/api/v1/auth/revoke-stale", http.MethodPost},
	} {
		response := remoteRequest(server, test.method, test.target, nil, "")
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != test.allow {
			t.Fatalf("%s %s = %d Allow=%q", test.method, test.target, response.Code, response.Header().Get("Allow"))
		}
	}
}

func TestDuplicateSessionCookiesAreRejectedWithoutAmbiguity(t *testing.T) {
	server := newRemoteTestServer(t, "", nil, 0, 0)
	login := activateRemoteSession(t, server, remoteLogin(t, server, "operator", "secret", false))
	request := httptest.NewRequest(http.MethodGet, "https://machine.example:8443/api/v1/capabilities", nil)
	request.AddCookie(login.cookie)
	request.AddCookie(&http.Cookie{Name: remoteSessionCookieName, Value: "attacker-controlled"})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate session cookies were accepted: %d", response.Code)
	}
}

func requestWithCookie(cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "https://machine.example:8443/", nil)
	request.AddCookie(cookie)
	return request
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
		{"/api/v1/catalog", "catalog", "", ""},
		{"/api/v1/catalog/folders/" + id, "catalog-folder", "", ""},
		{"/api/v1/catalog/setups/" + id + "/program/content", "catalog-setup-program-content", id, ""},
		{"/api/v1/catalog/setups/" + id + "/setup-sheet", "catalog-setup-setup-sheet", id, ""},
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
