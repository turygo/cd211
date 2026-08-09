package settings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		CD2Address:     "cd2.example:443",
		CD2Username:    "admin",
		CD2Password:    "horse staple 9",
		CD2Insecure:    true,
		CloudRoot:      "/115open/云下载",
		LocalRoot:      "/downloads",
		OfflineTimeout: 24 * time.Hour,
		CopyTimeout:    72 * time.Hour,
		VerifyTimeout:  10 * time.Minute,
	}
}

func completedValues(cfg Config) map[string]string {
	values := Values(cfg)
	values[KeySetupCompletedAt] = "2026-08-09T12:00:00Z"
	return values
}

type fakeStore struct {
	values map[string]string
	err    error
}

func (f *fakeStore) ListSettings(context.Context) (map[string]string, error) {
	return f.values, f.err
}

func TestParseRequiresEveryKey(t *testing.T) {
	keys := []string{
		KeyCD2Address, KeyCD2Username, KeyCD2Password, KeyCD2Insecure,
		KeyCloudRoot, KeyLocalRoot, KeyOfflineTimeout, KeyCopyTimeout, KeyVerifyTimeout,
	}
	for _, missing := range keys {
		t.Run(missing, func(t *testing.T) {
			values := Values(validConfig())
			delete(values, missing)
			_, err := Parse(values)
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("Parse(without %s) error = %v, want error naming %q", missing, err, missing)
			}
		})
	}
}

func TestParseRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{KeyCD2Address, "not-an-address"},
		{KeyCD2Address, "cd2.example:0"},
		{KeyCD2Address, ":443"},
		{KeyCD2Username, " \t "},
		{KeyCD2Password, " \t "},
		{KeyCD2Insecure, "sometimes"},
		{KeyCloudRoot, "relative/root"},
		{KeyLocalRoot, "relative/root"},
		{KeyOfflineTimeout, "0s"},
		{KeyOfflineTimeout, "not-a-duration"},
		{KeyCopyTimeout, "-1h"},
		{KeyVerifyTimeout, "0s"},
	}
	for _, test := range tests {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			values := Values(validConfig())
			values[test.key] = test.value
			_, err := Parse(values)
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Parse(%s=%q) error = %v, want error naming %q", test.key, test.value, err, test.key)
			}
		})
	}
}

func TestParseIgnoresUnknownKeys(t *testing.T) {
	values := Values(validConfig())
	values["unknown.key"] = "ignored"
	values[KeySetupCompletedAt] = "2026-08-09T12:00:00Z"

	cfg, err := Parse(values)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg != validConfig() {
		t.Fatalf("Parse() = %#v, want %#v", cfg, validConfig())
	}
}

func TestValuesParseRoundTrip(t *testing.T) {
	cfg := validConfig()
	got, err := Parse(Values(cfg))
	if err != nil {
		t.Fatalf("Parse(Values(cfg)) error = %v", err)
	}
	if got != cfg {
		t.Fatalf("round trip = %#v, want %#v", got, cfg)
	}
	if values := Values(cfg); values[KeySetupCompletedAt] != "" {
		t.Fatalf("Values() contains %q = %q, want absent", KeySetupCompletedAt, values[KeySetupCompletedAt])
	}
}

func TestValuesCanonicalSerialization(t *testing.T) {
	cfg := validConfig()
	values := Values(cfg)
	if values[KeyCD2Insecure] != "true" {
		t.Errorf("cd2.insecure = %q, want true", values[KeyCD2Insecure])
	}
	if values[KeyOfflineTimeout] != "24h0m0s" {
		t.Errorf("timeouts.offline = %q, want 24h0m0s", values[KeyOfflineTimeout])
	}
}

func TestLoadWithoutCompletion(t *testing.T) {
	store := &fakeStore{values: Values(validConfig())}
	cfg, completed, err := Load(context.Background(), store)
	if err != nil || completed || cfg != (Config{}) {
		t.Fatalf("Load() = (%#v, %t, %v), want (zero, false, nil)", cfg, completed, err)
	}
}

func TestLoadCompleted(t *testing.T) {
	store := &fakeStore{values: completedValues(validConfig())}
	cfg, completed, err := Load(context.Background(), store)
	if err != nil || !completed || cfg != validConfig() {
		t.Fatalf("Load() = (%#v, %t, %v), want (config, true, nil)", cfg, completed, err)
	}
}

func TestLoadCompletedButInvalid(t *testing.T) {
	values := completedValues(validConfig())
	values[KeyOfflineTimeout] = "0s"
	store := &fakeStore{values: values}
	cfg, completed, err := Load(context.Background(), store)
	if err == nil || completed || cfg != (Config{}) {
		t.Fatalf("Load() = (%#v, %t, %v), want error and not completed", cfg, completed, err)
	}
	if !strings.Contains(err.Error(), KeyOfflineTimeout) {
		t.Fatalf("Load() error = %v, want error naming %q", err, KeyOfflineTimeout)
	}
}

func TestLoadPropagatesStoreError(t *testing.T) {
	store := &fakeStore{err: errors.New("boom")}
	if _, _, err := Load(context.Background(), store); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Load() error = %v, want store error", err)
	}
}
