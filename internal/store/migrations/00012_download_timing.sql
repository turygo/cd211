-- +goose Up
ALTER TABLE downloads ADD COLUMN offline_started_at TIMESTAMP;
ALTER TABLE downloads ADD COLUMN copy_completed_at TIMESTAMP;

-- +goose Down
ALTER TABLE downloads DROP COLUMN copy_completed_at;
ALTER TABLE downloads DROP COLUMN offline_started_at;
