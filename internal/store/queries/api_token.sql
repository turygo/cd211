-- name: GetAPIToken :one
SELECT *
FROM api_token
WHERE id = 1;

-- name: InsertAPIToken :exec
INSERT INTO api_token (
    id,
    token_hash,
    token_hint,
    created_at,
    updated_at,
    row_version
) VALUES (
    1,
    sqlc.arg(token_hash),
    sqlc.arg(token_hint),
    sqlc.arg(created_at),
    sqlc.arg(updated_at),
    0
);


-- name: DeleteAPIToken :execrows
DELETE FROM api_token
WHERE id = 1
  AND row_version = sqlc.arg(expected_row_version);
