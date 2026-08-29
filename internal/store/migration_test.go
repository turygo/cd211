package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/outbox"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
	_ "modernc.org/sqlite"
)

func TestOpenMigratesLegacyDomainEventsSequence(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 5)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	payload := []byte(`{"id":"evt_legacy","type":"webhook.test"}`)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO domain_events (
			id, type, aggregate_type, aggregate_id, aggregate_version, payload, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "evt_legacy", outbox.EventTypeTest, outbox.AggregateWebhookEndpoint, "7", 3, payload, now); err != nil {
		t.Fatalf("insert legacy event fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO webhook_endpoints (
			id, name, url, hmac_secret, bearer_token, enabled, created_at, updated_at, row_version
		) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)
	`, 7, "legacy-endpoint", "https://example.invalid/webhook", "fixture-secret", "fixture-token", now, now, 3); err != nil {
		t.Fatalf("insert endpoint fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (
			id, event_id, endpoint_id, endpoint_name, event_type, aggregate_type,
			aggregate_id, status, attempt_count, next_attempt_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)
	`, 11, "evt_legacy", 7, "legacy-endpoint", outbox.EventTypeTest, outbox.AggregateWebhookEndpoint, "7", now, now, now); err != nil {
		t.Fatalf("insert delivery fixture: %v", err)
	}

	rebuildDomainEventsAsLegacy(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy fixture database: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open(legacy database) error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	assertDomainEventsSequenceSchema(t, store.db)

	event, err := store.queries.GetEvent(ctx, "evt_legacy")
	if err != nil {
		t.Fatalf("GetEvent() error = %v", err)
	}
	if event.Sequence != 1 || event.Type != outbox.EventTypeTest || event.AggregateType != outbox.AggregateWebhookEndpoint ||
		event.AggregateID != "7" || event.AggregateVersion != 3 || string(event.Payload) != string(payload) || !event.OccurredAt.Equal(now) {
		t.Errorf("migrated event = %+v, want preserved legacy event with sequence 1", event)
	}

	assertWebhookEventForeignKey(t, store.db)

	leaseDuration := 30 * time.Second
	claimedAt := now.Add(time.Minute)
	claim, err := store.ClaimWebhookDue(ctx, "migration-worker", claimedAt, leaseDuration)
	if err != nil {
		t.Fatalf("ClaimWebhookDue() error = %v", err)
	}
	if claim == nil {
		t.Fatal("ClaimWebhookDue() = nil, want legacy pending delivery")
	}
	if claim.DeliveryID != 11 || claim.EventID != "evt_legacy" || claim.EventType != outbox.EventTypeTest ||
		claim.Owner != "migration-worker" || claim.AttemptCount != 1 || claim.FirstAttemptAt == nil ||
		!claim.FirstAttemptAt.Equal(claimedAt) || string(claim.Payload) != string(payload) {
		t.Errorf("claim = %+v, want first claim with preserved event payload", claim)
	}
	delivery, err := store.GetWebhookDelivery(ctx, 11)
	if err != nil {
		t.Fatalf("GetWebhookDelivery() error = %v", err)
	}
	if delivery.Status != outbox.StatusDelivering || delivery.AttemptCount != 1 || delivery.LeaseOwner != "migration-worker" ||
		delivery.FirstAttemptAt == nil || !delivery.FirstAttemptAt.Equal(claimedAt) || delivery.LeaseUntil == nil ||
		!delivery.LeaseUntil.Equal(claimedAt.Add(leaseDuration)) {
		t.Errorf("claimed delivery = %+v, want persisted lease state", delivery)
	}
}

