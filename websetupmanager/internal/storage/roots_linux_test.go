//go:build linux

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRootsPersistOpaqueLibraryIDAndRemainReady(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	first, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	identifier := first.LibraryID()
	if len(identifier) != 32 || strings.Contains(identifier, library) {
		t.Fatalf("unsafe library ID %q", identifier)
	}
	if len(first.LibraryFingerprint()) != 64 || strings.Contains(first.LibraryFingerprint(), library) {
		t.Fatalf("unsafe library fingerprint %q", first.LibraryFingerprint())
	}
	if err := first.Check(); err != nil {
		t.Fatal(err)
	}
	if free, err := first.FreeBytes(true); err != nil || free == 0 {
		t.Fatalf("FreeBytes() = %d, %v", free, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.LibraryID() != identifier {
		t.Fatalf("library ID changed: %q != %q", second.LibraryID(), identifier)
	}
}

func TestOpenLibraryRejectsTraversalAndSymlinkSentinel(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	sentinelDir := t.TempDir()
	sentinel := filepath.Join(sentinelDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(library, "link")); err != nil {
		t.Fatal(err)
	}
	roots, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer roots.Close()
	for _, attack := range []string{"../sentinel", "/etc/passwd", "objects/../../sentinel", "link", "..\\sentinel", "bad\x00name"} {
		file, openErr := roots.OpenLibrary(attack, unix.O_RDONLY, 0)
		if openErr == nil {
			file.Close()
			t.Fatalf("attack %q unexpectedly opened", attack)
		}
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "outside" {
		t.Fatalf("sentinel changed: %q, %v", contents, err)
	}
}

func TestSpecialFileCanBeRejectedWithoutBlocking(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(library, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer roots.Close()
	file, err := roots.OpenLibrary("pipe", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := requireRegular(file); err == nil {
		t.Fatal("FIFO accepted as regular content")
	}
}
