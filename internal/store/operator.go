package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	storedb "github.com/turygo/cd211/internal/store/sqlc"
)

// GetOperatorPasswordHash returns the persisted operator password hash, or ""
// when no password has been set.
func (s *Store) GetOperatorPasswordHash(ctx context.Context) (string, error) {
	hash, err := s.queries.GetOperatorPassword(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get operator password: %w", err)
	}
	return hash, nil
}

// SetOperatorPasswordHash durably replaces the operator password hash.
func (s *Store) SetOperatorPasswordHash(ctx context.Context, hash string, now time.Time) error {
	if hash == "" {
		return errors.New("operator password hash is empty")
	}
	err := s.queries.UpsertOperatorPassword(ctx, storedb.UpsertOperatorPasswordParams{
		PasswordHash: hash, UpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("set operator password: %w", err)
	}
	return nil
}
