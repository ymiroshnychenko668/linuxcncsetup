CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL CHECK (name <> ''),
    checksum TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE library_instances (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    fingerprint TEXT NOT NULL UNIQUE CHECK (fingerprint <> ''),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE setups (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 512),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 65536),
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'ready', 'attention', 'archived')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    source TEXT NOT NULL DEFAULT 'created'
        CHECK (source IN ('created', 'imported', 'duplicated')),
    source_setup_id TEXT,
    ready_revision INTEGER CHECK (ready_revision IS NULL OR ready_revision > 0),
    attention_reason TEXT,
    archived_from_status TEXT
        CHECK (archived_from_status IS NULL OR archived_from_status IN ('draft', 'ready', 'attention')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    archived_at TEXT,
    CHECK ((status = 'archived') = (archived_from_status IS NOT NULL)),
    CHECK (status = 'ready' OR ready_revision IS NULL OR ready_revision <= revision)
) STRICT;

CREATE TABLE storage_objects (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE RESTRICT,
    storage_key TEXT NOT NULL CHECK (storage_key <> ''),
    media_type TEXT NOT NULL CHECK (media_type <> ''),
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    sha256 TEXT CHECK (sha256 IS NULL OR length(sha256) = 64),
    ref_count INTEGER NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_verified_at TEXT,
    UNIQUE (library_id, storage_key)
) STRICT;

CREATE TABLE setup_artifacts (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    setup_id TEXT NOT NULL REFERENCES setups(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('program', 'setup_sheet')),
    display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 255),
    normalized_name TEXT NOT NULL CHECK (length(normalized_name) BETWEEN 1 AND 1024),
    storage_object_id TEXT NOT NULL REFERENCES storage_objects(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    identity_device INTEGER NOT NULL,
    identity_inode INTEGER NOT NULL,
    identity_size INTEGER NOT NULL CHECK (identity_size >= 0),
    identity_mtime_ns INTEGER NOT NULL,
    identity_ctime_ns INTEGER NOT NULL,
    object_version TEXT NOT NULL CHECK (object_version <> ''),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (role = 'program' OR is_primary = 0),
    UNIQUE (setup_id, normalized_name)
) STRICT;

CREATE TABLE validation_runs (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    setup_id TEXT NOT NULL REFERENCES setups(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'conflict')),
    result_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at TEXT,
    finished_at TEXT,
    CHECK (json_valid(result_json))
) STRICT;

CREATE TABLE current_setup (
    library_id TEXT PRIMARY KEY REFERENCES library_instances(id) ON DELETE CASCADE,
    setup_id TEXT NOT NULL UNIQUE REFERENCES setups(id) ON DELETE RESTRICT,
    revision_selected INTEGER NOT NULL CHECK (revision_selected > 0),
    selected_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE recent_setups (
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    setup_id TEXT NOT NULL REFERENCES setups(id) ON DELETE CASCADE,
    last_artifact_id TEXT REFERENCES setup_artifacts(id) ON DELETE SET NULL,
    last_line INTEGER CHECK (last_line IS NULL OR last_line > 0),
    last_opened_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (library_id, setup_id)
) STRICT;

CREATE TABLE import_sessions (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE RESTRICT,
    idempotency_key TEXT NOT NULL CHECK (idempotency_key <> ''),
    setup_name TEXT NOT NULL CHECK (length(setup_name) BETWEEN 1 AND 512),
    setup_description TEXT NOT NULL DEFAULT '' CHECK (length(setup_description) <= 65536),
    state TEXT NOT NULL DEFAULT 'staging'
        CHECK (state IN ('staging', 'committing', 'succeeded', 'draft_saved', 'failed', 'cancelled', 'conflict')),
    bytes_received INTEGER NOT NULL DEFAULT 0 CHECK (bytes_received >= 0),
    byte_limit INTEGER CHECK (byte_limit IS NULL OR byte_limit >= 0),
    setup_id TEXT REFERENCES setups(id) ON DELETE SET NULL,
    error_code TEXT,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at TEXT,
    UNIQUE (library_id, idempotency_key)
) STRICT;

CREATE TABLE import_artifacts (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    import_session_id TEXT NOT NULL REFERENCES import_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('program', 'setup_sheet')),
    display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 255),
    normalized_name TEXT NOT NULL CHECK (length(normalized_name) BETWEEN 1 AND 1024),
    staging_key TEXT NOT NULL CHECK (staging_key <> ''),
    media_type TEXT,
    byte_size INTEGER NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    sha256 TEXT CHECK (sha256 IS NULL OR length(sha256) = 64),
    object_version TEXT,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'uploading', 'staged', 'excluded', 'published', 'failed')),
    storage_object_id TEXT REFERENCES storage_objects(id) ON DELETE RESTRICT,
    artifact_id TEXT REFERENCES setup_artifacts(id) ON DELETE SET NULL,
    error_code TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (import_session_id, normalized_name)
) STRICT;

