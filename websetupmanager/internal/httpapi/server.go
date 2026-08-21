// Package httpapi serves the setup-domain API and embedded React application.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/auth"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
	"golang.org/x/net/http/httpguts"
)

const appCSP = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'self'; form-action 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; worker-src 'self' blob:; frame-src 'self' blob:"

const remoteSessionCookieName = "__Host-websetupmanager_session"

// Checker is implemented by the database and managed-root health checks.
type Checker interface {
	Check(context.Context) error
}

// CheckFunc adapts a function to Checker.
type CheckFunc func(context.Context) error

// Check calls the wrapped readiness function.
func (f CheckFunc) Check(ctx context.Context) error { return f(ctx) }

// Config contains only public-safe runtime capability values and security
// policy. It deliberately contains no physical paths or storage keys.
type Config struct {
	ListenAddress             string
	LibraryID                 string
	LibraryAlias              string
	GCodeExtensions           []string
	RequireSetupSheetForReady bool
	RequestReadIdleTimeout    time.Duration
	ResponseWriteIdleTimeout  time.Duration
	RemoteAccess              bool
	AllowedUser               string
	AuthRememberTimeout       time.Duration
	AuthConcurrency           int
	RemoteAuthToken           string
	// EnableLegacyAPI keeps the pre-catalog setup workflow available only for
	// compatibility tests and controlled migrations. Production leaves it false.
	EnableLegacyAPI bool
}

// AuthDependencies contains the remote-browser authentication dependencies.
// It is deliberately separate from Config so tests and local-only callers do
// not need to manufacture authentication state.
type AuthDependencies struct {
	Authenticator auth.Authenticator
	Sessions      *auth.Store
	Throttler     *auth.Throttler
}

// Server is the root HTTP handler.
type Server struct {
	config        Config
	database      Checker
	storage       Checker
	static        fs.FS
	fileServer    http.Handler
	logger        *slog.Logger
	csrfToken     string
	allowedHosts  map[string]struct{}
	shuttingDown  atomic.Bool
	service       *service.Service
	authenticator auth.Authenticator
	authSessions  *auth.Store
	throttler     *auth.Throttler
	authSlots     chan struct{}
	requestsMu    sync.Mutex
	requests      map[uint64]trackedRequest
	nextRequest   atomic.Uint64
}

type trackedRequest struct {
	cancel context.CancelFunc
	body   io.ReadCloser
}

// New constructs a secure same-origin handler.
func New(configuration Config, database, storage Checker, static fs.FS, logger *slog.Logger) (*Server, error) {
	return newServer(configuration, database, storage, static, AuthDependencies{}, logger)
}

// NewAuthenticated constructs a handler with remote browser authentication.
// Remote mode fails closed unless all authentication dependencies are present.
func NewAuthenticated(configuration Config, database, storage Checker, static fs.FS, authentication AuthDependencies, logger *slog.Logger) (*Server, error) {
	return newServer(configuration, database, storage, static, authentication, logger)
}

