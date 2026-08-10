package outbox

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

func TestEncodeDecodeCursor(t *testing.T) {
	createdAt := time.Date(2026, 8, 6, 12, 30, 45, 123456789, time.UTC)
	for _, id := range []int64{0, 1, 999999999999} {
		cursor := EncodeCursor(createdAt, id)
		if cursor == "" {
			t.Fatalf("EncodeCursor(%v, %d) is empty", createdAt, id)
		}
		gotTime, gotID, err := DecodeCursor(cursor)
		if err != nil {
			t.Fatalf("DecodeCursor(%q) error = %v", cursor, err)
		}
		if !gotTime.Equal(createdAt) || gotID != id {
			t.Errorf("DecodeCursor(%q) = (%v, %d), want (%v, %d)", cursor, gotTime, gotID, createdAt, id)
		}
	}
}

func TestDecodeCursorRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{
		"",
		"not-base64!!",
		base64.RawURLEncoding.EncodeToString([]byte("no-separator")),
		base64.RawURLEncoding.EncodeToString([]byte(":123")),
		base64.RawURLEncoding.EncodeToString([]byte("2026-08-06T12:00:00Z:")),
		base64.RawURLEncoding.EncodeToString([]byte("not-a-time:1")),
		base64.RawURLEncoding.EncodeToString([]byte("2026-08-06T12:00:00Z:abc")),
		base64.RawURLEncoding.EncodeToString([]byte("2026-08-06T12:00:00Z:-1")),
	} {
		if _, _, err := DecodeCursor(value); err == nil {
			t.Errorf("DecodeCursor(%q) error = nil, want error", value)
		}
	}
}

