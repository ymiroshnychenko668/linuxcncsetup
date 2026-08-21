//go:build linux

package config

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func readSafeLinuxCNCINI(filename string, maximum int64) ([]byte, error) {
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
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
