package outbox

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

// DownloadData is the immutable data envelope of download domain events. JSON
// field names are part of the outbound contract and must not change.
type downloadData struct {
	Hash            string  `json:"hash"`
	Name            string  `json:"name"`
	Category        string  `json:"category"`
	State           string  `json:"state"`
	PreviousState   string  `json:"previous_state"`
	Progress        float64 `json:"progress"`
	ContentPath     string  `json:"content_path"`
	TotalSize       int64   `json:"total_size"`
	Error           string  `json:"error"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	CompletedAt     *string `json:"completed_at,omitempty"`
	DownloadVersion int64   `json:"download_version"`
}

type downloadEnvelope struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	SchemaVersion int          `json:"schema_version"`
	OccurredAt    string       `json:"occurred_at"`
	Data          downloadData `json:"data"`
}

type testData struct {
	EndpointID   int64  `json:"endpoint_id"`
	EndpointName string `json:"endpoint_name"`
	Message      string `json:"message"`
}

type testEnvelope struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	SchemaVersion int      `json:"schema_version"`
	OccurredAt    string   `json:"occurred_at"`
	Data          testData `json:"data"`
}

// BuildDownloadPayload serializes the immutable payload for a download domain
// event. It is called once at mutation time from the post-update row and is
// never rebuilt from later download state.
func BuildDownloadPayload(eventID, eventType string, previousState domain.State, download domain.Download) ([]byte, error) {
	errorText := ""
	if eventType == EventTypeFailed {
		errorText = domain.SanitizeDownloadError(download)
	}
	data := downloadData{
		Hash:            download.Hash,
		Name:            download.Name,
		Category:        download.Category,
		State:           string(download.State),
		PreviousState:   string(previousState),
		Progress:        downloadProgress(download),
		ContentPath:     download.ContentPath,
		TotalSize:       download.TotalSize,
		Error:           errorText,
		CreatedAt:       formatTime(download.CreatedAt),
		UpdatedAt:       formatTime(download.UpdatedAt),
		DownloadVersion: download.RowVersion,
	}
	if download.CompletedAt != nil {
		value := formatTime(*download.CompletedAt)
		data.CompletedAt = &value
	}
	payload, err := json.Marshal(downloadEnvelope{
		ID:            eventID,
		Type:          eventType,
		SchemaVersion: EventSchemaVersion,
		OccurredAt:    formatTime(download.UpdatedAt),
		Data:          data,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	return payload, nil
}

// BuildTestPayload serializes a durable webhook.test event payload. The event
// is not a download domain event; it is targeted only at one endpoint.
func BuildTestPayload(eventID string, endpointID int64, endpointName string, occurredAt time.Time) ([]byte, error) {
	payload, err := json.Marshal(testEnvelope{
		ID:            eventID,
		Type:          EventTypeTest,
		SchemaVersion: EventSchemaVersion,
		OccurredAt:    formatTime(occurredAt),
		Data: testData{
			EndpointID:   endpointID,
			EndpointName: endpointName,
			Message:      TestMessage,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal webhook.test payload: %w", err)
	}
	return payload, nil
}

// downloadProgress mirrors the qBittorrent projection where it applies and
// falls back to qbit progress for states the projection cannot map.
func downloadProgress(download domain.Download) float64 {
	if projection, err := domain.Project(download); err == nil {
		return projection.Progress
	}
	if download.QbitProgress < 0 {
		return 0
	}
	if download.QbitProgress > 1 {
		return 1
	}
	return download.QbitProgress
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// NewEventID returns an event ID: "evt_" plus 32 lowercase hex chars from 16
// crypto-random bytes.
func NewEventID() string {
	raw := cryptoBytes(16)
	return "evt_" + hex.EncodeToString(raw)
}

// NewHMACSecret returns a 32-byte secret encoded with base64.RawURLEncoding.
func NewHMACSecret() string {
	return base64.RawURLEncoding.EncodeToString(cryptoBytes(32))
}

func cryptoBytes(size int) []byte {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("outbox: crypto/rand unavailable: %v", err))
	}
	return raw
}

// DeliveryDelay returns the deterministic retry delay after the given 1-based
// attempt: 30s, 1m, 2m, 4m, 8m, 16m, 32m, 1h, 2h, 4h, 6h, then a 6h cap.
// There is no jitter.
func DeliveryDelay(attempt int64) time.Duration {
	delays := [...]time.Duration{
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
		32 * time.Minute,
		time.Hour,
		2 * time.Hour,
		4 * time.Hour,
		6 * time.Hour,
	}
	if attempt < 1 {
		attempt = 1
	}
	index := int(attempt - 1)
	if index >= len(delays) {
		index = len(delays) - 1
	}
	return delays[index]
}
