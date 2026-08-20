//go:build linux

package config

import (
	"crypto/tls"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxTLSMaterialBytes int64 = 1 << 20

// LoadTLSCertificate reads the configured certificate and private key exactly
// once through held directory descriptors. The returned in-memory key pair can
// be installed in http.Server.TLSConfig so the listener never reopens mutable
// host paths after startup validation.
func (c Config) LoadTLSCertificate() (*tls.Certificate, error) {
	if c.TLSCertFile == "" && c.TLSKeyFile == "" {
		return nil, nil
	}
	if c.TLSCertFile == "" || c.TLSKeyFile == "" {
		return nil, errors.New("TLS certificate and key must be configured together")
	}
	certificatePEM, err := readTLSMaterial(c.TLSCertFile, false)
	if err != nil {
		return nil, errors.New("TLS certificate is unavailable")
	}
	keyPEM, err := readTLSMaterial(c.TLSKeyFile, true)
	if err != nil {
		return nil, errors.New("TLS private key is unavailable")
	}
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		return nil, errors.New("TLS certificate and private key are invalid")
	}
	return &certificate, nil
}

func readTLSMaterial(filename string, privateKey bool) ([]byte, error) {
	if !filepath.IsAbs(filename) {
		return nil, errors.New("TLS material path is not absolute")
	}
	cleaned := filepath.Clean(filename)
	basename := filepath.Base(cleaned)
	if basename == "" || basename == "." || basename == string(filepath.Separator) {
		return nil, errors.New("TLS material basename is invalid")
	}
	parentFD, err := openTLSParent(filepath.Dir(cleaned))
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)

	fd, err := unix.Openat(parentFD, basename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "tls-material")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("TLS material descriptor is invalid")
	}

	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || !safeTLSMaterialStat(before, privateKey) {
		_ = file.Close()
		return nil, errors.New("TLS material is not a private regular file")
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, maxTLSMaterialBytes+1))
	statErr := unix.Fstat(fd, &after)
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || int64(len(contents)) > maxTLSMaterialBytes ||
		int64(len(contents)) != before.Size || !sameTLSMaterialStat(before, after, privateKey) {
		return nil, errors.New("TLS material changed or exceeded its size limit")
	}
	return contents, nil
}

func safeTLSMaterialStat(stat unix.Stat_t, privateKey bool) bool {
	permissions := stat.Mode & 0o777
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1 && permissions&0o133 == 0 &&
		(!privateKey || permissions&0o007 == 0) && stat.Size >= 0 && stat.Size <= maxTLSMaterialBytes
}

func sameTLSMaterialStat(first, second unix.Stat_t, privateKey bool) bool {
	return safeTLSMaterialStat(second, privateKey) && first.Dev == second.Dev && first.Ino == second.Ino &&
		first.Size == second.Size && first.Mtim == second.Mtim && first.Ctim == second.Ctim
}

// openTLSParent walks from the filesystem root using held descriptors and
// O_NOFOLLOW on every component. A configured parent symlink therefore cannot
// redirect certificate loading, and renaming an ancestor after it is opened
// does not move the capability used by the next component.
func openTLSParent(parent string) (int, error) {
	cleaned := filepath.Clean(parent)
	if !filepath.IsAbs(cleaned) {
		return -1, errors.New("TLS material parent is not absolute")
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	components := strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		if component == "." || component == ".." {
			_ = unix.Close(current)
			return -1, errors.New("TLS material parent is invalid")
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
	}
	return current, nil
}
