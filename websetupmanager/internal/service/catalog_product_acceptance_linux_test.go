//go:build linux

package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
	"golang.org/x/sys/unix"
)

// catalogProductStack deliberately reopens every durable component in the
// migration tests. This catches bugs hidden by an in-memory service restart.
type catalogProductStack struct {
	libraryRoot string
	stateRoot   string
	programRoot string
	roots       *storage.Roots
	database    *database.DB
	objects     *storage.Store
	catalog     *storage.CatalogStore
	service     *Service
}

func newCatalogProductStack(t *testing.T) *catalogProductStack {
	t.Helper()
	base := t.TempDir()
	stack := &catalogProductStack{
		libraryRoot: filepath.Join(base, "library"),
		stateRoot:   filepath.Join(base, "state"),
		programRoot: filepath.Join(base, "programs"),
	}
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{stack.libraryRoot, 0o750},
		{stack.stateRoot, 0o700},
		{stack.programRoot, 0o750},
	} {
		if err := os.Mkdir(directory.path, directory.mode); err != nil {
			t.Fatal(err)
		}
	}
	stack.open(t)
	t.Cleanup(func() { stack.close(t) })
	return stack
}

func (s *catalogProductStack) open(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var err error
	s.roots, err = storage.NewRoots(s.libraryRoot, s.stateRoot, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	s.database, err = database.Open(ctx, s.stateRoot)
	if err != nil {
		_ = s.roots.Close()
		t.Fatal(err)
	}
	if err = s.database.EnsureLibrary(ctx, s.roots.LibraryID(), s.roots.LibraryFingerprint()); err != nil {
		_ = s.database.Close()
		_ = s.roots.Close()
		t.Fatal(err)
	}
	s.objects, err = storage.NewStore(s.roots, storage.StoreOptions{})
	if err != nil {
		_ = s.database.Close()
		_ = s.roots.Close()
		t.Fatal(err)
	}
	s.catalog, err = storage.NewCatalogStore(s.programRoot, s.objects, 0o640)
	if err != nil {
		_ = s.database.Close()
		_ = s.roots.Close()
		t.Fatal(err)
	}
	s.service, err = New(Options{
		Database: s.database, Objects: s.objects, Catalog: s.catalog,
		LibraryID: s.roots.LibraryID(), CatalogRootLabel: "LinuxCNC",
		CatalogRootDisplay: "~/linuxcnc/nc_files",
		GCodeExtensions:    []string{".ngc", ".nc", ".tap"},
	})
	if err != nil {
		_ = s.catalog.Close()
		_ = s.database.Close()
		_ = s.roots.Close()
		t.Fatal(err)
	}
}

func (s *catalogProductStack) close(t *testing.T) {
	t.Helper()
	if s.service != nil {
		s.service.Close()
		s.service = nil
	}
	if s.catalog != nil {
		if err := s.catalog.Close(); err != nil {
			t.Errorf("close catalog: %v", err)
		}
		s.catalog = nil
	}
	if s.database != nil {
		if err := s.database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
		s.database = nil
	}
	if s.roots != nil {
		if err := s.roots.Close(); err != nil {
			t.Errorf("close roots: %v", err)
		}
		s.roots = nil
	}
}

func (s *catalogProductStack) restart(t *testing.T) {
	t.Helper()
	s.close(t)
	s.open(t)
}

func mustCatalogID(t *testing.T, generate func() (string, error)) string {
	t.Helper()
	id, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func catalogDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func (s *catalogProductStack) createLegacySetup(t *testing.T, name string) *domain.Setup {
	t.Helper()
	setup, err := s.service.CreateSetup(context.Background(), CreateSetupInput{
		Name: name, Description: "legacy " + name,
		IdempotencyKey: "legacy-source-" + catalogDigest([]byte(name))[:16],
	})
	if err != nil {
		t.Fatal(err)
	}
	return setup
}

type legacyFixtureFile struct {
	artifactID string
	objectID   string
	object     *storage.Object
	content    []byte
}

func (s *catalogProductStack) addLegacyFile(t *testing.T, setupID string, role domain.ArtifactRole,
	name, mediaType string, content []byte, position int,
) legacyFixtureFile {
	t.Helper()
	ctx := context.Background()
	staged, err := s.objects.Stage(ctx, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	object, err := s.objects.Publish(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	objectID := mustCatalogID(t, domain.NewStorageObjectID)
	if _, err := s.database.SQL().ExecContext(ctx, `
		INSERT INTO storage_objects(id, library_id, storage_key, media_type, byte_size, sha256)
		VALUES (?, ?, ?, ?, ?, ?)`, objectID, s.roots.LibraryID(), object.Key, mediaType,
		object.Size, object.SHA256); err != nil {
		// Multiple legacy artifacts are allowed to share immutable content.
		if err := s.database.SQL().QueryRowContext(ctx, `
			SELECT id FROM storage_objects WHERE library_id = ? AND storage_key = ?`,
			s.roots.LibraryID(), object.Key).Scan(&objectID); err != nil {
			t.Fatal(err)
		}
	}
	artifactID := mustCatalogID(t, domain.NewArtifactID)
	normalized, err := domain.ArtifactNameKey(name)
	if err != nil {
		t.Fatal(err)
	}
	primary := 0
	if role == domain.ArtifactRoleProgram && position == 0 {
		primary = 1
	}
	if _, err := s.database.SQL().ExecContext(ctx, `
		INSERT INTO setup_artifacts(
			id, setup_id, role, display_name, normalized_name, storage_object_id,
			position, is_primary, identity_device, identity_inode, identity_size,
			identity_mtime_ns, identity_ctime_ns, object_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactID, setupID, role, name, normalized, objectID, position, primary,
		int64(object.Identity.Device), int64(object.Identity.Inode), object.Size,
		object.Identity.ModTimeNS, object.Identity.ChangeTimeNS, object.Version); err != nil {
		t.Fatal(err)
	}
	return legacyFixtureFile{artifactID: artifactID, objectID: objectID, object: object, content: content}
}

func TestCatalogCRUDAllowsIncompleteSetupAndEnforcesSingularComponents(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()

	folder, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
		Name: "Orders", IdempotencyKey: "catalog-folder-orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	setupInput := CreateCatalogSetupInput{FolderID: folder.ID, Name: "Bracket",
		Description: "Fixture A", IdempotencyKey: "catalog-setup-bracket"}
	setup, err := stack.service.CreateCatalogSetup(ctx, setupInput)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Program != nil || setup.SetupSheet != nil || setup.Revision != 1 {
		t.Fatalf("new setup is not incomplete: %#v", setup)
	}
	replay, err := stack.service.CreateCatalogSetup(ctx, setupInput)
	if err != nil || replay.ID != setup.ID || replay.Revision != setup.Revision {
		t.Fatalf("create replay = %#v, %v", replay, err)
	}

	firstProgram := []byte("G0 X1\nM2\n")
	if _, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, DisplayName: "first.ngc", Content: bytes.NewReader(firstProgram),
		ExpectedSize: int64(len(firstProgram)), IdempotencyKey: "catalog-program-missing-create-precondition",
	}); !domain.IsErrorCode(err, domain.CodePreconditionRequired) {
		t.Fatalf("missing create precondition = %v", err)
	}
	setup, err = stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "first.ngc", Content: bytes.NewReader(firstProgram),
		ExpectedSize: int64(len(firstProgram)), IdempotencyKey: "catalog-program-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	programID := setup.Program.ID
	if setup.Revision != 2 || setup.Program.DisplayName != "first.ngc" || setup.SetupSheet != nil {
		t.Fatalf("program result = %#v", setup)
	}

	replacement := []byte("G0 X2\nM2\n")
	if _, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, DisplayName: "renamed.tap", Content: bytes.NewReader(replacement),
		ExpectedSize: int64(len(replacement)), IdempotencyKey: "catalog-program-missing-replace-precondition",
	}); !domain.IsErrorCode(err, domain.CodePreconditionRequired) {
		t.Fatalf("missing replacement precondition = %v", err)
	}
	if _, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, ExpectedFileVersion: strings.Repeat("f", 64),
		DisplayName: "renamed.tap", Content: bytes.NewReader(replacement), ExpectedSize: int64(len(replacement)),
		IdempotencyKey: "catalog-program-wrong-replace-precondition",
	}); !domain.IsErrorCode(err, domain.CodeArtifactChanged) {
		t.Fatalf("wrong replacement precondition = %v", err)
	}
	setup, err = stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, ExpectedFileVersion: setup.Program.Version,
		DisplayName: "renamed.tap", Content: bytes.NewReader(replacement),
		ExpectedSize: int64(len(replacement)), IdempotencyKey: "catalog-program-replace-rename",
	})
	if err != nil {
		t.Fatal(err)
	}
	if setup.Revision != 3 || setup.Program == nil || setup.Program.ID != programID ||
		setup.Program.DisplayName != "renamed.tap" || setup.Program.SHA256 != catalogDigest(replacement) {
		t.Fatalf("renamed replacement = %#v", setup)
	}
	if _, err := os.Stat(filepath.Join(stack.programRoot, "Orders", "first.ngc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old program still visible: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(stack.programRoot, "Orders", "renamed.tap"))
	if err != nil || !bytes.Equal(stored, replacement) {
		t.Fatalf("replacement bytes = %q, %v", stored, err)
	}

	sheet := []byte("%PDF-1.4\n%%EOF\n")
	setup, err = stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleSetupSheet, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "drawing.pdf", Content: bytes.NewReader(sheet),
		ExpectedSize: int64(len(sheet)), IdempotencyKey: "catalog-sheet-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if setup.Revision != 4 || setup.Program == nil || setup.SetupSheet == nil {
		t.Fatalf("complete setup = %#v", setup)
	}
	tree, err := stack.service.GetCatalogTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Folders) != 1 || len(tree.Setups) != 1 || tree.Setups[0].Program == nil || tree.Setups[0].SetupSheet == nil {
		t.Fatalf("catalog tree = %#v", tree)
	}

	if _, err := stack.service.DeleteCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram,
		setup.Revision, "", "catalog-program-delete-missing-precondition"); !domain.IsErrorCode(err, domain.CodePreconditionRequired) {
		t.Fatalf("missing delete precondition = %v", err)
	}
	if _, err := stack.service.DeleteCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram,
		setup.Revision, strings.Repeat("e", 64), "catalog-program-delete-wrong-precondition"); !domain.IsErrorCode(err, domain.CodeArtifactChanged) {
		t.Fatalf("wrong delete precondition = %v", err)
	}
	setup, err = stack.service.DeleteCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram,
		setup.Revision, setup.Program.Version, "catalog-program-delete")
	if err != nil {
		t.Fatal(err)
	}
	if setup.Program != nil || setup.SetupSheet == nil || setup.Revision != 5 {
		t.Fatalf("delete must preserve incomplete setup: %#v", setup)
	}
}

func TestCatalogRejectsExecutableExtensionWithoutPublishing(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	setup, err := stack.service.CreateCatalogSetup(ctx, CreateCatalogSetupInput{
		Name: "Unsafe extension", IdempotencyKey: "unsafe-extension-setup",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("print('must never execute')\n")
	_, err = stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "job.py",
		Content: bytes.NewReader(content), ExpectedSize: int64(len(content)), IdempotencyKey: "unsafe-extension-put",
	})
	if !domain.IsErrorCode(err, domain.CodeUnsupportedFileType) {
		t.Fatalf("Python catalog upload error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stack.programRoot, "job.py")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Python upload reached PROGRAM_PREFIX: %v", err)
	}
	if _, err := New(Options{Database: stack.database, Objects: stack.objects, Catalog: stack.catalog,
		LibraryID: stack.roots.LibraryID(), GCodeExtensions: []string{".py"}}); err == nil {
		t.Fatal("service accepted an executable catalog extension")
	}
}

func TestCatalogFolderPrepareFailureKeepsRecoverableIntentWithoutFilesystemLeak(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	if _, err := stack.database.SQL().ExecContext(ctx, `
		CREATE TRIGGER test_fail_catalog_folder_prepare
		BEFORE UPDATE OF details_json ON catalog_operations
		WHEN OLD.operation = 'folder_create'
		BEGIN SELECT RAISE(ABORT, 'injected catalog journal failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
		Name: "Interrupted", IdempotencyKey: "catalog-folder-interrupted",
	})
	if err == nil {
		t.Fatal("injected journal failure unexpectedly succeeded")
	}
	var state, target, temporary string
	if err := stack.database.SQL().QueryRowContext(ctx, `
		SELECT state, target_path, COALESCE(temporary_path, '')
		  FROM catalog_operations WHERE idempotency_key = ?`,
		"catalog-folder-interrupted").Scan(&state, &target, &temporary); err != nil {
		t.Fatal(err)
	}
	if state != "intent" || target != "Interrupted" || !strings.HasPrefix(temporary, ".wsm-create-") {
		t.Fatalf("recoverable operation = %q/%q/%q", state, target, temporary)
	}
	entries, err := os.ReadDir(stack.programRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("failed folder preparation leaked entries: %v", names)
	}
}

func TestLegacyCatalogMigrationSplitsZeroOneAndManyProgramsAndSurvivesRestart(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()

	zero := stack.createLegacySetup(t, "No program")
	zeroSheet := stack.addLegacyFile(t, zero.ID, domain.ArtifactRoleSetupSheet,
		"zero.html", "text/html", []byte("<!doctype html><p>zero</p>"), 0)
	one := stack.createLegacySetup(t, "One program")
	oneProgram := stack.addLegacyFile(t, one.ID, domain.ArtifactRoleProgram,
		"one.ngc", "text/x-gcode", []byte("G0 X1\nM2\n"), 0)
	oneSheet := stack.addLegacyFile(t, one.ID, domain.ArtifactRoleSetupSheet,
		"one.pdf", "application/pdf", []byte("%PDF-1.4\n%%EOF\n"), 0)
	many := stack.createLegacySetup(t, "Many programs")
	manyA := stack.addLegacyFile(t, many.ID, domain.ArtifactRoleProgram,
		"many-a.ngc", "text/x-gcode", []byte("G0 X2\nM2\n"), 0)
	manyB := stack.addLegacyFile(t, many.ID, domain.ArtifactRoleProgram,
		"many-b.nc", "text/x-gcode", []byte("G0 X3\nM2\n"), 1)
	manySheet := stack.addLegacyFile(t, many.ID, domain.ArtifactRoleSetupSheet,
		"many.html", "text/html", []byte("<!doctype html><p>many</p>"), 0)

	var sourceSetupCount, sourceArtifactCount, sourceObjectCount int
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setups`).Scan(&sourceSetupCount); err != nil {
		t.Fatal(err)
	}
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setup_artifacts`).Scan(&sourceArtifactCount); err != nil {
		t.Fatal(err)
	}
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM storage_objects`).Scan(&sourceObjectCount); err != nil {
		t.Fatal(err)
	}
	if err := stack.service.MigrateLegacyCatalog(ctx); err != nil {
		t.Fatal(err)
	}

	tree, err := stack.service.GetCatalogTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Setups) != 4 {
		t.Fatalf("split setup count = %d, want 4", len(tree.Setups))
	}
	programCount, sheetCount, incompleteCount := 0, 0, 0
	setupNames := make(map[string]bool, len(tree.Setups))
	setupIDs := make([]string, 0, len(tree.Setups))
	for _, setup := range tree.Setups {
		setupNames[setup.Name] = true
		setupIDs = append(setupIDs, setup.ID)
		if setup.Program != nil {
			programCount++
		} else {
			incompleteCount++
		}
		if setup.SetupSheet != nil {
			sheetCount++
		}
	}
	if programCount != 3 || sheetCount != 4 || incompleteCount != 1 {
		t.Fatalf("program/sheet/incomplete = %d/%d/%d", programCount, sheetCount, incompleteCount)
	}
	if !setupNames["many-a"] || !setupNames["many-b"] || setupNames["many-b (2)"] {
		t.Fatalf("natural program basenames were not preserved: %v", setupNames)
	}
	sort.Strings(setupIDs)

	var mappingCount, completedMappings, manifestCount, copiedManifest int
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*), sum(state = 'completed') FROM catalog_legacy_migrations`).Scan(&mappingCount, &completedMappings); err != nil {
		t.Fatal(err)
	}
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*), sum(outcome = 'copied') FROM catalog_legacy_file_manifest`).Scan(&manifestCount, &copiedManifest); err != nil {
		t.Fatal(err)
	}
	if mappingCount != 4 || completedMappings != 4 || manifestCount != 7 || copiedManifest != 7 {
		t.Fatalf("mapping/manifest accounting = %d/%d/%d/%d", mappingCount, completedMappings, manifestCount, copiedManifest)
	}

	rows, err := stack.database.SQL().QueryContext(ctx, `
		SELECT manifest.byte_size, manifest.sha256, file.byte_size, file.sha256,
		       manifest.target_relative_path, file.relative_path
		  FROM catalog_legacy_file_manifest manifest
		  JOIN catalog_files file ON file.id = manifest.catalog_file_id
		 ORDER BY manifest.source_key, manifest.role`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var manifestSize, fileSize int64
		var manifestSHA, fileSHA, target, relative string
		if err := rows.Scan(&manifestSize, &manifestSHA, &fileSize, &fileSHA, &target, &relative); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if manifestSize != fileSize || manifestSHA != fileSHA || target != relative {
			rows.Close()
			t.Fatalf("manifest mismatch: %d/%s/%s vs %d/%s/%s",
				manifestSize, manifestSHA, target, fileSize, fileSHA, relative)
		}
		content, err := os.ReadFile(filepath.Join(stack.programRoot, filepath.FromSlash(relative)))
		if err != nil || int64(len(content)) != fileSize || catalogDigest(content) != fileSHA {
			rows.Close()
			t.Fatalf("published manifest object %q: size=%d sha=%s err=%v", relative, len(content), catalogDigest(content), err)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	// The source model and immutable source objects are evidence, not scratch
	// space: migration must retain every row and byte.
	var afterSetups, afterArtifacts, afterObjects int
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setups`).Scan(&afterSetups); err != nil {
		t.Fatal(err)
	}
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setup_artifacts`).Scan(&afterArtifacts); err != nil {
		t.Fatal(err)
	}
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM storage_objects`).Scan(&afterObjects); err != nil {
		t.Fatal(err)
	}
	if afterSetups != sourceSetupCount || afterArtifacts != sourceArtifactCount || afterObjects != sourceObjectCount {
		t.Fatalf("legacy source changed: setups %d/%d artifacts %d/%d objects %d/%d",
			afterSetups, sourceSetupCount, afterArtifacts, sourceArtifactCount, afterObjects, sourceObjectCount)
	}
	for _, fixture := range []legacyFixtureFile{zeroSheet, oneProgram, oneSheet, manyA, manyB, manySheet} {
		reader, err := stack.objects.OpenObject(fixture.object.Key, fixture.object.SHA256, fixture.object.Version)
		if err != nil {
			t.Fatal(err)
		}
		actual := make([]byte, len(fixture.content))
		if _, err := reader.Read(actual); err != nil {
			_ = reader.Close()
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil || !bytes.Equal(actual, fixture.content) {
			t.Fatalf("legacy object %s changed: %v", fixture.objectID, err)
		}
	}

	stack.restart(t)
	if err := stack.service.MigrateLegacyCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := stack.service.GetCatalogTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restartedIDs := make([]string, 0, len(restarted.Setups))
	for _, setup := range restarted.Setups {
		restartedIDs = append(restartedIDs, setup.ID)
	}
	sort.Strings(restartedIDs)
	if fmt.Sprint(restartedIDs) != fmt.Sprint(setupIDs) {
		t.Fatalf("restart duplicated or replaced setups: %v != %v", restartedIDs, setupIDs)
	}
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM catalog_legacy_file_manifest`).Scan(&manifestCount); err != nil {
		t.Fatal(err)
	}
	if manifestCount != 7 {
		t.Fatalf("restart manifest count = %d", manifestCount)
	}
}

func TestLegacyCatalogMigrationPersistsNameAndPhysicalCollisionsForManualReview(t *testing.T) {
	tests := []struct {
		name      string
		collision func(*testing.T, *catalogProductStack, *domain.CatalogFolder)
		assert    func(*testing.T, *catalogProductStack)
	}{
		{
			name: "catalog name",
			collision: func(t *testing.T, stack *catalogProductStack, folder *domain.CatalogFolder) {
				t.Helper()
				if _, err := stack.service.CreateCatalogSetup(context.Background(), CreateCatalogSetupInput{
					FolderID: folder.ID, Name: "collision", IdempotencyKey: "existing-catalog-setup",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "physical file",
			collision: func(t *testing.T, stack *catalogProductStack, folder *domain.CatalogFolder) {
				t.Helper()
				filename := filepath.Join(stack.programRoot, filepath.FromSlash(folder.RelativePath), "collision.ngc")
				if err := os.WriteFile(filename, []byte("EXTERNAL SENTINEL\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, stack *catalogProductStack) {
				t.Helper()
				content, err := os.ReadFile(filepath.Join(stack.programRoot, "Импортировано", "Collision source", "collision.ngc"))
				if err != nil || string(content) != "EXTERNAL SENTINEL\n" {
					t.Fatalf("external collision was changed: %q, %v", content, err)
				}
				var outcome, errorCode string
				if err := stack.database.SQL().QueryRowContext(context.Background(), `
					SELECT outcome, COALESCE(error_code, '') FROM catalog_legacy_file_manifest`).Scan(&outcome, &errorCode); err != nil {
					t.Fatal(err)
				}
				if outcome != "manual_review" || errorCode != "MIGRATION_REVIEW_REQUIRED" {
					t.Fatalf("physical collision manifest = %q/%q", outcome, errorCode)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stack := newCatalogProductStack(t)
			ctx := context.Background()
			legacy := stack.createLegacySetup(t, "Collision source")
			stack.addLegacyFile(t, legacy.ID, domain.ArtifactRoleProgram,
				"collision.ngc", "text/x-gcode", []byte("G0 X9\nM2\n"), 0)
			root, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
				Name: "Импортировано", IdempotencyKey: "existing-import-root",
			})
			if err != nil {
				t.Fatal(err)
			}
			folder, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
				ParentFolderID: root.ID, Name: legacy.Name, IdempotencyKey: "existing-import-source",
			})
			if err != nil {
				t.Fatal(err)
			}
			// Model a prior migration attempt that durably claimed these two
			// folders. Without provenance, a same-name operator folder is itself
			// the collision and migration correctly stops before a mapping exists.
			if _, err := stack.database.SQL().ExecContext(ctx, `
				UPDATE catalog_folders
				   SET legacy_source_key = CASE id WHEN ? THEN 'legacy-folder-root' WHEN ? THEN ? END
				 WHERE id IN (?, ?)`, root.ID, folder.ID, "legacy-folder:"+legacy.ID,
				root.ID, folder.ID); err != nil {
				t.Fatal(err)
			}
			test.collision(t, stack, folder)

			err = stack.service.MigrateLegacyCatalog(ctx)
			if err == nil || !domain.IsErrorCode(err, domain.CodeInvalidContent) {
				t.Fatalf("collision migration error = %v", err)
			}
			var migrationState, mappingState, errorCode string
			if err := stack.database.SQL().QueryRowContext(ctx, `SELECT legacy_migration_state FROM catalog_state WHERE library_id = ?`,
				stack.roots.LibraryID()).Scan(&migrationState); err != nil {
				t.Fatal(err)
			}
			if err := stack.database.SQL().QueryRowContext(ctx, `SELECT state, COALESCE(error_code, '') FROM catalog_legacy_migrations WHERE legacy_setup_id = ?`,
				legacy.ID).Scan(&mappingState, &errorCode); err != nil {
				t.Fatal(err)
			}
			if migrationState != "manual_review" || mappingState != "manual_review" || errorCode != "MIGRATION_REVIEW_REQUIRED" {
				t.Fatalf("manual review state = %q/%q/%q", migrationState, mappingState, errorCode)
			}
			if test.assert != nil {
				test.assert(t, stack)
			}
			var legacyCount int
			if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setups WHERE id = ?`, legacy.ID).Scan(&legacyCount); err != nil {
				t.Fatal(err)
			}
			if legacyCount != 1 {
				t.Fatal("legacy setup disappeared after collision")
			}
		})
	}
}

