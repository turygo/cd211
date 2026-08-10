package nativeapi

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestEventCursorRoundTripAndCanonicalEncoding(t *testing.T) {
	for _, sequence := range []int64{0, 1, math.MaxInt64} {
		sequence := sequence
		t.Run(fmt.Sprint(sequence), func(t *testing.T) {
			encoded := encodeEventCursor(sequence)
			if len(encoded) != base64.RawURLEncoding.EncodedLen(9) {
				t.Fatalf("cursor length = %d, want 12", len(encoded))
			}
			if encoded != base64.RawURLEncoding.EncodeToString(append([]byte{eventCursorVersion}, func() []byte {
				bytes := make([]byte, 8)
				binary.BigEndian.PutUint64(bytes, uint64(sequence))
				return bytes
			}()...)) {
				t.Fatalf("cursor %q is not canonical raw URL base64", encoded)
			}
			got, err := decodeEventCursor(encoded)
			if err != nil || got != sequence {
				t.Fatalf("decode = %d, %v; want %d", got, err, sequence)
			}
		})
	}
}

func TestDecodeEventCursorRejectsInvalidInputs(t *testing.T) {
	raw := make([]byte, 9)
	raw[0] = eventCursorVersion
	binary.BigEndian.PutUint64(raw[1:], math.MaxInt64)
	valid := base64.RawURLEncoding.EncodeToString(raw)
	short := base64.RawURLEncoding.EncodeToString(raw[:8])
	padBits := base64.RawURLEncoding.EncodeToString(raw[:8])[:10] + "B"
	overflow := append([]byte(nil), raw...)
	binary.BigEndian.PutUint64(overflow[1:], math.MaxUint64)
	wrongVersion := append([]byte(nil), raw...)
	wrongVersion[0] = 2
	cases := []string{"", "latest", valid + "=", "!" + valid[1:], short, valid + "A", padBits,
		base64.RawURLEncoding.EncodeToString(wrongVersion), base64.RawURLEncoding.EncodeToString(overflow),
	}
	for _, value := range cases {
		if got, err := decodeEventCursor(value); err == nil {
			t.Errorf("decode(%q) = %d, nil; want rejection", value, got)
		}
	}
}

func eventQueryRequest(raw string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/v1/events?"+raw, nil)
}

func TestParseEventsQueryDefaultsAndLatest(t *testing.T) {
	got, ok := parseEventsQuery(eventQueryRequest(""))
	if !ok || got.cursor != 0 || got.cursorLatest || !got.includeCompleted || !got.includeFailed || got.limit != 100 || got.wait != 0 {
		t.Fatalf("defaults = %#v, ok=%v", got, ok)
	}
	got, ok = parseEventsQuery(eventQueryRequest("cursor=latest"))
	if !ok || !got.cursorLatest || got.cursor != 0 {
		t.Fatalf("latest = %#v, ok=%v", got, ok)
	}
}

func TestParseEventsQueryStrictParameters(t *testing.T) {
	validHash := "ABCDEFabcdefABCDEFabcdefABCDEFabcdefABCD"
	validCursor := encodeEventCursor(1)
	cases := []struct {
		name, query string
		valid       bool
		check       func(eventsQuery) bool
	}{
		{"types trim", "types=%20download.completed%20,%20download.failed%20", true, func(q eventsQuery) bool { return q.includeCompleted && q.includeFailed }},
		{"only completed", "types=download.completed", true, func(q eventsQuery) bool { return q.includeCompleted && !q.includeFailed }},
		{"only failed", "types=download.failed", true, func(q eventsQuery) bool { return !q.includeCompleted && q.includeFailed }},
		{"hash lowercase", "hash=" + validHash, true, func(q eventsQuery) bool { return q.hash == "abcdefabcdefabcdefabcdefabcdefabcdefabcd" }},
		{"limit bounds", "limit=1", true, func(q eventsQuery) bool { return q.limit == 1 }},
		{"limit max", "limit=500", true, func(q eventsQuery) bool { return q.limit == 500 }},
		{"wait max", "wait=25s", true, func(q eventsQuery) bool { return q.wait == 25*time.Second }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, ok := parseEventsQuery(eventQueryRequest(tc.query))
			if !ok || !tc.check(q) {
				t.Fatalf("query = %#v, ok=%v", q, ok)
			}
		})
	}
	_ = validCursor
	invalid := []string{"cursor=", "cursor=latest%20", "cursor=" + validCursor + "%20", "types=", "types=download.completed,download.completed", "types=download.other", "types=download.completed,,download.failed", "hash=", "hash=" + validHash + "%20", "limit=0", "limit=501", "limit=1%20", "wait=-1s", "wait=25.000000001s", "wait=1s%20", "foo=x", "limit=1&limit=2", "cursor=" + validCursor + "&cursor=latest"}
	for _, raw := range invalid {
		if _, ok := parseEventsQuery(eventQueryRequest(raw)); ok {
			t.Errorf("accepted invalid query %q", raw)
		}
	}
}

func TestParseEventsQueryRejectsUnknownAndWhitespaceScalars(t *testing.T) {
	for _, raw := range []string{"hash=%20" + url.QueryEscape("0123456789012345678901234567890123456789"), "wait=%200s", "limit=%200"} {
		if _, ok := parseEventsQuery(eventQueryRequest(raw)); ok {
			t.Errorf("accepted %q", raw)
		}
	}
}
