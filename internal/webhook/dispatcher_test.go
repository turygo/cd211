package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/outbox"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
	// durations records every NewTimer request for idle-wait assertions.
	durations []time.Duration
	// timer is the single timer handed out; tests fire or ignore it.
	timer *fakeTimer
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.durations = append(c.durations, d)
	if c.timer == nil {
		c.timer = &fakeTimer{ch: make(chan time.Time, 1)}
	}
	return c.timer
}

func (c *fakeClock) timerDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durations...)
}

type fakeTimer struct {
	ch      chan time.Time
	stopped bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool {
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

// fakeDelivery is one durable delivery row inside the fake repository.
type fakeDelivery struct {
	delivery        outbox.Delivery
	payload         []byte
	url             string
	hmacSecret      string
	bearerToken     string
	endpointEnabled bool
	endpointDeleted bool
}

// fakeRepository mirrors the documented store contract closely enough for the
// dispatcher loop to be exercised end to end: claim (lease, attempt, first
// attempt), commit (CAS, succeeded/dead/cancelled, retry scheduling via
// outbox.DeliveryDelay and outbox.RetryDeadline), ordering, and pruning.
type fakeRepository struct {
	mu         sync.Mutex
	rows       []*fakeDelivery
	nextID     int64
	commitErrs []error
	commits    []outbox.Result
	pruned     []int64
}

func (r *fakeRepository) addDelivery(event outbox.Delivery, payload []byte, url, secret, bearer string) *fakeDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := &fakeDelivery{
		delivery:        event,
		payload:         payload,
		url:             url,
		hmacSecret:      secret,
		bearerToken:     bearer,
		endpointEnabled: true,
	}
	row.delivery.ID = r.nextID
	r.nextID++
	row.delivery.Status = outbox.StatusPending
	row.delivery.RowVersion = 1
	if row.delivery.CreatedAt.IsZero() {
		row.delivery.CreatedAt = time.Now()
	}
	row.delivery.UpdatedAt = row.delivery.CreatedAt
	r.rows = append(r.rows, row)
	return row
}

// blocks reports whether a row is non-terminal and therefore orders later rows
// behind it.
func blocks(row *fakeDelivery) bool {
	return row.delivery.Status == outbox.StatusPending || row.delivery.Status == outbox.StatusDelivering
}

// claimable reports whether the row may be claimed at now: enabled,
// non-deleted, pending or delivering with an expired or absent lease, and due.
func (r *fakeRepository) claimable(row *fakeDelivery, now time.Time) bool {
	if !row.endpointEnabled || row.endpointDeleted {
		return false
	}
	switch row.delivery.Status {
	case outbox.StatusPending:
		return row.delivery.NextAttemptAt == nil || !row.delivery.NextAttemptAt.After(now)
	case outbox.StatusDelivering:
		if row.delivery.LeaseUntil != nil && row.delivery.LeaseUntil.After(now) {
			return false
		}
		return row.delivery.NextAttemptAt == nil || !row.delivery.NextAttemptAt.After(now)
	default:
		return false
	}
}

// orderingBlocked reports whether an earlier row for the same endpoint+
// aggregate is still pending or delivering.
func (r *fakeRepository) orderingBlocked(row *fakeDelivery) bool {
	for _, other := range r.rows {
		if other == row ||
			other.delivery.EndpointID != row.delivery.EndpointID ||
			other.delivery.AggregateType != row.delivery.AggregateType ||
			other.delivery.AggregateID != row.delivery.AggregateID ||
			other.delivery.ID >= row.delivery.ID {
			continue
		}
		if blocks(other) {
			return true
		}
	}
	return false
}

func (r *fakeRepository) CommitWebhookClaim(_ context.Context, claim outbox.Claim, result outbox.Result, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.commitErrs) != 0 {
		err := r.commitErrs[0]
		r.commitErrs = r.commitErrs[1:]
		return err
	}
	row := r.byID(claim.DeliveryID)
	if row == nil ||
		row.delivery.Status != outbox.StatusDelivering ||
		row.delivery.LeaseOwner != claim.Owner ||
		row.delivery.RowVersion != claim.Version {
		return outbox.ErrClaimLost
	}
	r.commits = append(r.commits, result)

	row.delivery.RowVersion++
	row.delivery.UpdatedAt = now
	row.delivery.LastHTTPStatus = result.LastHTTPStatus
	row.delivery.LastError = result.LastError
	row.delivery.NextAttemptAt = result.NextAttemptAt
	row.delivery.Status = result.Status
	row.delivery.DeliveredAt = result.DeliveredAt
	row.delivery.LeaseOwner = ""
	row.delivery.LeaseUntil = nil
	if row.endpointDeleted {
		row.delivery.Status = outbox.StatusCancelled
		row.delivery.NextAttemptAt = nil
		row.delivery.DeliveredAt = nil
		row.delivery.LastError = ""
	}
	return nil
}

