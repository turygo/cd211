package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

// memoryRepository is a narrow in-memory fake of the durable session boundary.
// It mirrors the repository contract: digest-keyed records, expiry purge,
// capacity eviction ordered by expires_at, then created_at, then digest, an
// expiry compare-and-swap refresh, and idempotent revocation.
type memoryRepository struct {
	mu       sync.Mutex
	sessions map[Digest]Session
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{sessions: make(map[Digest]Session)}
}

func (r *memoryRepository) CreateSession(ctx context.Context, digest Digest, session Session, now time.Time, capacity int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[digest]; exists {
		return false, nil
	}
	for candidate, stored := range r.sessions {
		if !now.Before(stored.ExpiresAt) {
			delete(r.sessions, candidate)
		}
	}
	for len(r.sessions) >= capacity {
		var evictDigest Digest
		var evictSession Session
		first := true
		for candidate, stored := range r.sessions {
			if first || sessionEvictsFirst(candidate, stored, evictDigest, evictSession) {
				evictDigest = candidate
				evictSession = stored
				first = false
			}
		}
		delete(r.sessions, evictDigest)
	}
	r.sessions[digest] = session
	return true, nil
}

func (r *memoryRepository) GetSession(ctx context.Context, digest Digest) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[digest]
	if !exists {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (r *memoryRepository) RefreshSession(ctx context.Context, digest Digest, expectedExpiresAt, newExpiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[digest]
	if !exists || !session.ExpiresAt.Equal(expectedExpiresAt) {
		return false, nil
	}
	session.ExpiresAt = newExpiresAt
	r.sessions[digest] = session
	return true, nil
}

func (r *memoryRepository) RevokeSession(ctx context.Context, digest Digest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, digest)
	return nil
}

func (r *memoryRepository) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var removed int64
	for digest, session := range r.sessions {
		if !now.Before(session.ExpiresAt) {
			delete(r.sessions, digest)
			removed++
		}
	}
	return removed, nil
}

func (r *memoryRepository) CountSessions(ctx context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions), nil
}

func sessionEvictsFirst(digest Digest, session Session, otherDigest Digest, other Session) bool {
	if !session.ExpiresAt.Equal(other.ExpiresAt) {
		return session.ExpiresAt.Before(other.ExpiresAt)
	}
	if !session.CreatedAt.Equal(other.CreatedAt) {
		return session.CreatedAt.Before(other.CreatedAt)
	}
	return bytes.Compare(digest[:], otherDigest[:]) < 0
}

// failingRepository fails every repository method with a fixed error, letting
// tests prove that repository failures propagate as errors rather than as
// authentication misses.
type failingRepository struct {
	Repository
	err error
}

func (f failingRepository) CreateSession(context.Context, Digest, Session, time.Time, int) (bool, error) {
	return false, f.err
}

func (f failingRepository) GetSession(context.Context, Digest) (Session, error) {
	return Session{}, f.err
}

func (f failingRepository) RefreshSession(context.Context, Digest, time.Time, time.Time) (bool, error) {
	return false, f.err
}

func (f failingRepository) RevokeSession(context.Context, Digest) error {
	return f.err
}

func (f failingRepository) PurgeExpiredSessions(context.Context, time.Time) (int64, error) {
	return 0, f.err
}

func (f failingRepository) CountSessions(context.Context) (int, error) {
	return 0, f.err
}

// revokeFailingRepository only fails revocation, so Get can prove that a
// deletion failure on an expired record is returned rather than masked.
type revokeFailingRepository struct {
	Repository
	err error
}

func (f revokeFailingRepository) RevokeSession(context.Context, Digest) error {
	return f.err
}

// refreshFailingRepository only fails the expiry refresh, so Get can prove
// that a renewal failure propagates rather than falling back to ErrNotFound.
type refreshFailingRepository struct {
	Repository
	err error
}

func (f refreshFailingRepository) RefreshSession(context.Context, Digest, time.Time, time.Time) (bool, error) {
	return false, f.err
}

// refreshLoserRepository simulates losing a concurrent refresh: the winner's
// renewal is applied to the underlying repository and the caller is told its
// CAS missed, exercising the reread path in Get. It is used by pointer so a
// test can set winnerExpiry after constructing the store.
type refreshLoserRepository struct {
	Repository
	winnerExpiry time.Time
}

func (r refreshLoserRepository) RefreshSession(ctx context.Context, digest Digest, expectedExpiresAt, _ time.Time) (bool, error) {
	if _, err := r.Repository.RefreshSession(ctx, digest, expectedExpiresAt, r.winnerExpiry); err != nil {
		return false, err
	}
	return false, nil
}

