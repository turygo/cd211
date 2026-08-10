package nativeapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/outbox"
	"github.com/turygo/cd211/internal/store"
)

// HTTP contract tests for GET /api/v1/events. They drive the real harness
// and store, seed completed/failed events through the existing transition
// helpers, and assert the observable page contract: only completed/failed
// visible, strict sequence-ascending item order with the immutable nested
// envelope, items always [] never null, replayable cursors, type/hash
// filters, lookahead has_more, hidden/filtered rows advancing next_cursor to
// the store high-water, the explicit future cursor rejection, and the
// wait/long-poll behaviors (timeout final scan, commit wake, broadcast,
// cancellation, lifecycle shutdown).

// eventPage mirrors the exact /api/v1/events success contract: items is
// always a JSON array (never null), each item pairs the opaque cursor of its
// event sequence with the stored immutable envelope, and next_cursor/has_more
// describe pagination.
type eventPage struct {
	Items      []eventPageItem `json:"items"`
	NextCursor string          `json:"next_cursor"`
	HasMore    bool            `json:"has_more"`
}

type eventPageItem struct {
	Cursor string          `json:"cursor"`
	Event  json.RawMessage `json:"event"`
}

// eventEnvelope is the immutable download event payload nested inside each
// item: id/type/schema_version/occurred_at plus the full download data.
type eventEnvelope struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	Data          struct {
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
		CompletedAt     *string `json:"completed_at"`
		DownloadVersion int64   `json:"download_version"`
	} `json:"data"`
}

func decodeEventPage(t *testing.T, response *httptest.ResponseRecorder) eventPage {
	t.Helper()
	var page eventPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode event page %q: %v", response.Body.String(), err)
	}
	return page
}

func decodeEnvelope(t *testing.T, raw json.RawMessage) eventEnvelope {
	t.Helper()
	var envelope eventEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode event envelope %q: %v", string(raw), err)
	}
	return envelope
}

