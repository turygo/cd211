-- name: ListDownloadFileOverrides :many
SELECT * FROM download_file_overrides
WHERE download_hash = sqlc.arg(download_hash)
ORDER BY file_index ASC;

-- name: UpsertDownloadFileOverride :exec
INSERT INTO download_file_overrides (download_hash, file_index, relative_path, priority)
VALUES (sqlc.arg(download_hash), sqlc.arg(file_index), sqlc.arg(relative_path), sqlc.arg(priority))
ON CONFLICT(download_hash, file_index) DO UPDATE SET
  relative_path = excluded.relative_path,
  priority = excluded.priority;

-- name: DeleteDownloadFileOverride :exec
DELETE FROM download_file_overrides
WHERE download_hash = sqlc.arg(download_hash) AND file_index = sqlc.arg(file_index);

-- name: UpdateDownloadTags :execrows
UPDATE downloads SET tags = sqlc.arg(tags), updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state != 'DELETED';

-- name: UpdateDownloadAutoTMM :execrows
UPDATE downloads SET auto_tmm = sqlc.arg(auto_tmm), updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state != 'DELETED';

-- name: UpdateDownloadSavePath :execrows
UPDATE downloads SET save_path = sqlc.arg(save_path), workspace_path = sqlc.narg(workspace_path), updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state IN ('STOPPED', 'ACCEPTED') AND lease_owner IS NULL AND content_path IS NULL AND copy_source_path IS NULL AND cloud_result_path IS NULL AND row_version = sqlc.arg(expected_row_version);

-- name: GetSetting :one
SELECT * FROM settings WHERE key = sqlc.arg(key);

-- name: TouchDownload :execrows
UPDATE downloads SET updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state != 'DELETED';

-- name: SetDownloadName :execrows
UPDATE downloads SET name = sqlc.arg(name), name_overridden = 1, updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state != 'DELETED';
-- name: ListQbtTags :many
SELECT name FROM qbt_tags ORDER BY name ASC;

-- name: InsertQbtTag :execrows
INSERT INTO qbt_tags (name) VALUES (sqlc.arg(name))
ON CONFLICT(name) DO NOTHING;

-- name: DeleteQbtTag :execrows
DELETE FROM qbt_tags WHERE name = sqlc.arg(name);

-- name: DeleteQbtCategories :execrows
DELETE FROM categories
WHERE name IN (sqlc.slice(names));

-- name: ClearDownloadCategories :execrows
UPDATE downloads
SET category = '', updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE category IN (sqlc.slice(names))
  AND state != 'DELETED';

-- name: ReverifyDownload :execrows
UPDATE downloads
SET
    state = 'VERIFYING_LOCAL',
    last_error = NULL,
    last_error_code = NULL,
    attempt_count = 0,
    phase_started_at = sqlc.arg(now),
    next_run_at = sqlc.arg(now),
    lease_until = NULL,
    lease_owner = NULL,
    completed_at = NULL,
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state = 'COMPLETED';
