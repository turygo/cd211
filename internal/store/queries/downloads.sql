-- name: InsertDownload :exec
INSERT INTO downloads (
    hash,
    name,
    source_kind,
    submission_uri,
    category,
    cloud_folder,
    save_path,
    destination_name,
    cloud_task_name,
    cloud_source_path,
    content_path,
    is_multi_file,
    total_size,
    state,
    offline_progress,
    copy_progress,
    qbit_progress,
    last_upstream_status,
    last_error,
    phase_started_at,
    next_run_at,
    lease_until,
    lease_owner,
    attempt_count,
    delete_files_requested,
    created_at,
    updated_at,
    completed_at,
    removed_at,
    row_version
) VALUES (
    sqlc.arg(hash),
    sqlc.arg(name),
    sqlc.arg(source_kind),
    sqlc.arg(submission_uri),
    sqlc.arg(category),
    sqlc.arg(cloud_folder),
    sqlc.arg(save_path),
    sqlc.arg(destination_name),
    sqlc.arg(cloud_task_name),
    sqlc.arg(cloud_source_path),
    sqlc.arg(content_path),
    sqlc.arg(is_multi_file),
    sqlc.arg(total_size),
    sqlc.arg(state),
    sqlc.arg(offline_progress),
    sqlc.arg(copy_progress),
    sqlc.arg(qbit_progress),
    sqlc.arg(last_upstream_status),
    sqlc.arg(last_error),
    sqlc.arg(phase_started_at),
    sqlc.arg(next_run_at),
    sqlc.arg(lease_until),
    sqlc.arg(lease_owner),
    sqlc.arg(attempt_count),
    sqlc.arg(delete_files_requested),
    sqlc.arg(created_at),
    sqlc.arg(updated_at),
    sqlc.arg(completed_at),
    sqlc.arg(removed_at),
    sqlc.arg(row_version)
);

-- name: GetDownload :one
SELECT *
FROM downloads
WHERE hash = sqlc.arg(hash);

-- name: ListVisibleDownloads :many
SELECT *
FROM downloads
WHERE category = sqlc.arg(category)
  AND (removed_at IS NULL OR (state = 'DELETE_REQUESTED' AND last_error IS NOT NULL))
ORDER BY created_at DESC, hash ASC;

-- name: InsertDownloadFile :exec
INSERT INTO download_files (
    download_hash,
    file_index,
    relative_path,
    size
) VALUES (
    sqlc.arg(download_hash),
    sqlc.arg(file_index),
    sqlc.arg(relative_path),
    sqlc.arg(size)
);

-- name: ListDownloadFiles :many
SELECT *
FROM download_files
WHERE download_hash = sqlc.arg(download_hash)
ORDER BY file_index ASC;

-- name: ListAllVisibleDownloads :many
SELECT *
FROM downloads
WHERE removed_at IS NULL OR (state = 'DELETE_REQUESTED' AND last_error IS NOT NULL)
ORDER BY created_at DESC, hash ASC;

-- name: DeleteDownloadFiles :exec
DELETE FROM download_files
WHERE download_hash = sqlc.arg(download_hash);

-- name: ReviveDownload :exec
UPDATE downloads
SET
    name = sqlc.arg(name),
    source_kind = sqlc.arg(source_kind),
    submission_uri = sqlc.arg(submission_uri),
    category = sqlc.arg(category),
    cloud_folder = sqlc.arg(cloud_folder),
    save_path = sqlc.arg(save_path),
    destination_name = sqlc.narg(destination_name),
    cloud_task_name = sqlc.arg(cloud_task_name),
    cloud_source_path = sqlc.arg(cloud_source_path),
    content_path = sqlc.narg(content_path),
    is_multi_file = sqlc.narg(is_multi_file),
    total_size = sqlc.arg(total_size),
    state = sqlc.arg(state),
    offline_progress = sqlc.arg(offline_progress),
    copy_progress = sqlc.arg(copy_progress),
    qbit_progress = sqlc.arg(qbit_progress),
    last_upstream_status = sqlc.narg(last_upstream_status),
    last_error = NULL,
    phase_started_at = sqlc.arg(phase_started_at),
    next_run_at = sqlc.arg(next_run_at),
    lease_until = NULL,
    lease_owner = NULL,
    attempt_count = 0,
    delete_files_requested = 0,
    pause_requested = 0,
    created_at = sqlc.arg(created_at),
    updated_at = sqlc.arg(updated_at),
    completed_at = NULL,
    removed_at = NULL,
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state = 'DELETED';

