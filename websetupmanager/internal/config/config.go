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
	DefaultAuthIdleTimeout     = 30 * time.Minute
	DefaultAuthAbsoluteTimeout = 12 * time.Hour
	DefaultAuthRememberTimeout = 30 * 24 * time.Hour
	DefaultAuthConcurrency     = 4
	DefaultLoginAttempts       = 5
	DefaultLoginWindow         = 10 * time.Minute
	DefaultAuthSessionCapacity = 128
	DefaultPAMService          = "websetupmanager"
	DefaultProgramRootDisplay  = "~/linuxcnc/nc_files"
)

var defaultGCodeExtensions = []string{".ngc", ".nc", ".tap"}

var safeGCodeExtensions = map[string]struct{}{
	".ngc": {}, ".nc": {}, ".tap": {},
}

// Config contains only startup-controlled settings. Physical storage settings
// are never writable through the public HTTP API.
type Config struct {
	LibraryDir                string
	StateDir                  string
	ProgramRoot               string
	LinuxCNCINI               string
	ProgramRootDisplay        string
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
	AllowedUser               string
	PAMService                string
	AuthIdleTimeout           time.Duration
	AuthAbsoluteTimeout       time.Duration
	AuthRememberTimeout       time.Duration
	AuthConcurrency           int
	LoginAttempts             int
	LoginWindow               time.Duration
	AuthSessionCapacity       int
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
		ProgramRoot:           strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_PROGRAM_ROOT")),
		LinuxCNCINI:           strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_LINUXCNC_INI")),
		ProgramRootDisplay:    envOr("WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY", DefaultProgramRootDisplay),
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
		PAMService:            envOr("WEB_SETUP_MANAGER_PAM_SERVICE", DefaultPAMService),
		AuthIdleTimeout:       DefaultAuthIdleTimeout,
		AuthAbsoluteTimeout:   DefaultAuthAbsoluteTimeout,
		AuthRememberTimeout:   DefaultAuthRememberTimeout,
		AuthConcurrency:       DefaultAuthConcurrency,
		LoginAttempts:         DefaultLoginAttempts,
		LoginWindow:           DefaultLoginWindow,
		AuthSessionCapacity:   DefaultAuthSessionCapacity,
		AllowedUser:           strings.TrimSpace(os.Getenv("WEB_SETUP_MANAGER_ALLOWED_USER")),
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
	if c.AuthConcurrency, err = envInt("WEB_SETUP_MANAGER_AUTH_CONCURRENCY", c.AuthConcurrency); err != nil {
		return Config{}, err
	}
	if c.LoginAttempts, err = envInt("WEB_SETUP_MANAGER_LOGIN_ATTEMPTS", c.LoginAttempts); err != nil {
		return Config{}, err
	}
	if c.AuthSessionCapacity, err = envInt("WEB_SETUP_MANAGER_AUTH_SESSION_CAPACITY", c.AuthSessionCapacity); err != nil {
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
		"WEB_SETUP_MANAGER_AUTH_IDLE_TIMEOUT":       &c.AuthIdleTimeout,
		"WEB_SETUP_MANAGER_AUTH_ABSOLUTE_TIMEOUT":   &c.AuthAbsoluteTimeout,
		"WEB_SETUP_MANAGER_AUTH_REMEMBER_TIMEOUT":   &c.AuthRememberTimeout,
		"WEB_SETUP_MANAGER_LOGIN_WINDOW":            &c.LoginWindow,
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
	for _, item := range []struct{ name, value string }{
		{"WEB_SETUP_MANAGER_LIBRARY_DIR", c.LibraryDir},
		{"WEB_SETUP_MANAGER_STATE_DIR", c.StateDir},
		{"WEB_SETUP_MANAGER_PROGRAM_ROOT", c.ProgramRoot},
		{"WEB_SETUP_MANAGER_LINUXCNC_INI", c.LinuxCNCINI},
	} {
		name, value := item.name, item.value
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be absolute", name)
		}
	}
	c.ProgramRootDisplay = strings.TrimSpace(c.ProgramRootDisplay)
	if !validPublicRootDisplay(c.ProgramRootDisplay) {
		return errors.New("WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY must be a safe relative display hint of 1-200 characters")
	}
	c.LibraryAlias = strings.TrimSpace(c.LibraryAlias)
	if !validPublicLabel(c.LibraryAlias) {
		return errors.New("WEB_SETUP_MANAGER_LIBRARY_ALIAS must be a safe public label of 1-100 characters")
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
	if c.ArtifactFileMode&0o111 != 0 || c.ArtifactFileMode&0o022 != 0 || c.ArtifactFileMode&0o400 == 0 {
		return errors.New("WEB_SETUP_MANAGER_ARTIFACT_FILE_MODE must be owner-readable, non-executable and non-writable by group/others")
	}
	if c.MaxHeaderBytes < 8<<10 || c.MaxHeaderBytes > 1<<20 {
		return errors.New("WEB_SETUP_MANAGER_MAX_HEADER_BYTES must be between 8192 and 1048576")
	}
	for name, value := range map[string]time.Duration{
		"shutdown timeout": c.ShutdownTimeout, "read header timeout": c.ReadHeaderTimeout,
		"read timeout": c.ReadTimeout, "idle timeout": c.IdleTimeout,
		"idempotency TTL": c.IdempotencyTTL, "delete confirmation TTL": c.DeleteConfirmationTTL,
		"reconcile interval": c.ReconcileInterval, "import session expiry": c.ImportSessionExpiry,
		"authentication idle timeout": c.AuthIdleTimeout, "authentication absolute timeout": c.AuthAbsoluteTimeout,
		"authentication remember timeout": c.AuthRememberTimeout, "authentication login window": c.LoginWindow,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return errors.New("WEB_SETUP_MANAGER_LISTEN_ADDRESS must be host:port")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return errors.New("WEB_SETUP_MANAGER_LISTEN_ADDRESS port must be between 1 and 65535")
	}
	// Keep downstream bind and same-origin authority checks on one canonical
	// numeric representation (for example, :080 becomes :80).
	c.ListenAddress = net.JoinHostPort(host, strconv.FormatUint(portNumber, 10))
	ip := net.ParseIP(strings.Trim(host, "[]"))
	isLoopback := ip != nil && ip.IsLoopback()
	if host == "localhost" {
		isLoopback = true
	}
	if !isLoopback {
		if !c.RemoteAccess {
			return errors.New("non-loopback listen requires WEB_SETUP_MANAGER_REMOTE_ACCESS=true")
		}
	}
	if c.RemoteAccess {
		if !validAccountName(c.AllowedUser) || c.AllowedUser == "root" {
			return errors.New("remote access requires a valid non-root WEB_SETUP_MANAGER_ALLOWED_USER")
		}
		if !c.TrustedTLSProxy && (c.TLSCertFile == "" || c.TLSKeyFile == "") {
			return errors.New("remote access requires TLS certificate/key or a trusted TLS proxy")
		}
	}
	if c.RemoteAuthToken != "" && len(c.RemoteAuthToken) < 32 {
		return errors.New("WEB_SETUP_MANAGER_REMOTE_AUTH_TOKEN must contain at least 32 characters when configured")
	}
	if !validPAMService(c.PAMService) {
		return errors.New("WEB_SETUP_MANAGER_PAM_SERVICE is invalid")
	}
	if c.AuthConcurrency < 1 || c.AuthConcurrency > 64 {
		return errors.New("WEB_SETUP_MANAGER_AUTH_CONCURRENCY must be between 1 and 64")
	}
	if c.LoginAttempts < 1 || c.LoginAttempts > 100 {
		return errors.New("WEB_SETUP_MANAGER_LOGIN_ATTEMPTS must be between 1 and 100")
	}
	if c.AuthSessionCapacity < 1 || c.AuthSessionCapacity > 10000 {
		return errors.New("WEB_SETUP_MANAGER_AUTH_SESSION_CAPACITY must be between 1 and 10000")
	}
	if c.AuthIdleTimeout > c.AuthAbsoluteTimeout {
		return errors.New("WEB_SETUP_MANAGER_AUTH_IDLE_TIMEOUT must not exceed the absolute timeout")
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
		if _, safe := safeGCodeExtensions[extension]; !safe {
			return errors.New("G-code extensions must be selected from .ngc, .nc and .tap")
		}
		if _, duplicate := seen[extension]; duplicate {
			return errors.New("G-code extensions must be unique")
		}
		seen[extension] = struct{}{}
		c.GCodeExtensions[i] = extension
	}
	return nil
}

// ValidateRoots canonicalizes and verifies existing storage roots without
// widening or following them. Deployment creates both roots explicitly so a
// configured symlink can never redirect first-run state creation.
func (c *Config) ValidateRoots() error {
	library, err := canonicalDirectory(c.LibraryDir)
	if err != nil {
		return errors.New("library directory is unavailable")
	}
	state, err := canonicalDirectory(c.StateDir)
	if err != nil {
		return errors.New("state directory is unavailable")
	}
	program := ""
	if c.ProgramRoot != "" {
		program, err = canonicalDirectory(c.ProgramRoot)
		if err != nil {
			return errors.New("LinuxCNC program root is unavailable")
		}
	}
	for _, root := range []struct{ label, directory string }{
		{label: "library", directory: library},
		{label: "state", directory: state},
		{label: "program root", directory: program},
	} {
		label, directory := root.label, root.directory
		if directory == "" {
			continue
		}
		info, statErr := os.Stat(directory)
		if statErr != nil || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s directory permissions are unsafe", label)
		}
	}
	if pathsOverlap(library, state) {
		return errors.New("library and state directories must be disjoint")
	}
	if program != "" && (pathsOverlap(program, state) || pathsOverlap(program, library)) {
		return errors.New("program root, library and state directories must be disjoint")
	}
	c.LibraryDir = library
	c.StateDir = state
	c.ProgramRoot = program
	return nil
}

