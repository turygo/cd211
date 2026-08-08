package creds

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryStore struct {
	hash      string
	updatedAt time.Time
	getErr    error
	setErr    error
}

func (m *memoryStore) GetOperatorPasswordHash(context.Context) (string, error) {
	return m.hash, m.getErr
}

func (m *memoryStore) SetOperatorPasswordHash(_ context.Context, hash string, now time.Time) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.hash, m.updatedAt = hash, now
	return nil
}

func TestVerifyDefaultAndChangedPassword(t *testing.T) {
	store := &memoryStore{}
	manager, err := New(store)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		username, password string
		want               bool
	}{
		{Username, DefaultPassword, true},
		{Username, "wrong", false},
		{"root", DefaultPassword, false},
	}
	for _, item := range cases {
		ok, err := manager.Verify(ctx, item.username, item.password)
		if err != nil || ok != item.want {
			t.Errorf("Verify(%q, %q) = (%t, %v), want (%t, nil)", item.username, item.password, ok, err, item.want)
		}
	}

	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if err := manager.Change(ctx, "wrong", "horse staple 9", now); !errors.Is(err, ErrCurrentPasswordMismatch) {
		t.Fatalf("Change(wrong current) = %v, want ErrCurrentPasswordMismatch", err)
	}
	if store.hash != "" {
		t.Fatal("failed change persisted a hash")
	}

	if err := manager.Change(ctx, DefaultPassword, "horse staple 9", now); err != nil {
		t.Fatalf("Change() = %v", err)
	}
	if !strings.HasPrefix(store.hash, hashScheme+"$") || strings.Contains(store.hash, "horse staple 9") {
		t.Fatalf("stored hash %q is not an opaque %s record", store.hash, hashScheme)
	}
	if !store.updatedAt.Equal(now) {
		t.Fatalf("updatedAt = %v, want %v", store.updatedAt, now)
	}

	ok, err := manager.Verify(ctx, Username, "horse staple 9")
	if err != nil || !ok {
		t.Fatalf("Verify(new) = (%t, %v), want (true, nil)", ok, err)
	}
	ok, err = manager.Verify(ctx, Username, DefaultPassword)
	if err != nil || ok {
		t.Fatalf("Verify(default after change) = (%t, %v), want (false, nil)", ok, err)
	}

	// A second change must prove the current (changed) password.
	if err := manager.Change(ctx, DefaultPassword, "another pass 1", now); !errors.Is(err, ErrCurrentPasswordMismatch) {
		t.Fatalf("Change(stale current) = %v, want ErrCurrentPasswordMismatch", err)
	}
}

func TestVerifyRejectsCorruptHashRecords(t *testing.T) {
	for _, corrupt := range []string{
		"plaintext",
		"bcrypt$10$abc$def",
		hashScheme + "$notanumber$c2FsdA$aGFzaA",
		hashScheme + "$600000$!!!$aGFzaA",
		hashScheme + "$600000$c2FsdA$!!!",
	} {
		manager, err := New(&memoryStore{hash: corrupt})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		if ok, err := manager.Verify(context.Background(), Username, DefaultPassword); err == nil || ok {
			t.Errorf("Verify(corrupt %q) = (%t, %v), want error", corrupt, ok, err)
		}
	}
}

func TestNewRequiresStore(t *testing.T) {
	if manager, err := New(nil); err == nil || manager != nil {
		t.Fatalf("New(nil) = (%v, %v), want error", manager, err)
	}
}
