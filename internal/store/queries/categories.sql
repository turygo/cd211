-- name: UpsertCategory :one
INSERT INTO categories (
    name,
    cloud_path,
    save_path,
    enabled,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(name),
    sqlc.arg(cloud_path),
    sqlc.arg(save_path),
    sqlc.arg(enabled),
    sqlc.arg(created_at),
    sqlc.arg(updated_at)
)
ON CONFLICT (name) DO UPDATE SET
    cloud_path = excluded.cloud_path,
    save_path = excluded.save_path,
    enabled = excluded.enabled,
    updated_at = excluded.updated_at
RETURNING *;

-- name: GetCategory :one
SELECT *
FROM categories
WHERE name = sqlc.arg(name);

-- name: ListCategories :many
SELECT *
FROM categories
ORDER BY name ASC;
