-- name: ClaimDue :one
UPDATE downloads
SET
    lease_owner = sqlc.arg(owner),
    lease_until = sqlc.arg(lease_until),
    row_version = row_version + 1
WHERE hash = (
    SELECT candidate.hash
    FROM downloads AS candidate
    WHERE candidate.next_run_at <= sqlc.arg(now)
      AND candidate.state NOT IN ('COMPLETED', 'FAILED', 'CANCELLED', 'DELETED')
      AND (candidate.lease_until IS NULL OR candidate.lease_until <= sqlc.arg(now))
    ORDER BY candidate.next_run_at ASC, candidate.created_at ASC, candidate.hash ASC
    LIMIT 1
)
RETURNING *;

-- name: CommitClaim :execrows
UPDATE downloads
SET
    name = sqlc.arg(name),
    destination_name = sqlc.narg(destination_name),
    cloud_task_name = sqlc.arg(cloud_task_name),
    cloud_source_path = sqlc.arg(cloud_source_path),
    content_path = sqlc.arg(content_path),
    is_multi_file = sqlc.arg(is_multi_file),
    total_size = sqlc.arg(total_size),
    state = sqlc.arg(state),
    offline_progress = sqlc.arg(offline_progress),
    copy_progress = sqlc.arg(copy_progress),
    qbit_progress = sqlc.arg(qbit_progress),
    last_upstream_status = sqlc.arg(last_upstream_status),
    last_error = sqlc.arg(last_error),
    phase_started_at = sqlc.arg(phase_started_at),
    next_run_at = sqlc.arg(next_run_at),
    attempt_count = sqlc.arg(attempt_count),
    delete_files_requested = sqlc.arg(delete_files_requested),
    updated_at = sqlc.arg(updated_at),
    completed_at = sqlc.arg(completed_at),
    removed_at = sqlc.arg(removed_at),
    lease_until = NULL,
    lease_owner = NULL,
    row_version = row_version + 1
WHERE hash = sqlc.arg(hash)
  AND state = sqlc.arg(expected_state)
  AND lease_owner = sqlc.arg(lease_owner)
  AND row_version = sqlc.arg(expected_row_version);
