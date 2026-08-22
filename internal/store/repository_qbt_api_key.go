package store

import (
	"bytes"
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

// GetQBTAPIKey returns the configured qBittorrent API key, including its
// persisted plaintext for the authenticated Settings page and its digest for
// request verification.
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

// GenerateQBTAPIKey creates the single qBittorrent API key row and persists
// the generated secret so Settings can display it on every visit. An inactive
// tombstone is reactivated with a new key after revocation.
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
		KeySecret: string(secret),
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
		KeySecret: string(secret),
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
	if row.KeySecret != "" {
		secret := qbtkey.Secret(row.KeySecret)
		if !qbtkey.Valid(secret) || !bytes.Equal(qbtkey.Hash(secret), row.KeyHash) {
			return qbtkey.Key{}, errors.New("stored qBittorrent API key secret is invalid")
		}
	}
	return qbtkey.Key{
		Secret:     qbtkey.Secret(row.KeySecret),
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
