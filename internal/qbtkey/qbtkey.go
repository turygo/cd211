// Package qbtkey implements the single global qBittorrent WebUI API key:
// plaintext generation, SHA-256 hashing, display hints, and constant-time
// verification. Only the digest of a key is ever persisted; the plaintext
// exists only in the one-time generate/rotate response and in-memory request
// authentication.
package qbtkey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Prefix is the fixed human-readable prefix of every qBittorrent API key.
const Prefix = "qbt_"

// SecretBytes is the number of crypto-random bytes in the key payload.
const SecretBytes = 32

// SecretLength is the exact plaintext length of a well-formed key: Prefix plus
// 43 unpadded base64url characters encoding SecretBytes.
const SecretLength = len(Prefix) + 43

// HintSuffix is the number of final characters of the complete key shown in
// the display hint.
const HintSuffix = 6

// Sentinel errors owned by the qBittorrent API key contract so consumers never
// need to import the store package.
var (
	// ErrNotFound is returned when no qBittorrent API key row is configured.
	ErrNotFound = errors.New("qbtkey: qBittorrent API key is not configured")
	// ErrConflict is returned when a qBittorrent API key lifecycle transition
	// fails its invariant.
	ErrConflict = errors.New("qbtkey: qBittorrent API key state changed")
)

// Secret is a plaintext qBittorrent API key. It must never be persisted,
// logged, placed in a session, or carried in a redirect.
type Secret string

// Key is the durable metadata of the single qBittorrent API key row. Digest is
// the SHA-256 of the plaintext secret, which is never stored in plaintext.
type Key struct {
	Digest     []byte
	Hint       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	RowVersion int64
}

// Repository is the durable single-row qBittorrent API key boundary consumed
// by WebUI lifecycle operations and API authentication. *store.Store
// implements it.
type Repository interface {
	GetQBTAPIKey(context.Context) (Key, error)
	GenerateQBTAPIKey(context.Context, time.Time) (Secret, error)
	RotateQBTAPIKey(context.Context, int64, time.Time) (Secret, error)
	RevokeQBTAPIKey(context.Context, int64) error
}

// Generate returns a fresh qBittorrent API key: Prefix plus base64.RawURLEncoding
// of SecretBytes crypto-random bytes.
func Generate() (Secret, error) {
	raw := make([]byte, SecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("qBittorrent API key: crypto/rand unavailable: %w", err)
	}
	return Secret(Prefix + base64.RawURLEncoding.EncodeToString(raw)), nil
}

// Hash returns the SHA-256 digest of the secret.
func Hash(secret Secret) []byte {
	digest := sha256.Sum256([]byte(secret))
	return digest[:]
}

// Hint renders the display hint: the fixed prefix, a literal Unicode ellipsis,
// and the final HintSuffix characters of the complete key.
func Hint(secret Secret) string {
	if !Valid(secret) {
		return ""
	}
	return Prefix + "…" + string(secret[len(secret)-HintSuffix:])
}

// Valid reports whether the secret has the exact qBittorrent API key shape:
// the fixed prefix, exact length, and a base64url payload decoding to exactly
// SecretBytes. It never depends on the secret's randomness.
func Valid(secret Secret) bool {
	if len(secret) != SecretLength || !strings.HasPrefix(string(secret), Prefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(secret[len(Prefix):]))
	if err != nil || len(raw) != SecretBytes {
		return false
	}
	return true
}

// Verify reports whether the secret matches the stored digest. Malformed
// secrets and wrong-length digests are rejected before any comparison; a
// well-formed secret is compared against the digest in constant time.
func Verify(secret Secret, digest []byte) bool {
	if !Valid(secret) || len(digest) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(Hash(secret), digest) == 1
}
