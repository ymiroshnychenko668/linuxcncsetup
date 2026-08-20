// Package config loads and validates Web Setup Manager runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListenAddress       = "127.0.0.1:8080"
	DefaultLibraryAlias        = "Сетапы"
	DefaultRecentSetupsLimit   = 30
	DefaultMaxParallelJobs     = 2
	DefaultShutdownTimeout     = 15 * time.Second
	DefaultReadHeaderTimeout   = 10 * time.Second
	DefaultReadTimeout         = 30 * time.Second
	DefaultIdleTimeout         = 2 * time.Minute
	DefaultMaxHeaderBytes      = 16 << 10
	DefaultArtifactFileMode    = 0o640
	DefaultIdempotencyTTL      = 24 * time.Hour
	DefaultDeleteConfirmation  = 5 * time.Minute
	DefaultReconcileInterval   = time.Minute
	DefaultImportSessionExpiry = 24 * time.Hour
)

var defaultGCodeExtensions = []string{".gcode", ".nc", ".ngc", ".tap", ".cnc"}

// Config contains only startup-controlled settings. Physical storage settings
// are never writable through the public HTTP API.
type Config struct {
	LibraryDir                string
	StateDir                  string
	ListenAddress             string
	LibraryAlias              string
	GCodeExtensions           []string
	RecentSetupsLimit         int
	MaxParallelHeavyJobs      int
	ArtifactUploadLimit       int64
	ImportTotalLimit          int64
	RequireSetupSheetForReady bool
	ArtifactFileMode          os.FileMode
	ShutdownTimeout           time.Duration
	ReadHeaderTimeout         time.Duration
	ReadTimeout               time.Duration
	IdleTimeout               time.Duration
	MaxHeaderBytes            int
	IdempotencyTTL            time.Duration
	DeleteConfirmationTTL     time.Duration
	ReconcileInterval         time.Duration
	ImportSessionExpiry       time.Duration
	RemoteAccess              bool
	RemoteAuthToken           string
	TLSCertFile               string
	TLSKeyFile                string
	TrustedTLSProxy           bool
}