// byID must be called with r.mu held.
func (r *fakeRepository) byID(id int64) *fakeDelivery {
	for _, row := range r.rows {
		if row.delivery.ID == id {
			return row
		}
	}
	return nil
}

func (r *fakeRepository) ClaimWebhookDue(_ context.Context, owner string, now time.Time, lease time.Duration) (*outbox.Claim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if !r.claimable(row, now) || r.orderingBlocked(row) {
			continue
		}
		row.delivery.Status = outbox.StatusDelivering
		row.delivery.LeaseOwner = owner
		until := now.Add(lease)
		row.delivery.LeaseUntil = &until
		row.delivery.AttemptCount++
		if row.delivery.FirstAttemptAt == nil {
			first := now
			row.delivery.FirstAttemptAt = &first
		}
		row.delivery.RowVersion++
		row.delivery.UpdatedAt = now
		return &outbox.Claim{
			DeliveryID:     row.delivery.ID,
			Owner:          owner,
			Version:        row.delivery.RowVersion,
			EndpointID:     row.delivery.EndpointID,
			EventID:        row.delivery.EventID,
			EventType:      row.delivery.EventType,
			Payload:        row.payload,
			URL:            row.url,
			HMACSecret:     row.hmacSecret,
			BearerToken:    row.bearerToken,
			AttemptCount:   row.delivery.AttemptCount,
			FirstAttemptAt: row.delivery.FirstAttemptAt,
		}, nil
	}
	return nil, nil
}

// NextWebhookDue returns the earliest future due time among claimable rows so
// the worker can sleep until it; nil when nothing will ever be due.
func (r *fakeRepository) NextWebhookDue(_ context.Context, now time.Time) (*time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var earliest *time.Time
	for _, row := range r.rows {
		if !row.endpointEnabled || row.endpointDeleted {
			continue
		}
		switch row.delivery.Status {
		case outbox.StatusPending, outbox.StatusDelivering:
			if row.delivery.LeaseUntil != nil && row.delivery.LeaseUntil.After(now) {
				continue
			}
		default:
			continue
		}
		if r.orderingBlocked(row) {
			continue
		}
		at := row.delivery.NextAttemptAt
		if at == nil {
			continue
		}
		if earliest == nil || at.Before(*earliest) {
			earliest = at
		}
	}
	return earliest, nil
}

func (r *fakeRepository) PruneWebhookDeliveries(_ context.Context, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-90 * 24 * time.Hour)
	var kept []*fakeDelivery
	var count int64
	for _, row := range r.rows {
		terminal := row.delivery.Status == outbox.StatusSucceeded || row.delivery.Status == outbox.StatusCancelled
		if terminal && row.delivery.UpdatedAt.Before(cutoff) {
			r.pruned = append(r.pruned, row.delivery.ID)
			count++
			continue
		}
		kept = append(kept, row)
	}
	r.rows = kept
	return count, nil
}

func (r *fakeRepository) status(id int64) outbox.DeliveryStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row := r.byID(id); row != nil {
		return row.delivery.Status
	}
	return ""
}

func (r *fakeRepository) attemptCount(id int64) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row := r.byID(id); row != nil {
		return row.delivery.AttemptCount
	}
	return 0
}

func (r *fakeRepository) nextAttemptAt(id int64) *time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row := r.byID(id); row != nil {
		return row.delivery.NextAttemptAt
	}
	return nil
}

