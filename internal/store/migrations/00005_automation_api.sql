-- +goose Up
CREATE TABLE api_token (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
    token_hint TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0)
);

-- +goose Down
DROP TABLE api_token;