func newServer(configuration Config, database, storage Checker, static fs.FS, authentication AuthDependencies, logger *slog.Logger) (*Server, error) {
	if database == nil || storage == nil || static == nil {
		return nil, errors.New("HTTP dependencies are incomplete")
	}
	if configuration.RemoteAccess && (configuration.AllowedUser == "" || configuration.AuthRememberTimeout <= 0 ||
		configuration.AuthConcurrency <= 0 || authentication.Authenticator == nil ||
		authentication.Sessions == nil || authentication.Throttler == nil) {
		return nil, errors.New("remote authentication dependencies are incomplete")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	token, err := randomToken(32)
	if err != nil {
		return nil, errors.New("generate request security token")
	}
	hosts, err := allowedHosts(configuration.ListenAddress)
	if err != nil {
		return nil, errors.New("invalid HTTP listen address")
	}
	server := &Server{
		config: configuration, database: database, storage: storage, static: static,
		fileServer: http.FileServer(http.FS(static)), logger: logger,
		csrfToken: token, allowedHosts: hosts, requests: make(map[uint64]trackedRequest),
		authenticator: authentication.Authenticator, authSessions: authentication.Sessions,
		throttler: authentication.Throttler,
	}
	if configuration.RemoteAccess {
		server.authSlots = make(chan struct{}, configuration.AuthConcurrency)
	}
	return server, nil
}

// NewWithService is New with the setup-domain implementation enabled.
func NewWithService(configuration Config, database, storage Checker, application *service.Service, static fs.FS, logger *slog.Logger) (*Server, error) {
	return newWithService(configuration, database, storage, application, static, AuthDependencies{}, logger)
}

// NewWithServiceAuthenticated is NewWithService with remote browser
// authentication enabled.
func NewWithServiceAuthenticated(configuration Config, database, storage Checker, application *service.Service, static fs.FS, authentication AuthDependencies, logger *slog.Logger) (*Server, error) {
	return newWithService(configuration, database, storage, application, static, authentication, logger)
}

func newWithService(configuration Config, database, storage Checker, application *service.Service, static fs.FS, authentication AuthDependencies, logger *slog.Logger) (*Server, error) {
	if application == nil {
		return nil, errors.New("setup service is unavailable")
	}
	server, err := newServer(configuration, database, storage, static, authentication, logger)
	if err != nil {
		return nil, err
	}
	server.service = application
	return server, nil
}

// BeginShutdown rejects new mutations, makes health endpoints fail and
// cooperatively cancels active mutations/content streams so their staging and
// journal cleanup can finish before process shutdown.
func (s *Server) BeginShutdown() {
	if s.shuttingDown.Swap(true) {
		return
	}
	s.requestsMu.Lock()
	requests := make([]trackedRequest, 0, len(s.requests))
	for _, request := range s.requests {
		requests = append(requests, request)
	}
	s.requestsMu.Unlock()
	for _, request := range requests {
		request.cancel()
		// Request.Body.Close is required by net/http to unblock a concurrent
		// Read. Context cancellation alone cannot release a handler waiting for
		// the next upload byte before the configured read-idle deadline.
		if request.body != nil {
			_ = request.body.Close()
		}
	}
}

// ServeHTTP applies security, request identity, recovery and safe structured
// access logging around the route dispatcher.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID, err := randomToken(12)
	if err != nil {
		requestID = "request-id-unavailable"
	}
	w.Header().Set("X-Request-ID", requestID)
	setSecurityHeaders(w.Header())
	recorder := &statusRecorder{
		ResponseWriter: w, status: http.StatusOK,
		writeIdleTimeout: s.config.ResponseWriteIdleTimeout,
	}
	var requestBody *idleDeadlineReadCloser
	if r.Body != nil {
		controller := http.NewResponseController(recorder)
		requestBody = &idleDeadlineReadCloser{
			ReadCloser: r.Body,
			controller: controller,
			timeout:    s.config.RequestReadIdleTimeout,
		}
		r.Body = requestBody
		if s.config.RequestReadIdleTimeout > 0 {
			// Set an initial deadline even for routes that reject a body without
			// reading it. Leaving it in place until net/http completes its bounded
			// post-handler drain prevents a slow ignored body from pinning a
			// connection after ServeHTTP returns; subsequent request parsing resets
			// the connection deadline through ReadHeaderTimeout.
			_ = controller.SetReadDeadline(time.Now().Add(s.config.RequestReadIdleTimeout))
		}
	}
	if s.config.ResponseWriteIdleTimeout > 0 {
		defer func() { _ = http.NewResponseController(recorder).SetWriteDeadline(time.Time{}) }()
	}
	started := time.Now()
	routeName, setupID, artifactID, importID, jobID := safeRouteContext(r.URL.Path)
	if isCancellableRequest(r) {
		var done func()
		r, done = s.trackRequest(r)
		defer done()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("http panic recovered", "request_id", requestID, "result", "INTERNAL_ERROR")
			recorder.setErrorCode("INTERNAL_ERROR")
			if !recorder.wroteHeader {
				writeError(recorder, http.StatusInternalServerError, requestID, "INTERNAL_ERROR", "The request could not be completed.", nil, false)
			}
		}
		requestBytes := int64(0)
		if requestBody != nil {
			requestBytes = requestBody.bytes
		}
		s.logger.Info("http request",
			"request_id", requestID,
			"method", safeMethod(r.Method),
			"route", routeName,
			"operation", routeName,
			"setup_id", setupID,
			"artifact_id", artifactID,
			"import_id", importID,
			"job_id", jobID,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"bytes", requestBytes+recorder.bytes,
			"request_bytes", requestBytes,
			"response_bytes", recorder.bytes,
			"result", recorder.result(),
			"error_code", recorder.errorCode,
		)
	}()

	if s.shuttingDown.Load() && isMutation(r.Method) {
		writeError(recorder, http.StatusServiceUnavailable, requestID, "SERVICE_SHUTTING_DOWN", "The service is shutting down.", nil, true)
		return
	}
	if s.config.RemoteAccess && !isPublicRemoteRoute(r.URL.Path) {
		principal, ok := s.authenticateRemote(recorder, r, requestID)
		if !ok {
			return
		}
		r = r.WithContext(withRequestPrincipal(r.Context(), principal))
	}

	s.route(recorder, r, requestID)
}