func (r *fakeRepository) lastError(id int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row := r.byID(id); row != nil {
		return row.delivery.LastError
	}
	return ""
}

func (r *fakeRepository) disableEndpoint(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.delivery.EndpointID == id {
			row.endpointEnabled = false
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestDispatcher(repo Repository, client HTTPClient, clock Clock, version string) *Dispatcher {
	dispatcher, err := New(Config{
		Owner:          "test-owner",
		LeaseDuration:  30 * time.Second,
		RequestTimeout: 10 * time.Second,
		WorkerCount:    4,
		MaxIdleWait:    time.Second,
		PruneInterval:  time.Hour,
		Version:        version,
	}, repo, client, clock, testLogger())
	if err != nil {
		panic(err)
	}
	return dispatcher
}

// captureServer records the raw request for signature/header assertions.
type captureServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
	hits     int
}

type capturedRequest struct {
	method  string
	body    []byte
	headers http.Header
}

func newCaptureServer(status int, responseBody string) *captureServer {
	c := &captureServer{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.requests = append(c.requests, capturedRequest{method: r.Method, body: body, headers: r.Header.Clone()})
		c.hits++
		c.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, responseBody)
	}))
	return c
}

func (c *captureServer) Close() { c.server.Close() }

func expectedSignature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func baseDelivery(eventType, eventID string) outbox.Delivery {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	return outbox.Delivery{
		EventID:       eventID,
		EndpointID:    7,
		EndpointName:  "radarr",
		EventType:     eventType,
		AggregateType: "download",
		AggregateID:   "abc123",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func (r *fakeRepository) addDefault(now time.Time) *fakeDelivery {
	return r.addDelivery(baseDelivery("download.completed", "evt_123"), []byte(`{"id":"evt_123"}`), "http://example.test/hook", "secret-key", "bearer-token")
}

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

func TestNewValidation(t *testing.T) {
	repo := &fakeRepository{}
	client := NewHTTPClient(time.Second)
	clock := &fakeClock{now: time.Now()}
	base := Config{Owner: "o", LeaseDuration: 30 * time.Second, RequestTimeout: 10 * time.Second, WorkerCount: 4, MaxIdleWait: time.Second, PruneInterval: time.Hour}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty owner", func(c *Config) { c.Owner = "  " }},
		{"lease not exceeding timeout plus margin", func(c *Config) { c.LeaseDuration = 15 * time.Second }},
		{"zero request timeout", func(c *Config) { c.RequestTimeout = 0 }},
		{"zero workers", func(c *Config) { c.WorkerCount = 0 }},
		{"zero idle wait", func(c *Config) { c.MaxIdleWait = 0 }},
		{"zero prune interval", func(c *Config) { c.PruneInterval = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := base
			tc.mutate(&config)
			if _, err := New(config, repo, client, clock, testLogger()); err == nil {
				t.Fatalf("New(%s) succeeded, want error", tc.name)
			}
		})
	}
	if _, err := New(base, nil, client, clock, testLogger()); err == nil {
		t.Fatal("New(nil repo) succeeded, want error")
	}
	if _, err := New(base, repo, nil, clock, testLogger()); err == nil {
		t.Fatal("New(nil client) succeeded, want error")
	}
	if _, err := New(base, repo, client, nil, testLogger()); err == nil {
		t.Fatal("New(nil clock) succeeded, want error")
	}
	if _, err := New(base, repo, client, clock, nil); err == nil {
		t.Fatal("New(nil logger) succeeded, want error")
	}
}

// ---------------------------------------------------------------------------
// Exact signature, headers, and body
// ---------------------------------------------------------------------------

func TestDeliverExactSignatureAndHeaders(t *testing.T) {
	capture := newCaptureServer(200, "ok")
	defer capture.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = capture.server.URL
	row.hmacSecret = "s3cr3t"
	row.bearerToken = "tok-1"

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "1.2.3")
	claimed, err := dispatcher.Step(context.Background())
	if err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if !claimed {
		t.Fatal("Step() claimed = false, want true")
	}
	if repo.status(row.delivery.ID) != outbox.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded", repo.status(row.delivery.ID))
	}

	capture.mu.Lock()
	req := capture.requests[0]
	hits := capture.hits
	capture.mu.Unlock()
	if hits != 1 {
		t.Fatalf("server hits = %d, want 1", hits)
	}
	if req.method != http.MethodPost {
		t.Fatalf("method = %q, want POST", req.method)
	}
	wantBody := []byte(`{"id":"evt_123"}`)
	if !bytes.Equal(req.body, wantBody) {
		t.Fatalf("body = %q, want exact payload %q", req.body, wantBody)
	}
	if got := req.headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := req.headers.Get("User-Agent"); got != "CD211/1.2.3" {
		t.Fatalf("User-Agent = %q, want CD211/1.2.3", got)
	}
	if got := req.headers.Get("X-CD211-Event"); got != "download.completed" {
		t.Fatalf("X-CD211-Event = %q", got)
	}
	if got := req.headers.Get("X-CD211-Event-ID"); got != "evt_123" {
		t.Fatalf("X-CD211-Event-ID = %q", got)
	}
	wantTimestamp := fmt.Sprintf("%d", now.Unix())
	if got := req.headers.Get("X-CD211-Timestamp"); got != wantTimestamp {
		t.Fatalf("X-CD211-Timestamp = %q, want %q", got, wantTimestamp)
	}
	wantSig := "v1=" + expectedSignature("s3cr3t", now.Unix(), wantBody)
	if got := req.headers.Get("X-CD211-Signature"); got != wantSig {
		t.Fatalf("X-CD211-Signature = %q, want %q", got, wantSig)
	}
	if got := req.headers.Get("Authorization"); got != "Bearer tok-1" {
		t.Fatalf("Authorization = %q, want Bearer tok-1", got)
	}
}

