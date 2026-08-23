-- +goose Up
ALTER TABLE downloads ADD COLUMN tags TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN auto_tmm INTEGER NOT NULL DEFAULT 0 CHECK (auto_tmm IN (0, 1));
ALTER TABLE downloads ADD COLUMN name_overridden INTEGER NOT NULL DEFAULT 0 CHECK (name_overridden IN (0, 1));

CREATE TABLE download_file_overrides (
    download_hash TEXT NOT NULL,
    file_index INTEGER NOT NULL,
    relative_path TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 1 CHECK (priority IN (0, 1, 6, 7)),
    PRIMARY KEY (download_hash, file_index),
    FOREIGN KEY (download_hash) REFERENCES downloads(hash) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE download_file_overrides;
-- SQLite cannot drop columns on all supported versions; the forward migration is intentionally irreversible.
