-- name: GetSession :one
SELECT *
FROM sessions
WHERE sid_digest = sqlc.arg(sid_digest)
  AND audience = sqlc.arg(audience);

-- name: InsertSession :exec
INSERT INTO sessions (sid_digest, audience, csrf_token, created_at, expires_at)
VALUES (sqlc.arg(sid_digest), sqlc.arg(audience), sqlc.arg(csrf_token), sqlc.arg(created_at), sqlc.arg(expires_at));

-- name: PurgeExpiredSessions :execrows
DELETE FROM sessions
WHERE expires_at <= sqlc.arg(now);

-- name: EvictOldestSession :exec
DELETE FROM sessions
WHERE sid_digest = (
    SELECT sid_digest
    FROM sessions
    ORDER BY expires_at ASC, created_at ASC, sid_digest ASC
    LIMIT 1
);

-- name: RefreshSession :execrows
UPDATE sessions
SET expires_at = sqlc.arg(new_expires_at)
WHERE sid_digest = sqlc.arg(sid_digest)
  AND audience = sqlc.arg(audience)
  AND expires_at = sqlc.arg(expected_expires_at);

-- name: RevokeSession :exec
DELETE FROM sessions
WHERE sid_digest = sqlc.arg(sid_digest)
  AND audience = sqlc.arg(audience);

-- name: CountSessions :one
SELECT COUNT(*)
FROM sessions;
