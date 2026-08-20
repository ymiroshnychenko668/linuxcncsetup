package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validTestConfig() Config {
	return Config{
		LibraryDir: "/library", StateDir: "/state", ListenAddress: DefaultListenAddress,
		LibraryAlias: "Setups", GCodeExtensions: []string{".ngc"}, RecentSetupsLimit: 30,
		MaxParallelHeavyJobs: 2, ArtifactFileMode: 0o640, ShutdownTimeout: DefaultShutdownTimeout,
		ReadHeaderTimeout: DefaultReadHeaderTimeout, ReadTimeout: DefaultReadTimeout,
		IdleTimeout: DefaultIdleTimeout, MaxHeaderBytes: DefaultMaxHeaderBytes,
		IdempotencyTTL: DefaultIdempotencyTTL, DeleteConfirmationTTL: DefaultDeleteConfirmation,
		ReconcileInterval: DefaultReconcileInterval, ImportSessionExpiry: DefaultImportSessionExpiry,
	}
}

func TestLoadDefaultsAndNormalizesExtensions(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	t.Setenv("WEB_SETUP_MANAGER_LIBRARY_DIR", library)
	t.Setenv("WEB_SETUP_MANAGER_STATE_DIR", state)
	t.Setenv("WEB_SETUP_MANAGER_GCODE_EXTENSIONS", ".NGC, .tap")

	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.ListenAddress != DefaultListenAddress || configuration.LibraryAlias != DefaultLibraryAlias {
		t.Fatalf("unexpected defaults: %+v", configuration)
	}
	if strings.Join(configuration.GCodeExtensions, ",") != ".ngc,.tap" {
		t.Fatalf("extensions = %#v", configuration.GCodeExtensions)
	}
}

func TestValidateRejectsUnsafeRemoteAndExecutableMode(t *testing.T) {
	valid := validTestConfig()

	executable := valid
	executable.ArtifactFileMode = 0o750
	if err := executable.Validate(); err == nil {
		t.Fatal("executable artifact mode was accepted")
	}
	sharedWritable := valid
	sharedWritable.ArtifactFileMode = 0o662
	if err := sharedWritable.Validate(); err == nil {
		t.Fatal("group/world-writable artifact mode was accepted")
	}
	writeOnly := valid
	writeOnly.ArtifactFileMode = 0o200
	if err := writeOnly.Validate(); err == nil {
		t.Fatal("owner-write-only artifact mode was accepted")
	}
	remote := valid
	remote.ListenAddress = "0.0.0.0:8080"
	if err := remote.Validate(); err == nil {
		t.Fatal("unprotected remote listener was accepted")
	}
	remote.RemoteAccess = true
	remote.RemoteAuthToken = strings.Repeat("x", 32)
	remote.TrustedTLSProxy = true
	if err := remote.Validate(); err != nil {
		t.Fatalf("protected remote config rejected: %v", err)
	}
}

func TestValidateRequiresAuthenticationAndTransportForLoopbackRemoteMode(t *testing.T) {
	configuration := validTestConfig()
	configuration.RemoteAccess = true
	if err := configuration.Validate(); err == nil {
		t.Fatal("loopback remote mode without authentication was accepted")
	}
	configuration.RemoteAuthToken = strings.Repeat("x", 32)
	if err := configuration.Validate(); err == nil {
		t.Fatal("loopback remote mode without protected transport was accepted")
	}
	configuration.TrustedTLSProxy = true
	if err := configuration.Validate(); err != nil {
		t.Fatalf("protected loopback remote mode rejected: %v", err)
	}
}

func TestValidateRejectsMissingZeroAndNamedListenPorts(t *testing.T) {
	for _, address := range []string{"127.0.0.1:", "localhost:0", "127.0.0.1:http", "127.0.0.1:65536"} {
		t.Run(address, func(t *testing.T) {
			configuration := validTestConfig()
			configuration.ListenAddress = address
			if err := configuration.Validate(); err == nil {
				t.Fatalf("listen address %q was accepted", address)
			}
		})
	}
}

