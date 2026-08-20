//go:build linux

package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLoadTLSCertificateReadsSafePairOnce(t *testing.T) {
	certificatePath, keyPath := writeTestTLSKeyPair(t)
	configuration := Config{TLSCertFile: certificatePath, TLSKeyFile: keyPath}
	certificate, err := configuration.LoadTLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if certificate == nil || len(certificate.Certificate) != 1 || certificate.PrivateKey == nil {
		t.Fatalf("incomplete TLS certificate: %+v", certificate)
	}
	if empty, err := (Config{}).LoadTLSCertificate(); err != nil || empty != nil {
		t.Fatalf("empty TLS config = %+v, %v", empty, err)
	}
}

func TestLoadTLSCertificateRejectsUnsafeEntriesWithoutPathLeak(t *testing.T) {
	certificatePath, keyPath := writeTestTLSKeyPair(t)
	directory := filepath.Dir(certificatePath)

	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "linked-key.pem")
		if err := os.Symlink(keyPath, link); err != nil {
			t.Fatal(err)
		}
		_, err := (Config{TLSCertFile: certificatePath, TLSKeyFile: link}).LoadTLSCertificate()
		if err == nil || strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), link) {
			t.Fatalf("unsafe symlink error: %v", err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "hard-linked-key.pem")
		if err := os.Link(keyPath, link); err != nil {
			t.Fatal(err)
		}
		_, err := (Config{TLSCertFile: certificatePath, TLSKeyFile: keyPath}).LoadTLSCertificate()
		if err == nil || strings.Contains(err.Error(), directory) {
			t.Fatalf("unsafe hard-link error: %v", err)
		}
	})
}

func TestLoadTLSCertificateRejectsSpecialSharedWritableAndOversizedFiles(t *testing.T) {
	t.Run("FIFO", func(t *testing.T) {
		certificatePath, _ := writeTestTLSKeyPair(t)
		fifo := filepath.Join(t.TempDir(), "key.fifo")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := (Config{TLSCertFile: certificatePath, TLSKeyFile: fifo}).LoadTLSCertificate()
		if err == nil {
			t.Fatal("FIFO TLS key was accepted")
		}
	})

	t.Run("shared writable", func(t *testing.T) {
		certificatePath, keyPath := writeTestTLSKeyPair(t)
		if err := os.Chmod(keyPath, 0o666); err != nil {
			t.Fatal(err)
		}
		_, err := (Config{TLSCertFile: certificatePath, TLSKeyFile: keyPath}).LoadTLSCertificate()
		if err == nil {
			t.Fatal("shared-writable TLS key was accepted")
		}
	})

	t.Run("world readable private key", func(t *testing.T) {
		certificatePath, keyPath := writeTestTLSKeyPair(t)
		if err := os.Chmod(keyPath, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := (Config{TLSCertFile: certificatePath, TLSKeyFile: keyPath}).LoadTLSCertificate()
		if err == nil {
			t.Fatal("world-readable TLS private key was accepted")
		}
		if err := os.Chmod(keyPath, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := (Config{TLSCertFile: certificatePath, TLSKeyFile: keyPath}).LoadTLSCertificate(); err != nil {
			t.Fatalf("group-readable TLS private key rejected: %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		_, keyPath := writeTestTLSKeyPair(t)
		oversized := filepath.Join(t.TempDir(), "oversized.pem")
		file, err := os.OpenFile(oversized, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxTLSMaterialBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = (Config{TLSCertFile: oversized, TLSKeyFile: keyPath}).LoadTLSCertificate()
		if err == nil || strings.Contains(err.Error(), oversized) {
			t.Fatalf("unsafe oversized error: %v", err)
		}
	})
}

func TestLoadTLSCertificateRejectsSymlinkedParent(t *testing.T) {
	certificatePath, keyPath := writeTestTLSKeyPair(t)
	parent := filepath.Dir(certificatePath)
	linkedParent := filepath.Join(t.TempDir(), "tls")
	if err := os.Symlink(parent, linkedParent); err != nil {
		t.Fatal(err)
	}
	_, err := (Config{
		TLSCertFile: filepath.Join(linkedParent, filepath.Base(certificatePath)),
		TLSKeyFile:  filepath.Join(linkedParent, filepath.Base(keyPath)),
	}).LoadTLSCertificate()
	if err == nil || strings.Contains(err.Error(), parent) || strings.Contains(err.Error(), linkedParent) {
		t.Fatalf("symlinked TLS parent error: %v", err)
	}
}

func writeTestTLSKeyPair(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "certificate.pem")
	keyPath := filepath.Join(directory, "private-key.pem")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificatePath, keyPath
}