func TestLegacyCatalogMigrationRefusesUnownedSameNameFoldersBeforeMixingData(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*testing.T, *catalogProductStack, *domain.Setup)
	}{
		{
			name: "root lacks provenance",
			arrange: func(t *testing.T, stack *catalogProductStack, _ *domain.Setup) {
				t.Helper()
				if _, err := stack.service.CreateCatalogFolder(context.Background(), CreateCatalogFolderInput{
					Name: "Импортировано", IdempotencyKey: "operator-import-root",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "child lacks provenance",
			arrange: func(t *testing.T, stack *catalogProductStack, legacy *domain.Setup) {
				t.Helper()
				ctx := context.Background()
				root, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
					Name: "Импортировано", IdempotencyKey: "owned-import-root",
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stack.database.SQL().ExecContext(ctx,
					`UPDATE catalog_folders SET legacy_source_key = 'legacy-folder-root' WHERE id = ?`, root.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
					ParentFolderID: root.ID, Name: legacy.Name, IdempotencyKey: "operator-import-child",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stack := newCatalogProductStack(t)
			ctx := context.Background()
			legacy := stack.createLegacySetup(t, "Provenance source")
			stack.addLegacyFile(t, legacy.ID, domain.ArtifactRoleProgram,
				"provenance.ngc", "text/x-gcode", []byte("G0 X7\nM2\n"), 0)
			test.arrange(t, stack, legacy)
			err := stack.service.MigrateLegacyCatalog(ctx)
			if err == nil || !domain.IsErrorCode(err, domain.CodeInvalidContent) {
				t.Fatalf("unowned folder migration = %v", err)
			}
			var migrationState string
			if err := stack.database.SQL().QueryRowContext(ctx,
				`SELECT legacy_migration_state FROM catalog_state WHERE library_id = ?`, stack.roots.LibraryID()).Scan(&migrationState); err != nil {
				t.Fatal(err)
			}
			var mappings, files int
			if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM catalog_legacy_migrations`).Scan(&mappings); err != nil {
				t.Fatal(err)
			}
			if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM catalog_files`).Scan(&files); err != nil {
				t.Fatal(err)
			}
			if migrationState != "manual_review" || mappings != 0 || files != 0 {
				t.Fatalf("unowned folder mixed data: state=%q mappings=%d files=%d", migrationState, mappings, files)
			}
			var sourceCount int
			if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM setups WHERE id = ?`, legacy.ID).Scan(&sourceCount); err != nil {
				t.Fatal(err)
			}
			if sourceCount != 1 {
				t.Fatal("legacy source was changed at provenance collision")
			}
		})
	}
}

func TestCatalogRecoveryRejectsSubstitutedMoveTarget(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	folder, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
		Name: "Move", IdempotencyKey: "move-folder",
	})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := stack.service.CreateCatalogSetup(ctx, CreateCatalogSetupInput{
		FolderID: folder.ID, Name: "Move setup", IdempotencyKey: "move-setup",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("G0 X8\nM2\n")
	setup, err = stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "before.ngc", Content: bytes.NewReader(content),
		ExpectedSize: int64(len(content)), IdempotencyKey: "move-program",
	})
	if err != nil {
		t.Fatal(err)
	}
	file := setup.Program
	moved, err := stack.catalog.MoveExpected(ctx, file.RelativePath, "Move/after.ngc", file.SHA256, file.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stack.programRoot, "Move", "after.ngc")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stack.programRoot, "Move", "after.ngc"), content, 0o640); err != nil {
		t.Fatal(err)
	}
	opID := mustCatalogID(t, domain.NewOperationID)
	details := catalogOperationDetails{
		SetupID: setup.ID, FileID: file.ID, Role: domain.ArtifactRoleProgram,
		SourcePath: file.RelativePath, TargetPath: "Move/after.ngc",
		ExpectedSHA256: file.SHA256, ResultSHA256: file.SHA256,
		ExpectedVersion: file.Version, ResultVersion: moved.Version,
		ExpectedDevice: file.IdentityDevice, ExpectedInode: file.IdentityInode,
		ExpectedSize: file.ByteSize, ResultDevice: moved.Identity.Device,
		ResultInode: moved.Identity.Inode, ResultSize: moved.Size,
	}
	if err := stack.service.insertCatalogOperation(ctx, opID, "move", details); err != nil {
		t.Fatal(err)
	}
	payload, err := jsonMarshalCatalogDetails(details)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_operations SET state = 'storage_applied',
		expected_version = ?, result_version = ?, details_json = ? WHERE id = ?`,
		file.Version, moved.Version, payload, opID); err != nil {
		t.Fatal(err)
	}

	err = stack.service.RecoverCatalogOperations(ctx)
	if err == nil || !domain.IsErrorCode(err, domain.CodeArtifactChanged) {
		t.Fatalf("substituted move recovery = %v", err)
	}
	sentinel, err := os.ReadFile(filepath.Join(stack.programRoot, "Move", "after.ngc"))
	if err != nil || !bytes.Equal(sentinel, content) {
		t.Fatalf("substituted target changed: %q, %v", sentinel, err)
	}
	if _, err := os.Stat(filepath.Join(stack.programRoot, "Move", "before.ngc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery moved an untrusted inode: %v", err)
	}
	var state string
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT state FROM catalog_operations WHERE id = ?`, opID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "storage_applied" {
		t.Fatalf("unsafe move was marked terminal: %s", state)
	}
}

func TestCatalogRecoveryRejectsSameContentPublishAndDeleteSubstitution(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		stack := newCatalogProductStack(t)
		ctx := context.Background()
		setup := createIncompleteCatalogSetup(t, stack, "Publish substitution", "publish-substitution-setup")
		content := []byte("G0 X16\nM2\n")
		name := "publish-substitution.ngc"
		staged, err := stack.objects.Stage(ctx, bytes.NewReader(content), int64(len(content)))
		if err != nil {
			t.Fatal(err)
		}
		opID := mustCatalogID(t, domain.NewOperationID)
		details := catalogOperationDetails{
			SetupID: setup.ID, FileID: mustCatalogID(t, domain.NewArtifactID), Role: domain.ArtifactRoleProgram,
			TargetPath: name, TemporaryPath: catalogOperationTemp(name, opID), DisplayName: name,
			MediaType: "text/x-gcode", ResultSHA256: staged.SHA256, ExpectedRevision: setup.Revision,
			ResultSize: staged.Size, ByteSize: staged.Size,
		}
		if err := stack.service.insertCatalogOperation(ctx, opID, "publish", details); err != nil {
			t.Fatal(err)
		}
		publication, err := stack.catalog.PublishPrepared(ctx, staged, name, "", "", opID, func(prepared *storage.Object) error {
			details.ResultDevice, details.ResultInode, details.ResultSize = prepared.Identity.Device,
				prepared.Identity.Inode, prepared.Size
			payload, marshalErr := jsonMarshalCatalogDetails(details)
			if marshalErr != nil {
				return marshalErr
			}
			_, updateErr := stack.database.SQL().ExecContext(ctx,
				`UPDATE catalog_operations SET details_json = ? WHERE id = ?`, payload, opID)
			return updateErr
		})
		if err != nil {
			t.Fatal(err)
		}
		details.ResultDevice, details.ResultInode = publication.Object.Identity.Device, publication.Object.Identity.Inode
		details.ResultSize, details.ResultVersion = publication.Object.Size, publication.Object.Version
		payload, err := jsonMarshalCatalogDetails(details)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_operations SET state = 'storage_applied',
			result_version = ?, details_json = ? WHERE id = ?`, publication.Object.Version, payload, opID); err != nil {
			t.Fatal(err)
		}
		stack.restart(t)
		publicPath := filepath.Join(stack.programRoot, name)
		if err := os.Remove(publicPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(publicPath, content, 0o640); err != nil {
			t.Fatal(err)
		}
		err = stack.service.RecoverCatalogOperations(ctx)
		if err == nil || !domain.IsErrorCode(err, domain.CodeArtifactChanged) {
			t.Fatalf("same-content publish substitution recovery = %v", err)
		}
		actual, err := os.ReadFile(publicPath)
		if err != nil || !bytes.Equal(actual, content) {
			t.Fatalf("substituted publish target changed: %q, %v", actual, err)
		}
		var state string
		if err := stack.database.SQL().QueryRowContext(ctx, `SELECT state FROM catalog_operations WHERE id = ?`, opID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "storage_applied" {
			t.Fatalf("untrusted publish was terminalized: %s", state)
		}
	})

	t.Run("delete", func(t *testing.T) {
		stack := newCatalogProductStack(t)
		ctx := context.Background()
		setup := createIncompleteCatalogSetup(t, stack, "Delete substitution", "delete-substitution-setup")
		content := []byte("G0 X17\nM2\n")
		setup, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
			ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "delete-substitution.ngc", Content: bytes.NewReader(content),
			ExpectedSize: int64(len(content)), IdempotencyKey: "delete-substitution-seed",
		})
		if err != nil {
			t.Fatal(err)
		}
		file := setup.Program
		opID := mustCatalogID(t, domain.NewOperationID)
		temporary := ".wsm-trash-" + opID + "-a"
		details := catalogOperationDetails{SetupID: setup.ID, FileID: file.ID, Role: file.Role,
			TargetPath: file.RelativePath, TemporaryPath: temporary, ExpectedSHA256: file.SHA256,
			ExpectedVersion: file.Version, ExpectedRevision: setup.Revision,
			ExpectedDevice: file.IdentityDevice, ExpectedInode: file.IdentityInode, ExpectedSize: file.ByteSize,
			ByteSize: file.ByteSize}
		if err := stack.service.insertCatalogOperation(ctx, opID, "delete", details); err != nil {
			t.Fatal(err)
		}
		if _, err := stack.catalog.Quarantine(file.RelativePath, file.SHA256, file.Version, opID, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := stack.database.SQL().ExecContext(ctx,
			`UPDATE catalog_operations SET state = 'storage_applied' WHERE id = ?`, opID); err != nil {
			t.Fatal(err)
		}
		stack.restart(t)
		publicPath := filepath.Join(stack.programRoot, filepath.FromSlash(file.RelativePath))
		if err := os.WriteFile(publicPath, content, 0o640); err != nil {
			t.Fatal(err)
		}
		err = stack.service.RecoverCatalogOperations(ctx)
		if err == nil || !domain.IsErrorCode(err, domain.CodeArtifactChanged) {
			t.Fatalf("same-content delete substitution recovery = %v", err)
		}
		actual, err := os.ReadFile(publicPath)
		if err != nil || !bytes.Equal(actual, content) {
			t.Fatalf("substituted delete target changed: %q, %v", actual, err)
		}
		if _, err := os.Stat(filepath.Join(stack.programRoot, temporary)); err != nil {
			t.Fatalf("identity-bound quarantined original was changed: %v", err)
		}
		var state string
		if err := stack.database.SQL().QueryRowContext(ctx, `SELECT state FROM catalog_operations WHERE id = ?`, opID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "storage_applied" {
			t.Fatalf("untrusted delete was terminalized: %s", state)
		}
	})
}

func jsonMarshalCatalogDetails(details catalogOperationDetails) (string, error) {
	// Keep JSON construction in one helper so durable-state fixtures use the
	// same private journal representation as production service code.
	data, err := json.Marshal(details)
	return string(data), err
}

func createIncompleteCatalogSetup(t *testing.T, stack *catalogProductStack, name, key string) *domain.CatalogSetup {
	t.Helper()
	setup, err := stack.service.CreateCatalogSetup(context.Background(), CreateCatalogSetupInput{
		Name: name, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return setup
}

func catalogPutProgramHash(t *testing.T, setupID string, revision domain.Revision, name string, content []byte) string {
	t.Helper()
	hash, err := idempotencyRequestHash("catalogPutProgram", struct {
		SetupID             string          `json:"setupId"`
		ExpectedRevision    domain.Revision `json:"expectedRevision"`
		ExpectedFileVersion string          `json:"expectedFileVersion,omitempty"`
		CreateOnly          bool            `json:"createOnly"`
		DisplayName         string          `json:"displayName"`
		Size                int64           `json:"size"`
		SHA256              string          `json:"sha256"`
	}{setupID, revision, "", true, name, int64(len(content)), catalogDigest(content)})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func assertCatalogClaimMissing(t *testing.T, stack *catalogProductStack, key string) {
	t.Helper()
	var count int
	if err := stack.database.SQL().QueryRowContext(context.Background(), `
		SELECT count(*) FROM idempotency_requests WHERE library_id = ? AND key = ?`,
		stack.roots.LibraryID(), key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("idempotency claim %q was not reset", key)
	}
}

func TestCatalogRecoveryCoversIntentStorageAppliedAndDBAppliedPublishPhases(t *testing.T) {
	t.Run("intent resets same key", func(t *testing.T) {
		stack := newCatalogProductStack(t)
		ctx := context.Background()
		setup := createIncompleteCatalogSetup(t, stack, "Intent setup", "intent-setup")
		content := []byte("G0 X11\nM2\n")
		key := "publish-intent-retry"
		requestHash := catalogPutProgramHash(t, setup.ID, setup.Revision, "intent.ngc", content)
		if _, err := stack.service.claimIdempotency(ctx, key, "catalogPutProgram", requestHash); err != nil {
			t.Fatal(err)
		}
		opID := mustCatalogID(t, domain.NewOperationID)
		details := catalogOperationDetails{
			SetupID: setup.ID, FileID: mustCatalogID(t, domain.NewArtifactID), Role: domain.ArtifactRoleProgram,
			TargetPath: "intent.ngc", TemporaryPath: catalogOperationTemp("intent.ngc", opID),
			DisplayName: "intent.ngc", MediaType: "text/x-gcode", ResultSHA256: catalogDigest(content),
			ExpectedRevision: setup.Revision, ResultSize: int64(len(content)), ByteSize: int64(len(content)),
			IdempotencyKey: key, RequestOperation: "catalogPutProgram", RequestHash: requestHash,
		}
		if err := stack.service.insertCatalogOperation(ctx, opID, "publish", details); err != nil {
			t.Fatal(err)
		}

		stack.restart(t)
		if err := stack.service.RecoverCatalogOperations(ctx); err != nil {
			t.Fatal(err)
		}
		assertCatalogClaimMissing(t, stack, key)
		var state string
		if err := stack.database.SQL().QueryRowContext(ctx, `SELECT state FROM catalog_operations WHERE id = ?`, opID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "failed" {
			t.Fatalf("intent recovery state = %q", state)
		}
		result, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
			ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "intent.ngc", Content: bytes.NewReader(content),
			ExpectedSize: int64(len(content)), IdempotencyKey: key,
		})
		if err != nil || result.Program == nil {
			t.Fatalf("same-key retry = %#v, %v", result, err)
		}
	})

	t.Run("storage applied rolls back and resets same key", func(t *testing.T) {
		stack := newCatalogProductStack(t)
		ctx := context.Background()
		setup := createIncompleteCatalogSetup(t, stack, "Storage setup", "storage-setup")
		content := []byte("G0 X12\nM2\n")
		name, key := "storage.ngc", "publish-storage-retry"
		requestHash := catalogPutProgramHash(t, setup.ID, setup.Revision, name, content)
		if _, err := stack.service.claimIdempotency(ctx, key, "catalogPutProgram", requestHash); err != nil {
			t.Fatal(err)
		}
		staged, err := stack.objects.Stage(ctx, bytes.NewReader(content), int64(len(content)))
		if err != nil {
			t.Fatal(err)
		}
		opID := mustCatalogID(t, domain.NewOperationID)
		details := catalogOperationDetails{
			SetupID: setup.ID, FileID: mustCatalogID(t, domain.NewArtifactID), Role: domain.ArtifactRoleProgram,
			TargetPath: name, TemporaryPath: catalogOperationTemp(name, opID), DisplayName: name,
			MediaType: "text/x-gcode", ResultSHA256: staged.SHA256, ExpectedRevision: setup.Revision,
			ResultSize: staged.Size, ByteSize: staged.Size, IdempotencyKey: key,
			RequestOperation: "catalogPutProgram", RequestHash: requestHash,
		}
		if err := stack.service.insertCatalogOperation(ctx, opID, "publish", details); err != nil {
			t.Fatal(err)
		}
		publication, err := stack.catalog.PublishPrepared(ctx, staged, name, "", "", opID, func(prepared *storage.Object) error {
			details.ResultDevice, details.ResultInode, details.ResultSize = prepared.Identity.Device,
				prepared.Identity.Inode, prepared.Size
			payload, marshalErr := jsonMarshalCatalogDetails(details)
			if marshalErr != nil {
				return marshalErr
			}
			_, updateErr := stack.database.SQL().ExecContext(ctx,
				`UPDATE catalog_operations SET details_json = ? WHERE id = ? AND state = 'intent'`, payload, opID)
			return updateErr
		})
		if err != nil {
			t.Fatal(err)
		}
		details.ResultDevice, details.ResultInode = publication.Object.Identity.Device, publication.Object.Identity.Inode
		details.ResultSize, details.ResultVersion = publication.Object.Size, publication.Object.Version
		payload, err := jsonMarshalCatalogDetails(details)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stack.database.SQL().ExecContext(ctx, `
			UPDATE catalog_operations SET state = 'storage_applied', result_version = ?, details_json = ? WHERE id = ?`,
			publication.Object.Version, payload, opID); err != nil {
			t.Fatal(err)
		}
		// Deliberately abandon the unresolved publication and reopen every
		// durable component, the state left by a process death at this phase.
		stack.restart(t)
		if err := stack.service.RecoverCatalogOperations(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(stack.programRoot, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-committed publication survived recovery: %v", err)
		}
		assertCatalogClaimMissing(t, stack, key)
		result, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
			ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: name, Content: bytes.NewReader(content),
			ExpectedSize: int64(len(content)), IdempotencyKey: key,
		})
		if err != nil || result.Program == nil || result.Program.SHA256 != catalogDigest(content) {
			t.Fatalf("same-key storage retry = %#v, %v", result, err)
		}
	})

	t.Run("db applied completes and replays", func(t *testing.T) {
		stack := newCatalogProductStack(t)
		ctx := context.Background()
		setup := createIncompleteCatalogSetup(t, stack, "Committed setup", "committed-setup")
		content := []byte("G0 X13\nM2\n")
		key := "publish-db-applied-replay"
		committed, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
			ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "committed.ngc", Content: bytes.NewReader(content),
			ExpectedSize: int64(len(content)), IdempotencyKey: key,
		})
		if err != nil {
			t.Fatal(err)
		}
		var opID string
		if err := stack.database.SQL().QueryRowContext(ctx, `
			SELECT id FROM catalog_operations WHERE idempotency_key = ? AND operation = 'publish'`, key).Scan(&opID); err != nil {
			t.Fatal(err)
		}
		if _, err := stack.database.SQL().ExecContext(ctx, `
			UPDATE catalog_operations SET state = 'db_applied', completed_at = NULL WHERE id = ?`, opID); err != nil {
			t.Fatal(err)
		}
		stack.restart(t)
		if err := stack.service.RecoverCatalogOperations(ctx); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := stack.database.SQL().QueryRowContext(ctx, `SELECT state FROM catalog_operations WHERE id = ?`, opID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "completed" {
			t.Fatalf("db_applied recovery state = %q", state)
		}
		replay, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
			ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "committed.ngc", Content: bytes.NewReader(content),
			ExpectedSize: int64(len(content)), IdempotencyKey: key,
		})
		if err != nil || replay.ID != committed.ID || replay.Revision != committed.Revision || replay.Program.ID != committed.Program.ID {
			t.Fatalf("committed replay = %#v, %v", replay, err)
		}
	})
}

func TestCatalogRecoveryRemovesPreparedHiddenFolderAndAllowsSameKeyRetry(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	name, key := "Prepared", "prepared-folder-retry"
	requestHash, err := idempotencyRequestHash("catalogCreateFolder", struct {
		ParentFolderID string `json:"parentFolderId"`
		Name           string `json:"name"`
	}{"", name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.service.claimIdempotency(ctx, key, "catalogCreateFolder", requestHash); err != nil {
		t.Fatal(err)
	}
	opID := mustCatalogID(t, domain.NewOperationID)
	temporary := catalogFolderCreateTemp(name, opID)
	details := catalogOperationDetails{FolderID: mustCatalogID(t, domain.NewFolderID), TargetPath: name,
		TemporaryPath: temporary, DisplayName: name, IdempotencyKey: key,
		RequestOperation: "catalogCreateFolder", RequestHash: requestHash}
	if err := stack.service.insertCatalogOperation(ctx, opID, "folder_create", details); err != nil {
		t.Fatal(err)
	}
	created, err := stack.catalog.CreateFolderPrepared(name, opID, func(prepared *storage.Object) error {
		details.ResultDevice, details.ResultInode, details.ResultSize = prepared.Identity.Device,
			prepared.Identity.Inode, prepared.Size
		payload, marshalErr := jsonMarshalCatalogDetails(details)
		if marshalErr != nil {
			return marshalErr
		}
		_, updateErr := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_operations SET details_json = ? WHERE id = ?`, payload, opID)
		return updateErr
	})
	if err != nil {
		t.Fatal(err)
	}
	// A crash before the no-replace publication leaves the prepared inode at
	// its deterministic private name. Rename back models exactly that state.
	if err := os.Rename(filepath.Join(stack.programRoot, name), filepath.Join(stack.programRoot, temporary)); err != nil {
		t.Fatal(err)
	}
	details.ResultDevice, details.ResultInode, details.ResultSize = created.Identity.Device, created.Identity.Inode, created.Size
	details.ResultVersion = created.Version
	payload, err := jsonMarshalCatalogDetails(details)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_operations
		SET state = 'storage_applied', result_version = ?, details_json = ? WHERE id = ?`, created.Version, payload, opID); err != nil {
		t.Fatal(err)
	}
	stack.restart(t)
	if err := stack.service.RecoverCatalogOperations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{name, temporary} {
		if _, err := os.Stat(filepath.Join(stack.programRoot, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("folder recovery left %q: %v", relative, err)
		}
	}
	assertCatalogClaimMissing(t, stack, key)
	folder, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{Name: name, IdempotencyKey: key})
	if err != nil || folder == nil || folder.Name != name {
		t.Fatalf("same-key folder retry = %#v, %v", folder, err)
	}
}

func TestCatalogRecoveryRestoresUncommittedDeleteAndAllowsSameKeyRetry(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	setup := createIncompleteCatalogSetup(t, stack, "Delete setup", "delete-setup")
	content := []byte("G0 X14\nM2\n")
	setup, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: "delete.ngc", Content: bytes.NewReader(content),
		ExpectedSize: int64(len(content)), IdempotencyKey: "delete-program-seed",
	})
	if err != nil {
		t.Fatal(err)
	}
	file := setup.Program
	key := "delete-storage-retry"
	operationName := "catalogDeleteFile:" + setup.ID + ":" + string(domain.ArtifactRoleProgram)
	requestHash, err := idempotencyRequestHash(operationName, struct {
		Expected            domain.Revision `json:"expectedRevision"`
		ExpectedFileVersion string          `json:"expectedFileVersion"`
	}{setup.Revision, file.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.service.claimIdempotency(ctx, key, operationName, requestHash); err != nil {
		t.Fatal(err)
	}
	opID := mustCatalogID(t, domain.NewOperationID)
	temporary := ".wsm-trash-" + opID + "-a"
	details := catalogOperationDetails{SetupID: setup.ID, FileID: file.ID, Role: file.Role,
		TargetPath: file.RelativePath, TemporaryPath: temporary, ExpectedSHA256: file.SHA256,
		ExpectedVersion: file.Version, ExpectedRevision: setup.Revision,
		ExpectedDevice: file.IdentityDevice, ExpectedInode: file.IdentityInode, ExpectedSize: file.ByteSize,
		ByteSize: file.ByteSize, IdempotencyKey: key, RequestOperation: operationName, RequestHash: requestHash}
	if err := stack.service.insertCatalogOperation(ctx, opID, "delete", details); err != nil {
		t.Fatal(err)
	}
	quarantine, err := stack.catalog.Quarantine(file.RelativePath, file.SHA256, file.Version, opID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if quarantine == nil {
		t.Fatal("quarantine was not created")
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_operations SET state = 'storage_applied' WHERE id = ?`, opID); err != nil {
		t.Fatal(err)
	}
	stack.restart(t)
	if err := stack.service.RecoverCatalogOperations(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(stack.programRoot, file.RelativePath))
	if err != nil || !bytes.Equal(restored, content) {
		t.Fatalf("delete rollback = %q, %v", restored, err)
	}
	assertCatalogClaimMissing(t, stack, key)
	result, err := stack.service.DeleteCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, setup.Revision, file.Version, key)
	if err != nil || result.Program != nil {
		t.Fatalf("same-key delete retry = %#v, %v", result, err)
	}
}

const catalogSIGKILLHelperEnv = "WEB_SETUP_MANAGER_CATALOG_SIGKILL_HELPER"

var catalogSIGKILLContent = []byte("G0 X15\nM2\n")

// TestCatalogPublishSIGKILLHelper is invoked only as a child test process. It
// stops after the public rename and durable storage_applied journal update;
// the parent sends SIGKILL, so no defer or testing cleanup can repair state.
func TestCatalogPublishSIGKILLHelper(t *testing.T) {
	if os.Getenv(catalogSIGKILLHelperEnv) != "1" {
		return
	}
	revisionValue, err := strconv.ParseInt(os.Getenv("WSM_CATALOG_CRASH_REVISION"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	stack := &catalogProductStack{
		libraryRoot: os.Getenv("WSM_CATALOG_CRASH_LIBRARY"),
		stateRoot:   os.Getenv("WSM_CATALOG_CRASH_STATE"),
		programRoot: os.Getenv("WSM_CATALOG_CRASH_PROGRAM"),
	}
	stack.open(t)
	ctx := context.Background()
	setupID := os.Getenv("WSM_CATALOG_CRASH_SETUP")
	name := os.Getenv("WSM_CATALOG_CRASH_NAME")
	key := os.Getenv("WSM_CATALOG_CRASH_KEY")
	revision := domain.Revision(revisionValue)
	requestHash := catalogPutProgramHash(t, setupID, revision, name, catalogSIGKILLContent)
	if _, err := stack.service.claimIdempotency(ctx, key, "catalogPutProgram", requestHash); err != nil {
		t.Fatal(err)
	}
	staged, err := stack.objects.Stage(ctx, bytes.NewReader(catalogSIGKILLContent), int64(len(catalogSIGKILLContent)))
	if err != nil {
		t.Fatal(err)
	}
	opID := mustCatalogID(t, domain.NewOperationID)
	details := catalogOperationDetails{
		SetupID: setupID, FileID: mustCatalogID(t, domain.NewArtifactID), Role: domain.ArtifactRoleProgram,
		TargetPath: name, TemporaryPath: catalogOperationTemp(name, opID), DisplayName: name,
		MediaType: "text/x-gcode", ResultSHA256: staged.SHA256, ExpectedRevision: revision,
		ResultSize: staged.Size, ByteSize: staged.Size, IdempotencyKey: key,
		RequestOperation: "catalogPutProgram", RequestHash: requestHash,
	}
	if err := stack.service.insertCatalogOperation(ctx, opID, "publish", details); err != nil {
		t.Fatal(err)
	}
	publication, err := stack.catalog.PublishPrepared(ctx, staged, name, "", "", opID, func(prepared *storage.Object) error {
		details.ResultDevice, details.ResultInode, details.ResultSize = prepared.Identity.Device,
			prepared.Identity.Inode, prepared.Size
		payload, marshalErr := jsonMarshalCatalogDetails(details)
		if marshalErr != nil {
			return marshalErr
		}
		_, updateErr := stack.database.SQL().ExecContext(ctx,
			`UPDATE catalog_operations SET details_json = ? WHERE id = ? AND state = 'intent'`, payload, opID)
		return updateErr
	})
	if err != nil {
		t.Fatal(err)
	}
	details.ResultDevice, details.ResultInode = publication.Object.Identity.Device, publication.Object.Identity.Inode
	details.ResultSize, details.ResultVersion = publication.Object.Size, publication.Object.Version
	payload, err := jsonMarshalCatalogDetails(details)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_operations
		SET state = 'storage_applied', result_version = ?, details_json = ? WHERE id = ? AND state = 'intent'`,
		publication.Object.Version, payload, opID); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "CATALOG_CRASH_READY "+opID); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestCatalogPublishSIGKILLRecoveryRemovesUncommittedFileAndRetriesSameKey(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	setup := createIncompleteCatalogSetup(t, stack, "Killed setup", "killed-setup")
	name, key := "killed.ngc", "publish-sigkill-retry"
	stack.close(t)

	command := exec.Command(os.Args[0], "-test.run=^TestCatalogPublishSIGKILLHelper$")
	command.Env = append(os.Environ(),
		catalogSIGKILLHelperEnv+"=1",
		"WSM_CATALOG_CRASH_LIBRARY="+stack.libraryRoot,
		"WSM_CATALOG_CRASH_STATE="+stack.stateRoot,
		"WSM_CATALOG_CRASH_PROGRAM="+stack.programRoot,
		"WSM_CATALOG_CRASH_SETUP="+setup.ID,
		"WSM_CATALOG_CRASH_REVISION="+strconv.FormatInt(int64(setup.Revision), 10),
		"WSM_CATALOG_CRASH_NAME="+name,
		"WSM_CATALOG_CRASH_KEY="+key,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "CATALOG_CRASH_READY ") {
				ready <- scanner.Text()
				return
			}
		}
		ready <- ""
	}()
	var readyLine string
	select {
	case readyLine = <-ready:
		if readyLine == "" {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("crash helper exited before durable point: %s", stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("crash helper did not reach durable point: %s", stderr.String())
	}
	opID := strings.TrimPrefix(readyLine, "CATALOG_CRASH_READY ")
	if !domain.IsValidID(opID) {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("crash helper returned invalid operation ID %q", opID)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILL helper unexpectedly exited successfully")
	}

	stack.open(t)
	if err := stack.service.RecoverCatalogOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stack.programRoot, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SIGKILL publication survived rollback: %v", err)
	}
	if err := filepath.WalkDir(stack.programRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".wsm-") {
			return fmt.Errorf("hidden recovery entry remains: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT state FROM catalog_operations WHERE id = ?`, opID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("SIGKILL recovery state = %q", state)
	}
	assertCatalogClaimMissing(t, stack, key)
	result, err := stack.service.PutCatalogFile(ctx, setup.ID, domain.ArtifactRoleProgram, PutCatalogFileInput{
		ExpectedRevision: setup.Revision, CreateOnly: true, DisplayName: name, Content: bytes.NewReader(catalogSIGKILLContent),
		ExpectedSize: int64(len(catalogSIGKILLContent)), IdempotencyKey: key,
	})
	if err != nil || result.Program == nil || result.Program.SHA256 != catalogDigest(catalogSIGKILLContent) {
		t.Fatalf("same-key retry after SIGKILL = %#v, %v", result, err)
	}
}

func sparseCatalogVersion(stat unix.Stat_t, persistedSHA string) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d:%d:%d:%d:%d:%d:%d:%s", stat.Dev, stat.Ino, stat.Size,
		stat.Mtim.Sec, stat.Mtim.Nsec, stat.Ctim.Sec, stat.Ctim.Nsec, persistedSHA)
	return hex.EncodeToString(hash.Sum(nil))
}

func TestCatalogSparseTenGiBMetadataAndTailRangeStayBounded(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	setup := createIncompleteCatalogSetup(t, stack, "Sparse setup", "sparse-setup")
	const sparseSize int64 = 10 << 30
	const rangeSize int64 = 8
	name := "ten-gib.ngc"
	physical := filepath.Join(stack.programRoot, name)
	file, err := os.OpenFile(physical, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(sparseSize); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("M2\n"), sparseSize-3); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(physical, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Size != sparseSize {
		t.Fatalf("sparse size = %d", stat.Size)
	}
	if allocated := stat.Blocks * 512; allocated > 1<<20 {
		t.Fatalf("sparse fixture allocated %d bytes", allocated)
	}
	persistedSHA := strings.Repeat("0", sha256.Size*2)
	version := sparseCatalogVersion(stat, persistedSHA)
	nameKey, err := domain.ArtifactNameKey(name)
	if err != nil {
		t.Fatal(err)
	}
	artifactID := mustCatalogID(t, domain.NewArtifactID)
	if _, err := stack.database.SQL().ExecContext(ctx, `
		INSERT INTO catalog_files(
			id, library_id, setup_id, role, display_name, relative_path, path_key,
			media_type, byte_size, sha256, object_version, identity_device,
			identity_inode, identity_size, identity_mtime_ns, identity_ctime_ns)
		VALUES (?, ?, ?, 'program', ?, ?, ?, 'text/x-gcode', ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactID, stack.roots.LibraryID(), setup.ID, name, name, nameKey, sparseSize,
		persistedSHA, version, int64(stat.Dev), int64(stat.Ino), stat.Size,
		stat.Mtim.Sec*1_000_000_000+stat.Mtim.Nsec, stat.Ctim.Sec*1_000_000_000+stat.Ctim.Nsec); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	metadata, err := stack.service.InspectCatalogContent(ctx, setup.ID, domain.ArtifactRoleProgram)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("metadata inspection scanned sparse content: %s", elapsed)
	}
	if metadata.ByteSize != sparseSize || metadata.Version != version || metadata.ArtifactID != artifactID {
		t.Fatalf("sparse metadata = %#v", metadata)
	}

	started = time.Now()
	contentRange, err := stack.service.ReadCatalogRange(ctx, setup.ID, domain.ArtifactRoleProgram,
		version, sparseSize-rangeSize, rangeSize)
	elapsed = time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 2*time.Second || int64(len(contentRange.Data)) != rangeSize || contentRange.ByteSize != sparseSize {
		t.Fatalf("bounded sparse range: len=%d total=%d elapsed=%s", len(contentRange.Data), contentRange.ByteSize, elapsed)
	}
	if !bytes.Equal(contentRange.Data, []byte{0, 0, 0, 0, 0, 'M', '2', '\n'}) {
		t.Fatalf("sparse tail = %q", contentRange.Data)
	}
	if _, err := stack.service.ReadCatalogRange(ctx, setup.ID, domain.ArtifactRoleProgram,
		strings.Repeat("f", 64), 0, rangeSize); !domain.IsErrorCode(err, domain.CodeArtifactChanged) {
		t.Fatalf("stale sparse range version = %v", err)
	}
}

func TestCatalogRecoveryRejectsSpecialAndSymlinkJournalTargets(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stack.programRoot, "journal.ngc")); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", sha256.Size*2)
	details := catalogOperationDetails{TargetPath: "journal.ngc", TemporaryPath: ".wsm-upload-" + strings.Repeat("b", 32),
		ResultSHA256: digest, ResultDevice: 1, ResultInode: 1, ResultSize: 1}
	opID := mustCatalogID(t, domain.NewOperationID)
	if err := stack.service.insertCatalogOperation(ctx, opID, "publish", details); err != nil {
		t.Fatal(err)
	}
	err := stack.service.RecoverCatalogOperations(ctx)
	if err == nil || !domain.IsErrorCode(err, domain.CodeStorageUnavailable) {
		t.Fatalf("symlink journal recovery = %v", err)
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "outside" {
		t.Fatalf("outside sentinel changed: %q, %v", content, err)
	}
	var stat unix.Stat_t
	if err := unix.Lstat(filepath.Join(stack.programRoot, "journal.ngc"), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFLNK {
		t.Fatalf("journal symlink changed: mode=%#o err=%v", stat.Mode, err)
	}
}
