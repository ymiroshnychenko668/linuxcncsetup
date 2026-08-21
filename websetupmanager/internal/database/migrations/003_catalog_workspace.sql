CREATE TABLE catalog_state (
    library_id TEXT PRIMARY KEY REFERENCES library_instances(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    legacy_migration_state TEXT NOT NULL DEFAULT 'pending'
        CHECK (legacy_migration_state IN ('pending', 'running', 'completed', 'manual_review', 'failed')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE catalog_folders (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    parent_id TEXT REFERENCES catalog_folders(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    name_key TEXT NOT NULL CHECK (name_key <> ''),
    relative_path TEXT NOT NULL CHECK (relative_path <> ''),
    path_key TEXT NOT NULL CHECK (path_key <> ''),
    legacy_source_key TEXT,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (parent_id IS NULL OR parent_id <> id),
    UNIQUE (library_id, id),
    UNIQUE (library_id, path_key)
) STRICT;

CREATE TABLE catalog_setups (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    folder_id TEXT REFERENCES catalog_folders(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 512),
    name_key TEXT NOT NULL CHECK (name_key <> ''),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 65536),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    legacy_setup_id TEXT REFERENCES setups(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (library_id, id)
) STRICT;

CREATE TABLE catalog_files (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    setup_id TEXT NOT NULL REFERENCES catalog_setups(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('program', 'setup_sheet')),
    display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 255),
    relative_path TEXT NOT NULL CHECK (relative_path <> ''),
    path_key TEXT NOT NULL CHECK (path_key <> ''),
    media_type TEXT NOT NULL CHECK (media_type <> ''),
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    object_version TEXT NOT NULL CHECK (object_version <> ''),
    identity_device INTEGER NOT NULL,
    identity_inode INTEGER NOT NULL,
    identity_size INTEGER NOT NULL CHECK (identity_size >= 0),
    identity_mtime_ns INTEGER NOT NULL,
    identity_ctime_ns INTEGER NOT NULL,
    legacy_storage_object_id TEXT REFERENCES storage_objects(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (setup_id, role),
    UNIQUE (library_id, path_key)
) STRICT;

CREATE TABLE catalog_legacy_migrations (
    source_key TEXT PRIMARY KEY CHECK (source_key <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    legacy_setup_id TEXT NOT NULL REFERENCES setups(id) ON DELETE CASCADE,
    legacy_program_artifact_id TEXT REFERENCES setup_artifacts(id) ON DELETE CASCADE,
    catalog_setup_id TEXT REFERENCES catalog_setups(id) ON DELETE SET NULL,
    target_folder_id TEXT REFERENCES catalog_folders(id) ON DELETE SET NULL,
    target_name TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'publishing', 'completed', 'manual_review', 'failed')),
    error_code TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (legacy_program_artifact_id)
) STRICT;

-- One target setup is produced for every legacy program (or one incomplete
-- target when a legacy setup has no program).  A setup sheet is intentionally
-- fanned out and therefore appears once for every target in this manifest.
-- This makes migration accounting restartable and proves that every source
-- artifact was either copied or assigned an explicit outcome.
CREATE TABLE catalog_legacy_file_manifest (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    source_key TEXT NOT NULL REFERENCES catalog_legacy_migrations(source_key) ON DELETE CASCADE,
    legacy_artifact_id TEXT REFERENCES setup_artifacts(id) ON DELETE SET NULL,
    catalog_file_id TEXT REFERENCES catalog_files(id) ON DELETE SET NULL,
    role TEXT NOT NULL CHECK (role IN ('program', 'setup_sheet')),
    target_relative_path TEXT NOT NULL CHECK (target_relative_path <> ''),
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    outcome TEXT NOT NULL DEFAULT 'pending'
        CHECK (outcome IN ('pending', 'copied', 'already_present', 'manual_review', 'failed')),
    error_code TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (source_key, role)
) STRICT;

CREATE TABLE catalog_operations (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    setup_id TEXT REFERENCES catalog_setups(id) ON DELETE SET NULL,
    file_id TEXT,
    operation TEXT NOT NULL CHECK (operation IN ('publish', 'delete', 'move', 'folder_create', 'folder_delete')),
    target_path TEXT NOT NULL CHECK (target_path <> ''),
    temporary_path TEXT,
    expected_version TEXT,
    result_version TEXT,
    expected_revision INTEGER CHECK (expected_revision IS NULL OR expected_revision > 0),
    idempotency_key TEXT,
    request_hash TEXT CHECK (request_hash IS NULL OR length(request_hash) = 64),
    state TEXT NOT NULL DEFAULT 'intent'
        CHECK (state IN ('intent', 'storage_applied', 'db_applied', 'completed', 'failed')),
    details_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details_json)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    completed_at TEXT
) STRICT;

CREATE UNIQUE INDEX catalog_folders_root_name ON catalog_folders(library_id, name_key) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX catalog_folders_child_name ON catalog_folders(library_id, parent_id, name_key) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX catalog_folders_legacy_source ON catalog_folders(library_id, legacy_source_key) WHERE legacy_source_key IS NOT NULL;
CREATE UNIQUE INDEX catalog_setups_root_name ON catalog_setups(library_id, name_key) WHERE folder_id IS NULL;
CREATE UNIQUE INDEX catalog_setups_folder_name ON catalog_setups(library_id, folder_id, name_key) WHERE folder_id IS NOT NULL;
CREATE INDEX catalog_folders_parent ON catalog_folders(library_id, parent_id, name_key, id);
CREATE INDEX catalog_setups_folder ON catalog_setups(library_id, folder_id, name_key, id);
CREATE INDEX catalog_files_setup ON catalog_files(setup_id, role);
CREATE INDEX catalog_legacy_migrations_state ON catalog_legacy_migrations(library_id, state, source_key);
CREATE INDEX catalog_legacy_file_manifest_outcome ON catalog_legacy_file_manifest(outcome, source_key, role);
CREATE INDEX catalog_operations_recovery ON catalog_operations(library_id, state, created_at, id);

CREATE TRIGGER catalog_folders_library_guard_insert
BEFORE INSERT ON catalog_folders
WHEN NEW.parent_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM catalog_folders parent WHERE parent.id = NEW.parent_id AND parent.library_id = NEW.library_id
)
BEGIN SELECT RAISE(ABORT, 'catalog folder library mismatch'); END;

CREATE TRIGGER catalog_folders_library_guard_update
BEFORE UPDATE OF parent_id, library_id ON catalog_folders
WHEN NEW.parent_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM catalog_folders parent WHERE parent.id = NEW.parent_id AND parent.library_id = NEW.library_id
)
BEGIN SELECT RAISE(ABORT, 'catalog folder library mismatch'); END;

