//go:build linux

package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func readSafeLinuxCNCINI(filename string, maximum int64) ([]byte, error) {
	fd, err := openAbsoluteNoFollow(filename)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "linuxcnc-ini")
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG ||
		before.Mode&0o022 != 0 || before.Nlink != 1 || before.Size < 0 || before.Size > maximum {
		return nil, errors.New("unsafe LinuxCNC INI")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(contents)) > maximum {
		return nil, errors.New("read LinuxCNC INI")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino ||
		before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return nil, errors.New("LinuxCNC INI changed while reading")
	}
	return contents, nil
}

func openAbsoluteNoFollow(filename string) (int, error) {
	if !filepath.IsAbs(filename) {
		return -1, errors.New("path is not absolute")
	}
	cleaned := filepath.Clean(filename)
	components := strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator))
	if len(components) == 0 || components[0] == "" {
		return -1, errors.New("path has no file name")
	}
	current, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for index, component := range components {
		flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index == len(components)-1 {
			flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		}
		next, openErr := unix.Openat(current, component, flags, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
	}
	return current, nil
}