// ---------------------------------------------------------------------------
// Attempt timestamp and signature change across retries
// ---------------------------------------------------------------------------

func TestDeliverAttemptTimestamp(t *testing.T) {
	capture := newCaptureServer(500, "boom")
	defer capture.Close()

	start := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	repo := &fakeRepository{}
	row := repo.addDefault(start)
	row.url = capture.server.URL
	row.hmacSecret = "s3cr3t"
	row.bearerToken = ""

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("first Step() error = %v", err)
	}
	clock.Advance(37 * time.Second)
	next := clock.Now()
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("second Step() error = %v", err)
	}

	capture.mu.Lock()
	reqs := append([]capturedRequest(nil), capture.requests...)
	capture.mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	body := []byte(`{"id":"evt_123"}`)
	firstTS := reqs[0].headers.Get("X-CD211-Timestamp")
	secondTS := reqs[1].headers.Get("X-CD211-Timestamp")
	if firstTS != fmt.Sprintf("%d", start.Unix()) {
		t.Fatalf("first timestamp = %q, want %d", firstTS, start.Unix())
	}
	if secondTS != fmt.Sprintf("%d", next.Unix()) {
		t.Fatalf("second timestamp = %q, want %d", secondTS, next.Unix())
	}
	if firstTS == secondTS {
		t.Fatal("attempt timestamps identical across retries")
	}
	if got := reqs[1].headers.Get("X-CD211-Signature"); got != "v1="+expectedSignature("s3cr3t", next.Unix(), body) {
		t.Fatalf("second signature = %q, want signed with the attempt timestamp", got)
	}
	if got := reqs[1].headers.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want absent when no bearer configured", got)
	}
	// Two failures inside the 24-hour window schedule a retry, not a dead-letter.
	if got := repo.status(row.delivery.ID); got != outbox.StatusPending {
		t.Fatalf("status = %q, want pending (retry scheduled)", got)
	}
	if got := repo.attemptCount(row.delivery.ID); got != 2 {
		t.Fatalf("attempt count = %d, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// Bearer token presence
// ---------------------------------------------------------------------------

func TestDeliverBearerAbsent(t *testing.T) {
	capture := newCaptureServer(204, "")
	defer capture.Close()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = capture.server.URL
	row.bearerToken = ""

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	capture.mu.Lock()
	req := capture.requests[0]
	capture.mu.Unlock()
	if got := req.headers.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want absent", got)
	}
}

// ---------------------------------------------------------------------------
// No redirects
// ---------------------------------------------------------------------------

func TestDeliverNoRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect target was followed")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirector.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = redirector.URL

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if got := repo.status(row.delivery.ID); got != outbox.StatusPending {
		t.Fatalf("status = %q, want pending (scheduled retry)", got)
	}
	if got := repo.lastError(row.delivery.ID); got != "HTTP 302" {
		t.Fatalf("LastError = %q, want HTTP 302", got)
	}
	if next := repo.nextAttemptAt(row.delivery.ID); next == nil {
		t.Fatal("NextAttemptAt nil, want scheduled retry")
	}
}

