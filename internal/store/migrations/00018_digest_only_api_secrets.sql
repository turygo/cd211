-- +goose Up
-- Rebuild both singleton tables without plaintext secret columns.
ALTER TABLE api_token RENAME TO api_token_legacy;
CREATE TABLE api_token (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
    token_hint TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0)
);
INSERT INTO api_token (id, token_hash, token_hint, created_at, updated_at, row_version)
SELECT id, token_hash, token_hint, created_at, updated_at, row_version
FROM api_token_legacy;
DROP TABLE api_token_legacy;

ALTER TABLE qbt_api_key RENAME TO qbt_api_key_legacy;
CREATE TABLE qbt_api_key (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    key_hash BLOB NOT NULL CHECK (length(key_hash) = 32),
    key_hint TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0)
);
INSERT INTO qbt_api_key (id, key_hash, key_hint, created_at, updated_at, active, row_version)
SELECT id, key_hash, key_hint, created_at, updated_at, active, row_version
FROM qbt_api_key_legacy;
DROP TABLE qbt_api_key_legacy;

-- +goose Down
ALTER TABLE api_token RENAME TO api_token_digest;
CREATE TABLE api_token (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
    token_hint TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0),
    token_secret TEXT NOT NULL DEFAULT ''
);
INSERT INTO api_token (id, token_hash, token_hint, created_at, updated_at, row_version, token_secret)
SELECT id, token_hash, token_hint, created_at, updated_at, row_version, ''
FROM api_token_digest;
DROP TABLE api_token_digest;

ALTER TABLE qbt_api_key RENAME TO qbt_api_key_digest;
CREATE TABLE qbt_api_key (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    key_hash BLOB NOT NULL CHECK (length(key_hash) = 32),
    key_hint TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0),
    key_secret TEXT NOT NULL DEFAULT ''
);
INSERT INTO qbt_api_key (id, key_hash, key_hint, created_at, updated_at, active, row_version, key_secret)
SELECT id, key_hash, key_hint, created_at, updated_at, active, row_version, ''
FROM qbt_api_key_digest;
DROP TABLE qbt_api_key_digest;
