package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/session"
)

const (
	sessionTestTTL             = 24 * time.Hour
	sessionTestRefreshInterval = time.Hour
)

// sessionTestClock is a minimal session.Clock for deterministic tests.
type sessionTestClock struct {
	now time.Time
}

func (c *sessionTestClock) Now() time.Time {
	return c.now
}

func (c *sessionTestClock) Advance(duration time.Duration) {
	c.now = c.now.Add(duration)
}

func openSessionStore(t *testing.T, repository session.Repository, clock session.Clock, capacity int) *session.Store {
	t.Helper()
	store, err := session.New(repository, clock, rand.Reader, sessionTestTTL, sessionTestRefreshInterval, capacity)
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}
	return store
}

func TestSessionReopenPersistence(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "sessions.sqlite")
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	clock := &sessionTestClock{now: now}

	first, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	sessions := openSessionStore(t, first, clock, 8)
	sid, current, err := sessions.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}

	second, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close(second) error = %v", err)
		}
	})
	reopened := openSessionStore(t, second, clock, 8)

	got, renewed, err := reopened.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}
	if renewed {
		t.Fatal("Get() renewed a fresh session after reopen")
	}
	if got.CSRFToken != current.CSRFToken || !got.CreatedAt.Equal(current.CreatedAt) || !got.ExpiresAt.Equal(current.ExpiresAt) {
		t.Fatalf("reopened session = %+v, want %+v", got, current)
	}
}

