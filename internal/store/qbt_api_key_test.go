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

func TestQBTAPIKeyGenerateRotateRevokeLifecycle(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	if _, err := store.GetQBTAPIKey(ctx); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Fatalf("GetQBTAPIKey(empty) error = %v, want qbtkey.ErrNotFound", err)
	}
	if _, err := store.RotateQBTAPIKey(ctx, 0, now); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Fatalf("RotateQBTAPIKey(missing) error = %v, want qbtkey.ErrNotFound", err)
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
		t.Errorf("initial metadata = %+v, want version 0 and created=updated=now", info)
	}
	if info.Hint != qbtkey.Hint(first) || !qbtkey.Verify(first, info.Digest) {
		t.Errorf("initial qBittorrent API key metadata does not match generated key: %+v", info)
	}

	if _, err := store.GenerateQBTAPIKey(ctx, now.Add(time.Minute)); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("second generate error = %v, want qbtkey.ErrConflict", err)
	}

	second, err := store.RotateQBTAPIKey(ctx, info.RowVersion, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("RotateQBTAPIKey() error = %v", err)
	}
	if second == first || !qbtkey.Valid(second) {
		t.Fatalf("rotated qBittorrent API key = %q, want a fresh valid key", second)
	}
	rotated, err := store.GetQBTAPIKey(ctx)
	if err != nil {
		t.Fatalf("GetQBTAPIKey() after rotate error = %v", err)
	}
	if rotated.RowVersion != 1 || !rotated.CreatedAt.Equal(now) || !rotated.UpdatedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("rotated metadata = %+v, want preserved created_at and bumped version", rotated)
	}
	if qbtkey.Verify(first, rotated.Digest) || !qbtkey.Verify(second, rotated.Digest) {
		t.Error("rotation did not immediately invalidate old key and verify new key")
	}
	if _, err := store.RotateQBTAPIKey(ctx, info.RowVersion, now.Add(2*time.Hour)); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("stale rotate error = %v, want qbtkey.ErrConflict", err)
	}

	if err := store.RevokeQBTAPIKey(ctx, rotated.RowVersion); err != nil {
		t.Fatalf("RevokeQBTAPIKey() error = %v", err)
	}
	if _, err := store.GetQBTAPIKey(ctx); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Fatalf("GetQBTAPIKey() after revoke error = %v, want qbtkey.ErrNotFound", err)
	}
	if err := store.RevokeQBTAPIKey(ctx, rotated.RowVersion); err != nil {
		t.Errorf("revoke absent error = %v, want idempotent success", err)
	}
	if _, err := store.RotateQBTAPIKey(ctx, rotated.RowVersion, now.Add(3*time.Hour)); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Errorf("rotate after revoke error = %v, want qbtkey.ErrNotFound", err)
	}

	generated, err := store.GenerateQBTAPIKey(ctx, now.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("generate after revoke error = %v", err)
	}
	fresh, err := store.GetQBTAPIKey(ctx)
	if err != nil {
		t.Fatalf("GetQBTAPIKey() before stale revoke error = %v", err)
	}
	if fresh.RowVersion <= rotated.RowVersion {
		t.Fatalf("regenerated row version = %d, want greater than revoked version %d", fresh.RowVersion, rotated.RowVersion)
	}
	if _, err := store.RotateQBTAPIKey(ctx, rotated.RowVersion, now.Add(5*time.Hour)); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("pre-revoke stale rotate error = %v, want qbtkey.ErrConflict", err)
	}
	if err := store.RevokeQBTAPIKey(ctx, rotated.RowVersion); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("pre-revoke stale revoke error = %v, want qbtkey.ErrConflict", err)
	}
	if _, err := store.RotateQBTAPIKey(ctx, fresh.RowVersion, now.Add(5*time.Hour)); err != nil {
		t.Fatalf("rotate generated key error = %v", err)
	}
	if err := store.RevokeQBTAPIKey(ctx, fresh.RowVersion); !errors.Is(err, qbtkey.ErrConflict) {
		t.Errorf("stale revoke error = %v, want qbtkey.ErrConflict", err)
	}
	current, err := store.GetQBTAPIKey(ctx)
	if err != nil {
		t.Fatalf("stale revoke removed qBittorrent API key: %v", err)
	}
	if qbtkey.Verify(generated, current.Digest) {
		t.Error("generated qBittorrent API key still verifies after rotation")
	}
}

func TestQBTAPIKeyPersistsDigestOnlyAndIsIndependent(t *testing.T) {
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

	var rawHash []byte
	var hint string
	if err := store.db.QueryRowContext(ctx, "SELECT key_hash, key_hint FROM qbt_api_key WHERE id = 1").Scan(&rawHash, &hint); err != nil {
		t.Fatalf("read qbt_api_key row: %v", err)
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
	var rowCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM qbt_api_key").Scan(&rowCount); err != nil {
		t.Fatalf("count qbt_api_key rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("qbt_api_key rows = %d, want exactly 1", rowCount)
	}

	// Revoking the qBittorrent key must not affect the independent native token.
	if err := store.RevokeQBTAPIKey(ctx, 0); err != nil {
		t.Fatalf("RevokeQBTAPIKey() error = %v", err)
	}
	if _, err := store.GetAPIToken(ctx); err != nil {
		t.Fatalf("native token unavailable after qBittorrent key revoke: %v", err)
	}
	if _, err := store.GetQBTAPIKey(ctx); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Fatalf("GetQBTAPIKey() after independent revoke error = %v, want qbtkey.ErrNotFound", err)
	}
}