// ValidateFiles checks optional TLS material without returning host paths in
// errors. Storage contents are validated by the fd-relative storage package.
func (c Config) ValidateFiles() error {
	if c.LinuxCNCINI != "" {
		if err := c.validateLinuxCNCINI(); err != nil {
			return err
		}
	}
	if c.TLSCertFile == "" {
		return nil
	}
	for _, material := range []struct {
		name       string
		filename   string
		privateKey bool
	}{
		{name: "TLS certificate", filename: c.TLSCertFile},
		{name: "TLS private key", filename: c.TLSKeyFile, privateKey: true},
	} {
		name, filename := material.name, material.filename
		if !filepath.IsAbs(filename) {
			return fmt.Errorf("%s must be an absolute regular file", name)
		}
		parent := filepath.Clean(filepath.Dir(filename))
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err != nil || resolvedParent != parent {
			return fmt.Errorf("%s is unavailable", name)
		}
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is unavailable", name)
		}
		permissions := info.Mode().Perm()
		if permissions&0o133 != 0 || (material.privateKey && permissions&0o007 != 0) {
			return fmt.Errorf("%s is unavailable", name)
		}
		file, err := os.Open(filename)
		if err != nil {
			return fmt.Errorf("%s is unreadable", name)
		}
		openedInfo, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
			return fmt.Errorf("%s changed during validation", name)
		}
	}
	return nil
}