// Load reads WEB_SETUP_MANAGER_* environment variables and validates syntax.
func Load() (Config, error) {
	stateDir, err := defaultStateDir()
	if err != nil {
		return Config{}, errors.New("determine default state directory")
	}
	c := Config{
		LibraryDir:            strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_LIBRARY_DIR")),
		StateDir:              envOr("WEB_SETUP_MANAGER_STATE_DIR", stateDir),
		ListenAddress:         envOr("WEB_SETUP_MANAGER_LISTEN_ADDRESS", DefaultListenAddress),
		LibraryAlias:          envOr("WEB_SETUP_MANAGER_LIBRARY_ALIAS", DefaultLibraryAlias),
		GCodeExtensions:       append([]string(nil), defaultGCodeExtensions...),
		RecentSetupsLimit:     DefaultRecentSetupsLimit,
		MaxParallelHeavyJobs:  DefaultMaxParallelJobs,
		ArtifactFileMode:      DefaultArtifactFileMode,
		ShutdownTimeout:       DefaultShutdownTimeout,
		ReadHeaderTimeout:     DefaultReadHeaderTimeout,
		ReadTimeout:           DefaultReadTimeout,
		IdleTimeout:           DefaultIdleTimeout,
		MaxHeaderBytes:        DefaultMaxHeaderBytes,
		IdempotencyTTL:        DefaultIdempotencyTTL,
		DeleteConfirmationTTL: DefaultDeleteConfirmation,
		ReconcileInterval:     DefaultReconcileInterval,
		ImportSessionExpiry:   DefaultImportSessionExpiry,
		RemoteAuthToken:       strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_REMOTE_AUTH_TOKEN")),
		TLSCertFile:           strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_TLS_CERT_FILE")),
		TLSKeyFile:            strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_TLS_KEY_FILE")),
	}
	if raw := strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_GCODE_EXTENSIONS")); raw != "" {
		c.GCodeExtensions = strings.Split(raw, ",")
	}
	if c.RecentSetupsLimit, err = envInt("WEB_SETUP_MANAGER_RECENT_SETUPS_LIMIT", c.RecentSetupsLimit); err != nil {
		return Config{}, err
	}
	if c.MaxParallelHeavyJobs, err = envInt("WEB_SETUP_MANAGER_MAX_PARALLEL_HEAVY_JOBS", c.MaxParallelHeavyJobs); err != nil {
		return Config{}, err
	}
	if c.MaxHeaderBytes, err = envInt("WEB_SETUP_MANAGER_MAX_HEADER_BYTES", c.MaxHeaderBytes); err != nil {
		return Config{}, err
	}
	if c.ArtifactUploadLimit, err = envBytes("WEB_SETUP_MANAGER_ARTIFACT_UPLOAD_LIMIT", 0); err != nil {
		return Config{}, err
	}
	if c.ImportTotalLimit, err = envBytes("WEB_SETUP_MANAGER_IMPORT_TOTAL_LIMIT", 0); err != nil {
		return Config{}, err
	}
	if c.RequireSetupSheetForReady, err = envBool("WEB_SETUP_MANAGER_REQUIRE_SETUP_SHEET_FOR_READY", false); err != nil {
		return Config{}, err
	}
	if c.RemoteAccess, err = envBool("WEB_SETUP_MANAGER_REMOTE_ACCESS", false); err != nil {
		return Config{}, err
	}
	if c.TrustedTLSProxy, err = envBool("WEB_SETUP_MANAGER_TRUSTED_TLS_PROXY", false); err != nil {
		return Config{}, err
	}
	if c.ArtifactFileMode, err = envFileMode("WEB_SETUP_MANAGER_ARTIFACT_FILE_MODE", c.ArtifactFileMode); err != nil {
		return Config{}, err
	}
	for name, target := range map[string]*time.Duration{
		"WEB_SETUP_MANAGER_SHUTDOWN_TIMEOUT":        &c.ShutdownTimeout,
		"WEB_SETUP_MANAGER_READ_HEADER_TIMEOUT":     &c.ReadHeaderTimeout,
		"WEB_SETUP_MANAGER_READ_TIMEOUT":            &c.ReadTimeout,
		"WEB_SETUP_MANAGER_IDLE_TIMEOUT":            &c.IdleTimeout,
		"WEB_SETUP_MANAGER_IDEMPOTENCY_TTL":         &c.IdempotencyTTL,
		"WEB_SETUP_MANAGER_DELETE_CONFIRMATION_TTL": &c.DeleteConfirmationTTL,
		"WEB_SETUP_MANAGER_RECONCILE_INTERVAL":      &c.ReconcileInterval,
		"WEB_SETUP_MANAGER_IMPORT_SESSION_EXPIRY":   &c.ImportSessionExpiry,
	} {
		if *target, err = envDuration(name, *target); err != nil {
			return Config{}, err
		}
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks all security-sensitive invariants not requiring filesystem IO.
func (c *Config) Validate() error {
	if c.LibraryDir == "" {
		return errors.New("WEB_SETUP_MANAGER_LIBRARY_DIR is required")
	}
	for name, value := range map[string]string{
		"WEB_SETUP_MANAGER_LIBRARY_DIR": c.LibraryDir,
		"WEB_SETUP_MANAGER_STATE_DIR":   c.StateDir,
	} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be absolute", name)
		}
	}
	if strings.TrimSpace(c.LibraryAlias) == "" || len([]rune(c.LibraryAlias)) > 100 {
		return errors.New("WEB_SETUP_MANAGER_LIBRARY_ALIAS must contain 1-100 characters")
	}
	if c.RecentSetupsLimit < 1 || c.RecentSetupsLimit > 1000 {
		return errors.New("WEB_SETUP_MANAGER_RECENT_SETUPS_LIMIT must be between 1 and 1000")
	}
	if c.MaxParallelHeavyJobs < 1 || c.MaxParallelHeavyJobs > 16 {
		return errors.New("WEB_SETUP_MANAGER_MAX_PARALLEL_HEAVY_JOBS must be between 1 and 16")
	}
	if c.ArtifactUploadLimit < 0 || c.ImportTotalLimit < 0 {
		return errors.New("upload limits cannot be negative")
	}
	if c.ArtifactFileMode&0o111 != 0 || c.ArtifactFileMode.Perm() == 0 {
		return errors.New("WEB_SETUP_MANAGER_ARTIFACT_FILE_MODE must be non-executable and non-zero")
	}
	if c.MaxHeaderBytes < 8<<10 || c.MaxHeaderBytes > 1<<20 {
		return errors.New("WEB_SETUP_MANAGER_MAX_HEADER_BYTES must be between 8192 and 1048576")
	}
	for name, value := range map[string]time.Duration{
		"shutdown timeout": c.ShutdownTimeout, "read header timeout": c.ReadHeaderTimeout,
		"read timeout": c.ReadTimeout, "idle timeout": c.IdleTimeout,
		"idempotency TTL": c.IdempotencyTTL, "delete confirmation TTL": c.DeleteConfirmationTTL,
		"reconcile interval": c.ReconcileInterval, "import session expiry": c.ImportSessionExpiry,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	host, _, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return errors.New("WEB_SETUP_MANAGER_LISTEN_ADDRESS must be host:port")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	isLoopback := ip != nil && ip.IsLoopback()
	if host == "localhost" {
		isLoopback = true
	}
	if !isLoopback {
		if !c.RemoteAccess {
			return errors.New("non-loopback listen requires WEB_SETUP_MANAGER_REMOTE_ACCESS=true")
		}
		if len(c.RemoteAuthToken) < 32 {
			return errors.New("remote access requires an authentication token of at least 32 characters")
		}
		if !c.TrustedTLSProxy && (c.TLSCertFile == "" || c.TLSKeyFile == "") {
			return errors.New("remote access requires TLS certificate/key or a trusted TLS proxy")
		}
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("TLS certificate and key must be configured together")
	}
	seen := make(map[string]struct{}, len(c.GCodeExtensions))
	for i, extension := range c.GCodeExtensions {
		extension = strings.ToLower(strings.TrimSpace(extension))
		if !validExtension(extension) {
			return errors.New("G-code extensions must be dot-prefixed ASCII alphanumeric values")
		}
		if _, duplicate := seen[extension]; duplicate {
			return errors.New("G-code extensions must be unique")
		}
		seen[extension] = struct{}{}
		c.GCodeExtensions[i] = extension
	}
	return nil
}