// ---------------------------------------------------------------------------
// 2xx success
// ---------------------------------------------------------------------------

func TestDeliver2xxSuccess(t *testing.T) {
	capture := newCaptureServer(204, "")
	defer capture.Close()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = capture.server.URL

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if repo.status(row.delivery.ID) != outbox.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded", repo.status(row.delivery.ID))
	}
	repo.mu.Lock()
	result := repo.commits[0]
	repo.mu.Unlock()
	if result.LastHTTPStatus != 204 {
		t.Fatalf("committed HTTPStatus = %d, want 204", result.LastHTTPStatus)
	}
	if result.LastError != "" {
		t.Fatalf("committed Error = %q, want empty", result.LastError)
	}
}

// ---------------------------------------------------------------------------
// Non-2xx and network retry classification
// ---------------------------------------------------------------------------

func TestDeliverNon2xxAndNetworkRetry(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = "http://127.0.0.1:1/closed" // nothing listens on port 1
	row.bearerToken = ""

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if got := repo.lastError(row.delivery.ID); got != outbox.ErrorCategoryConnection {
		t.Fatalf("network LastError = %q, want %q", got, outbox.ErrorCategoryConnection)
	}
	repo.mu.Lock()
	result := repo.commits[0]
	repo.mu.Unlock()
	if result.LastHTTPStatus != 0 {
		t.Fatalf("network HTTPStatus = %d, want 0", result.LastHTTPStatus)
	}
	next := repo.nextAttemptAt(row.delivery.ID)
	if next == nil || !next.Equal(now.Add(outbox.DeliveryDelay(1))) {
		t.Fatalf("next attempt = %v, want now + DeliveryDelay(1)", next)
	}
}

// ---------------------------------------------------------------------------
// 24-hour dead-letter
// ---------------------------------------------------------------------------

func TestDeliverDeadLetterAfter24Hours(t *testing.T) {
	capture := newCaptureServer(500, "boom")
	defer capture.Close()
	start := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	repo := &fakeRepository{}
	row := repo.addDefault(start)
	row.url = capture.server.URL

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	for i := range 100 {
		if _, err := dispatcher.Step(context.Background()); err != nil {
			t.Fatalf("Step() error = %v", err)
		}
		if repo.status(row.delivery.ID) == outbox.StatusDead {
			break
		}
		next := repo.nextAttemptAt(row.delivery.ID)
		if next == nil {
			t.Fatalf("attempt %d: no next attempt and not dead", i+1)
		}
		clock.Advance(next.Sub(clock.Now()))
		clock.Advance(time.Nanosecond)
	}
	if got := repo.status(row.delivery.ID); got != outbox.StatusDead {
		t.Fatalf("status = %q, want dead", got)
	}
	if got := repo.lastError(row.delivery.ID); got != "HTTP 500" {
		t.Fatalf("LastError = %q, want HTTP 500", got)
	}
	if repo.attemptCount(row.delivery.ID) < 3 {
		t.Fatalf("attempts = %d, want multiple retries before dead-letter", repo.attemptCount(row.delivery.ID))
	}
	repo.mu.Lock()
	first := row.delivery.FirstAttemptAt
	repo.mu.Unlock()
	if first == nil {
		t.Fatal("FirstAttemptAt nil")
	}
	// Dead-lettering follows the deterministic capped schedule: the delivery
	// dies on the attempt whose cumulative delays first reach RetryDeadline,
	// and NextAttemptAt is cleared.
	if next := repo.nextAttemptAt(row.delivery.ID); next != nil {
		t.Fatalf("NextAttemptAt = %v after dead-letter, want nil", next)
	}
	attempts := repo.attemptCount(row.delivery.ID)
	var cumulative time.Duration
	for i := int64(1); i <= attempts; i++ {
		cumulative += outbox.DeliveryDelay(i)
	}
	if cumulative < outbox.RetryDeadline {
		t.Fatalf("dead-lettered after %d attempts with cumulative schedule %v, below %v", attempts, cumulative, outbox.RetryDeadline)
	}
	if cumulative-outbox.DeliveryDelay(attempts) >= outbox.RetryDeadline {
		t.Fatalf("dead-lettered one attempt late: prior cumulative %v already at/after %v", cumulative-outbox.DeliveryDelay(attempts), outbox.RetryDeadline)
	}
}

