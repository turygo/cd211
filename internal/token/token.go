// Package token implements the single global Automation API token: plaintext
// generation, SHA-256 hashing, display hints, and constant-time verification.
// The plaintext is persisted so the authenticated Settings page can display it
// on every visit.
package token

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

// Prefix is the fixed human-readable prefix of every API token.
const Prefix = "cd211_api_"

// SecretBytes is the number of crypto-random bytes in the token payload.
const SecretBytes = 32

// SecretLength is the exact plaintext length of a well-formed token: Prefix
// plus 43 unpadded base64url characters encoding SecretBytes.
const SecretLength = len(Prefix) + 43

// HintSuffix is the number of final characters of the complete token shown in
// the display hint.
const HintSuffix = 6

// Sentinel errors owned by the token contract so consumers never need to
// import the store package.
var (
	// ErrNotFound is returned when no API token row is configured.
	ErrNotFound = errors.New("token: API token is not configured")
	// ErrConflict is returned when a lifecycle transition fails its invariant:
	// generating while a token exists, or a revoke whose expected row version
	// no longer matches the stored row.
	ErrConflict = errors.New("token: API token state changed")
)

// Secret is a plaintext API token. It is persisted so the authenticated
// Settings page can display the configured token on every visit.
type Secret string

// Token is the durable single API token row. Secret is persisted for Settings
// display and Digest is used for constant-time request verification. Secret is
// empty only for rows created before persistent token display was introduced.
type Token struct {
	Secret     Secret
	Digest     []byte
	Hint       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	RowVersion int64
}

// Repository is the durable single-row API token boundary consumed by the Web
// UI and the native API auth middleware. *store.Store implements it.
type Repository interface {
	GetAPIToken(context.Context) (Token, error)
	GenerateAPIToken(context.Context, time.Time) (Secret, error)
	RevokeAPIToken(context.Context, int64) error
}

// Generate returns a fresh token: Prefix plus base64.RawURLEncoding of
// SecretBytes crypto-random bytes.
func Generate() (Secret, error) {
	raw := make([]byte, SecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("token: crypto/rand unavailable: %w", err)
	}
	return Secret(Prefix + base64.RawURLEncoding.EncodeToString(raw)), nil
}

// Hash returns the SHA-256 digest of the secret.
func Hash(secret Secret) []byte {
	digest := sha256.Sum256([]byte(secret))
	return digest[:]
}

// Hint renders the display hint: the fixed prefix, the literal Unicode
// ellipsis, and the final HintSuffix characters of the complete token.
func Hint(secret Secret) string {
	if !Valid(secret) {
		return ""
	}
	return Prefix + "…" + string(secret[len(secret)-HintSuffix:])
}

// Valid reports whether the secret has the exact token shape: the fixed
// prefix, the exact length, and a base64url payload that decodes to exactly
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
