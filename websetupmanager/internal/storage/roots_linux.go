//go:build linux

// Package storage implements root-anchored managed storage operations.
package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	libraryMarkerName = ".websetupmanager-library-id"
	objectsDirName    = "objects"
	objectTempDirName = ".object-staging"
	uploadStageName   = "staging"
	indexesDirName    = "indexes"
)

var errUnsafePath = errors.New("unsafe managed storage path")

// Roots owns directory descriptors for both configured roots. Filesystem
// operations are resolved relative to these descriptors and never by joining
// a user-controlled host path.
type Roots struct {
	libraryFD   int
	stateFD     int
	libraryID   string
	fingerprint string
	fileMode    fs.FileMode
	closeOnce   sync.Once
}

// NewRoots opens and initializes the configured storage roots.
func NewRoots(libraryDir, stateDir string, fileMode fs.FileMode) (*Roots, error) {
	libraryFD, err := openRoot(libraryDir)
	if err != nil {
		return nil, errors.New("open managed library root")
	}
	stateFD, err := openRoot(stateDir)
	if err != nil {
		_ = unix.Close(libraryFD)
		return nil, errors.New("open service state root")
	}
	roots := &Roots{libraryFD: libraryFD, stateFD: stateFD, fileMode: fileMode.Perm()}
	fail := func(err error) (*Roots, error) {
		_ = roots.Close()
		return nil, err
	}
	for _, directory := range []struct {
		fd   int
		name string
		mode uint32
	}{
		{libraryFD, objectsDirName, 0o750},
		{libraryFD, objectTempDirName, 0o700},
		{stateFD, uploadStageName, 0o700},
		{stateFD, indexesDirName, 0o700},
	} {
		if err := ensureDirectory(directory.fd, directory.name, directory.mode); err != nil {
			return fail(errors.New("initialize managed storage layout"))
		}
	}
	libraryID, err := ensureLibraryID(libraryFD)
	if err != nil {
		return fail(errors.New("initialize library identity"))
	}
	roots.libraryID = libraryID
	fingerprint, err := rootFingerprint(libraryFD, libraryID)
	if err != nil {
		return fail(errors.New("inspect library identity"))
	}
	roots.fingerprint = fingerprint
	if err := roots.checkWritable(); err != nil {
		return fail(err)
	}
	return roots, nil
}

// LibraryID is a stable opaque identifier persisted inside the managed root.
func (r *Roots) LibraryID() string { return r.libraryID }

// LibraryFingerprint binds state to this physical root without exposing its
// path, device or inode through the public API.
func (r *Roots) LibraryFingerprint() string { return r.fingerprint }

// Check verifies that both held roots still refer to usable directories and
// that their filesystems report available blocks.
func (r *Roots) Check() error {
	for _, descriptor := range []int{r.libraryFD, r.stateFD} {
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errors.New("storage root is unavailable")
		}
		var statfs unix.Statfs_t
		if err := unix.Fstatfs(descriptor, &statfs); err != nil || statfs.Blocks == 0 {
			return errors.New("storage filesystem is unavailable")
		}
	}
	return nil
}

