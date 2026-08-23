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
UPDATE downloads SET save_path = sqlc.arg(save_path), updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state IN ('STOPPED', 'ACCEPTED') AND lease_owner IS NULL AND content_path IS NULL AND copy_source_path IS NULL AND cloud_result_path IS NULL AND row_version = sqlc.arg(expected_row_version);

-- name: GetSetting :one
SELECT * FROM settings WHERE key = sqlc.arg(key);

-- name: TouchDownload :execrows
UPDATE downloads SET updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state != 'DELETED';

-- name: SetDownloadName :execrows
UPDATE downloads SET name = sqlc.arg(name), updated_at = sqlc.arg(updated_at), row_version = row_version + 1
WHERE hash = sqlc.arg(hash) AND state != 'DELETED';
