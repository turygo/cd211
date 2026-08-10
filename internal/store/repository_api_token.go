package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	storedb "github.com/turygo/cd211/internal/store/sqlc"
	"github.com/turygo/cd211/internal/token"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Compile-time check that the store satisfies the token persistence contract.
var _ token.Repository = (*Store)(nil)

// GetAPIToken returns the metadata of the single configured API token row.
// The plaintext secret and its digest are never exposed to ordinary readers;
// Digest is populated for the auth middleware's constant-time verification.
func (s *Store) GetAPIToken(ctx context.Context) (token.Token, error) {
	row, err := s.queries.GetAPIToken(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return token.Token{}, token.ErrNotFound
	}
	if err != nil {
		return token.Token{}, fmt.Errorf("get API token: %w", err)
	}
	return apiTokenFromDB(row)
}

// GenerateAPIToken creates the single API token row and returns the plaintext
// secret exactly once. It is only valid while no token exists: an existing row
// is a conflict, never a silent replacement, so a concurrent generate has
// exactly one winner.
func (s *Store) GenerateAPIToken(ctx context.Context, now time.Time) (token.Secret, error) {
	if now.IsZero() {
		return "", errors.New("API token generation time is required")
	}
	secret, err := token.Generate()
	if err != nil {
		return "", err
	}
	err = s.queries.InsertAPIToken(ctx, storedb.InsertAPITokenParams{
		TokenHash: token.Hash(secret),
		TokenHint: token.Hint(secret),
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	})
	if err != nil {
		if tokenRowConflict(err) {
			return "", token.ErrConflict
		}
		return "", fmt.Errorf("insert API token: %w", err)
	}
	return secret, nil
}

// RotateAPIToken replaces the token value of the single row under a CAS on
// row_version, preserving created_at and bumping updated_at and row_version.
// The old token becomes invalid the moment the update commits. A missing row
// is ErrNotFound; a row whose version moved since the form was rendered is
// ErrConflict and the operator must reload Settings.
func (s *Store) RotateAPIToken(ctx context.Context, expectedVersion int64, now time.Time) (token.Secret, error) {
	if expectedVersion < 0 || now.IsZero() {
		return "", errors.New("API token version or rotation time is invalid")
	}
	secret, err := token.Generate()
	if err != nil {
		return "", err
	}
	updated, err := s.queries.UpdateAPIToken(ctx, storedb.UpdateAPITokenParams{
		TokenHash:          token.Hash(secret),
		TokenHint:          token.Hint(secret),
		UpdatedAt:          now.UTC(),
		ExpectedRowVersion: expectedVersion,
	})
	if err != nil {
		return "", fmt.Errorf("rotate API token: %w", err)
	}
	if updated == 0 {
		if _, err := s.queries.GetAPIToken(ctx); errors.Is(err, sql.ErrNoRows) {
			return "", token.ErrNotFound
		} else if err != nil {
			return "", fmt.Errorf("read API token after rotate miss: %w", err)
		}
		return "", token.ErrConflict
	}
	return secret, nil
}

// RevokeAPIToken deletes the single row under a CAS on row_version. Deleting
// an already-absent token is idempotent; a stale version on an existing row
// is ErrConflict and the operator must reload Settings. After revocation the
// API is disabled until a new token is generated.
func (s *Store) RevokeAPIToken(ctx context.Context, expectedVersion int64) error {
	if expectedVersion < 0 {
		return errors.New("API token version is invalid")
	}
	updated, err := s.queries.DeleteAPIToken(ctx, expectedVersion)
	if err != nil {
		return fmt.Errorf("revoke API token: %w", err)
	}
	if updated == 0 {
		if _, err := s.queries.GetAPIToken(ctx); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read API token after revoke miss: %w", err)
		}
		return token.ErrConflict
	}
	return nil
}

func apiTokenFromDB(row storedb.ApiToken) (token.Token, error) {
	if len(row.TokenHash) != 32 || row.TokenHint == "" || row.CreatedAt.IsZero() ||
		row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) || row.RowVersion < 0 {
		return token.Token{}, errors.New("stored API token is invalid")
	}
	return token.Token{
		Digest:     row.TokenHash,
		Hint:       row.TokenHint,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
		RowVersion: row.RowVersion,
	}, nil
}

func tokenRowConflict(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
