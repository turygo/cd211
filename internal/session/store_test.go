package session

import (
	"bytes"
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

func TestNewValidatesConfiguration(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	reader := bytes.NewReader(nil)
	var nilClock *testClock
	var nilReader *bytes.Reader

	tests := []struct {
		name     string
		clock    Clock
		random   io.Reader
		ttl      time.Duration
		capacity int
	}{
		{name: "nil clock", random: reader, ttl: time.Hour, capacity: 1},
		{name: "nil random", clock: clock, ttl: time.Hour, capacity: 1},
		{name: "typed nil clock", clock: nilClock, random: reader, ttl: time.Hour, capacity: 1},
		{name: "typed nil random", clock: clock, random: nilReader, ttl: time.Hour, capacity: 1},
		{name: "zero ttl", clock: clock, random: reader, capacity: 1},
		{name: "negative ttl", clock: clock, random: reader, ttl: -time.Second, capacity: 1},
		{name: "zero capacity", clock: clock, random: reader, ttl: time.Hour},
		{name: "negative capacity", clock: clock, random: reader, ttl: time.Hour, capacity: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := New(test.clock, test.random, test.ttl, test.capacity)
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
	store := newTestStore(t, clock, tokenData(0, 1), time.Hour, 4)
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
	store := newTestStore(t, clock, tokenData(0, 1), time.Hour, 2)
	for _, remote := range []string{"192.0.2.1:1", "192.0.2.2:2", "192.0.2.3:3"} {
		store.AuthorizeLogin(remote, false)
		clock.Advance(time.Second)
	}
	if len(store.logins) != 2 {
		t.Fatalf("login attempt records = %d, want bounded capacity 2", len(store.logins))
	}
}

func TestCreateProducesIndependent256BitTokens(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(0, 1), time.Hour, 1)

	sid, session, err := store.Create()
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
	stored, ok := store.Get(sid)
	if !ok {
		t.Fatal("Get() did not return the created session")
	}
	if stored.CSRFToken != originalCSRF {
		t.Fatalf("Get() CSRF token = %q after caller mutation, want %q", stored.CSRFToken, originalCSRF)
	}
}

func TestGetExpiresAtAbsoluteBoundaryWithoutSliding(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 2), time.Hour, 1)

	sid, created, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(30 * time.Minute)
	got, ok := store.Get(sid)
	if !ok {
		t.Fatal("Get() did not return an unexpired session")
	}
	if !got.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("Get() slid expiry from %v to %v", created.ExpiresAt, got.ExpiresAt)
	}
	clock.Advance(30 * time.Minute)
	if _, ok := store.Get(sid); ok {
		t.Fatal("Get() returned a session at its expiry boundary")
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("Len() = %d after expired Get(), want 0", got)
	}
}

func TestGetRejectsMalformedSID(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 2), time.Hour, 1)
	sid, _, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	malformed := []string{"", sid[:42], sid + "A", "!" + sid[1:], sid[:42] + "B"}
	for _, candidate := range malformed {
		if _, ok := store.Get(candidate); ok {
			t.Fatalf("Get(%q) returned a session for malformed SID", candidate)
		}
	}
	if _, ok := store.Get(sid); !ok {
		t.Fatal("Get() removed the valid session while handling malformed SIDs")
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 2), time.Hour, 1)
	sid, _, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	store.Revoke(sid)
	store.Revoke(sid)
	if _, ok := store.Get(sid); ok {
		t.Fatal("Get() returned a revoked session")
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("Len() = %d after repeated Revoke(), want 0", got)
	}
}