// ---------------------------------------------------------------------------
// Cancellation and lease recovery
// ---------------------------------------------------------------------------

func TestCancellationLeaseRecovery(t *testing.T) {
	start := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	repo := &fakeRepository{}
	row := repo.addDefault(start)
	row.url = "http://127.0.0.1:1/closed"
	row.bearerToken = ""

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the attempt
	claimed, err := dispatcher.Step(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Step(cancelled) error = %v, want context.Canceled", err)
	}
	if !claimed {
		t.Fatal("Step(cancelled) claimed = false, want true (lease was taken)")
	}
	repo.mu.Lock()
	commitCount := len(repo.commits)
	repo.mu.Unlock()
	if commitCount != 0 {
		t.Fatalf("commits = %d after cancellation, want 0", commitCount)
	}
	if repo.status(row.delivery.ID) != outbox.StatusDelivering {
		t.Fatalf("status = %q, want delivering (lease in place)", repo.status(row.delivery.ID))
	}

	// Lease expiry makes the row claimable again; the delivery then succeeds.
	capture := newCaptureServer(200, "ok")
	defer capture.Close()
	row.url = capture.server.URL
	clock.Advance(31 * time.Second)
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step(after lease expiry) error = %v", err)
	}
	if got := repo.status(row.delivery.ID); got != outbox.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded after lease recovery", got)
	}
}

func TestCommitClaimLostIsBenign(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = "http://127.0.0.1:1/closed"
	row.bearerToken = ""
	repo.commitErrs = []error{outbox.ErrClaimLost}

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	claimed, err := dispatcher.Step(context.Background())
	if err != nil {
		t.Fatalf("Step() error = %v, want nil for claim-lost", err)
	}
	if !claimed {
		t.Fatal("Step() claimed = false, want true")
	}
}

// ---------------------------------------------------------------------------
// No raw URL/body/secret persistence
// ---------------------------------------------------------------------------

func TestNoRawURLBodyOrSecretPersistence(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = "http://127.0.0.1:1/closed?token=super-secret-query&sid=abc"
	row.hmacSecret = "super-secret-hmac"
	row.bearerToken = "super-secret-bearer"
	row.payload = []byte(`{"secret":"super-secret-body"}`)

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	repo.mu.Lock()
	committed := append([]outbox.Result(nil), repo.commits...)
	repo.mu.Unlock()
	if len(committed) != 1 {
		t.Fatalf("commits = %d, want 1", len(committed))
	}
	for _, result := range committed {
		for _, forbidden := range []string{"super-secret", "127.0.0.1", "token=", "sid="} {
			if strings.Contains(result.LastError, forbidden) {
				t.Fatalf("committed error %q leaks %q", result.LastError, forbidden)
			}
		}
	}
	if got := repo.lastError(row.delivery.ID); got != outbox.ErrorCategoryConnection {
		t.Fatalf("LastError = %q, want %q", got, outbox.ErrorCategoryConnection)
	}
}

func TestHTTPErrorBodyNotPersisted(t *testing.T) {
	capture := newCaptureServer(500, "internal secret body with password=xyz")
	defer capture.Close()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = capture.server.URL
	row.bearerToken = ""

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if got := repo.lastError(row.delivery.ID); got != "HTTP 500" {
		t.Fatalf("LastError = %q, want HTTP 500 (never the response body)", got)
	}
}

