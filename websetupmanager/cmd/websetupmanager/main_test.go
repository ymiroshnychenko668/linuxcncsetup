package main

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/config"
)

func TestNewWebServerAppliesResourceTimeouts(t *testing.T) {
	settings := config.Config{
		ListenAddress: "127.0.0.1:8080", ReadHeaderTimeout: config.DefaultReadHeaderTimeout,
		ReadTimeout: config.DefaultReadTimeout, IdleTimeout: config.DefaultIdleTimeout,
		MaxHeaderBytes: config.DefaultMaxHeaderBytes,
	}
	server := newWebServer(settings, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if server.Addr != settings.ListenAddress || server.ReadHeaderTimeout != settings.ReadHeaderTimeout ||
		server.ReadTimeout != settings.ReadTimeout || server.IdleTimeout != settings.IdleTimeout ||
		server.MaxHeaderBytes != settings.MaxHeaderBytes {
		t.Fatalf("unexpected server config: %+v", server)
	}
}

type fakeServingServer struct {
	httpCalls int
	tlsCalls  int
}

func (f *fakeServingServer) ListenAndServe() error {
	f.httpCalls++
	return http.ErrServerClosed
}

func (f *fakeServingServer) ListenAndServeTLS(string, string) error {
	f.tlsCalls++
	return http.ErrServerClosed
}

func TestServeUsesTLSOnlyWhenConfigured(t *testing.T) {
	server := &fakeServingServer{}
	if err := serve(server, config.Config{}); err != http.ErrServerClosed || server.httpCalls != 1 || server.tlsCalls != 0 {
		t.Fatalf("plain serve = %v, %+v", err, server)
	}
	server = &fakeServingServer{}
	settings := config.Config{TLSCertFile: "/configured/cert", TLSKeyFile: "/configured/key"}
	if err := serve(server, settings); err != http.ErrServerClosed || server.httpCalls != 0 || server.tlsCalls != 1 {
		t.Fatalf("TLS serve = %v, %+v", err, server)
	}
}
