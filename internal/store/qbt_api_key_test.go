package store

import (
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
	if info.RowVersion != 0 || !info.CreatedAt.Equal(now) || !info.UpdatedAt.Equal(now) {
		t.Errorf("initial key = %+v, want metadata and version 0", info)
	}
	if info.Hint != qbtkey.Hint(first) || !qbtkey.Verify(first, info.Digest) {
		t.Errorf("initial qBittorrent API key does not match generated key: %+v", info)
	}
	if info.Secret != first {
		t.Error("stored qBittorrent key does not retain the generated secret")
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
	var revokedSecret string
	var revokedVersion int64
	if err := store.db.QueryRowContext(ctx, "SELECT key_secret, row_version FROM qbt_api_key WHERE id = 1").Scan(&revokedSecret, &revokedVersion); err != nil {
		t.Fatalf("read revoked key tombstone: %v", err)
	}
	if revokedSecret != "" || revokedVersion <= info.RowVersion {
		t.Error("revocation must clear the saved secret and advance the tombstone version")
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
	if current.RowVersion <= revokedVersion {
		t.Errorf("regenerated key = %+v, want new metadata and monotonic version", current)
	}
	if current.Secret != second || current.Secret == first || !qbtkey.Verify(second, current.Digest) || qbtkey.Verify(first, current.Digest) {
		t.Error("regenerated qBittorrent key must expose and authenticate only the new secret")
	}
	if err := store.RevokeQBTAPIKey(ctx, info.RowVersion); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("stale revoke error = %v, want qbtkey.ErrConflict", err)
	}
}

func TestQBTAPIKeyIsIndependent(t *testing.T) {
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

	if err := store.RevokeQBTAPIKey(ctx, 0); err != nil {
		t.Fatalf("RevokeQBTAPIKey() error = %v", err)
	}
	if _, err := store.GetAPIToken(ctx); err != nil {
		t.Fatalf("native token unavailable after qBittorrent key revoke: %v", err)
	}
}