func TestInvalidEndpointURLRejected(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for _, bad := range []string{
		"ftp://example.test/hook",
		"http://user:pass@example.test/hook",
		"http://example.test/hook#frag",
		"not a url",
	} {
		clock := &fakeClock{now: now}
		repo := &fakeRepository{}
		row := repo.addDefault(now)
		row.url = bad
		dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
		if _, err := dispatcher.Step(context.Background()); err != nil {
			t.Fatalf("Step(%q) error = %v", bad, err)
		}
		if got := repo.lastError(row.delivery.ID); got != outbox.ErrorCategoryRequest {
			t.Fatalf("LastError(%q) = %q, want %q", bad, got, outbox.ErrorCategoryRequest)
		}
	}
}

// ---------------------------------------------------------------------------
// Request timeout classification
// ---------------------------------------------------------------------------

func TestDeliverRequestTimeout(t *testing.T) {
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer blocking.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = blocking.URL
	row.bearerToken = ""

	// A client-level timeout fires long before the dispatcher's own request
	// deadline, so the error must classify as "request timeout" either way.
	client := &http.Client{Timeout: 50 * time.Millisecond}
	dispatcher := newTestDispatcher(repo, client, clock, "unknown")
	if _, err := dispatcher.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if got := repo.lastError(row.delivery.ID); got != outbox.ErrorCategoryTimeout {
		t.Fatalf("LastError = %q, want %q", got, outbox.ErrorCategoryTimeout)
	}
	repo.mu.Lock()
	result := repo.commits[0]
	repo.mu.Unlock()
	if result.LastHTTPStatus != 0 {
		t.Fatalf("timeout HTTPStatus = %d, want 0", result.LastHTTPStatus)
	}
	if next := repo.nextAttemptAt(row.delivery.ID); next == nil {
		t.Fatal("NextAttemptAt nil, want scheduled retry after timeout")
	}
}

// ---------------------------------------------------------------------------
// Ordering and endpoint disable
// ---------------------------------------------------------------------------

func TestOrderingBlocksLaterRows(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}

	type flakyServer struct {
		mu   sync.Mutex
		hits int
	}
	flaky := &flakyServer{}
	flakyHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flaky.mu.Lock()
		flaky.hits++
		status := http.StatusInternalServerError
		if flaky.hits > 1 {
			status = http.StatusOK
		}
		flaky.mu.Unlock()
		w.WriteHeader(status)
	}))
	defer flakyHTTP.Close()
	capture := newCaptureServer(200, "ok")
	defer capture.Close()

	first := repo.addDefault(now)
	first.delivery.AggregateID = "same-download"
	first.url = flakyHTTP.URL
	second := repo.addDelivery(baseDelivery("download.completed", "evt_456"), []byte(`{"id":"evt_456"}`), capture.server.URL, "k", "")
	second.delivery.AggregateID = "same-download"

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")

	// Step 1: the earliest row is claimed and fails, scheduling a retry.
	if claimed, err := dispatcher.Step(context.Background()); err != nil || !claimed {
		t.Fatalf("Step 1 = (%v, %v), want (true, nil)", claimed, err)
	}
	if repo.status(first.delivery.ID) != outbox.StatusPending {
		t.Fatalf("first status = %q, want pending with scheduled retry after failure", repo.status(first.delivery.ID))
	}
	// Step 2: the later row is due but must wait for the earlier one.
	if claimed, err := dispatcher.Step(context.Background()); err != nil || claimed {
		t.Fatalf("Step 2 = (%v, %v), want (false, nil) while first row is delivering", claimed, err)
	}
	capture.mu.Lock()
	hitsAfterBlocked := capture.hits
	capture.mu.Unlock()
	if hitsAfterBlocked != 0 {
		t.Fatalf("later row delivered %d times while earlier row pending, want 0", hitsAfterBlocked)
	}

	// The earlier row retries and succeeds; only then may the later row run.
	next := repo.nextAttemptAt(first.delivery.ID)
	if next == nil {
		t.Fatal("first row has no scheduled retry")
	}
	clock.Advance(next.Sub(clock.Now()))
	clock.Advance(time.Nanosecond)
	if claimed, err := dispatcher.Step(context.Background()); err != nil || !claimed {
		t.Fatalf("Step 3 = (%v, %v), want (true, nil)", claimed, err)
	}
	if repo.status(first.delivery.ID) != outbox.StatusSucceeded {
		t.Fatalf("first status = %q, want succeeded after retry", repo.status(first.delivery.ID))
	}
	// succeeded rows do not block; the later row is now delivered.
	if claimed, err := dispatcher.Step(context.Background()); err != nil || !claimed {
		t.Fatalf("Step 4 = (%v, %v), want (true, nil)", claimed, err)
	}
	capture.mu.Lock()
	hitsAfterFirst := capture.hits
	capture.mu.Unlock()
	if hitsAfterFirst != 1 {
		t.Fatalf("later row hits = %d, want 1 after earlier row became terminal", hitsAfterFirst)
	}
}

