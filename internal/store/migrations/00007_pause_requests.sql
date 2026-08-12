-- +goose Up
ALTER TABLE downloads
ADD COLUMN pause_requested INTEGER NOT NULL DEFAULT 0 CHECK (pause_requested IN (0, 1));

-- +goose Down
ALTER TABLE downloads DROP COLUMN pause_requested;
