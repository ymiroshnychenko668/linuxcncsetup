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

func testCatalog(t *testing.T) (*CatalogStore, *Store, string) {
	t.Helper()
	objects, _, _, _ := testStore(t, 0)
	root := t.TempDir()
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalogStore(root, objects, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := catalog.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return catalog, objects, root
}

func stageCatalog(t *testing.T, store *Store, contents string) *StagedObject {
	t.Helper()
	staged, err := store.Stage(context.Background(), strings.NewReader(contents), int64(len(contents)))
	if err != nil {
		t.Fatal(err)
	}
	return staged
}

func TestCatalogPublishesAndReadsRelativeProgram(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	if err := os.Mkdir(filepath.Join(root, "orders"), 0o750); err != nil {
		t.Fatal(err)
	}
	staged := stageCatalog(t, objects, "G0 X1\n")
	publication, err := catalog.Publish(context.Background(), staged, "orders/part.ngc", "", "", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := publication.Commit(); err != nil {
		t.Fatal(err)
	}
	if publication.Object.Key != "orders/part.ngc" || publication.Object.Size != 6 {
		t.Fatalf("object = %+v", publication.Object)
	}
	buffer := make([]byte, 16)
	count, total, err := catalog.ReadRange(context.Background(), "orders/part.ngc", publication.Object.SHA256, publication.Object.Version, 0, buffer)
	if err != nil || total != 6 || string(buffer[:count]) != "G0 X1\n" {
		t.Fatalf("range = %q, %d, %v", buffer[:count], total, err)
	}
	physical, err := os.ReadFile(filepath.Join(root, "orders", "part.ngc"))
	if err != nil || !bytes.Equal(physical, []byte("G0 X1\n")) {
		t.Fatalf("physical = %q, %v", physical, err)
	}
}

func TestCatalogReplacementCanRollbackAndCommit(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	first := stageCatalog(t, objects, "first")
	created, err := catalog.Publish(context.Background(), first, "part.ngc", "", "", strings.Repeat("1", 32))
	if err != nil || created.Commit() != nil {
		t.Fatalf("create: %v", err)
	}
	old := created.Object

	replacement := stageCatalog(t, objects, "second")
	changed, err := catalog.Publish(context.Background(), replacement, "part.ngc", old.SHA256, old.Version, strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := changed.Rollback(); err != nil {
		t.Fatal(err)
	}
	old = changed.Restored
	contents, _ := os.ReadFile(filepath.Join(root, "part.ngc"))
	if string(contents) != "first" {
		t.Fatalf("rollback contents = %q", contents)
	}

	replacement = stageCatalog(t, objects, "third")
	changed, err = catalog.Publish(context.Background(), replacement, "part.ngc", old.SHA256, old.Version, strings.Repeat("3", 32))
	if err != nil || changed.Commit() != nil {
		t.Fatalf("replace/commit: %v", err)
	}
	contents, _ = os.ReadFile(filepath.Join(root, "part.ngc"))
	if string(contents) != "third" {
		t.Fatalf("commit contents = %q", contents)
	}
}

func TestCatalogCreateRefusesExistingAndChangedTarget(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	if err := os.WriteFile(filepath.Join(root, "part.ngc"), []byte("external"), 0o640); err != nil {
		t.Fatal(err)
	}
	staged := stageCatalog(t, objects, "new")
	_, err := catalog.Publish(context.Background(), staged, "part.ngc", "", "", strings.Repeat("4", 32))
	if !errors.Is(err, ErrObjectChanged) {
		t.Fatalf("create error = %v", err)
	}
	contents, _ := os.ReadFile(filepath.Join(root, "part.ngc"))
	if string(contents) != "external" {
		t.Fatalf("existing target changed: %q", contents)
	}
}

func TestCatalogReplacementDetectsTargetSubstitutionRace(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	created, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "original"), "part.ngc", "", "", strings.Repeat("8", 32))
	if err != nil || created.Commit() != nil {
		t.Fatal(err)
	}
	catalog.beforeExchange = func() {
		if err := os.Rename(filepath.Join(root, "part.ngc"), filepath.Join(root, "moved.ngc")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "part.ngc"), []byte("substitute"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	staged := stageCatalog(t, objects, "replacement")
	_, err = catalog.Publish(context.Background(), staged, "part.ngc", created.Object.SHA256, created.Object.Version, strings.Repeat("9", 32))
	if !errors.Is(err, ErrObjectChanged) {
		t.Fatalf("race error = %v", err)
	}
	_ = objects.Discard(staged)
	current, _ := os.ReadFile(filepath.Join(root, "part.ngc"))
	moved, _ := os.ReadFile(filepath.Join(root, "moved.ngc"))
	if string(current) != "substitute" || string(moved) != "original" {
		t.Fatalf("race files current=%q moved=%q", current, moved)
	}
}

func TestCatalogReplacementRestoresSpecialFileSubstitution(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	created, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "original"), "part.ngc", "", "", strings.Repeat("6", 32))
	if err != nil || created.Commit() != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.beforeExchange = func() {
		if err := os.Rename(filepath.Join(root, "part.ngc"), filepath.Join(root, "original.ngc")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, filepath.Join(root, "part.ngc")); err != nil {
			t.Fatal(err)
		}
	}
	staged := stageCatalog(t, objects, "replacement")
	_, err = catalog.Publish(context.Background(), staged, "part.ngc", created.Object.SHA256, created.Object.Version, strings.Repeat("7", 32))
	if !errors.Is(err, ErrObjectChanged) {
		t.Fatalf("special substitution error = %v", err)
	}
	_ = objects.Discard(staged)
	target, err := os.Readlink(filepath.Join(root, "part.ngc"))
	if err != nil || target != sentinel {
		t.Fatalf("substitute was not restored: %q, %v", target, err)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("sentinel = %q, %v", contents, err)
	}
}

func TestCatalogRejectsTraversalSymlinkSpecialAndReservedTree(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe.ngc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sentinel, filepath.Join(root, "hardlink.ngc")); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"../sentinel", "escape/changed.ngc", "ngcgui_lib/job.ngc", ".wsm-hidden/job.ngc"} {
		staged := stageCatalog(t, objects, "bad")
		if _, err := catalog.Publish(context.Background(), staged, relative, "", "", strings.Repeat("5", 32)); err == nil {
			t.Fatalf("unsafe path %q accepted", relative)
		}
		_ = objects.Discard(staged)
	}
	staged := stageCatalog(t, objects, "bad")
	if _, err := catalog.Publish(context.Background(), staged, "pipe.ngc", "", "", strings.Repeat("6", 32)); err == nil {
		t.Fatal("FIFO target accepted")
	}
	_ = objects.Discard(staged)
	staged = stageCatalog(t, objects, "bad")
	if _, err := catalog.Publish(context.Background(), staged, "hardlink.ngc", "", "", strings.Repeat("a", 32)); err == nil {
		t.Fatal("hard-linked target accepted")
	}
	_ = objects.Discard(staged)
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("sentinel = %q, %v", contents, err)
	}
}

