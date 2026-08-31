-- name: GetQBTAPIKey :one
SELECT *
FROM qbt_api_key
WHERE id = 1
  AND active = 1;

-- name: InsertQBTAPIKey :exec
INSERT INTO qbt_api_key (
    id,
    key_hash,
    key_hint,
    created_at,
    updated_at,
    active,
    row_version
) VALUES (
    1,
    sqlc.arg(key_hash),
    sqlc.arg(key_hint),
    sqlc.arg(created_at),
    sqlc.arg(updated_at),
    1,
    0
);

-- name: ActivateQBTAPIKey :execrows
UPDATE qbt_api_key
SET
    key_hash = sqlc.arg(key_hash),
    key_hint = sqlc.arg(key_hint),
    created_at = sqlc.arg(created_at),
    updated_at = sqlc.arg(updated_at),
    active = 1,
    row_version = row_version + 1
WHERE id = 1
  AND active = 0;


-- name: RevokeQBTAPIKey :execrows
UPDATE qbt_api_key
SET
    active = 0,
    row_version = row_version + 1
WHERE id = 1
  AND active = 1
  AND row_version = sqlc.arg(expected_row_version);
