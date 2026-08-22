package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/auth"
	"golang.org/x/net/http/httpguts"
)

type principalKind uint8

const (
	principalBearer principalKind = iota + 1
	principalSession
)

type requestPrincipal struct {
	kind    principalKind
	token   string
	session auth.Session
}

type requestPrincipalKey struct{}

func withRequestPrincipal(ctx context.Context, principal requestPrincipal) context.Context {
	return context.WithValue(ctx, requestPrincipalKey{}, principal)
}

func requestPrincipalFrom(ctx context.Context) (requestPrincipal, bool) {
	principal, ok := ctx.Value(requestPrincipalKey{}).(requestPrincipal)
	return principal, ok
}

func isPublicRemoteRoute(requestPath string) bool {
	switch requestPath {
	case "/healthz", "/readyz", "/api/v1/auth/login", "/api/v1/auth/session", "/api/v1/auth/activate", "/api/v1/auth/logout", "/api/v1/auth/revoke-stale":
		return true
	default:
		// The embedded SPA and its fingerprinted assets contain no credentials or
		// setup data. Serving them lets the application present its own login view
		// instead of triggering a browser Basic-auth prompt.
		return !strings.HasPrefix(requestPath, "/api/")
	}
}

func (s *Server) authenticateRemote(w http.ResponseWriter, r *http.Request, requestID string) (requestPrincipal, bool) {
	if values := r.Header.Values("Authorization"); len(values) != 0 {
		if len(values) == 1 {
			scheme, credential, found := strings.Cut(strings.TrimSpace(values[0]), " ")
			if found && strings.EqualFold(scheme, "Bearer") && credential != "" &&
				!strings.ContainsAny(credential, " \t\r\n,") && constantTimeEqual(s.config.RemoteAuthToken, credential) {
				return requestPrincipal{kind: principalBearer}, true
			}
		}
		s.writeAuthenticationRequired(w, requestID)
		return requestPrincipal{}, false
	}

	token, session, ok := s.browserSession(r)
	if !ok || session.Username != s.config.AllowedUser {
		if token != "" {
			_ = s.authSessions.Delete(token)
			s.clearSessionCookie(w)
		}
		s.writeAuthenticationRequired(w, requestID)
		return requestPrincipal{}, false
	}
	if !session.Activated {
		s.writeAuthenticationRequired(w, requestID)
		return requestPrincipal{}, false
	}
	return requestPrincipal{kind: principalSession, token: token, session: session}, true
}

func (s *Server) browserSession(r *http.Request) (string, auth.Session, bool) {
	var token string
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == remoteSessionCookieName {
			token = cookie.Value
			count++
		}
	}
	if count != 1 || token == "" {
		return token, auth.Session{}, false
	}
	session, ok := s.authSessions.Get(token)
	return token, session, ok
}

func (s *Server) writeAuthenticationRequired(w http.ResponseWriter, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	if s.config.RemoteAuthToken != "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="web-setup-manager"`)
	}
	writeError(w, http.StatusUnauthorized, requestID, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil, false)
}

type loginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	RememberMe bool   `json:"rememberMe"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodPost) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	if !s.config.RemoteAccess {
		writeError(w, http.StatusNotFound, requestID, "NOT_FOUND", "The requested resource was not found.", nil, false)
		return
	}
	if !validRemoteSessionOrigin(r) {
		writeError(w, http.StatusForbidden, requestID, "ORIGIN_REJECTED", "The request origin is not allowed.", nil, false)
		return
	}
	var request loginRequest
	if err := decodeJSON(w, r, &request, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, requestID, "INVALID_JSON", "The request body is invalid.", nil, false)
		return
	}
	defer func() { request.Password = "" }()
	if request.Username == "" || request.Password == "" || len(request.Username) > 256 || len(request.Password) > 4096 ||
		strings.ContainsRune(request.Username, '\x00') || strings.ContainsRune(request.Password, '\x00') {
		s.loginFailure(w, r, requestID, request.Username)
		return
	}
	ipKey, usernameKey := loginThrottleKeys(r, request.Username)
	if !s.throttler.Allow(ipKey) || !s.throttler.Allow(usernameKey) {
		writeError(w, http.StatusTooManyRequests, requestID, "AUTHENTICATION_LIMITED", "Authentication is temporarily unavailable. Try again later.", nil, true)
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		writeError(w, http.StatusTooManyRequests, requestID, "AUTHENTICATION_LIMITED", "Authentication is temporarily unavailable. Try again later.", nil, true)
		return
	}

	// Always perform one PAM exchange against the configured account, even when
	// the submitted username differs. This avoids probing arbitrary OS accounts
	// and removes the fast username-existence timing path.
	authenticationErr := s.authenticator.Authenticate(r.Context(), s.config.AllowedUser, request.Password)
	if errors.Is(authenticationErr, auth.ErrUnavailable) {
		s.logger.Error("authenticate configured account", "operation", "auth-login", "result", "failed", "error_code", "AUTHENTICATION_UNAVAILABLE")
		writeError(w, http.StatusServiceUnavailable, requestID, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", nil, true)
		return
	}
	if request.Username != s.config.AllowedUser || authenticationErr != nil {
		s.throttler.Failure(ipKey)
		s.throttler.Failure(usernameKey)
		writeError(w, http.StatusUnauthorized, requestID, "AUTHENTICATION_FAILED", "Invalid username or password.", nil, false)
		return
	}

	var token string
	var session auth.Session
	var err error
	if request.RememberMe {
		token, session, err = s.authSessions.CreateRemembered(request.Username, s.config.AuthRememberTimeout)
	} else {
		token, session, err = s.authSessions.Create(request.Username)
	}
	if err != nil {
		s.logger.Error("create authentication session", "operation", "auth-login", "result", "failed", "error_code", "AUTHENTICATION_UNAVAILABLE")
		writeError(w, http.StatusServiceUnavailable, requestID, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", nil, true)
		return
	}
	// A successful login rotates an existing session from this browser. Create
	// the replacement first so a capacity/persistence error cannot discard the
	// session that is still represented by the browser cookie. Store.Delete is
	// transactional for remembered sessions; on failure the old session remains
	// valid and the new, undisclosed token is removed best-effort.
	if oldToken, oldSession, oldOK := s.browserSession(r); oldOK && oldSession.Username == s.config.AllowedUser {
		if err := s.authSessions.Delete(oldToken); err != nil {
			_ = s.authSessions.Delete(token)
			s.logger.Error("rotate authentication session", "operation", "auth-login", "result", "failed", "error_code", "AUTHENTICATION_UNAVAILABLE")
			writeError(w, http.StatusServiceUnavailable, requestID, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", nil, true)
			return
		}
	}
	s.throttler.Success(ipKey)
	s.throttler.Success(usernameKey)
	s.setSessionCookie(w, token, session)
	writeJSON(w, http.StatusOK, authenticationResponse(session, true))
}

func (s *Server) loginFailure(w http.ResponseWriter, r *http.Request, requestID, username string) {
	ipKey, usernameKey := loginThrottleKeys(r, username)
	if s.throttler.Allow(ipKey) {
		s.throttler.Failure(ipKey)
	}
	if s.throttler.Allow(usernameKey) {
		s.throttler.Failure(usernameKey)
	}
	writeError(w, http.StatusUnauthorized, requestID, "AUTHENTICATION_FAILED", "Invalid username or password.", nil, false)
}

func (s *Server) authenticationSession(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	if !s.config.RemoteAccess {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true, "loginRequired": false, "user": nil, "csrfToken": s.csrfToken,
		})
		return
	}
	token, session, ok := s.browserSession(r)
	if !ok || session.Username != s.config.AllowedUser {
		if token != "" {
			_ = s.authSessions.Delete(token)
			s.clearSessionCookie(w)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false, "loginRequired": true, "user": nil,
		})
		return
	}
	if !session.Activated {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false, "loginRequired": true, "user": nil,
		})
		return
	}
	writeJSON(w, http.StatusOK, authenticationResponse(session, true))
}

func (s *Server) activateSession(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodPost) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	if !s.config.RemoteAccess {
		writeError(w, http.StatusNotFound, requestID, "NOT_FOUND", "The requested resource was not found.", nil, false)
		return
	}
	token, session, ok := s.browserSession(r)
	if !ok || session.Username != s.config.AllowedUser {
		s.writeAuthenticationRequired(w, requestID)
		return
	}
	if code, message := s.sessionMutationRejection(r, session); code != "" {
		writeError(w, http.StatusForbidden, requestID, code, message, nil, false)
		return
	}
	if _, activated, err := s.authSessions.Activate(token, r.Header.Get("X-CSRF-Token")); err != nil {
		s.logger.Error("activate authentication session", "operation", "auth-activate", "result", "failed", "error_code", "AUTHENTICATION_UNAVAILABLE")
		writeError(w, http.StatusServiceUnavailable, requestID, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", nil, true)
		return
	} else if !activated {
		s.writeAuthenticationRequired(w, requestID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodPost) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	if !s.config.RemoteAccess {
		writeError(w, http.StatusNotFound, requestID, "NOT_FOUND", "The requested resource was not found.", nil, false)
		return
	}
	token, session, ok := s.browserSession(r)
	if !ok || session.Username != s.config.AllowedUser {
		if token != "" {
			_ = s.authSessions.Delete(token)
			s.clearSessionCookie(w)
		}
		s.writeAuthenticationRequired(w, requestID)
		return
	}
	if !s.authorizeSessionMutation(r, session) {
		code, message := s.sessionMutationRejection(r, session)
		writeError(w, http.StatusForbidden, requestID, code, message, nil, false)
		return
	}
	if err := s.authSessions.Delete(token); err != nil {
		s.logger.Error("delete authentication session", "operation", "auth-logout", "result", "failed", "error_code", "AUTHENTICATION_UNAVAILABLE")
		writeError(w, http.StatusServiceUnavailable, requestID, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", nil, true)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// revokeStale invalidates exactly the session bound to the request cookie and
// CSRF token without emitting Set-Cookie. Omitting a cookie mutation is
// intentional: a delayed stale-login revocation response must not erase a
// newer login cookie that the browser accepted while this request was in
// flight.
func (s *Server) revokeStale(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodPost) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	if !s.config.RemoteAccess {
		writeError(w, http.StatusNotFound, requestID, "NOT_FOUND", "The requested resource was not found.", nil, false)
		return
	}
	token, session, ok := s.browserSession(r)
	if !ok || session.Username != s.config.AllowedUser {
		if token != "" {
			_ = s.authSessions.Delete(token)
		}
		s.writeAuthenticationRequired(w, requestID)
		return
	}
	if !s.authorizeSessionMutation(r, session) {
		code, message := s.sessionMutationRejection(r, session)
		writeError(w, http.StatusForbidden, requestID, code, message, nil, false)
		return
	}
	if err := s.authSessions.Delete(token); err != nil {
		s.logger.Error("delete stale authentication session", "operation", "auth-revoke-stale", "result", "failed", "error_code", "AUTHENTICATION_UNAVAILABLE")
		writeError(w, http.StatusServiceUnavailable, requestID, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", nil, true)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func authenticationResponse(session auth.Session, loginRequired bool) map[string]any {
	return map[string]any{
		"authenticated": true,
		"loginRequired": loginRequired,
		"user":          map[string]string{"username": session.Username},
		"csrfToken":     session.CSRFToken,
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, session auth.Session) {
	cookie := &http.Cookie{
		Name: remoteSessionCookieName, Value: token, Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	}
	if session.Remembered {
		cookie.Expires = session.ExpiresAt
		remaining := time.Until(session.ExpiresAt)
		if remaining > 0 {
			cookie.MaxAge = max(1, int(remaining/time.Second))
		}
	}
	http.SetCookie(w, cookie)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: remoteSessionCookieName, Value: "", Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0),
	})
}

func (s *Server) authorizeSessionMutation(r *http.Request, session auth.Session) bool {
	code, _ := s.sessionMutationRejection(r, session)
	return code == ""
}

func (s *Server) sessionMutationRejection(r *http.Request, session auth.Session) (string, string) {
	if !isMutation(r.Method) || s.shuttingDown.Load() {
		return "REQUEST_FORBIDDEN", "The mutation was rejected by the same-origin security policy."
	}
	if !validRemoteSessionOrigin(r) {
		return "ORIGIN_REJECTED", "The request origin is not allowed."
	}
	if !auth.EqualCSRF(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		return "CSRF_REJECTED", "The request could not be verified."
	}
	return "", ""
}

func validRemoteSessionOrigin(r *http.Request) bool {
	if !httpguts.ValidHostHeader(r.Host) {
		return false
	}
	values := r.Header.Values("Origin")
	if len(values) != 1 || values[0] == "" || strings.Contains(values[0], ",") {
		return false
	}
	origin, err := url.Parse(values[0])
	if err != nil || origin.Scheme != "https" || origin.User != nil || origin.Opaque != "" ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	return strings.EqualFold(origin.Host, r.Host)
}

func loginThrottleKeys(r *http.Request, submittedUsername string) (string, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	digest := sha256.Sum256([]byte(submittedUsername))
	return "ip\x00" + host, "username\x00" + fmt.Sprintf("%x", digest[:])
}