CREATE TRIGGER catalog_setups_library_guard_insert
BEFORE INSERT ON catalog_setups
WHEN NEW.folder_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM catalog_folders folder WHERE folder.id = NEW.folder_id AND folder.library_id = NEW.library_id
)
BEGIN SELECT RAISE(ABORT, 'catalog setup library mismatch'); END;

CREATE TRIGGER catalog_setups_library_guard_update
BEFORE UPDATE OF folder_id, library_id ON catalog_setups
WHEN NEW.folder_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM catalog_folders folder WHERE folder.id = NEW.folder_id AND folder.library_id = NEW.library_id
)
BEGIN SELECT RAISE(ABORT, 'catalog setup library mismatch'); END;

CREATE TRIGGER catalog_files_library_guard_insert
BEFORE INSERT ON catalog_files
WHEN NOT EXISTS (
    SELECT 1 FROM catalog_setups setup WHERE setup.id = NEW.setup_id AND setup.library_id = NEW.library_id
)
BEGIN SELECT RAISE(ABORT, 'catalog file library mismatch'); END;

CREATE TRIGGER catalog_files_library_guard_update
BEFORE UPDATE OF setup_id, library_id ON catalog_files
WHEN NOT EXISTS (
    SELECT 1 FROM catalog_setups setup WHERE setup.id = NEW.setup_id AND setup.library_id = NEW.library_id
)
BEGIN SELECT RAISE(ABORT, 'catalog file library mismatch'); END;

CREATE TRIGGER catalog_folders_revision_monotonic
BEFORE UPDATE OF revision ON catalog_folders
WHEN NEW.revision < OLD.revision OR NEW.revision > OLD.revision + 1
BEGIN SELECT RAISE(ABORT, 'catalog folder revision must stay unchanged or increase by one'); END;

CREATE TRIGGER catalog_setups_revision_monotonic
BEFORE UPDATE OF revision ON catalog_setups
WHEN NEW.revision < OLD.revision OR NEW.revision > OLD.revision + 1
BEGIN SELECT RAISE(ABORT, 'catalog setup revision must stay unchanged or increase by one'); END;
