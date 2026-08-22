//go:build linux

package service

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func TestLegacyCatalogMigrationResumesPopulatedVersionThreeLinkedSetup(t *testing.T) {
	stack := newCatalogProductStack(t)
	ctx := context.Background()
	legacy := stack.createLegacySetup(t, "Version three source")
	content := []byte("G0 X23\nM2\n")
	fixture := stack.addLegacyFile(t, legacy.ID, domain.ArtifactRoleProgram,
		"v3-resume.ngc", "text/x-gcode", content, 0)
	sourceKey := "legacy:" + legacy.ID + ":" + fixture.artifactID

	root, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
		Name: "Импортировано", IdempotencyKey: "legacy-folder-root",
	})
	if err != nil {
		t.Fatal(err)
	}
	folder, err := stack.service.CreateCatalogFolder(ctx, CreateCatalogFolderInput{
		ParentFolderID: root.ID, Name: legacy.Name, IdempotencyKey: "legacy-folder:" + legacy.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_folders
		SET legacy_source_key = CASE id WHEN ? THEN 'legacy-folder-root' WHEN ? THEN ? END
		WHERE id IN (?, ?)`, root.ID, folder.ID, "legacy-folder:"+legacy.ID, root.ID, folder.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `INSERT INTO catalog_legacy_migrations(
		source_key, library_id, legacy_setup_id, legacy_program_artifact_id, target_folder_id, target_name, state)
		VALUES (?, ?, ?, ?, ?, 'v3-resume', 'pending')`, sourceKey, stack.roots.LibraryID(),
		legacy.ID, fixture.artifactID, folder.ID); err != nil {
		t.Fatal(err)
	}
	setup, err := stack.service.CreateCatalogSetup(ctx, CreateCatalogSetupInput{
		FolderID: folder.ID, Name: "v3-resume", Description: legacy.Description,
		IdempotencyKey: "legacy-setup:" + sourceKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx,
		`UPDATE catalog_setups SET legacy_setup_id = ? WHERE id = ?`, legacy.ID, setup.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_legacy_migrations
		SET catalog_setup_id = ?, state = 'publishing' WHERE source_key = ?`, setup.ID, sourceKey); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.database.SQL().ExecContext(ctx, `UPDATE catalog_state
		SET legacy_migration_state = 'running' WHERE library_id = ?`, stack.roots.LibraryID()); err != nil {
		t.Fatal(err)
	}

	stack.close(t)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(stack.stateRoot, "websetupmanager.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP INDEX catalog_setups_legacy_source;
		ALTER TABLE catalog_setups DROP COLUMN legacy_source_key;
		ALTER TABLE auth_sessions DROP COLUMN activated;
		DELETE FROM schema_migrations WHERE version >= 4;`); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	stack.open(t)

	var backfilled string
	if err := stack.database.SQL().QueryRowContext(ctx, `SELECT COALESCE(legacy_source_key, '')
		FROM catalog_setups WHERE id = ?`, setup.ID).Scan(&backfilled); err != nil {
		t.Fatal(err)
	}
	if backfilled != sourceKey {
		t.Fatalf("v3 setup provenance = %q, want %q", backfilled, sourceKey)
	}
	stack.service.now = func() time.Time { return time.Now().UTC().Add(2 * defaultIdempotencyTTL) }
	if err := stack.service.MigrateLegacyCatalog(ctx); err != nil {
		t.Fatalf("resume populated v3 catalog: %v", err)
	}
	assertCompletedLegacyRestart(t, stack, legacy.ID, sourceKey, fixture, content)
}

func TestLegacyCatalogMigrationResumesAtomicCheckpointAfterExpiredIdempotency(t *testing.T) {
	for _, checkpoint := range []string{"folder-created", "setup-created", "file-published"} {
		t.Run(checkpoint, func(t *testing.T) {
			stack := newCatalogProductStack(t)
			ctx := context.Background()
			legacy := stack.createLegacySetup(t, "Restart source")
			content := []byte("G0 X17\nM2\n")
			fixture := stack.addLegacyFile(t, legacy.ID, domain.ArtifactRoleProgram,
				"restart.ngc", "text/x-gcode", content, 0)
			sourceKey := "legacy:" + legacy.ID + ":" + fixture.artifactID

			hit, err := runLegacyMigrationUntilCheckpoint(stack.service, checkpoint)
			if err != nil {
				t.Fatalf("migration before %s: %v", checkpoint, err)
			}
			if !hit {
				t.Fatalf("migration did not reach checkpoint %q", checkpoint)
			}
			beforeID := legacyCheckpointDurableID(t, stack, checkpoint, sourceKey)
			if beforeID == "" {
				t.Fatalf("checkpoint %q left no durable ownership linkage", checkpoint)
			}

			stack.restart(t)
			future := time.Now().UTC().Add(2 * defaultIdempotencyTTL)
			stack.service.now = func() time.Time { return future }
			var unexpired int
			if err := stack.database.SQL().QueryRowContext(ctx, `SELECT count(*)
				FROM idempotency_requests WHERE key LIKE 'legacy-%' AND expires_at > ?`,
				sqlTimestamp(future)).Scan(&unexpired); err != nil {
				t.Fatal(err)
			}
			if unexpired != 0 {
				t.Fatalf("checkpoint %q still has %d unexpired migration claims", checkpoint, unexpired)
			}
			if err := stack.service.MigrateLegacyCatalog(ctx); err != nil {
				t.Fatalf("resume after %s: %v", checkpoint, err)
			}
			if afterID := legacyCheckpointDurableID(t, stack, checkpoint, sourceKey); afterID != beforeID {
				t.Fatalf("checkpoint %q durable target changed: %q -> %q", checkpoint, beforeID, afterID)
			}

			assertCompletedLegacyRestart(t, stack, legacy.ID, sourceKey, fixture, content)
		})
	}
}

func runLegacyMigrationUntilCheckpoint(service *Service, checkpoint string) (hit bool, err error) {
	marker := new(int)
	service.legacyMigrationTestHook = func(actual string) {
		if actual == checkpoint {
			panic(marker)
		}
	}
	defer func() {
		service.legacyMigrationTestHook = nil
		if recovered := recover(); recovered != nil {
			if recovered != marker {
				panic(recovered)
			}
			hit = true
			err = nil
		}
	}()
	err = service.MigrateLegacyCatalog(context.Background())
	return false, err
}

func legacyCheckpointDurableID(t *testing.T, stack *catalogProductStack, checkpoint, sourceKey string) string {
	t.Helper()
	var id string
	var err error
	switch checkpoint {
	case "folder-created":
		err = stack.database.SQL().QueryRow(`SELECT id FROM catalog_folders
			WHERE library_id = ? AND legacy_source_key = 'legacy-folder-root'`, stack.roots.LibraryID()).Scan(&id)
	case "setup-created":
		err = stack.database.SQL().QueryRow(`SELECT COALESCE(catalog_setup_id, '')
			FROM catalog_legacy_migrations WHERE source_key = ?`, sourceKey).Scan(&id)
	case "file-published":
		err = stack.database.SQL().QueryRow(`SELECT COALESCE(catalog_file_id, '')
			FROM catalog_legacy_file_manifest WHERE source_key = ? AND role = 'program'`, sourceKey).Scan(&id)
	default:
		t.Fatalf("unknown migration checkpoint %q", checkpoint)
	}
	if err != nil {
		t.Fatalf("read checkpoint %q linkage: %v", checkpoint, err)
	}
	return id
}

func assertCompletedLegacyRestart(t *testing.T, stack *catalogProductStack, legacySetupID, sourceKey string,
	fixture legacyFixtureFile, content []byte,
) {
	t.Helper()
	ctx := context.Background()
	var folders, setups, files, mappings, manifests int
	for query, destination := range map[string]*int{
		`SELECT count(*) FROM catalog_folders`:              &folders,
		`SELECT count(*) FROM catalog_setups`:               &setups,
		`SELECT count(*) FROM catalog_files`:                &files,
		`SELECT count(*) FROM catalog_legacy_migrations`:    &mappings,
		`SELECT count(*) FROM catalog_legacy_file_manifest`: &manifests,
	} {
		if err := stack.database.SQL().QueryRowContext(ctx, query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if folders != 2 || setups != 1 || files != 1 || mappings != 1 || manifests != 1 {
		t.Fatalf("restart duplicated catalog rows: folders=%d setups=%d files=%d mappings=%d manifests=%d",
			folders, setups, files, mappings, manifests)
	}

	var migrationState, setupSource, setupLegacyID, manifestOutcome string
	var setupID, mappedSetupID, fileID, mappedFileID, legacyObjectID string
	var targetPath, filePath, manifestSHA, fileSHA string
	err := stack.database.SQL().QueryRowContext(ctx, `SELECT state.catalog_state,
		migration.catalog_setup_id, setup.id, setup.legacy_source_key, setup.legacy_setup_id,
		manifest.outcome, manifest.catalog_file_id, file.id, file.legacy_storage_object_id,
		manifest.target_relative_path, file.relative_path, manifest.sha256, file.sha256
		FROM (SELECT legacy_migration_state AS catalog_state FROM catalog_state WHERE library_id = ?) state
		JOIN catalog_legacy_migrations migration ON migration.source_key = ?
		JOIN catalog_setups setup ON setup.id = migration.catalog_setup_id
		JOIN catalog_legacy_file_manifest manifest ON manifest.source_key = migration.source_key AND manifest.role = 'program'
		JOIN catalog_files file ON file.id = manifest.catalog_file_id`, stack.roots.LibraryID(), sourceKey).Scan(
		&migrationState, &mappedSetupID, &setupID, &setupSource, &setupLegacyID,
		&manifestOutcome, &mappedFileID, &fileID, &legacyObjectID,
		&targetPath, &filePath, &manifestSHA, &fileSHA)
	if err != nil {
		t.Fatal(err)
	}
	if migrationState != "completed" || mappedSetupID == "" || mappedSetupID != setupID ||
		setupSource != sourceKey || setupLegacyID != legacySetupID || manifestOutcome != "copied" ||
		mappedFileID == "" || mappedFileID != fileID || legacyObjectID != fixture.objectID ||
		targetPath != filePath || manifestSHA != fixture.object.SHA256 || fileSHA != fixture.object.SHA256 {
		t.Fatalf("atomic migration linkage mismatch: state=%q setup=%q/%q source=%q legacy=%q file=%q/%q object=%q path=%q/%q sha=%q/%q",
			migrationState, mappedSetupID, setupID, setupSource, setupLegacyID, mappedFileID, fileID,
			legacyObjectID, targetPath, filePath, manifestSHA, fileSHA)
	}
	actual, err := os.ReadFile(filepath.Join(stack.programRoot, filepath.FromSlash(filePath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, content) {
		t.Fatalf("migrated bytes changed after restart: %q", actual)
	}
}
