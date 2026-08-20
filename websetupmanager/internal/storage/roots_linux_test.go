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

func TestLibraryFingerprintSurvivesColdCopyToNewInode(t *testing.T) {
	originalLibrary := t.TempDir()
	original, err := NewRoots(originalLibrary, t.TempDir(), 0o640)
	if err != nil {
		t.Fatal(err)
	}
	wantID, wantFingerprint := original.LibraryID(), original.LibraryFingerprint()
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(originalLibrary, libraryMarkerName))
	if err != nil {
		t.Fatal(err)
	}
	restoredLibrary := t.TempDir()
	if err := os.WriteFile(filepath.Join(restoredLibrary, libraryMarkerName), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := NewRoots(restoredLibrary, t.TempDir(), 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.LibraryID() != wantID || restored.LibraryFingerprint() != wantFingerprint {
		t.Fatalf("restored identity = %q/%q, want %q/%q",
			restored.LibraryID(), restored.LibraryFingerprint(), wantID, wantFingerprint)
	}
}

func TestStateIdentityRefersToHeldStateRoot(t *testing.T) {
	library := t.TempDir()
	parent := t.TempDir()
	state := filepath.Join(parent, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	roots, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer roots.Close()

	identity, err := roots.StateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(state, &stat); err != nil {
		t.Fatal(err)
	}
	if identity.Device != uint64(stat.Dev) || identity.Inode != stat.Ino {
		t.Fatalf("StateIdentity() = %+v, want device=%d inode=%d", identity, stat.Dev, stat.Ino)
	}

	if err := os.Rename(state, filepath.Join(parent, "original-state")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	afterReplacement, err := roots.StateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if afterReplacement != identity {
		t.Fatalf("held state identity changed: got %+v, want %+v", afterReplacement, identity)
	}
}

func TestNewRootsRejectsSymlinkInRootPath(t *testing.T) {
	realParent := t.TempDir()
	linkedParent := filepath.Join(t.TempDir(), "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(linkedParent, "library")
	if err := os.Mkdir(filepath.Join(realParent, "library"), 0o750); err != nil {
		t.Fatal(err)
	}
	roots, err := NewRoots(library, t.TempDir(), 0o640)
	if err == nil {
		_ = roots.Close()
		t.Fatal("root path with a symlink component was accepted")
	}
}

func TestNewRootsRejectsSamePhysicalRoot(t *testing.T) {
	root := t.TempDir()
	roots, err := NewRoots(root, root, 0o640)
	if err == nil {
		_ = roots.Close()
		t.Fatal("same physical root was accepted for library and state")
	}
}

func TestNewRootsRejectsUnsafeLibraryIdentityMarker(t *testing.T) {
	t.Run("hard link", func(t *testing.T) {
		library := t.TempDir()
		sentinel := filepath.Join(t.TempDir(), "identity-sentinel")
		contents := strings.Repeat("a", 32) + "\n"
		if err := os.WriteFile(sentinel, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(sentinel, filepath.Join(library, libraryMarkerName)); err != nil {
			t.Fatal(err)
		}
		roots, err := NewRoots(library, t.TempDir(), 0o640)
		if err == nil {
			_ = roots.Close()
			t.Fatal("hard-linked library identity marker was accepted")
		}
		got, readErr := os.ReadFile(sentinel)
		if readErr != nil || string(got) != contents {
			t.Fatalf("external sentinel changed: %q, %v", got, readErr)
		}
	})

	t.Run("shared writable", func(t *testing.T) {
		library := t.TempDir()
		marker := filepath.Join(library, libraryMarkerName)
		if err := os.WriteFile(marker, []byte(strings.Repeat("b", 32)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(marker, 0o666); err != nil {
			t.Fatal(err)
		}
		roots, err := NewRoots(library, t.TempDir(), 0o640)
		if err == nil {
			_ = roots.Close()
			t.Fatal("shared-writable library identity marker was accepted")
		}
	})
}

func TestRootsCheckRejectsReplacedManagedDirectory(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	roots, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer roots.Close()

	staging := filepath.Join(library, objectTempDirName)
	saved := filepath.Join(library, objectTempDirName+".saved")
	if err := os.Rename(staging, saved); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, staging); err != nil {
		t.Fatal(err)
	}
	if err := roots.Check(); err == nil {
		t.Fatal("readiness accepted a symlink-substituted managed directory")
	}
	if err := os.Remove(staging); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, staging); err != nil {
		t.Fatal(err)
	}
	if err := roots.Check(); err != nil {
		t.Fatalf("readiness did not recover after safe directory restore: %v", err)
	}
	if err := os.Chmod(staging, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := roots.Check(); err == nil {
		t.Fatal("readiness accepted a group-writable managed directory")
	}
}

func TestRootsCheckRejectsRootThatBecomesSharedWritable(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	roots, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer roots.Close()
	if err := os.Chmod(library, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := roots.Check(); err == nil {
		t.Fatal("readiness accepted a shared-writable managed root")
	}
}

func TestNewRootsRejectsSharedWritableManagedDirectory(t *testing.T) {
	library := t.TempDir()
	state := t.TempDir()
	staging := filepath.Join(library, objectTempDirName)
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(staging, 0o770); err != nil {
		t.Fatal(err)
	}
	if roots, err := NewRoots(library, state, 0o640); err == nil {
		_ = roots.Close()
		t.Fatal("group-writable managed staging directory was accepted")
	}
}

func TestNewRootsRejectsUnsafeArtifactFileModes(t *testing.T) {
	for _, mode := range []os.FileMode{0, 0o200, 0o750, 0o660, 0o606} {
		t.Run(mode.String(), func(t *testing.T) {
			roots, err := NewRoots(t.TempDir(), t.TempDir(), mode)
			if err == nil {
				_ = roots.Close()
				t.Fatalf("unsafe artifact mode %#o was accepted", mode)
			}
		})
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