func TestValidateCanonicalizesNumericListenPort(t *testing.T) {
	configuration := validTestConfig()
	configuration.ListenAddress = "127.0.0.1:080"
	if err := configuration.Validate(); err != nil {
		t.Fatal(err)
	}
	if configuration.ListenAddress != "127.0.0.1:80" {
		t.Fatalf("canonical listen address = %q", configuration.ListenAddress)
	}
}

func TestValidateRootsRejectsOverlapAndSymlink(t *testing.T) {
	library := t.TempDir()
	state := filepath.Join(library, "state")
	configuration := Config{LibraryDir: library, StateDir: state}
	if err := configuration.ValidateRoots(); err == nil {
		t.Fatal("nested roots were accepted")
	}

	state = t.TempDir()
	libraryLink := filepath.Join(t.TempDir(), "library-link")
	if err := os.Symlink(library, libraryLink); err != nil {
		t.Fatal(err)
	}
	configuration = Config{LibraryDir: libraryLink, StateDir: state}
	if err := configuration.ValidateRoots(); err == nil {
		t.Fatal("symlink root was accepted")
	}
}

func TestValidateRootsRejectsFilesystemRoot(t *testing.T) {
	configuration := Config{LibraryDir: string(filepath.Separator), StateDir: t.TempDir()}
	if err := configuration.ValidateRoots(); err == nil {
		t.Fatal("filesystem root was accepted as a managed storage root")
	}
}

func TestValidateRootsRejectsSharedWritableStateDirectory(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	if err := os.Chmod(state, 0o770); err != nil {
		t.Fatal(err)
	}
	configuration := Config{LibraryDir: library, StateDir: state}
	if err := configuration.ValidateRoots(); err == nil {
		t.Fatal("group-writable state directory was accepted")
	}
}

func TestValidateRootsRejectsSharedWritableLibraryDirectory(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	if err := os.Chmod(library, 0o777); err != nil {
		t.Fatal(err)
	}
	configuration := Config{LibraryDir: library, StateDir: state}
	if err := configuration.ValidateRoots(); err == nil {
		t.Fatal("group/world-writable library directory was accepted")
	}
}

func TestErrorsDoNotEchoPhysicalPaths(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "host-secret-library")
	configuration := Config{LibraryDir: secretPath, StateDir: t.TempDir()}
	err := configuration.ValidateRoots()
	if err == nil || strings.Contains(err.Error(), secretPath) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestValidateFilesRejectsTLSLinkWithoutLeakingPath(t *testing.T) {
	directory := t.TempDir()
	certificate := filepath.Join(directory, "certificate.pem")
	key := filepath.Join(directory, "private-key.pem")
	if err := os.WriteFile(certificate, []byte("certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := Config{TLSCertFile: certificate, TLSKeyFile: key}
	if err := configuration.ValidateFiles(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "key-link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	configuration.TLSKeyFile = link
	err := configuration.ValidateFiles()
	if err == nil || strings.Contains(err.Error(), directory) {
		t.Fatalf("unsafe TLS validation error: %v", err)
	}
}

func TestValidateFilesRejectsSharedWritableTLSMaterial(t *testing.T) {
	directory := t.TempDir()
	certificate := filepath.Join(directory, "certificate.pem")
	key := filepath.Join(directory, "private-key.pem")
	if err := os.WriteFile(certificate, []byte("certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, 0o666); err != nil {
		t.Fatal(err)
	}
	configuration := Config{TLSCertFile: certificate, TLSKeyFile: key}
	if err := configuration.ValidateFiles(); err == nil || strings.Contains(err.Error(), directory) {
		t.Fatalf("unsafe TLS material validation error: %v", err)
	}
}

func TestValidateFilesRejectsWorldReadablePrivateKey(t *testing.T) {
	directory := t.TempDir()
	certificate := filepath.Join(directory, "certificate.pem")
	key := filepath.Join(directory, "private-key.pem")
	if err := os.WriteFile(certificate, []byte("certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	configuration := Config{TLSCertFile: certificate, TLSKeyFile: key}
	if err := configuration.ValidateFiles(); err == nil || strings.Contains(err.Error(), directory) {
		t.Fatalf("world-readable private key validation error: %v", err)
	}
	if err := os.Chmod(key, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := configuration.ValidateFiles(); err != nil {
		t.Fatalf("group-readable private key rejected: %v", err)
	}
}