// casMissRepository always reports a refresh CAS miss and hides the record on
// the reread, exercising the contract's reread path when the record vanished.
type casMissRepository struct {
	Repository
	reads int
}

func (c *casMissRepository) RefreshSession(context.Context, Digest, time.Time, time.Time) (bool, error) {
	return false, nil
}

func (c *casMissRepository) GetSession(ctx context.Context, digest Digest) (Session, error) {
	c.reads++
	if c.reads >= 2 {
		return Session{}, ErrNotFound
	}
	return c.Repository.GetSession(ctx, digest)
}

func TestNewValidatesConfiguration(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	reader := bytes.NewReader(nil)
	repository := newMemoryRepository()
	var nilClock *testClock
	var nilReader *bytes.Reader
	var nilRepository *memoryRepository

	tests := []struct {
		name            string
		repository      Repository
		clock           Clock
		random          io.Reader
		ttl             time.Duration
		refreshInterval time.Duration
		capacity        int
	}{
		{name: "nil repository", clock: clock, random: reader, ttl: time.Hour, refreshInterval: time.Minute, capacity: 1},
		{name: "typed nil repository", repository: nilRepository, clock: clock, random: reader, ttl: time.Hour, refreshInterval: time.Minute, capacity: 1},
		{name: "nil clock", repository: repository, random: reader, ttl: time.Hour, refreshInterval: time.Minute, capacity: 1},
		{name: "nil random", repository: repository, clock: clock, ttl: time.Hour, refreshInterval: time.Minute, capacity: 1},
		{name: "typed nil clock", repository: repository, clock: nilClock, random: reader, ttl: time.Hour, refreshInterval: time.Minute, capacity: 1},
		{name: "typed nil random", repository: repository, clock: clock, random: nilReader, ttl: time.Hour, refreshInterval: time.Minute, capacity: 1},
		{name: "zero ttl", repository: repository, clock: clock, random: reader, refreshInterval: time.Minute, capacity: 1},
		{name: "negative ttl", repository: repository, clock: clock, random: reader, ttl: -time.Second, refreshInterval: time.Minute, capacity: 1},
		{name: "zero refresh interval", repository: repository, clock: clock, random: reader, ttl: time.Hour, capacity: 1},
		{name: "negative refresh interval", repository: repository, clock: clock, random: reader, ttl: time.Hour, refreshInterval: -time.Second, capacity: 1},
		{name: "refresh interval equals ttl", repository: repository, clock: clock, random: reader, ttl: time.Hour, refreshInterval: time.Hour, capacity: 1},
		{name: "refresh interval exceeds ttl", repository: repository, clock: clock, random: reader, ttl: time.Hour, refreshInterval: 2 * time.Hour, capacity: 1},
		{name: "zero capacity", repository: repository, clock: clock, random: reader, ttl: time.Hour, refreshInterval: time.Minute},
		{name: "negative capacity", repository: repository, clock: clock, random: reader, ttl: time.Hour, refreshInterval: time.Minute, capacity: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := New(test.repository, test.clock, test.random, test.ttl, test.refreshInterval, test.capacity)
			if err == nil {
				t.Fatal("New() succeeded for invalid configuration")
			}
			if store != nil {
				t.Fatal("New() returned a store for invalid configuration")
			}
		})
	}
}

func TestAuthorizeLoginBansByAddressAndExpires(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(0, 1), time.Hour, time.Minute, 4)
	for attempt := 1; attempt < loginFailureLimit; attempt++ {
		if decision := store.AuthorizeLogin("192.0.2.10:"+strconv.Itoa(1000+attempt), false); decision != LoginInvalid {
			t.Fatalf("failure %d decision = %v, want invalid", attempt, decision)
		}
	}
	if decision := store.AuthorizeLogin("192.0.2.10:2000", false); decision != LoginBanned {
		t.Fatalf("threshold decision = %v, want banned", decision)
	}
	if decision := store.AuthorizeLogin("192.0.2.10:3000", true); decision != LoginBanned {
		t.Fatalf("valid credentials bypassed active ban: %v", decision)
	}
	if decision := store.AuthorizeLogin("192.0.2.11:3000", true); decision != LoginAllowed {
		t.Fatalf("independent address decision = %v, want allowed", decision)
	}
	clock.Advance(loginBanDuration)
	if decision := store.AuthorizeLogin("192.0.2.10:4000", true); decision != LoginAllowed {
		t.Fatalf("expired ban decision = %v, want allowed", decision)
	}
}

