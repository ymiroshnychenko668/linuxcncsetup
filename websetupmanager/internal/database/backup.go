package database

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

const (
	backupStepPages  int32 = 128
	backupTempPrefix       = ".sqlite-backup-"
	backupTempSuffix       = ".tmp"
)

type sqliteBackuper interface {
	NewBackup(destinationURI string) (*sqlite.Backup, error)
}

func migrationBackupName(fromVersion, toVersion int64) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate migration backup identity: %w", err)
	}
	return fmt.Sprintf("websetupmanager.pre-migration-v%d-to-v%d-%s.sqlite3",
		fromVersion, toVersion, hex.EncodeToString(random[:])), nil
}

// Backup creates a consistent online SQLite backup using SQLite's backup API.
// Destination may be a basename or an absolute direct child of StateDir. It
// must not already exist; backups never overwrite prior recovery points.
func (d *DB) Backup(ctx context.Context, destination string) (finalErr error) {
	if d == nil || d.sql == nil || d.lock == nil {
		return driver.ErrBadConn
	}
	destinationName, err := d.backupBasename(destination)
	if err != nil {
		return err
	}
	if err := ensureEntryAbsent(d.lock.dirFD, destinationName); err != nil {
		return err
	}

	tempName, err := createBackupTemp(d.lock.dirFD)
	if err != nil {
		return fmt.Errorf("create database backup staging file: %w", err)
	}
	defer func() {
		if tempName != "" {
			_ = unix.Unlinkat(d.lock.dirFD, tempName, 0)
		}
	}()

	// Resolve through the already-open directory descriptor. This keeps the
	// SQLite destination rooted even if an external process renames StateDir.
	tempURI := fmt.Sprintf("/proc/self/fd/%d/%s", d.lock.dirFD, tempName)
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve SQLite backup connection: %w", err)
	}
	defer conn.Close()

	if err := conn.Raw(func(raw any) error {
		backuper, ok := raw.(sqliteBackuper)
		if !ok {
			return errors.New("SQLite driver does not expose online backup API")
		}
		backup, err := backuper.NewBackup(tempURI)
		if err != nil {
			return err
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(backupStepPages)
			if err != nil {
				return err
			}
			if !more {
				break
			}
		}
		err = backup.Finish()
		finished = true
		return err
	}); err != nil {
		return fmt.Errorf("create consistent SQLite backup: %w", err)
	}

	if err := syncRegularEntry(d.lock.dirFD, tempName); err != nil {
		return fmt.Errorf("sync SQLite backup: %w", err)
	}
	// linkat is an atomic no-replace publication on the same filesystem.
	if err := unix.Linkat(d.lock.dirFD, tempName, d.lock.dirFD, destinationName, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%w: destination already exists", ErrInvalidBackupDestination)
		}
		return fmt.Errorf("publish SQLite backup: %w", err)
	}
	if err := unix.Unlinkat(d.lock.dirFD, tempName, 0); err != nil {
		return fmt.Errorf("remove SQLite backup staging link: %w", err)
	}
	tempName = ""
	if err := unix.Fsync(d.lock.dirFD); err != nil {
		return fmt.Errorf("sync SQLite backup directory: %w", err)
	}
	return nil
}

func (d *DB) backupBasename(destination string) (string, error) {
	if destination == "" {
		return "", ErrInvalidBackupDestination
	}
	name := destination
	if filepath.IsAbs(destination) {
		clean := filepath.Clean(destination)
		if filepath.Dir(clean) != filepath.Clean(d.stateDir) {
			return "", ErrInvalidBackupDestination
		}
		name = filepath.Base(clean)
	}
	if !validBasename(name) || name == processLockName {
		return "", ErrInvalidBackupDestination
	}
	return name, nil
}

func ensureEntryAbsent(dirFD int, name string) error {
	var stat unix.Stat_t
	err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil
	case err != nil:
		return fmt.Errorf("%w: inspect destination: %v", ErrInvalidBackupDestination, err)
	default:
		return fmt.Errorf("%w: destination already exists", ErrInvalidBackupDestination)
	}
}

func createBackupTemp(dirFD int) (string, error) {
	var random [16]byte
	for attempts := 0; attempts < 10; attempts++ {
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := backupTempPrefix + hex.EncodeToString(random[:]) + backupTempSuffix
		fd, err := unix.Openat(
			dirFD,
			name,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		if err == nil {
			if closeErr := unix.Close(fd); closeErr != nil {
				_ = unix.Unlinkat(dirFD, name, 0)
				return "", closeErr
			}
			return name, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", err
		}
	}
	return "", fs.ErrExist
}

// cleanupBackupTemps removes only names from the private namespace used by
// Backup. It is called after acquiring the single-process lock, so a leftover
// file can only belong to an interrupted earlier process. Symlinks and special
// files are unlinked as directory entries and are never opened or followed.
func cleanupBackupTemps(dirFD int) error {
	duplicate, err := unix.Dup(dirFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(duplicate), "database-state")
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isBackupTempName(entry.Name()) {
			continue
		}
		if err := unix.Unlinkat(dirFD, entry.Name(), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return nil
}

func isBackupTempName(name string) bool {
	for _, auxiliary := range []string{"-journal", "-wal", "-shm"} {
		name = strings.TrimSuffix(name, auxiliary)
	}
	if !strings.HasPrefix(name, backupTempPrefix) || !strings.HasSuffix(name, backupTempSuffix) {
		return false
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(name, backupTempPrefix), backupTempSuffix)
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == 16 && encoded == strings.ToLower(encoded)
}

func syncRegularEntry(dirFD int, name string) error {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return errors.New("backup staging object is not a private regular file")
	}
	return unix.Fsync(fd)
}