// ValidateRoots canonicalizes and verifies storage roots without widening them.
// The state directory may be created because it is service-owned; the library
// must already exist and is never silently replaced by a parent directory.
func (c *Config) ValidateRoots() error {
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return errors.New("state directory is unavailable")
	}
	library, err := canonicalDirectory(c.LibraryDir)
	if err != nil {
		return errors.New("library directory is unavailable")
	}
	state, err := canonicalDirectory(c.StateDir)
	if err != nil {
		return errors.New("state directory is unavailable")
	}
	if pathsOverlap(library, state) {
		return errors.New("library and state directories must be disjoint")
	}
	c.LibraryDir = library
	c.StateDir = state
	return nil
}

// ValidateFiles checks optional TLS material without returning host paths in
// errors. Storage contents are validated by the fd-relative storage package.
func (c Config) ValidateFiles() error {
	if c.TLSCertFile == "" {
		return nil
	}
	for name, filename := range map[string]string{
		"TLS certificate": c.TLSCertFile,
		"TLS private key": c.TLSKeyFile,
	} {
		if !filepath.IsAbs(filename) {
			return fmt.Errorf("%s must be an absolute regular file", name)
		}
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is unavailable", name)
		}
		file, err := os.Open(filename)
		if err != nil {
			return fmt.Errorf("%s is unreadable", name)
		}
		_ = file.Close()
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	cleaned, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(cleaned)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("not a real directory")
	}
	return filepath.Abs(cleaned)
}

func pathsOverlap(first, second string) bool {
	if first == second {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(first, second+separator) || strings.HasPrefix(second, first+separator)
}

func validExtension(value string) bool {
	if len(value) < 2 || value[0] != '.' {
		return false
	}
	for _, character := range value[1:] {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func defaultStateDir() (string, error) {
	if base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); base != "" {
		if !filepath.IsAbs(base) {
			return "", errors.New("XDG_STATE_HOME must be absolute")
		}
		return filepath.Join(base, "websetupmanager"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("home directory is unavailable")
	}
	return filepath.Join(home, ".local", "state", "websetupmanager"), nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func envBytes(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a byte count", name)
	}
	return parsed, nil
}

func envBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration", name)
	}
	return parsed, nil
}

func envFileMode(name string, fallback os.FileMode) (os.FileMode, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 8, 12)
	if err != nil {
		return 0, fmt.Errorf("%s must be an octal file mode", name)
	}
	return os.FileMode(parsed), nil
}