CREATE TABLE jobs (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind <> ''),
    setup_id TEXT REFERENCES setups(id) ON DELETE SET NULL,
    import_session_id TEXT REFERENCES import_sessions(id) ON DELETE SET NULL,
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'conflict')),
    progress REAL NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 1),
    bytes_done INTEGER NOT NULL DEFAULT 0 CHECK (bytes_done >= 0),
    bytes_total INTEGER CHECK (bytes_total IS NULL OR bytes_total >= 0),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    error_code TEXT,
    result_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at TEXT,
    finished_at TEXT,
    CHECK (json_valid(result_json)),
    CHECK (bytes_total IS NULL OR bytes_done <= bytes_total)
) STRICT;

CREATE TABLE ui_state (
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL CHECK (client_id <> ''),
    screen TEXT NOT NULL DEFAULT 'library',
    selected_setup_id TEXT REFERENCES setups(id) ON DELETE SET NULL,
    selected_artifact_id TEXT REFERENCES setup_artifacts(id) ON DELETE SET NULL,
    filters_json TEXT NOT NULL DEFAULT '{}',
    view_json TEXT NOT NULL DEFAULT '{}',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (library_id, client_id),
    CHECK (json_valid(filters_json)),
    CHECK (json_valid(view_json))
) STRICT;

CREATE TABLE operation_journal (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE RESTRICT,
    operation TEXT NOT NULL CHECK (operation <> ''),
    setup_id TEXT,
    artifact_id TEXT,
    storage_object_id TEXT,
    import_session_id TEXT,
    job_id TEXT,
    expected_revision INTEGER CHECK (expected_revision IS NULL OR expected_revision > 0),
    target_revision INTEGER CHECK (target_revision IS NULL OR target_revision > 0),
    state TEXT NOT NULL DEFAULT 'intent'
        CHECK (state IN ('intent', 'storage_applied', 'db_applied', 'completed', 'failed', 'conflict')),
    details_json TEXT NOT NULL DEFAULT '{}',
    error_code TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    completed_at TEXT,
    CHECK (json_valid(details_json))
) STRICT;

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE RESTRICT,
    operation TEXT NOT NULL CHECK (operation <> ''),
    setup_id TEXT,
    artifact_id TEXT,
    job_id TEXT,
    from_revision INTEGER CHECK (from_revision IS NULL OR from_revision > 0),
    to_revision INTEGER CHECK (to_revision IS NULL OR to_revision > 0),
    result TEXT NOT NULL CHECK (result IN ('succeeded', 'failed', 'cancelled', 'conflict')),
    error_code TEXT,
    details_json TEXT NOT NULL DEFAULT '{}',
    occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (json_valid(details_json))
) STRICT;

CREATE TABLE idempotency_requests (
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    key TEXT NOT NULL CHECK (key <> ''),
    operation TEXT NOT NULL CHECK (operation <> ''),
    request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
    state TEXT NOT NULL DEFAULT 'in_progress'
        CHECK (state IN ('in_progress', 'completed', 'failed', 'conflict')),
    response_status INTEGER CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
    result_json TEXT NOT NULL DEFAULT '{}',
    error_code TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at TEXT NOT NULL,
    CHECK (json_valid(result_json)),
    PRIMARY KEY (library_id, key)
) STRICT;

