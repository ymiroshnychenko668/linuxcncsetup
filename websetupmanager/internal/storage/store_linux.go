//go:build linux

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	copyBufferSize     = 256 << 10
	spaceCheckEvery    = 8 << 20
	minimumFreeReserve = 8 << 20
)

var (
	// ErrInsufficientStorage indicates the target filesystem cannot safely
	// complete the atomic operation.
	ErrInsufficientStorage = errors.New("insufficient managed storage")
	// ErrTooLarge indicates a configured artifact byte limit was exceeded.
	ErrTooLarge = errors.New("artifact exceeds configured byte limit")
	// ErrObjectChanged indicates identity/version changed during an operation.
	ErrObjectChanged = errors.New("managed object changed")
	// ErrInvalidObject indicates a non-regular, corrupted or malformed object.
	ErrInvalidObject = errors.New("invalid managed object")
)

// Store publishes immutable content below the managed library root.
type Store struct {
	roots       *Roots
	uploadLimit int64
}

// StoreOptions controls only configured limits. A zero UploadLimit means that
// free space and filesystem limits are the only size constraints.
type StoreOptions struct {
	UploadLimit int64
}

// StagedObject is an internal temporary upload. Path is deliberately
// unexported from every public API DTO.
type StagedObject struct {
	relative string
	Size     int64
	SHA256   string
}

// Object is an immutable published storage object. Key is internal and must
// never be serialized into an HTTP response.
type Object struct {
	Key      string
	Size     int64
	SHA256   string
	Version  string
	Identity ObjectIdentity
}

// ObjectIdentity is the inode identity captured through the already-open file
// descriptor. It is service-internal metadata used to detect unlink/replace
// attacks and external modifications; no physical path is included.
type ObjectIdentity struct {
	Device       uint64
	Inode        uint64
	ModTimeNS    int64
	ChangeTimeNS int64
}

// ObjectKeyForSHA deterministically derives the internal content-addressed
// key without touching the filesystem. Service code uses it to persist an
// active adoption reservation before Publish, closing the GC/adoption race.
// The returned key is internal metadata and must never be serialized.
func (s *Store) ObjectKeyForSHA(sha string) (string, error) {
	if s == nil || len(sha) != sha256.Size*2 || !isLowerHex(sha) {
		return "", ErrInvalidObject
	}
	return objectsDirName + "/" + sha[:2] + "/" + sha, nil
}

// NewStore returns an immutable-object store over already validated roots.
func NewStore(roots *Roots, options StoreOptions) (*Store, error) {
	if roots == nil || options.UploadLimit < 0 {
		return nil, errors.New("invalid object store configuration")
	}
	return &Store{roots: roots, uploadLimit: options.UploadLimit}, nil
}