func TestBuildDownloadPayload(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	completedAt := now.Add(time.Minute)
	download := domain.Download{
		Hash: "0123456789abcdef0123456789abcdef01234567", Name: "release",
		Category: "movies", State: domain.StateCompleted, ContentPath: "/downloads/release",
		TotalSize: 4096, UpdatedAt: completedAt, CreatedAt: now, CompletedAt: &completedAt,
		RowVersion: 17, QbitProgress: 1, OfflineProgress: 1, CopyProgress: 1,
	}
	payload, err := BuildDownloadPayload("evt_0123456789abcdef0123456789abcdef", EventTypeCompleted, domain.StateVerifyingLocal, download)
	if err != nil {
		t.Fatalf("BuildDownloadPayload() error = %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if envelope["id"] != "evt_0123456789abcdef0123456789abcdef" || envelope["type"] != EventTypeCompleted || envelope["schema_version"] != float64(1) {
		t.Errorf("envelope = %v, want id/type/schema_version", envelope)
	}
	if _, ok := envelope["occurred_at"]; !ok {
		t.Errorf("envelope lacks occurred_at")
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %v", envelope["data"])
	}
	want := map[string]any{
		"hash": download.Hash, "name": "release", "category": "movies", "state": "COMPLETED",
		"previous_state": "VERIFYING_LOCAL", "progress": float64(1), "content_path": "/downloads/release",
		"total_size": float64(4096), "error": "", "download_version": float64(17),
	}
	for key, value := range want {
		if data[key] != value {
			t.Errorf("data[%q] = %v, want %v", key, data[key], value)
		}
	}
	if _, ok := data["completed_at"]; !ok {
		t.Errorf("completed_at must be present for completed events")
	}

	t.Run("failed event redacts and omits completed_at", func(t *testing.T) {
		failed := download
		failed.State = domain.StateFailed
		failed.QbitProgress = 0.4
		failed.CompletedAt = nil
		failed.LastError = "tracker passkey=secret rejected"
		payload, err := BuildDownloadPayload("evt_f", EventTypeFailed, domain.StateVerifyingLocal, failed)
		if err != nil {
			t.Fatalf("BuildDownloadPayload(failed) error = %v", err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("failed payload is not JSON: %v", err)
		}
		data := envelope["data"].(map[string]any)
		if data["error"] != domain.RedactedErrorText {
			t.Errorf("data[error] = %v, want %q", data["error"], domain.RedactedErrorText)
		}
		if _, ok := data["completed_at"]; ok {
			t.Errorf("completed_at must be omitted when nil")
		}
	})

	t.Run("created event has empty previous state", func(t *testing.T) {
		created := download
		created.State = domain.StateAccepted
		created.CompletedAt = nil
		payload, err := BuildDownloadPayload("evt_c", EventTypeCreated, "", created)
		if err != nil {
			t.Fatalf("BuildDownloadPayload(created) error = %v", err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("created payload is not JSON: %v", err)
		}
		data := envelope["data"].(map[string]any)
		if data["previous_state"] != "" {
			t.Errorf("data[previous_state] = %v, want empty", data["previous_state"])
		}
	})
}

func TestBuildTestPayload(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	payload, err := BuildTestPayload("evt_t", 42, "alerts", now)
	if err != nil {
		t.Fatalf("BuildTestPayload() error = %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("test payload is not JSON: %v", err)
	}
	if envelope["type"] != EventTypeTest || envelope["schema_version"] != float64(1) {
		t.Errorf("envelope = %v, want type webhook.test and schema version 1", envelope)
	}
	data := envelope["data"].(map[string]any)
	if data["endpoint_id"] != float64(42) || data["endpoint_name"] != "alerts" || data["message"] != TestMessage {
		t.Errorf("data = %v, want endpoint_id 42, endpoint_name alerts, message %q", data, TestMessage)
	}
}

func TestValidateEndpointInput(t *testing.T) {
	valid := EndpointInput{
		Name: "alerts", URL: "https://example.com/hook?token=visible-in-query",
		SubscribeCompleted: true,
	}
	if err := ValidateEndpointInput(valid); err != nil {
		t.Fatalf("ValidateEndpointInput(valid) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*EndpointInput)
	}{
		{"empty name", func(in *EndpointInput) { in.Name = "  " }},
		{"name too long", func(in *EndpointInput) { in.Name = strings.Repeat("x", 65) }},
		{"name control character", func(in *EndpointInput) { in.Name = "bad\nname" }},
		{"relative url", func(in *EndpointInput) { in.URL = "/hook" }},
		{"non-http scheme", func(in *EndpointInput) { in.URL = "ftp://example.com/hook" }},
		{"userinfo", func(in *EndpointInput) { in.URL = "https://user:pass@example.com/hook" }},
		{"fragment", func(in *EndpointInput) { in.URL = "https://example.com/hook#frag" }},
		{"no host", func(in *EndpointInput) { in.URL = "https://" }},
		{"url too long", func(in *EndpointInput) { in.URL = "https://example.com/" + strings.Repeat("x", 2048) }},
		{"no subscription", func(in *EndpointInput) { in.SubscribeCompleted = false; in.SubscribeFailed = false }},
		{"bearer too long", func(in *EndpointInput) { in.BearerToken = strings.Repeat("x", 4097) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if err := ValidateEndpointInput(input); err == nil {
				t.Errorf("ValidateEndpointInput(%+v) error = nil, want error", input)
			}
		})
	}
}

func TestDisplayURL(t *testing.T) {
	if got := DisplayURL("https://example.com/hook?key=secret&x=1"); got != "https://example.com/hook?…" {
		t.Errorf("DisplayURL(with query) = %q, want redacted marker", got)
	}
	if got := DisplayURL("https://example.com/hook"); got != "https://example.com/hook" {
		t.Errorf("DisplayURL(no query) = %q, want unchanged", got)
	}
}

func TestDeliveryDelay(t *testing.T) {
	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 16 * time.Minute, 32 * time.Minute,
		time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour,
	}
	for index, expected := range want {
		if got := DeliveryDelay(int64(index + 1)); got != expected {
			t.Errorf("DeliveryDelay(%d) = %v, want %v", index+1, got, expected)
		}
	}
	if got := DeliveryDelay(0); got != 30*time.Second {
		t.Errorf("DeliveryDelay(0) = %v, want 30s", got)
	}
	if got := DeliveryDelay(100); got != 6*time.Hour {
		t.Errorf("DeliveryDelay(100) = %v, want 6h cap", got)
	}
}

func TestNewEventIDAndHMACSecret(t *testing.T) {
	eventID := NewEventID()
	if !strings.HasPrefix(eventID, "evt_") || len(eventID) != 4+32 {
		t.Errorf("NewEventID() = %q, want evt_ plus 32 hex chars", eventID)
	}
	lower := eventID[4:]
	for _, character := range lower {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			t.Errorf("NewEventID() contains non-lowercase-hex char %q", character)
		}
	}
	first := NewHMACSecret()
	second := NewHMACSecret()
	if first == second || len(first) != 43 { // 32 bytes base64url
		t.Errorf("NewHMACSecret() = %q / %q, want two distinct 43-char values", first, second)
	}
}
