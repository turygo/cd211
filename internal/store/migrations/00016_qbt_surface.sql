-- +goose Up
ALTER TABLE downloads ADD COLUMN private INTEGER CHECK (private IN (0, 1));
UPDATE downloads SET private = 0 WHERE source_kind = 'torrent';

-- Tags that are created globally remain visible even when unassigned.
CREATE TABLE qbt_tags (
    name TEXT PRIMARY KEY
);

-- +goose Down
DROP TABLE qbt_tags;
-- SQLite cannot drop columns on all supported versions; the forward migration is intentionally irreversible.
