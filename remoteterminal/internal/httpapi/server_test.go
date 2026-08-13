package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/auth"
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/codeservers"
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/sessions"
)

const testID = "0123456789abcdef0123456789abcdef"

type fakeAuthenticator struct {
	mu        sync.Mutex
	err       error
	users     []string
	passwords []string
	block     <-chan struct{}
	entered   chan<- struct{}
}

func (f *fakeAuthenticator) Authenticate(ctx context.Context, user, password string) error {
	f.mu.Lock()
	f.users = append(f.users, user)
	f.passwords = append(f.passwords, password)
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

type fakeSessionManager struct {
	mu             sync.Mutex
	items          []sessions.Session
	createErr      error
	deleteErr      error
	connectErr     error
	selectionErr   error
	proxyErr       error
	proxyCalled    int
	deletedID      string
	connectedID    string
	selectionID    string
	selectionText  string
	active         int
	proxiedRequest *http.Request
	proxyHandler   http.Handler
}

func (f *fakeSessionManager) List(context.Context) ([]sessions.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessions.Session(nil), f.items...), nil
}
func (f *fakeSessionManager) Create(_ context.Context, name string) (sessions.Session, error) {
	if f.createErr != nil {
		return sessions.Session{}, f.createErr
	}
	created := sessions.Session{ID: testID, Name: name, Windows: 1}
	f.mu.Lock()
	f.items = append(f.items, created)
	f.mu.Unlock()
	return created, nil
}
func (f *fakeSessionManager) Delete(_ context.Context, id string) error {
	f.deletedID = id
	return f.deleteErr
}
func (f *fakeSessionManager) Connect(_ context.Context, id string) (sessions.Session, string, error) {
	f.connectedID = id
	if f.connectErr != nil {
		return sessions.Session{}, "", f.connectErr
	}
	return sessions.Session{ID: id, Name: "Main", TerminalConnected: true}, "/terminal/" + id + "/", nil
}
func (f *fakeSessionManager) LatestSelection(_ context.Context, id string) (string, error) {
	f.selectionID = id
	return f.selectionText, f.selectionErr
}
func (f *fakeSessionManager) Proxy(_ context.Context, _ string) (http.Handler, error) {
	f.proxyCalled++
	if f.proxyErr != nil {
		return nil, f.proxyErr
	}
	if f.proxyHandler != nil {
		return f.proxyHandler, nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.proxiedRequest = r
		w.WriteHeader(http.StatusNoContent)
	}), nil
}
func (f *fakeSessionManager) ActiveTerminals() int { return f.active }

type fakeCodeServerManager struct {
	mu             sync.Mutex
	items          []codeservers.Instance
	listing        codeservers.DirectoryListing
	browseErr      error
	listErr        error
	createErr      error
	deleteErr      error
	proxyErr       error
	reused         bool
	browsedPath    string
	createdPath    string
	deletedID      string
	proxyCalled    int
	proxiedID      string
	proxiedRequest *http.Request
	proxyHandler   http.Handler
}

func (f *fakeCodeServerManager) Browse(_ context.Context, requestedPath string) (codeservers.DirectoryListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.browsedPath = requestedPath
	return f.listing, f.browseErr
}

func (f *fakeCodeServerManager) List(context.Context) ([]codeservers.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]codeservers.Instance(nil), f.items...), f.listErr
}

func (f *fakeCodeServerManager) Create(_ context.Context, folderPath string) (codeservers.Instance, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdPath = folderPath
	if f.createErr != nil {
		return codeservers.Instance{}, false, f.createErr
	}
	created := codeservers.Instance{ID: testID, Name: "project", FolderPath: folderPath, URL: "/code/" + testID + "/"}
	f.items = append(f.items, created)
	return created, f.reused, nil
}

func (f *fakeCodeServerManager) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedID = id
	return f.deleteErr
}

func (f *fakeCodeServerManager) Proxy(_ context.Context, id string) (http.Handler, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxyCalled++
	f.proxiedID = id
	if f.proxyErr != nil {
		return nil, f.proxyErr
	}
	if f.proxyHandler != nil {
		return f.proxyHandler, nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.proxiedRequest = r
		w.Header().Add("Content-Security-Policy", "frame-ancestors 'self'")
		w.Header().Add("Content-Security-Policy", "default-src 'self'; worker-src 'self' blob:")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.WriteHeader(http.StatusNoContent)
	}), nil
}

type harness struct {
	server      *Server
	auth        *fakeAuthenticator
	manager     *fakeSessionManager
	codeServers *fakeCodeServerManager
}

func newHarness(t *testing.T) harness {
	return newHarnessWithTimeouts(t, time.Hour, time.Hour)
}

func newHarnessWithTimeouts(t *testing.T, idle, absolute time.Duration) harness {
	return newHarnessWithOptions(t, idle, absolute, false)
}

