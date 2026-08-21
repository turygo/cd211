package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/turygo/cd211/internal/qbtkey"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Compile-time check that the store satisfies the qBittorrent API key
// persistence contract.
var _ qbtkey.Repository = (*Store)(nil)

// GetQBTAPIKey returns the metadata of the single configured qBittorrent API
// key row. The plaintext secret is never exposed to ordinary readers; Digest
// is populated for constant-time verification.
func (s *Store) GetQBTAPIKey(ctx context.Context) (qbtkey.Key, error) {
	row, err := s.queries.GetQBTAPIKey(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return qbtkey.Key{}, qbtkey.ErrNotFound
	}
	if err != nil {
		return qbtkey.Key{}, fmt.Errorf("get qBittorrent API key: %w", err)
	}
	return qbtKeyFromDB(row)
}

// GenerateQBTAPIKey creates the single qBittorrent API key row and returns the
// plaintext secret exactly once. An existing row is a conflict, never a
// silent replacement.
func (s *Store) GenerateQBTAPIKey(ctx context.Context, now time.Time) (qbtkey.Secret, error) {
	if now.IsZero() {
		return "", errors.New("qBittorrent API key generation time is required")
	}
	secret, err := qbtkey.Generate()
	if err != nil {
		return "", err
	}
	keyHash := qbtkey.Hash(secret)
	keyHint := qbtkey.Hint(secret)
	now = now.UTC()
	err = s.queries.InsertQBTAPIKey(ctx, storedb.InsertQBTAPIKeyParams{
		KeyHash:   keyHash,
		KeyHint:   keyHint,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err == nil {
		return secret, nil
	}
	if !qbtKeyRowConflict(err) {
		return "", fmt.Errorf("insert qBittorrent API key: %w", err)
	}
	updated, err := s.queries.ActivateQBTAPIKey(ctx, storedb.ActivateQBTAPIKeyParams{
		KeyHash:   keyHash,
		KeyHint:   keyHint,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return "", fmt.Errorf("reactivate qBittorrent API key: %w", err)
	}
	if updated == 0 {
		return "", qbtkey.ErrConflict
	}
	return secret, nil
}

// RotateQBTAPIKey replaces the qBittorrent API key under a CAS on
// row_version, preserving created_at and bumping updated_at and row_version.
// The old key becomes invalid when the update commits. A missing row is
// ErrNotFound; a stale version is ErrConflict.
func (s *Store) RotateQBTAPIKey(ctx context.Context, expectedVersion int64, now time.Time) (qbtkey.Secret, error) {
	if expectedVersion < 0 || now.IsZero() {
		return "", errors.New("qBittorrent API key version or rotation time is invalid")
	}
	secret, err := qbtkey.Generate()
	if err != nil {
		return "", err
	}
	updated, err := s.queries.UpdateQBTAPIKey(ctx, storedb.UpdateQBTAPIKeyParams{
		KeyHash:            qbtkey.Hash(secret),
		KeyHint:            qbtkey.Hint(secret),
		UpdatedAt:          now.UTC(),
		ExpectedRowVersion: expectedVersion,
	})
	if err != nil {
		return "", fmt.Errorf("rotate qBittorrent API key: %w", err)
	}
	if updated == 0 {
		if _, err := s.queries.GetQBTAPIKey(ctx); errors.Is(err, sql.ErrNoRows) {
			return "", qbtkey.ErrNotFound
		} else if err != nil {
			return "", fmt.Errorf("read qBittorrent API key after rotate miss: %w", err)
		}
		return "", qbtkey.ErrConflict
	}
	return secret, nil
}

// RevokeQBTAPIKey marks the single qBittorrent API key row inactive under a
// CAS on row_version. Retaining the tombstone keeps row versions monotonic
// across revoke and regenerate cycles. Revoking an inactive key is idempotent;
// a stale version on an active key is ErrConflict.
func (s *Store) RevokeQBTAPIKey(ctx context.Context, expectedVersion int64) error {
	if expectedVersion < 0 {
		return errors.New("qBittorrent API key version is invalid")
	}
	updated, err := s.queries.RevokeQBTAPIKey(ctx, expectedVersion)
	if err != nil {
		return fmt.Errorf("revoke qBittorrent API key: %w", err)
	}
	if updated == 0 {
		if _, err := s.queries.GetQBTAPIKey(ctx); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read qBittorrent API key after revoke miss: %w", err)
		}
		return qbtkey.ErrConflict
	}
	return nil
}

func qbtKeyFromDB(row storedb.QbtApiKey) (qbtkey.Key, error) {
	if row.Active != 1 || len(row.KeyHash) != 32 || row.KeyHint == "" || row.CreatedAt.IsZero() ||
		row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) || row.RowVersion < 0 {
		return qbtkey.Key{}, errors.New("stored qBittorrent API key is invalid")
	}
	return qbtkey.Key{
		Digest:     row.KeyHash,
		Hint:       row.KeyHint,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
		RowVersion: row.RowVersion,
	}, nil
}

func qbtKeyRowConflict(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
