package codeservers

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

const (
	secureAuthenticationCookie   = "__Host-remoteterminal_session"
	insecureAuthenticationCookie = "remoteterminal_session"
	reservedAuthenticationCookie = secureAuthenticationCookie
)

// newCodeServerProxy returns a handler that understands the externally visible
// per-instance prefix. It deliberately does not add CSP or authentication:
// those are policies of the containing HTTP service.
func newCodeServerProxy(socket, id string) (http.Handler, *http.Transport) {
	target, _ := url.Parse("http://unix")
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(dialCtx, "unix", socket)
		},
	}
	basePath := instanceBasePath(id)
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalHost := request.Host
		originalRemoteAddr := request.RemoteAddr
		originalTLS := request.TLS != nil
		originalDirector(request)
		stripCodeServerPrefix(request.URL, basePath)
		sanitizeCodeServerRequest(request.Header)
		request.Header.Set("X-Forwarded-Host", originalHost)
		request.Header.Set("X-Forwarded-Prefix", basePath)
		if originalTLS {
			request.Header.Set("X-Forwarded-Proto", "https")
		} else {
			request.Header.Set("X-Forwarded-Proto", "http")
		}
		if clientIP, _, err := net.SplitHostPort(originalRemoteAddr); err == nil {
			// Go 1.19's ReverseProxy appends RemoteAddr after Director. Make
			// RemoteAddr carry the authoritative client IP and leave the header
			// empty so only that value is written.
			request.Header.Del("X-Forwarded-For")
			request.RemoteAddr = net.JoinHostPort(clientIP, "0")
		}
		// NewSingleHostReverseProxy intentionally preserves the incoming Host.
		// code-server uses it for Origin validation and subpath URL generation.
		request.Host = originalHost
	}
	proxy.Transport = transport
	proxy.ModifyResponse = func(response *http.Response) error {
		rewriteCodeServerResponse(response.Header, basePath)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "code-server is unavailable", http.StatusBadGateway)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == basePath:
			target := basePath + "/"
			if request.URL.RawQuery != "" {
				target += "?" + request.URL.RawQuery
			}
			http.Redirect(w, request, target, http.StatusPermanentRedirect)
		case strings.HasPrefix(request.URL.Path, basePath+"/"):
			proxy.ServeHTTP(w, request)
		default:
			http.NotFound(w, request)
		}
	}), transport
}

func stripCodeServerPrefix(requestURL *url.URL, basePath string) {
	requestURL.Path = strings.TrimPrefix(requestURL.Path, basePath)
	if requestURL.Path == "" {
		requestURL.Path = "/"
	}
	if requestURL.RawPath != "" {
		requestURL.RawPath = strings.TrimPrefix(requestURL.RawPath, basePath)
		if requestURL.RawPath == "" {
			requestURL.RawPath = "/"
		}
	}
}

func sanitizeCodeServerRequest(header http.Header) {
	for name := range header {
		canonical := http.CanonicalHeaderKey(name)
		if canonical == "Forwarded" || strings.HasPrefix(canonical, "X-Forwarded-") ||
			strings.HasPrefix(canonical, "X-Remote-Terminal-") {
			header.Del(name)
		}
	}
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"X-CSRF-Token",
		"X-XSRF-Token",
		"X-Forwarded-Authorization",
		"X-Forwarded-Access-Token",
	} {
		header.Del(name)
	}
	stripReservedCookie(header)
}

func stripReservedCookie(header http.Header) {
	values := header.Values("Cookie")
	if len(values) == 0 {
		return
	}
	header.Del("Cookie")
	for _, value := range values {
		kept := make([]string, 0)
		for _, part := range strings.Split(value, ";") {
			part = strings.TrimSpace(part)
			name, _, ok := strings.Cut(part, "=")
			if !ok || reservedCookieName(strings.TrimSpace(name)) {
				continue
			}
			kept = append(kept, part)
		}
		if len(kept) > 0 {
			header.Add("Cookie", strings.Join(kept, "; "))
		}
	}
}

func rewriteCodeServerResponse(header http.Header, basePath string) {
	// Keep code-server's functional CSP and add a separate containment policy.
	// Browsers enforce every CSP header, so this restricts framing without
	// overriding the workers, blobs, webviews, and scripts required by VS Code.
	header.Add("Content-Security-Policy", "frame-ancestors 'self'")
	header.Set("X-Frame-Options", "SAMEORIGIN")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Permissions-Policy", "clipboard-read=(self), clipboard-write=(self)")
	// An editor or extension is less trusted than the containing application.
	// It must not opt its same-origin proxy responses into cross-origin reads or
	// clear the authentication cookie that protects the outer application.
	for name := range header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-Allow-") {
			header.Del(name)
		}
	}
	header.Del("Clear-Site-Data")
	if location := header.Get("Location"); isRootRelative(location) &&
		location != basePath && !strings.HasPrefix(location, basePath+"/") {
		header.Set("Location", basePath+location)
	}
	// Never let an upstream response authorize a service-worker scope outside
	// its editor instance. Set this unconditionally so missing, malformed, or
	// unexpectedly broad upstream values all converge on the safe scope.
	header.Set("Service-Worker-Allowed", basePath+"/")

	setCookies := header.Values("Set-Cookie")
	if len(setCookies) == 0 {
		return
	}
	header.Del("Set-Cookie")
	for _, setCookie := range setCookies {
		cookieName := strings.TrimSpace(strings.SplitN(setCookie, "=", 2)[0])
		if reservedCookieName(cookieName) {
			continue
		}
		parts := strings.Split(setCookie, ";")
		scoped := make([]string, 0, len(parts)+1)
		scoped = append(scoped, parts[0])
		hasPath := false
		for index := 1; index < len(parts); index++ {
			attribute := strings.TrimSpace(parts[index])
			name, _, ok := strings.Cut(attribute, "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), "Domain") {
				// Force a host-only cookie. An embedded editor must not be able
				// to write cookies for sibling hosts on a parent domain.
				continue
			}
			if ok && strings.EqualFold(strings.TrimSpace(name), "Path") {
				if !hasPath {
					scoped = append(scoped, " Path="+basePath+"/")
					hasPath = true
				}
				continue
			}
			scoped = append(scoped, parts[index])
		}
		if !hasPath {
			scoped = append(scoped, " Path="+basePath+"/")
		}
		header.Add("Set-Cookie", strings.Join(scoped, ";"))
	}
}

func reservedCookieName(name string) bool {
	return name == secureAuthenticationCookie || name == insecureAuthenticationCookie
}

func isRootRelative(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//")
}
