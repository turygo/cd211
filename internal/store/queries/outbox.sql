-- name: InsertEvent :exec
INSERT INTO domain_events (
    id,
    type,
    aggregate_type,
    aggregate_id,
    aggregate_version,
    payload,
    occurred_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(type),
    sqlc.arg(aggregate_type),
    sqlc.arg(aggregate_id),
    sqlc.arg(aggregate_version),
    sqlc.arg(payload),
    sqlc.arg(occurred_at)
);

-- name: GetEvent :one
SELECT *
FROM domain_events
WHERE id = sqlc.arg(id);

-- name: ListEventsByAggregate :many
SELECT *
FROM domain_events
WHERE aggregate_type = sqlc.arg(aggregate_type)
  AND aggregate_id = sqlc.arg(aggregate_id)
ORDER BY occurred_at ASC, id ASC;

-- name: InsertEndpoint :one
INSERT INTO webhook_endpoints (
    name,
    url,
    hmac_secret,
    bearer_token,
    enabled,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(name),
    sqlc.arg(url),
    sqlc.arg(hmac_secret),
    sqlc.narg(bearer_token),
    sqlc.arg(enabled),
    sqlc.arg(created_at),
    sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetEndpoint :one
SELECT *
FROM webhook_endpoints
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: GetEndpointRaw :one
SELECT *
FROM webhook_endpoints
WHERE id = sqlc.arg(id);

-- name: ListEndpoints :many
SELECT *
FROM webhook_endpoints
WHERE deleted_at IS NULL
ORDER BY name COLLATE NOCASE ASC, id ASC;

-- name: UpdateEndpoint :one
UPDATE webhook_endpoints
SET
    name = sqlc.arg(name),
    url = sqlc.arg(url),
    bearer_token = sqlc.narg(bearer_token),
    enabled = sqlc.arg(enabled),
    updated_at = sqlc.arg(updated_at),
    row_version = row_version + 1
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL
RETURNING *;

-- name: SetEndpointEnabled :execrows
UPDATE webhook_endpoints
SET
    enabled = sqlc.arg(enabled),
    updated_at = sqlc.arg(updated_at),
    row_version = row_version + 1
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: RotateEndpointSecret :one
UPDATE webhook_endpoints
SET
    hmac_secret = sqlc.arg(hmac_secret),
    updated_at = sqlc.arg(updated_at),
    row_version = row_version + 1
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL
RETURNING *;

-- name: DeleteEndpoint :execrows
UPDATE webhook_endpoints
SET
    enabled = 0,
    deleted_at = sqlc.arg(deleted_at),
    updated_at = sqlc.arg(updated_at),
    row_version = row_version + 1
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: EndpointExists :one
SELECT EXISTS(SELECT 1 FROM webhook_endpoints WHERE id = sqlc.arg(id));

-- name: GetEndpointDeletedAt :one
SELECT deleted_at
FROM webhook_endpoints
WHERE id = sqlc.arg(id);

-- name: InsertSubscription :exec
INSERT INTO webhook_subscriptions (
    endpoint_id,
    event_type
) VALUES (
    sqlc.arg(endpoint_id),
    sqlc.arg(event_type)
);

-- name: DeleteSubscription :exec
DELETE FROM webhook_subscriptions
WHERE endpoint_id = sqlc.arg(endpoint_id)
  AND event_type = sqlc.arg(event_type);

-- name: GetEndpointSubscriptions :many
SELECT event_type
FROM webhook_subscriptions
WHERE endpoint_id = sqlc.arg(endpoint_id)
ORDER BY event_type ASC;

-- name: ListEndpointSubscriptions :many
SELECT endpoint_id, event_type
FROM webhook_subscriptions
ORDER BY endpoint_id ASC, event_type ASC;

-- name: ListSubscribedEndpoints :many
SELECT endpoint.id, endpoint.name
FROM webhook_endpoints AS endpoint
JOIN webhook_subscriptions AS subscription ON subscription.endpoint_id = endpoint.id
WHERE subscription.event_type = sqlc.arg(event_type)
  AND endpoint.enabled = 1
  AND endpoint.deleted_at IS NULL
ORDER BY endpoint.id ASC;

-- name: InsertDelivery :one
INSERT INTO webhook_deliveries (
    event_id,
    endpoint_id,
    endpoint_name,
    event_type,
    aggregate_type,
    aggregate_id,
    status,
    next_attempt_at,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(event_id),
    sqlc.arg(endpoint_id),
    sqlc.arg(endpoint_name),
    sqlc.arg(event_type),
    sqlc.arg(aggregate_type),
    sqlc.arg(aggregate_id),
    'pending',
    sqlc.arg(next_attempt_at),
    sqlc.arg(created_at),
    sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetDelivery :one
SELECT *
FROM webhook_deliveries
WHERE id = sqlc.arg(id);

-- name: ListDeliveries :many
SELECT *
FROM webhook_deliveries
WHERE endpoint_id = COALESCE(sqlc.narg(endpoint_id), endpoint_id)
  AND event_type = COALESCE(sqlc.narg(event_type), event_type)
  AND status = COALESCE(sqlc.narg(status), status)
  AND (
      created_at < sqlc.narg(cursor_time)
      OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.arg(cursor_id))
      OR sqlc.arg(cursor_id) < 0
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit);

-- name: ClaimWebhookDue :one
UPDATE webhook_deliveries
SET
    status = 'delivering',
    attempt_count = attempt_count + 1,
    first_attempt_at = COALESCE(first_attempt_at, sqlc.arg(now)),
    lease_owner = sqlc.arg(owner),
    lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE id = (
    SELECT candidate.id
    FROM webhook_deliveries AS candidate
    JOIN webhook_endpoints AS endpoint ON endpoint.id = candidate.endpoint_id
    WHERE candidate.status IN ('pending', 'delivering')
      AND candidate.next_attempt_at <= sqlc.arg(now)
      AND (candidate.lease_until IS NULL OR candidate.lease_until <= sqlc.arg(now))
      AND endpoint.enabled = 1
      AND endpoint.deleted_at IS NULL
      AND NOT EXISTS (
          SELECT 1
          FROM webhook_deliveries AS earlier
          WHERE earlier.endpoint_id = candidate.endpoint_id
            AND earlier.aggregate_type = candidate.aggregate_type
            AND earlier.aggregate_id = candidate.aggregate_id
            AND earlier.status IN ('pending', 'delivering')
            AND (
                earlier.created_at < candidate.created_at
                OR (earlier.created_at = candidate.created_at AND earlier.id < candidate.id)
            )
      )
    ORDER BY candidate.next_attempt_at ASC, candidate.created_at ASC, candidate.id ASC
    LIMIT 1
)
RETURNING *;

-- name: CommitWebhookClaim :execrows
UPDATE webhook_deliveries
SET
    status = sqlc.arg(status),
    last_http_status = sqlc.narg(last_http_status),
    last_error = sqlc.narg(last_error),
    next_attempt_at = sqlc.narg(next_attempt_at),
    delivered_at = sqlc.narg(delivered_at),
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE id = sqlc.arg(id)
  AND status = 'delivering'
  AND lease_owner = sqlc.arg(lease_owner)
  AND row_version = sqlc.arg(expected_row_version);

-- name: NextWebhookDue :one
SELECT effective_due.next_due
FROM (
    SELECT candidate.next_attempt_at AS next_due
    FROM webhook_deliveries AS candidate
    JOIN webhook_endpoints AS endpoint ON endpoint.id = candidate.endpoint_id
    WHERE candidate.status IN ('pending', 'delivering')
      AND candidate.next_attempt_at IS NOT NULL
      AND endpoint.enabled = 1
      AND endpoint.deleted_at IS NULL
      AND (
          candidate.lease_until IS NULL
          OR candidate.lease_until <= sqlc.arg(now)
          OR candidate.lease_until <= candidate.next_attempt_at
      )
      AND NOT EXISTS (
          SELECT 1
          FROM webhook_deliveries AS earlier
          WHERE earlier.endpoint_id = candidate.endpoint_id
            AND earlier.aggregate_type = candidate.aggregate_type
            AND earlier.aggregate_id = candidate.aggregate_id
            AND earlier.status IN ('pending', 'delivering')
            AND (
                earlier.created_at < candidate.created_at
                OR (earlier.created_at = candidate.created_at AND earlier.id < candidate.id)
            )
      )

    UNION ALL

    SELECT candidate.lease_until AS next_due
    FROM webhook_deliveries AS candidate
    JOIN webhook_endpoints AS endpoint ON endpoint.id = candidate.endpoint_id
    WHERE candidate.status IN ('pending', 'delivering')
      AND candidate.next_attempt_at IS NOT NULL
      AND endpoint.enabled = 1
      AND endpoint.deleted_at IS NULL
      AND candidate.lease_until > sqlc.arg(now)
      AND candidate.lease_until > candidate.next_attempt_at
      AND NOT EXISTS (
          SELECT 1
          FROM webhook_deliveries AS earlier
          WHERE earlier.endpoint_id = candidate.endpoint_id
            AND earlier.aggregate_type = candidate.aggregate_type
            AND earlier.aggregate_id = candidate.aggregate_id
            AND earlier.status IN ('pending', 'delivering')
            AND (
                earlier.created_at < candidate.created_at
                OR (earlier.created_at = candidate.created_at AND earlier.id < candidate.id)
            )
      )
) AS effective_due
ORDER BY effective_due.next_due ASC
LIMIT 1;

-- name: ReplayDelivery :one
UPDATE webhook_deliveries
SET
    status = 'pending',
    attempt_count = 0,
    first_attempt_at = NULL,
    next_attempt_at = sqlc.arg(now),
    lease_owner = NULL,
    lease_until = NULL,
    last_http_status = NULL,
    last_error = NULL,
    delivered_at = NULL,
    updated_at = sqlc.arg(now),
    row_version = row_version + 1
WHERE webhook_deliveries.id = sqlc.arg(id)
  AND status = 'dead'
  AND endpoint_id IN (
      SELECT endpoint.id
      FROM webhook_endpoints AS endpoint
      WHERE endpoint.enabled = 1 AND endpoint.deleted_at IS NULL
  )
RETURNING *;

-- name: CancelEndpointDeliveries :exec
UPDATE webhook_deliveries
SET
    status = 'cancelled',
    next_attempt_at = NULL,
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = sqlc.arg(updated_at),
    row_version = row_version + 1
WHERE endpoint_id = sqlc.arg(endpoint_id)
  AND status IN ('pending', 'dead');

-- name: CancelSubscriptionDeliveries :exec
UPDATE webhook_deliveries
SET
    status = 'cancelled',
    next_attempt_at = NULL,
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = sqlc.arg(updated_at),
    row_version = row_version + 1
WHERE endpoint_id = sqlc.arg(endpoint_id)
  AND event_type = sqlc.arg(event_type)
  AND status IN ('pending', 'dead');

-- name: PruneDeliveries :execrows
DELETE FROM webhook_deliveries
WHERE status IN ('succeeded', 'cancelled')
  AND updated_at < sqlc.arg(cutoff);
