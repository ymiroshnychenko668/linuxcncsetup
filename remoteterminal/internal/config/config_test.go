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
		ListenAddress:   "192.0.2.10:8443",
		AllowedUser:     "operator",
		MachineName:     "Workshop Mill",
		TLSCertFile:     "/etc/remoteterminal/tls.crt",
		TLSKeyFile:      "/etc/remoteterminal/tls.key",
		WebDir:          "/opt/remoteterminal/web",
		RuntimeDir:      "/run/remoteterminal",
		TmuxBinary:      "tmux",
		TtydBinary:      "ttyd",
		PAMService:      "remoteterminal",
		MaxSessions:     8,
		IdleTimeout:     time.Minute,
		AbsoluteTimeout: time.Hour,
		AuthConcurrency: 4,
		LoginAttempts:   5,
		LoginWindow:     time.Minute,
		SessionCapacity: 128,
		ShutdownTimeout: time.Second,
		TerminalTimeout: time.Second,
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
		{"invalid user", func(c *Config) { c.AllowedUser = "root:other" }, "invalid"},
		{"invalid machine name", func(c *Config) { c.MachineName = "Mill #1" }, "MACHINE_NAME"},
		{"long machine name", func(c *Config) { c.MachineName = strings.Repeat("m", 65) }, "MACHINE_NAME"},
		{"idle beyond absolute", func(c *Config) { c.IdleTimeout = 2 * time.Hour }, "must not exceed"},
		{"too many sessions", func(c *Config) { c.MaxSessions = 65 }, "between 1 and 64"},
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

func TestLoadReadsDurationsAndLimits(t *testing.T) {
	for name, value := range map[string]string{
		"REMOTE_TERMINAL_LISTEN_ADDRESS":   "192.0.2.20:9443",
		"REMOTE_TERMINAL_ALLOWED_USER":     "machine",
		"REMOTE_TERMINAL_MACHINE_NAME":     "Main CNC Mill",
		"REMOTE_TERMINAL_TLS_CERT_FILE":    "/tmp/cert",
		"REMOTE_TERMINAL_TLS_KEY_FILE":     "/tmp/key",
		"REMOTE_TERMINAL_WEB_DIR":          "/tmp/web",
		"REMOTE_TERMINAL_MAX_SESSIONS":     "12",
		"REMOTE_TERMINAL_IDLE_TIMEOUT":     "20m",
		"REMOTE_TERMINAL_ABSOLUTE_TIMEOUT": "4h",
	} {
		t.Setenv(name, value)
	}
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.MachineName != "Main CNC Mill" || configuration.MaxSessions != 12 ||
		configuration.IdleTimeout != 20*time.Minute || configuration.AbsoluteTimeout != 4*time.Hour {
		t.Fatalf("unexpected loaded config: %+v", configuration)
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
	if err := configuration.ValidateFiles(); err != nil {
		t.Fatal(err)
	}
}
