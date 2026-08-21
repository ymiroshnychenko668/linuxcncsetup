ALTER TABLE catalog_setups ADD COLUMN legacy_source_key TEXT;

-- Version 3 linked a migrated setup in a separate transaction after creating
-- it. Only an exact, same-library mapping with matching legacy provenance is
-- safe to adopt. Make any orphan, mismatch, or multiply-linked target abort
-- this migration transaction instead of choosing a row by name.
CREATE TABLE _catalog_migration_004_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
) STRICT;

INSERT INTO _catalog_migration_004_guard(valid)
SELECT CASE WHEN
    EXISTS (
        SELECT 1
          FROM catalog_legacy_migrations mapping
          LEFT JOIN catalog_setups setup ON setup.id = mapping.catalog_setup_id
         WHERE mapping.catalog_setup_id IS NOT NULL
           AND (
               setup.id IS NULL OR
               setup.library_id <> mapping.library_id OR
               setup.legacy_setup_id IS NULL OR
               setup.legacy_setup_id <> mapping.legacy_setup_id
           )
    ) OR
    EXISTS (
        SELECT mapping.catalog_setup_id
          FROM catalog_legacy_migrations mapping
         WHERE mapping.catalog_setup_id IS NOT NULL
         GROUP BY mapping.catalog_setup_id
        HAVING count(*) <> 1
    ) OR
    EXISTS (
        SELECT 1
          FROM catalog_setups setup
         WHERE setup.legacy_setup_id IS NOT NULL
           AND (
               SELECT count(*)
                 FROM catalog_legacy_migrations mapping
                WHERE mapping.catalog_setup_id = setup.id
                  AND mapping.library_id = setup.library_id
                  AND mapping.legacy_setup_id = setup.legacy_setup_id
           ) <> 1
    )
THEN 0 ELSE 1 END;

UPDATE catalog_setups AS setup
   SET legacy_source_key = (
       SELECT mapping.source_key
         FROM catalog_legacy_migrations mapping
        WHERE mapping.catalog_setup_id = setup.id
          AND mapping.library_id = setup.library_id
          AND mapping.legacy_setup_id = setup.legacy_setup_id
   )
 WHERE setup.legacy_setup_id IS NOT NULL;

DROP TABLE _catalog_migration_004_guard;

CREATE UNIQUE INDEX catalog_setups_legacy_source
    ON catalog_setups(library_id, legacy_source_key)
    WHERE legacy_source_key IS NOT NULL;
