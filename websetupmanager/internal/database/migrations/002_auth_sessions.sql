CREATE TABLE auth_sessions (
    token_hash TEXT PRIMARY KEY
        CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    username TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 256),
    csrf_token TEXT NOT NULL
        CHECK (length(csrf_token) = 43 AND csrf_token NOT GLOB '*[^A-Za-z0-9_-]*'),
    created_at TEXT NOT NULL CHECK (created_at <> ''),
    expires_at TEXT NOT NULL CHECK (expires_at <> ''),
    scope TEXT NOT NULL CHECK (length(scope) BETWEEN 1 AND 512),
    CHECK (created_at < expires_at)
) STRICT;

CREATE INDEX auth_sessions_expiry
    ON auth_sessions(expires_at);