func TestSessionExpiryPurge(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	clock := &sessionTestClock{now: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	sessions := openSessionStore(t, store, clock, 8)
	for range 3 {
		if _, _, err := sessions.Create(ctx); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	if got, err := sessions.Len(ctx); err != nil || got != 3 {
		t.Fatalf("Len() = (%d, %v), want 3", got, err)
	}

	clock.Advance(sessionTestTTL - time.Minute)
	if got, err := sessions.PurgeExpired(ctx); err != nil || got != 0 {
		t.Fatalf("PurgeExpired() before expiry = (%d, %v), want 0", got, err)
	}
	clock.Advance(time.Minute)
	if got, err := sessions.PurgeExpired(ctx); err != nil || got != 3 {
		t.Fatalf("PurgeExpired() at expiry boundary = (%d, %v), want 3", got, err)
	}
	if got, err := sessions.Len(ctx); err != nil || got != 0 {
		t.Fatalf("Len() after purge = (%d, %v), want 0", got, err)
	}
}

func TestSessionRefreshCAS(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	digest := session.Digest{1}
	created := session.Session{CSRFToken: "csrf-token", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	inserted, err := store.CreateSession(ctx, digest, created, now, 4)
	if err != nil || !inserted {
		t.Fatalf("CreateSession() = (%t, %v), want inserted", inserted, err)
	}

	renewedAt := now.Add(48 * time.Hour)
	updated, err := store.RefreshSession(ctx, digest, created.ExpiresAt, renewedAt)
	if err != nil || !updated {
		t.Fatalf("RefreshSession() = (%t, %v), want updated", updated, err)
	}
	got, err := store.GetSession(ctx, digest)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if !got.ExpiresAt.Equal(renewedAt) || got.CSRFToken != created.CSRFToken || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("refreshed session = %+v, want new expiry with preserved identity", got)
	}

	updated, err = store.RefreshSession(ctx, digest, created.ExpiresAt, now.Add(72*time.Hour))
	if err != nil || updated {
		t.Fatalf("stale RefreshSession() = (%t, %v), want miss", updated, err)
	}
	got, err = store.GetSession(ctx, digest)
	if err != nil || !got.ExpiresAt.Equal(renewedAt) {
		t.Fatalf("session after stale CAS = (%+v, %v), want unchanged expiry", got, err)
	}

	updated, err = store.RefreshSession(ctx, digest, renewedAt, now.Add(72*time.Hour))
	if err != nil || !updated {
		t.Fatalf("follow-up RefreshSession() = (%t, %v), want updated", updated, err)
	}
}

func TestSessionRevocation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	digest := session.Digest{2}
	inserted, err := store.CreateSession(ctx, digest, session.Session{CSRFToken: "csrf", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, now, 4)
	if err != nil || !inserted {
		t.Fatalf("CreateSession() = (%t, %v), want inserted", inserted, err)
	}

	if err := store.RevokeSession(ctx, digest); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, err := store.GetSession(ctx, digest); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("GetSession() after revoke = %v, want ErrNotFound", err)
	}
	if err := store.RevokeSession(ctx, digest); err != nil {
		t.Fatalf("idempotent RevokeSession() error = %v", err)
	}
	if count, err := store.CountSessions(ctx); err != nil || count != 0 {
		t.Fatalf("CountSessions() = (%d, %v), want 0", count, err)
	}
}

func TestSessionCapacityEviction(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	insert := func(t *testing.T, store *Store, digest session.Digest, created, expires time.Time) {
		t.Helper()
		inserted, err := store.CreateSession(context.Background(), digest,
			session.Session{CSRFToken: "csrf", CreatedAt: created, ExpiresAt: expires}, created, 2)
		if err != nil || !inserted {
			t.Fatalf("CreateSession() = (%t, %v), want inserted", inserted, err)
		}
	}
	assertGone := func(t *testing.T, store *Store, digest session.Digest) {
		t.Helper()
		if _, err := store.GetSession(context.Background(), digest); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("GetSession(evicted) = %v, want ErrNotFound", err)
		}
	}
	assertPresent := func(t *testing.T, store *Store, digest session.Digest) {
		t.Helper()
		if _, err := store.GetSession(context.Background(), digest); err != nil {
			t.Fatalf("GetSession(retained) = %v, want present", err)
		}
	}

	t.Run("earliest expiry", func(t *testing.T) {
		store := testStore(t)
		insert(t, store, session.Digest{0x11}, now, now.Add(time.Hour))
		insert(t, store, session.Digest{0x22}, now.Add(time.Minute), now.Add(time.Hour+time.Minute))
		insert(t, store, session.Digest{0x33}, now.Add(2*time.Minute), now.Add(time.Hour+2*time.Minute))
		assertGone(t, store, session.Digest{0x11})
		assertPresent(t, store, session.Digest{0x22})
		assertPresent(t, store, session.Digest{0x33})
	})

	t.Run("created_at tie", func(t *testing.T) {
		store := testStore(t)
		sameExpiry := now.Add(2 * time.Hour)
		insert(t, store, session.Digest{0x11}, now, sameExpiry)
		insert(t, store, session.Digest{0x22}, now.Add(time.Minute), sameExpiry)
		insert(t, store, session.Digest{0x33}, now.Add(2*time.Minute), now.Add(3*time.Hour))
		assertGone(t, store, session.Digest{0x11})
		assertPresent(t, store, session.Digest{0x22})
		assertPresent(t, store, session.Digest{0x33})
	})

	t.Run("digest tie", func(t *testing.T) {
		store := testStore(t)
		insert(t, store, session.Digest{0x01}, now, now.Add(time.Hour))
		insert(t, store, session.Digest{0x80}, now, now.Add(time.Hour))
		insert(t, store, session.Digest{0x40}, now.Add(time.Minute), now.Add(time.Hour+time.Minute))
		assertGone(t, store, session.Digest{0x01})
		assertPresent(t, store, session.Digest{0x80})
		assertPresent(t, store, session.Digest{0x40})
	})

	t.Run("collision does not evict", func(t *testing.T) {
		store := testStore(t)
		ctx := context.Background()
		insert(t, store, session.Digest{0x11}, now, now.Add(time.Hour))
		insert(t, store, session.Digest{0x22}, now, now.Add(time.Hour))
		inserted, err := store.CreateSession(ctx, session.Digest{0x11},
			session.Session{CSRFToken: "csrf", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, now, 2)
		if err != nil || inserted {
			t.Fatalf("colliding CreateSession() = (%t, %v), want not inserted", inserted, err)
		}
		assertPresent(t, store, session.Digest{0x11})
		assertPresent(t, store, session.Digest{0x22})
		if count, err := store.CountSessions(ctx); err != nil || count != 2 {
			t.Fatalf("CountSessions() after collision = (%d, %v), want 2", count, err)
		}
	})
}

func TestSessionDigestOnlyStorage(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	clock := &sessionTestClock{now: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	sessions := openSessionStore(t, store, clock, 4)
	sid, current, err := sessions.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantDigest := sha256.Sum256([]byte(sid))

	rows, err := store.db.QueryContext(ctx, "SELECT sid_digest, csrf_token FROM sessions")
	if err != nil {
		t.Fatalf("query sessions rows: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var digest []byte
		var csrf string
		if err := rows.Scan(&digest, &csrf); err != nil {
			t.Fatalf("scan session row: %v", err)
		}
		count++
		if len(digest) != sha256.Size {
			t.Fatalf("stored sid_digest length = %d, want %d", len(digest), sha256.Size)
		}
		if !bytes.Equal(digest, wantDigest[:]) {
			t.Fatalf("stored sid_digest = %x, want SHA-256 of the SID %x", digest, wantDigest)
		}
		if string(digest) == sid {
			t.Fatal("raw SID persisted in sid_digest")
		}
		if csrf != current.CSRFToken {
			t.Fatalf("stored csrf_token = %q, want %q", csrf, current.CSRFToken)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate session rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("session rows = %d, want 1", count)
	}
}

func TestSessionPersistedRecordValidation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	corruptInsert := func(digest []byte, csrf string, created, expires time.Time) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, "PRAGMA ignore_check_constraints = ON"); err != nil {
			t.Fatalf("enable ignored check constraints: %v", err)
		}
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO sessions (sid_digest, csrf_token, created_at, expires_at) VALUES (?, ?, ?, ?)",
			digest, csrf, created, expires); err != nil {
			t.Fatalf("insert corrupt session row: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, "PRAGMA ignore_check_constraints = OFF"); err != nil {
			t.Fatalf("disable ignored check constraints: %v", err)
		}
	}

	var emptyCSRF session.Digest
	emptyCSRF[0] = 0x51
	corruptInsert(emptyCSRF[:], "", now, now.Add(time.Hour))
	if _, err := store.GetSession(ctx, emptyCSRF); err == nil || !strings.Contains(err.Error(), "stored session is invalid") {
		t.Fatalf("GetSession(empty CSRF) = %v, want invalid stored session", err)
	}

	var staleExpiry session.Digest
	staleExpiry[0] = 0x52
	corruptInsert(staleExpiry[:], "csrf", now, now)
	if _, err := store.GetSession(ctx, staleExpiry); err == nil || !strings.Contains(err.Error(), "stored session is invalid") {
		t.Fatalf("GetSession(expiry at creation) = %v, want invalid stored session", err)
	}
}
