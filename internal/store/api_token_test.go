package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/qbtkey"
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
	if info.RowVersion != 0 || !info.CreatedAt.Equal(now) || !info.UpdatedAt.Equal(now) {
		t.Errorf("initial token = %+v, want metadata and version 0", info)
	}
	if info.Hint != "cd211_api_…"+string(first[len(first)-6:]) {
		t.Errorf("hint = %q, want prefix + ellipsis + final 6 characters", info.Hint)
	}
	if !token.Verify(first, info.Digest) {
		t.Error("generated token does not verify against the stored digest")
	}
	if info.Secret != first {
		t.Error("stored token does not retain the generated secret")
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
	if current.Secret != second || current.Secret == first || !token.Verify(second, current.Digest) || token.Verify(first, current.Digest) {
		t.Error("regenerated token must expose and authenticate only the new secret")
	}
	if current.RowVersion != 0 {
		t.Errorf("regenerated token = %+v, want new metadata and version 0", current)
	}
	if err := store.RevokeAPIToken(ctx, 1); !errors.Is(err, token.ErrConflict) {
		t.Errorf("stale revoke error = %v, want token.ErrConflict", err)
	}
}

func TestAPISecretsPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "secrets.sqlite")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	nativeSecret, err := store.GenerateAPIToken(ctx, now)
	if err != nil {
		t.Fatalf("GenerateAPIToken() error = %v", err)
	}
	qbtSecret, err := store.GenerateQBTAPIKey(ctx, now)
	if err != nil {
		t.Fatalf("GenerateQBTAPIKey() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	nativeInfo, err := reopened.GetAPIToken(ctx)
	if err != nil {
		t.Fatalf("GetAPIToken() after reopen: %v", err)
	}
	if nativeInfo.Secret != nativeSecret || !token.Verify(nativeInfo.Secret, nativeInfo.Digest) {
		t.Error("reopened token does not recover and authenticate the generated secret")
	}
	qbtInfo, err := reopened.GetQBTAPIKey(ctx)
	if err != nil {
		t.Fatalf("GetQBTAPIKey() after reopen: %v", err)
	}
	if qbtInfo.Secret != qbtSecret || !qbtkey.Verify(qbtInfo.Secret, qbtInfo.Digest) {
		t.Error("reopened qBittorrent key does not recover and authenticate the generated secret")
	}
	for _, info := range []any{nativeInfo, qbtInfo} {
		data, err := json.Marshal(info)
		if err != nil {
			t.Fatalf("marshal credential metadata: %v", err)
		}
		if bytes.Contains(data, []byte(nativeSecret)) || bytes.Contains(data, []byte(qbtSecret)) {
			t.Error("credential metadata JSON exposes a plaintext secret")
		}
	}
}

func TestAPISecretsMigrationPreservesLegacyAuthentication(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-secrets.sqlite")
	db := openDatabaseAtMigration(t, databasePath, 18)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	nativeSecret, err := token.Generate()
	if err != nil {
		t.Fatal(err)
	}
	qbtSecret, err := qbtkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO api_token (id, token_hash, token_hint, created_at, updated_at, row_version)
		VALUES (1, ?, ?, ?, ?, 4)
	`, token.Hash(nativeSecret), token.Hint(nativeSecret), now, now); err != nil {
		t.Fatalf("insert legacy token: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO qbt_api_key (id, key_hash, key_hint, created_at, updated_at, row_version)
		VALUES (1, ?, ?, ?, ?, 7)
	`, qbtkey.Hash(qbtSecret), qbtkey.Hint(qbtSecret), now, now); err != nil {
		t.Fatalf("insert legacy qBittorrent key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	nativeInfo, err := store.GetAPIToken(ctx)
	if err != nil {
		t.Fatalf("GetAPIToken() after migration: %v", err)
	}
	if nativeInfo.Secret != "" || !token.Verify(nativeSecret, nativeInfo.Digest) || nativeInfo.RowVersion != 4 {
		t.Error("migration must preserve legacy token authentication and version without inventing a secret")
	}
	qbtInfo, err := store.GetQBTAPIKey(ctx)
	if err != nil {
		t.Fatalf("GetQBTAPIKey() after migration: %v", err)
	}
	if qbtInfo.Secret != "" || !qbtkey.Verify(qbtSecret, qbtInfo.Digest) || qbtInfo.RowVersion != 7 {
		t.Error("migration must preserve legacy qBittorrent authentication and version without inventing a secret")
	}
}
