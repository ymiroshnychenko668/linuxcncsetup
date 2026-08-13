package codeservers

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProxyStripsPrefixSanitizesCredentialsAndRewritesResponse(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "code.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan *http.Request, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		received <- request.Clone(context.Background())
		w.Header().Set("Content-Security-Policy", "default-src 'self'; worker-src 'self' blob:")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		w.Header().Set("Clear-Site-Data", `"cookies"`)
		w.Header().Set("Location", "/login?next=%2F")
		w.Header().Set("Service-Worker-Allowed", "/api")
		w.Header().Add("Set-Cookie", "code-server-session=abc; Domain=.example; Path=/api; HttpOnly; SameSite=Lax")
		w.Header().Add("Set-Cookie", "editor-preference=dark; Secure")
		w.Header().Add("Set-Cookie", reservedAuthenticationCookie+"=attack; Path=/; Secure")
		w.Header().Add("Set-Cookie", insecureAuthenticationCookie+"=attack; Path=/")
		w.WriteHeader(http.StatusFound)
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()

	id := "0123456789abcdef0123456789abcdef"
	handler, transport := newCodeServerProxy(socket, id)
	defer transport.CloseIdleConnections()
	request := httptest.NewRequest(http.MethodGet,
		"https://machine.example/code/"+id+"/stable/path?q=1", nil)
	request.Host = "machine.example"
	request.RemoteAddr = "192.0.2.7:4242"
	request.Header.Set("Cookie", reservedAuthenticationCookie+"=secret; "+
		insecureAuthenticationCookie+"=insecure-secret; theme=dark; code=xyz")
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Forwarded", "for=attacker")
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	request.Header.Set("X-Forwarded-Host", "attacker.example")
	request.Header.Set("X-Forwarded-Proto", "javascript")
	request.Header.Set("X-Forwarded-Prefix", "/attacker")
	request.Header.Set("X-Remote-Terminal-Secret", "internal")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%q", recorder.Code, recorder.Body.String())
	}
	base := "/code/" + id
	if got := recorder.Header().Get("Location"); got != base+"/login?next=%2F" {
		t.Fatalf("Location = %q", got)
	}
	if got := recorder.Header().Get("Service-Worker-Allowed"); got != base+"/" {
		t.Fatalf("Service-Worker-Allowed = %q", got)
	}
	policies := recorder.Header().Values("Content-Security-Policy")
	if len(policies) != 2 || !strings.Contains(policies[0], "worker-src 'self' blob:") ||
		policies[1] != "frame-ancestors 'self'" {
		t.Fatalf("Content-Security-Policy = %#v", policies)
	}
	for name, want := range map[string]string{
		"X-Frame-Options":        "SAMEORIGIN",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := recorder.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Allow-Private-Network",
		"Clear-Site-Data",
	} {
		if got := recorder.Header().Get(name); got != "" {
			t.Fatalf("unsafe upstream %s survived: %q", name, got)
		}
	}
	cookies := recorder.Header().Values("Set-Cookie")
	if len(cookies) != 2 || cookies[0] != "code-server-session=abc; Path="+base+"/; HttpOnly; SameSite=Lax" ||
		cookies[1] != "editor-preference=dark; Secure; Path="+base+"/" {
		t.Fatalf("Set-Cookie = %#v", cookies)
	}

	select {
	case upstream := <-received:
		if upstream.URL.Path != "/stable/path" || upstream.URL.RawQuery != "q=1" {
			t.Fatalf("upstream URL = %s?%s", upstream.URL.Path, upstream.URL.RawQuery)
		}
		if upstream.Host != "machine.example" {
			t.Fatalf("upstream Host = %q", upstream.Host)
		}
		if got := upstream.Header.Get("Cookie"); got != "theme=dark; code=xyz" {
			t.Fatalf("upstream Cookie = %q", got)
		}
		for _, name := range []string{"Authorization", "X-CSRF-Token", "Forwarded", "X-Remote-Terminal-Secret"} {
			if value := upstream.Header.Get(name); value != "" {
				t.Errorf("upstream %s survived: %q", name, value)
			}
		}
		if got := upstream.Header.Get("X-Forwarded-For"); got != "192.0.2.7" {
			t.Errorf("X-Forwarded-For = %q", got)
		}
		if got := upstream.Header.Get("X-Forwarded-Host"); got != "machine.example" {
			t.Errorf("X-Forwarded-Host = %q", got)
		}
		if got := upstream.Header.Get("X-Forwarded-Proto"); got != "https" {
			t.Errorf("X-Forwarded-Proto = %q", got)
		}
		if got := upstream.Header.Get("X-Forwarded-Prefix"); got != base {
			t.Errorf("X-Forwarded-Prefix = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream request was not received")
	}

	httpRequest := httptest.NewRequest(http.MethodGet,
		"http://machine.example/code/"+id+"/plain", nil)
	httpRequest.Host = "machine.example"
	httpRequest.RemoteAddr = "192.0.2.8:4242"
	httpRequest.Header.Set("X-Forwarded-Proto", "https")
	httpRecorder := httptest.NewRecorder()
	handler.ServeHTTP(httpRecorder, httpRequest)
	if httpRecorder.Code != http.StatusFound {
		t.Fatalf("HTTP proxy status = %d", httpRecorder.Code)
	}
	select {
	case upstream := <-received:
		if got := upstream.Header.Get("X-Forwarded-Proto"); got != "http" {
			t.Errorf("HTTP X-Forwarded-Proto = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP upstream request was not received")
	}
}

func TestReservedCookieFilteringPreservesOnlyUnrelatedCookies(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		want   []string
	}{
		{name: "secure reserved", values: []string{secureAuthenticationCookie + "=secret"}},
		{name: "insecure reserved", values: []string{insecureAuthenticationCookie + "=secret"}},
		{
			name:   "mixed and multiple fields",
			values: []string{secureAuthenticationCookie + "=one; theme=dark", insecureAuthenticationCookie + "=two; editor=value"},
			want:   []string{"theme=dark", "editor=value"},
		},
		{
			name:   "near matches survive",
			values: []string{"x" + secureAuthenticationCookie + "=one; " + insecureAuthenticationCookie + "_other=two"},
			want:   []string{"x" + secureAuthenticationCookie + "=one; " + insecureAuthenticationCookie + "_other=two"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := make(http.Header)
			for _, value := range test.values {
				header.Add("Cookie", value)
			}
			stripReservedCookie(header)
			got := header.Values("Cookie")
			if len(got) != len(test.want) {
				t.Fatalf("Cookie values = %#v, want %#v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("Cookie values = %#v, want %#v", got, test.want)
				}
			}
		})
	}
}