func TestAuthorizeLoginAttemptMapIsBounded(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(0, 1), time.Hour, time.Minute, 2)
	for _, remote := range []string{"192.0.2.1:1", "192.0.2.2:2", "192.0.2.3:3"} {
		store.AuthorizeLogin(remote, false)
		clock.Advance(time.Second)
	}
	if len(store.logins) != 2 {
		t.Fatalf("login attempt records = %d, want bounded capacity 2", len(store.logins))
	}
}

func TestCreateProducesIndependent256BitTokens(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(0, 1), time.Hour, time.Minute, 1)

	sid, session, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(sid) != 43 || len(session.CSRFToken) != 43 {
		t.Fatalf("token lengths = %d, %d, want 43, 43", len(sid), len(session.CSRFToken))
	}
	sidBytes, err := base64.RawURLEncoding.DecodeString(sid)
	if err != nil {
		t.Fatalf("DecodeString(sid) error = %v", err)
	}
	csrfBytes, err := base64.RawURLEncoding.DecodeString(session.CSRFToken)
	if err != nil {
		t.Fatalf("DecodeString(CSRFToken) error = %v", err)
	}
	if len(sidBytes) != tokenBytes || len(csrfBytes) != tokenBytes {
		t.Fatalf("decoded token lengths = %d, %d, want %d, %d", len(sidBytes), len(csrfBytes), tokenBytes, tokenBytes)
	}
	if sid == session.CSRFToken {
		t.Fatal("Create() derived identical SID and CSRF token")
	}
	if want := clock.Now().Add(time.Hour); !session.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", session.ExpiresAt, want)
	}

	originalCSRF := session.CSRFToken
	session.CSRFToken = "changed"
	stored, renewed, err := store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if renewed {
		t.Fatal("Get() renewed a fresh session")
	}
	if stored.CSRFToken != originalCSRF {
		t.Fatalf("Get() CSRF token = %q after caller mutation, want %q", stored.CSRFToken, originalCSRF)
	}
}

func TestGetExpiresAtAbsoluteBoundaryWithoutSliding(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 2), time.Hour, 50*time.Minute, 1)

	sid, created, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(30 * time.Minute)
	got, renewed, err := store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if renewed {
		t.Fatal("Get() renewed before the refresh threshold")
	}
	if !got.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("Get() slid expiry from %v to %v", created.ExpiresAt, got.ExpiresAt)
	}
	clock.Advance(30 * time.Minute)
	if _, _, err := store.Get(ctx, sid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() at expiry boundary = (%v), want ErrNotFound", err)
	}
	if got, err := store.Len(ctx); err != nil || got != 0 {
		t.Fatalf("Len() = (%d, %v) after expired Get(), want 0", got, err)
	}
}

func TestGetRejectsMalformedSID(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 2), time.Hour, time.Minute, 1)
	sid, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	malformed := []string{"", sid[:42], sid + "A", "!" + sid[1:], sid[:42] + "B"}
	for _, candidate := range malformed {
		if _, _, err := store.Get(ctx, candidate); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q) error = %v, want ErrNotFound", candidate, err)
		}
	}
	if _, _, err := store.Get(ctx, sid); err != nil {
		t.Fatal("Get() removed the valid session while handling malformed SIDs")
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 2), time.Hour, time.Minute, 1)
	sid, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := store.Revoke(ctx, sid); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if err := store.Revoke(ctx, sid); err != nil {
		t.Fatalf("second Revoke() error = %v", err)
	}
	if _, _, err := store.Get(ctx, sid); !errors.Is(err, ErrNotFound) {
		t.Fatal("Get() returned a revoked session")
	}
	if got, err := store.Len(ctx); err != nil || got != 0 {
		t.Fatalf("Len() = (%d, %v) after repeated Revoke(), want 0", got, err)
	}
}

func TestCreateEvictsEarliestExpiry(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(2, 20, 3, 30, 4, 40), time.Hour, time.Minute, 2)
	firstSID, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	clock.Advance(time.Minute)
	secondSID, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	thirdSID, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("third Create() error = %v", err)
	}
	if _, _, err := store.Get(ctx, firstSID); !errors.Is(err, ErrNotFound) {
		t.Fatal("Create() retained the session with earliest expiry")
	}
	if _, _, err := store.Get(ctx, secondSID); err != nil {
		t.Fatal("Create() evicted a later-expiring session")
	}
	if _, _, err := store.Get(ctx, thirdSID); err != nil {
		t.Fatal("Create() did not retain the new session")
	}
}

