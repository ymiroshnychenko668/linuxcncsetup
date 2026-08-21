//go:build linux

package storage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// CatalogStore owns LinuxCNC's PROGRAM_PREFIX through a held directory fd.
// Every operation is relative, refuses symlinks/special files, and cannot
// escape the configured root.
type CatalogStore struct {
	rootFD         int
	rootPath       string
	staging        *Store
	fileMode       os.FileMode
	beforeExchange func()
	beforeMove     func()
	syncDirectory  func(int) error
	closeOnce      sync.Once
}

type CatalogPublication struct {
	Object      *Object
	Restored    *Object
	store       *CatalogStore
	target      string
	oldTemp     string
	replacement bool
	newStat     unix.Stat_t
	oldStat     unix.Stat_t
	newSHA      string
	oldSHA      string
	resolved    bool
}

type CatalogQuarantine struct {
	Restored *Object
	store    *CatalogStore
	original string
	hidden   string
	stat     unix.Stat_t
	sha      string
	resolved bool
}

type CatalogFolderQuarantine struct {
	store    *CatalogStore
	original string
	hidden   string
	stat     unix.Stat_t
	resolved bool
}

func NewCatalogStore(programRoot string, staging *Store, fileMode os.FileMode) (*CatalogStore, error) {
	if staging == nil || fileMode.Perm()&0o400 == 0 || fileMode.Perm()&0o111 != 0 || fileMode.Perm()&0o022 != 0 {
		return nil, errors.New("invalid catalog store configuration")
	}
	fd, err := openRoot(programRoot)
	if err != nil {
		return nil, errors.New("open LinuxCNC program root")
	}
	store := &CatalogStore{rootFD: fd, rootPath: filepath.Clean(programRoot), staging: staging,
		fileMode: fileMode.Perm(), syncDirectory: unix.Fsync}
	if err := store.Check(); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return store, nil
}

func (s *CatalogStore) Close() error {
	if s == nil {
		return nil
	}
	var result error
	s.closeOnce.Do(func() { result = unix.Close(s.rootFD) })
	return result
}

func (s *CatalogStore) Check() error {
	if s == nil || s.rootFD < 0 {
		return errors.New("catalog storage is unavailable")
	}
	var stat unix.Stat_t
	var active unix.Stat_t
	var statfs unix.Statfs_t
	activeFD, activeErr := openRoot(s.rootPath)
	if activeErr == nil {
		activeErr = unix.Fstat(activeFD, &active)
		_ = unix.Close(activeFD)
	}
	if activeErr != nil || unix.Fstat(s.rootFD, &stat) != nil || stat.Dev != active.Dev || stat.Ino != active.Ino ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 ||
		unix.Fstatfs(s.rootFD, &statfs) != nil || statfs.Flags&unix.ST_RDONLY != 0 ||
		unix.Faccessat(s.rootFD, ".", unix.W_OK|unix.X_OK, unix.AT_EACCESS) != nil {
		return errors.New("catalog storage is unavailable")
	}
	return nil
}

