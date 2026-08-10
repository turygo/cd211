-- +goose Up
CREATE TABLE domain_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    aggregate_version INTEGER NOT NULL CHECK (aggregate_version >= 0),
    payload BLOB NOT NULL,
    occurred_at TIMESTAMP NOT NULL
);

CREATE INDEX idx_domain_events_feed_type_sequence
    ON domain_events (type, sequence)
    WHERE aggregate_type = 'download'
      AND type IN ('download.completed', 'download.failed');
CREATE INDEX idx_domain_events_feed_aggregate_type_sequence
    ON domain_events (aggregate_id, type, sequence)
    WHERE aggregate_type = 'download'
      AND type IN ('download.completed', 'download.failed');

CREATE TABLE webhook_endpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    url TEXT NOT NULL,
    hmac_secret TEXT NOT NULL,
    bearer_token TEXT,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP,
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0)
);

CREATE TABLE webhook_subscriptions (
    endpoint_id INTEGER NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('download.completed', 'download.failed')),
    PRIMARY KEY (endpoint_id, event_type),
    FOREIGN KEY (endpoint_id) REFERENCES webhook_endpoints(id) ON DELETE CASCADE
);

CREATE TABLE webhook_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL,
    endpoint_id INTEGER NOT NULL,
    endpoint_name TEXT NOT NULL,
    event_type TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'delivering', 'succeeded', 'dead', 'cancelled')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    first_attempt_at TIMESTAMP,
    next_attempt_at TIMESTAMP,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    last_http_status INTEGER CHECK (last_http_status IS NULL OR last_http_status >= 0),
    last_error TEXT,
    delivered_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0),
    UNIQUE (event_id, endpoint_id),
    FOREIGN KEY (event_id) REFERENCES domain_events(id),
    FOREIGN KEY (endpoint_id) REFERENCES webhook_endpoints(id)
);

CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at, lease_until, created_at, id);
CREATE INDEX idx_webhook_deliveries_order ON webhook_deliveries (endpoint_id, aggregate_type, aggregate_id, created_at, id);
CREATE INDEX idx_webhook_deliveries_history ON webhook_deliveries (created_at, id);
CREATE INDEX idx_webhook_deliveries_endpoint ON webhook_deliveries (endpoint_id, created_at, id);
CREATE INDEX idx_webhook_deliveries_prune ON webhook_deliveries (status, updated_at);

-- +goose Down
DROP TABLE webhook_deliveries;
DROP TABLE webhook_subscriptions;
DROP TABLE webhook_endpoints;
DROP TABLE domain_events;