func TestProxyRedirectsBarePrefixAndRejectsWrongPrefix(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	handler, transport := newCodeServerProxy(filepath.Join(t.TempDir(), "missing.sock"), id)
	defer transport.CloseIdleConnections()
	base := "/code/" + id

	request := httptest.NewRequest(http.MethodGet, "https://machine.example"+base+"?a=1", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPermanentRedirect || recorder.Header().Get("Location") != base+"/?a=1" {
		t.Fatalf("redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "https://machine.example/code/wrong/", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("wrong prefix status = %d", recorder.Code)
	}
}

func TestProxyPreservesAbsoluteAndAlreadyPrefixedLocations(t *testing.T) {
	base := "/code/0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		location string
		want     string
	}{
		{"https://example.com/login", "https://example.com/login"},
		{"//example.com/login", "//example.com/login"},
		{base + "/already", base + "/already"},
		{"relative", "relative"},
	} {
		header := make(http.Header)
		header.Set("Location", test.location)
		rewriteCodeServerResponse(header, base)
		if got := header.Get("Location"); got != test.want {
			t.Errorf("Location %q rewritten to %q, want %q", test.location, got, test.want)
		}
	}
}

func TestResponseAlwaysConfinesServiceWorkerScope(t *testing.T) {
	base := "/code/0123456789abcdef0123456789abcdef"
	for _, upstream := range []string{"", "/", "/api", "/code", "garbage"} {
		header := make(http.Header)
		if upstream != "" {
			header.Set("Service-Worker-Allowed", upstream)
		}
		rewriteCodeServerResponse(header, base)
		if got := header.Get("Service-Worker-Allowed"); got != base+"/" {
			t.Errorf("upstream scope %q became %q, want %q", upstream, got, base+"/")
		}
	}
}

func TestProxySupportsWebSocketUpgradeOverUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "code.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	upgradedPath := make(chan string, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upgradedPath <- request.URL.Path
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream writer cannot hijack")
			return
		}
		connection, buffer, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			t.Error(hijackErr)
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buffer.Flush()
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()

	id := "0123456789abcdef0123456789abcdef"
	handler, transport := newCodeServerProxy(socket, id)
	defer transport.CloseIdleConnections()
	outer := httptest.NewServer(handler)
	defer outer.Close()
	address := strings.TrimPrefix(outer.URL, "http://")
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, _ = fmt.Fprintf(connection,
		"GET /code/%s/websocket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n",
		id, address)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d", response.StatusCode)
	}
	select {
	case path := <-upgradedPath:
		if path != "/websocket" {
			t.Fatalf("upgraded path = %q", path)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream upgrade was not received")
	}
}

func TestHealthProbeRequiresSuccessfulJSONObject(t *testing.T) {
	base := t.TempDir()
	for _, test := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"healthy", http.StatusOK, `{"status":"alive"}`, true},
		{"not-json", http.StatusOK, `alive`, false},
		{"array", http.StatusOK, `[]`, false},
		{"failed", http.StatusServiceUnavailable, `{"status":"starting"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket := filepath.Join(base, test.name+".sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/healthz" {
					t.Errorf("health path = %q", request.URL.Path)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})}
			go func() { _ = server.Serve(listener) }()
			manager := NewManager(Config{})
			err = manager.probeHealth(context.Background(), socket)
			_ = server.Close()
			_ = listener.Close()
			_ = os.Remove(socket)
			if (err == nil) != test.ok {
				t.Fatalf("probeHealth error = %v, ok want %t", err, test.ok)
			}
		})
	}
}
