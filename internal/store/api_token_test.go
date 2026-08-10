package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/token"
)

func TestAPITokenGenerateRotateRevokeLifecycle(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	if _, err := store.GetAPIToken(ctx); !errors.Is(err, token.ErrNotFound) {
		t.Fatalf("GetAPIToken(empty) error = %v, want token.ErrNotFound", err)
	}

	first, err := store.GenerateAPIToken(ctx, now)
	if err != nil {
		t.Fatalf("GenerateAPIToken() error = %v", err)
	}
	if !token.Valid(first) || !strings.HasPrefix(string(first), token.Prefix) {
		t.Fatalf("generated token = %q, want a valid %s token", first, token.Prefix)
	}

	info, err := store.GetAPIToken(ctx)
	if err != nil {
		t.Fatalf("GetAPIToken() error = %v", err)
	}
	if info.RowVersion != 0 || !info.CreatedAt.Equal(now) || !info.UpdatedAt.Equal(now) {
		t.Errorf("initial metadata = %+v, want version 0 and created=updated=now", info)
	}
	if info.Hint != "cd211_api_…"+string(first[len(first)-6:]) {
		t.Errorf("hint = %q, want prefix + ellipsis + final 6 characters", info.Hint)
	}
	if !token.Verify(first, info.Digest) {
		t.Error("generated token does not verify against the stored digest")
	}

	// Generate over an existing token is a conflict, never a replacement.
	if _, err := store.GenerateAPIToken(ctx, now.Add(time.Minute)); !errors.Is(err, token.ErrConflict) {
		t.Errorf("second generate error = %v, want token.ErrConflict", err)
	}

	// Rotation preserves created_at and bumps updated_at and row_version.
	second, err := store.RotateAPIToken(ctx, 0, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("RotateAPIToken() error = %v", err)
	}
	if second == first || !token.Valid(second) {
		t.Fatalf("rotated token = %q, want a fresh valid token", second)
	}
	rotated, err := store.GetAPIToken(ctx)
	if err != nil {
		t.Fatalf("GetAPIToken() after rotate error = %v", err)
	}
	if rotated.RowVersion != 1 || !rotated.CreatedAt.Equal(now) || !rotated.UpdatedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("rotated metadata = %+v, want preserved created_at and bumped version", rotated)
	}
	if token.Verify(first, rotated.Digest) {
		t.Error("old token still verifies after rotation")
	}
	if !token.Verify(second, rotated.Digest) {
		t.Error("rotated token does not verify against the stored digest")
	}

	// A stale rotate is a conflict; a rotate on a missing row is not found.
	if _, err := store.RotateAPIToken(ctx, 0, now.Add(2*time.Hour)); !errors.Is(err, token.ErrConflict) {
		t.Errorf("stale rotate error = %v, want token.ErrConflict", err)
	}
	if err := store.RevokeAPIToken(ctx, 1); err != nil {
		t.Fatalf("RevokeAPIToken() error = %v", err)
	}
	if _, err := store.GetAPIToken(ctx); !errors.Is(err, token.ErrNotFound) {
		t.Fatalf("GetAPIToken() after revoke error = %v, want token.ErrNotFound", err)
	}
	if _, err := store.RotateAPIToken(ctx, 1, now.Add(3*time.Hour)); !errors.Is(err, token.ErrNotFound) {
		t.Errorf("rotate after revoke error = %v, want token.ErrNotFound", err)
	}

	// Revoke is idempotent when absent; a stale revoke on an existing row is
	// a conflict and must not remove the token.
	if err := store.RevokeAPIToken(ctx, 1); err != nil {
		t.Errorf("revoke absent error = %v, want idempotent success", err)
	}
	if _, err := store.GenerateAPIToken(ctx, now.Add(4*time.Hour)); err != nil {
		t.Fatalf("generate after revoke error = %v", err)
	}
	generated, err := store.GetAPIToken(ctx)
	if err != nil {
		t.Fatalf("GetAPIToken() before stale revoke error = %v", err)
	}
	if generated.RowVersion != 0 {
		t.Fatalf("generated metadata = %+v, want version 0", generated)
	}
	if _, err := store.RotateAPIToken(ctx, generated.RowVersion, now.Add(5*time.Hour)); err != nil {
		t.Fatalf("rotate generated token error = %v", err)
	}
	if err := store.RevokeAPIToken(ctx, 0); !errors.Is(err, token.ErrConflict) {
		t.Errorf("stale revoke error = %v, want token.ErrConflict", err)
	}
	if _, err := store.GetAPIToken(ctx); err != nil {
		t.Errorf("stale revoke removed the token: %v", err)
	}
}

func TestAPITokenPersistsDigestOnly(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	secret, err := store.GenerateAPIToken(ctx, now)
	if err != nil {
		t.Fatalf("GenerateAPIToken() error = %v", err)
	}
	var (
		rawHash []byte
		hint    string
	)
	if err := store.db.QueryRowContext(ctx, "SELECT token_hash, token_hint FROM api_token WHERE id = 1").Scan(&rawHash, &hint); err != nil {
		t.Fatalf("read api_token row: %v", err)
	}
	if bytes.Equal(rawHash, []byte(secret)) {
		t.Fatal("api_token stored the plaintext token as token_hash")
	}
	if !bytes.Equal(rawHash, token.Hash(secret)) {
		t.Error("stored token_hash is not the SHA-256 digest of the secret")
	}
	if hint != "cd211_api_…"+string(secret[len(secret)-6:]) {
		t.Errorf("stored hint = %q, want the display hint", hint)
	}
	var rowCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_token").Scan(&rowCount); err != nil {
		t.Fatalf("count api_token rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("api_token rows = %d, want exactly 1", rowCount)
	}
}