func TestCatalogRangeRejectsIdentityChange(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	staged := stageCatalog(t, objects, "abcdef")
	publication, err := catalog.Publish(context.Background(), staged, "part.ngc", "", "", strings.Repeat("7", 32))
	if err != nil || publication.Commit() != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("ABCDEF"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(root, "part.ngc")); err != nil {
		t.Fatal(err)
	}
	_, _, err = catalog.ReadRange(context.Background(), "part.ngc", publication.Object.SHA256, publication.Object.Version, 0, make([]byte, 3))
	if !errors.Is(err, ErrObjectChanged) && !errors.Is(err, io.EOF) {
		t.Fatalf("changed range error = %v", err)
	}
}

func TestCatalogPublicationRefusesPostPublishSubstitution(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	created, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "old"), "part.ngc", "", "", strings.Repeat("b", 32))
	if err != nil || created.Commit() != nil {
		t.Fatal(err)
	}
	operationID := strings.Repeat("c", 32)
	replacement, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "new"), "part.ngc", created.Object.SHA256, created.Object.Version, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "part.ngc"), []byte("bad"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Commit(); !errors.Is(err, ErrObjectChanged) {
		t.Fatalf("commit error = %v", err)
	}
	backup, err := os.ReadFile(filepath.Join(root, ".wsm-upload-"+operationID))
	if err != nil || string(backup) != "old" {
		t.Fatalf("recoverable backup = %q, %v", backup, err)
	}
}

func TestCatalogMoveAndQuarantineAreIdentityBound(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	if err := os.Mkdir(filepath.Join(root, "from"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "to"), 0o750); err != nil {
		t.Fatal(err)
	}
	created, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "G0 X1\n"), "from/job.ngc", "", "", strings.Repeat("d", 32))
	if err != nil || created.Commit() != nil {
		t.Fatal(err)
	}
	moved, err := catalog.MoveExpected(context.Background(), "from/job.ngc", "to/job.ngc", created.Object.SHA256, created.Object.Version)
	if err != nil || moved == nil {
		t.Fatalf("move = %+v, %v", moved, err)
	}
	quarantine, err := catalog.Quarantine("to/job.ngc", moved.SHA256, moved.Version, strings.Repeat("e", 32), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "to", "job.ngc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("public file still exists: %v", err)
	}
	if err := quarantine.Restore(); err != nil {
		t.Fatal(err)
	}
	restored, err := catalog.Inspect("to/job.ngc", moved.SHA256, "")
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err = catalog.Quarantine("to/job.ngc", restored.SHA256, restored.Version, strings.Repeat("f", 32), 0)
	if err != nil || quarantine.Discard() != nil {
		t.Fatalf("discard: %v", err)
	}
}