func TestLastErrorCodeMigrationBackfillsLegacy(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-errors.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 7)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	withError := testSubmission("a", now)
	withError.Download.LastError = "local deletion failed"
	clean := testSubmission("b", now)
	for _, submission := range []domain.Submission{withError, clean} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO downloads (
				hash, name, source_kind, submission_uri, category, cloud_folder, save_path,
				total_size, state, offline_progress, copy_progress, qbit_progress,
				last_error, phase_started_at, next_run_at, attempt_count, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, 0, ?, ?)
		`, submission.Download.Hash, submission.Download.Name, string(submission.Download.SourceKind),
			submission.Download.SubmissionURI, submission.Download.Category, submission.Download.CloudFolder,
			submission.Download.SavePath, submission.Download.TotalSize, string(submission.Download.State),
			nullableString(submission.Download.LastError), submission.Download.PhaseStartedAt,
			nullableTime(submission.Download.NextRunAt), submission.Download.CreatedAt, submission.Download.UpdatedAt,
		); err != nil {
			t.Fatalf("insert legacy error fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy error fixture database: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open(legacy error database) error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	legacy, err := store.GetDownload(ctx, withError.Download.Hash)
	if err != nil || legacy.LastErrorCode != string(domain.ProblemLegacy) || legacy.LastError != "local deletion failed" {
		t.Fatalf("legacy row = (%+v, %v), want legacy code with stored text", legacy, err)
	}
	cleanRow, err := store.GetDownload(ctx, clean.Download.Hash)
	if err != nil || cleanRow.LastErrorCode != "" {
		t.Fatalf("clean row = (%+v, %v), want no problem code", cleanRow, err)
	}
}
func TestCloudContentResolutionMigrationPreservesHistoricalCopyIdentity(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-cloud-content.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 12)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	hash := strings.Repeat("c", 40)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO downloads (
			hash, name, source_kind, submission_uri, category, cloud_folder, save_path,
			cloud_source_path, total_size, state, offline_progress, copy_progress,
			qbit_progress, phase_started_at, next_run_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?)
	`, hash, "historical", string(domain.SourceMagnet), "magnet:?xt=urn:btih:"+hash,
		"", "/cloud", "/downloads", "/cloud/historical", 1, string(domain.StateAccepted),
		now, now, now, now); err != nil {
		t.Fatalf("insert historical cloud path fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close historical fixture database: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open(historical cloud database) error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	download, err := store.GetDownload(ctx, hash)
	if err != nil {
		t.Fatalf("GetDownload(migrated historical row) error = %v", err)
	}
	if download.CloudResultPath != "/cloud/historical" || download.CopySourcePath != "/cloud/historical" {
		t.Fatalf("migrated historical paths = (%q, %q), want preserved copy identity", download.CloudResultPath, download.CopySourcePath)
	}
}

func TestDomainEventsSequenceMigrationPreservesCanonicalCursor(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "canonical.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 5)
	now := time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)

	for _, fixture := range []struct {
		sequence int64
		id       string
	}{
		{sequence: 17, id: "evt_keep"},
		{sequence: 29, id: "evt_deleted"},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO domain_events (
				sequence, id, type, aggregate_type, aggregate_id, aggregate_version, payload, occurred_at
			) VALUES (?, ?, ?, ?, ?, 0, ?, ?)
		`, fixture.sequence, fixture.id, outbox.EventTypeCompleted, "download", fixture.id, []byte(`{"fixture":true}`), now); err != nil {
			t.Fatalf("insert canonical event %q: %v", fixture.id, err)
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM domain_events WHERE id = ?", "evt_deleted"); err != nil {
		t.Fatalf("delete high-water event fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close canonical fixture database: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open(canonical database) error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	assertDomainEventsSequenceSchema(t, store.db)
	event, err := store.queries.GetEvent(ctx, "evt_keep")
	if err != nil {
		t.Fatalf("GetEvent(evt_keep) error = %v", err)
	}
	if event.Sequence != 17 {
		t.Errorf("evt_keep sequence = %d, want 17", event.Sequence)
	}
	var watermark int64
	if err := store.db.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name = 'domain_events'").Scan(&watermark); err != nil {
		t.Fatalf("read domain_events sqlite_sequence: %v", err)
	}
	if watermark != 29 {
		t.Errorf("domain_events sqlite_sequence = %d, want deleted high-water 29", watermark)
	}

	if err := store.queries.InsertEvent(ctx, storedb.InsertEventParams{
		ID:               "evt_after_migration",
		Type:             outbox.EventTypeCompleted,
		AggregateType:    "download",
		AggregateID:      "after",
		AggregateVersion: 0,
		Payload:          []byte(`{"after":true}`),
		OccurredAt:       now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("InsertEvent(after migration) error = %v", err)
	}
	inserted, err := store.queries.GetEvent(ctx, "evt_after_migration")
	if err != nil {
		t.Fatalf("GetEvent(after migration) error = %v", err)
	}
	if inserted.Sequence != 30 {
		t.Errorf("post-migration sequence = %d, want 30", inserted.Sequence)
	}
}

func TestWorkspaceMigrationRejectsRetainedLegacyCD211Destination(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-workspace.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 14)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, err := db.ExecContext(ctx, `
		INSERT INTO downloads (
			hash, name, source_kind, submission_uri, category, cloud_folder,
			save_path, destination_name, content_path, state, phase_started_at, created_at, updated_at
		) VALUES (?, ?, 'magnet', ?, '', '/cloud', '/downloads', '.cd211',
		          '/downloads/.cd211', 'DELETED', ?, ?, ?)
	`, strings.Repeat("a", 40), "legacy", "magnet:?xt=urn:btih:"+strings.Repeat("a", 40), now, now, now)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy .cd211 fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy workspace fixture: %v", err)
	}

	if migrated, err := Open(ctx, databasePath); err == nil {
		_ = migrated.Close()
		t.Fatal("Open() accepted a retained legacy .cd211 destination")
	}

	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("reopen failed migration fixture: %v", err)
	}
	defer raw.Close()
	var columns int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('downloads') WHERE name = 'workspace_path'`).Scan(&columns); err != nil {
		t.Fatalf("inspect rolled-back workspace column: %v", err)
	}
	if columns != 0 {
		t.Fatalf("workspace migration partially applied: workspace_path columns = %d", columns)
	}
}
func TestWorkspaceMigrationRejectsReservedLogicalSavePaths(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		savePath string
		category bool
	}{
		{name: "download save root", savePath: "/downloads/.cd211"},
		{name: "download quarantine", savePath: "/downloads/.cd211/.quarantine"},
		{name: "category save root", savePath: "/downloads/.cd211/category", category: true},
		{name: "category quarantine", savePath: "/downloads/.cd211/.quarantine", category: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "legacy-workspace.sqlite")
			db := openDatabaseAtMigration(t, databasePath, 14)
			now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
			if test.category {
				_, err := db.ExecContext(ctx, `
					INSERT INTO categories (name, cloud_path, save_path, enabled, created_at, updated_at)
					VALUES ('reserved', '/cloud', ?, 1, ?, ?)
				`, test.savePath, now, now)
				if err != nil {
					_ = db.Close()
					t.Fatalf("insert reserved category fixture: %v", err)
				}
			} else {
				insertLegacyWorkspaceDownload(t, db, strings.Repeat("a", 40), test.savePath, "ACCEPTED", nil)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close legacy workspace fixture: %v", err)
			}

			if migrated, err := Open(ctx, databasePath); err == nil {
				_ = migrated.Close()
				t.Fatal("Open() accepted a reserved logical save path")
			}
			assertWorkspaceMigrationRolledBack(t, databasePath)
		})
	}
}