// CreateFolderPrepared builds a directory under a private operation name,
// persists its inode through beforeVisible, and only then publishes it with a
// no-replace rename.  A crash can therefore be reconciled without adopting an
// unrelated directory at the public pathname.
func (s *CatalogStore) CreateFolderPrepared(relative, operationID string, beforeVisible func(*Object) error) (_ *Object, err error) {
	if !validCatalogPath(relative) || !isLowerHex(operationID) || len(operationID) != 32 || beforeVisible == nil {
		return nil, ErrInvalidObject
	}
	temporary := folderCreateTemporary(relative, operationID)
	if !validOperationInternalPath(relative, temporary, ".wsm-create-") {
		return nil, ErrInvalidObject
	}
	parent, targetName, err := openParent(s.rootFD, relative)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	temporaryName := path.Base(temporary)
	if err := unix.Mkdirat(parent, temporaryName, 0o750); err != nil {
		return nil, err
	}
	hiddenExists := true
	cleanupKnown := false
	var prepared unix.Stat_t
	defer func() {
		if err != nil && hiddenExists && cleanupKnown {
			if cleanupErr := removeEmptyDirectoryExpected(s.rootFD, temporary, gcPathForOperation(temporary), prepared); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()
	fd, err := unix.Openat(parent, temporaryName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "catalog-folder-create")
	defer directory.Close()
	if err := unix.Fstat(fd, &prepared); err != nil || prepared.Mode&unix.S_IFMT != unix.S_IFDIR || prepared.Mode&0o022 != 0 {
		return nil, ErrInvalidObject
	}
	cleanupKnown = true
	if err := unix.Fsync(fd); err != nil {
		return nil, err
	}
	if err := unix.Fsync(parent); err != nil {
		return nil, err
	}
	if err := beforeVisible(objectFromStat(relative, "", prepared)); err != nil {
		return nil, err
	}
	if err := renameAtNoReplace(parent, temporaryName, parent, targetName); err != nil {
		return nil, err
	}
	hiddenExists = false
	var published unix.Stat_t
	if err := unix.Fstat(fd, &published); err != nil || !sameStableObject(prepared, published) {
		if rollbackErr := renameAtNoReplace(parent, targetName, parent, temporaryName); rollbackErr != nil {
			return nil, errors.Join(ErrObjectChanged, rollbackErr)
		}
		hiddenExists = true
		return nil, ErrObjectChanged
	}
	if err := unix.Fsync(parent); err != nil {
		if rollbackErr := renameAtNoReplace(parent, targetName, parent, temporaryName); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		hiddenExists = true
		_ = unix.Fsync(parent)
		return nil, err
	}
	return objectFromStat(relative, "", published), nil
}

func (s *CatalogStore) MoveExpected(ctx context.Context, oldRelative, newRelative, expectedSHA, expectedVersion string) (*Object, error) {
	if !validCatalogPath(oldRelative) || !validCatalogPath(newRelative) || oldRelative == newRelative ||
		strings.HasPrefix(newRelative+"/", oldRelative+"/") {
		return nil, errUnsafePath
	}
	oldParent, oldName, err := openParent(s.rootFD, oldRelative)
	if err != nil {
		return nil, err
	}
	defer unix.Close(oldParent)
	newParent, newName, err := openParent(s.rootFD, newRelative)
	if err != nil {
		return nil, err
	}
	defer unix.Close(newParent)
	sourceFD, err := openBeneath(s.rootFD, oldRelative, unix.O_PATH, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(sourceFD)
	var source unix.Stat_t
	if err := unix.Fstat(sourceFD, &source); err != nil ||
		(source.Mode&unix.S_IFMT != unix.S_IFREG && source.Mode&unix.S_IFMT != unix.S_IFDIR) ||
		source.Mode&unix.S_IFMT == unix.S_IFREG && source.Nlink != 1 {
		return nil, errUnsafePath
	}
	regular := source.Mode&unix.S_IFMT == unix.S_IFREG
	if expectedVersion != "" && versionForStat(source, expectedSHA) != expectedVersion {
		return nil, ErrObjectChanged
	}
	if regular {
		verified, verifyErr := s.verifyInternalSHA(ctx, oldRelative, expectedSHA)
		if verifyErr != nil || !sameStableObject(source, verified) {
			if verifyErr != nil {
				return nil, verifyErr
			}
			return nil, ErrObjectChanged
		}
		source = verified
	}
	if s.beforeMove != nil {
		s.beforeMove()
	}
	if err := renameAtNoReplace(oldParent, oldName, newParent, newName); err != nil {
		return nil, err
	}
	targetFD, openErr := openBeneath(s.rootFD, newRelative, unix.O_PATH, 0)
	var target unix.Stat_t
	if openErr == nil {
		openErr = unix.Fstat(targetFD, &target)
		_ = unix.Close(targetFD)
	}
	if openErr != nil || !sameStableObject(source, target) {
		if rollbackErr := renameAtNoReplace(newParent, newName, oldParent, oldName); rollbackErr != nil {
			return objectFromStat(newRelative, expectedSHA, target), errors.Join(ErrObjectChanged, rollbackErr)
		}
		return nil, ErrObjectChanged
	}
	if err := s.syncDirectory(oldParent); err != nil {
		return s.rollbackMoveAfterSyncFailure(oldParent, oldName, newParent, newName, source, target, expectedSHA, err)
	}
	if oldParent != newParent {
		if err := s.syncDirectory(newParent); err != nil {
			return s.rollbackMoveAfterSyncFailure(oldParent, oldName, newParent, newName, source, target, expectedSHA, err)
		}
	}
	if regular {
		post, err := s.verifyInternalSHA(ctx, newRelative, expectedSHA)
		if err != nil || !sameStableObject(source, post) {
			if rollbackErr := renameAtNoReplace(newParent, newName, oldParent, oldName); rollbackErr != nil {
				return objectFromStat(newRelative, expectedSHA, target), errors.Join(ErrObjectChanged, rollbackErr)
			}
			_ = unix.Fsync(oldParent)
			if oldParent != newParent {
				_ = unix.Fsync(newParent)
			}
			return nil, ErrObjectChanged
		}
		return objectFromStat(newRelative, expectedSHA, post), nil
	}
	return objectFromStat(newRelative, "", target), nil
}

func (s *CatalogStore) rollbackMoveAfterSyncFailure(oldParent int, oldName string, newParent int, newName string,
	source, target unix.Stat_t, expectedSHA string, cause error,
) (*Object, error) {
	if rollbackErr := renameAtNoReplace(newParent, newName, oldParent, oldName); rollbackErr != nil {
		return objectFromStat(newName, expectedSHA, target), errors.Join(cause, rollbackErr)
	}
	restoredFD, openErr := unix.Openat(oldParent, oldName, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	var restored unix.Stat_t
	if openErr == nil {
		openErr = unix.Fstat(restoredFD, &restored)
		_ = unix.Close(restoredFD)
	}
	if openErr != nil || !sameStableObject(source, restored) {
		return objectFromStat(oldName, expectedSHA, restored), errors.Join(cause, ErrObjectChanged)
	}
	if err := unix.Fsync(oldParent); err != nil {
		return objectFromStat(oldName, expectedSHA, restored), errors.Join(cause, err)
	}
	if oldParent != newParent {
		if err := unix.Fsync(newParent); err != nil {
			return objectFromStat(oldName, expectedSHA, restored), errors.Join(cause, err)
		}
	}
	return nil, cause
}

// Quarantine atomically removes a regular catalog file from its public name
// while retaining it under a private deterministic root entry until the
// corresponding SQLite transaction commits.
func (s *CatalogStore) Quarantine(relative, expectedSHA, expectedVersion, operationID string, index int) (*CatalogQuarantine, error) {
	if !validCatalogPath(relative) || !isLowerHex(operationID) || len(operationID) != 32 || index < 0 {
		return nil, errUnsafePath
	}
	hidden := ".wsm-trash-" + operationID + "-" + string(rune('a'+index))
	if index > 25 {
		return nil, errUnsafePath
	}
	oldParent, oldName, err := openParent(s.rootFD, relative)
	if err != nil {
		return nil, err
	}
	defer unix.Close(oldParent)
	file, err := s.openRegular(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := statFile(file)
	if err != nil || stat.Nlink != 1 || versionForStat(stat, expectedSHA) != expectedVersion {
		return nil, ErrInvalidObject
	}
	verified, err := s.verifyInternalSHA(context.Background(), relative, expectedSHA)
	if err != nil || !sameStableObject(stat, verified) {
		return nil, ErrObjectChanged
	}
	stat = verified
	root, err := unix.Dup(s.rootFD)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	if s.beforeMove != nil {
		s.beforeMove()
	}
	if err := renameAtNoReplace(oldParent, oldName, root, hidden); err != nil {
		return nil, err
	}
	hiddenFD, openErr := openBeneath(s.rootFD, hidden, unix.O_PATH, 0)
	var moved unix.Stat_t
	if openErr == nil {
		openErr = unix.Fstat(hiddenFD, &moved)
		_ = unix.Close(hiddenFD)
	}
	if openErr != nil || !sameStableObject(stat, moved) {
		if rollbackErr := renameAtNoReplace(root, hidden, oldParent, oldName); rollbackErr != nil {
			return &CatalogQuarantine{store: s, original: relative, hidden: hidden, stat: moved, sha: expectedSHA}, errors.Join(ErrObjectChanged, rollbackErr)
		}
		return nil, ErrObjectChanged
	}
	post, verifyErr := s.verifyInternalSHA(context.Background(), hidden, expectedSHA)
	if verifyErr != nil || !sameStableObject(stat, post) {
		if rollbackErr := renameAtNoReplace(root, hidden, oldParent, oldName); rollbackErr != nil {
			return &CatalogQuarantine{store: s, original: relative, hidden: hidden, stat: moved, sha: expectedSHA}, errors.Join(ErrObjectChanged, rollbackErr)
		}
		return nil, ErrObjectChanged
	}
	quarantine := &CatalogQuarantine{store: s, original: relative, hidden: hidden, stat: post, sha: expectedSHA}
	if err := unix.Fsync(oldParent); err != nil {
		return quarantine, err
	}
	if err := unix.Fsync(root); err != nil {
		return quarantine, err
	}
	return quarantine, nil
}

func (q *CatalogQuarantine) Restore() error {
	if q == nil || q.store == nil || q.resolved || !validInternalName(q.hidden, ".wsm-trash-") || !validCatalogPath(q.original) {
		return ErrInvalidObject
	}
	current, err := q.store.verifyInternalSHA(context.Background(), q.hidden, q.sha)
	if err != nil || !sameStableObject(current, q.stat) {
		return ErrObjectChanged
	}
	root, err := unix.Dup(q.store.rootFD)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	targetParent, targetName, err := openParent(q.store.rootFD, q.original)
	if err != nil {
		return err
	}
	defer unix.Close(targetParent)
	if err := renameAtNoReplace(root, q.hidden, targetParent, targetName); err != nil {
		return err
	}
	if err := unix.Fsync(root); err != nil {
		return err
	}
	if err := unix.Fsync(targetParent); err != nil {
		return err
	}
	restored, err := q.store.statInternal(q.original)
	if err != nil || !sameStableObject(restored, q.stat) {
		return ErrObjectChanged
	}
	verified, err := q.store.verifyInternalSHA(context.Background(), q.original, q.sha)
	if err != nil || !sameStableObject(verified, q.stat) {
		return ErrObjectChanged
	}
	q.Restored = objectFromStat(q.original, q.sha, verified)
	q.resolved = true
	return nil
}

func (q *CatalogQuarantine) Discard() error {
	if q == nil || q.store == nil || q.resolved || !validInternalName(q.hidden, ".wsm-trash-") {
		return ErrInvalidObject
	}
	current, err := q.store.verifyInternalSHA(context.Background(), q.hidden, q.sha)
	if err != nil || !sameStableObject(current, q.stat) {
		return ErrObjectChanged
	}
	if err := removeRegularExpected(q.store.rootFD, q.hidden, gcPathForOperation(q.hidden), q.stat); err != nil {
		return err
	}
	if err := unix.Fsync(q.store.rootFD); err != nil {
		return err
	}
	q.resolved = true
	return nil
}

// QuarantineEmptyFolder atomically hides an expected empty directory until
// the database delete commits. The held inode identity prevents a source
// substitution from being accepted after rename.
func (s *CatalogStore) QuarantineEmptyFolder(relative, expectedVersion, operationID string) (*CatalogFolderQuarantine, error) {
	if !validCatalogPath(relative) || !isLowerHex(operationID) || len(operationID) != 32 {
		return nil, errUnsafePath
	}
	hidden := ".wsm-trash-" + operationID + "-dir"
	parent, name, err := openParent(s.rootFD, relative)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := openBeneath(s.rootFD, relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "catalog-folder")
	defer directory.Close()
	var stat unix.Stat_t
	err = unix.Fstat(int(directory.Fd()), &stat)
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 {
		return nil, ErrInvalidObject
	}
	if expectedVersion != "" && versionForStat(stat, "") != expectedVersion {
		return nil, ErrObjectChanged
	}
	if names, readErr := directory.Readdirnames(1); readErr == nil || len(names) != 0 || !errors.Is(readErr, io.EOF) {
		return nil, unix.ENOTEMPTY
	}
	root, err := unix.Dup(s.rootFD)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	if s.beforeMove != nil {
		s.beforeMove()
	}
	if err := renameAtNoReplace(parent, name, root, hidden); err != nil {
		return nil, err
	}
	movedFD := -1
	var openErr error
	movedFD, openErr = openBeneath(s.rootFD, hidden, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	var moved unix.Stat_t
	if openErr == nil {
		openErr = unix.Fstat(movedFD, &moved)
	}
	if openErr != nil || !sameStableObject(stat, moved) {
		if movedFD >= 0 {
			_ = unix.Close(movedFD)
		}
		if rollbackErr := renameAtNoReplace(root, hidden, parent, name); rollbackErr != nil {
			return &CatalogFolderQuarantine{store: s, original: relative, hidden: hidden, stat: moved}, errors.Join(ErrObjectChanged, rollbackErr)
		}
		return nil, ErrObjectChanged
	}
	movedDirectory := os.NewFile(uintptr(movedFD), "catalog-folder-hidden")
	names, readErr := movedDirectory.Readdirnames(1)
	_ = movedDirectory.Close()
	if readErr == nil || len(names) != 0 || !errors.Is(readErr, io.EOF) {
		if rollbackErr := renameAtNoReplace(root, hidden, parent, name); rollbackErr != nil {
			return &CatalogFolderQuarantine{store: s, original: relative, hidden: hidden, stat: moved}, errors.Join(unix.ENOTEMPTY, rollbackErr)
		}
		return nil, unix.ENOTEMPTY
	}
	quarantine := &CatalogFolderQuarantine{store: s, original: relative, hidden: hidden, stat: moved}
	if err := unix.Fsync(parent); err != nil {
		return quarantine, err
	}
	if err := unix.Fsync(root); err != nil {
		return quarantine, err
	}
	return quarantine, nil
}

func (q *CatalogFolderQuarantine) Restore() error {
	if q == nil || q.store == nil || q.resolved || !validInternalName(q.hidden, ".wsm-trash-") || !validCatalogPath(q.original) {
		return ErrInvalidObject
	}
	current, err := statDirectory(q.store.rootFD, q.hidden)
	if err != nil || !sameStableObject(current, q.stat) {
		return ErrObjectChanged
	}
	root, err := unix.Dup(q.store.rootFD)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	parent, name, err := openParent(q.store.rootFD, q.original)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	if err := renameAtNoReplace(root, q.hidden, parent, name); err != nil {
		return err
	}
	restored, err := statDirectory(q.store.rootFD, q.original)
	if err != nil || !sameStableObject(restored, q.stat) {
		return ErrObjectChanged
	}
	if err := unix.Fsync(root); err != nil {
		return err
	}
	if err := unix.Fsync(parent); err != nil {
		return err
	}
	q.resolved = true
	return nil
}

func (q *CatalogFolderQuarantine) Discard() error {
	if q == nil || q.store == nil || q.resolved || !validInternalName(q.hidden, ".wsm-trash-") {
		return ErrInvalidObject
	}
	current, err := statDirectory(q.store.rootFD, q.hidden)
	if err != nil || !sameStableObject(current, q.stat) {
		return ErrObjectChanged
	}
	root, err := unix.Dup(q.store.rootFD)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	if err := unix.Unlinkat(root, q.hidden, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err := unix.Fsync(root); err != nil {
		return err
	}
	q.resolved = true
	return nil
}

// Publish copies one durable staging upload into PROGRAM_PREFIX. If a target
// exists, RENAME_EXCHANGE keeps the old file recoverable until Commit.
func (s *CatalogStore) Publish(ctx context.Context, staged *StagedObject, relative, expectedSHA, expectedVersion, operationID string) (_ *CatalogPublication, err error) {
	return s.publish(ctx, staged, relative, expectedSHA, expectedVersion, operationID, nil)
}

// PublishPrepared durably records the prospective inode through beforeVisible
// before it acquires any pathname in PROGRAM_PREFIX.  Callers use this to bind
// their operation journal to the inode that a later rename may expose.
func (s *CatalogStore) PublishPrepared(ctx context.Context, staged *StagedObject, relative, expectedSHA, expectedVersion,
	operationID string, beforeVisible func(*Object) error,
) (_ *CatalogPublication, err error) {
	if beforeVisible == nil {
		return nil, ErrInvalidObject
	}
	return s.publish(ctx, staged, relative, expectedSHA, expectedVersion, operationID, beforeVisible)
}

func (s *CatalogStore) publish(ctx context.Context, staged *StagedObject, relative, expectedSHA, expectedVersion,
	operationID string, beforeVisible func(*Object) error,
) (_ *CatalogPublication, err error) {
	if !validCatalogPath(relative) || !s.staging.validStaged(staged) || !isLowerHex(operationID) || len(operationID) != 32 {
		return nil, ErrInvalidObject
	}
	if err := s.requireFree(uint64(max(staged.Size, 0))); err != nil {
		return nil, err
	}
	source, err := s.staging.OpenStaged(staged)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	before, err := statFile(source)
	if err != nil {
		return nil, ErrInvalidObject
	}
	parentPath := path.Dir(relative)
	if parentPath == "." {
		parentPath = ""
	}
	tempName := ".wsm-upload-" + operationID
	tempRelative := tempName
	if parentPath != "" {
		tempRelative = parentPath + "/" + tempName
	}
	tempParent, stagedName, err := openParent(s.rootFD, tempRelative)
	if err != nil {
		return nil, err
	}
	defer unix.Close(tempParent)
	destinationFD, err := unix.Openat(tempParent, ".", unix.O_WRONLY|unix.O_TMPFILE|unix.O_CLOEXEC, uint32(s.fileMode.Perm()))
	if err != nil {
		return nil, errors.New("unnamed atomic catalog staging is unavailable")
	}
	destination := os.NewFile(uintptr(destinationFD), "catalog-upload")
	tempExists := false
	var destinationStat unix.Stat_t
	destinationClosed := false
	defer func() {
		var closeErr error
		if !destinationClosed {
			closeErr = destination.Close()
		}
		if tempExists {
			_ = removeRegularExpected(s.rootFD, tempRelative, gcPathForOperation(tempRelative), destinationStat)
		}
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	hash := sha256.New()
	written, err := copyContext(ctx, io.MultiWriter(destination, hash), source, func(total int64) error {
		if total%spaceCheckEvery < copyBufferSize {
			return s.requireFree(minimumFreeReserve)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	after, err := statFile(source)
	if err != nil || !sameStat(before, after) || written != staged.Size ||
		subtle.ConstantTimeCompare([]byte(hex.EncodeToString(hash.Sum(nil))), []byte(staged.SHA256)) != 1 {
		return nil, ErrObjectChanged
	}
	if err := destination.Chmod(s.fileMode); err != nil || destination.Sync() != nil {
		return nil, errors.New("sync catalog publication")
	}
	if err := unix.Fstat(destinationFD, &destinationStat); err != nil {
		return nil, errors.New("inspect atomic catalog staging")
	}
	if beforeVisible != nil {
		if err := beforeVisible(objectFromStat(relative, staged.SHA256, destinationStat)); err != nil {
			return nil, err
		}
	}
	if err := unix.Linkat(destinationFD, "", tempParent, stagedName, unix.AT_EMPTY_PATH); err != nil {
		return nil, errors.New("link atomic catalog staging")
	}
	tempExists = true
	if err := unix.Fstat(destinationFD, &destinationStat); err != nil || unix.Fsync(tempParent) != nil || destination.Close() != nil {
		return nil, errors.New("sync linked catalog publication")
	}
	destinationClosed = true
	tempCopiedStat, err := s.verifyInternalSHA(ctx, tempRelative, staged.SHA256)
	if err != nil || !sameStableObject(tempCopiedStat, destinationStat) {
		if err != nil {
			return nil, err
		}
		return nil, ErrObjectChanged
	}

	targetParent, targetName, err := openParent(s.rootFD, relative)
	if err != nil {
		return nil, err
	}
	defer unix.Close(targetParent)
	var current unix.Stat_t
	statErr := unix.Fstatat(targetParent, targetName, &current, unix.AT_SYMLINK_NOFOLLOW)
	replacement := statErr == nil
	if statErr != nil && !errors.Is(statErr, unix.ENOENT) {
		return nil, statErr
	}
	if replacement && (current.Mode&unix.S_IFMT != unix.S_IFREG || current.Nlink != 1) {
		return nil, ErrInvalidObject
	}
	if expectedVersion == "" && replacement || expectedVersion != "" && !replacement {
		return nil, ErrObjectChanged
	}
	if replacement && versionForStat(current, expectedSHA) != expectedVersion {
		return nil, ErrObjectChanged
	}
	if replacement {
		if s.beforeExchange != nil {
			s.beforeExchange()
		}
		if err := unix.Renameat2(tempParent, stagedName, targetParent, targetName, unix.RENAME_EXCHANGE); err != nil {
			return nil, errors.New("atomic catalog replacement unavailable")
		}
		tempExists = false
		exchanged, verifyErr := s.verifyInternalSHA(ctx, tempRelative, expectedSHA)
		if verifyErr != nil || !sameStableObject(current, exchanged) {
			publicNew, publicErr := s.statInternal(relative)
			var hiddenOld unix.Stat_t
			hiddenErr := unix.Fstatat(tempParent, stagedName, &hiddenOld, unix.AT_SYMLINK_NOFOLLOW)
			if publicErr != nil || hiddenErr != nil || !sameStableObject(publicNew, tempCopiedStat) {
				return nil, errors.Join(ErrObjectChanged, errors.New("catalog replacement requires recovery"))
			}
			if rollbackErr := unix.Renameat2(tempParent, stagedName, targetParent, targetName, unix.RENAME_EXCHANGE); rollbackErr != nil {
				return nil, errors.Join(ErrObjectChanged, rollbackErr)
			}
			if cleanupErr := removeRegularExpected(s.rootFD, tempRelative, gcPathForOperation(tempRelative), tempCopiedStat); cleanupErr != nil {
				return nil, errors.Join(ErrObjectChanged, cleanupErr)
			}
			_ = unix.Fsync(targetParent)
			return nil, ErrObjectChanged
		}
	} else if err := renameAtNoReplace(tempParent, stagedName, targetParent, targetName); err != nil {
		return nil, err
	} else {
		tempExists = false
	}
	publication := &CatalogPublication{store: s, target: relative, oldTemp: tempRelative, replacement: replacement,
		newSHA: staged.SHA256, oldSHA: expectedSHA}
	defer func() {
		if err != nil && !publication.resolved {
			if rollbackErr := publication.Rollback(); rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
		}
	}()
	if err := unix.Fsync(targetParent); err != nil {
		return nil, err
	}
	newStat, err := s.verifyInternalSHA(ctx, relative, staged.SHA256)
	if err != nil {
		return nil, err
	}
	publication.newStat = newStat
	if replacement {
		publication.oldStat, err = s.statInternal(tempRelative)
		if err != nil {
			return nil, err
		}
	}
	object := objectFromStat(relative, staged.SHA256, newStat)
	if err := s.staging.Discard(staged); err != nil {
		return nil, err
	}
	publication.Object = object
	return publication, nil
}

func (p *CatalogPublication) Commit() error {
	if p == nil || p.store == nil || p.resolved {
		return ErrInvalidObject
	}
	current, err := p.store.verifyInternalSHA(context.Background(), p.target, p.newSHA)
	if err != nil || !sameStableObject(current, p.newStat) {
		return ErrObjectChanged
	}
	if !p.replacement {
		p.resolved = true
		return nil
	}
	old, err := p.store.verifyInternalSHA(context.Background(), p.oldTemp, p.oldSHA)
	if err != nil || !sameStableObject(old, p.oldStat) {
		return ErrObjectChanged
	}
	if err := removeRegularExpected(p.store.rootFD, p.oldTemp, gcPathForOperation(p.oldTemp), p.oldStat); err != nil {
		return err
	}
	if err := syncParent(p.store.rootFD, p.oldTemp); err != nil {
		return err
	}
	p.resolved = true
	return nil
}

func (p *CatalogPublication) Rollback() error {
	if p == nil || p.store == nil || p.resolved {
		return ErrInvalidObject
	}
	if !p.replacement {
		if err := removeRegularExpected(p.store.rootFD, p.target, gcPathForOperation(p.oldTemp), p.newStat); err != nil {
			return err
		}
		if err := syncParent(p.store.rootFD, p.target); err != nil {
			return err
		}
		p.resolved = true
		return nil
	}
	currentNew, err := p.store.verifyInternalSHA(context.Background(), p.target, p.newSHA)
	if err != nil || !sameStableObject(currentNew, p.newStat) {
		return ErrObjectChanged
	}
	currentOld, err := p.store.verifyInternalSHA(context.Background(), p.oldTemp, p.oldSHA)
	if err != nil || !sameStableObject(currentOld, p.oldStat) {
		return ErrObjectChanged
	}
	oldParent, oldName, err := openParent(p.store.rootFD, p.oldTemp)
	if err != nil {
		return err
	}
	defer unix.Close(oldParent)
	targetParent, targetName, err := openParent(p.store.rootFD, p.target)
	if err != nil {
		return err
	}
	defer unix.Close(targetParent)
	if err := unix.Renameat2(oldParent, oldName, targetParent, targetName, unix.RENAME_EXCHANGE); err != nil {
		return err
	}
	if err := unix.Fsync(targetParent); err != nil {
		return err
	}
	newAtTemp, err := p.store.statInternal(p.oldTemp)
	if err != nil || !sameStableObject(newAtTemp, p.newStat) {
		return ErrObjectChanged
	}
	if err := removeRegularExpected(p.store.rootFD, p.oldTemp, gcPathForOperation(p.oldTemp), newAtTemp); err != nil {
		return err
	}
	if err := syncParent(p.store.rootFD, p.oldTemp); err != nil {
		return err
	}
	restored, err := p.store.verifyInternalSHA(context.Background(), p.target, p.oldSHA)
	if err != nil || !sameStableObject(restored, p.oldStat) {
		return ErrObjectChanged
	}
	p.Restored = objectFromStat(p.target, p.oldSHA, restored)
	p.resolved = true
	return nil
}

func (s *CatalogStore) Inspect(relative, expectedSHA, expectedVersion string) (*Object, error) {
	var stat unix.Stat_t
	var err error
	if expectedSHA != "" {
		stat, err = s.verifyInternalSHA(context.Background(), relative, expectedSHA)
	} else {
		var file *os.File
		file, err = s.openRegular(relative)
		if err == nil {
			stat, err = statFile(file)
			_ = file.Close()
		}
	}
	if err != nil {
		return nil, err
	}
	object := objectFromStat(relative, expectedSHA, stat)
	if expectedVersion != "" && subtle.ConstantTimeCompare([]byte(object.Version), []byte(expectedVersion)) != 1 {
		return nil, ErrObjectChanged
	}
	return object, nil
}

// InspectMetadata binds a cheap metadata read to the persisted digest/version
// without rereading file bytes.  The version includes inode, size and both
// timestamps, so an in-place write is detected while large Range requests stay
// O(requested bytes). Full hashing remains mandatory for publication/recovery.
func (s *CatalogStore) InspectMetadata(relative, persistedSHA, expectedVersion string) (*Object, error) {
	if !isLowerHex(persistedSHA) || len(persistedSHA) != sha256.Size*2 || expectedVersion == "" {
		return nil, ErrInvalidObject
	}
	file, err := s.openRegular(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := statFile(file)
	if err != nil {
		return nil, err
	}
	object := objectFromStat(relative, persistedSHA, stat)
	if subtle.ConstantTimeCompare([]byte(object.Version), []byte(expectedVersion)) != 1 {
		return nil, ErrObjectChanged
	}
	return object, nil
}

func (s *CatalogStore) InspectFolder(relative, expectedVersion string) (*Object, error) {
	if !validCatalogPath(relative) {
		return nil, ErrInvalidObject
	}
	stat, err := statDirectory(s.rootFD, relative)
	if err != nil {
		return nil, err
	}
	object := objectFromStat(relative, "", stat)
	if expectedVersion != "" && subtle.ConstantTimeCompare([]byte(object.Version), []byte(expectedVersion)) != 1 {
		return nil, ErrObjectChanged
	}
	return object, nil
}

// RecoverPublication reconciles a durable publish intent after restart.  The
// database state selects forward cleanup or rollback; hashes bind every inode
// touched so unrelated files are never removed.
func (s *CatalogStore) RecoverPublication(ctx context.Context, target, temporary, oldSHA, newSHA,
	expectedVersion, resultVersion string, expectedDevice, expectedInode, resultDevice, resultInode uint64,
	expectedSize, resultSize int64, dbWantsNew bool,
) (*Object, error) {
	if !validCatalogPath(target) || !validOperationInternalPath(target, temporary, ".wsm-upload-") ||
		!isLowerHex(newSHA) || len(newSHA) != sha256.Size*2 || oldSHA != "" && (!isLowerHex(oldSHA) || len(oldSHA) != sha256.Size*2) {
		return nil, ErrInvalidObject
	}
	sameContent := oldSHA != "" && subtle.ConstantTimeCompare([]byte(oldSHA), []byte(newSHA)) == 1
	targetExists, err := internalEntryExists(s.rootFD, target)
	if err != nil {
		return nil, err
	}
	temporaryExists, err := internalEntryExists(s.rootFD, temporary)
	if err != nil {
		return nil, err
	}
	guard := gcPathForOperation(temporary)
	guardExists, err := internalEntryExists(s.rootFD, guard)
	if err != nil {
		return nil, err
	}
	if guardExists {
		expectedGuardSHA := newSHA
		expectedGuardDevice, expectedGuardInode, expectedGuardSize := resultDevice, resultInode, resultSize
		if dbWantsNew {
			expectedGuardSHA = oldSHA
			expectedGuardDevice, expectedGuardInode, expectedGuardSize = expectedDevice, expectedInode, expectedSize
		}
		if expectedGuardSHA == "" || expectedGuardDevice == 0 || expectedGuardInode == 0 {
			return nil, ErrObjectChanged
		}
		guardStat, matches, matchErr := s.matchInternalSHA(ctx, guard, expectedGuardSHA)
		if matchErr != nil {
			return nil, matchErr
		}
		if !matches || uint64(guardStat.Dev) != expectedGuardDevice || guardStat.Ino != expectedGuardInode ||
			guardStat.Size != expectedGuardSize {
			return nil, ErrObjectChanged
		}
		if err := unlinkJournalGuardExpected(s.rootFD, guard, guardStat); err != nil {
			return nil, err
		}
	}
	newTarget, targetIsNew, newErr := s.matchInternalSHA(ctx, target, newSHA)
	if newErr != nil {
		return nil, newErr
	}
	var oldTarget unix.Stat_t
	targetIsOld := false
	if !targetIsNew && oldSHA != "" {
		oldTarget, targetIsOld, newErr = s.matchInternalSHA(ctx, target, oldSHA)
		if newErr != nil {
			return nil, newErr
		}
	}
	newTemporary, temporaryIsNew, err := s.matchInternalSHA(ctx, temporary, newSHA)
	if err != nil {
		return nil, err
	}
	var oldTemporary unix.Stat_t
	temporaryIsOld := false
	if !temporaryIsNew && oldSHA != "" {
		oldTemporary, temporaryIsOld, err = s.matchInternalSHA(ctx, temporary, oldSHA)
		if err != nil {
			return nil, err
		}
	}
	if sameContent {
		if targetExists {
			version := versionForStat(newTarget, newSHA)
			switch {
			case version == expectedVersion:
				targetIsOld, targetIsNew = true, false
				oldTarget = newTarget
				temporaryIsNew, temporaryIsOld = temporaryExists, false
			case resultVersion == "" || version == resultVersion:
				targetIsNew, targetIsOld = true, false
				temporaryIsOld, temporaryIsNew = temporaryExists, false
				oldTemporary = newTemporary
			default:
				targetIsNew, targetIsOld = false, false
			}
		}
	}
	oldIdentityMatches := func(stat unix.Stat_t) bool {
		return oldSHA != "" && expectedDevice != 0 && expectedInode != 0 && uint64(stat.Dev) == expectedDevice &&
			stat.Ino == expectedInode && stat.Size == expectedSize
	}
	newIdentityMatches := func(stat unix.Stat_t) bool {
		return resultDevice != 0 && resultInode != 0 && uint64(stat.Dev) == resultDevice &&
			stat.Ino == resultInode && stat.Size == resultSize
	}
	if targetIsOld && !oldIdentityMatches(oldTarget) || temporaryIsOld && !oldIdentityMatches(oldTemporary) ||
		targetIsNew && !newIdentityMatches(newTarget) || temporaryIsNew && !newIdentityMatches(newTemporary) {
		return nil, ErrObjectChanged
	}
	if targetExists && !targetIsNew && !targetIsOld || temporaryExists && !temporaryIsNew && !temporaryIsOld {
		return nil, ErrObjectChanged
	}
	if dbWantsNew {
		if !targetIsNew {
			return nil, ErrObjectChanged
		}
		if temporaryIsOld {
			publication := &CatalogPublication{store: s, target: target, oldTemp: temporary, replacement: true,
				newStat: newTarget, oldStat: oldTemporary, newSHA: newSHA, oldSHA: oldSHA}
			if err := publication.Commit(); err != nil {
				return nil, err
			}
		} else if temporaryIsNew {
			return nil, ErrObjectChanged
		}
		return objectFromStat(target, newSHA, newTarget), nil
	}
	if targetIsOld {
		if temporaryIsNew {
			if err := removeRegularExpected(s.rootFD, temporary, gcPathForOperation(temporary), newTemporary); err != nil {
				return nil, err
			}
			if err := syncParent(s.rootFD, temporary); err != nil {
				return nil, err
			}
		} else if temporaryIsOld {
			return nil, ErrObjectChanged
		}
		return objectFromStat(target, oldSHA, oldTarget), nil
	}
	if targetIsNew {
		if oldSHA == "" {
			if err := removeRegularExpected(s.rootFD, target, gcPathForOperation(temporary), newTarget); err != nil {
				return nil, err
			}
			if err := syncParent(s.rootFD, target); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if !temporaryIsOld {
			return nil, ErrObjectChanged
		}
		publication := &CatalogPublication{store: s, target: target, oldTemp: temporary, replacement: true,
			newStat: newTarget, oldStat: oldTemporary, newSHA: newSHA, oldSHA: oldSHA}
		if err := publication.Rollback(); err != nil {
			return nil, err
		}
		return publication.Restored, nil
	}
	if temporaryIsNew {
		if err := removeRegularExpected(s.rootFD, temporary, gcPathForOperation(temporary), newTemporary); err != nil {
			return nil, err
		}
		if err := syncParent(s.rootFD, temporary); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if temporaryIsOld {
		return nil, ErrObjectChanged
	}
	return nil, nil
}

func (s *CatalogStore) RecoverQuarantine(ctx context.Context, target, temporary, sha string,
	expectedDevice, expectedInode uint64, expectedSize int64, dbDeleted bool,
) (*Object, error) {
	if !validCatalogPath(target) || !validOperationInternalPath(target, temporary, ".wsm-trash-") ||
		!isLowerHex(sha) || len(sha) != sha256.Size*2 {
		return nil, ErrInvalidObject
	}
	targetEntryExists, err := internalEntryExists(s.rootFD, target)
	if err != nil {
		return nil, err
	}
	guard := gcPathForOperation(temporary)
	guardEntryExists, err := internalEntryExists(s.rootFD, guard)
	if err != nil {
		return nil, err
	}
	if guardEntryExists {
		guardStat, matches, matchErr := s.matchInternalSHA(ctx, guard, sha)
		if matchErr != nil {
			return nil, matchErr
		}
		if !dbDeleted || !matches {
			return nil, ErrObjectChanged
		}
		if expectedDevice == 0 || expectedInode == 0 || uint64(guardStat.Dev) != expectedDevice ||
			guardStat.Ino != expectedInode || guardStat.Size != expectedSize {
			return nil, ErrObjectChanged
		}
		if err := unlinkJournalGuardExpected(s.rootFD, guard, guardStat); err != nil {
			return nil, err
		}
	}
	hiddenEntryExists, err := internalEntryExists(s.rootFD, temporary)
	if err != nil {
		return nil, err
	}
	targetStat, targetExists, err := s.matchInternalSHA(ctx, target, sha)
	if err != nil {
		return nil, err
	}
	hiddenStat, hiddenExists, err := s.matchInternalSHA(ctx, temporary, sha)
	if err != nil {
		return nil, err
	}
	if targetEntryExists && !targetExists || hiddenEntryExists && !hiddenExists {
		return nil, ErrObjectChanged
	}
	identityMatches := func(stat unix.Stat_t) bool {
		return expectedDevice != 0 && expectedInode != 0 && uint64(stat.Dev) == expectedDevice &&
			stat.Ino == expectedInode && stat.Size == expectedSize
	}
	if targetExists && !identityMatches(targetStat) || hiddenExists && !identityMatches(hiddenStat) {
		return nil, ErrObjectChanged
	}
	if targetExists && hiddenExists {
		return nil, ErrObjectChanged
	}
	if dbDeleted {
		if targetExists {
			return nil, ErrObjectChanged
		}
		if hiddenExists {
			quarantine := &CatalogQuarantine{store: s, original: target, hidden: temporary, stat: hiddenStat, sha: sha}
			if err := quarantine.Discard(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if targetExists {
		return objectFromStat(target, sha, targetStat), nil
	}
	if !hiddenExists {
		return nil, ErrObjectChanged
	}
	quarantine := &CatalogQuarantine{store: s, original: target, hidden: temporary, stat: hiddenStat, sha: sha}
	if err := quarantine.Restore(); err != nil {
		return nil, err
	}
	return quarantine.Restored, nil
}

func (s *CatalogStore) RecoverFolderQuarantine(target, temporary string, expectedDevice, expectedInode uint64,
	expectedSize int64, dbDeleted bool,
) error {
	if !validCatalogPath(target) || !validOperationInternalPath(target, temporary, ".wsm-trash-") {
		return ErrInvalidObject
	}
	targetEntry, err := internalEntryExists(s.rootFD, target)
	if err != nil {
		return err
	}
	hiddenEntry, err := internalEntryExists(s.rootFD, temporary)
	if err != nil {
		return err
	}
	targetStat, targetDirectory, err := maybeDirectory(s.rootFD, target)
	if err != nil {
		return err
	}
	hiddenStat, hiddenDirectory, err := maybeDirectory(s.rootFD, temporary)
	if err != nil {
		return err
	}
	if targetEntry && !targetDirectory || hiddenEntry && !hiddenDirectory || targetDirectory && hiddenDirectory {
		return ErrObjectChanged
	}
	identityMatches := func(stat unix.Stat_t) bool {
		return expectedDevice == 0 && expectedInode == 0 || uint64(stat.Dev) == expectedDevice && stat.Ino == expectedInode && stat.Size == expectedSize
	}
	if targetDirectory && !identityMatches(targetStat) || hiddenDirectory && !identityMatches(hiddenStat) {
		return ErrObjectChanged
	}
	if dbDeleted {
		if targetDirectory {
			return ErrObjectChanged
		}
		if hiddenDirectory {
			quarantine := &CatalogFolderQuarantine{store: s, original: target, hidden: temporary, stat: hiddenStat}
			return quarantine.Discard()
		}
		return nil
	}
	if targetDirectory {
		return nil
	}
	if !hiddenDirectory {
		return ErrObjectChanged
	}
	quarantine := &CatalogFolderQuarantine{store: s, original: target, hidden: temporary, stat: hiddenStat}
	return quarantine.Restore()
}

// RecoverFolderCreate completes or rolls back a prepared directory create.
// Both its private and public names are accepted only when they resolve to the
// inode captured before publication.
func (s *CatalogStore) RecoverFolderCreate(target, temporary, resultVersion string,
	resultDevice, resultInode uint64, resultSize int64, dbWantsNew bool,
) (*Object, error) {
	if !validCatalogPath(target) || !validOperationInternalPath(target, temporary, ".wsm-create-") {
		return nil, ErrInvalidObject
	}
	guard := gcPathForOperation(temporary)
	guardEntry, err := internalEntryExists(s.rootFD, guard)
	if err != nil {
		return nil, err
	}
	guardStat, guardDirectory, err := maybeDirectory(s.rootFD, guard)
	if err != nil {
		return nil, err
	}
	identityMatches := func(stat unix.Stat_t) bool {
		return resultDevice != 0 && resultInode != 0 && uint64(stat.Dev) == resultDevice &&
			stat.Ino == resultInode && stat.Size == resultSize
	}
	if guardEntry {
		if !guardDirectory || dbWantsNew || !identityMatches(guardStat) {
			return nil, ErrObjectChanged
		}
		if err := unlinkDirectoryGuardExpected(s.rootFD, guard, guardStat); err != nil {
			return nil, err
		}
	}
	targetEntry, err := internalEntryExists(s.rootFD, target)
	if err != nil {
		return nil, err
	}
	temporaryEntry, err := internalEntryExists(s.rootFD, temporary)
	if err != nil {
		return nil, err
	}
	targetStat, targetDirectory, err := maybeDirectory(s.rootFD, target)
	if err != nil {
		return nil, err
	}
	temporaryStat, temporaryDirectory, err := maybeDirectory(s.rootFD, temporary)
	if err != nil {
		return nil, err
	}
	if targetEntry && !targetDirectory || temporaryEntry && !temporaryDirectory ||
		targetDirectory && temporaryDirectory {
		return nil, ErrObjectChanged
	}
	if targetDirectory && !identityMatches(targetStat) || temporaryDirectory && !identityMatches(temporaryStat) {
		return nil, ErrObjectChanged
	}
	if !targetDirectory && !temporaryDirectory {
		if dbWantsNew {
			return nil, ErrObjectChanged
		}
		return nil, nil
	}
	if dbWantsNew {
		if temporaryDirectory {
			parent, targetName, err := openParent(s.rootFD, target)
			if err != nil {
				return nil, err
			}
			defer unix.Close(parent)
			if err := renameAtNoReplace(parent, path.Base(temporary), parent, targetName); err != nil {
				return nil, err
			}
			if err := unix.Fsync(parent); err != nil {
				return nil, err
			}
			targetStat = temporaryStat
		}
		object := objectFromStat(target, "", targetStat)
		if resultVersion != "" && object.Version != resultVersion {
			// A rename changes ctime on some filesystems. Identity remains the
			// authoritative binding; return the current version for DB refresh.
			resultVersion = object.Version
		}
		return object, nil
	}
	stat := targetStat
	relative := target
	if temporaryDirectory {
		stat, relative = temporaryStat, temporary
	}
	if err := removeEmptyDirectoryExpected(s.rootFD, relative, guard, stat); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *CatalogStore) matchInternalSHA(ctx context.Context, relative, sha string) (unix.Stat_t, bool, error) {
	stat, err := s.verifyInternalSHA(ctx, relative, sha)
	if errors.Is(err, unix.ENOENT) {
		return unix.Stat_t{}, false, nil
	}
	if errors.Is(err, ErrObjectChanged) {
		return unix.Stat_t{}, false, nil
	}
	if err != nil {
		return unix.Stat_t{}, false, err
	}
	return stat, true, nil
}

func internalEntryExists(rootFD int, relative string) (bool, error) {
	parent, name, err := openParent(rootFD, relative)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(parent)
	var stat unix.Stat_t
	err = unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return err == nil, err
}

func maybeDirectory(rootFD int, relative string) (unix.Stat_t, bool, error) {
	stat, err := statDirectory(rootFD, relative)
	if errors.Is(err, unix.ENOENT) {
		return unix.Stat_t{}, false, nil
	}
	if err != nil {
		return unix.Stat_t{}, false, nil
	}
	return stat, true, nil
}

func (s *CatalogStore) ReadRange(ctx context.Context, relative, expectedSHA, expectedVersion string, offset int64, destination []byte) (int, int64, error) {
	if offset < 0 || len(destination) == 0 {
		return 0, 0, ErrInvalidObject
	}
	file, err := s.openRegular(relative)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	before, err := statFile(file)
	if err != nil {
		return 0, 0, err
	}
	if versionForStat(before, expectedSHA) != expectedVersion {
		return 0, before.Size, ErrObjectChanged
	}
	if offset >= before.Size {
		return 0, before.Size, io.EOF
	}
	if int64(len(destination)) > before.Size-offset {
		destination = destination[:before.Size-offset]
	}
	count, readErr := file.ReadAt(destination, offset)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return count, before.Size, readErr
	}
	if err := ctx.Err(); err != nil {
		return 0, before.Size, err
	}
	after, err := statFile(file)
	if err != nil || !sameStat(before, after) || versionForStat(after, expectedSHA) != expectedVersion {
		return 0, before.Size, ErrObjectChanged
	}
	return count, before.Size, nil
}

func (s *CatalogStore) openRegular(relative string) (*os.File, error) {
	if !validCatalogPath(relative) {
		return nil, ErrInvalidObject
	}
	fd, err := openBeneath(s.rootFD, relative, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "catalog-file")
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = file.Close()
		return nil, ErrInvalidObject
	}
	return file, nil
}

func (s *CatalogStore) statInternal(relative string) (unix.Stat_t, error) {
	fd, err := openBeneath(s.rootFD, relative, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return unix.Stat_t{}, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return unix.Stat_t{}, ErrInvalidObject
	}
	return stat, nil
}

func statDirectory(rootFD int, relative string) (unix.Stat_t, error) {
	fd, err := openBeneath(rootFD, relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return unix.Stat_t{}, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 {
		return unix.Stat_t{}, ErrInvalidObject
	}
	return stat, nil
}

func (s *CatalogStore) verifyInternalSHA(ctx context.Context, relative, expectedSHA string) (unix.Stat_t, error) {
	fd, err := openBeneath(s.rootFD, relative, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), "catalog-verification")
	defer file.Close()
	before, err := statFile(file)
	if err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 {
		return unix.Stat_t{}, ErrInvalidObject
	}
	hash := sha256.New()
	if _, err := copyContext(ctx, hash, file, nil); err != nil {
		return unix.Stat_t{}, err
	}
	after, err := statFile(file)
	if err != nil || !sameStat(before, after) || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(hash.Sum(nil))), []byte(expectedSHA)) != 1 {
		return unix.Stat_t{}, ErrObjectChanged
	}
	return after, nil
}

func (s *CatalogStore) requireFree(required uint64) error {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(s.rootFD, &stat); err != nil || stat.Bsize <= 0 {
		return errors.New("catalog filesystem is unavailable")
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	if required > ^uint64(0)-minimumFreeReserve || available < required+minimumFreeReserve {
		return ErrInsufficientStorage
	}
	return nil
}

func validCatalogPath(relative string) bool {
	components, err := safeComponents(relative)
	if err != nil || len(components) == 0 {
		return false
	}
	if strings.EqualFold(components[0], "ngcgui_lib") {
		return false
	}
	for _, component := range components {
		if strings.HasPrefix(component, ".wsm-") {
			return false
		}
	}
	return true
}

func renameAtNoReplace(oldParent int, oldName string, newParent int, newName string) error {
	err := unix.Renameat2(oldParent, oldName, newParent, newName, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return errors.New("atomic no-replace rename is unavailable")
	}
	return err
}

func removeRegularExpected(rootFD int, relative, guard string, expected unix.Stat_t) error {
	if !validInternalName(guard, ".wsm-gc-") {
		return ErrInvalidObject
	}
	parent, name, err := openParent(rootFD, relative)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	fd, err := openBeneath(rootFD, relative, unix.O_PATH, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil || !sameStableObject(actual, expected) ||
		actual.Mode&unix.S_IFMT != unix.S_IFREG || actual.Nlink != 1 {
		return ErrObjectChanged
	}
	root, err := unix.Dup(rootFD)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	if err := renameAtNoReplace(parent, name, root, guard); err != nil {
		return err
	}
	var moved unix.Stat_t
	if err := unix.Fstatat(root, guard, &moved, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameStableObject(moved, expected) {
		if rollbackErr := renameAtNoReplace(root, guard, parent, name); rollbackErr != nil {
			return errors.Join(ErrObjectChanged, rollbackErr)
		}
		return ErrObjectChanged
	}
	if err := unix.Fsync(parent); err != nil {
		return err
	}
	if err := unix.Fsync(root); err != nil {
		return err
	}
	if err := unix.Unlinkat(root, guard, 0); err != nil {
		return err
	}
	return unix.Fsync(root)
}

func unlinkJournalGuardExpected(rootFD int, guard string, expected unix.Stat_t) error {
	if !validInternalName(guard, ".wsm-gc-") {
		return ErrInvalidObject
	}
	fd, err := openBeneath(rootFD, guard, unix.O_PATH, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !sameStableObject(stat, expected) ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return ErrObjectChanged
	}
	if err := unix.Unlinkat(rootFD, guard, 0); err != nil {
		return err
	}
	return unix.Fsync(rootFD)
}

func removeEmptyDirectoryExpected(rootFD int, relative, guard string, expected unix.Stat_t) error {
	if !validInternalName(guard, ".wsm-gc-") {
		return ErrInvalidObject
	}
	parent, name, err := openParent(rootFD, relative)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	actual, err := statDirectory(rootFD, relative)
	if err != nil || !sameStableObject(actual, expected) {
		return ErrObjectChanged
	}
	root, err := unix.Dup(rootFD)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	if err := renameAtNoReplace(parent, name, root, guard); err != nil {
		return err
	}
	moved, err := statDirectory(rootFD, guard)
	if err != nil || !sameStableObject(moved, expected) {
		if rollbackErr := renameAtNoReplace(root, guard, parent, name); rollbackErr != nil {
			return errors.Join(ErrObjectChanged, rollbackErr)
		}
		return ErrObjectChanged
	}
	if err := unix.Fsync(parent); err != nil {
		return err
	}
	if err := unix.Fsync(root); err != nil {
		return err
	}
	return unlinkDirectoryGuardExpected(rootFD, guard, moved)
}

func unlinkDirectoryGuardExpected(rootFD int, guard string, expected unix.Stat_t) error {
	if !validInternalName(guard, ".wsm-gc-") {
		return ErrInvalidObject
	}
	actual, err := statDirectory(rootFD, guard)
	if err != nil || !sameStableObject(actual, expected) {
		return ErrObjectChanged
	}
	if err := unix.Unlinkat(rootFD, guard, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(rootFD)
}

func folderCreateTemporary(target, operationID string) string {
	name := ".wsm-create-" + operationID + "-dir"
	if parent := path.Dir(target); parent != "." {
		return parent + "/" + name
	}
	return name
}

func gcPathForOperation(temporary string) string {
	name := path.Base(temporary)
	name = strings.TrimPrefix(name, ".wsm-")
	return ".wsm-gc-" + name
}

// sameStableObject deliberately ignores timestamps: rename changes ctime.
// Dev/inode bind the opened inode while size/type/link-count reject aliases;
// callers hash regular-file bytes before relying on this predicate.
func sameStableObject(first, second unix.Stat_t) bool {
	return first.Dev == second.Dev && first.Ino == second.Ino && first.Size == second.Size &&
		first.Mode&unix.S_IFMT == second.Mode&unix.S_IFMT && first.Nlink == second.Nlink
}

func syncParent(rootFD int, relative string) error {
	parent, _, err := openParent(rootFD, relative)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return unix.Fsync(parent)
}

func validInternalName(name, prefix string) bool {
	return validComponent(name) && strings.HasPrefix(name, prefix) && !strings.Contains(name, "/")
}

func validOperationInternalPath(target, temporary, prefix string) bool {
	components, err := safeComponents(temporary)
	if err != nil || len(components) == 0 || !validInternalName(components[len(components)-1], prefix) {
		return false
	}
	for _, component := range components[:len(components)-1] {
		if strings.HasPrefix(component, ".wsm-") || strings.EqualFold(component, "ngcgui_lib") {
			return false
		}
	}
	if prefix == ".wsm-upload-" || prefix == ".wsm-create-" {
		return path.Dir(temporary) == path.Dir(target)
	}
	return len(components) == 1
}
