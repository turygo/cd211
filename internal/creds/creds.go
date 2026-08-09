// Package creds owns the fixed operator identity and the changeable,
// durably hashed operator password shared by the Web UI and the
// qBittorrent-compatible API.
package creds

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Username is the fixed operator account name.
const Username = "admin"

// ErrCurrentPasswordMismatch reports a password change whose current-password
// proof failed.
var ErrCurrentPasswordMismatch = errors.New("current password does not match")

// PBKDF2-SHA256 parameters (OWASP 2023 recommendation).
const (
	hashIterations = 600_000
	hashKeyLength  = 32
	hashSaltLength = 16
	hashScheme     = "pbkdf2-sha256"
)

// Store persists the single operator password hash.
type Store interface {
	// GetOperatorPasswordHash returns "" while no password has been set.
	GetOperatorPasswordHash(ctx context.Context) (string, error)
	SetOperatorPasswordHash(ctx context.Context, hash string, now time.Time) error
}

// Manager verifies operator credentials and applies password changes.
type Manager struct {
	store Store
}

// New creates a Manager over the durable password store.
func New(store Store) (*Manager, error) {
	if store == nil {
		return nil, errors.New("creds store is nil")
	}
	return &Manager{store: store}, nil
}

// Verify reports whether the supplied credentials match the fixed username
// and the currently active password. With no stored password hash,
// verification fails.
func (m *Manager) Verify(ctx context.Context, username, password string) (bool, error) {
	usernameInput := sha256.Sum256([]byte(username))
	usernameFixed := sha256.Sum256([]byte(Username))
	usernameMatch := subtle.ConstantTimeCompare(usernameInput[:], usernameFixed[:]) == 1

	encoded, err := m.store.GetOperatorPasswordHash(ctx)
	if err != nil {
		return false, err
	}
	var passwordMatch bool
	if encoded != "" {
		passwordMatch, err = verifyHash(encoded, password)
		if err != nil {
			return false, err
		}
	}
	return usernameMatch && passwordMatch, nil
}

// Change replaces the operator password after proving the current one.
func (m *Manager) Change(ctx context.Context, current, next string, now time.Time) error {
	ok, err := m.Verify(ctx, Username, current)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCurrentPasswordMismatch
	}
	encoded, err := HashPassword(next)
	if err != nil {
		return err
	}
	return m.store.SetOperatorPasswordHash(ctx, encoded, now)
}

// HashPassword derives an opaque PBKDF2-SHA256 hash record from a password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, hashSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, hashKeyLength)
	if err != nil {
		return "", fmt.Errorf("derive password hash: %w", err)
	}
	return strings.Join([]string{
		hashScheme,
		strconv.Itoa(hashIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

func verifyHash(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false, fmt.Errorf("unsupported password hash format %q", parts[0])
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return false, errors.New("invalid password hash iteration count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false, errors.New("invalid password hash salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, errors.New("invalid password hash key")
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false, fmt.Errorf("derive password hash: %w", err)
	}
	return subtle.ConstantTimeCompare(key, expected) == 1, nil
}
