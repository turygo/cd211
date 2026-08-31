-- +goose Up
-- Existing SIDs have no trustworthy audience; invalidate them rather than guessing.
DROP INDEX idx_sessions_expires_at;
ALTER TABLE sessions RENAME TO sessions_legacy;
CREATE TABLE sessions (
    sid_digest BLOB PRIMARY KEY NOT NULL,
    audience TEXT NOT NULL CHECK (audience IN ('web', 'qbt')),
    csrf_token TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    CHECK (length(sid_digest) = 32),
    CHECK ((audience = 'web' AND length(csrf_token) > 0) OR (audience = 'qbt' AND length(csrf_token) = 0)),
    CHECK (expires_at > created_at)
);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);
DROP TABLE sessions_legacy;

-- +goose Down
DROP INDEX idx_sessions_expires_at;
DROP TABLE sessions;
CREATE TABLE sessions (
    sid_digest BLOB PRIMARY KEY NOT NULL,
    csrf_token TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    CHECK (length(sid_digest) = 32),
    CHECK (length(csrf_token) > 0),
    CHECK (expires_at > created_at)
);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);