func TestPurgeExpiredReturnsRemovalCount(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 11, 2, 12, 3, 13), time.Minute, time.Second, 3)
	for range 3 {
		if _, _, err := store.Create(ctx); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	clock.Advance(time.Minute)
	if got, err := store.PurgeExpired(ctx); err != nil || got != 3 {
		t.Fatalf("PurgeExpired() = (%d, %v), want 3", got, err)
	}
	if got, err := store.PurgeExpired(ctx); err != nil || got != 0 {
		t.Fatalf("second PurgeExpired() = (%d, %v), want 0", got, err)
	}
}

func TestGetRefreshesAtThresholdAndNotBefore(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 2), time.Hour, 10*time.Minute, 1)
	sid, created, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	clock.Advance(9 * time.Minute)
	got, renewed, err := store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() before threshold error = %v", err)
	}
	if renewed || !got.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("Get() before threshold = (renewed %t, expiry %v), want fresh expiry %v", renewed, got.ExpiresAt, created.ExpiresAt)
	}

	clock.Advance(time.Minute)
	got, renewed, err = store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() at threshold error = %v", err)
	}
	wantExpiry := clock.Now().Add(time.Hour)
	if !renewed || !got.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("Get() at threshold = (renewed %t, expiry %v), want renewed expiry %v", renewed, got.ExpiresAt, wantExpiry)
	}
	if got.CSRFToken != created.CSRFToken || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("Get() refresh changed identity: %+v", got)
	}

	clock.Advance(5 * time.Minute)
	got, renewed, err = store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() after refresh error = %v", err)
	}
	if renewed || !got.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("Get() after refresh = (renewed %t, expiry %v), want unchanged %v", renewed, got.ExpiresAt, wantExpiry)
	}

	clock.Advance(5 * time.Minute)
	got, renewed, err = store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() at second threshold error = %v", err)
	}
	wantExpiry = clock.Now().Add(time.Hour)
	if !renewed || !got.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("Get() at second threshold = (renewed %t, expiry %v), want renewed expiry %v", renewed, got.ExpiresAt, wantExpiry)
	}
}

func TestGetRefreshCASSMissReturnsAuthoritativeRecord(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	repository := &refreshLoserRepository{Repository: newMemoryRepository()}
	store, err := New(repository, clock, tokenData(1, 2), time.Hour, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	sid, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(15 * time.Minute)
	repository.winnerExpiry = clock.Now().Add(time.Hour)
	got, renewed, err := store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if renewed {
		t.Fatal("Get() renewed after a CAS miss")
	}
	if !got.ExpiresAt.Equal(repository.winnerExpiry) {
		t.Fatalf("Get() expiry = %v, want authoritative winner expiry %v", got.ExpiresAt, repository.winnerExpiry)
	}
}

func TestGetRefreshCASSMissWithMissingReread(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	repository := &casMissRepository{Repository: newMemoryRepository()}
	store, err := New(repository, clock, tokenData(1, 2), time.Hour, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sid, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(15 * time.Minute)
	if _, _, err := store.Get(ctx, sid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after CAS miss with vanished record = %v, want ErrNotFound", err)
	}
}

func TestGetDeletesExpiredRecordBeforeNotFound(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	repository := newMemoryRepository()
	store, err := New(repository, clock, tokenData(1, 2), time.Hour, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sid, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(time.Hour)
	if _, _, err := store.Get(ctx, sid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() on expired session = %v, want ErrNotFound", err)
	}
	if _, err := repository.GetSession(ctx, hashSID(sid)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired record still persisted after Get(), want ErrNotFound, got %v", err)
	}
}

func TestRepositoryErrorsPropagate(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	repositoryErr := errors.New("repository failure")
	store, err := New(failingRepository{Repository: newMemoryRepository(), err: repositoryErr}, clock, tokenData(1, 2), time.Hour, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, _, err := store.Create(ctx); !errors.Is(err, repositoryErr) {
		t.Fatalf("Create() error = %v, want repository error", err)
	}
	if _, _, err := store.Get(ctx, tokenString(1)); !errors.Is(err, repositoryErr) {
		t.Fatalf("Get() error = %v, want repository error", err)
	}
	if err := store.Revoke(ctx, tokenString(1)); !errors.Is(err, repositoryErr) {
		t.Fatalf("Revoke() error = %v, want repository error", err)
	}
	if _, err := store.PurgeExpired(ctx); !errors.Is(err, repositoryErr) {
		t.Fatalf("PurgeExpired() error = %v, want repository error", err)
	}
	if _, err := store.Len(ctx); !errors.Is(err, repositoryErr) {
		t.Fatalf("Len() error = %v, want repository error", err)
	}
}

func TestGetExpiredRecordDeletionFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	repositoryErr := errors.New("revoke failure")
	base := newMemoryRepository()
	store, err := New(revokeFailingRepository{Repository: base, err: repositoryErr}, clock, tokenData(1, 2), time.Hour, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sid, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(time.Hour)
	if _, _, err := store.Get(ctx, sid); !errors.Is(err, repositoryErr) {
		t.Fatalf("Get() deletion failure = %v, want repository error", err)
	}
}

func TestGetRefreshFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	repositoryErr := errors.New("refresh failure")
	base := newMemoryRepository()
	store, err := New(refreshFailingRepository{Repository: base, err: repositoryErr}, clock, tokenData(1, 2), time.Hour, 10*time.Minute, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sid, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(15 * time.Minute)
	if _, _, err := store.Get(ctx, sid); !errors.Is(err, repositoryErr) {
		t.Fatalf("Get() refresh failure = %v, want repository error", err)
	}
}

func TestCreateRetriesSIDCollisionsAndFailsAfterThreeAttempts(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 11, 1, 12, 2, 13), time.Hour, time.Minute, 2)
	originalSID, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("initial Create() error = %v", err)
	}
	newSID, session, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("retrying Create() error = %v", err)
	}
	if newSID == originalSID || newSID != tokenString(2) {
		t.Fatalf("Create() SID = %q, want a distinct retried SID %q", newSID, tokenString(2))
	}
	if session.CSRFToken != tokenString(13) {
		t.Fatalf("Create() CSRF token = %q, want %q", session.CSRFToken, tokenString(13))
	}

	store = newTestStore(t, clock, tokenData(9, 19, 9, 20, 9, 21, 9, 22), time.Hour, time.Minute, 2)
	if _, _, err := store.Create(ctx); err != nil {
		t.Fatalf("initial Create() error = %v", err)
	}
	if _, _, err := store.Create(ctx); !errors.Is(err, errSIDCollision) {
		t.Fatalf("Create() collision error = %v, want %v", err, errSIDCollision)
	}
}

