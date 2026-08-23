-- +goose Up
-- cloud_source_path historically identified the completed offline result root.
-- Preserve that identity as cloud_result_path, and initialize the exact copy
-- source to the same root so historical rows retain their old cleanup/copy
-- semantics. New workflows may resolve copy_source_path to a child object.
ALTER TABLE downloads RENAME COLUMN cloud_source_path TO cloud_result_path;
ALTER TABLE downloads ADD COLUMN copy_source_path TEXT;
UPDATE downloads
SET copy_source_path = cloud_result_path
WHERE cloud_result_path IS NOT NULL AND cloud_result_path != '';

-- +goose Down
ALTER TABLE downloads RENAME COLUMN cloud_result_path TO cloud_source_path;
ALTER TABLE downloads DROP COLUMN copy_source_path;
