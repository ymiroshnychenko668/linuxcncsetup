// Package httpapi exposes authentication, tmux session management, the
// authenticated ttyd proxy, health, and the production React bundle.
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/auth"
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/codeservers"
	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/sessions"
)

const (
	secureCookieName   = "__Host-remoteterminal_session"
	insecureCookieName = "remoteterminal_session"
	// cookieName remains the secure default used by existing HTTPS-oriented
	// tests and by callers which omit the explicit insecure-HTTP opt-in.
	cookieName  = secureCookieName
	csrfHeader  = "X-CSRF-Token"
	maxJSONBody = 8 << 10
)

type SessionManager interface {
	List(context.Context) ([]sessions.Session, error)
	Create(context.Context, string) (sessions.Session, error)
	Delete(context.Context, string) error
	Connect(context.Context, string) (sessions.Session, string, error)
	TakeLatestSelection(context.Context, string) (string, error)
	DiscardSelections(context.Context, string) error
	Proxy(context.Context, string) (http.Handler, error)
	ActiveTerminals() int
}

type CodeServerManager interface {
	Browse(context.Context, string) (codeservers.DirectoryListing, error)
	List(context.Context) ([]codeservers.Instance, error)
	Create(context.Context, string) (codeservers.Instance, bool, error)
	Delete(context.Context, string) error
	Proxy(context.Context, string) (http.Handler, error)
}

type Config struct {
	AllowedUser     string
	MachineName     string
	WebDir          string
	AbsoluteTimeout time.Duration
	RememberTimeout time.Duration
	AuthConcurrency int
	// InsecureHTTP must only be set when the outer listener is plain HTTP. Its
	// zero value deliberately preserves the secure __Host- cookie contract.
	InsecureHTTP bool
}

// Server is an http.Handler and owns only HTTP-layer lifecycle state. Child
// process shutdown remains the session manager's responsibility.
type Server struct {
	config        Config
	authenticator auth.Authenticator
	authSessions  *auth.Store
	throttler     *auth.Throttler
	sessions      SessionManager
	codeServers   CodeServerManager
	logger        *log.Logger
	static        http.Handler
	authSlots     chan struct{}
	connections   *connectionTracker
	cookieName    string
	cookieSecure  bool
	shuttingDown  uint32
}

