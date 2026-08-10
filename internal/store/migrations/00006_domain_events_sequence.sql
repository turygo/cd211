-- +goose Up
-- Rebuild the table for both legacy and canonical schemas. In the legacy
-- seven-column schema, rowid is the only durable insertion-order key and
-- becomes the initial feed sequence. In the canonical schema, sequence is an
-- INTEGER PRIMARY KEY alias for rowid, so this preserves every existing cursor.
-- Preserve AUTOINCREMENT watermarks separately so deleted IDs are not reused.
--
-- webhook_deliveries must be rebuilt in the same transaction: SQLite checks
-- the child foreign key while dropping domain_events even when the replacement
-- parent has the same name by commit time.
CREATE TEMP TABLE outbox_migration_watermarks (
    domain_event_sequence INTEGER NOT NULL,
    webhook_delivery_id INTEGER NOT NULL
);

INSERT INTO outbox_migration_watermarks (
    domain_event_sequence,
    webhook_delivery_id
)
SELECT
    (
        SELECT MAX(sequence)
        FROM (
            SELECT COALESCE(MAX(rowid), 0) AS sequence FROM domain_events
            UNION ALL
            SELECT COALESCE(MAX(seq), 0) AS sequence FROM sqlite_sequence WHERE name = 'domain_events'
        )
    ),
    (
        SELECT MAX(id)
        FROM (
            SELECT COALESCE(MAX(id), 0) AS id FROM webhook_deliveries
            UNION ALL
            SELECT COALESCE(MAX(seq), 0) AS id FROM sqlite_sequence WHERE name = 'webhook_deliveries'
        )
    );

CREATE TEMP TABLE webhook_deliveries_backup AS
SELECT * FROM webhook_deliveries;

DROP TABLE webhook_deliveries;

CREATE TABLE domain_events_rebuilt (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    aggregate_version INTEGER NOT NULL CHECK (aggregate_version >= 0),
    payload BLOB NOT NULL,
    occurred_at TIMESTAMP NOT NULL
);

INSERT INTO domain_events_rebuilt (
    sequence,
    id,
    type,
    aggregate_type,
    aggregate_id,
    aggregate_version,
    payload,
    occurred_at
)
SELECT
    rowid,
    id,
    type,
    aggregate_type,
    aggregate_id,
    aggregate_version,
    payload,
    occurred_at
FROM domain_events
ORDER BY rowid;

DROP TABLE domain_events;
ALTER TABLE domain_events_rebuilt RENAME TO domain_events;

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

INSERT INTO webhook_deliveries (
    id,
    event_id,
    endpoint_id,
    endpoint_name,
    event_type,
    aggregate_type,
    aggregate_id,
    status,
    attempt_count,
    first_attempt_at,
    next_attempt_at,
    lease_owner,
    lease_until,
    last_http_status,
    last_error,
    delivered_at,
    created_at,
    updated_at,
    row_version
)
SELECT
    id,
    event_id,
    endpoint_id,
    endpoint_name,
    event_type,
    aggregate_type,
    aggregate_id,
    status,
    attempt_count,
    first_attempt_at,
    next_attempt_at,
    lease_owner,
    lease_until,
    last_http_status,
    last_error,
    delivered_at,
    created_at,
    updated_at,
    row_version
FROM webhook_deliveries_backup
ORDER BY id;

DELETE FROM sqlite_sequence
WHERE name IN ('domain_events', 'webhook_deliveries');
INSERT INTO sqlite_sequence (name, seq)
SELECT 'domain_events', domain_event_sequence
FROM outbox_migration_watermarks
WHERE domain_event_sequence > 0
UNION ALL
SELECT 'webhook_deliveries', webhook_delivery_id
FROM outbox_migration_watermarks
WHERE webhook_delivery_id > 0;

DROP TABLE webhook_deliveries_backup;
DROP TABLE outbox_migration_watermarks;

CREATE INDEX idx_domain_events_feed_type_sequence
    ON domain_events (type, sequence)
    WHERE aggregate_type = 'download'
      AND type IN ('download.completed', 'download.failed');
CREATE INDEX idx_domain_events_feed_aggregate_type_sequence
    ON domain_events (aggregate_id, type, sequence)
    WHERE aggregate_type = 'download'
      AND type IN ('download.completed', 'download.failed');

CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at, lease_until, created_at, id);
CREATE INDEX idx_webhook_deliveries_order ON webhook_deliveries (endpoint_id, aggregate_type, aggregate_id, created_at, id);
CREATE INDEX idx_webhook_deliveries_history ON webhook_deliveries (created_at, id);
CREATE INDEX idx_webhook_deliveries_endpoint ON webhook_deliveries (endpoint_id, created_at, id);
CREATE INDEX idx_webhook_deliveries_prune ON webhook_deliveries (status, updated_at);

-- +goose Down
-- This repair is intentionally forward-only: removing sequence would make the
-- current store queries incompatible with the database.
SELECT 1;
