ALTER TABLE auth_sessions
    ADD COLUMN activated INTEGER NOT NULL DEFAULT 0
        CHECK (activated IN (0, 1));
