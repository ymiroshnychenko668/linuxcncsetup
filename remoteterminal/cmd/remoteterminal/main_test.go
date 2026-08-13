package main

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net/http"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/remoteterminal/internal/config"
)

type fakeServingServer struct {
	plainCalls int
	tlsCalls   int
	certFile   string
	keyFile    string
	err        error
}

func (f *fakeServingServer) ListenAndServe() error {
	f.plainCalls++
	return f.err
}

func (f *fakeServingServer) ListenAndServeTLS(certFile, keyFile string) error {
	f.tlsCalls++
	f.certFile = certFile
	f.keyFile = keyFile
	return f.err
}

func TestServeConfiguredSelectsTransport(t *testing.T) {
	sentinel := errors.New("listener stopped")
	for _, test := range []struct {
		name       string
		transport  config.Transport
		plainCalls int
		tlsCalls   int
	}{
		{name: "https", transport: config.TransportHTTPS, tlsCalls: 1},
		{name: "http", transport: config.TransportHTTP, plainCalls: 1},
		{name: "zero value fails secure", tlsCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &fakeServingServer{err: sentinel}
			settings := config.Config{
				Transport:   test.transport,
				TLSCertFile: "/cert.pem",
				TLSKeyFile:  "/key.pem",
			}
			if err := serveConfigured(server, settings); !errors.Is(err, sentinel) {
				t.Fatalf("serveConfigured() error = %v", err)
			}
			if server.plainCalls != test.plainCalls || server.tlsCalls != test.tlsCalls {
				t.Fatalf("listener calls = plain %d, TLS %d", server.plainCalls, server.tlsCalls)
			}
			if test.tlsCalls == 1 && (server.certFile != settings.TLSCertFile || server.keyFile != settings.TLSKeyFile) {
				t.Fatalf("TLS files = %q, %q", server.certFile, server.keyFile)
			}
		})
	}
}

func TestNewWebServerConfiguresTLSOnlyForSecureTransport(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	httpsServer := newWebServer(config.Config{Transport: config.TransportHTTPS}, handler, logger)
	if httpsServer.TLSConfig == nil || httpsServer.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("HTTPS TLS config = %+v", httpsServer.TLSConfig)
	}
	httpServer := newWebServer(config.Config{Transport: config.TransportHTTP}, handler, logger)
	if httpServer.TLSConfig != nil {
		t.Fatalf("HTTP TLS config = %+v, want nil", httpServer.TLSConfig)
	}
}