func newHarnessForTransport(t *testing.T, insecureHTTP bool) harness {
	return newHarnessWithOptions(t, time.Hour, time.Hour, insecureHTTP)
}

func newHarnessWithOptions(t *testing.T, idle, absolute time.Duration, insecureHTTP bool) harness {
	t.Helper()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><div>remote app</div>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "asset.js"), []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	authenticator := &fakeAuthenticator{}
	manager := &fakeSessionManager{}
	codeServerManager := &fakeCodeServerManager{}
	server, err := New(Config{
		AllowedUser: "operator", MachineName: "Workshop Mill", WebDir: web, AbsoluteTimeout: time.Hour, AuthConcurrency: 1,
		InsecureHTTP: insecureHTTP,
	}, authenticator, auth.NewStore(idle, absolute, 32), auth.NewThrottler(5, time.Minute), manager, codeServerManager,
		log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return harness{server: server, auth: authenticator, manager: manager, codeServers: codeServerManager}
}

func TestPublicConfigExposesMachineNameWithoutAuthentication(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodGet, "http://example.test/api/config", nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"machineName":"Workshop Mill"`) {
		t.Fatalf("config response = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("config response Cache-Control = %q", response.Header().Get("Cache-Control"))
	}

	request = httptest.NewRequest(http.MethodPost, "http://example.test/api/config", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("config POST status = %d", response.Code)
	}
}

func TestPerIPThrottleDoesNotRevealConfiguredUsernameAfterBogusExhaustion(t *testing.T) {
	h := newHarness(t)
	for index := 0; index < 5; index++ {
		result := login(t, h.server, fmt.Sprintf("bogus-%d", index), "guess")
		if result.status != http.StatusUnauthorized {
			t.Fatalf("bogus attempt %d status = %d", index, result.status)
		}
	}
	bogusLimited := login(t, h.server, "another-bogus-name", "guess")
	allowedLimited := login(t, h.server, "operator", "correct-password")
	if bogusLimited.status != http.StatusTooManyRequests || allowedLimited.status != http.StatusTooManyRequests ||
		string(bogusLimited.body) != string(allowedLimited.body) {
		t.Fatalf("post-exhaustion responses reveal username:\nbogus %d %s\nallowed %d %s",
			bogusLimited.status, bogusLimited.body, allowedLimited.status, allowedLimited.body)
	}
	h.auth.mu.Lock()
	defer h.auth.mu.Unlock()
	if len(h.auth.users) != 5 {
		t.Fatalf("rate-limited attempts unexpectedly reached PAM: %d total calls", len(h.auth.users))
	}
}

type loginResult struct {
	cookie *http.Cookie
	csrf   string
	body   []byte
	status int
}

func login(t *testing.T, server http.Handler, username, password string) loginResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	request := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/login", bytes.NewReader(body))
	request.Host = "example.test"
	request.RemoteAddr = "192.0.2.5:12345"
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	result := response.Result()
	data := response.Body.Bytes()
	var decoded struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.Unmarshal(data, &decoded)
	var cookie *http.Cookie
	for _, candidate := range result.Cookies() {
		if candidate.Name == cookieName {
			candidate := candidate
			cookie = candidate
		}
	}
	return loginResult{cookie: cookie, csrf: decoded.CSRF, body: append([]byte(nil), data...), status: response.Code}
}

func authenticatedRequest(method, target string, cookie *http.Cookie, csrf string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, "http://example.test"+target, body)
	request.Host = "example.test"
	request.RemoteAddr = "192.0.2.5:12345"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Origin", "http://example.test")
		request.Header.Set(csrfHeader, csrf)
	}
	return request
}

func TestLoginSetsSecureOpaqueCookieAndUsesConfiguredPAMUser(t *testing.T) {
	h := newHarness(t)
	result := login(t, h.server, "operator", "secret")
	if result.status != http.StatusOK {
		t.Fatalf("login status = %d: %s", result.status, result.body)
	}
	if result.cookie == nil || !result.cookie.Secure || !result.cookie.HttpOnly ||
		result.cookie.SameSite != http.SameSiteStrictMode || result.cookie.Path != "/" || result.cookie.Value == "" {
		t.Fatalf("insecure login cookie: %+v", result.cookie)
	}
	if result.csrf == "" || result.csrf == result.cookie.Value {
		t.Fatalf("invalid independent CSRF token: %q", result.csrf)
	}
	h.auth.mu.Lock()
	defer h.auth.mu.Unlock()
	if len(h.auth.users) != 1 || h.auth.users[0] != "operator" || h.auth.passwords[0] != "secret" {
		t.Fatalf("PAM calls = users %#v passwords %#v", h.auth.users, h.auth.passwords)
	}
}

func TestAuthenticationCookieWorksThroughConfiguredTransport(t *testing.T) {
	for _, test := range []struct {
		name         string
		insecureHTTP bool
		cookieName   string
		secure       bool
	}{
		{name: "https", cookieName: secureCookieName, secure: true},
		{name: "http", insecureHTTP: true, cookieName: insecureCookieName},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarnessForTransport(t, test.insecureHTTP)
			var outer *httptest.Server
			if test.insecureHTTP {
				outer = httptest.NewServer(h.server)
			} else {
				outer = httptest.NewTLSServer(h.server)
			}
			defer outer.Close()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := outer.Client()
			client.Jar = jar
			body := strings.NewReader(`{"username":"operator","password":"secret"}`)
			request, err := http.NewRequest(http.MethodPost, outer.URL+"/api/auth/login", body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", outer.URL)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var loginResponse struct {
				CSRFToken string `json:"csrfToken"`
			}
			if err := json.NewDecoder(response.Body).Decode(&loginResponse); err != nil {
				_ = response.Body.Close()
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("login status = %d", response.StatusCode)
			}
			var authenticationCookie *http.Cookie
			for _, candidate := range response.Cookies() {
				if candidate.Name == test.cookieName {
					authenticationCookie = candidate
				}
			}
			if authenticationCookie == nil || authenticationCookie.Secure != test.secure ||
				!authenticationCookie.HttpOnly || authenticationCookie.Path != "/" ||
				authenticationCookie.SameSite != http.SameSiteStrictMode ||
				authenticationCookie.Domain != "" || authenticationCookie.Value == "" {
				t.Fatalf("authentication cookie = %+v", authenticationCookie)
			}

			response, err = client.Get(outer.URL + "/api/auth/session")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("cookie-jar session status = %d", response.StatusCode)
			}

			wrongCookieName := insecureCookieName
			if test.insecureHTTP {
				wrongCookieName = secureCookieName
			}
			wrongRequest, err := http.NewRequest(http.MethodGet, outer.URL+"/api/auth/session", nil)
			if err != nil {
				t.Fatal(err)
			}
			wrongRequest.AddCookie(&http.Cookie{Name: wrongCookieName, Value: authenticationCookie.Value})
			wrongClient := &http.Client{Transport: client.Transport}
			wrongResponse, err := wrongClient.Do(wrongRequest)
			if err != nil {
				t.Fatal(err)
			}
			_ = wrongResponse.Body.Close()
			if wrongResponse.StatusCode != http.StatusUnauthorized {
				t.Fatalf("wrong-mode cookie status = %d", wrongResponse.StatusCode)
			}

			logoutRequest, err := http.NewRequest(http.MethodPost, outer.URL+"/api/auth/logout", nil)
			if err != nil {
				t.Fatal(err)
			}
			logoutRequest.Header.Set("Origin", outer.URL)
			logoutRequest.Header.Set(csrfHeader, loginResponse.CSRFToken)
			logoutResponse, err := client.Do(logoutRequest)
			if err != nil {
				t.Fatal(err)
			}
			_ = logoutResponse.Body.Close()
			if logoutResponse.StatusCode != http.StatusOK {
				t.Fatalf("logout status = %d", logoutResponse.StatusCode)
			}
			var cleared *http.Cookie
			for _, candidate := range logoutResponse.Cookies() {
				if candidate.Name == test.cookieName {
					cleared = candidate
				}
			}
			if cleared == nil || cleared.Secure != test.secure || !cleared.HttpOnly ||
				cleared.Path != "/" || cleared.SameSite != http.SameSiteStrictMode || cleared.MaxAge != -1 {
				t.Fatalf("cleared cookie = %+v", cleared)
			}
		})
	}
}

func TestValidOriginUsesDirectRequestTransport(t *testing.T) {
	for _, test := range []struct {
		name       string
		requestURL string
		origins    []string
		forwarded  string
		want       bool
	}{
		{name: "http same origin", requestURL: "http://machine.test:8080/api", origins: []string{"http://machine.test:8080"}, want: true},
		{name: "https same origin", requestURL: "https://machine.test:8443/api", origins: []string{"https://machine.test:8443"}, want: true},
		{name: "http rejects https origin", requestURL: "http://machine.test:8080/api", origins: []string{"https://machine.test:8080"}},
		{name: "https rejects http origin", requestURL: "https://machine.test:8443/api", origins: []string{"http://machine.test:8443"}},
		{name: "reject cross host", requestURL: "http://machine.test:8080/api", origins: []string{"http://attacker.test:8080"}},
		{name: "reject missing", requestURL: "http://machine.test:8080/api"},
		{name: "reject null", requestURL: "http://machine.test:8080/api", origins: []string{"null"}},
		{name: "reject path", requestURL: "http://machine.test:8080/api", origins: []string{"http://machine.test:8080/path"}},
		{name: "reject duplicate fields", requestURL: "http://machine.test:8080/api", origins: []string{"http://machine.test:8080", "http://machine.test:8080"}},
		{name: "ignore spoofed forwarded proto", requestURL: "http://machine.test:8080/api", origins: []string{"https://machine.test:8080"}, forwarded: "https"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.requestURL, nil)
			for _, origin := range test.origins {
				request.Header.Add("Origin", origin)
			}
			if test.forwarded != "" {
				request.Header.Set("X-Forwarded-Proto", test.forwarded)
			}
			if got := validOrigin(request); got != test.want {
				t.Fatalf("validOrigin() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestUnknownUsernameStillPerformsPAMForConfiguredAccount(t *testing.T) {
	h := newHarness(t)
	result := login(t, h.server, "somebody-else", "guess")
	if result.status != http.StatusUnauthorized {
		t.Fatalf("login status = %d, want 401", result.status)
	}
	h.auth.mu.Lock()
	defer h.auth.mu.Unlock()
	if len(h.auth.users) != 1 || h.auth.users[0] != "operator" || h.auth.passwords[0] != "guess" {
		t.Fatalf("unknown username probed wrong PAM account: %#v %#v", h.auth.users, h.auth.passwords)
	}
}

func TestAuthenticationFailuresAreIndistinguishable(t *testing.T) {
	unknown := newHarness(t)
	unknownResult := login(t, unknown.server, "unknown", "password")
	badPassword := newHarness(t)
	badPassword.auth.err = auth.ErrInvalidCredentials
	badPasswordResult := login(t, badPassword.server, "operator", "password")
	if unknownResult.status != badPasswordResult.status || string(unknownResult.body) != string(badPasswordResult.body) {
		t.Fatalf("failure responses differ:\nunknown: %d %s\nbad password: %d %s",
			unknownResult.status, unknownResult.body, badPasswordResult.status, badPasswordResult.body)
	}
}

func TestLoginRejectsCrossOriginAndBoundsAuthenticationConcurrency(t *testing.T) {
	h := newHarness(t)
	body := strings.NewReader(`{"username":"operator","password":"secret"}`)
	request := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/login", body)
	request.Host = "example.test"
	request.Header.Set("Origin", "http://evil.test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login status = %d", response.Code)
	}

	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	h.auth.block = block
	h.auth.entered = entered
	firstDone := make(chan loginResult, 1)
	go func() { firstDone <- login(t, h.server, "operator", "secret") }()
	<-entered
	second := login(t, h.server, "operator", "secret")
	if second.status != http.StatusTooManyRequests {
		t.Fatalf("concurrent login status = %d, want 429", second.status)
	}
	close(block)
	if first := <-firstDone; first.status != http.StatusOK {
		t.Fatalf("first login failed: %d", first.status)
	}
}

func TestJSONDecoderRejectsUnknownOversizedAndMultipleValues(t *testing.T) {
	h := newHarness(t)
	for name, body := range map[string]string{
		"unknown":  `{"username":"operator","password":"x","extra":true}`,
		"multiple": `{"username":"operator","password":"x"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/login", strings.NewReader(body))
			request.Host = "example.test"
			request.Header.Set("Origin", "http://example.test")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			h.server.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_json"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	oversized := `{"username":"operator","password":"` + strings.Repeat("x", maxJSONBody) + `"}`
	request := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/login", strings.NewReader(oversized))
	request.Host = "example.test"
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), `"code":"body_too_large"`) {
		t.Fatalf("oversized response = %d %s", response.Code, response.Body.String())
	}
}