func isCancellableRequest(r *http.Request) bool {
	return isMutation(r.Method) || strings.HasSuffix(r.URL.Path, "/content")
}

func (s *Server) trackRequest(r *http.Request) (*http.Request, func()) {
	ctx, cancel := context.WithCancel(r.Context())
	identifier := s.nextRequest.Add(1)
	s.requestsMu.Lock()
	if s.shuttingDown.Load() {
		cancel()
		if r.Body != nil {
			_ = r.Body.Close()
		}
	} else {
		s.requests[identifier] = trackedRequest{cancel: cancel, body: r.Body}
	}
	s.requestsMu.Unlock()
	var once sync.Once
	done := func() {
		once.Do(func() {
			s.requestsMu.Lock()
			delete(s.requests, identifier)
			s.requestsMu.Unlock()
			cancel()
		})
	}
	return r.WithContext(ctx), done
}

func safeRouteContext(requestPath string) (route, setupID, artifactID, importID, jobID string) {
	segments := strings.Split(strings.Trim(requestPath, "/"), "/")
	switch requestPath {
	case "/healthz":
		return "healthz", "", "", "", ""
	case "/readyz":
		return "readyz", "", "", "", ""
	case "/api/v1/capabilities":
		return "capabilities", "", "", "", ""
	case "/api/v1/auth/login":
		return "auth-login", "", "", "", ""
	case "/api/v1/auth/session":
		return "auth-session", "", "", "", ""
	case "/api/v1/auth/logout":
		return "auth-logout", "", "", "", ""
	case "/api/v1/catalog":
		return "catalog", "", "", "", ""
	case "/api/v1/catalog/folders":
		return "catalog-folders", "", "", "", ""
	case "/api/v1/catalog/setups":
		return "catalog-setups", "", "", "", ""
	case "/api/v1/setups":
		return "setups", "", "", "", ""
	case "/api/v1/setups/name-check":
		return "setup-name-check", "", "", "", ""
	case "/api/v1/setup-imports":
		return "setup-imports", "", "", "", ""
	case "/api/v1/setup-imports/preflight":
		return "setup-import-preflight", "", "", "", ""
	case "/api/v1/current-setup":
		return "current-setup", "", "", "", ""
	case "/api/v1/recent-setups":
		return "recent-setups", "", "", "", ""
	case "/api/v1/ui-state":
		return "ui-state", "", "", "", ""
	case "/api/v1/jobs":
		return "jobs", "", "", "", ""
	}
	if len(segments) >= 5 && segments[0] == "api" && segments[1] == "v1" &&
		segments[2] == "catalog" && segments[3] == "folders" && safeEntityID(segments[4]) {
		return "catalog-folder", "", "", "", ""
	}
	if len(segments) >= 5 && segments[0] == "api" && segments[1] == "v1" &&
		segments[2] == "catalog" && segments[3] == "setups" && safeEntityID(segments[4]) {
		setupID = segments[4]
		route = "catalog-setup"
		if len(segments) >= 6 {
			switch segments[5] {
			case "program", "setup-sheet":
				route = "catalog-setup-" + segments[5]
				if len(segments) == 7 && segments[6] == "content" {
					route += "-content"
				}
			}
		}
		return route, setupID, "", "", ""
	}
	if len(segments) >= 4 && segments[0] == "api" && segments[1] == "v1" &&
		segments[2] == "setup-imports" && safeEntityID(segments[3]) {
		importID = segments[3]
		route = "setup-import"
		if len(segments) >= 5 {
			switch segments[4] {
			case "artifacts":
				route = "setup-import-artifacts"
				if len(segments) >= 6 && safeEntityID(segments[5]) {
					artifactID = segments[5]
				}
			case "commit":
				route = "setup-import-commit"
			}
		}
		return route, "", artifactID, importID, ""
	}
	if len(segments) >= 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "setups" && safeEntityID(segments[3]) {
		setupID = segments[3]
		route = "setup"
		if len(segments) >= 5 {
			switch segments[4] {
			case "programs":
				route = "setup-programs"
				if len(segments) >= 6 && safeEntityID(segments[5]) {
					artifactID = segments[5]
				}
			case "setup-sheet", "upload-jobs", "validate", "duplicate", "archive", "restore", "delete-plan", "audit", "validations":
				route = "setup-" + segments[4]
			}
		}
		return route, setupID, artifactID, "", ""
	}
	if len(segments) >= 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "jobs" && safeEntityID(segments[3]) {
		if len(segments) == 4 {
			return "job", "", "", "", segments[3]
		}
		if len(segments) == 5 && segments[4] == "upload" {
			return "job-upload", "", "", "", segments[3]
		}
	}
	if strings.HasPrefix(requestPath, "/api/") {
		return "unknown-api", "", "", "", ""
	}
	return "frontend", "", "", "", ""
}

