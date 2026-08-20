//go:build linux

// Package storage implements root-anchored managed storage operations.
package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/libraryidentity"
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

// RootIdentity identifies one physical directory for security checks between
// components. It is internal deployment metadata and must never be exposed by
// the HTTP API.
type RootIdentity struct {
	Device uint64
	Inode  uint64
}

// NewRoots opens and initializes the configured storage roots.
func NewRoots(libraryDir, stateDir string, fileMode fs.FileMode) (*Roots, error) {
	permissions := fileMode.Perm()
	if permissions&0o400 == 0 || permissions&0o111 != 0 || permissions&0o022 != 0 {
		return nil, errors.New("managed artifact file mode is unsafe")
	}
	libraryFD, err := openRoot(libraryDir)
	if err != nil {
		return nil, errors.New("open managed library root")
	}
	stateFD, err := openRoot(stateDir)
	if err != nil {
		_ = unix.Close(libraryFD)
		return nil, errors.New("open service state root")
	}
	var libraryStat, stateStat unix.Stat_t
	if unix.Fstat(libraryFD, &libraryStat) != nil || unix.Fstat(stateFD, &stateStat) != nil ||
		libraryStat.Dev == stateStat.Dev && libraryStat.Ino == stateStat.Ino {
		_ = unix.Close(libraryFD)
		_ = unix.Close(stateFD)
		return nil, errors.New("managed storage roots are not physically disjoint")
	}
	roots := &Roots{libraryFD: libraryFD, stateFD: stateFD, fileMode: permissions}
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

// LibraryFingerprint binds state to the persisted random marker. It excludes
// host identity so a matched state+library backup remains restorable.
func (r *Roots) LibraryFingerprint() string { return r.fingerprint }

// StateIdentity returns the identity of the already-opened state root. A
// caller opening another resource below the configured state path can require
// that it resolved to this same directory, closing the rename/substitution
// window between storage and database initialization.
func (r *Roots) StateIdentity() (RootIdentity, error) {
	if r == nil || r.stateFD < 0 {
		return RootIdentity{}, errors.New("service state root is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(r.stateFD, &stat); err != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 {
		return RootIdentity{}, errors.New("service state root is unavailable")
	}
	return RootIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

// Check verifies that both held roots and every security-sensitive managed
// directory still refer to usable, writable directories. It is intentionally
// non-mutating so readiness probes never create storage objects.
func (r *Roots) Check() error {
	for _, descriptor := range []int{r.libraryFD, r.stateFD} {
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 {
			return errors.New("storage root is unavailable")
		}
		var statfs unix.Statfs_t
		if err := unix.Fstatfs(descriptor, &statfs); err != nil || statfs.Blocks == 0 || statfs.Flags&unix.ST_RDONLY != 0 {
			return errors.New("storage filesystem is unavailable")
		}
	}
	for _, directory := range []struct {
		rootFD int
		name   string
	}{
		{r.libraryFD, objectsDirName},
		{r.libraryFD, objectTempDirName},
		{r.stateFD, uploadStageName},
		{r.stateFD, indexesDirName},
	} {
		fd, err := openBeneath(directory.rootFD, directory.name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return errors.New("managed storage directory is unavailable")
		}
		var stat unix.Stat_t
		var statfs unix.Statfs_t
		statErr := unix.Fstat(fd, &stat)
		statfsErr := unix.Fstatfs(fd, &statfs)
		accessErr := unix.Faccessat(fd, ".", unix.W_OK|unix.X_OK, unix.AT_EACCESS)
		closeErr := unix.Close(fd)
		if statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 ||
			statfsErr != nil || statfs.Blocks == 0 || statfs.Flags&unix.ST_RDONLY != 0 || accessErr != nil || closeErr != nil {
			return errors.New("managed storage directory is unavailable")
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
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return -1, errUnsafePath
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(cleaned, "/"), "/") {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 {
		_ = unix.Close(fd)
		return -1, errors.New("managed storage root permissions are unsafe")
	}
	return fd, nil
}

func ensureDirectory(rootFD int, name string, mode uint32) error {
	if !validComponent(name) {
		return errUnsafePath
	}
	created := false
	if err := unix.Mkdirat(rootFD, name, mode); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
	} else {
		created = true
	}
	fd, err := openBeneath(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if stat.Mode&0o022 != 0 {
		_ = unix.Close(fd)
		return errors.New("managed directory is writable by group or others")
	}
	if err := unix.Close(fd); err != nil {
		return err
	}
	if created {
		return unix.Fsync(rootFD)
	}
	return nil
}

func ensureLibraryID(libraryFD int) (string, error) {
	created := false
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
		created = true
	}
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 ||
		stat.Mode&0o022 != 0 || stat.Size > 128 {
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
	if created {
		if err := unix.Fsync(libraryFD); err != nil {
			return "", err
		}
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
	return libraryidentity.Fingerprint(libraryID), nil
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
	if err := unix.Unlinkat(parent, components[len(components)-1], 0); err != nil {
		return err
	}
	// Make the removed directory entry durable before callers commit catalog
	// metadata that forgets the object. Otherwise a power loss can resurrect an
	// untracked immutable file after SQLite has committed its GC row deletion.
	return unix.Fsync(parent)
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