func TestSessionAndLogoutRequireCSRFAndOrigin(t *testing.T) {
	h := newHarness(t)
	credentials := login(t, h.server, "operator", "secret")
	if credentials.status != http.StatusOK {
		t.Fatal("login failed")
	}

	body := strings.NewReader(`{"name":"Main"}`)
	request := authenticatedRequest(http.MethodPost, "/api/sessions", credentials.cookie, "wrong", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "csrf_rejected") {
		t.Fatalf("bad CSRF response = %d %s", response.Code, response.Body.String())
	}

	body = strings.NewReader(`{"name":"Main"}`)
	request = authenticatedRequest(http.MethodPost, "/api/sessions", credentials.cookie, credentials.csrf, body)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), testID) {
		t.Fatalf("create response = %d %s", response.Code, response.Body.String())
	}

	request = authenticatedRequest(http.MethodPost, "/api/auth/logout", credentials.cookie, credentials.csrf, nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("logout response = %d %s", response.Code, response.Body.String())
	}
	request = authenticatedRequest(http.MethodGet, "/api/auth/session", credentials.cookie, "", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out cookie remained valid: %d", response.Code)
	}
}

func TestSessionConnectDeleteAndErrors(t *testing.T) {
	h := newHarness(t)
	credentials := login(t, h.server, "operator", "secret")
	request := authenticatedRequest(http.MethodPost, "/api/sessions/"+testID+"/connect", credentials.cookie, credentials.csrf, nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || h.manager.connectedID != testID || !strings.Contains(response.Body.String(), "/terminal/"+testID+"/") {
		t.Fatalf("connect response = %d %s", response.Code, response.Body.String())
	}
	request = authenticatedRequest(http.MethodDelete, "/api/sessions/"+testID, credentials.cookie, credentials.csrf, nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || h.manager.deletedID != testID {
		t.Fatalf("delete response = %d %s", response.Code, response.Body.String())
	}

	h.manager.connectErr = sessions.ErrNotFound
	request = authenticatedRequest(http.MethodPost, "/api/sessions/"+testID+"/connect", credentials.cookie, credentials.csrf, nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"session_not_found"`) {
		t.Fatalf("not found response = %d %s", response.Code, response.Body.String())
	}
	h.manager.createErr = sessions.ErrLimitReached
	body := strings.NewReader(`{"name":"Other"}`)
	request = authenticatedRequest(http.MethodPost, "/api/sessions", credentials.cookie, credentials.csrf, body)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"session_limit"`) {
		t.Fatalf("limit response = %d %s", response.Code, response.Body.String())
	}
}

func TestDirectoryAndCodeServerAPIsRequireAuthenticationAndProtectMutations(t *testing.T) {
	h := newHarness(t)
	parentPath := "/srv"
	h.codeServers.listing = codeservers.DirectoryListing{
		Path:        "/srv/projects",
		ParentPath:  &parentPath,
		Directories: []codeservers.Directory{{Name: "mill ui", Path: "/srv/projects/mill ui"}},
	}

	request := httptest.NewRequest(http.MethodGet, "http://example.test/api/directories?path=%2Fsrv%2Fprojects", nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || h.codeServers.browsedPath != "" {
		t.Fatalf("unauthenticated directory response = %d, browsed %q", response.Code, h.codeServers.browsedPath)
	}

	credentials := login(t, h.server, "operator", "secret")
	request = authenticatedRequest(http.MethodGet, "/api/directories?path=%2Fsrv%2Fprojects", credentials.cookie, "", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || h.codeServers.browsedPath != "/srv/projects" ||
		!strings.Contains(response.Body.String(), `"parentPath":"/srv"`) ||
		!strings.Contains(response.Body.String(), `"name":"mill ui"`) {
		t.Fatalf("directory response = %d %s, browsed %q", response.Code, response.Body.String(), h.codeServers.browsedPath)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("directory Cache-Control = %q", response.Header().Get("Cache-Control"))
	}

	body := strings.NewReader(`{"folderPath":"/srv/projects/mill ui"}`)
	request = authenticatedRequest(http.MethodPost, "/api/code-servers", credentials.cookie, "wrong", body)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || h.codeServers.createdPath != "" {
		t.Fatalf("unverified launch response = %d, created %q", response.Code, h.codeServers.createdPath)
	}

	body = strings.NewReader(`{"folderPath":"/srv/projects/mill ui"}`)
	request = authenticatedRequest(http.MethodPost, "/api/code-servers", credentials.cookie, credentials.csrf, body)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || h.codeServers.createdPath != "/srv/projects/mill ui" ||
		!strings.Contains(response.Body.String(), `"url":"/code/`+testID+`/"`) ||
		!strings.Contains(response.Body.String(), `"reused":false`) {
		t.Fatalf("launch response = %d %s, created %q", response.Code, response.Body.String(), h.codeServers.createdPath)
	}

	h.codeServers.reused = true
	body = strings.NewReader(`{"folderPath":"/srv/projects/mill ui"}`)
	request = authenticatedRequest(http.MethodPost, "/api/code-servers", credentials.cookie, credentials.csrf, body)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"reused":true`) {
		t.Fatalf("reused launch response = %d %s", response.Code, response.Body.String())
	}

	request = authenticatedRequest(http.MethodGet, "/api/code-servers", credentials.cookie, "", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"codeServers"`) {
		t.Fatalf("list response = %d %s", response.Code, response.Body.String())
	}

	request = authenticatedRequest(http.MethodDelete, "/api/code-servers/"+testID, credentials.cookie, credentials.csrf, nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || h.codeServers.deletedID != testID {
		t.Fatalf("shutdown response = %d %s, deleted %q", response.Code, response.Body.String(), h.codeServers.deletedID)
	}
}

func TestCodeServerAPIErrorsUseStablePublicStatuses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid", codeservers.ErrInvalidPath, http.StatusBadRequest, "invalid_directory"},
		{"inaccessible", codeservers.ErrDirectoryInaccessible, http.StatusForbidden, "directory_inaccessible"},
		{"missing", codeservers.ErrDirectoryNotFound, http.StatusNotFound, "directory_not_found"},
		{"limit", codeservers.ErrLimitReached, http.StatusConflict, "code_server_limit"},
		{"startup", codeservers.ErrStartFailed, http.StatusBadGateway, "code_server_start_failed"},
		{"shutdown", codeservers.ErrShuttingDown, http.StatusServiceUnavailable, "shutting_down"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			credentials := login(t, h.server, "operator", "secret")
			h.codeServers.createErr = test.err
			request := authenticatedRequest(http.MethodPost, "/api/code-servers", credentials.cookie, credentials.csrf,
				strings.NewReader(`{"folderPath":"/project"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			h.server.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLatestSelectionRequiresAuthenticationAndReturnsNoStoreJSON(t *testing.T) {
	h := newHarness(t)
	h.manager.selectionText = "line one\nрядок два"

	request := httptest.NewRequest(http.MethodGet, "http://example.test/api/sessions/"+testID+"/clipboard", nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || h.manager.selectionID != "" {
		t.Fatalf("unauthenticated clipboard response = %d, manager id = %q", response.Code, h.manager.selectionID)
	}

	credentials := login(t, h.server, "operator", "secret")
	request = authenticatedRequest(http.MethodGet, "/api/sessions/"+testID+"/clipboard", credentials.cookie, "", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || h.manager.selectionID != testID ||
		!strings.Contains(response.Body.String(), `"text":"line one\nрядок два"`) {
		t.Fatalf("clipboard response = %d %s, manager id = %q", response.Code, response.Body.String(), h.manager.selectionID)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("clipboard Cache-Control = %q", response.Header().Get("Cache-Control"))
	}

	h.manager.selectionErr = sessions.ErrNoSelection
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"no_selection"`) {
		t.Fatalf("no-selection response = %d %s", response.Code, response.Body.String())
	}

	h.manager.selectionErr = sessions.ErrInvalidSelection
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"selection_invalid"`) {
		t.Fatalf("invalid-selection response = %d %s", response.Code, response.Body.String())
	}

	request = authenticatedRequest(http.MethodPost, "/api/sessions/"+testID+"/clipboard", credentials.cookie, "", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("clipboard POST response = %d Allow=%q", response.Code, response.Header().Get("Allow"))
	}
}

func TestTerminalProxyRequiresAuthenticationAndWebSocketOrigin(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodGet, "http://example.test/terminal/"+testID+"/", nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || h.manager.proxyCalled != 0 {
		t.Fatalf("unauthenticated proxy response = %d, calls %d", response.Code, h.manager.proxyCalled)
	}
	credentials := login(t, h.server, "operator", "secret")
	request = authenticatedRequest(http.MethodGet, "/terminal/"+testID+"/ws", credentials.cookie, "", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || h.manager.proxyCalled != 0 {
		t.Fatalf("origin-less WebSocket response = %d, calls %d", response.Code, h.manager.proxyCalled)
	}
	request = authenticatedRequest(http.MethodGet, "/terminal/"+testID+"/ws", credentials.cookie, "", nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Origin", "http://example.test")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || h.manager.proxyCalled != 1 || h.manager.proxiedRequest.URL.Path != "/terminal/"+testID+"/ws" {
		t.Fatalf("valid WebSocket response = %d, calls %d", response.Code, h.manager.proxyCalled)
	}
}

func TestCodeServerProxyRequiresAuthenticationAndUsesRouteSpecificPolicy(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodGet, "http://example.test/code/"+testID+"/", nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || h.codeServers.proxyCalled != 0 {
		t.Fatalf("unauthenticated proxy response = %d, calls %d", response.Code, h.codeServers.proxyCalled)
	}

	credentials := login(t, h.server, "operator", "secret")
	request = authenticatedRequest(http.MethodGet, "/code/"+testID+"/resource.js", credentials.cookie, "", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || h.codeServers.proxyCalled != 1 || h.codeServers.proxiedID != testID ||
		h.codeServers.proxiedRequest.URL.Path != "/code/"+testID+"/resource.js" {
		t.Fatalf("proxy response = %d, calls %d, id %q, request %+v", response.Code, h.codeServers.proxyCalled, h.codeServers.proxiedID, h.codeServers.proxiedRequest)
	}
	policies := response.Header().Values("Content-Security-Policy")
	if len(policies) != 2 || policies[0] != "frame-ancestors 'self'" || !strings.Contains(policies[1], "worker-src 'self' blob:") {
		t.Fatalf("code-server CSP values = %#v", policies)
	}
	for name, want := range map[string]string{
		"X-Frame-Options":        "SAMEORIGIN",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := response.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}

	request = authenticatedRequest(http.MethodPost, "/code/"+testID+"/api", credentials.cookie, "", nil)
	request.Header.Del("Origin")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || h.codeServers.proxyCalled != 1 {
		t.Fatalf("origin-less code-server mutation = %d, calls %d", response.Code, h.codeServers.proxyCalled)
	}
	request = authenticatedRequest(http.MethodPost, "/code/"+testID+"/api", credentials.cookie, "", nil)
	request.Header.Set("Origin", "http://attacker.example")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || h.codeServers.proxyCalled != 1 {
		t.Fatalf("cross-origin code-server mutation = %d, calls %d", response.Code, h.codeServers.proxyCalled)
	}
	request = authenticatedRequest(http.MethodPost, "/code/"+testID+"/api", credentials.cookie, "", nil)
	request.Header.Set("Origin", "http://example.test")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || h.codeServers.proxyCalled != 2 {
		t.Fatalf("same-origin code-server mutation = %d, calls %d", response.Code, h.codeServers.proxyCalled)
	}

	request = authenticatedRequest(http.MethodGet, "/code/"+testID+"/ws", credentials.cookie, "", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || h.codeServers.proxyCalled != 2 {
		t.Fatalf("origin-less WebSocket response = %d, calls %d", response.Code, h.codeServers.proxyCalled)
	}

	request = authenticatedRequest(http.MethodGet, "/code/"+testID+"/ws", credentials.cookie, "", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Origin", "http://example.test")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || h.codeServers.proxyCalled != 3 {
		t.Fatalf("same-origin WebSocket response = %d, calls %d", response.Code, h.codeServers.proxyCalled)
	}
}

type upgradedClient struct {
	connection net.Conn
	reader     *bufio.Reader
}

func holdingUpgradeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		connection, readWriter, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_, _ = readWriter.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = readWriter.Flush()
		go func() {
			_, _ = io.Copy(io.Discard, connection)
		}()
		// The HTTP-layer tracker owns the hijacked connection after return.
	})
}

func openTerminalWebSocket(t *testing.T, serverURL string, cookie *http.Cookie) upgradedClient {
	return openProxiedWebSocket(t, serverURL, cookie, "/terminal/"+testID+"/ws")
}

func openCodeServerWebSocket(t *testing.T, serverURL string, cookie *http.Cookie) upgradedClient {
	return openProxiedWebSocket(t, serverURL, cookie, "/code/"+testID+"/ws")
}

func openProxiedWebSocket(t *testing.T, serverURL string, cookie *http.Cookie, requestPath string) upgradedClient {
	t.Helper()
	address := strings.TrimPrefix(serverURL, "http://")
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: %s=%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n",
		requestPath, address, serverURL, cookieName, cookie.Value,
	)
	if _, err := io.WriteString(connection, request); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = connection.Close()
		t.Fatalf("proxy upgrade status = %d", response.StatusCode)
	}
	return upgradedClient{connection: connection, reader: reader}
}

func expectConnectionClosed(t *testing.T, client upgradedClient, within time.Duration) {
	t.Helper()
	defer client.connection.Close()
	if err := client.connection.SetReadDeadline(time.Now().Add(within)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.reader.ReadByte(); err == nil {
		t.Fatal("terminal connection remained readable after revocation")
	} else if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
		t.Fatal("terminal connection was not closed before the revocation deadline")
	}
}

func TestLogoutTerminatesAlreadyUpgradedTerminalConnection(t *testing.T) {
	h := newHarness(t)
	h.manager.proxyHandler = holdingUpgradeHandler()
	credentials := login(t, h.server, "operator", "secret")
	service := httptest.NewServer(h.server)
	defer service.Close()
	client := openTerminalWebSocket(t, service.URL, credentials.cookie)

	request, err := http.NewRequest(http.MethodPost, service.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(credentials.cookie)
	request.Header.Set("Origin", service.URL)
	request.Header.Set(csrfHeader, credentials.csrf)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
	expectConnectionClosed(t, client, 2*time.Second)
}

func TestLogoutTerminatesAlreadyUpgradedCodeServerConnection(t *testing.T) {
	h := newHarness(t)
	h.codeServers.proxyHandler = holdingUpgradeHandler()
	credentials := login(t, h.server, "operator", "secret")
	service := httptest.NewServer(h.server)
	defer service.Close()
	client := openCodeServerWebSocket(t, service.URL, credentials.cookie)

	request, err := http.NewRequest(http.MethodPost, service.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(credentials.cookie)
	request.Header.Set("Origin", service.URL)
	request.Header.Set(csrfHeader, credentials.csrf)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
	expectConnectionClosed(t, client, 2*time.Second)
}

func TestAuthenticationIdleExpiryTerminatesUpgradedTerminalConnection(t *testing.T) {
	h := newHarnessWithTimeouts(t, 250*time.Millisecond, 5*time.Second)
	h.manager.proxyHandler = holdingUpgradeHandler()
	credentials := login(t, h.server, "operator", "secret")
	service := httptest.NewServer(h.server)
	defer service.Close()
	client := openTerminalWebSocket(t, service.URL, credentials.cookie)
	// ttyd's WebSocket carries protocol traffic and periodic ping/pong frames.
	// Raw bytes alone must not refresh the authentication idle deadline.
	for index := 0; index < 8; index++ {
		if _, err := client.connection.Write([]byte{byte(index + 1)}); err != nil {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	expectConnectionClosed(t, client, 3*time.Second)
}

func TestAuthenticationAbsoluteExpiryTerminatesUpgradedTerminalConnection(t *testing.T) {
	h := newHarnessWithTimeouts(t, 300*time.Millisecond, 750*time.Millisecond)
	h.manager.proxyHandler = holdingUpgradeHandler()
	credentials := login(t, h.server, "operator", "secret")
	service := httptest.NewServer(h.server)
	defer service.Close()
	client := openTerminalWebSocket(t, service.URL, credentials.cookie)
	// Authenticated HTTP activity keeps extending the idle deadline. The
	// immutable absolute deadline must still revoke the upgraded connection.
	for index := 0; index < 3; index++ {
		time.Sleep(180 * time.Millisecond)
		request, err := http.NewRequest(http.MethodGet, service.URL+"/api/auth/session", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(credentials.cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("session refresh %d status = %d", index, response.StatusCode)
		}
	}
	expectConnectionClosed(t, client, 3*time.Second)
}

func TestBeginShutdownRejectsAllNewApplicationRequests(t *testing.T) {
	h := newHarness(t)
	h.manager.proxyHandler = holdingUpgradeHandler()
	credentials := login(t, h.server, "operator", "secret")
	h.server.BeginShutdown()
	request := authenticatedRequest(http.MethodGet, "/terminal/"+testID+"/ws", credentials.cookie, "", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Origin", "http://example.test")
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || h.manager.proxyCalled != 0 {
		t.Fatalf("shutdown terminal request = %d, proxy calls = %d", response.Code, h.manager.proxyCalled)
	}
	request = authenticatedRequest(http.MethodGet, "/code/"+testID+"/ws", credentials.cookie, "", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Origin", "http://example.test")
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || h.codeServers.proxyCalled != 0 {
		t.Fatalf("shutdown code-server request = %d, proxy calls = %d", response.Code, h.codeServers.proxyCalled)
	}
	for _, target := range []string{"/api/config", "/api/directories", "/client/route"} {
		request = httptest.NewRequest(http.MethodGet, "http://example.test"+target, nil)
		response = httptest.NewRecorder()
		h.server.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"shutting_down"`) {
			t.Fatalf("shutdown request %s = %d %q", target, response.Code, response.Body.String())
		}
	}
	if got := response.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("shutdown response omitted application security headers")
	}
}

func TestConnectionTrackerShutdownRaceClosesFutureHijacks(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		tracker := newConnectionTracker()
		serverConnection, clientConnection := net.Pipe()
		tracked := make(chan net.Conn, 1)
		start := make(chan struct{})
		go func() {
			<-start
			tracked <- tracker.Track("token", serverConnection, time.Now().Add(time.Hour), nil)
		}()
		go func() {
			<-start
			tracker.CloseAll()
		}()
		close(start)
		connection := <-tracked
		// If Track won the race, CloseAll closes it; if shutdown won, Track sees
		// the permanently sealed tracker and closes it immediately.
		_ = clientConnection.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := clientConnection.Write([]byte("probe")); err == nil {
			_ = connection.Close()
			_ = clientConnection.Close()
			t.Fatalf("iteration %d: connection survived tracker shutdown", iteration)
		}
		_ = connection.Close()
		_ = clientConnection.Close()
	}
}

func TestHealthStaticFallbackAndSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodGet, "http://example.test/healthz", nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "connect-src 'self'") || strings.Contains(got, "wss:") {
		t.Fatalf("unexpected CSP: %q", got)
	}
	request = httptest.NewRequest(http.MethodGet, "http://example.test/client/route", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "remote app") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SPA fallback = %d %q", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "http://example.test/asset.js", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "asset" {
		t.Fatalf("static asset = %d %q", response.Code, response.Body.String())
	}

	h.server.BeginShutdown()
	request = httptest.NewRequest(http.MethodGet, "http://example.test/healthz", nil)
	response = httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"ok\":false}\n" {
		t.Fatalf("shutdown health response = %d %q", response.Code, response.Body.String())
	}
}

func TestInternalDependencyDetailsAreNotReturned(t *testing.T) {
	h := newHarness(t)
	credentials := login(t, h.server, "operator", "secret")
	h.manager.connectErr = errors.New("exec /secret/path/ttyd: permission denied")
	request := authenticatedRequest(http.MethodPost, "/api/sessions/"+testID+"/connect", credentials.cookie, credentials.csrf, nil)
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "/secret/path") || !strings.Contains(response.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("internal error leaked detail: %d %s", response.Code, response.Body.String())
	}
}