func New(config Config, authenticator auth.Authenticator, authSessions *auth.Store,
	throttler *auth.Throttler, manager SessionManager, codeServerManager CodeServerManager, logger *log.Logger) (*Server, error) {
	if config.AllowedUser == "" || config.MachineName == "" || config.WebDir == "" || config.AbsoluteTimeout <= 0 || config.RememberTimeout <= 0 || config.AuthConcurrency <= 0 {
		return nil, errors.New("invalid HTTP server configuration")
	}
	if authenticator == nil || authSessions == nil || throttler == nil || manager == nil || codeServerManager == nil {
		return nil, errors.New("HTTP server dependencies must not be nil")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	static, err := newSPAHandler(config.WebDir)
	if err != nil {
		return nil, err
	}
	authenticationCookieName := secureCookieName
	authenticationCookieSecure := true
	if config.InsecureHTTP {
		authenticationCookieName = insecureCookieName
		authenticationCookieSecure = false
	}
	return &Server{
		config:        config,
		authenticator: authenticator,
		authSessions:  authSessions,
		throttler:     throttler,
		sessions:      manager,
		codeServers:   codeServerManager,
		logger:        logger,
		static:        static,
		authSlots:     make(chan struct{}, config.AuthConcurrency),
		connections:   newConnectionTracker(),
		cookieName:    authenticationCookieName,
		cookieSecure:  authenticationCookieSecure,
	}, nil
}

func (s *Server) BeginShutdown() {
	atomic.StoreUint32(&s.shuttingDown, 1)
	s.connections.CloseAll()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	codeServerRoute := strings.HasPrefix(r.URL.Path, "/code/")
	if !codeServerRoute {
		s.setSecurityHeaders(w)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Printf("panic serving request: %v", recovered)
			writeError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
		}
	}()
	if atomic.LoadUint32(&s.shuttingDown) != 0 && r.URL.Path != "/healthz" {
		if codeServerRoute {
			s.setSecurityHeaders(w)
		}
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "The service is shutting down.")
		return
	}

	switch {
	case r.URL.Path == "/healthz":
		s.health(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		s.api(w, r)
	case strings.HasPrefix(r.URL.Path, "/terminal/"):
		s.terminal(w, r)
	case strings.HasPrefix(r.URL.Path, "/code/"):
		s.codeServer(w, r)
	default:
		s.static.ServeHTTP(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	if atomic.LoadUint32(&s.shuttingDown) != 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/api/config":
		s.publicConfig(w, r)
	case "/api/auth/login":
		s.login(w, r)
	case "/api/auth/session":
		s.authSession(w, r)
	case "/api/auth/logout":
		s.logout(w, r)
	case "/api/sessions":
		s.sessionsCollection(w, r)
	case "/api/directories":
		s.directories(w, r)
	case "/api/code-servers":
		s.codeServersCollection(w, r)
	case "/api/status":
		s.status(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			s.sessionItem(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/code-servers/") {
			s.codeServerItem(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
	}
}

func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"machineName": s.config.MachineName})
}

type loginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	RememberMe bool   `json:"rememberMe"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !validOrigin(r) {
		writeError(w, http.StatusForbidden, "origin_rejected", "The request origin is not allowed.")
		return
	}
	var request loginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, err)
		return
	}
	defer func() { request.Password = "" }()
	if request.Username == "" || request.Password == "" || len(request.Username) > 256 || len(request.Password) > 4096 ||
		strings.ContainsRune(request.Username, '\x00') || strings.ContainsRune(request.Password, '\x00') {
		s.loginFailure(w, r, request.Username)
		return
	}
	ipKey, usernameKey := loginThrottleKeys(r, request.Username)
	if !s.throttler.Allow(ipKey) || !s.throttler.Allow(usernameKey) {
		writeError(w, http.StatusTooManyRequests, "authentication_limited", "Authentication is temporarily unavailable. Try again later.")
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		writeError(w, http.StatusTooManyRequests, "authentication_limited", "Authentication is temporarily unavailable. Try again later.")
		return
	}

	// Always perform exactly one PAM exchange against the configured account.
	// This keeps arbitrary submitted usernames from probing other OS accounts
	// and prevents a fast username-mismatch path from becoming a timing oracle.
	authenticationErr := s.authenticator.Authenticate(r.Context(), s.config.AllowedUser, request.Password)
	if request.Username != s.config.AllowedUser || authenticationErr != nil {
		s.throttler.Failure(ipKey)
		s.throttler.Failure(usernameKey)
		writeError(w, http.StatusUnauthorized, "authentication_failed", "Invalid username or password.")
		return
	}
	var token string
	var session auth.Session
	var err error
	if request.RememberMe {
		token, session, err = s.authSessions.CreateRemembered(request.Username, s.config.RememberTimeout)
	} else {
		token, session, err = s.authSessions.Create(request.Username)
	}
	if err != nil {
		s.logger.Printf("create authentication session: %v", err)
		writeError(w, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is temporarily unavailable.")
		return
	}
	s.throttler.Success(ipKey)
	s.throttler.Success(usernameKey)
	cookie := &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		Secure:   s.cookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if session.Remembered {
		cookie.Expires = session.ExpiresAt
		cookie.MaxAge = int(time.Until(session.ExpiresAt).Seconds())
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, authResponse(session))
}

func (s *Server) loginFailure(w http.ResponseWriter, r *http.Request, username string) {
	ipKey, usernameKey := loginThrottleKeys(r, username)
	if s.throttler.Allow(ipKey) {
		s.throttler.Failure(ipKey)
	}
	if s.throttler.Allow(usernameKey) {
		s.throttler.Failure(usernameKey)
	}
	writeError(w, http.StatusUnauthorized, "authentication_failed", "Invalid username or password.")
}

func (s *Server) authSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	_, session, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, authResponse(session))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	token, session, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if !s.authorizeMutation(w, r, session) {
		return
	}
	if err := s.authSessions.Delete(token); err != nil {
		s.logger.Printf("delete authentication session: %v", err)
		writeError(w, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is temporarily unavailable.")
		return
	}
	s.connections.CloseToken(token)
	s.clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) sessionsCollection(w http.ResponseWriter, r *http.Request) {
	_, session, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := s.sessions.List(r.Context())
		if err != nil {
			s.internalError(w, "list sessions", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": items})
	case http.MethodPost:
		if !s.authorizeMutation(w, r, session) {
			return
		}
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(w, r, &request); err != nil {
			writeDecodeError(w, err)
			return
		}
		created, err := s.sessions.Create(r.Context(), request.Name)
		if err != nil {
			s.sessionError(w, "create session", err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]interface{}{"session": created})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) directories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if _, _, ok := s.authenticate(w, r); !ok {
		return
	}
	paths, present := r.URL.Query()["path"]
	if len(paths) > 1 {
		writeError(w, http.StatusBadRequest, "invalid_directory", "The directory path is invalid.")
		return
	}
	requestedPath := ""
	if present && len(paths) == 1 {
		requestedPath = paths[0]
	}
	listing, err := s.codeServers.Browse(r.Context(), requestedPath)
	if err != nil {
		s.codeServerError(w, "browse directories", err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) codeServersCollection(w http.ResponseWriter, r *http.Request) {
	_, authSession, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := s.codeServers.List(r.Context())
		if err != nil {
			s.codeServerError(w, "list code servers", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"codeServers": items})
	case http.MethodPost:
		if !s.authorizeMutation(w, r, authSession) {
			return
		}
		var request struct {
			FolderPath string `json:"folderPath"`
		}
		if err := decodeJSON(w, r, &request); err != nil {
			writeDecodeError(w, err)
			return
		}
		instance, reused, err := s.codeServers.Create(r.Context(), request.FolderPath)
		if err != nil {
			s.codeServerError(w, "create code server", err)
			return
		}
		status := http.StatusCreated
		if reused {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]interface{}{"codeServer": instance, "reused": reused})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) codeServerItem(w http.ResponseWriter, r *http.Request) {
	_, authSession, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/api/code-servers/")
	if strings.Contains(remainder, "/") || !codeservers.ValidID(remainder) {
		writeError(w, http.StatusNotFound, "not_found", "The requested code server was not found.")
		return
	}
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, http.MethodDelete)
		return
	}
	if !s.authorizeMutation(w, r, authSession) {
		return
	}
	if err := s.codeServers.Delete(r.Context(), remainder); err != nil {
		s.codeServerError(w, "stop code server", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sessionItem(w http.ResponseWriter, r *http.Request) {
	_, authSession, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || !sessions.ValidID(parts[0]) {
		writeError(w, http.StatusNotFound, "not_found", "The requested session was not found.")
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if !s.authorizeMutation(w, r, authSession) {
			return
		}
		if err := s.sessions.Delete(r.Context(), id); err != nil {
			s.sessionError(w, "delete session", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "connect" && r.Method == http.MethodPost {
		if !s.authorizeMutation(w, r, authSession) {
			return
		}
		connected, terminalURL, err := s.sessions.Connect(r.Context(), id)
		if err != nil {
			s.sessionError(w, "connect session", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session": connected, "terminalUrl": terminalURL,
		})
		return
	}
	if len(parts) == 2 && parts[1] == "clipboard" && r.Method == http.MethodPost {
		if !s.authorizeMutation(w, r, authSession) {
			return
		}
		selection, err := s.sessions.TakeLatestSelection(r.Context(), id)
		if err != nil {
			s.sessionError(w, "read tmux selection", err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"text": selection})
		return
	}
	if len(parts) == 2 && parts[1] == "clipboard" && r.Method == http.MethodDelete {
		if !s.authorizeMutation(w, r, authSession) {
			return
		}
		// Once authorized, finish this short idempotent privacy cleanup even if
		// the browser closes or reloads while the source-barrier request is in
		// flight. This prevents an older tmux buffer resurfacing after an xterm
		// selection was already shown to the user.
		discardContext, cancelDiscard := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelDiscard()
		if err := s.sessions.DiscardSelections(discardContext, id); err != nil {
			s.sessionError(w, "discard stale tmux selections", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 1 {
		methodNotAllowed(w, http.MethodDelete)
		return
	}
	if len(parts) == 2 && parts[1] == "connect" {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if len(parts) == 2 && parts[1] == "clipboard" {
		methodNotAllowed(w, http.MethodPost, http.MethodDelete)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if _, _, ok := s.authenticate(w, r); !ok {
		return
	}
	codeServers, err := s.codeServers.List(r.Context())
	if err != nil {
		s.codeServerError(w, "inspect code servers", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
		"dependencies": map[string]interface{}{
			"tmux":       map[string]bool{"available": true},
			"ttyd":       map[string]bool{"available": true},
			"codeServer": map[string]bool{"available": true},
		},
		"activeTerminals":   s.sessions.ActiveTerminals(),
		"activeCodeServers": len(codeServers),
	})
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	// The terminal client never owns the system clipboard. Explicitly narrow
	// the route-level policy as defense in depth; code-server has its own proxy
	// policy and is unaffected.
	w.Header().Set("Permissions-Policy", "clipboard-read=(), clipboard-write=()")
	if atomic.LoadUint32(&s.shuttingDown) != 0 {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "The service is shutting down.")
		return
	}
	token, authSession, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/terminal/")
	id, _, _ := strings.Cut(remainder, "/")
	if !sessions.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	if isWebSocket(r) && !validOrigin(r) {
		writeError(w, http.StatusForbidden, "origin_rejected", "The request origin is not allowed.")
		return
	}
	proxy, err := s.sessions.Proxy(r.Context(), id)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) || errors.Is(err, sessions.ErrInvalidID) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, sessions.ErrTerminalNotRunning) {
			http.Error(w, "terminal is not connected", http.StatusConflict)
			return
		}
		s.logger.Printf("proxy terminal: %v", err)
		http.Error(w, "terminal is unavailable", http.StatusBadGateway)
		return
	}
	if isWebSocket(r) {
		trackedWriter := &trackedResponseWriter{
			ResponseWriter: w,
			tracker:        s.connections,
			token:          token,
			deadline:       authSession.Deadline(),
			validUntil: func() (time.Time, bool) {
				return s.authSessions.DeadlineFor(token)
			},
		}
		proxy.ServeHTTP(trackedWriter, r)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) codeServer(w http.ResponseWriter, r *http.Request) {
	if atomic.LoadUint32(&s.shuttingDown) != 0 {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "The service is shutting down.")
		return
	}
	token, authSession, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/code/")
	id, _, _ := strings.Cut(remainder, "/")
	if !codeservers.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	// Browser-driven mutations must originate from this exact request origin.
	// code-server does not use Remote Terminal's CSRF token, so this check is
	// the HTTP counterpart to the mandatory WebSocket Origin validation.
	unsafeMethod := r.Method != http.MethodGet && r.Method != http.MethodHead
	if (isWebSocket(r) || unsafeMethod) && !validOrigin(r) {
		writeError(w, http.StatusForbidden, "origin_rejected", "The request origin is not allowed.")
		return
	}
	proxy, err := s.codeServers.Proxy(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, codeservers.ErrNotFound), errors.Is(err, codeservers.ErrInvalidID):
			http.NotFound(w, r)
		case errors.Is(err, codeservers.ErrNotRunning):
			http.Error(w, "code server is unavailable", http.StatusBadGateway)
		case errors.Is(err, codeservers.ErrShuttingDown):
			writeError(w, http.StatusServiceUnavailable, "shutting_down", "The service is shutting down.")
		default:
			s.logger.Printf("proxy code server: %v", err)
			http.Error(w, "code server is unavailable", http.StatusBadGateway)
		}
		return
	}
	if isWebSocket(r) {
		trackedWriter := &trackedResponseWriter{
			ResponseWriter: w,
			tracker:        s.connections,
			token:          token,
			deadline:       authSession.Deadline(),
			validUntil: func() (time.Time, bool) {
				return s.authSessions.DeadlineFor(token)
			},
		}
		proxy.ServeHTTP(trackedWriter, r)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (string, auth.Session, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication is required.")
		return "", auth.Session{}, false
	}
	session, ok := s.authSessions.Get(cookie.Value)
	if !ok || session.Username != s.config.AllowedUser {
		s.connections.CloseToken(cookie.Value)
		s.clearCookie(w)
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication is required.")
		return "", auth.Session{}, false
	}
	s.connections.Refresh(cookie.Value, session.Deadline())
	return cookie.Value, session, true
}

func (s *Server) authorizeMutation(w http.ResponseWriter, r *http.Request, session auth.Session) bool {
	if !validOrigin(r) {
		writeError(w, http.StatusForbidden, "origin_rejected", "The request origin is not allowed.")
		return false
	}
	if !auth.EqualCSRF(session.CSRFToken, r.Header.Get(csrfHeader)) {
		writeError(w, http.StatusForbidden, "csrf_rejected", "The request could not be verified.")
		return false
	}
	return true
}

func (s *Server) sessionError(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, sessions.ErrInvalidName), errors.Is(err, sessions.ErrInvalidID):
		writeError(w, http.StatusBadRequest, "invalid_session", "The session name or identifier is invalid.")
	case errors.Is(err, sessions.ErrNotFound):
		writeError(w, http.StatusNotFound, "session_not_found", "The requested session was not found.")
	case errors.Is(err, sessions.ErrNameExists):
		writeError(w, http.StatusConflict, "session_exists", "A session with that name already exists.")
	case errors.Is(err, sessions.ErrLimitReached):
		writeError(w, http.StatusConflict, "session_limit", "The maximum number of sessions has been reached.")
	case errors.Is(err, sessions.ErrNoSelection):
		writeError(w, http.StatusConflict, "no_selection", "Select terminal text with the mouse first.")
	case errors.Is(err, sessions.ErrSelectionTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "selection_too_large", "The terminal selection is too large to copy.")
	case errors.Is(err, sessions.ErrInvalidSelection):
		writeError(w, http.StatusUnprocessableEntity, "selection_invalid", "The terminal selection is not valid text.")
	case errors.Is(err, sessions.ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "The service is shutting down.")
	default:
		s.internalError(w, action, err)
	}
}

func (s *Server) codeServerError(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, codeservers.ErrInvalidPath), errors.Is(err, codeservers.ErrInvalidID):
		writeError(w, http.StatusBadRequest, "invalid_directory", "The directory path or identifier is invalid.")
	case errors.Is(err, codeservers.ErrDirectoryInaccessible):
		writeError(w, http.StatusForbidden, "directory_inaccessible", "The directory is not accessible.")
	case errors.Is(err, codeservers.ErrDirectoryNotFound):
		writeError(w, http.StatusNotFound, "directory_not_found", "The directory was not found.")
	case errors.Is(err, codeservers.ErrNotFound):
		writeError(w, http.StatusNotFound, "code_server_not_found", "The requested code server was not found.")
	case errors.Is(err, codeservers.ErrLimitReached):
		writeError(w, http.StatusConflict, "code_server_limit", "The maximum number of code servers has been reached.")
	case errors.Is(err, codeservers.ErrStartFailed):
		s.logger.Printf("%s: %v", action, err)
		writeError(w, http.StatusBadGateway, "code_server_start_failed", "Code Server could not be started.")
	case errors.Is(err, codeservers.ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "The service is shutting down.")
	default:
		s.internalError(w, action, err)
	}
}

func (s *Server) internalError(w http.ResponseWriter, action string, err error) {
	s.logger.Printf("%s: %v", action, err)
	writeError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: "", Path: "/", Secure: s.cookieSecure, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0),
	})
}

func (s *Server) setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; font-src 'self' data:; frame-src 'self'; frame-ancestors 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Permissions-Policy", "clipboard-read=(self), clipboard-write=(self)")
}

func validOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	raw := values[0]
	if raw == "" || strings.Contains(raw, ",") {
		return false
	}
	origin, err := url.Parse(raw)
	if err != nil || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin.Scheme == scheme && strings.EqualFold(origin.Host, r.Host)
}

func isWebSocket(r *http.Request) bool {
	return headerContainsToken(r.Header, "Connection", "upgrade") &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

func headerContainsToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func loginThrottleKeys(r *http.Request, submitted string) (string, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	digest := sha256.Sum256([]byte(submitted))
	return "ip\x00" + host, "username\x00" + fmt.Sprintf("%x", digest[:])
}

type decodeError struct {
	tooLarge bool
	err      error
}

func (e *decodeError) Error() string { return e.err.Error() }

func decodeJSON(w http.ResponseWriter, r *http.Request, destination interface{}) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &decodeError{err: errors.New("Content-Type must be application/json")}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maximum *http.MaxBytesError
		return &decodeError{tooLarge: errors.As(err, &maximum), err: err}
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		var maximum *http.MaxBytesError
		return &decodeError{tooLarge: errors.As(err, &maximum), err: errors.New("request must contain exactly one JSON value")}
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var decode *decodeError
	if errors.As(err, &decode) && decode.tooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "The request body is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "The request body is invalid.")
}

func authResponse(session auth.Session) map[string]interface{} {
	return map[string]interface{}{
		"authenticated": true,
		"user":          map[string]string{"username": session.Username},
		"csrfToken":     session.CSRFToken,
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The request method is not allowed.")
}

type spaHandler struct {
	directory string
	files     http.Handler
}

func newSPAHandler(directory string) (http.Handler, error) {
	index := filepath.Join(directory, "index.html")
	if info, err := os.Stat(index); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("web directory does not contain index.html")
	}
	return &spaHandler{directory: directory, files: http.FileServer(http.Dir(directory))}, nil
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	cleaned := path.Clean("/" + r.URL.Path)
	name := strings.TrimPrefix(cleaned, "/")
	if name != "" && name != "." {
		if info, err := os.Stat(filepath.Join(h.directory, filepath.FromSlash(name))); err == nil && !info.IsDir() {
			h.files.ServeHTTP(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(h.directory, "index.html"))
}
