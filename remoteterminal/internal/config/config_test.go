package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		ListenAddress:     "192.0.2.10:8443",
		AllowedUser:       "operator",
		MachineName:       "Workshop Mill",
		Transport:         TransportHTTPS,
		TLSCertFile:       "/etc/remoteterminal/tls.crt",
		TLSKeyFile:        "/etc/remoteterminal/tls.key",
		WebDir:            "/opt/remoteterminal/web",
		RuntimeDir:        "/run/remoteterminal",
		StateDir:          "/var/lib/remoteterminal",
		TmuxBinary:        "tmux",
		TtydBinary:        "ttyd",
		CodeServerBinary:  "code-server",
		PAMService:        "remoteterminal",
		MaxSessions:       8,
		MaxCodeServers:    2,
		IdleTimeout:       time.Minute,
		AbsoluteTimeout:   time.Hour,
		RememberTimeout:   30 * 24 * time.Hour,
		AuthConcurrency:   4,
		LoginAttempts:     5,
		LoginWindow:       time.Minute,
		SessionCapacity:   128,
		ShutdownTimeout:   time.Second,
		TerminalTimeout:   time.Second,
		CodeServerTimeout: time.Second,
	}
}

func TestValidateRejectsUnsafeListenAndPaths(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{"wildcard IPv4", func(c *Config) { c.ListenAddress = "0.0.0.0:8443" }, "unspecified"},
		{"wildcard IPv6", func(c *Config) { c.ListenAddress = "[::]:8443" }, "unspecified"},
		{"missing port", func(c *Config) { c.ListenAddress = "192.0.2.10" }, "host:port"},
		{"relative runtime", func(c *Config) { c.RuntimeDir = "run/remoteterminal" }, "absolute"},
		{"relative state", func(c *Config) { c.StateDir = "var/lib/remoteterminal" }, "absolute"},
		{"filesystem root runtime", func(c *Config) { c.RuntimeDir = "/" }, "application-specific"},
		{"broad state root", func(c *Config) { c.StateDir = "/var" }, "application-specific"},
		{"state below runtime", func(c *Config) { c.StateDir = "/run/remoteterminal/state" }, "must not overlap"},
		{"runtime below state", func(c *Config) { c.RuntimeDir = "/var/lib/remoteterminal/run" }, "must not overlap"},
		{"invalid user", func(c *Config) { c.AllowedUser = "root:other" }, "invalid"},
		{"invalid machine name", func(c *Config) { c.MachineName = "Mill #1" }, "MACHINE_NAME"},
		{"long machine name", func(c *Config) { c.MachineName = strings.Repeat("m", 65) }, "MACHINE_NAME"},
		{"idle beyond absolute", func(c *Config) { c.IdleTimeout = 2 * time.Hour }, "must not exceed"},
		{"zero remember timeout", func(c *Config) { c.RememberTimeout = 0 }, "REMEMBER_TIMEOUT"},
		{"too many sessions", func(c *Config) { c.MaxSessions = 65 }, "between 1 and 64"},
		{"too many code servers", func(c *Config) { c.MaxCodeServers = 9 }, "between 1 and 8"},
		{"invalid transport", func(c *Config) { c.Transport = "ftp" }, "TRANSPORT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := validConfig()
			test.change(&configuration)
			if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateTransportTLSContract(t *testing.T) {
	tests := []struct {
		name      string
		transport Transport
		cert      string
		key       string
		wantError string
	}{
		{name: "https pair", transport: TransportHTTPS, cert: "/tmp/cert", key: "/tmp/key"},
		{name: "https missing certificate", transport: TransportHTTPS, key: "/tmp/key", wantError: "TLS_CERT_FILE"},
		{name: "https missing key", transport: TransportHTTPS, cert: "/tmp/cert", wantError: "TLS_KEY_FILE"},
		{name: "http without TLS", transport: TransportHTTP},
		{name: "http rejects certificate", transport: TransportHTTP, cert: "/tmp/cert", wantError: "must be empty"},
		{name: "http rejects key", transport: TransportHTTP, key: "/tmp/key", wantError: "must be empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := validConfig()
			configuration.Transport = test.transport
			configuration.TLSCertFile = test.cert
			configuration.TLSKeyFile = test.key
			err := configuration.Validate()
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestLoadReadsDurationsAndLimits(t *testing.T) {
	for name, value := range map[string]string{
		"REMOTE_TERMINAL_LISTEN_ADDRESS":   "192.0.2.20:9443",
		"REMOTE_TERMINAL_ALLOWED_USER":     "machine",
		"REMOTE_TERMINAL_MACHINE_NAME":     "Main CNC Mill",
		"REMOTE_TERMINAL_TRANSPORT":        "https",
		"REMOTE_TERMINAL_TLS_CERT_FILE":    "/tmp/cert",
		"REMOTE_TERMINAL_TLS_KEY_FILE":     "/tmp/key",
		"REMOTE_TERMINAL_WEB_DIR":          "/tmp/web",
		"REMOTE_TERMINAL_MAX_SESSIONS":     "12",
		"REMOTE_TERMINAL_MAX_CODE_SERVERS": "3",
		"REMOTE_TERMINAL_IDLE_TIMEOUT":     "20m",
		"REMOTE_TERMINAL_ABSOLUTE_TIMEOUT": "4h",
		"REMOTE_TERMINAL_REMEMBER_TIMEOUT": "720h",
	} {
		t.Setenv(name, value)
	}
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.MachineName != "Main CNC Mill" || configuration.MaxSessions != 12 || configuration.MaxCodeServers != 3 ||
		configuration.IdleTimeout != 20*time.Minute || configuration.AbsoluteTimeout != 4*time.Hour ||
		configuration.RememberTimeout != 30*24*time.Hour ||
		configuration.Transport != TransportHTTPS {
		t.Fatalf("unexpected loaded config: %+v", configuration)
	}
}

func TestLoadReadsExplicitHTTPTransportWithoutTLSFiles(t *testing.T) {
	t.Setenv("REMOTE_TERMINAL_TLS_CERT_FILE", "")
	t.Setenv("REMOTE_TERMINAL_TLS_KEY_FILE", "")
	for name, value := range map[string]string{
		"REMOTE_TERMINAL_LISTEN_ADDRESS": "192.0.2.20:8080",
		"REMOTE_TERMINAL_ALLOWED_USER":   "machine",
		"REMOTE_TERMINAL_MACHINE_NAME":   "Main CNC Mill",
		"REMOTE_TERMINAL_TRANSPORT":      "http",
		"REMOTE_TERMINAL_WEB_DIR":        "/tmp/web",
	} {
		t.Setenv(name, value)
	}
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Transport != TransportHTTP || configuration.TLSCertFile != "" || configuration.TLSKeyFile != "" {
		t.Fatalf("unexpected loaded HTTP config: %+v", configuration)
	}
}

func TestValidateFiles(t *testing.T) {
	directory := t.TempDir()
	cert := filepath.Join(directory, "cert")
	key := filepath.Join(directory, "key")
	web := filepath.Join(directory, "web")
	bin := filepath.Join(directory, "bin")
	if err := os.MkdirAll(web, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cert, []byte("cert"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	configuration := validConfig()
	configuration.TLSCertFile = cert
	configuration.TLSKeyFile = key
	configuration.WebDir = web
	configuration.TmuxBinary = bin
	configuration.TtydBinary = bin
	configuration.CodeServerBinary = bin
	if err := configuration.ValidateFiles(); err != nil {
		t.Fatal(err)
	}

	configuration.Transport = TransportHTTP
	configuration.TLSCertFile = ""
	configuration.TLSKeyFile = ""
	if err := configuration.ValidateFiles(); err != nil {
		t.Fatalf("ValidateFiles() in HTTP mode inspected TLS files: %v", err)
	}
}
