package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/qbtkey"
	"github.com/turygo/cd211/internal/token"
)

func TestQBTAPIKeyGenerateDisplayRevokeLifecycle(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	if _, err := store.GetQBTAPIKey(ctx); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Fatalf("GetQBTAPIKey(empty) error = %v, want qbtkey.ErrNotFound", err)
	}
	first, err := store.GenerateQBTAPIKey(ctx, now)
	if err != nil {
		t.Fatalf("GenerateQBTAPIKey() error = %v", err)
	}
	if !qbtkey.Valid(first) || !strings.HasPrefix(string(first), qbtkey.Prefix) {
		t.Fatalf("generated qBittorrent API key = %q, want valid %s key", first, qbtkey.Prefix)
	}
	info, err := store.GetQBTAPIKey(ctx)
	if err != nil {
		t.Fatalf("GetQBTAPIKey() error = %v", err)
	}
	if info.Secret != first || info.RowVersion != 0 || !info.CreatedAt.Equal(now) || !info.UpdatedAt.Equal(now) {
		t.Errorf("initial key = %+v, want persisted secret and version 0", info)
	}
	if info.Hint != qbtkey.Hint(first) || !qbtkey.Verify(first, info.Digest) {
		t.Errorf("initial qBittorrent API key does not match generated key: %+v", info)
	}
	if _, err := store.GenerateQBTAPIKey(ctx, now.Add(time.Minute)); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("second generate error = %v, want qbtkey.ErrConflict", err)
	}
	if err := store.RevokeQBTAPIKey(ctx, 0); err != nil {
		t.Fatalf("RevokeQBTAPIKey() error = %v", err)
	}
	if _, err := store.GetQBTAPIKey(ctx); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Fatalf("GetQBTAPIKey() after revoke error = %v, want qbtkey.ErrNotFound", err)
	}
	if err := store.RevokeQBTAPIKey(ctx, 0); err != nil {
		t.Errorf("revoke absent error = %v, want idempotent success", err)
	}
	second, err := store.GenerateQBTAPIKey(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("generate after revoke error = %v", err)
	}
	current, err := store.GetQBTAPIKey(ctx)
	if err != nil {
		t.Fatalf("GetQBTAPIKey() after regenerate: %v", err)
	}
	if current.Secret != second || current.RowVersion <= info.RowVersion {
		t.Errorf("regenerated key = %+v, want new persisted secret and monotonic version", current)
	}
	if err := store.RevokeQBTAPIKey(ctx, info.RowVersion); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("stale revoke error = %v, want qbtkey.ErrConflict", err)
	}
}

func TestQBTAPIKeyPersistsSecretAndIsIndependent(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	qbtSecret, err := store.GenerateQBTAPIKey(ctx, now)
	if err != nil {
		t.Fatalf("GenerateQBTAPIKey() error = %v", err)
	}
	nativeSecret, err := store.GenerateAPIToken(ctx, now)
	if err != nil {
		t.Fatalf("GenerateAPIToken() error = %v", err)
	}
	if token.Valid(token.Secret(qbtSecret)) {
		t.Error("qBittorrent API key was accepted by native token shape validation")
	}
	if token.Valid(nativeSecret) && qbtkey.Valid(qbtkey.Secret(nativeSecret)) {
		t.Error("native token was accepted by qBittorrent API key shape validation")
	}

	var (
		rawHash   []byte
		rawSecret string
		hint      string
	)
	if err := store.db.QueryRowContext(ctx, "SELECT key_hash, key_secret, key_hint FROM qbt_api_key WHERE id = 1").Scan(&rawHash, &rawSecret, &hint); err != nil {
		t.Fatalf("read qbt_api_key row: %v", err)
	}
	if rawSecret != string(qbtSecret) {
		t.Errorf("stored key_secret = %q, want generated key", rawSecret)
	}
	if bytes.Equal(rawHash, []byte(qbtSecret)) {
		t.Fatal("qbt_api_key stored plaintext in key_hash")
	}
	if !bytes.Equal(rawHash, qbtkey.Hash(qbtSecret)) {
		t.Error("stored key_hash is not the SHA-256 digest of the qBittorrent API key")
	}
	if hint != qbtkey.Hint(qbtSecret) {
		t.Errorf("stored key_hint = %q, want %q", hint, qbtkey.Hint(qbtSecret))
	}

	if err := store.RevokeQBTAPIKey(ctx, 0); err != nil {
		t.Fatalf("RevokeQBTAPIKey() error = %v", err)
	}
	if _, err := store.GetAPIToken(ctx); err != nil {
		t.Fatalf("native token unavailable after qBittorrent key revoke: %v", err)
	}
}
