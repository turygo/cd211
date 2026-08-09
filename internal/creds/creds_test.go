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

func TestVerifyWithoutPasswordRowFails(t *testing.T) {
	manager, err := New(&memoryStore{})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	ctx := context.Background()

	for _, item := range []struct {
		username, password string
	}{
		{Username, ""},
		{Username, "anything"},
		{"root", "anything"},
	} {
		ok, err := manager.Verify(ctx, item.username, item.password)
		if err != nil || ok {
			t.Errorf("Verify(%q, %q) = (%t, %v), want (false, nil)", item.username, item.password, ok, err)
		}
	}
}

func TestVerifyAndChangePassword(t *testing.T) {
	store := &memoryStore{}
	manager, err := New(store)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	// No password has been set, so there is nothing to prove a change with.
	if err := manager.Change(ctx, "anything", "horse staple 9", now); !errors.Is(err, ErrCurrentPasswordMismatch) {
		t.Fatalf("Change(no password) = %v, want ErrCurrentPasswordMismatch", err)
	}

	// The setup path seeds the hash directly.
	initial, err := HashPassword("horse staple 9")
	if err != nil {
		t.Fatalf("HashPassword(): %v", err)
	}
	if err := store.SetOperatorPasswordHash(ctx, initial, now); err != nil {
		t.Fatalf("SetOperatorPasswordHash(): %v", err)
	}

	ok, err := manager.Verify(ctx, Username, "horse staple 9")
	if err != nil || !ok {
		t.Fatalf("Verify(initial) = (%t, %v), want (true, nil)", ok, err)
	}
	ok, err = manager.Verify(ctx, Username, "wrong")
	if err != nil || ok {
		t.Fatalf("Verify(wrong) = (%t, %v), want (false, nil)", ok, err)
	}
	ok, err = manager.Verify(ctx, "root", "horse staple 9")
	if err != nil || ok {
		t.Fatalf("Verify(wrong user) = (%t, %v), want (false, nil)", ok, err)
	}

	if err := manager.Change(ctx, "wrong", "another pass 1", now); !errors.Is(err, ErrCurrentPasswordMismatch) {
		t.Fatalf("Change(wrong current) = %v, want ErrCurrentPasswordMismatch", err)
	}
	if store.hash != initial {
		t.Fatal("failed change replaced the stored hash")
	}

	if err := manager.Change(ctx, "horse staple 9", "another pass 1", now); err != nil {
		t.Fatalf("Change() = %v", err)
	}
	if !strings.HasPrefix(store.hash, hashScheme+"$") || strings.Contains(store.hash, "another pass 1") {
		t.Fatalf("stored hash %q is not an opaque %s record", store.hash, hashScheme)
	}
	if !store.updatedAt.Equal(now) {
		t.Fatalf("updatedAt = %v, want %v", store.updatedAt, now)
	}

	ok, err = manager.Verify(ctx, Username, "another pass 1")
	if err != nil || !ok {
		t.Fatalf("Verify(changed) = (%t, %v), want (true, nil)", ok, err)
	}
	ok, err = manager.Verify(ctx, Username, "horse staple 9")
	if err != nil || ok {
		t.Fatalf("Verify(stale password) = (%t, %v), want (false, nil)", ok, err)
	}

	// A second change must prove the current (changed) password.
	if err := manager.Change(ctx, "horse staple 9", "third pass 2", now); !errors.Is(err, ErrCurrentPasswordMismatch) {
		t.Fatalf("Change(stale current) = %v, want ErrCurrentPasswordMismatch", err)
	}
}

func TestHashPasswordProducesVerifiableRecords(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword(): %v", err)
	}
	if !strings.HasPrefix(encoded, hashScheme+"$") {
		t.Fatalf("HashPassword() = %q, want %s scheme", encoded, hashScheme)
	}
	ok, err := verifyHash(encoded, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verifyHash(matching) = (%t, %v), want (true, nil)", ok, err)
	}
	ok, err = verifyHash(encoded, "wrong")
	if err != nil || ok {
		t.Fatalf("verifyHash(mismatch) = (%t, %v), want (false, nil)", ok, err)
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
		if ok, err := manager.Verify(context.Background(), Username, "horse staple 9"); err == nil || ok {
			t.Errorf("Verify(corrupt %q) = (%t, %v), want error", corrupt, ok, err)
		}
	}
}

func TestNewRequiresStore(t *testing.T) {
	if manager, err := New(nil); err == nil || manager != nil {
		t.Fatalf("New(nil) = (%v, %v), want error", manager, err)
	}
}
