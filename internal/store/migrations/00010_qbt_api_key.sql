-- +goose Up
CREATE TABLE qbt_api_key (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    key_hash BLOB NOT NULL CHECK (length(key_hash) = 32),
    key_hint TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0)
);

-- +goose Down
DROP TABLE qbt_api_key;
