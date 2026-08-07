package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	storedb "github.com/turygo/cd211/internal/store/sqlc"
)

func TestOpenConfiguresAndMigratesDatabase(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "private", "state", "cd211.sqlite")

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	parentInfo, err := os.Stat(filepath.Dir(databasePath))
	if err != nil {
		t.Fatalf("Stat(database parent) error = %v", err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("database parent mode = %o, want 700", got)
	}

	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s) error = %v", filepath.Base(path), err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(path), got)
		}
	}

	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}

	assertPragmaInt(t, store.db, "PRAGMA synchronous", 2)
	assertPragmaInt(t, store.db, "PRAGMA foreign_keys", 1)
	assertPragmaInt(t, store.db, "PRAGMA busy_timeout", 5000)

	for _, table := range []string{"categories", "downloads", "download_files"} {
		var count int
		err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count)
		if err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %q count = %d, want 1", table, count)
		}
	}
}

func TestOpenRejectsSymlinkDatabasePath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.sqlite")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "database.sqlite")
	if err := os.Symlink(target, databasePath); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(context.Background(), databasePath); err == nil {
		_ = store.Close()
		t.Fatal("Open() accepted a symlink database path")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "unchanged" {
		t.Fatalf("symlink target was modified: %q", content)
	}
}

func TestCategoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)

	inserted, err := store.queries.UpsertCategory(ctx, storedb.UpsertCategoryParams{
		Name:      "movies",
		CloudPath: "/cloud/movies",
		SavePath:  "/local/movies",
		Enabled:   1,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertCategory() error = %v", err)
	}
	if inserted.Name != "movies" {
		t.Errorf("UpsertCategory().Name = %q, want movies", inserted.Name)
	}

	got, err := store.queries.GetCategory(ctx, "movies")
	if err != nil {
		t.Fatalf("GetCategory() error = %v", err)
	}
	if got.CloudPath != "/cloud/movies" || got.SavePath != "/local/movies" || got.Enabled != 1 {
		t.Errorf("GetCategory() = %+v, want round-tripped category", got)
	}
}

func TestClaimDueOrdersAndHonorsLeases(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)

	insertTestDownload(t, store, "later", now.Add(time.Minute), now, sql.NullTime{})
	insertTestDownload(t, store, "expired", now.Add(-time.Minute), now.Add(2*time.Second), sql.NullTime{Time: now.Add(-time.Second), Valid: true})
	insertTestDownload(t, store, "first", now.Add(-time.Minute), now, sql.NullTime{})
	insertTestDownload(t, store, "live", now.Add(-2*time.Minute), now, sql.NullTime{Time: now.Add(time.Minute), Valid: true})
	insertTestDownload(t, store, "terminal", now.Add(-3*time.Minute), now, sql.NullTime{})
	if _, err := store.db.ExecContext(ctx, "UPDATE downloads SET state = 'COMPLETED' WHERE hash = 'terminal'"); err != nil {
		t.Fatalf("mark terminal download: %v", err)
	}

	claim, err := store.queries.ClaimDue(ctx, storedb.ClaimDueParams{
		Owner:      sql.NullString{String: "worker-1", Valid: true},
		LeaseUntil: sql.NullTime{Time: now.Add(time.Minute), Valid: true},
		Now:        sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		t.Fatalf("ClaimDue() error = %v", err)
	}
	if claim.Hash != "first" || claim.RowVersion != 1 || !claim.LeaseOwner.Valid || claim.LeaseOwner.String != "worker-1" {
		t.Errorf("first ClaimDue() = %+v, want first due unleased row with version 1", claim)
	}

	claim, err = store.queries.ClaimDue(ctx, storedb.ClaimDueParams{
		Owner:      sql.NullString{String: "worker-2", Valid: true},
		LeaseUntil: sql.NullTime{Time: now.Add(time.Minute), Valid: true},
		Now:        sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		t.Fatalf("second ClaimDue() error = %v", err)
	}
	if claim.Hash != "expired" || claim.RowVersion != 1 {
		t.Errorf("second ClaimDue() = %+v, want expired lease row with version 1", claim)
	}
}

func TestReady(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	localRoot := filepath.Join(t.TempDir(), "local-root")
	if err := os.Mkdir(localRoot, 0o700); err != nil {
		t.Fatalf("Mkdir(local root) error = %v", err)
	}

	if err := store.Ready(ctx, localRoot); err != nil {
		t.Errorf("Ready(existing root) error = %v", err)
	}
	if err := store.Ready(ctx, filepath.Join(localRoot, "missing")); err == nil {
		t.Error("Ready(missing root) error = nil, want error")
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func insertTestDownload(t *testing.T, store *Store, hash string, nextRunAt, createdAt time.Time, leaseUntil sql.NullTime) {
	t.Helper()
	err := store.queries.InsertDownload(context.Background(), storedb.InsertDownloadParams{
		Hash:           hash,
		Name:           hash,
		SourceKind:     "magnet",
		SubmissionUri:  "magnet:?xt=urn:btih:" + hash,
		Category:       "",
		CloudFolder:    "/cloud",
		SavePath:       "/local",
		TotalSize:      0,
		State:          "ACCEPTED",
		PhaseStartedAt: createdAt,
		NextRunAt:      sql.NullTime{Time: nextRunAt, Valid: true},
		LeaseUntil:     leaseUntil,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	})
	if err != nil {
		t.Fatalf("InsertDownload(%q) error = %v", hash, err)
	}
}

func assertPragmaInt(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Errorf("%s = %d, want %d", query, got, want)
	}
}