func TestCatalogMoveAndQuarantineRollbackSameInodeMutation(t *testing.T) {
	catalog, objects, root := testCatalog(t)
	created, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "first"), "part.ngc", "", "", strings.Repeat("1", 32))
	if err != nil || created.Commit() != nil {
		t.Fatal(err)
	}
	catalog.beforeMove = func() {
		if err := os.WriteFile(filepath.Join(root, "part.ngc"), []byte("other"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if moved, err := catalog.MoveExpected(context.Background(), "part.ngc", "moved.ngc", created.Object.SHA256, created.Object.Version); !errors.Is(err, ErrObjectChanged) || moved != nil {
		t.Fatalf("move race = %+v, %v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(root, "part.ngc")); err != nil {
		t.Fatalf("move was not rolled back: %v", err)
	}
	catalog.beforeMove = nil
	raceFile, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "fresh"), "race.ngc", "", "", strings.Repeat("5", 32))
	if err != nil || raceFile.Commit() != nil {
		t.Fatal(err)
	}
	catalog.beforeMove = func() {
		if err := os.WriteFile(filepath.Join(root, "race.ngc"), []byte("again"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if quarantine, err := catalog.Quarantine("race.ngc", raceFile.Object.SHA256, raceFile.Object.Version, strings.Repeat("2", 32), 0); !errors.Is(err, ErrObjectChanged) || quarantine != nil {
		t.Fatalf("quarantine race = %+v, %v", quarantine, err)
	}
	if _, err := os.Stat(filepath.Join(root, "race.ngc")); err != nil {
		t.Fatalf("quarantine was not rolled back: %v", err)
	}
}

func TestCatalogEmptyFolderQuarantineCanRestoreAndDiscard(t *testing.T) {
	catalog, _, root := testCatalog(t)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	folder, err := catalog.InspectFolder("empty", "")
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err := catalog.QuarantineEmptyFolder("empty", folder.Version, strings.Repeat("3", 32))
	if err != nil || quarantine.Restore() != nil {
		t.Fatalf("restore: %v", err)
	}
	folder, err = catalog.InspectFolder("empty", "")
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err = catalog.QuarantineEmptyFolder("empty", folder.Version, strings.Repeat("4", 32))
	if err != nil || quarantine.Discard() != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "empty")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("folder remains: %v", err)
	}
}

func TestCatalogMoveSyncFailureIsRolledBackOrReportedRecoverable(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "folder"
		}
		t.Run(name, func(t *testing.T) {
			catalog, objects, root := testCatalog(t)
			if err := os.Mkdir(filepath.Join(root, "from"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "to"), 0o750); err != nil {
				t.Fatal(err)
			}
			oldRelative, newRelative, digest := "from/part.ngc", "to/part.ngc", ""
			var version string
			if directory {
				oldRelative, newRelative = "from/job", "to/job"
				if err := os.Mkdir(filepath.Join(root, "from", "job"), 0o750); err != nil {
					t.Fatal(err)
				}
				object, err := catalog.InspectFolder(oldRelative, "")
				if err != nil {
					t.Fatal(err)
				}
				version = object.Version
			} else {
				publication, err := catalog.Publish(context.Background(), stageCatalog(t, objects, "G0 X7\n"),
					oldRelative, "", "", strings.Repeat("4", 32))
				if err != nil || publication.Commit() != nil {
					t.Fatalf("publish fixture: %v", err)
				}
				digest, version = publication.Object.SHA256, publication.Object.Version
			}
			injected := errors.New("injected directory sync failure")
			catalog.syncDirectory = func(int) error {
				physicalOld := filepath.Join(root, filepath.FromSlash(oldRelative))
				if directory {
					_ = os.Mkdir(physicalOld, 0o750)
				} else {
					_ = os.WriteFile(physicalOld, []byte("external sentinel"), 0o640)
				}
				return injected
			}
			uncertain, err := catalog.MoveExpected(context.Background(), oldRelative, newRelative, digest, version)
			if !errors.Is(err, injected) || uncertain == nil {
				t.Fatalf("uncertain move = %+v, %v", uncertain, err)
			}
			if directory {
				if _, err := os.Stat(filepath.Join(root, "to", "job")); err != nil {
					t.Fatalf("moved directory is not recoverable: %v", err)
				}
			} else {
				content, readErr := os.ReadFile(filepath.Join(root, "to", "part.ngc"))
				if readErr != nil || string(content) != "G0 X7\n" {
					t.Fatalf("moved file is not recoverable: %q, %v", content, readErr)
				}
			}
		})
	}
}

func TestCatalogCheckRejectsProgramRootPathReplacement(t *testing.T) {
	catalog, _, root := testCatalog(t)
	moved := root + ".moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Check(); err == nil {
		t.Fatal("substituted PROGRAM_PREFIX pathname remained ready")
	}
}
