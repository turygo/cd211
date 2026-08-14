-- +goose Up
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

-- +goose Down
DROP TABLE sessions;
