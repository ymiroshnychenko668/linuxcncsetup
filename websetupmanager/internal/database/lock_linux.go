package database

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

const processLockName = ".websetupmanager.lock"

type processLock struct {
	dirFD int
	fd    int

	closeOnce sync.Once
	closeErr  error
}

func acquireProcessLock(stateDir string, expected *StateDirectoryIdentity) (*processLock, error) {
	dirFD, err := unix.Open(stateDir, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open directory: %v", ErrInvalidStateDir, err)
	}
	var dirStat unix.Stat_t
	if err := unix.Fstat(dirFD, &dirStat); err != nil ||
		dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || dirStat.Mode&0o022 != 0 {
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("%w: state directory is unsafe", ErrInvalidStateDir)
	}
	if expected != nil &&
		(expected.Device != uint64(dirStat.Dev) || expected.Inode != dirStat.Ino) {
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("%w: state directory identity changed", ErrInvalidStateDir)
	}

	fd, err := unix.Openat(
		dirFD,
		processLockName,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0o600,
	)
	if err != nil {
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("%w: open process lock: %v", ErrInvalidStateDir, err)
	}

	lock := &processLock{dirFD: dirFD, fd: fd}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: inspect process lock: %v", ErrInvalidStateDir, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o022 != 0 {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: process lock is not a private regular file", ErrInvalidStateDir)
	}

	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("acquire process lock: %w", err)
	}

	return lock, nil
}

func (l *processLock) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		var errs []error
		if l.fd >= 0 {
			if err := unix.Flock(l.fd, unix.LOCK_UN); err != nil {
				errs = append(errs, fmt.Errorf("unlock process lock: %w", err))
			}
			if err := unix.Close(l.fd); err != nil {
				errs = append(errs, fmt.Errorf("close process lock: %w", err))
			}
			l.fd = -1
		}
		if l.dirFD >= 0 {
			if err := unix.Close(l.dirFD); err != nil {
				errs = append(errs, fmt.Errorf("close state directory: %w", err))
			}
			l.dirFD = -1
		}
		l.closeErr = errors.Join(errs...)
	})
	return l.closeErr
}