// Stage streams an upload into an exclusive service-state object while
// hashing it. Memory remains constant relative to input size.
func (s *Store) Stage(ctx context.Context, source io.Reader, expectedSize int64) (_ *StagedObject, err error) {
	if source == nil || expectedSize < -1 {
		return nil, ErrInvalidObject
	}
	if s.uploadLimit > 0 && expectedSize > s.uploadLimit {
		return nil, ErrTooLarge
	}
	required := uint64(0)
	if expectedSize > 0 {
		required = uint64(expectedSize)
	}
	if err := s.requireFree(false, required); err != nil {
		return nil, err
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, errors.New("generate staging identity")
	}
	relative := uploadStageName + "/" + id + ".upload"
	destination, err := s.roots.OpenState(relative, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return nil, errors.New("create upload staging object")
	}
	keep := false
	defer func() {
		closeErr := destination.Close()
		if !keep {
			_ = unlinkBeneath(s.roots.stateFD, relative)
		}
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	hash := sha256.New()
	buffer := make([]byte, copyBufferSize)
	var total int64
	var sinceSpaceCheck int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			total += int64(count)
			sinceSpaceCheck += int64(count)
			if s.uploadLimit > 0 && total > s.uploadLimit {
				return nil, ErrTooLarge
			}
			if sinceSpaceCheck >= spaceCheckEvery {
				if err := s.requireFree(false, minimumFreeReserve); err != nil {
					return nil, err
				}
				sinceSpaceCheck = 0
			}
			if _, err := hash.Write(buffer[:count]); err != nil {
				return nil, err
			}
			if err := writeAll(destination, buffer[:count]); err != nil {
				if errors.Is(err, unix.ENOSPC) {
					return nil, ErrInsufficientStorage
				}
				return nil, errors.New("write upload staging object")
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}
	if expectedSize >= 0 && total != expectedSize {
		return nil, fmt.Errorf("expected %d bytes, received %d: %w", expectedSize, total, ErrInvalidObject)
	}
	if err := destination.Sync(); err != nil {
		return nil, errors.New("sync upload staging object")
	}
	if err := requireRegular(destination); err != nil {
		return nil, ErrInvalidObject
	}
	keep = true
	return &StagedObject{relative: relative, Size: total, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

// OpenStaged opens a staged object for bounded validation before publication.
func (s *Store) OpenStaged(staged *StagedObject) (*os.File, error) {
	if !s.validStaged(staged) {
		return nil, ErrInvalidObject
	}
	file, err := s.roots.OpenState(staged.relative, unix.O_RDONLY, 0)
	if err != nil {
		return nil, ErrInvalidObject
	}
	if err := requireRegular(file); err != nil {
		file.Close()
		return nil, ErrInvalidObject
	}
	return file, nil
}

// Discard removes an unfinished staged upload idempotently.
func (s *Store) Discard(staged *StagedObject) error {
	if !s.validStaged(staged) {
		return ErrInvalidObject
	}
	err := unlinkBeneath(s.roots.stateFD, staged.relative)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

// Publish atomically makes a staged upload visible as an immutable managed
// object. The state and library roots may be on different filesystems.
func (s *Store) Publish(ctx context.Context, staged *StagedObject) (_ *Object, err error) {
	if !s.validStaged(staged) {
		return nil, ErrInvalidObject
	}
	if err := s.requireFree(true, uint64(staged.Size)); err != nil {
		return nil, err
	}
	prefix := staged.SHA256[:2]
	if err := ensureNestedDirectory(s.roots.libraryFD, objectsDirName, prefix, 0o750); err != nil {
		return nil, errors.New("prepare immutable object namespace")
	}
	finalRelative := objectsDirName + "/" + prefix + "/" + staged.SHA256
	if existing, openErr := s.VerifyObject(ctx, finalRelative, staged.SHA256, ""); openErr == nil {
		if err := s.Discard(staged); err != nil {
			return nil, err
		}
		return existing, nil
	} else if !errors.Is(openErr, unix.ENOENT) && !errors.Is(openErr, ErrInvalidObject) {
		return nil, openErr
	} else if errors.Is(openErr, ErrInvalidObject) {
		return nil, ErrInvalidObject
	}

	source, err := s.OpenStaged(staged)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	before, err := statFile(source)
	if err != nil {
		return nil, ErrInvalidObject
	}
	tempID, err := randomHex(16)
	if err != nil {
		return nil, errors.New("generate publication identity")
	}
	tempRelative := objectTempDirName + "/" + tempID + ".object"
	destination, err := s.roots.OpenLibrary(tempRelative, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, s.roots.fileMode)
	if err != nil {
		return nil, errors.New("create publication staging object")
	}
	tempExists := true
	defer func() {
		closeErr := destination.Close()
		if tempExists {
			_ = unlinkBeneath(s.roots.libraryFD, tempRelative)
		}
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	hash := sha256.New()
	written, err := copyContext(ctx, io.MultiWriter(destination, hash), source, func(total int64) error {
		if total%spaceCheckEvery < copyBufferSize {
			return s.requireFree(true, minimumFreeReserve)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	after, err := statFile(source)
	if err != nil || !sameStat(before, after) || written != staged.Size || hex.EncodeToString(hash.Sum(nil)) != staged.SHA256 {
		return nil, ErrObjectChanged
	}
	if err := destination.Chmod(s.roots.fileMode); err != nil {
		return nil, errors.New("set managed object permissions")
	}
	if err := destination.Sync(); err != nil {
		return nil, errors.New("sync publication staging object")
	}
	if err := renameNoReplace(s.roots.libraryFD, tempRelative, finalRelative); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return nil, errors.New("publish immutable object")
		}
	} else {
		tempExists = false
	}
	if err := syncDirectoryPath(s.roots.libraryFD, objectsDirName+"/"+prefix); err != nil {
		return nil, errors.New("sync immutable object namespace")
	}
	object, err := s.VerifyObject(ctx, finalRelative, staged.SHA256, "")
	if err != nil {
		return nil, err
	}
	if err := s.Discard(staged); err != nil {
		return nil, err
	}
	return object, nil
}

// OpenObject opens a regular immutable object and checks its opaque version.
func (s *Store) OpenObject(key, sha, expectedVersion string) (*os.File, error) {
	if !validObjectKey(key, sha) {
		return nil, ErrInvalidObject
	}
	return s.openVerified(key, sha, -1, expectedVersion)
}

// InspectObject verifies the inode identity captured in expectedVersion
// without hashing the entire object. It is suitable for HEAD and for the
// identity checks surrounding bounded Range reads.
func (s *Store) InspectObject(key, sha, expectedVersion string) (*Object, error) {
	file, err := s.OpenObject(key, sha, expectedVersion)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return objectFromFile(file, key, sha)
}

// ReadObjectRange reads into a caller-bounded buffer and rejects an object
// whose inode metadata changes before the block is complete. The returned
// total size is 64-bit and no allocation depends on it.
func (s *Store) ReadObjectRange(ctx context.Context, key, sha, expectedVersion string, offset int64, destination []byte) (int, int64, error) {
	if offset < 0 || len(destination) == 0 {
		return 0, 0, ErrInvalidObject
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	file, err := s.OpenObject(key, sha, expectedVersion)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	before, err := statFile(file)
	if err != nil {
		return 0, 0, ErrInvalidObject
	}
	if offset >= before.Size {
		return 0, before.Size, io.EOF
	}
	maximum := before.Size - offset
	if int64(len(destination)) > maximum {
		destination = destination[:maximum]
	}
	count, readErr := file.ReadAt(destination, offset)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return count, before.Size, readErr
	}
	if err := ctx.Err(); err != nil {
		return 0, before.Size, err
	}
	after, err := statFile(file)
	if err != nil || !sameStat(before, after) || versionForStat(after, sha) != expectedVersion {
		return 0, before.Size, ErrObjectChanged
	}
	return count, before.Size, nil
}

// ListObjects enumerates only canonical immutable object names through held
// root descriptors. It neither follows links nor descends through an entry
// whose name was not generated by this service.
func (s *Store) ListObjects(ctx context.Context) ([]*Object, error) {
	root, err := s.roots.OpenLibrary(objectsDirName, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	prefixes, err := root.ReadDir(-1)
	_ = root.Close()
	if err != nil {
		return nil, err
	}
	result := make([]*Object, 0)
	for _, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := prefix.Name()
		if len(name) != 2 || !isLowerHex(name) || !prefix.IsDir() || prefix.Type()&os.ModeSymlink != 0 {
			continue
		}
		directory, openErr := s.roots.OpenLibrary(objectsDirName+"/"+name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if openErr != nil {
			return nil, ErrInvalidObject
		}
		entries, readErr := directory.ReadDir(-1)
		_ = directory.Close()
		if readErr != nil {
			return nil, readErr
		}
		for _, entry := range entries {
			sha := entry.Name()
			if len(sha) != sha256.Size*2 || !isLowerHex(sha) || sha[:2] != name || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			key := objectsDirName + "/" + name + "/" + sha
			object, inspectErr := s.InspectObject(key, sha, "")
			if inspectErr != nil {
				return nil, ErrInvalidObject
			}
			result = append(result, object)
		}
	}
	sort.Slice(result, func(first, second int) bool { return result[first].Key < result[second].Key })
	return result, nil
}

// VerifyObject hashes a published object, compares identity before/after and
// returns its current metadata. It is used by validation and reconciliation.
func (s *Store) VerifyObject(ctx context.Context, key, sha, expectedVersion string) (*Object, error) {
	return s.VerifyObjectWithProgress(ctx, key, sha, expectedVersion, nil)
}

// VerifyObjectWithProgress is VerifyObject with a bounded streaming byte
// callback for durable jobs that must expose live progress and cancellation.
func (s *Store) VerifyObjectWithProgress(
	ctx context.Context,
	key, sha, expectedVersion string,
	progress func(int64) error,
) (*Object, error) {
	file, err := s.OpenObject(key, sha, expectedVersion)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := statFile(file)
	if err != nil {
		return nil, ErrInvalidObject
	}
	hash := sha256.New()
	written, err := copyContext(ctx, hash, file, progress)
	if err != nil {
		return nil, err
	}
	after, err := statFile(file)
	if err != nil || !sameStat(before, after) {
		return nil, ErrObjectChanged
	}
	if written != after.Size || hex.EncodeToString(hash.Sum(nil)) != sha {
		return nil, ErrObjectChanged
	}
	return objectFromStat(key, sha, after), nil
}

// RemoveObject removes one validated immutable key. Reference and journal
// safety is enforced by the service transaction before calling this method.
func (s *Store) RemoveObject(key, sha string) error {
	if !validObjectKey(key, sha) {
		return ErrInvalidObject
	}
	err := unlinkBeneath(s.roots.libraryFD, key)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

// CleanupStaging removes regular and special entries left directly in both
// private staging directories. It never follows links or recurses.
func (s *Store) CleanupStaging() error {
	for _, staging := range []struct {
		rootFD   int
		relative string
	}{
		{s.roots.stateFD, uploadStageName},
		{s.roots.libraryFD, objectTempDirName},
	} {
		if err := cleanupFlatDirectory(staging.rootFD, staging.relative); err != nil {
			return err
		}
	}
	return nil
}

func cleanupFlatDirectory(rootFD int, relative string) error {
	fd, err := openBeneath(rootFD, relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), "managed-staging")
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !validComponent(entry.Name()) || entry.IsDir() {
			continue
		}
		if err := unix.Unlinkat(fd, entry.Name(), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return nil
}

func (s *Store) openVerified(key, sha string, expectedSize int64, expectedVersion string) (*os.File, error) {
	file, err := s.roots.OpenLibrary(key, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if err := requireRegular(file); err != nil {
		file.Close()
		return nil, ErrInvalidObject
	}
	stat, err := statFile(file)
	if err != nil || (expectedSize >= 0 && stat.Size != expectedSize) {
		file.Close()
		return nil, ErrInvalidObject
	}
	version := versionForStat(stat, sha)
	if expectedVersion != "" && !constantStringEqual(version, expectedVersion) {
		file.Close()
		return nil, ErrObjectChanged
	}
	return file, nil
}

func (s *Store) validStaged(staged *StagedObject) bool {
	return staged != nil && staged.Size >= 0 && len(staged.SHA256) == sha256.Size*2 &&
		strings.HasPrefix(staged.relative, uploadStageName+"/") && strings.HasSuffix(staged.relative, ".upload")
}

func (s *Store) requireFree(library bool, required uint64) error {
	available, err := s.roots.FreeBytes(library)
	if err != nil {
		return err
	}
	if required > ^uint64(0)-minimumFreeReserve || available < required+minimumFreeReserve {
		return ErrInsufficientStorage
	}
	return nil
}

func objectFromFile(file *os.File, key, sha string) (*Object, error) {
	stat, err := statFile(file)
	if err != nil {
		return nil, err
	}
	return objectFromStat(key, sha, stat), nil
}

func objectFromStat(key, sha string, stat unix.Stat_t) *Object {
	return &Object{
		Key: key, Size: stat.Size, SHA256: sha, Version: versionForStat(stat, sha),
		Identity: ObjectIdentity{
			Device:       uint64(stat.Dev),
			Inode:        stat.Ino,
			ModTimeNS:    stat.Mtim.Sec*1_000_000_000 + stat.Mtim.Nsec,
			ChangeTimeNS: stat.Ctim.Sec*1_000_000_000 + stat.Ctim.Nsec,
		},
	}
}

func statFile(file *os.File) (unix.Stat_t, error) {
	var stat unix.Stat_t
	err := unix.Fstat(int(file.Fd()), &stat)
	if err == nil && stat.Mode&unix.S_IFMT != unix.S_IFREG {
		err = ErrInvalidObject
	}
	return stat, err
}

func sameStat(first, second unix.Stat_t) bool {
	return first.Dev == second.Dev && first.Ino == second.Ino && first.Size == second.Size &&
		first.Mtim == second.Mtim && first.Ctim == second.Ctim &&
		first.Mode&unix.S_IFMT == second.Mode&unix.S_IFMT
}

func versionForStat(stat unix.Stat_t, sha string) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d:%d:%d:%d:%d:%d:%d:%s", stat.Dev, stat.Ino, stat.Size,
		stat.Mtim.Sec, stat.Mtim.Nsec, stat.Ctim.Sec, stat.Ctim.Nsec, sha)
	return hex.EncodeToString(hash.Sum(nil))
}

func validObjectKey(key, sha string) bool {
	if len(sha) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return false
	}
	return key == objectsDirName+"/"+sha[:2]+"/"+sha
}

func constantStringEqual(first, second string) bool {
	if len(first) != len(second) {
		return false
	}
	var difference byte
	for index := range first {
		difference |= first[index] ^ second[index]
	}
	return difference == 0
}

func isLowerHex(value string) bool {
	for index := range len(value) {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return value != ""
}

func writeAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader, progress func(int64) error) (int64, error) {
	buffer := make([]byte, copyBufferSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if err := writeAll(destination, buffer[:count]); err != nil {
				if errors.Is(err, unix.ENOSPC) {
					return total, ErrInsufficientStorage
				}
				return total, err
			}
			total += int64(count)
			if progress != nil {
				if err := progress(total); err != nil {
					return total, err
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func ensureNestedDirectory(rootFD int, parent, child string, mode uint32) error {
	if !validComponent(parent) || !validComponent(child) {
		return errUnsafePath
	}
	parentFD, err := openBeneath(rootFD, parent, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	return ensureDirectory(parentFD, child, mode)
}

func renameNoReplace(rootFD int, oldRelative, newRelative string) error {
	oldParent, oldName, err := openParent(rootFD, oldRelative)
	if err != nil {
		return err
	}
	defer unix.Close(oldParent)
	newParent, newName, err := openParent(rootFD, newRelative)
	if err != nil {
		return err
	}
	defer unix.Close(newParent)
	err = unix.Renameat2(oldParent, oldName, newParent, newName, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	if err := unix.Linkat(oldParent, oldName, newParent, newName, 0); err != nil {
		return err
	}
	return unix.Unlinkat(oldParent, oldName, 0)
}

func openParent(rootFD int, relative string) (int, string, error) {
	components, err := safeComponents(relative)
	if err != nil {
		return -1, "", err
	}
	if len(components) == 1 {
		fd, err := unix.Dup(rootFD)
		return fd, components[0], err
	}
	fd, err := openBeneath(rootFD, strings.Join(components[:len(components)-1], "/"), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	return fd, components[len(components)-1], err
}

func syncDirectoryPath(rootFD int, relative string) error {
	fd, err := openBeneath(rootFD, relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fsync(fd)
}
