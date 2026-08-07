// Package session provides bounded in-memory authentication and session state.
package session

import (
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
	errNonPositiveTTL = errors.New("session: ttl must be positive")
	errNonPositiveCap = errors.New("session: capacity must be positive")
	errSIDCollision   = errors.New("session: sid collision retries exhausted")
)

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

// Store holds revocable sessions in process memory.
type Store struct {
	mu       sync.Mutex
	sessions map[string]Session
	logins   map[string]loginAttempt
	random   io.Reader
	clock    Clock
	ttl      time.Duration
	capacity int
}

// New constructs a bounded in-memory session store.
func New(clock Clock, random io.Reader, ttl time.Duration, capacity int) (*Store, error) {
	if isNil(clock) {
		return nil, errNilClock
	}
	if isNil(random) {
		return nil, errNilRandom
	}
	if ttl <= 0 {
		return nil, errNonPositiveTTL
	}
	if capacity <= 0 {
		return nil, errNonPositiveCap
	}

	return &Store{
		sessions: make(map[string]Session),
		logins:   make(map[string]loginAttempt),
		random:   random,
		clock:    clock,
		ttl:      ttl,
		capacity: capacity,
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

// Create generates a new session ID and an independent CSRF token.
func (s *Store) Create() (sid string, session Session, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	s.purgeExpiredLocked(now)

	for range 3 {
		candidateSID, err := s.newToken()
		if err != nil {
			return "", Session{}, err
		}
		csrfToken, err := s.newToken()
		if err != nil {
			return "", Session{}, err
		}
		if _, exists := s.sessions[candidateSID]; exists {
			continue
		}

		if len(s.sessions) == s.capacity {
			s.evictOneLocked()
		}

		session = Session{
			CSRFToken: csrfToken,
			CreatedAt: now,
			ExpiresAt: now.Add(s.ttl),
		}
		s.sessions[candidateSID] = session
		return candidateSID, session, nil
	}

	return "", Session{}, errSIDCollision
}

// Get returns a session when sid is well-formed and has not expired.
func (s *Store) Get(sid string) (Session, bool) {
	if !validSID(sid) {
		return Session{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sid]
	if !exists {
		return Session{}, false
	}
	if !s.clock.Now().Before(session.ExpiresAt) {
		delete(s.sessions, sid)
		return Session{}, false
	}
	return session, true
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

// Revoke removes sid, if it is present. It is safe to call repeatedly.
func (s *Store) Revoke(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sid)
}

// PurgeExpired removes all sessions at or beyond their absolute expiry time
// and returns how many records it removed.
func (s *Store) PurgeExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.purgeExpiredLocked(s.clock.Now())
}

// Len returns the number of records currently retained by the store.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Store) newToken() (string, error) {
	var token [tokenBytes]byte
	if _, err := io.ReadFull(s.random, token[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func (s *Store) purgeExpiredLocked(now time.Time) int {
	removed := 0
	for sid, session := range s.sessions {
		if !now.Before(session.ExpiresAt) {
			delete(s.sessions, sid)
			removed++
		}
	}
	return removed
}

func (s *Store) evictOneLocked() {
	var evictSID string
	var evictSession Session
	first := true
	for sid, session := range s.sessions {
		if first || sessionExpiresFirst(sid, session, evictSID, evictSession) {
			evictSID = sid
			evictSession = session
			first = false
		}
	}
	delete(s.sessions, evictSID)
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

func sessionExpiresFirst(sid string, session Session, otherSID string, other Session) bool {
	if !session.ExpiresAt.Equal(other.ExpiresAt) {
		return session.ExpiresAt.Before(other.ExpiresAt)
	}
	if !session.CreatedAt.Equal(other.CreatedAt) {
		return session.CreatedAt.Before(other.CreatedAt)
	}
	return sid < otherSID
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
