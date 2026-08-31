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

type Audience string

const (
	AudienceWeb Audience = "web"
	AudienceQBT Audience = "qbt"
)

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
	CreateSession(ctx context.Context, digest Digest, current Session, now time.Time, capacity int) (inserted bool, err error)
	GetSession(ctx context.Context, digest Digest, audience Audience) (Session, error)
	RefreshSession(ctx context.Context, digest Digest, audience Audience, expectedExpiresAt, newExpiresAt time.Time) (updated bool, err error)
	RevokeSession(ctx context.Context, digest Digest, audience Audience) error
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
	Audience  Audience
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

// Create generates a new session ID and persists the record through the
// repository. Web sessions receive an independent CSRF token; qB sessions do
// not need one. The plaintext SID is returned exactly once.
func (s *Store) Create(ctx context.Context, audience Audience) (sid string, current Session, err error) {
	if audience != AudienceWeb && audience != AudienceQBT {
		return "", Session{}, errors.New("session: invalid audience")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	for range 3 {
		candidateSID, err := s.newToken()
		if err != nil {
			return "", Session{}, err
		}
		csrfToken := ""
		if audience == AudienceWeb {
			csrfToken, err = s.newToken()
			if err != nil {
				return "", Session{}, err
			}
		}
		current = Session{
			Audience:  audience,
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

// Get returns a session when sid is well-formed, persisted for the requested
// audience, and not expired.
func (s *Store) Get(ctx context.Context, sid string, audience Audience) (current Session, renewed bool, err error) {
	if audience != AudienceWeb && audience != AudienceQBT {
		return Session{}, false, ErrNotFound
	}
	if !validSID(sid) {
		return Session{}, false, ErrNotFound
	}

	now := s.clock.Now()
	digest := hashSID(sid)
	current, err = s.repository.GetSession(ctx, digest, audience)
	if errors.Is(err, ErrNotFound) {
		return Session{}, false, ErrNotFound
	}
	if err != nil {
		return Session{}, false, err
	}
	if !now.Before(current.ExpiresAt) {
		if err := s.repository.RevokeSession(ctx, digest, audience); err != nil {
			return Session{}, false, err
		}
		return Session{}, false, ErrNotFound
	}
	if !now.Before(current.ExpiresAt.Add(-(s.ttl - s.refreshInterval))) {
		newExpiresAt := now.Add(s.ttl)
		updated, err := s.repository.RefreshSession(ctx, digest, audience, current.ExpiresAt, newExpiresAt)
		if err != nil {
			return Session{}, false, err
		}
		if updated {
			current.ExpiresAt = newExpiresAt
			return current, true, nil
		}
		current, err = s.repository.GetSession(ctx, digest, audience)
		if errors.Is(err, ErrNotFound) {
			return Session{}, false, ErrNotFound
		}
		if err != nil {
			return Session{}, false, err
		}
		if !now.Before(current.ExpiresAt) {
			if err := s.repository.RevokeSession(ctx, digest, audience); err != nil {
				return Session{}, false, err
			}
			return Session{}, false, ErrNotFound
		}
	}
	return current, false, nil
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

// Revoke durably removes sid for the requested audience, if it is present.
// It is safe to call repeatedly.
func (s *Store) Revoke(ctx context.Context, sid string, audience Audience) error {
	if audience != AudienceWeb && audience != AudienceQBT {
		return ErrNotFound
	}
	return s.repository.RevokeSession(ctx, hashSID(sid), audience)
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