func TestCreateEvictsEarliestExpiryAndLexicalSIDTie(t *testing.T) {
	t.Run("earliest expiry", func(t *testing.T) {
		clock := &testClock{now: time.Unix(100, 0)}
		store := newTestStore(t, clock, tokenData(2, 20, 3, 30, 4, 40), time.Hour, 2)
		firstSID, _, err := store.Create()
		if err != nil {
			t.Fatalf("first Create() error = %v", err)
		}
		clock.Advance(time.Minute)
		secondSID, _, err := store.Create()
		if err != nil {
			t.Fatalf("second Create() error = %v", err)
		}
		thirdSID, _, err := store.Create()
		if err != nil {
			t.Fatalf("third Create() error = %v", err)
		}
		if _, ok := store.Get(firstSID); ok {
			t.Fatal("Create() retained the session with earliest expiry")
		}
		if _, ok := store.Get(secondSID); !ok {
			t.Fatal("Create() evicted a later-expiring session")
		}
		if _, ok := store.Get(thirdSID); !ok {
			t.Fatal("Create() did not retain the new session")
		}
	})

	t.Run("lexical sid tie", func(t *testing.T) {
		clock := &testClock{now: time.Unix(100, 0)}
		sidZ := tokenString(255)
		sidA := tokenString(0)
		store := newTestStore(t, clock, tokenData(255, 50, 0, 60, 1, 70), time.Hour, 2)
		createdZ, _, err := store.Create()
		if err != nil {
			t.Fatalf("first Create() error = %v", err)
		}
		createdA, _, err := store.Create()
		if err != nil {
			t.Fatalf("second Create() error = %v", err)
		}
		_, _, err = store.Create()
		if err != nil {
			t.Fatalf("third Create() error = %v", err)
		}
		if createdZ != sidZ || createdA != sidA {
			t.Fatalf("test token order = %q, %q, want %q, %q", createdZ, createdA, sidZ, sidA)
		}
		if _, ok := store.Get(sidA); ok {
			t.Fatal("Create() did not evict the lexical-first SID at an exact time tie")
		}
		if _, ok := store.Get(sidZ); !ok {
			t.Fatal("Create() evicted the lexical-later SID at an exact time tie")
		}
	})
}

func TestPurgeExpiredReturnsRemovalCount(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 11, 2, 12, 3, 13), time.Minute, 3)
	for range 3 {
		if _, _, err := store.Create(); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	clock.Advance(time.Minute)
	if got := store.PurgeExpired(); got != 3 {
		t.Fatalf("PurgeExpired() = %d, want 3", got)
	}
	if got := store.PurgeExpired(); got != 0 {
		t.Fatalf("second PurgeExpired() = %d, want 0", got)
	}
}

func TestCreateRetriesSIDCollisionsAndFailsAfterThreeAttempts(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, tokenData(1, 11, 1, 12, 2, 13), time.Hour, 2)
	originalSID, _, err := store.Create()
	if err != nil {
		t.Fatalf("initial Create() error = %v", err)
	}
	newSID, session, err := store.Create()
	if err != nil {
		t.Fatalf("retrying Create() error = %v", err)
	}
	if newSID == originalSID || newSID != tokenString(2) {
		t.Fatalf("Create() SID = %q, want a distinct retried SID %q", newSID, tokenString(2))
	}
	if session.CSRFToken != tokenString(13) {
		t.Fatalf("Create() CSRF token = %q, want %q", session.CSRFToken, tokenString(13))
	}

	store = newTestStore(t, clock, tokenData(9, 19, 9, 20, 9, 21, 9, 22), time.Hour, 2)
	if _, _, err := store.Create(); err != nil {
		t.Fatalf("initial Create() error = %v", err)
	}
	if _, _, err := store.Create(); !errors.Is(err, errSIDCollision) {
		t.Fatalf("Create() collision error = %v, want %v", err, errSIDCollision)
	}
}

func TestCreatePropagatesShortReadAndRandomError(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}

	shortStore := newTestStore(t, clock, bytes.NewReader(bytes.Repeat([]byte{1}, tokenBytes-1)), time.Hour, 1)
	if _, _, err := shortStore.Create(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Create() short-read error = %v, want %v", err, io.ErrUnexpectedEOF)
	}

	randomErr := errors.New("random reader failed")
	errorStore := newTestStore(t, clock, errorReader{err: randomErr}, time.Hour, 1)
	if _, _, err := errorStore.Create(); !errors.Is(err, randomErr) {
		t.Fatalf("Create() random error = %v, want %v", err, randomErr)
	}
}

func TestStoreConcurrentCreateGetAndRevoke(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newTestStore(t, clock, rand.Reader, time.Hour, 10_000)

	const goroutines = 24
	const iterations = 40
	var group sync.WaitGroup
	for range goroutines {
		group.Add(1)
		go func() {
			defer group.Done()
			for range iterations {
				sid, _, err := store.Create()
				if err != nil {
					t.Errorf("Create() error = %v", err)
					return
				}
				if _, ok := store.Get(sid); !ok {
					t.Error("Get() did not return the newly created session")
					return
				}
				store.Revoke(sid)
				if _, ok := store.Get(sid); ok {
					t.Error("Get() returned a revoked session")
					return
				}
			}
		}()
	}
	group.Wait()
}

func newTestStore(t *testing.T, clock Clock, random io.Reader, ttl time.Duration, capacity int) *Store {
	t.Helper()
	store, err := New(clock, random, ttl, capacity)
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
