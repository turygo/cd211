package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/settings"
)

func setupValues() map[string]string {
	return map[string]string{
		settings.KeyCD2Address:     "cd2.example:443",
		settings.KeyCD2Username:    "admin",
		settings.KeyCD2Password:    "horse staple 9",
		settings.KeyCD2Insecure:    "true",
		settings.KeyCloudRoot:      "/cloud",
		settings.KeyLocalRoot:      "/downloads",
		settings.KeyOfflineTimeout: "24h",
		settings.KeyCopyTimeout:    "72h",
		settings.KeyVerifyTimeout:  "10m",
	}
}

func TestCompleteSetupWritesEverythingAtomically(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	if err := store.CompleteSetup(ctx, "hash-1", setupValues(), now); err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}

	values, err := store.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings() error = %v", err)
	}
	for key, want := range setupValues() {
		if got := values[key]; got != want {
			t.Errorf("setting %s = %q, want %q", key, got, want)
		}
	}
	if got := values[settings.KeySetupCompletedAt]; got != "2026-08-09T12:00:00Z" {
		t.Errorf("setup.completed_at = %q, want RFC3339 UTC time", got)
	}

	hash, err := store.GetOperatorPasswordHash(ctx)
	if err != nil || hash != "hash-1" {
		t.Fatalf("GetOperatorPasswordHash() = (%q, %v), want hash-1", hash, err)
	}
}

func TestCompleteSetupRejectsSecondCallWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if err := store.CompleteSetup(ctx, "hash-1", setupValues(), now); err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}

	replacement := setupValues()
	replacement[settings.KeyCD2Address] = "other.example:443"
	later := now.Add(time.Hour)
	if err := store.CompleteSetup(ctx, "hash-2", replacement, later); !errors.Is(err, ErrSetupCompleted) {
		t.Fatalf("second CompleteSetup() = %v, want ErrSetupCompleted", err)
	}

	values, err := store.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings() error = %v", err)
	}
	for key, want := range setupValues() {
		if got := values[key]; got != want {
			t.Errorf("setting %s = %q after rejected setup, want %q", key, got, want)
		}
	}
	if got := values[settings.KeySetupCompletedAt]; got != "2026-08-09T12:00:00Z" {
		t.Errorf("setup.completed_at = %q after rejected setup, want unchanged", got)
	}
	hash, err := store.GetOperatorPasswordHash(ctx)
	if err != nil || hash != "hash-1" {
		t.Fatalf("GetOperatorPasswordHash() = (%q, %v) after rejected setup, want hash-1", hash, err)
	}
}

func TestCompleteSetupGuardsOnPreExistingPasswordRow(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if err := store.SetOperatorPasswordHash(ctx, "hash-1", now); err != nil {
		t.Fatalf("SetOperatorPasswordHash() error = %v", err)
	}

	if err := store.CompleteSetup(ctx, "hash-2", setupValues(), now); !errors.Is(err, ErrSetupCompleted) {
		t.Fatalf("CompleteSetup() with password row = %v, want ErrSetupCompleted", err)
	}
	values, err := store.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings() error = %v", err)
	}
	if len(values) != 0 {
		t.Errorf("ListSettings() = %v after rejected setup, want empty", values)
	}
}

func TestCompleteSetupRejectsEmptyHash(t *testing.T) {
	store := openTestStore(t)
	if err := store.CompleteSetup(context.Background(), "", setupValues(), time.Now()); err == nil {
		t.Fatal("CompleteSetup(empty hash) error = nil, want error")
	}
}

func TestReplaceSettingsDoesNotTouchCompletionOrPassword(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if err := store.CompleteSetup(ctx, "hash-1", setupValues(), now); err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}

	update := map[string]string{
		settings.KeyCD2Address:  "new.example:443",
		settings.KeyCD2Insecure: "false",
	}
	later := now.Add(time.Hour)
	if err := store.ReplaceSettings(ctx, update, later); err != nil {
		t.Fatalf("ReplaceSettings() error = %v", err)
	}

	values, err := store.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings() error = %v", err)
	}
	if got := values[settings.KeyCD2Address]; got != "new.example:443" {
		t.Errorf("cd2.address = %q, want updated value", got)
	}
	if got := values[settings.KeyCD2Insecure]; got != "false" {
		t.Errorf("cd2.insecure = %q, want updated value", got)
	}
	for _, key := range []string{
		settings.KeyCD2Username, settings.KeyCD2Password, settings.KeyCloudRoot, settings.KeyLocalRoot,
		settings.KeyOfflineTimeout, settings.KeyCopyTimeout, settings.KeyVerifyTimeout,
	} {
		if got, want := values[key], setupValues()[key]; got != want {
			t.Errorf("setting %s = %q, want untouched %q", key, got, want)
		}
	}
	if got := values[settings.KeySetupCompletedAt]; got != "2026-08-09T12:00:00Z" {
		t.Errorf("setup.completed_at = %q after ReplaceSettings, want untouched", got)
	}
	hash, err := store.GetOperatorPasswordHash(ctx)
	if err != nil || hash != "hash-1" {
		t.Fatalf("GetOperatorPasswordHash() = (%q, %v) after ReplaceSettings, want hash-1", hash, err)
	}
}

func TestReplaceSettingsRejectsZeroTime(t *testing.T) {
	store := openTestStore(t)
	err := store.ReplaceSettings(context.Background(), setupValues(), time.Time{})
	if err == nil {
		t.Fatal("ReplaceSettings(zero time) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "time") {
		t.Fatalf("ReplaceSettings() error = %v, want time error", err)
	}
}
