// Package config loads and validates the service's environment-based
// configuration. Keeping configuration in an EnvironmentFile makes the
// systemd installation straightforward and avoids a second configuration
// parser in the privileged installer.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultRuntimeDir      = "/run/remoteterminal"
	DefaultMaxSessions     = 8
	DefaultIdleTimeout     = 30 * time.Minute
	DefaultAbsoluteTimeout = 12 * time.Hour
	DefaultAuthConcurrency = 4
	DefaultLoginAttempts   = 5
	DefaultLoginWindow     = 10 * time.Minute
	DefaultSessionCapacity = 128
)

// Config contains all runtime settings. Secrets are deliberately absent: the
// user's password is accepted only in a login request and immediately handed
// to PAM.
type Config struct {
	ListenAddress   string
	AllowedUser     string
	MachineName     string
	TLSCertFile     string
	TLSKeyFile      string
	WebDir          string
	RuntimeDir      string
	TmuxBinary      string
	TtydBinary      string
	PAMService      string
	MaxSessions     int
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	AuthConcurrency int
	LoginAttempts   int
	LoginWindow     time.Duration
	SessionCapacity int
	ShutdownTimeout time.Duration
	TerminalTimeout time.Duration
}

// Load reads Config from the process environment and validates values that do
// not depend on filesystem state.
func Load() (Config, error) {
	c := Config{
		ListenAddress:   strings.TrimSpace(os.Getenv("REMOTE_TERMINAL_LISTEN_ADDRESS")),
		AllowedUser:     strings.TrimSpace(os.Getenv("REMOTE_TERMINAL_ALLOWED_USER")),
		MachineName:     strings.TrimSpace(os.Getenv("REMOTE_TERMINAL_MACHINE_NAME")),
		TLSCertFile:     strings.TrimSpace(os.Getenv("REMOTE_TERMINAL_TLS_CERT_FILE")),
		TLSKeyFile:      strings.TrimSpace(os.Getenv("REMOTE_TERMINAL_TLS_KEY_FILE")),
		WebDir:          strings.TrimSpace(os.Getenv("REMOTE_TERMINAL_WEB_DIR")),
		RuntimeDir:      envOr("REMOTE_TERMINAL_RUNTIME_DIR", DefaultRuntimeDir),
		TmuxBinary:      envOr("REMOTE_TERMINAL_TMUX_BINARY", "tmux"),
		TtydBinary:      envOr("REMOTE_TERMINAL_TTYD_BINARY", "ttyd"),
		PAMService:      envOr("REMOTE_TERMINAL_PAM_SERVICE", "remoteterminal"),
		MaxSessions:     DefaultMaxSessions,
		IdleTimeout:     DefaultIdleTimeout,
		AbsoluteTimeout: DefaultAbsoluteTimeout,
		AuthConcurrency: DefaultAuthConcurrency,
		LoginAttempts:   DefaultLoginAttempts,
		LoginWindow:     DefaultLoginWindow,
		SessionCapacity: DefaultSessionCapacity,
		ShutdownTimeout: 10 * time.Second,
		TerminalTimeout: 5 * time.Second,
	}

	var err error
	if c.MaxSessions, err = envInt("REMOTE_TERMINAL_MAX_SESSIONS", c.MaxSessions); err != nil {
		return Config{}, err
	}
	if c.AuthConcurrency, err = envInt("REMOTE_TERMINAL_AUTH_CONCURRENCY", c.AuthConcurrency); err != nil {
		return Config{}, err
	}
	if c.LoginAttempts, err = envInt("REMOTE_TERMINAL_LOGIN_ATTEMPTS", c.LoginAttempts); err != nil {
		return Config{}, err
	}
	if c.SessionCapacity, err = envInt("REMOTE_TERMINAL_AUTH_SESSION_CAPACITY", c.SessionCapacity); err != nil {
		return Config{}, err
	}
	if c.IdleTimeout, err = envDuration("REMOTE_TERMINAL_IDLE_TIMEOUT", c.IdleTimeout); err != nil {
		return Config{}, err
	}
	if c.AbsoluteTimeout, err = envDuration("REMOTE_TERMINAL_ABSOLUTE_TIMEOUT", c.AbsoluteTimeout); err != nil {
		return Config{}, err
	}
	if c.LoginWindow, err = envDuration("REMOTE_TERMINAL_LOGIN_WINDOW", c.LoginWindow); err != nil {
		return Config{}, err
	}
	if c.ShutdownTimeout, err = envDuration("REMOTE_TERMINAL_SHUTDOWN_TIMEOUT", c.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if c.TerminalTimeout, err = envDuration("REMOTE_TERMINAL_START_TIMEOUT", c.TerminalTimeout); err != nil {
		return Config{}, err
	}

	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks syntax and security-sensitive invariants. ValidateFiles
// performs the checks that require an installed deployment tree.
func (c Config) Validate() error {
	var missing []string
	for name, value := range map[string]string{
		"REMOTE_TERMINAL_LISTEN_ADDRESS": c.ListenAddress,
		"REMOTE_TERMINAL_ALLOWED_USER":   c.AllowedUser,
		"REMOTE_TERMINAL_MACHINE_NAME":   c.MachineName,
		"REMOTE_TERMINAL_TLS_CERT_FILE":  c.TLSCertFile,
		"REMOTE_TERMINAL_TLS_KEY_FILE":   c.TLSKeyFile,
		"REMOTE_TERMINAL_WEB_DIR":        c.WebDir,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("missing required environment setting(s): %s", strings.Join(missing, ", "))
	}

	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("REMOTE_TERMINAL_LISTEN_ADDRESS must be a specific host:port: %w", err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip != nil && ip.IsUnspecified() {
		return errors.New("REMOTE_TERMINAL_LISTEN_ADDRESS must not use an unspecified address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("REMOTE_TERMINAL_LISTEN_ADDRESS contains an invalid port")
	}
	if strings.ContainsAny(c.AllowedUser, "\x00\r\n:") {
		return errors.New("REMOTE_TERMINAL_ALLOWED_USER contains invalid characters")
	}
	if !validMachineName(c.MachineName) {
		return errors.New("REMOTE_TERMINAL_MACHINE_NAME must be 1-64 characters and use letters, numbers, spaces, dots, underscores, or hyphens")
	}
	if strings.ContainsAny(c.PAMService, "\x00/\\\r\n") || c.PAMService == "" {
		return errors.New("REMOTE_TERMINAL_PAM_SERVICE is invalid")
	}
	for name, value := range map[string]string{
		"REMOTE_TERMINAL_TLS_CERT_FILE": c.TLSCertFile,
		"REMOTE_TERMINAL_TLS_KEY_FILE":  c.TLSKeyFile,
		"REMOTE_TERMINAL_WEB_DIR":       c.WebDir,
		"REMOTE_TERMINAL_RUNTIME_DIR":   c.RuntimeDir,
	} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if c.MaxSessions < 1 || c.MaxSessions > 64 {
		return errors.New("REMOTE_TERMINAL_MAX_SESSIONS must be between 1 and 64")
	}
	if c.AuthConcurrency < 1 || c.AuthConcurrency > 64 {
		return errors.New("REMOTE_TERMINAL_AUTH_CONCURRENCY must be between 1 and 64")
	}
	if c.LoginAttempts < 1 || c.LoginAttempts > 100 {
		return errors.New("REMOTE_TERMINAL_LOGIN_ATTEMPTS must be between 1 and 100")
	}
	if c.SessionCapacity < 1 || c.SessionCapacity > 10000 {
		return errors.New("REMOTE_TERMINAL_AUTH_SESSION_CAPACITY must be between 1 and 10000")
	}
	for name, value := range map[string]time.Duration{
		"REMOTE_TERMINAL_IDLE_TIMEOUT":     c.IdleTimeout,
		"REMOTE_TERMINAL_ABSOLUTE_TIMEOUT": c.AbsoluteTimeout,
		"REMOTE_TERMINAL_LOGIN_WINDOW":     c.LoginWindow,
		"REMOTE_TERMINAL_SHUTDOWN_TIMEOUT": c.ShutdownTimeout,
		"REMOTE_TERMINAL_START_TIMEOUT":    c.TerminalTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if c.IdleTimeout > c.AbsoluteTimeout {
		return errors.New("REMOTE_TERMINAL_IDLE_TIMEOUT must not exceed REMOTE_TERMINAL_ABSOLUTE_TIMEOUT")
	}
	return nil
}

func validMachineName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		letterOrNumber := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if index == 0 {
			if !letterOrNumber {
				return false
			}
			continue
		}
		if !letterOrNumber && character != ' ' && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// ValidateFiles confirms that deployment artifacts are readable and binaries
// can be found. It should be called before opening the network listener.
func (c Config) ValidateFiles() error {
	for name, path := range map[string]string{
		"TLS certificate": c.TLSCertFile,
		"TLS private key": c.TLSKeyFile,
	} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s is not readable: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
	}
	info, err := os.Stat(c.WebDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("web directory is not usable: %w", err)
	}
	if _, err := os.Stat(filepath.Join(c.WebDir, "index.html")); err != nil {
		return fmt.Errorf("web directory has no index.html: %w", err)
	}
	for name, binary := range map[string]string{"tmux": c.TmuxBinary, "ttyd": c.TtydBinary} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("%s executable is unavailable: %w", name, err)
		}
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return value, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration: %w", name, err)
	}
	return value, nil
}