func safeEntityID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, requestID string) {
	switch r.URL.Path {
	case "/healthz":
		s.health(w, r, requestID)
	case "/readyz":
		s.ready(w, r, requestID)
	case "/api/v1/capabilities":
		s.capabilities(w, r, requestID)
	case "/api/v1/auth/login":
		s.login(w, r, requestID)
	case "/api/v1/auth/session":
		s.authenticationSession(w, r, requestID)
	case "/api/v1/auth/logout":
		s.logout(w, r, requestID)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v1/catalog") && s.routeCatalog(w, r, requestID) {
			return
		}
		if s.config.EnableLegacyAPI && strings.HasPrefix(r.URL.Path, "/api/v1/setups/") && s.routeContent(w, r, requestID) {
			return
		}
		if s.config.EnableLegacyAPI && s.service != nil && strings.HasPrefix(r.URL.Path, "/api/v1/") && s.routeDomain(w, r, requestID) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/fs" || strings.HasPrefix(r.URL.Path, "/fs/") {
			writeError(w, http.StatusNotFound, requestID, "NOT_FOUND", "The requested resource was not found.", nil, false)
			return
		}
		s.serveStatic(w, r, requestID)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request, requestID string) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.shuttingDown.Load() {
		writeJSONForRequest(w, r, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	writeJSONForRequest(w, r, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request, requestID string) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.shuttingDown.Load() {
		writeError(w, http.StatusServiceUnavailable, requestID, "SERVICE_SHUTTING_DOWN", "The service is shutting down.", nil, true)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.database.Check(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, requestID, "DATABASE_UNAVAILABLE", "The local database is unavailable.", nil, true)
		return
	}
	if err := s.storage.Check(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, requestID, "STORAGE_UNAVAILABLE", "Managed storage is unavailable.", nil, true)
		return
	}
	writeJSONForRequest(w, r, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request, requestID string) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	csrfToken := s.csrfToken
	if principal, ok := requestPrincipalFrom(r.Context()); ok && principal.kind == principalSession {
		csrfToken = principal.session.CSRFToken
	}
	writeJSONForRequest(w, r, http.StatusOK, map[string]any{
		"libraryId":                 s.config.LibraryID,
		"libraryAlias":              s.config.LibraryAlias,
		"gcodeExtensions":           s.config.GCodeExtensions,
		"requireSetupSheetForReady": false,
		"csrfToken":                 csrfToken,
		"features": map[string]bool{
			"setupCatalog": true,
			"setupLibrary": false,
			"validation":   false,
			"fileBrowser":  false,
			"linuxcncRun":  false,
			"offline":      true,
		},
	})
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, requestID string) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	requested := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if requested == "." || requested == "" {
		requested = "index.html"
	}
	if strings.HasPrefix(requested, ".") || strings.Contains(requested, "/.") {
		writeError(w, http.StatusNotFound, requestID, "NOT_FOUND", "The requested resource was not found.", nil, false)
		return
	}
	if info, err := fs.Stat(s.static, requested); err == nil && !info.IsDir() {
		if strings.HasPrefix(requested, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		if contentType := mime.TypeByExtension(path.Ext(requested)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		s.fileServer.ServeHTTP(w, r)
		return
	}
	if path.Ext(requested) != "" {
		writeError(w, http.StatusNotFound, requestID, "NOT_FOUND", "The requested resource was not found.", nil, false)
		return
	}
	index, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, requestID, "FRONTEND_UNAVAILABLE", "The application frontend is unavailable.", nil, true)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(index)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(index)
	}
}

