package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestQBTPreferencesPersistAcrossStoreReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "qbt.sqlite")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceSettings(ctx, map[string]string{"qbt.add_trackers": "https://tracker.example/announce", "qbt.add_trackers_enabled": "true"}, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	settings, err := repo.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["qbt.add_trackers"] != "https://tracker.example/announce" || settings["qbt.add_trackers_enabled"] != "true" {
		t.Fatalf("settings = %#v", settings)
	}
}