// validateLinuxCNCINI binds the writable catalog to the active machine's
// PROGRAM_PREFIX. It never returns either host path in an error.
func (c Config) validateLinuxCNCINI() error {
	if !filepath.IsAbs(c.LinuxCNCINI) || c.ProgramRoot == "" {
		return errors.New("LinuxCNC INI configuration is invalid")
	}
	contents, err := readSafeLinuxCNCINI(c.LinuxCNCINI, 8<<20)
	if err != nil {
		return errors.New("LinuxCNC INI is unavailable")
	}
	prefix := ""
	section := ""
	for _, raw := range strings.Split(string(contents), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToUpper(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "DISPLAY" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "PROGRAM_PREFIX") {
			prefix = strings.TrimSpace(value)
			if comment := strings.IndexAny(prefix, "#;"); comment >= 0 {
				prefix = strings.TrimSpace(prefix[:comment])
			}
			break
		}
	}
	if prefix == "" || !filepath.IsAbs(prefix) {
		return errors.New("LinuxCNC INI has no absolute DISPLAY PROGRAM_PREFIX")
	}
	resolved, err := canonicalDirectory(prefix)
	if err != nil || resolved != c.ProgramRoot {
		return errors.New("LinuxCNC PROGRAM_PREFIX does not match the configured program root")
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", err
	}
	// Any lexical difference means at least one configured path component was
	// a symlink. Reject it instead of silently changing the configured root.
	if resolved != cleaned {
		return "", errors.New("directory path contains a symbolic link")
	}
	info, err := os.Lstat(cleaned)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("not a real directory")
	}
	return cleaned, nil
}

func pathsOverlap(first, second string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		if err != nil || filepath.IsAbs(relative) {
			return false
		}
		return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return contains(first, second) || contains(second, first)
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

// validPublicRootDisplay accepts only an operator-facing relative hint. The
// value is returned by the public API, so an absolute Unix/Windows path, UNC
// authority or URI must fail closed even when supplied accidentally by an
// administrator. A leading "~/" is intentionally allowed for the documented
// LinuxCNC user-folder notation; it is display text and is never resolved.
func validPublicRootDisplay(value string) bool {
	if value == "" || len([]rune(value)) > 200 || filepath.IsAbs(value) ||
		strings.HasPrefix(value, "//") || strings.HasPrefix(value, `\\`) ||
		strings.Contains(value, `\`) || strings.Contains(value, "://") {
		return false
	}
	if len(value) >= 2 && value[1] == ':' &&
		(value[0] >= 'a' && value[0] <= 'z' || value[0] >= 'A' && value[0] <= 'Z') {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// validPublicLabel protects API-visible labels from accidentally becoming a
// disclosure channel for a configured host path. Labels are deliberately
// single-segment display text; catalog navigation is represented separately.
func validPublicLabel(value string) bool {
	if value == "" || len([]rune(value)) > 100 || filepath.IsAbs(value) ||
		strings.HasPrefix(value, "~") || strings.ContainsAny(value, `/\`) ||
		strings.Contains(value, "://") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validAccountName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validPAMService(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\x00/\\\r\n")
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
