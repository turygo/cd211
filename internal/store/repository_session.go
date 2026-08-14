package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/turygo/cd211/internal/session"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
)

// Compile-time check that the store satisfies the session persistence contract.
var _ session.Repository = (*Store)(nil)

// CreateSession durably inserts one session record inside a transaction. The
// transaction first purges records at or beyond now, evicts the oldest rows
// (earliest expires_at, then created_at, then digest) until the insert leaves
// the table at or under capacity, and only then inserts. A digest collision
// returns inserted=false before any eviction and rolls the transaction back,
// so a losing create never commits unintended evictions.
func (s *Store) CreateSession(ctx context.Context, digest session.Digest, current session.Session, now time.Time, capacity int) (bool, error) {
	if capacity <= 0 {
		return false, errors.New("session capacity must be positive")
	}
	if now.IsZero() {
		return false, errors.New("session creation time is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin session create: %w", err)
	}
	queries := s.queries.WithTx(tx)
	rollback := func(cause error) (bool, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return false, fmt.Errorf("rollback session create: %w", rollbackErr)
		}
		return false, cause
	}

	if _, err := queries.GetSession(ctx, digest[:]); err == nil {
		return rollback(nil)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(fmt.Errorf("read session for create: %w", err))
	}
	if _, err := queries.PurgeExpiredSessions(ctx, now.UTC()); err != nil {
		return rollback(fmt.Errorf("purge expired sessions: %w", err))
	}
	count, err := queries.CountSessions(ctx)
	if err != nil {
		return rollback(fmt.Errorf("count sessions: %w", err))
	}
	for count >= int64(capacity) {
		if err := queries.EvictOldestSession(ctx); err != nil {
			return rollback(fmt.Errorf("evict oldest session: %w", err))
		}
		count--
	}
	if err := queries.InsertSession(ctx, storedb.InsertSessionParams{
		SidDigest: digest[:],
		CsrfToken: current.CSRFToken,
		CreatedAt: current.CreatedAt.UTC(),
		ExpiresAt: current.ExpiresAt.UTC(),
	}); err != nil {
		return rollback(fmt.Errorf("insert session: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit session create: %w", err)
	}
	return true, nil
}

// GetSession returns the persisted session for digest, validating every
// stored field. A missing record maps to session.ErrNotFound; an invalid
// record is a repository error, never an authentication miss.
func (s *Store) GetSession(ctx context.Context, digest session.Digest) (session.Session, error) {
	row, err := s.queries.GetSession(ctx, digest[:])
	if errors.Is(err, sql.ErrNoRows) {
		return session.Session{}, session.ErrNotFound
	}
	if err != nil {
		return session.Session{}, fmt.Errorf("get session: %w", err)
	}
	return sessionFromDB(row)
}

// RefreshSession extends a session's expiry under a compare-and-swap on the
// expected expires_at. Only the expiry changes; the digest, CSRF token, and
// created_at are untouched. A missing row or a stale expected expiry reports
// updated=false.
func (s *Store) RefreshSession(ctx context.Context, digest session.Digest, expectedExpiresAt, newExpiresAt time.Time) (bool, error) {
	updated, err := s.queries.RefreshSession(ctx, storedb.RefreshSessionParams{
		SidDigest:         digest[:],
		ExpectedExpiresAt: expectedExpiresAt,
		NewExpiresAt:      newExpiresAt.UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("refresh session: %w", err)
	}
	return updated == 1, nil
}

// RevokeSession durably deletes a session record. Deleting an already-absent
// record is idempotent.
func (s *Store) RevokeSession(ctx context.Context, digest session.Digest) error {
	if err := s.queries.RevokeSession(ctx, digest[:]); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// PurgeExpiredSessions deletes every record at or beyond now and returns how
// many records it removed.
func (s *Store) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	removed, err := s.queries.PurgeExpiredSessions(ctx, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("purge expired sessions: %w", err)
	}
	return removed, nil
}

// CountSessions returns the number of retained session records.
func (s *Store) CountSessions(ctx context.Context) (int, error) {
	count, err := s.queries.CountSessions(ctx)
	if err != nil {
		return 0, fmt.Errorf("count sessions: %w", err)
	}
	return int(count), nil
}

func sessionFromDB(row storedb.Session) (session.Session, error) {
	if len(row.SidDigest) != sha256.Size || row.CsrfToken == "" || row.CreatedAt.IsZero() ||
		row.ExpiresAt.IsZero() || !row.ExpiresAt.After(row.CreatedAt) {
		return session.Session{}, errors.New("stored session is invalid")
	}
	return session.Session{
		CSRFToken: row.CsrfToken,
		CreatedAt: row.CreatedAt,
		ExpiresAt: row.ExpiresAt,
	}, nil
}