CREATE TABLE delete_confirmations (
    token_hash TEXT PRIMARY KEY CHECK (length(token_hash) = 64),
    library_id TEXT NOT NULL REFERENCES library_instances(id) ON DELETE CASCADE,
    setup_id TEXT NOT NULL REFERENCES setups(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    exact_name TEXT NOT NULL,
    program_count INTEGER NOT NULL CHECK (program_count >= 0),
    has_setup_sheet INTEGER NOT NULL CHECK (has_setup_sheet IN (0, 1)),
    unique_bytes INTEGER NOT NULL CHECK (unique_bytes >= 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at TEXT NOT NULL,
    consumed_at TEXT
) STRICT;

CREATE UNIQUE INDEX setup_artifacts_one_primary_program
    ON setup_artifacts(setup_id)
    WHERE role = 'program' AND is_primary = 1;

CREATE UNIQUE INDEX setup_artifacts_one_setup_sheet
    ON setup_artifacts(setup_id)
    WHERE role = 'setup_sheet';

CREATE UNIQUE INDEX import_artifacts_one_setup_sheet
    ON import_artifacts(import_session_id)
    WHERE role = 'setup_sheet' AND state <> 'excluded';

CREATE INDEX setups_library_status_updated
    ON setups(library_id, status, updated_at DESC, id DESC);
CREATE INDEX setups_library_name
    ON setups(library_id, name COLLATE NOCASE, id);
CREATE INDEX setup_artifacts_setup_role_position
    ON setup_artifacts(setup_id, role, position, id);
CREATE INDEX setup_artifacts_storage_object
    ON setup_artifacts(storage_object_id);
CREATE INDEX setup_artifacts_display_name
    ON setup_artifacts(display_name COLLATE NOCASE, setup_id);
CREATE INDEX storage_objects_gc
    ON storage_objects(library_id, ref_count, created_at);
CREATE INDEX validation_runs_setup_revision
    ON validation_runs(setup_id, revision, created_at DESC);
CREATE INDEX recent_setups_last_opened
    ON recent_setups(library_id, last_opened_at DESC, setup_id);
CREATE INDEX import_sessions_state_expiry
    ON import_sessions(library_id, state, expires_at);
CREATE INDEX import_artifacts_session_state
    ON import_artifacts(import_session_id, state, id);
CREATE INDEX jobs_state_updated
    ON jobs(library_id, state, updated_at, id);
CREATE INDEX jobs_setup
    ON jobs(setup_id, created_at DESC);
CREATE INDEX operation_journal_recovery
    ON operation_journal(library_id, state, created_at, id);
CREATE INDEX operation_journal_storage_object
    ON operation_journal(storage_object_id, state);
CREATE INDEX audit_events_setup_time
    ON audit_events(library_id, setup_id, occurred_at DESC, id);
CREATE INDEX idempotency_requests_expiry
    ON idempotency_requests(library_id, expires_at);
CREATE INDEX delete_confirmations_expiry
    ON delete_confirmations(library_id, expires_at);

CREATE TRIGGER setups_revision_monotonic
BEFORE UPDATE OF revision ON setups
WHEN NEW.revision < OLD.revision OR NEW.revision > OLD.revision + 1
BEGIN
    SELECT RAISE(ABORT, 'setup revision must stay unchanged or increase by one');
END;

CREATE TRIGGER setup_artifacts_library_guard_insert
BEFORE INSERT ON setup_artifacts
BEGIN
    SELECT CASE WHEN
        (SELECT library_id FROM setups WHERE id = NEW.setup_id) IS NOT
        (SELECT library_id FROM storage_objects WHERE id = NEW.storage_object_id)
    THEN RAISE(ABORT, 'artifact and storage object library mismatch') END;
END;

CREATE TRIGGER setup_artifacts_library_guard_update
BEFORE UPDATE OF setup_id, storage_object_id ON setup_artifacts
BEGIN
    SELECT CASE WHEN
        (SELECT library_id FROM setups WHERE id = NEW.setup_id) IS NOT
        (SELECT library_id FROM storage_objects WHERE id = NEW.storage_object_id)
    THEN RAISE(ABORT, 'artifact and storage object library mismatch') END;
END;

CREATE TRIGGER setup_artifacts_ref_insert
AFTER INSERT ON setup_artifacts
BEGIN
    UPDATE storage_objects
       SET ref_count = ref_count + 1
     WHERE id = NEW.storage_object_id;
END;

CREATE TRIGGER setup_artifacts_ref_delete
AFTER DELETE ON setup_artifacts
BEGIN
    UPDATE storage_objects
       SET ref_count = ref_count - 1
     WHERE id = OLD.storage_object_id;
END;

CREATE TRIGGER setup_artifacts_ref_update
AFTER UPDATE OF storage_object_id ON setup_artifacts
WHEN NEW.storage_object_id <> OLD.storage_object_id
BEGIN
    UPDATE storage_objects
       SET ref_count = ref_count - 1
     WHERE id = OLD.storage_object_id;
    UPDATE storage_objects
       SET ref_count = ref_count + 1
     WHERE id = NEW.storage_object_id;
END;

CREATE TRIGGER storage_objects_delete_guard
BEFORE DELETE ON storage_objects
WHEN OLD.ref_count <> 0 OR EXISTS (
    SELECT 1
      FROM operation_journal
     WHERE storage_object_id = OLD.id
       AND state IN ('intent', 'storage_applied', 'db_applied')
)
BEGIN
    SELECT RAISE(ABORT, 'storage object is still in use');
END;

CREATE TRIGGER current_setup_guard_insert
BEFORE INSERT ON current_setup
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
          FROM setups
         WHERE id = NEW.setup_id
           AND library_id = NEW.library_id
           AND status = 'ready'
           AND revision = NEW.revision_selected
    ) THEN RAISE(ABORT, 'current setup must be the ready revision in this library') END;
END;

CREATE TRIGGER current_setup_guard_update
BEFORE UPDATE OF library_id, setup_id, revision_selected ON current_setup
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
          FROM setups
         WHERE id = NEW.setup_id
           AND library_id = NEW.library_id
           AND status = 'ready'
           AND revision = NEW.revision_selected
    ) THEN RAISE(ABORT, 'current setup must be the ready revision in this library') END;