// listFeedEvents returns the store's authoritative completed/failed feed as
// the oracle for HTTP page assertions: sequences, immutable payload bytes,
// and stable IDs.
func listFeedEvents(t *testing.T, harness *nativeHarness) []outbox.Event {
	t.Helper()
	high, err := harness.repository.LatestEventSequence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	events, err := harness.repository.ListDownloadEvents(context.Background(), outbox.EventQuery{
		AfterSequence:    0,
		ThroughSequence:  high,
		IncludeCompleted: true,
		IncludeFailed:    true,
		Limit:            outbox.MaxEventFeedLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func (h *nativeHarness) submitDownload(t *testing.T, hash string, name string) {
	t.Helper()
	if response := h.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=" + name}); response.Code != http.StatusCreated {
		t.Fatalf("submit %s = %d %q", hash, response.Code, response.Body.String())
	}
}

// TestNativeEventsOnlyCompletedAndFailedVisible pins the feed's type filter:
// created and state_changed rows are durable history, never page content,
// while exactly the completed and failed terminal events surface.
func TestNativeEventsOnlyCompletedAndFailedVisible(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	harness.submitDownload(t, hashA, "FeedA")

	// An ACCEPTED download emits only its hidden created event (seq 1): the
	// feed is empty but next_cursor already sits at the high-water.
	page := decodeEventPage(t, harness.get(t, "/api/v1/events"))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(1) {
		t.Fatalf("feed after submit = %d items has_more %v next %q, want empty at seq 1", len(page.Items), page.HasMore, page.NextCursor)
	}

	// Six claim transitions emit five hidden state_changed rows and one
	// visible completed event (seq 7); only the latter may appear.
	advanceToCompleted(t, harness.repository, now, hashA)
	page = decodeEventPage(t, harness.get(t, "/api/v1/events"))
	if len(page.Items) != 1 || page.HasMore {
		t.Fatalf("feed after completed = %d items has_more %v, want exactly the completed event", len(page.Items), page.HasMore)
	}
	envelope := decodeEnvelope(t, page.Items[0].Event)
	if envelope.Type != outbox.EventTypeCompleted || envelope.Data.Hash != hashA || envelope.Data.State != "COMPLETED" {
		t.Fatalf("completed item = type %q hash %q state %q, want download.completed/%s/COMPLETED", envelope.Type, envelope.Data.Hash, envelope.Data.State, hashA)
	}
	if page.NextCursor != encodeEventCursor(7) {
		t.Fatalf("next_cursor = %q, want seq 7 high-water", page.NextCursor)
	}

	// A failed download contributes exactly its failed event (seq 9): the
	// feed grows to completed then failed, ascending.
	hashF := "3434343434343434343434343434343434343434"
	harness.submitDownload(t, hashF, "FeedF")
	claimTransition(t, harness.repository, now.Add(time.Hour), domain.StateFailed)
	page = decodeEventPage(t, harness.get(t, "/api/v1/events"))
	if len(page.Items) != 2 || page.HasMore {
		t.Fatalf("feed after failed = %d items has_more %v, want exactly completed+failed", len(page.Items), page.HasMore)
	}
	first := decodeEnvelope(t, page.Items[0].Event)
	second := decodeEnvelope(t, page.Items[1].Event)
	if first.Type != outbox.EventTypeCompleted || first.Data.Hash != hashA ||
		second.Type != outbox.EventTypeFailed || second.Data.Hash != hashF {
		t.Fatalf("feed = %q/%q then %q/%q, want completed/%s then failed/%s", first.Type, first.Data.Hash, second.Type, second.Data.Hash, hashA, hashF)
	}
	if page.NextCursor != encodeEventCursor(9) {
		t.Fatalf("next_cursor = %q, want seq 9 high-water", page.NextCursor)
	}
}

// TestNativeEventsSequenceOrderAndNestedEnvelope pins the page against the
// store's authoritative rows: items are strictly sequence ascending, each
// item cursor equals its event sequence, and the nested event is the stored
// immutable payload byte for byte with the full envelope fields.
func TestNativeEventsSequenceOrderAndNestedEnvelope(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	hashB := "3131313131313131313131313131313131313131"
	hashF := "3434343434343434343434343434343434343434"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, now, hashA)
	harness.submitDownload(t, hashB, "FeedB")
	advanceToCompleted(t, harness.repository, now, hashB)
	harness.submitDownload(t, hashF, "Failure")
	claimTransition(t, harness.repository, now.Add(time.Hour), domain.StateFailed)

	authoritative := listFeedEvents(t, harness)
	if len(authoritative) != 3 {
		t.Fatalf("authoritative feed = %d events, want 3", len(authoritative))
	}
	page := decodeEventPage(t, harness.get(t, "/api/v1/events"))
	if len(page.Items) != len(authoritative) || page.HasMore {
		t.Fatalf("page = %d items has_more %v, want %d items without more", len(page.Items), page.HasMore, len(authoritative))
	}
	high, err := harness.repository.LatestEventSequence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != encodeEventCursor(high) {
		t.Fatalf("next_cursor = %q, want high-water %d", page.NextCursor, high)
	}
	previous := int64(-1)
	for i, item := range page.Items {
		sequence, err := decodeEventCursor(item.Cursor)
		if err != nil || sequence != authoritative[i].Sequence {
			t.Fatalf("item %d cursor %q decodes to %d (err %v), want sequence %d", i, item.Cursor, sequence, err, authoritative[i].Sequence)
		}
		if sequence <= previous {
			t.Fatalf("item %d sequence %d is not strictly ascending after %d", i, sequence, previous)
		}
		previous = sequence
		if string(item.Event) != string(authoritative[i].Payload) {
			t.Fatalf("item %d nested event differs from the stored immutable payload:\n%s\n%s", i, item.Event, authoritative[i].Payload)
		}
		envelope := decodeEnvelope(t, item.Event)
		if envelope.ID != authoritative[i].ID {
			t.Fatalf("item %d id = %q, want %q", i, envelope.ID, authoritative[i].ID)
		}
		if envelope.Type != authoritative[i].Type {
			t.Fatalf("item %d type = %q, want %q", i, envelope.Type, authoritative[i].Type)
		}
		if envelope.SchemaVersion != outbox.EventSchemaVersion {
			t.Fatalf("item %d schema_version = %d, want %d", i, envelope.SchemaVersion, outbox.EventSchemaVersion)
		}
		if !strings.HasSuffix(envelope.OccurredAt, "Z") {
			t.Fatalf("item %d occurred_at = %q, want UTC RFC3339Nano", i, envelope.OccurredAt)
		}
		if parsed, err := time.Parse(time.RFC3339Nano, envelope.OccurredAt); err != nil || !parsed.Equal(authoritative[i].OccurredAt) {
			t.Fatalf("item %d occurred_at = %q (err %v), want the stored %v", i, envelope.OccurredAt, err, authoritative[i].OccurredAt)
		}
		if envelope.Data.Hash != authoritative[i].AggregateID {
			t.Fatalf("item %d data.hash = %q, want %q", i, envelope.Data.Hash, authoritative[i].AggregateID)
		}
		if envelope.Data.DownloadVersion != authoritative[i].AggregateVersion {
			t.Fatalf("item %d data.download_version = %d, want %d", i, envelope.Data.DownloadVersion, authoritative[i].AggregateVersion)
		}
		switch authoritative[i].Type {
		case outbox.EventTypeCompleted:
			if envelope.Data.State != "COMPLETED" || envelope.Data.Progress != 1 || envelope.Data.CompletedAt == nil || envelope.Data.Error != "" {
				t.Fatalf("item %d completed data = %#v", i, envelope.Data)
			}
		case outbox.EventTypeFailed:
			if envelope.Data.State != "FAILED" || envelope.Data.CompletedAt != nil || envelope.Data.Error != domain.RedactedErrorText {
				t.Fatalf("item %d failed data = %#v", i, envelope.Data)
			}
		default:
			t.Fatalf("item %d type %q is not a feed event type", i, authoritative[i].Type)
		}
	}
}

// TestNativeEventsEmptyItemsNeverNull pins the empty page shape: items is a
// JSON array, never null, with the scan high-water encoded as next_cursor.
func TestNativeEventsEmptyItemsNeverNull(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	response := harness.get(t, "/api/v1/events")
	if response.Code != http.StatusOK {
		t.Fatalf("events = %d %q, want 200", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"items":[]`) {
		t.Errorf("empty page body %q does not contain items:[]", body)
	}
	if strings.Contains(body, `"items":null`) {
		t.Errorf("empty page body %q contains items:null", body)
	}
	page := decodeEventPage(t, response)
	if page.Items == nil {
		t.Error("items decoded as nil, want an empty array")
	}
	if page.HasMore || page.NextCursor != encodeEventCursor(0) {
		t.Errorf("empty page = has_more %v next %q, want false at seq 0", page.HasMore, page.NextCursor)
	}
}

// TestNativeEventsReplaySameCursorImmutable proves the at-least-once
// contract: repeating the same input cursor and filters re-serves the same
// immutable events, IDs, and page bytes with no server-side ACK state.
func TestNativeEventsReplaySameCursorImmutable(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	hashB := "3131313131313131313131313131313131313131"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, now, hashA)
	harness.submitDownload(t, hashB, "FeedB")
	advanceToCompleted(t, harness.repository, now, hashB)

	cursor := encodeEventCursor(0)
	first := harness.get(t, "/api/v1/events?cursor="+cursor)
	second := harness.get(t, "/api/v1/events?cursor="+cursor)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("replay status = %d/%d, want 200", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("replay with the same cursor changed the body:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	page1 := decodeEventPage(t, first)
	page2 := decodeEventPage(t, second)
	if len(page1.Items) != 2 {
		t.Fatalf("first page = %d items, want 2", len(page1.Items))
	}
	for i := range page1.Items {
		if page1.Items[i].Cursor != page2.Items[i].Cursor {
			t.Errorf("item %d cursor changed between replays: %q vs %q", i, page1.Items[i].Cursor, page2.Items[i].Cursor)
		}
		if string(page1.Items[i].Event) != string(page2.Items[i].Event) {
			t.Errorf("item %d event changed between replays", i)
		}
	}

	// Replaying from the first item's cursor re-serves the tail from the
	// same immutable rows: exactly the second event.
	middle := decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+page1.Items[0].Cursor))
	if len(middle.Items) != 1 || middle.Items[0].Cursor != page1.Items[1].Cursor {
		t.Fatalf("cursor replay after first item = %d items, want the second event with cursor %q", len(middle.Items), page1.Items[1].Cursor)
	}
	if string(middle.Items[0].Event) != string(page1.Items[1].Event) {
		t.Errorf("middle replay event differs from the original second item")
	}
}

// TestNativeEventsTypeAndHashFilters pins the filter semantics: each filter
// narrows the page to exactly its events, cross-filters exclude, and a
// filtered-out hash still advances next_cursor to the scan high-water.
func TestNativeEventsTypeAndHashFilters(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	hashF := "3434343434343434343434343434343434343434"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, now, hashA)
	harness.submitDownload(t, hashF, "Failure")
	claimTransition(t, harness.repository, now.Add(time.Hour), domain.StateFailed)
	high, err := harness.repository.LatestEventSequence(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	page := decodeEventPage(t, harness.get(t, "/api/v1/events?types=download.completed"))
	if len(page.Items) != 1 || page.HasMore {
		t.Fatalf("completed filter = %d items has_more %v, want 1", len(page.Items), page.HasMore)
	}
	if envelope := decodeEnvelope(t, page.Items[0].Event); envelope.Type != outbox.EventTypeCompleted || envelope.Data.Hash != hashA {
		t.Fatalf("completed filter item = %q/%q, want download.completed/%s", envelope.Type, envelope.Data.Hash, hashA)
	}

	page = decodeEventPage(t, harness.get(t, "/api/v1/events?types=download.failed"))
	if len(page.Items) != 1 || page.HasMore {
		t.Fatalf("failed filter = %d items has_more %v, want 1", len(page.Items), page.HasMore)
	}
	if envelope := decodeEnvelope(t, page.Items[0].Event); envelope.Type != outbox.EventTypeFailed || envelope.Data.Hash != hashF {
		t.Fatalf("failed filter item = %q/%q, want download.failed/%s", envelope.Type, envelope.Data.Hash, hashF)
	}

	page = decodeEventPage(t, harness.get(t, "/api/v1/events?types=download.completed%2Cdownload.failed"))
	if len(page.Items) != 2 || page.HasMore {
		t.Fatalf("both types = %d items has_more %v, want 2", len(page.Items), page.HasMore)
	}
	firstSeq, err := decodeEventCursor(page.Items[0].Cursor)
	if err != nil {
		t.Fatal(err)
	}
	secondSeq, err := decodeEventCursor(page.Items[1].Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if firstSeq >= secondSeq {
		t.Fatalf("both-types page not ascending: %d then %d", firstSeq, secondSeq)
	}
	if envelope := decodeEnvelope(t, page.Items[0].Event); envelope.Type != outbox.EventTypeCompleted || envelope.Data.Hash != hashA {
		t.Fatalf("both-types first item = %q/%q, want completed/%s", envelope.Type, envelope.Data.Hash, hashA)
	}

	page = decodeEventPage(t, harness.get(t, "/api/v1/events?hash="+hashA))
	if len(page.Items) != 1 || page.HasMore {
		t.Fatalf("hash filter = %d items has_more %v, want 1", len(page.Items), page.HasMore)
	}
	if envelope := decodeEnvelope(t, page.Items[0].Event); envelope.Data.Hash != hashA || envelope.Type != outbox.EventTypeCompleted {
		t.Fatalf("hash filter item = %q/%q, want %s/download.completed", envelope.Type, envelope.Data.Hash, hashA)
	}

	// Cross filters: the failed hash is excluded by the completed type filter.
	page = decodeEventPage(t, harness.get(t, "/api/v1/events?hash="+hashF+"&types=download.completed"))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(high) {
		t.Fatalf("cross-filter page = %d items has_more %v next %q, want empty at high-water %d", len(page.Items), page.HasMore, page.NextCursor, high)
	}

	// A hash with no events at all still advances next_cursor to the high-water.
	page = decodeEventPage(t, harness.get(t, "/api/v1/events?hash=3535353535353535353535353535353535353535"))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(high) {
		t.Fatalf("absent-hash page = %d items has_more %v next %q, want empty at high-water %d", len(page.Items), page.HasMore, page.NextCursor, high)
	}
}

// TestNativeEventsHiddenAndFilterEmptyAdvanceNextCursor proves that hidden
// event types and filtered-out hashes never rescan: an empty page encodes the
// store high-water as next_cursor, skipping the invisible rows.
func TestNativeEventsHiddenAndFilterEmptyAdvanceNextCursor(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	hashB := "3131313131313131313131313131313131313131"
	hashC := "3232323232323232323232323232323232323232"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, now, hashA) // completed at seq 7
	harness.submitDownload(t, hashB, "FeedB")
	advanceToCompleted(t, harness.repository, now, hashB) // completed at seq 14
	harness.submitDownload(t, hashC, "Hidden")            // created at seq 15, hidden

	// The page ending before the hidden created row still advances over it.
	page := decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(7)))
	if len(page.Items) != 1 || page.HasMore {
		t.Fatalf("cursor 7 page = %d items has_more %v, want B's completed event", len(page.Items), page.HasMore)
	}
	if envelope := decodeEnvelope(t, page.Items[0].Event); envelope.Data.Hash != hashB || envelope.Type != outbox.EventTypeCompleted {
		t.Fatalf("cursor 7 item = %q/%q, want completed/%s", envelope.Type, envelope.Data.Hash, hashB)
	}
	if page.NextCursor != encodeEventCursor(15) {
		t.Fatalf("next_cursor = %q, want high-water 15 (hidden created row skipped)", page.NextCursor)
	}

	// A pure hidden gap: cursor past every visible event.
	page = decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(14)))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(15) {
		t.Fatalf("hidden-gap page = %d items has_more %v next %q, want empty at 15", len(page.Items), page.HasMore, page.NextCursor)
	}

	// Filtered-out hash: B's completed event exists but is excluded.
	page = decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(14)+"&hash="+hashA))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(15) {
		t.Fatalf("hash-filtered page = %d items has_more %v next %q, want empty at 15", len(page.Items), page.HasMore, page.NextCursor)
	}

	// Filtered-out type likewise.
	page = decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(14)+"&types=download.failed"))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(15) {
		t.Fatalf("type-filtered page = %d items has_more %v next %q, want empty at 15", len(page.Items), page.HasMore, page.NextCursor)
	}
}

// TestNativeEventsLimitOneLookahead pins the lookahead pagination: limit=1
// returns one item with has_more=true and next_cursor equal to that item's
// cursor, and the next request re-serves exactly the following event.
func TestNativeEventsLimitOneLookahead(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	hashB := "3131313131313131313131313131313131313131"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, now, hashA) // completed at seq 7
	harness.submitDownload(t, hashB, "FeedB")
	advanceToCompleted(t, harness.repository, now, hashB) // completed at seq 14

	first := decodeEventPage(t, harness.get(t, "/api/v1/events?limit=1"))
	if len(first.Items) != 1 || !first.HasMore {
		t.Fatalf("limit=1 page = %d items has_more %v, want 1 item with more", len(first.Items), first.HasMore)
	}
	firstEnvelope := decodeEnvelope(t, first.Items[0].Event)
	if firstEnvelope.Data.Hash != hashA || firstEnvelope.Type != outbox.EventTypeCompleted {
		t.Fatalf("limit=1 item = %q/%q, want completed/%s", firstEnvelope.Type, firstEnvelope.Data.Hash, hashA)
	}
	if first.NextCursor != first.Items[0].Cursor {
		t.Fatalf("next_cursor %q != last item cursor %q on a has_more page", first.NextCursor, first.Items[0].Cursor)
	}

	second := decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+first.NextCursor+"&limit=1"))
	if len(second.Items) != 1 || second.HasMore {
		t.Fatalf("second limit=1 page = %d items has_more %v, want 1 item without more", len(second.Items), second.HasMore)
	}
	secondEnvelope := decodeEnvelope(t, second.Items[0].Event)
	if secondEnvelope.Data.Hash != hashB || secondEnvelope.ID == firstEnvelope.ID {
		t.Fatalf("second page item = %q/%q, want the next event %s", secondEnvelope.Type, secondEnvelope.Data.Hash, hashB)
	}
	if second.NextCursor != encodeEventCursor(14) {
		t.Fatalf("second page next_cursor = %q, want high-water 14", second.NextCursor)
	}

	// limit=1 past the last event returns an empty page at the high-water.
	third := decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(14)+"&limit=1"))
	if len(third.Items) != 0 || third.HasMore || third.NextCursor != encodeEventCursor(14) {
		t.Fatalf("exhausted limit=1 page = %d items has_more %v next %q, want empty at 14", len(third.Items), third.HasMore, third.NextCursor)
	}
}

// TestNativeEventsFutureCursorRejected pins the explicit-cursor validation:
// a decoded cursor beyond the current high-water is a stable 400, the
// high-water cursor itself is valid, and cursor=latest is accepted shorthand.
func TestNativeEventsFutureCursorRejected(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, now, hashA) // high-water 7

	requireErrorBody(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(8)), http.StatusBadRequest, invalidRequestJSON)
	// A syntactically valid but astronomically future cursor is also 400.
	requireErrorBody(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(math.MaxInt64)), http.StatusBadRequest, invalidRequestJSON)

	// Cursor equal to the high-water is valid and yields an empty page.
	page := decodeEventPage(t, harness.get(t, "/api/v1/events?cursor="+encodeEventCursor(7)))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(7) {
		t.Fatalf("high-water cursor page = %d items has_more %v next %q, want empty at 7", len(page.Items), page.HasMore, page.NextCursor)
	}

	// cursor=latest is the exact shorthand, never a rejected cursor.
	page = decodeEventPage(t, harness.get(t, "/api/v1/events?cursor=latest"))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(7) {
		t.Fatalf("cursor=latest page = %d items has_more %v next %q, want empty at 7", len(page.Items), page.HasMore, page.NextCursor)
	}
}

// TestNativeEventsCursorLatestSkipsExisting pins the latest shorthand: a
// request at cursor=latest sees no pre-existing events and encodes the
// current high-water as next_cursor.
func TestNativeEventsCursorLatestSkipsExisting(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	hashA := "3030303030303030303030303030303030303030"
	hashB := "3131313131313131313131313131313131313131"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, now, hashA)
	harness.submitDownload(t, hashB, "FeedB")
	advanceToCompleted(t, harness.repository, now, hashB) // high-water 14

	page := decodeEventPage(t, harness.get(t, "/api/v1/events?cursor=latest"))
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(14) {
		t.Fatalf("cursor=latest page = %d items has_more %v next %q, want empty at 14", len(page.Items), page.HasMore, page.NextCursor)
	}
}

// TestNativeEventsCorruptPayloadStable500 proves storage corruption maps the
// whole request to the stable 500 before any success header or partial page
// is written, and that the corruption is one-shot.
func TestNativeEventsCorruptPayloadStable500(t *testing.T) {
	t.Parallel()
	var script *scriptedStore
	harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
		script = newScriptedStore(repository, signal)
		return script
	})
	hashA := "3030303030303030303030303030303030303030"
	harness.submitDownload(t, hashA, "FeedA")
	advanceToCompleted(t, harness.repository, harness.clock.now, hashA) // completed at seq 7

	script.corruptEvents = true
	requireErrorBody(t, harness.get(t, "/api/v1/events"), http.StatusInternalServerError, internalBody)

	// The corruption was a one-shot inject: the same request now serves the
	// real immutable page through the transparent wrapper.
	page := decodeEventPage(t, harness.get(t, "/api/v1/events"))
	if len(page.Items) != 1 || page.HasMore || page.Items[0].Cursor != encodeEventCursor(7) {
		t.Fatalf("post-corruption page = %d items has_more %v, want the completed event at seq 7", len(page.Items), page.HasMore)
	}
	if envelope := decodeEnvelope(t, page.Items[0].Event); envelope.Type != outbox.EventTypeCompleted || envelope.Data.Hash != hashA {
		t.Fatalf("post-corruption item = %q/%q, want completed/%s", envelope.Type, envelope.Data.Hash, hashA)
	}
}

// TestNativeEventsWaitEmptyTimeout200Empty drives a real 1s long poll with no
// events: the fixed deadline fires, the final scan answers 200 (not 204) with
// an empty page encoding the scan high-water.
func TestNativeEventsWaitEmptyTimeout200Empty(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	start := time.Now()
	response := harness.get(t, "/api/v1/events?wait=1s")
	elapsed := time.Since(start)
	if response.Code != http.StatusOK {
		t.Fatalf("events wait = %d %q, want 200", response.Code, response.Body.String())
	}
	if elapsed < 900*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Errorf("timeout answered after %v, want ~1s", elapsed)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	page := decodeEventPage(t, response)
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(0) {
		t.Fatalf("timeout page = %d items has_more %v next %q, want empty at seq 0", len(page.Items), page.HasMore, page.NextCursor)
	}
}

// TestNativeEventsWaitCommitWakesCursorLatest proves that a real completed or
// failed commit, landing after a cursor=latest request parked on an empty
// round, wakes the waiter: it answers 200 well before the 1s deadline with
// exactly the newly committed event.
func TestNativeEventsWaitCommitWakesCursorLatest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		eventType string
		state     string
		advance   func(t *testing.T, harness *nativeHarness, hash string)
	}{
		{
			name:      "completed",
			eventType: outbox.EventTypeCompleted,
			state:     "COMPLETED",
			advance: func(t *testing.T, harness *nativeHarness, hash string) {
				advanceToCompleted(t, harness.repository, harness.clock.now, hash)
			},
		},
		{
			name:      "failed",
			eventType: outbox.EventTypeFailed,
			state:     "FAILED",
			advance: func(t *testing.T, harness *nativeHarness, hash string) {
				claimTransition(t, harness.repository, harness.clock.now.Add(time.Hour), domain.StateFailed)
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var script *scriptedStore
			harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
				script = newScriptedStore(repository, signal)
				return script
			})
			hash := "3636363636363636363636363636363636363636"
			harness.submitDownload(t, hash, "Wake")
			start := time.Now()
			pending := harness.serveAsync(t, "/api/v1/events?cursor=latest&wait=1s")
			eventually(t, 2*time.Second, "waiter never performed its first scan", func() bool { return script.count("events") >= 1 })
			test.advance(t, harness, hash)
			pending.finish(t)
			if pending.recorder.Code != http.StatusOK {
				t.Fatalf("events wait = %d %q, want 200 after the commit", pending.recorder.Code, pending.recorder.Body.String())
			}
			if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
				t.Errorf("commit wake answered after %v, want well under the 1s deadline", elapsed)
			}
			page := decodeEventPage(t, pending.recorder)
			if len(page.Items) != 1 || page.HasMore {
				t.Fatalf("wake page = %d items has_more %v, want exactly the new event", len(page.Items), page.HasMore)
			}
			envelope := decodeEnvelope(t, page.Items[0].Event)
			if envelope.Type != test.eventType || envelope.Data.Hash != hash || envelope.Data.State != test.state {
				t.Fatalf("wake event = %q/%q/%q, want %s/%s/%s", envelope.Type, envelope.Data.Hash, envelope.Data.State, test.eventType, hash, test.state)
			}
			// The returned event is the newest commit: its sequence is the
			// current high-water, never a pre-existing row.
			high, err := harness.repository.LatestEventSequence(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if sequence, err := decodeEventCursor(page.Items[0].Cursor); err != nil || sequence != high {
				t.Fatalf("wake item cursor decodes to %d (err %v), want the committed high-water %d", sequence, err, high)
			}
		})
	}
}

// TestNativeEventsWaitBroadcastWakesTwoWaiters covers one commit waking every
// waiter parked on the same snapshot: two concurrent cursor=latest requests
// both answer 200 with the identical single-event page.
func TestNativeEventsWaitBroadcastWakesTwoWaiters(t *testing.T) {
	t.Parallel()
	var script *scriptedStore
	harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
		script = newScriptedStore(repository, signal)
		return script
	})
	now := harness.clock.now
	hash := "3737373737373737373737373737373737373737"
	harness.submitDownload(t, hash, "Broadcast")
	first := harness.serveAsync(t, "/api/v1/events?cursor=latest&wait=1s")
	second := harness.serveAsync(t, "/api/v1/events?cursor=latest&wait=1s")
	eventually(t, 2*time.Second, "waiters never both performed their first scan", func() bool { return script.count("events") >= 2 })
	advanceToCompleted(t, harness.repository, now, hash)
	first.finish(t)
	second.finish(t)
	for name, pending := range map[string]*pendingRequest{"first": first, "second": second} {
		if pending.recorder.Code != http.StatusOK {
			t.Fatalf("%s events wait = %d %q, want 200", name, pending.recorder.Code, pending.recorder.Body.String())
		}
		page := decodeEventPage(t, pending.recorder)
		if len(page.Items) != 1 || page.HasMore {
			t.Fatalf("%s wake page = %d items has_more %v, want the single committed event", name, len(page.Items), page.HasMore)
		}
		if envelope := decodeEnvelope(t, page.Items[0].Event); envelope.Type != outbox.EventTypeCompleted || envelope.Data.Hash != hash {
			t.Errorf("%s wake event = %q/%q, want completed/%s", name, envelope.Type, envelope.Data.Hash, hash)
		}
	}
	if first.recorder.Body.String() != second.recorder.Body.String() {
		t.Errorf("broadcast wake bodies differ:\n%s\n%s", first.recorder.Body.String(), second.recorder.Body.String())
	}
}

// TestNativeEventsWaitCancellationWritesNothing proves a cancelled long poll
// returns without writing any response.
func TestNativeEventsWaitCancellationWritesNothing(t *testing.T) {
	t.Parallel()
	var script *scriptedStore
	harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
		script = newScriptedStore(repository, signal)
		return script
	})
	pending := harness.serveAsync(t, "/api/v1/events?cursor=latest&wait=1s")
	eventually(t, 2*time.Second, "waiter never performed its first scan", func() bool { return script.count("events") >= 1 })
	pending.cancel()
	pending.finish(t)
	if pending.writer.didWrite() {
		t.Errorf("cancelled events wait wrote a response, want no response")
	}
}

// TestNativeEventsWaitLifecycleShutdownFinalPage covers the runtime
// retirement path: closing the shutdown channel performs the same final scan
// as a timeout and answers 200 immediately with the final empty page.
func TestNativeEventsWaitLifecycleShutdownFinalPage(t *testing.T) {
	t.Parallel()
	var script *scriptedStore
	harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
		script = newScriptedStore(repository, signal)
		return script
	})
	hash := "3838383838383838383838383838383838383838"
	harness.submitDownload(t, hash, "Shutdown") // hidden created event at seq 1
	start := time.Now()
	pending := harness.serveAsync(t, "/api/v1/events?wait=1s")
	eventually(t, 2*time.Second, "waiter never performed its first scan", func() bool { return script.count("events") >= 1 })
	close(harness.shutdown)
	pending.finish(t)
	if pending.recorder.Code != http.StatusOK {
		t.Fatalf("events wait = %d %q, want 200 final page", pending.recorder.Code, pending.recorder.Body.String())
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Errorf("lifecycle shutdown answered after %v, want an immediate final scan", elapsed)
	}
	if got := pending.recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := pending.recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	page := decodeEventPage(t, pending.recorder)
	if len(page.Items) != 0 || page.HasMore || page.NextCursor != encodeEventCursor(1) {
		t.Fatalf("final page = %d items has_more %v next %q, want empty at seq 1", len(page.Items), page.HasMore, page.NextCursor)
	}
}