func TestCreatePropagatesShortReadAndRandomError(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}

	shortStore := newTestStore(t, clock, bytes.NewReader(bytes.Repeat([]byte{1}, tokenBytes-1)), time.Hour, time.Minute, 1)
	if _, _, err := shortStore.Create(ctx); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Create() short-read error = %v, want %v", err, io.ErrUnexpectedEOF)
	}

	randomErr := errors.New("random reader failed")
	errorStore := newTestStore(t, clock, errorReader{err: randomErr}, time.Hour, time.Minute, 1)
	if _, _, err := errorStore.Create(ctx); !errors.Is(err, randomErr) {
		t.Fatalf("Create() random error = %v, want %v", err, randomErr)
	}
}

func TestStoreConcurrentCreateGetAndRevoke(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, rand.Reader, time.Hour, time.Minute, 10_000)

	const goroutines = 24
	const iterations = 40
	var group sync.WaitGroup
	for range goroutines {
		group.Add(1)
		go func() {
			defer group.Done()
			for range iterations {
				sid, _, err := store.Create(ctx)
				if err != nil {
					t.Errorf("Create() error = %v", err)
					return
				}
				if _, _, err := store.Get(ctx, sid); err != nil {
					t.Errorf("Get() error = %v", err)
					return
				}
				if err := store.Revoke(ctx, sid); err != nil {
					t.Errorf("Revoke() error = %v", err)
					return
				}
				if _, _, err := store.Get(ctx, sid); !errors.Is(err, ErrNotFound) {
					t.Errorf("Get() after Revoke() = %v, want ErrNotFound", err)
					return
				}
			}
		}()
	}
	group.Wait()
}

func newTestStore(t *testing.T, clock Clock, random io.Reader, ttl, refreshInterval time.Duration, capacity int) *Store {
	t.Helper()
	store, err := New(newMemoryRepository(), clock, random, ttl, refreshInterval, capacity)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store
}

func tokenData(values ...byte) *bytes.Reader {
	data := make([]byte, 0, len(values)*tokenBytes)
	for _, value := range values {
		data = append(data, bytes.Repeat([]byte{value}, tokenBytes)...)
	}
	return bytes.NewReader(data)
}

func tokenString(value byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, tokenBytes))
}