func TestEndpointDisablePausesPending(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = "http://127.0.0.1:1/closed"
	repo.disableEndpoint(row.delivery.EndpointID)

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	claimed, err := dispatcher.Step(context.Background())
	if err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if claimed {
		t.Fatal("Step() claimed = true, want false while endpoint disabled")
	}
	if repo.status(row.delivery.ID) != outbox.StatusPending {
		t.Fatalf("status = %q, want pending", repo.status(row.delivery.ID))
	}
}

// ---------------------------------------------------------------------------
// Worker loop: idle wait, next due, run cancellation, pruning
// ---------------------------------------------------------------------------

func TestWorkerIdleWaitCappedAtMaxIdlePoll(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	dispatcher.config.MaxIdleWait = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		dispatcher.worker(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(clock.timerDurations()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker never polled")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	duration := clock.timerDurations()[0]
	if duration != 2*time.Second {
		t.Fatalf("idle wait = %v, want 2s (max idle poll)", duration)
	}
}

func TestWorkerSleepsUntilDueWhenSoon(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	row := repo.addDefault(now)
	row.url = "http://127.0.0.1:1/closed"
	due := now.Add(150 * time.Millisecond)
	row.delivery.NextAttemptAt = &due

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	dispatcher.config.MaxIdleWait = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		dispatcher.worker(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(clock.timerDurations()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker never polled")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	duration := clock.timerDurations()[0]
	if duration != 150*time.Millisecond {
		t.Fatalf("idle wait = %v, want 150ms (until due)", duration)
	}
}

func TestRunCancellation(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
}

func TestPruneTrigger(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	oldRow := repo.addDelivery(baseDelivery("download.completed", "evt_old"), []byte(`{}`), "http://example.test/hook", "k", "")
	oldRow.delivery.Status = outbox.StatusSucceeded
	oldRow.delivery.UpdatedAt = now.Add(-100 * 24 * time.Hour)
	fresh := repo.addDelivery(baseDelivery("download.completed", "evt_fresh"), []byte(`{}`), "http://example.test/hook", "k", "")
	fresh.delivery.Status = outbox.StatusSucceeded
	fresh.delivery.UpdatedAt = now.Add(-time.Hour)
	pending := repo.addDelivery(baseDelivery("download.completed", "evt_pending"), []byte(`{}`), "http://example.test/hook", "k", "")
	pending.delivery.Status = outbox.StatusPending

	dispatcher := newTestDispatcher(repo, NewHTTPClient(time.Second), clock, "unknown")
	dispatcher.maybePrune(context.Background(), now)
	repo.mu.Lock()
	pruned := append([]int64(nil), repo.pruned...)
	rows := len(repo.rows)
	repo.mu.Unlock()
	if len(pruned) != 1 || pruned[0] != oldRow.delivery.ID {
		t.Fatalf("pruned = %v, want only the 90-day-old succeeded row %d", pruned, oldRow.delivery.ID)
	}
	if rows != 2 {
		t.Fatalf("rows after prune = %d, want 2 (fresh succeeded and pending kept)", rows)
	}
	// Within the interval the trigger is a no-op.
	dispatcher.maybePrune(context.Background(), now.Add(30*time.Minute))
	repo.mu.Lock()
	pruneCalls := len(repo.pruned)
	repo.mu.Unlock()
	if pruneCalls != 1 {
		t.Fatalf("prune calls = %d, want 1 (interval respected)", pruneCalls)
	}
}