// FreeBytes returns currently available bytes for staging and publication.
func (r *Roots) FreeBytes(library bool) (uint64, error) {
	descriptor := r.stateFD
	if library {
		descriptor = r.libraryFD
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(descriptor, &stat); err != nil {
		return 0, errors.New("storage filesystem is unavailable")
	}
	if stat.Bsize <= 0 {
		return 0, errors.New("storage filesystem reported an invalid block size")
	}
	blockSize := uint64(stat.Bsize)
	available := uint64(stat.Bavail)
	if available > math.MaxUint64/blockSize {
		return math.MaxUint64, nil
	}
	return available * blockSize, nil
}

// OpenLibrary opens an internal relative path below library_dir without
// following symlinks. O_NONBLOCK ensures a mistakenly introduced FIFO or device
// cannot block a worker before fstat rejects it.
func (r *Roots) OpenLibrary(relative string, flags int, mode fs.FileMode) (*os.File, error) {
	fd, err := openBeneath(r.libraryFD, relative, flags|unix.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "managed-object"), nil
}

// OpenState opens an internal relative path below state_dir without following
// symlinks.
func (r *Roots) OpenState(relative string, flags int, mode fs.FileMode) (*os.File, error) {
	fd, err := openBeneath(r.stateFD, relative, flags|unix.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "service-state"), nil
}

// EnsureLibraryDirectory creates one validated internal directory component.
func (r *Roots) EnsureLibraryDirectory(name string, mode fs.FileMode) error {
	if !validComponent(name) {
		return errUnsafePath
	}
	return ensureDirectory(r.libraryFD, name, uint32(mode.Perm()))
}

// Close releases root descriptors exactly once.
func (r *Roots) Close() error {
	var result error
	r.closeOnce.Do(func() {
		if err := unix.Close(r.libraryFD); err != nil {
			result = err
		}
		if err := unix.Close(r.stateFD); err != nil && result == nil {
			result = err
		}
	})
	return result
}

func (r *Roots) checkWritable() error {
	probeID, err := randomHex(8)
	if err != nil {
		return errors.New("generate storage readiness probe")
	}
	for _, probe := range []struct {
		fd   int
		name string
	}{
		{r.libraryFD, objectTempDirName + "/.write-probe-" + probeID},
		{r.stateFD, uploadStageName + "/.write-probe-" + probeID},
	} {
		fd, err := openBeneath(probe.fd, probe.name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
		if err != nil {
			return errors.New("storage root is not writable")
		}
		_ = unix.Close(fd)
		if err := unlinkBeneath(probe.fd, probe.name); err != nil {
			return errors.New("storage root cleanup failed")
		}
	}
	return nil
}

func openRoot(path string) (int, error) {
	return unix.Open(filepath.Clean(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func ensureDirectory(rootFD int, name string, mode uint32) error {
	if !validComponent(name) {
		return errUnsafePath
	}
	if err := unix.Mkdirat(rootFD, name, mode); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	fd, err := openBeneath(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func ensureLibraryID(libraryFD int) (string, error) {
	fd, err := openBeneath(libraryFD, libraryMarkerName, unix.O_RDWR, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = openBeneath(libraryFD, libraryMarkerName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0o600)
		if err != nil {
			return "", err
		}
		identifier, idErr := randomHex(16)
		if idErr != nil {
			_ = unix.Close(fd)
			return "", idErr
		}
		if _, writeErr := unix.Write(fd, []byte(identifier+"\n")); writeErr != nil {
			_ = unix.Close(fd)
			return "", writeErr
		}
		if syncErr := unix.Fsync(fd); syncErr != nil {
			_ = unix.Close(fd)
			return "", syncErr
		}
	}
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size > 128 {
		return "", errors.New("invalid library identity marker")
	}
	buffer := make([]byte, 128)
	count, err := unix.Pread(fd, buffer, 0)
	if err != nil {
		return "", err
	}
	identifier := strings.TrimSpace(string(buffer[:count]))
	decoded, err := hex.DecodeString(identifier)
	if err != nil || len(decoded) != 16 {
		return "", errors.New("invalid library identity marker")
	}
	return identifier, nil
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func rootFingerprint(fd int, libraryID string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", errors.New("invalid library root")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", libraryID, stat.Dev, stat.Ino)))
	return hex.EncodeToString(digest[:]), nil
}

func openBeneath(rootFD int, relative string, flags int, mode uint32) (int, error) {
	components, err := safeComponents(relative)
	if err != nil {
		return -1, err
	}
	how := &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Mode:    uint64(mode),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	}
	fd, openErr := unix.Openat2(rootFD, strings.Join(components, "/"), how)
	if openErr == nil {
		return fd, nil
	}
	if !errors.Is(openErr, unix.ENOSYS) && !errors.Is(openErr, unix.EINVAL) {
		return -1, openErr
	}
	return openBeneathFallback(rootFD, components, flags, mode)
}

func openBeneathFallback(rootFD int, components []string, flags int, mode uint32) (int, error) {
	current, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
	}
	defer unix.Close(current)
	return unix.Openat(current, components[len(components)-1], flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
}

func unlinkBeneath(rootFD int, relative string) error {
	components, err := safeComponents(relative)
	if err != nil {
		return err
	}
	parent := rootFD
	closeParent := false
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(parent, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if closeParent {
			_ = unix.Close(parent)
		}
		if openErr != nil {
			return openErr
		}
		parent = next
		closeParent = true
	}
	if closeParent {
		defer unix.Close(parent)
	}
	return unix.Unlinkat(parent, components[len(components)-1], 0)
}

func safeComponents(relative string) ([]string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.ContainsRune(relative, '\x00') || strings.Contains(relative, "\\") {
		return nil, errUnsafePath
	}
	components := strings.Split(relative, "/")
	for _, component := range components {
		if !validComponent(component) {
			return nil, errUnsafePath
		}
	}
	return components, nil
}

func validComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		!strings.ContainsAny(component, "/\\\x00")
}

func requireRegular(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("managed object is not a regular file: %w", errUnsafePath)
	}
	return nil
}
