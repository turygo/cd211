// Package session provides bounded authentication and session state. Session
// records are persisted through a Repository; only SHA-256 digests of session
// IDs ever reach the repository, so the raw SID exists only in the client
// cookie. Login attempt tracking and bans remain in process memory.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"
)

const tokenBytes = 32

const (
	loginWindow       = 5 * time.Minute
	loginBanDuration  = 15 * time.Minute
	loginFailureLimit = 5
)

var (
	errNilClock       = errors.New("session: clock is nil")
	errNilRandom      = errors.New("session: random reader is nil")
	errNilRepository  = errors.New("session: repository is nil")
	errNonPositiveTTL = errors.New("session: ttl must be positive")
	errNonPositiveCap = errors.New("session: capacity must be positive")
	errInvalidRefresh = errors.New("session: refresh interval must be positive and shorter than ttl")
	errSIDCollision   = errors.New("session: sid collision retries exhausted")
)

// ErrNotFound reports a session that is malformed, missing, or expired. The
// caller treats it as an authentication miss, never as a database failure.
var ErrNotFound = errors.New("session: not found")

// Digest is the SHA-256 digest of a canonical session ID. Only digests are
// persisted; the raw SID itself never is.
type Digest [sha256.Size]byte

// Repository is the durable session boundary consumed by Store. The SQLite
// store implements it.
type Repository interface {
	CreateSession(ctx context.Context, digest Digest, session Session, now time.Time, capacity int) (inserted bool, err error)
	GetSession(ctx context.Context, digest Digest) (Session, error)
	RefreshSession(ctx context.Context, digest Digest, expectedExpiresAt, newExpiresAt time.Time) (updated bool, err error)
	RevokeSession(ctx context.Context, digest Digest) error
	PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error)
	CountSessions(ctx context.Context) (int, error)
}

// Clock supplies the current time. It permits deterministic expiry handling in
// callers that need to control time.
type Clock interface {
	Now() time.Time
}