END;

CREATE TRIGGER setups_no_archive_current
BEFORE UPDATE OF status ON setups
WHEN NEW.status = 'archived' AND EXISTS (
    SELECT 1 FROM current_setup WHERE setup_id = OLD.id
)
BEGIN
    SELECT RAISE(ABORT, 'current setup cannot be archived');
END;

CREATE TRIGGER recent_setups_guard_insert
BEFORE INSERT ON recent_setups
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM setups
         WHERE id = NEW.setup_id AND library_id = NEW.library_id
    ) THEN RAISE(ABORT, 'recent setup library mismatch') END;
    SELECT CASE WHEN NEW.last_artifact_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM setup_artifacts
         WHERE id = NEW.last_artifact_id AND setup_id = NEW.setup_id
    ) THEN RAISE(ABORT, 'recent artifact setup mismatch') END;
END;

CREATE TRIGGER recent_setups_guard_update
BEFORE UPDATE OF library_id, setup_id, last_artifact_id ON recent_setups
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM setups
         WHERE id = NEW.setup_id AND library_id = NEW.library_id
    ) THEN RAISE(ABORT, 'recent setup library mismatch') END;
    SELECT CASE WHEN NEW.last_artifact_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM setup_artifacts
         WHERE id = NEW.last_artifact_id AND setup_id = NEW.setup_id
    ) THEN RAISE(ABORT, 'recent artifact setup mismatch') END;
END;