// authorizeMutation enforces the same-origin and CSRF policy used by every
// domain mutation handler.
func (s *Server) authorizeMutation(r *http.Request) bool {
	if !isMutation(r.Method) || s.shuttingDown.Load() {
		return false
	}
	if principal, ok := requestPrincipalFrom(r.Context()); ok && principal.kind == principalSession {
		return s.authorizeSessionMutation(r, principal.session)
	}
	requestHost := strings.ToLower(r.Host)
	if s.config.RemoteAccess {
		// A wildcard remote listen address cannot enumerate the operator-facing
		// DNS names. Validate the Host syntax and bind Origin to exactly that
		// authority; authentication and CSRF are enforced independently.
		if !httpguts.ValidHostHeader(r.Host) {
			return false
		}
	} else {
		if _, allowed := s.allowedHosts[requestHost]; !allowed {
			return false
		}
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
			return false
		}
		originHost := strings.ToLower(parsed.Host)
		if s.config.RemoteAccess {
			if originHost != requestHost {
				return false
			}
		} else {
			if _, allowed := s.allowedHosts[originHost]; !allowed {
				return false
			}
		}
	}
	supplied := r.Header.Get("X-CSRF-Token")
	return constantTimeEqual(s.csrfToken, supplied)
}

func constantTimeEqual(expected, supplied string) bool {
	if len(expected) == 0 || len(expected) != len(supplied) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func allowedHosts(listenAddress string) (map[string]struct{}, error) {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return nil, err
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, errors.New("invalid listen port")
	}
	port = strconv.FormatUint(portNumber, 10)
	host = strings.Trim(host, "[]")
	hosts := make(map[string]struct{})
	addAllowedAuthority(hosts, host, port)
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		for _, loopback := range []string{"localhost", "127.0.0.1", "::1"} {
			addAllowedAuthority(hosts, loopback, port)
		}
	}
	return hosts, nil
}

func addAllowedAuthority(hosts map[string]struct{}, host, port string) {
	hosts[strings.ToLower(net.JoinHostPort(host, port))] = struct{}{}
	if port != "80" && port != "443" {
		return
	}
	if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	hosts[strings.ToLower(host)] = struct{}{}
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", appCSP)
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "SAMEORIGIN")
}

func allowMethods(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	return false
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func safeMethod(method string) string {
	if len(method) > 16 {
		return "INVALID"
	}
	for _, character := range method {
		if character < 'A' || character > 'Z' {
			return "INVALID"
		}
	}
	return method
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONForRequest(w http.ResponseWriter, r *http.Request, status int, value any) {
	if r.Method != http.MethodHead {
		writeJSON(w, status, value)
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)+1))
	w.WriteHeader(status)
}

func writeError(w http.ResponseWriter, status int, requestID, code, message string, details any, retryable bool) {
	if recorder, ok := w.(interface{ setErrorCode(string) }); ok {
		recorder.setErrorCode(code)
	}
	payload := map[string]any{
		"code": code, "message": message, "request_id": requestID, "retryable": retryable,
	}
	if details != nil {
		payload["details"] = details
	}
	writeJSON(w, status, map[string]any{"error": payload})
}

type statusRecorder struct {
	http.ResponseWriter
	status           int
	bytes            int64
	wroteHeader      bool
	errorCode        string
	writeIdleTimeout time.Duration
}

// Unwrap lets http.ResponseController reach the server response writer. This
// is used to enforce an idle timeout for streaming request bodies without a
// fixed wall-clock deadline that would reject legitimately large uploads.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type idleDeadlineReadCloser struct {
	io.ReadCloser
	controller *http.ResponseController
	timeout    time.Duration
	bytes      int64
}

func (r *idleDeadlineReadCloser) Read(buffer []byte) (int, error) {
	if r.timeout > 0 {
		if err := r.controller.SetReadDeadline(time.Now().Add(r.timeout)); err != nil {
			return 0, err
		}
	}
	count, err := r.ReadCloser.Read(buffer)
	r.bytes += int64(count)
	return count, err
}

func (r *statusRecorder) setErrorCode(code string) {
	// Error codes are constants controlled by this process, never arbitrary
	// request data. Keep a defensive bound so logging cannot be amplified by a
	// future caller.
	if len(code) > 64 {
		code = "INTERNAL_ERROR"
	}
	r.errorCode = code
}

func (r *statusRecorder) result() string {
	if r.status >= http.StatusBadRequest || r.errorCode != "" {
		return "failed"
	}
	return "succeeded"
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	if r.writeIdleTimeout > 0 {
		_ = http.NewResponseController(r).SetWriteDeadline(time.Now().Add(r.writeIdleTimeout))
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(buffer []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.writeIdleTimeout > 0 {
		if err := http.NewResponseController(r).SetWriteDeadline(time.Now().Add(r.writeIdleTimeout)); err != nil {
			return 0, err
		}
	}
	count, err := r.ResponseWriter.Write(buffer)
	r.bytes += int64(count)
	if err != nil || count != len(buffer) {
		r.setErrorCode("RESPONSE_WRITE_FAILED")
	}
	return count, err
}
