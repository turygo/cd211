-- name: GetOperatorPassword :one
SELECT password_hash FROM operator_password WHERE id = 1;

-- name: UpsertOperatorPassword :exec
INSERT INTO operator_password (id, password_hash, updated_at)
VALUES (1, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    password_hash = excluded.password_hash,
    updated_at = excluded.updated_at;
