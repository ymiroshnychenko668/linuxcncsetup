//go:build linux

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testStore(t *testing.T, limit int64) (*Store, *Roots, string, string) {
	t.Helper()
	library := t.TempDir()
	state := t.TempDir()
	roots, err := NewRoots(library, state, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(roots, StoreOptions{UploadLimit: limit})
	if err != nil {
		roots.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { roots.Close() })
	return store, roots, library, state
}

func TestStagePublishOpenVerifyAndDeduplicate(t *testing.T) {
	store, _, _, _ := testStore(t, 0)
	contents := []byte("G21\nG0 X0 Y0\nM2\n")
	staged, err := store.Stage(context.Background(), bytes.NewReader(contents), int64(len(contents)))
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Publish(context.Background(), staged)
	if err != nil {
		t.Fatal(err)
	}
	if object.Size != int64(len(contents)) || len(object.Version) != 64 || strings.Contains(object.Key, "G21") {
		t.Fatalf("unexpected object: %+v", object)
	}
	if object.Identity.Inode == 0 || object.Identity.ModTimeNS == 0 || object.Identity.ChangeTimeNS == 0 {
		t.Fatalf("object identity was not captured: %+v", object.Identity)
	}
	opened, err := store.OpenObject(object.Key, object.SHA256, object.Version)
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(opened)
	opened.Close()
	if err != nil || !bytes.Equal(read, contents) {
		t.Fatalf("content = %q, %v", read, err)
	}
	verified, err := store.VerifyObject(context.Background(), object.Key, object.SHA256, object.Version)
	if err != nil || verified.Version != object.Version {
		t.Fatalf("verify = %+v, %v", verified, err)
	}
	secondStage, err := store.Stage(context.Background(), bytes.NewReader(contents), int64(len(contents)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Publish(context.Background(), secondStage)
	if err != nil || second.Key != object.Key || second.Version != object.Version {
		t.Fatalf("deduplicate = %+v, %v", second, err)
	}
}

func TestReadObjectRangeAndSafeEnumeration(t *testing.T) {
	store, _, library, _ := testStore(t, 0)
	contents := []byte("line one\nline two\nline three\n")
	staged, err := store.Stage(context.Background(), bytes.NewReader(contents), int64(len(contents)))
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Publish(context.Background(), staged)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	count, total, err := store.ReadObjectRange(context.Background(), object.Key, object.SHA256, object.Version, 9, buffer)
	if err != nil || total != int64(len(contents)) || string(buffer[:count]) != "line two" {
		t.Fatalf("range count=%d total=%d data=%q err=%v", count, total, buffer[:count], err)
	}
	objects, err := store.ListObjects(context.Background())
	if err != nil || len(objects) != 1 || objects[0].Key != object.Key {
		t.Fatalf("objects = %+v, %v", objects, err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(library, objectsDirName, object.SHA256[:2], strings.Repeat("f", 64))); err != nil {
		t.Fatal(err)
	}
	objects, err = store.ListObjects(context.Background())
	if err != nil || len(objects) != 1 {
		t.Fatalf("enumeration followed or rejected unrelated symlink: %+v, %v", objects, err)
	}
}

func TestStageLimitMismatchCancellationAndCleanup(t *testing.T) {
	store, _, _, state := testStore(t, 4)
	if _, err := store.Stage(context.Background(), strings.NewReader("12345"), 5); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("limit error = %v", err)
	}
	if _, err := store.Stage(context.Background(), strings.NewReader("123"), 4); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("size mismatch error = %v", err)
	}
	contextCancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Stage(contextCancelled, strings.NewReader("123"), -1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(state, uploadStageName))
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging leaked: %v, %v", entries, err)
	}
}

func TestObjectSubstitutionAndSpecialFilesAreRejected(t *testing.T) {
	store, _, library, _ := testStore(t, 0)
	staged, err := store.Stage(context.Background(), strings.NewReader("G0 X1\n"), -1)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Publish(context.Background(), staged)
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(library, filepath.FromSlash(object.Key))
	if err := os.WriteFile(physical, []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenObject(object.Key, object.SHA256, object.Version); !errors.Is(err, ErrObjectChanged) {
		t.Fatalf("tampered version error = %v", err)
	}
	if err := os.Remove(physical); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, physical); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenObject(object.Key, object.SHA256, ""); err == nil {
		t.Fatal("symlink object accepted")
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "outside" {
		t.Fatalf("sentinel changed: %q, %v", contents, err)
	}
}

func TestCleanupStagingUnlinksFIFOAndSymlinkWithoutFollowing(t *testing.T) {
	store, _, library, state := testStore(t, 0)
	stagingDirectories := []string{
		filepath.Join(state, uploadStageName),
		filepath.Join(library, objectTempDirName),
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, staging := range stagingDirectories {
		if err := unix.Mkfifo(filepath.Join(staging, "leftover.tmp"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, filepath.Join(staging, "link.tmp")); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CleanupStaging(); err != nil {
		t.Fatal(err)
	}
	for _, staging := range stagingDirectories {
		entries, err := os.ReadDir(staging)
		if err != nil || len(entries) != 0 {
			t.Fatalf("staging entries in %q = %v, %v", filepath.Base(staging), entries, err)
		}
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("sentinel changed: %q, %v", contents, err)
	}
}
