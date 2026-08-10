package outbox

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidCursor is returned when a delivery history cursor cannot be
// decoded. The Web UI pre-validates cursors with DecodeCursor and renders a
// 400 for this error.
var ErrInvalidCursor = errors.New("outbox: invalid delivery cursor")

// EncodeCursor packs a delivery's UTC created_at and numeric ID into an
// opaque URL-safe base64 cursor.
func EncodeCursor(createdAt time.Time, id int64) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor unpacks a delivery cursor produced by EncodeCursor.
func DecodeCursor(value string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	text := string(raw)
	separator := strings.LastIndexByte(text, ':')
	if separator <= 0 || separator == len(text)-1 {
		return time.Time{}, 0, ErrInvalidCursor
	}
	createdAt, err := time.Parse(time.RFC3339Nano, text[:separator])
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	id, err := strconv.ParseInt(text[separator+1:], 10, 64)
	if err != nil || id < 0 {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return createdAt.UTC(), id, nil
}