func TestWorkspaceMigrationAllowsUnretainedDeletedReservedSavePath(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-workspace.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 14)
	hash := strings.Repeat("b", 40)
	insertLegacyWorkspaceDownload(t, db, hash, "/downloads/.cd211", "DELETED", nil)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy workspace fixture: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open() rejected an unretained deleted reserved save path: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	download, err := store.GetDownload(ctx, hash)
	if err != nil {
		t.Fatalf("GetDownload() after migration: %v", err)
	}
	if download.WorkspacePath != "" {
		t.Fatalf("deleted reserved save path was backfilled to %q", download.WorkspacePath)
	}
}
func TestWorkspaceMigrationRejectsRetainedDeletedReservedSavePath(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-workspace.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 14)
	contentPath := "/downloads/.cd211/content"
	insertLegacyWorkspaceDownload(t, db, strings.Repeat("d", 40), "/downloads/.cd211", "DELETED", &contentPath)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy workspace fixture: %v", err)
	}

	if migrated, err := Open(ctx, databasePath); err == nil {
		_ = migrated.Close()
		t.Fatal("Open() accepted a retained deleted reserved save path")
	}
	assertWorkspaceMigrationRolledBack(t, databasePath)
}

func TestWorkspaceMigrationAllowsNearReservedSavePath(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-workspace.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 14)
	hash := strings.Repeat("c", 40)
	insertLegacyWorkspaceDownload(t, db, hash, "/downloads/.cd211-backup", "ACCEPTED", nil)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy workspace fixture: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open() rejected a near-reserved save path: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	download, err := store.GetDownload(ctx, hash)
	if err != nil {
		t.Fatalf("GetDownload() after migration: %v", err)
	}
	wantWorkspace := "/downloads/.cd211-backup/.cd211/" + hash
	if download.WorkspacePath != wantWorkspace {
		t.Fatalf("workspace path = %q, want %q", download.WorkspacePath, wantWorkspace)
	}
}

