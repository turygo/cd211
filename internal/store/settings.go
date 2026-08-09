package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/turygo/cd211/internal/settings"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
)

// ErrSetupCompleted reports that setup has already been completed.
var ErrSetupCompleted = errors.New("setup already completed")

// ListSettings returns every persisted setting keyed by its name.
func (s *Store) ListSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.queries.ListSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	return values, nil
}

// ReplaceSettings atomically upserts exactly the given settings in one
// transaction, never touching setup.completed_at or operator_password.
func (s *Store) ReplaceSettings(ctx context.Context, values map[string]string, now time.Time) error {
	if now.IsZero() {
		return errors.New("settings update time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin settings replace: %w", err)
	}
	queries := s.queries.WithTx(tx)
	for key, value := range values {
		if err := queries.UpsertSetting(ctx, storedb.UpsertSettingParams{Key: key, Value: value, UpdatedAt: now}); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("replace settings: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit settings replace: %w", err)
	}
	return nil
}

// CompleteSetup atomically records the operator password hash, the given
// settings, and setup completion in one transaction. It fails with
// ErrSetupCompleted when an operator password or setup.completed_at already
// exists.
func (s *Store) CompleteSetup(ctx context.Context, passwordHash string, values map[string]string, now time.Time) error {
	if passwordHash == "" {
		return errors.New("operator password hash is empty")
	}
	if now.IsZero() {
		return errors.New("setup completion time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin setup completion: %w", err)
	}
	queries := s.queries.WithTx(tx)
	finish := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("rollback setup completion: %w", rollbackErr)
		}
		return cause
	}

	if _, err := queries.GetOperatorPassword(ctx); err == nil {
		return finish(ErrSetupCompleted)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return finish(fmt.Errorf("read operator password: %w", err))
	}

	rows, err := queries.ListSettings(ctx)
	if err != nil {
		return finish(fmt.Errorf("read settings: %w", err))
	}
	for _, row := range rows {
		if row.Key == settings.KeySetupCompletedAt {
			return finish(ErrSetupCompleted)
		}
	}

	if err := queries.UpsertOperatorPassword(ctx, storedb.UpsertOperatorPasswordParams{
		PasswordHash: passwordHash, UpdatedAt: now,
	}); err != nil {
		return finish(fmt.Errorf("set operator password: %w", err))
	}
	for key, value := range values {
		if err := queries.UpsertSetting(ctx, storedb.UpsertSettingParams{Key: key, Value: value, UpdatedAt: now}); err != nil {
			return finish(fmt.Errorf("upsert setup setting: %w", err))
		}
	}
	completedAt := now.UTC().Format(time.RFC3339)
	if err := queries.UpsertSetting(ctx, storedb.UpsertSettingParams{
		Key: settings.KeySetupCompletedAt, Value: completedAt, UpdatedAt: now,
	}); err != nil {
		return finish(fmt.Errorf("record setup completion: %w", err))
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit setup completion: %w", err)
	}
	return nil
}
