// Package httpapi serves the setup-domain API and embedded React application.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const appCSP = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'self'; form-action 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; worker-src 'self' blob:; frame-src 'self'"

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
	RemoteAccess              bool
	RemoteAuthToken           string
}

// Server is the root HTTP handler.
type Server struct {
	config       Config
	database     Checker
	storage      Checker
	static       fs.FS
	fileServer   http.Handler
	logger       *slog.Logger
	csrfToken    string
	allowedHosts map[string]struct{}
	shuttingDown atomic.Bool
}

// New constructs a secure same-origin handler.
func New(configuration Config, database, storage Checker, static fs.FS, logger *slog.Logger) (*Server, error) {
	if database == nil || storage == nil || static == nil {
		return nil, errors.New("HTTP dependencies are incomplete")
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
		csrfToken: token, allowedHosts: hosts,
	}
	return server, nil
}

// BeginShutdown rejects new mutations and makes health endpoints fail.
func (s *Server) BeginShutdown() { s.shuttingDown.Store(true) }

// ServeHTTP applies security, request identity, recovery and safe structured
// access logging around the route dispatcher.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID, err := randomToken(12)
	if err != nil {
		requestID = "request-id-unavailable"
	}
	w.Header().Set("X-Request-ID", requestID)
	setSecurityHeaders(w.Header())
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("http panic recovered", "request_id", requestID, "result", "INTERNAL_ERROR")
			if !recorder.wroteHeader {
				writeError(recorder, http.StatusInternalServerError, requestID, "INTERNAL_ERROR", "The request could not be completed.", nil, false)
			}
		}
		s.logger.Info("http request",
			"request_id", requestID,
			"method", safeMethod(r.Method),
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"bytes", recorder.bytes,
		)
	}()

	if s.shuttingDown.Load() && isMutation(r.Method) {
		writeError(recorder, http.StatusServiceUnavailable, requestID, "SERVICE_SHUTTING_DOWN", "The service is shutting down.", nil, true)
		return
	}
	if s.config.RemoteAccess && strings.HasPrefix(r.URL.Path, "/api/v1/") && !s.authorizeRemote(r) {
		recorder.Header().Set("WWW-Authenticate", `Bearer realm="web-setup-manager"`)
		writeError(recorder, http.StatusUnauthorized, requestID, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil, false)
		return
	}

	s.route(recorder, r, requestID)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, requestID string) {
	switch r.URL.Path {
	case "/healthz":
		s.health(w, r, requestID)
	case "/readyz":
		s.ready(w, r, requestID)
	case "/api/v1/capabilities":
		s.capabilities(w, r, requestID)
	default:
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
	writeJSONForRequest(w, r, http.StatusOK, map[string]any{
		"libraryId":                 s.config.LibraryID,
		"libraryAlias":              s.config.LibraryAlias,
		"gcodeExtensions":           s.config.GCodeExtensions,
		"requireSetupSheetForReady": s.config.RequireSetupSheetForReady,
		"csrfToken":                 s.csrfToken,
		"features": map[string]bool{
			"setupLibrary": true,
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
	if _, allowed := s.allowedHosts[strings.ToLower(r.Host)]; !allowed {
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
			return false
		}
		if _, allowed := s.allowedHosts[strings.ToLower(parsed.Host)]; !allowed {
			return false
		}
	}
	supplied := r.Header.Get("X-CSRF-Token")
	return constantTimeEqual(s.csrfToken, supplied)
}

func (s *Server) authorizeRemote(r *http.Request) bool {
	prefix := "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return constantTimeEqual(s.config.RemoteAuthToken, strings.TrimPrefix(header, prefix))
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
	host = strings.Trim(host, "[]")
	hosts := map[string]struct{}{strings.ToLower(net.JoinHostPort(host, port)): {}}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		for _, loopback := range []string{"localhost", "127.0.0.1", "::1"} {
			hosts[strings.ToLower(net.JoinHostPort(loopback, port))] = struct{}{}
		}
	}
	return hosts, nil
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
	payload := map[string]any{
		"code": code, "message": message, "requestId": requestID, "retryable": retryable,
	}
	if details != nil {
		payload["details"] = details
	}
	writeJSON(w, status, map[string]any{"error": payload})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(buffer []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	count, err := r.ResponseWriter.Write(buffer)
	r.bytes += int64(count)
	return count, err
}