func insertLegacyWorkspaceDownload(t *testing.T, db *sql.DB, hash, savePath, state string, contentPath *string) {
	t.Helper()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var content sql.NullString
	if contentPath != nil {
		content = sql.NullString{String: *contentPath, Valid: true}
	}
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO downloads (
			hash, name, source_kind, submission_uri, category, cloud_folder,
			save_path, destination_name, content_path, state, phase_started_at, created_at, updated_at
		) VALUES (?, ?, 'magnet', ?, '', '/cloud', ?, NULL, ?, ?, ?, ?, ?)
	`, hash, "legacy", "magnet:?xt=urn:btih:"+hash, savePath, content, state, now, now, now)
	if err != nil {
		t.Fatalf("insert legacy workspace fixture: %v", err)
	}
}

func assertWorkspaceMigrationRolledBack(t *testing.T, databasePath string) {
	t.Helper()
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("reopen failed migration fixture: %v", err)
	}
	defer raw.Close()
	var columns int
	if err := raw.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pragma_table_info('downloads') WHERE name = 'workspace_path'`).Scan(&columns); err != nil {
		t.Fatalf("inspect rolled-back workspace column: %v", err)
	}
	if columns != 0 {
		t.Fatalf("workspace migration partially applied: workspace_path columns = %d", columns)
	}
}

func openDatabaseAtMigration(t *testing.T, databasePath string, version int64) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := configureDatabase(ctx, db); err != nil {
		_ = db.Close()
		t.Fatalf("configure fixture database: %v", err)
	}
	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		_ = db.Close()
		t.Fatalf("load fixture migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create fixture migration provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, version); err != nil {
		_ = db.Close()
		t.Fatalf("migrate fixture database to %d: %v", version, err)
	}
	return db
}

func rebuildDomainEventsAsLegacy(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable foreign keys for legacy fixture: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin legacy schema fixture: %v", err)
	}
	statements := []string{
		`CREATE TABLE domain_events_legacy (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			aggregate_type TEXT NOT NULL,
			aggregate_id TEXT NOT NULL,
			aggregate_version INTEGER NOT NULL CHECK (aggregate_version >= 0),
			payload BLOB NOT NULL,
			occurred_at TIMESTAMP NOT NULL
		)`,
		`INSERT INTO domain_events_legacy (
			id, type, aggregate_type, aggregate_id, aggregate_version, payload, occurred_at
		) SELECT id, type, aggregate_type, aggregate_id, aggregate_version, payload, occurred_at
		FROM domain_events ORDER BY sequence`,
		"DROP TABLE domain_events",
		"ALTER TABLE domain_events_legacy RENAME TO domain_events",
		"CREATE INDEX idx_domain_events_aggregate ON domain_events (aggregate_type, aggregate_id, occurred_at, id)",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			t.Fatalf("build legacy domain_events schema: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy schema fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys after legacy fixture: %v", err)
	}
}

func assertDomainEventsSequenceSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var columns int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('domain_events')
		WHERE name = 'sequence' AND type = 'INTEGER' AND pk = 1
	`).Scan(&columns); err != nil {
		t.Fatalf("inspect domain_events sequence column: %v", err)
	}
	if columns != 1 {
		t.Errorf("domain_events sequence primary-key columns = %d, want 1", columns)
	}
	for _, index := range []string{
		"idx_domain_events_feed_type_sequence",
		"idx_domain_events_feed_aggregate_type_sequence",
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&count); err != nil {
			t.Fatalf("inspect index %q: %v", index, err)
		}
		if count != 1 {
			t.Errorf("index %q count = %d, want 1", index, count)
		}
	}
}

func assertWebhookEventForeignKey(t *testing.T, db *sql.DB) {
	t.Helper()
	var table, from, to string
	if err := db.QueryRow(`
		SELECT "table", "from", "to"
		FROM pragma_foreign_key_list('webhook_deliveries')
		WHERE "from" = 'event_id'
	`).Scan(&table, &from, &to); err != nil {
		t.Fatalf("inspect webhook event foreign key: %v", err)
	}
	if table != "domain_events" || from != "event_id" || to != "id" {
		t.Errorf("webhook event foreign key = (%q, %q, %q), want (domain_events, event_id, id)", table, from, to)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("PRAGMA foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var child string
		var rowID int64
		var parent string
		var constraint int64
		if err := rows.Scan(&child, &rowID, &parent, &constraint); err != nil {
			t.Fatalf("scan foreign key violation: %v", err)
		}
		t.Errorf("foreign key violation: child=%s rowid=%d parent=%s constraint=%d", child, rowID, parent, constraint)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign key check: %v", err)
	}
}

func TestSessionsMigrationCreatesSchemaAndEnforcesChecks(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "sessions.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 8)
	var preExisting int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions'").Scan(&preExisting); err != nil {
		_ = db.Close()
		t.Fatalf("inspect pre-migration schema: %v", err)
	}
	if preExisting != 0 {
		_ = db.Close()
		t.Fatalf("sessions table exists before migration 9")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-sessions fixture database: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open(sessions migration) error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	columns := map[string]struct {
		kind    string
		notNull bool
		pk      bool
	}{
		"sid_digest": {"BLOB", true, true},
		"csrf_token": {"TEXT", true, false},
		"created_at": {"DATETIME", true, false},
		"expires_at": {"DATETIME", true, false},
	}
	rows, err := store.db.QueryContext(ctx, "PRAGMA table_info(sessions)")
	if err != nil {
		t.Fatalf("read sessions table info: %v", err)
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var (
			cid        int
			name, kind string
			notNull    int
			dflt       sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &kind, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan sessions column: %v", err)
		}
		want, exists := columns[name]
		if !exists {
			continue
		}
		found++
		if !strings.EqualFold(kind, want.kind) {
			t.Errorf("sessions.%s kind = %q, want %q", name, kind, want.kind)
		}
		if (notNull == 1) != want.notNull {
			t.Errorf("sessions.%s notNull = %t, want %t", name, notNull == 1, want.notNull)
		}
		if (pk == 1) != want.pk {
			t.Errorf("sessions.%s pk = %t, want %t", name, pk == 1, want.pk)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sessions columns: %v", err)
	}
	if found != len(columns) {
		t.Fatalf("sessions columns found = %d, want %d", found, len(columns))
	}

	var indexName string
	if err := store.db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'sessions' AND name = 'idx_sessions_expires_at'",
	).Scan(&indexName); err != nil {
		t.Fatalf("expires_at index missing: %v", err)
	}

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	reject := func(name string, digest []byte, csrf string, created, expires time.Time) {
		t.Helper()
		statement := "INSERT INTO sessions (sid_digest, csrf_token, created_at, expires_at) VALUES (?, ?, ?, ?)"
		if _, err := store.db.ExecContext(ctx, statement, digest, csrf, created, expires); err == nil {
			t.Errorf("%s: insert unexpectedly succeeded", name)
		}
	}
	reject("empty csrf token", make([]byte, 32), "", now, now.Add(time.Hour))
	reject("short digest", make([]byte, 16), "csrf", now, now.Add(time.Hour))
	reject("expiry at creation", make([]byte, 32), "csrf", now, now)
	reject("expiry before creation", make([]byte, 32), "csrf", now, now.Add(-time.Hour))

	var validDigest [32]byte
	validDigest[0] = 0x7f
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO sessions (sid_digest, csrf_token, created_at, expires_at) VALUES (?, 'csrf', ?, ?)",
		validDigest[:], now, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert valid session row: %v", err)
	}
}
