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

func TestAPITokenGenerateDisplayRevokeLifecycle(t *testing.T) {
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
	if info.Secret != first || info.RowVersion != 0 || !info.CreatedAt.Equal(now) || !info.UpdatedAt.Equal(now) {
		t.Errorf("initial token = %+v, want persisted secret and version 0", info)
	}
	if info.Hint != "cd211_api_…"+string(first[len(first)-6:]) {
		t.Errorf("hint = %q, want prefix + ellipsis + final 6 characters", info.Hint)
	}
	if !token.Verify(first, info.Digest) {
		t.Error("generated token does not verify against the stored digest")
	}

	if _, err := store.GenerateAPIToken(ctx, now.Add(time.Minute)); !errors.Is(err, token.ErrConflict) {
		t.Errorf("second generate error = %v, want token.ErrConflict", err)
	}
	if err := store.RevokeAPIToken(ctx, 0); err != nil {
		t.Fatalf("RevokeAPIToken() error = %v", err)
	}
	if _, err := store.GetAPIToken(ctx); !errors.Is(err, token.ErrNotFound) {
		t.Fatalf("GetAPIToken() after revoke error = %v, want token.ErrNotFound", err)
	}
	if err := store.RevokeAPIToken(ctx, 0); err != nil {
		t.Errorf("revoke absent error = %v, want idempotent success", err)
	}

	second, err := store.GenerateAPIToken(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("generate after revoke error = %v", err)
	}
	current, err := store.GetAPIToken(ctx)
	if err != nil {
		t.Fatalf("GetAPIToken() after regenerate: %v", err)
	}
	if current.Secret != second || current.RowVersion != 0 {
		t.Errorf("regenerated token = %+v, want new persisted secret and version 0", current)
	}
	if err := store.RevokeAPIToken(ctx, 1); !errors.Is(err, token.ErrConflict) {
		t.Errorf("stale revoke error = %v, want token.ErrConflict", err)
	}
}

func TestAPITokenPersistsSecretAndDigest(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	secret, err := store.GenerateAPIToken(ctx, now)
	if err != nil {
		t.Fatalf("GenerateAPIToken() error = %v", err)
	}
	var (
		rawHash   []byte
		rawSecret string
		hint      string
	)
	if err := store.db.QueryRowContext(ctx, "SELECT token_hash, token_secret, token_hint FROM api_token WHERE id = 1").Scan(&rawHash, &rawSecret, &hint); err != nil {
		t.Fatalf("read api_token row: %v", err)
	}
	if rawSecret != string(secret) {
		t.Errorf("stored token_secret = %q, want generated secret", rawSecret)
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
}