// Session is the state associated with a session ID. The session ID itself is
// deliberately kept separate as the store key.
type Session struct {
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// LoginDecision is the outcome of one credential check for a client address.
type LoginDecision uint8

const (
	LoginAllowed LoginDecision = iota
	LoginInvalid
	LoginBanned
)

type loginAttempt struct {
	failures    int
	windowStart time.Time
	bannedUntil time.Time
	touchedAt   time.Time
}

// Store issues, refreshes, and revokes sessions, delegating all durable state
// to a Repository. Login attempt tracking remains in process memory.
type Store struct {
	mu              sync.Mutex
	repository      Repository
	logins          map[string]loginAttempt
	random          io.Reader
	clock           Clock
	ttl             time.Duration
	refreshInterval time.Duration
	capacity        int
}

// New constructs a session store over a durable repository. The repository
// must not be nil, the clock and random reader must be usable, the ttl and
// capacity must be positive, and the refresh interval must be positive and
// shorter than the ttl.
func New(repository Repository, clock Clock, random io.Reader, ttl, refreshInterval time.Duration, capacity int) (*Store, error) {
	if isNil(repository) {
		return nil, errNilRepository
	}
	if isNil(clock) {
		return nil, errNilClock
	}
	if isNil(random) {
		return nil, errNilRandom
	}
	if ttl <= 0 {
		return nil, errNonPositiveTTL
	}
	if refreshInterval <= 0 || refreshInterval >= ttl {
		return nil, errInvalidRefresh
	}
	if capacity <= 0 {
		return nil, errNonPositiveCap
	}

	return &Store{
		repository:      repository,
		logins:          make(map[string]loginAttempt),
		random:          random,
		clock:           clock,
		ttl:             ttl,
		refreshInterval: refreshInterval,
		capacity:        capacity,
	}, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Create generates a new session ID and an independent CSRF token, persists
// the record through the repository, and returns the plaintext SID exactly
// once. The repository reports whether the insert won; a digest collision
// retries with a fresh SID.
func (s *Store) Create(ctx context.Context) (sid string, current Session, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	for range 3 {
		candidateSID, err := s.newToken()
		if err != nil {
			return "", Session{}, err
		}
		csrfToken, err := s.newToken()
		if err != nil {
			return "", Session{}, err
		}
		current = Session{
			CSRFToken: csrfToken,
			CreatedAt: now,
			ExpiresAt: now.Add(s.ttl),
		}
		inserted, err := s.repository.CreateSession(ctx, hashSID(candidateSID), current, now, s.capacity)
		if err != nil {
			return "", Session{}, err
		}
		if inserted {
			return candidateSID, current, nil
		}
	}

	return "", Session{}, errSIDCollision
}

// Get returns a session when sid is well-formed, persisted, and not expired.
// Malformed, missing, or expired SIDs return ErrNotFound; an expired record is
// deleted first. A session at or beyond ExpiresAt-(ttl-refreshInterval) is
// renewed to ExpiresAt=now+ttl through an expiry compare-and-swap; when the
// CAS misses, the authoritative persisted record is reread and returned.
func (s *Store) Get(ctx context.Context, sid string) (current Session, renewed bool, err error) {
	if !validSID(sid) {
		return Session{}, false, ErrNotFound
	}

	now := s.clock.Now()
	digest := hashSID(sid)
	session, err := s.repository.GetSession(ctx, digest)
	if errors.Is(err, ErrNotFound) {
		return Session{}, false, ErrNotFound
	}
	if err != nil {
		return Session{}, false, err
	}
	if !now.Before(session.ExpiresAt) {
		if err := s.repository.RevokeSession(ctx, digest); err != nil {
			return Session{}, false, err
		}
		return Session{}, false, ErrNotFound
	}
	if !now.Before(session.ExpiresAt.Add(-(s.ttl - s.refreshInterval))) {
		newExpiresAt := now.Add(s.ttl)
		updated, err := s.repository.RefreshSession(ctx, digest, session.ExpiresAt, newExpiresAt)
		if err != nil {
			return Session{}, false, err
		}
		if updated {
			session.ExpiresAt = newExpiresAt
			return session, true, nil
		}
		session, err = s.repository.GetSession(ctx, digest)
		if errors.Is(err, ErrNotFound) {
			return Session{}, false, ErrNotFound
		}
		if err != nil {
			return Session{}, false, err
		}
		if !now.Before(session.ExpiresAt) {
			if err := s.repository.RevokeSession(ctx, digest); err != nil {
				return Session{}, false, err
			}
			return Session{}, false, ErrNotFound
		}
		return session, false, nil
	}
	return session, false, nil
}

// AuthorizeLogin applies the shared bounded per-address login failure policy.
func (s *Store) AuthorizeLogin(remoteAddress string, credentialsValid bool) LoginDecision {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	key := clientAddress(remoteAddress)
	s.purgeLoginAttemptsLocked(now)
	attempt, exists := s.logins[key]
	if exists && now.Before(attempt.bannedUntil) {
		attempt.touchedAt = now
		s.logins[key] = attempt
		return LoginBanned
	}
	if credentialsValid {
		delete(s.logins, key)
		return LoginAllowed
	}
	if !exists || !now.Before(attempt.windowStart.Add(loginWindow)) {
		attempt = loginAttempt{windowStart: now}
	}
	attempt.failures++
	attempt.touchedAt = now
	if attempt.failures >= loginFailureLimit {
		attempt.bannedUntil = now.Add(loginBanDuration)
	}
	if !exists && len(s.logins) == s.capacity {
		s.evictLoginAttemptLocked()
	}
	s.logins[key] = attempt
	if !attempt.bannedUntil.IsZero() {
		return LoginBanned
	}
	return LoginInvalid
}

// Revoke durably removes sid, if it is present. It is safe to call repeatedly.
func (s *Store) Revoke(ctx context.Context, sid string) error {
	return s.repository.RevokeSession(ctx, hashSID(sid))
}

// PurgeExpired removes all sessions at or beyond their absolute expiry time
// and returns how many records it removed.
func (s *Store) PurgeExpired(ctx context.Context) (int64, error) {
	return s.repository.PurgeExpiredSessions(ctx, s.clock.Now())
}

// Len returns the number of records currently retained by the store.
func (s *Store) Len(ctx context.Context) (int, error) {
	return s.repository.CountSessions(ctx)
}

func (s *Store) newToken() (string, error) {
	var token [tokenBytes]byte
	if _, err := io.ReadFull(s.random, token[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func (s *Store) purgeLoginAttemptsLocked(now time.Time) {
	for key, attempt := range s.logins {
		if !now.Before(attempt.bannedUntil) && !now.Before(attempt.windowStart.Add(loginWindow)) {
			delete(s.logins, key)
		}
	}
}

func (s *Store) evictLoginAttemptLocked() {
	var oldestKey string
	var oldest time.Time
	first := true
	for key, attempt := range s.logins {
		if first || attempt.touchedAt.Before(oldest) || (attempt.touchedAt.Equal(oldest) && key < oldestKey) {
			oldestKey = key
			oldest = attempt.touchedAt
			first = false
		}
	}
	delete(s.logins, oldestKey)
}

// hashSID digests the canonical SID text with SHA-256. The digest is the only
// representation of a SID that may be persisted.
func hashSID(sid string) Digest {
	return sha256.Sum256([]byte(sid))
}

func clientAddress(remoteAddress string) string {
	remoteAddress = strings.TrimSpace(remoteAddress)
	if host, _, err := net.SplitHostPort(remoteAddress); err == nil && host != "" {
		return host
	}
	if remoteAddress == "" {
		return "unknown"
	}
	return remoteAddress
}

func validSID(sid string) bool {
	if len(sid) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	for i := range len(sid) - 1 {
		if !isRawURLBase64Character(sid[i]) {
			return false
		}
	}
	return isFinalTokenCharacter(sid[len(sid)-1])
}

func isRawURLBase64Character(char byte) bool {
	return 'A' <= char && char <= 'Z' ||
		'a' <= char && char <= 'z' ||
		'0' <= char && char <= '9' ||
		char == '-' || char == '_'
}

func isFinalTokenCharacter(char byte) bool {
	switch char {
	case 'A', 'E', 'I', 'M', 'Q', 'U', 'Y', 'c', 'g', 'k', 'o', 's', 'w', '0', '4', '8':
		return true
	default:
		return false
	}
}