-- name: SetDownloadCategory :execrows
UPDATE downloads
SET
    category = sqlc.arg(category),
    updated_at = sqlc.arg(updated_at),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash);

-- name: StartDownload :execrows
UPDATE downloads
SET
    state = CASE
        WHEN cloud_source_path IS NOT NULL
             AND is_multi_file IS NOT NULL
             AND (content_path IS NOT NULL OR last_upstream_status IN ('copy:COMPLETED', 'revive:retained_content'))
        THEN 'VERIFYING_LOCAL'
        WHEN cloud_source_path IS NOT NULL
             AND is_multi_file IS NOT NULL
        THEN 'SUBMITTING_COPY'
        ELSE 'ACCEPTED'
    END,
    pause_requested = 0,
    phase_started_at = sqlc.arg(now),
    next_run_at = sqlc.arg(now),
    lease_until = NULL,
    lease_owner = NULL,
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state = 'STOPPED';

-- name: RetryDownload :execrows
UPDATE downloads
SET
    state = sqlc.arg(state),
    last_error = NULL,
    attempt_count = 0,
    phase_started_at = sqlc.arg(now),
    next_run_at = sqlc.arg(now),
    lease_until = NULL,
    lease_owner = NULL,
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state = 'FAILED';

-- name: RetryCleanup :execrows
UPDATE downloads
SET
    last_error = NULL,
    attempt_count = 0,
    phase_started_at = sqlc.arg(now),
    next_run_at = sqlc.arg(now),
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state IN ('CANCEL_REQUESTED', 'DELETE_REQUESTED')
  AND last_error IS NOT NULL;

-- name: PauseDownload :execrows
UPDATE downloads
SET
    state = 'CANCEL_REQUESTED',
    pause_requested = 1,
    phase_started_at = sqlc.arg(now),
    next_run_at = sqlc.arg(now),
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state IN (
      'ACCEPTED',
      'SUBMITTING_OFFLINE',
      'WAITING_OFFLINE',
      'SUBMITTING_COPY',
      'WAITING_COPY',
      'VERIFYING_LOCAL'
  );

-- name: CancelDownload :execrows
UPDATE downloads
SET
    state = 'CANCEL_REQUESTED',
    pause_requested = 0,
    phase_started_at = sqlc.arg(now),
    next_run_at = sqlc.arg(now),
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state IN (
      'ACCEPTED',
      'STOPPED',
      'SUBMITTING_OFFLINE',
      'WAITING_OFFLINE',
      'SUBMITTING_COPY',
      'WAITING_COPY',
      'VERIFYING_LOCAL'
  );

-- name: RequestDelete :execrows
UPDATE downloads
SET
    state = 'DELETE_REQUESTED',
    pause_requested = 0,
    delete_files_requested = CASE
        WHEN delete_files_requested = 1 OR sqlc.arg(delete_files_requested) = 1 THEN 1
        ELSE 0
    END,
    phase_started_at = sqlc.arg(now),
    next_run_at = sqlc.arg(now),
    updated_at = sqlc.arg(now),
    removed_at = COALESCE(removed_at, sqlc.arg(now)),
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state != 'DELETED'
  AND (
      state != 'DELETE_REQUESTED'
      OR (sqlc.arg(delete_files_requested) = 1 AND delete_files_requested = 0)
  );


-- name: NextDue :one
SELECT effective_due.next_due
FROM (
    SELECT candidate.next_run_at AS next_due
    FROM downloads AS candidate
    WHERE candidate.state NOT IN ('STOPPED', 'COMPLETED', 'FAILED', 'CANCELLED', 'DELETED')
      AND candidate.next_run_at IS NOT NULL
      AND (candidate.lease_until IS NULL OR candidate.lease_until <= sqlc.arg(now) OR candidate.lease_until <= candidate.next_run_at)

    UNION ALL

    SELECT candidate.lease_until AS next_due
    FROM downloads AS candidate
    WHERE candidate.state NOT IN ('STOPPED', 'COMPLETED', 'FAILED', 'CANCELLED', 'DELETED')
      AND candidate.next_run_at IS NOT NULL
      AND candidate.lease_until > sqlc.arg(now)
      AND candidate.lease_until > candidate.next_run_at
) AS effective_due
ORDER BY effective_due.next_due ASC
LIMIT 1;