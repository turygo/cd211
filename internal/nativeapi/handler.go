// Package nativeapi implements the authenticated native automation API:
// submission, query, terminal wait, and the completed/failed event pull feed.
// The handler decodes strict JSON/multipart bodies and maps every outcome to
// the stable native JSON contract; parsing, category lookup, destination paths,
// retained-content revival and persistence run in the shared submission.Service.
// The wait route and event feed block until a download reaches a terminal state
// or an event commits, observing the process-owned signal without a transaction.
// Auth.Middleware enforces the single native Bearer token at the mount point.
package nativeapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/outbox"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/submission"
	"github.com/turygo/cd211/internal/torrentmeta"
)

const (
	downloadsPath   = "/api/v1/downloads"
	downloadsPrefix = "/api/v1/downloads/"
	eventsPath      = "/api/v1/events"

	// Wait route timing bounds. The server WriteTimeout is 30s, so a wait
	// never exceeds 25s plus one bounded final read; the 1s floor keeps a
	// poll-style caller from hammering the store.
	defaultWaitTimeout = 25 * time.Second
	minWaitTimeout     = time.Second
	maxWaitTimeout     = 25 * time.Second

	// Event feed parameter bounds. The limit mirrors the qBittorrent path:
	// default 100, maximum 500, with one store lookahead row (max 501). wait
	// is a fixed absolute per-request deadline from 0 (single scan) up to
	// 25s, so the server WriteTimeout always covers it plus one bounded
	// final scan.
	defaultEventLimit = int64(100)
	minEventLimit     = int64(1)
	maxEventLimit     = int64(500)
	maxEventWait      = 25 * time.Second

	// eventCursorVersion is the version byte of the opaque event cursor.
	eventCursorVersion = 1

	// versionHeader carries the observed nonterminal row version on the
	// 204 timeout/shutdown answer, base-10.
	versionHeader = "X-CD211-Download-Version"
)

// Stable JSON error bodies. They are pre-rendered constants so error-path
// responses allocate nothing and never vary; like the auth errors they are
// always newline-terminated and served with Cache-Control: no-store.
const (
	invalidRequestBody       = "{\"error\":{\"code\":\"invalid_request\",\"message\":\"Request is invalid\"}}\n"
	downloadNotFoundBody     = "{\"error\":{\"code\":\"download_not_found\",\"message\":\"Download was not found\"}}\n"
	methodNotAllowedBody     = "{\"error\":{\"code\":\"method_not_allowed\",\"message\":\"Method is not allowed\"}}\n"
	requestTooLargeBody      = "{\"error\":{\"code\":\"request_too_large\",\"message\":\"Request is too large\"}}\n"
	unsupportedMediaTypeBody = "{\"error\":{\"code\":\"unsupported_media_type\",\"message\":\"Content type is not supported\"}}\n"
	invalidSubmissionBody    = "{\"error\":{\"code\":\"invalid_submission\",\"message\":\"Submission is invalid\"}}\n"
)

// Config carries the request bounds and lifecycle dependency for the native
// endpoints. The bounds mirror the qBittorrent adapter's limits so both
// surfaces accept the same maximum body size and in-memory multipart
// footprint; Shutdown is the generation lifecycle boundary for waiters.
type Config struct {
	MaxRequestBytes int64
	TorrentLimits   torrentmeta.Limits
	// Shutdown closes when the owning runtime generation is retired or the
	// process is shutting down. An in-flight wait then performs the same
	// final authoritative read as a timeout, so the caller receives a clean
	// retry boundary immediately instead of waiting out the 25s deadline.
	Shutdown <-chan struct{}
}

// Store is the narrow read boundary the query and event feed handlers need.
// *store.Store implements it; the auth middleware reads the API token from
// the same store on every request so generate/rotate/revoke applies
// immediately.
type Store interface {
	GetDownload(context.Context, string) (domain.Download, error)
	// LatestEventSequence returns the current high-water: the largest
	// committed domain event sequence, or 0 when no event exists yet.
	LatestEventSequence(context.Context) (int64, error)
	// ListDownloadEvents returns events after AfterSequence through
	// ThroughSequence for the requested download types/hash, ascending by
	// sequence, bounded by Limit.
	ListDownloadEvents(context.Context, outbox.EventQuery) ([]outbox.Event, error)
}

// EventSignal is the process-owned broadcast that waiters observe. Snapshot
// returns the current notification channel; the store closes it (and installs
// a fresh one) after any transaction that inserted a domain_events row commits
// successfully, so a waiter that snapshots before its authoritative query can
// never miss a transition committing between the two. The signal carries no
// event data and the database remains authoritative; *outbox.Signal implements
// this interface.
type EventSignal interface {
	Snapshot() <-chan struct{}
}

// Handler serves the configured native API routes: POST /api/v1/downloads,
// GET /api/v1/downloads/{hash}, GET /api/v1/downloads/{hash}/wait, and GET
// /api/v1/events. It must be wrapped in Auth.Middleware; the setup-mode
// placeholder handles the unauthenticated 503 surface instead.
type Handler struct {
	config  Config
	service *submission.Service
	repo    Store
	signal  EventSignal
}

// NewHandler constructs the native handler. service, repo, and signal must be
// non-nil and config.Shutdown must not be nil; any nil is a programming error,
// not a runtime fallback.
func NewHandler(config Config, service *submission.Service, repo Store, signal EventSignal) (*Handler, error) {
	if service == nil || repo == nil || signal == nil || config.Shutdown == nil {
		return nil, errors.New("nativeapi: submission service, store, event signal, or shutdown is nil")
	}
	if config.MaxRequestBytes <= 0 || config.TorrentLimits.MaxInputBytes <= 0 {
		return nil, errors.New("nativeapi: request limits are invalid")
	}
	if config.MaxRequestBytes < int64(config.TorrentLimits.MaxInputBytes)+(64<<10) {
		return nil, errors.New("nativeapi: request limit is too small")
	}
	return &Handler{config: config, service: service, repo: repo, signal: signal}, nil
}

// ServeHTTP routes within the /api/v1 surface. Only the Phase 2, 3, and 4
// resources exist: the downloads collection, a single hash resource, its wait
// sub-resource, and the event feed. Anything else is an unknown path.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == downloadsPath:
		h.serveDownloads(w, r)
	case r.URL.Path == eventsPath:
		h.serveEvents(w, r)
	case strings.HasPrefix(r.URL.Path, downloadsPrefix):
		remainder := r.URL.Path[len(downloadsPrefix):]
		if remainder == "" {
			downloadNotFound(w)
			return
		}
		hash, rest, hasRest := strings.Cut(remainder, "/")
		if hasRest {
			// Only the exact {hash}/wait sub-resource exists; any other
			// slash form is an unknown path.
			if rest != "wait" {
				downloadNotFound(w)
				return
			}
			canonical, ok := canonicalHash(hash)
			if !ok {
				invalidRequest(w)
				return
			}
			h.serveDownloadWait(w, r, canonical)
			return
		}
		canonical, ok := canonicalHash(hash)
		if !ok {
			invalidRequest(w)
			return
		}
		h.serveDownload(w, r, canonical)
	default:
		downloadNotFound(w)
	}
}

func (h *Handler) serveDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	h.submit(w, r)
}

func (h *Handler) serveDownload(w http.ResponseWriter, r *http.Request, hash string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			downloadNotFound(w)
			return
		}
		internalError(w)
		return
	}
	writeJSON(w, http.StatusOK, h.model(download))
}

// serveEvents implements GET /api/v1/events, the completed/failed event pull
// feed. The request carries optional cursor/types/hash/limit/wait parameters;
// each observation round snapshots the shared signal first, reads the
// high-water, resolves the cursor against it, and lists matching events.
// wait>0 long-polls on the signal with one fixed absolute timer: unrelated
// wakes re-scan without extending the deadline, while the timer and lifecycle
// shutdown both perform one final scan and answer its 200 page (possibly
// empty). Request cancellation returns without writing anything.
func (h *Handler) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	params, ok := parseEventsQuery(r)
	if !ok {
		invalidRequest(w)
		return
	}
	var timer *time.Timer
	if params.wait > 0 {
		timer = time.NewTimer(params.wait)
		defer timer.Stop()
	}
	cursor := params.cursor
	for {
		if r.Context().Err() != nil {
			return
		}
		// Snapshot before the authoritative high-water read: a commit
		// landing between the two closes this channel and is observed on
		// the next round.
		snapshot := h.signal.Snapshot()
		highWater, err := h.repo.LatestEventSequence(r.Context())
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			internalError(w)
			return
		}
		if params.cursorLatest {
			// latest waits only for events after the initial high-water.
			params.cursorLatest = false
			cursor = highWater
		} else if !params.cursorChecked && cursor > highWater {
			// A decoded cursor beyond the initial high-water is invalid.
			invalidRequest(w)
			return
		}
		params.cursorChecked = true
		events, err := h.repo.ListDownloadEvents(r.Context(), outbox.EventQuery{
			AfterSequence:    cursor,
			ThroughSequence:  highWater,
			IncludeCompleted: params.includeCompleted,
			IncludeFailed:    params.includeFailed,
			AggregateID:      params.hash,
			Limit:            params.limit + 1,
		})
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			internalError(w)
			return
		}
		if len(events) == 0 && timer != nil {
			// Advance the in-request cursor to this scan's high-water so
			// hidden types and filtered hashes are never re-scanned, then
			// park on the captured snapshot, the fixed deadline, runtime
			// shutdown, or the caller's cancellation.
			cursor = highWater
			select {
			case <-snapshot:
			case <-timer.C:
				h.finalEventsScan(w, r, cursor, params)
				return
			case <-h.config.Shutdown:
				h.finalEventsScan(w, r, cursor, params)
				return
			case <-r.Context().Done():
				return
			}
			continue
		}
		h.writeEvents(w, r, events, params.limit, highWater)
		return
	}
}

// finalEventsScan is the last observation round after the fixed deadline or
// runtime shutdown fired: snapshot, read the high-water, query from the
// advanced cursor, and write the resulting 200 page even when empty. Every
// successful empty answer encodes that scan's high-water as next_cursor.
func (h *Handler) finalEventsScan(w http.ResponseWriter, r *http.Request, cursor int64, params eventsQuery) {
	if r.Context().Err() != nil {
		return
	}
	h.signal.Snapshot()
	highWater, err := h.repo.LatestEventSequence(r.Context())
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		internalError(w)
		return
	}
	events, err := h.repo.ListDownloadEvents(r.Context(), outbox.EventQuery{
		AfterSequence:    cursor,
		ThroughSequence:  highWater,
		IncludeCompleted: params.includeCompleted,
		IncludeFailed:    params.includeFailed,
		AggregateID:      params.hash,
		Limit:            params.limit + 1,
	})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		internalError(w)
		return
	}
	h.writeEvents(w, r, events, params.limit, highWater)
}

// writeEvents validates every selected payload and writes the 200 page:
// items is always a JSON array (never null), each item pairs the cursor of
// its event's sequence with the stored payload verbatim, and next_cursor is
// the last returned sequence when has_more, otherwise the scan's high-water
// (so hidden types and filtered hashes advance without rescanning). Invalid
// JSON in any selected payload is storage corruption: the whole request maps
// to the stable 500 before any success header is written.
func (h *Handler) writeEvents(w http.ResponseWriter, r *http.Request, events []outbox.Event, limit int64, highWater int64) {
	if r.Context().Err() != nil {
		return
	}
	for _, event := range events {
		if !json.Valid(event.Payload) {
			internalError(w)
			return
		}
	}
	hasMore := int64(len(events)) > limit
	next := highWater
	if hasMore {
		events = events[:int(limit)]
		next = events[len(events)-1].Sequence
	}
	items := make([]eventItem, 0, len(events))
	for _, event := range events {
		items = append(items, eventItem{
			Cursor: encodeEventCursor(event.Sequence),
			Event:  event.Payload,
		})
	}
	authHeaders(w)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(eventsResponse{
		Items:      items,
		NextCursor: encodeEventCursor(next),
		HasMore:    hasMore,
	})
}

// serveDownloadWait implements GET /api/v1/downloads/{hash}/wait. A terminal
// download answers 200 with the exact query model immediately; otherwise the
// handler waits until a terminal transition commits, the optional timeout
// elapses, the runtime generation shuts down, or the caller disconnects.
// Timeout and shutdown both perform one final authoritative read so the
// response reflects the latest committed row; a cancelled request context
// returns without writing anything.
func (h *Handler) serveDownloadWait(w http.ResponseWriter, r *http.Request, hash string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	timeout, ok := parseWaitTimeout(r)
	if !ok {
		invalidRequest(w)
		return
	}
	// One timer per request with a fixed absolute deadline: an unrelated
	// event wakes the loop but never extends the wait.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if r.Context().Err() != nil {
			return
		}
		// Snapshot the signal before the authoritative query, so a commit
		// between the two is observed on this round.
		snapshot := h.signal.Snapshot()
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if errors.Is(err, store.ErrNotFound) {
				downloadNotFound(w)
				return
			}
			internalError(w)
			return
		}
		if terminal, _ := terminalOutcome(download.State); terminal {
			writeJSON(w, http.StatusOK, h.model(download))
			return
		}
		select {
		case <-snapshot:
			// A committed domain event (possibly unrelated): loop around
			// and snapshot before querying again.
		case <-timer.C:
			h.finalRead(w, r, hash)
			return
		case <-h.config.Shutdown:
			// Generation retirement or process shutdown: same final read
			// so an in-flight caller gets a clean retry boundary.
			h.finalRead(w, r, hash)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// parseWaitTimeout validates the optional wait query parameter. The only
// accepted parameter is a single "timeout" Go duration within [1s, 25s];
// absent means the 25s default. Empty, invalid, below-min, above-max,
// duplicate, and unknown parameters are all rejected.
func parseWaitTimeout(r *http.Request) (time.Duration, bool) {
	values := r.URL.Query()
	for name := range values {
		if name != "timeout" {
			return 0, false
		}
	}
	raw, present := values["timeout"]
	if !present {
		return defaultWaitTimeout, true
	}
	if len(raw) != 1 {
		return 0, false
	}
	timeout, err := time.ParseDuration(raw[0])
	if err != nil || timeout < minWaitTimeout || timeout > maxWaitTimeout {
		return 0, false
	}
	return timeout, true
}

// eventsQuery is the validated event feed request. cursor is the decoded
// sequence (0 when omitted, meaning the oldest retained event);
// cursorLatest marks the literal latest shorthand, resolved against the
// initial high-water; cursorChecked guards the one-time
// explicit-cursor-vs-high-water validation; hash is the canonical lowercase
// aggregate filter (empty means all hashes).
type eventsQuery struct {
	cursor           int64
	cursorLatest     bool
	cursorChecked    bool
	includeCompleted bool
	includeFailed    bool
	hash             string
	limit            int64
	wait             time.Duration
}

// parseEventsQuery validates every event feed query parameter syntactically,
// with no database access: unknown or duplicate parameters and any empty,
// malformed, or out-of-range value are rejected. Only the comma-separated
// types items trim surrounding whitespace; every other scalar rejects it.
func parseEventsQuery(r *http.Request) (eventsQuery, bool) {
	values := r.URL.Query()
	query := eventsQuery{includeCompleted: true, includeFailed: true, limit: defaultEventLimit}
	for name, raw := range values {
		if len(raw) != 1 {
			return eventsQuery{}, false
		}
		switch name {
		case "cursor":
			value := raw[0]
			if value == "" {
				return eventsQuery{}, false
			}
			if value == "latest" {
				query.cursorLatest = true
			} else {
				cursor, err := decodeEventCursor(value)
				if err != nil {
					return eventsQuery{}, false
				}
				query.cursor = cursor
			}
		case "types":
			completed, failed, ok := parseEventTypes(raw[0])
			if !ok {
				return eventsQuery{}, false
			}
			query.includeCompleted = completed
			query.includeFailed = failed
		case "hash":
			if strings.TrimSpace(raw[0]) != raw[0] {
				return eventsQuery{}, false
			}
			hash, ok := canonicalHash(raw[0])
			if !ok {
				return eventsQuery{}, false
			}
			query.hash = hash
		case "limit":
			limit, err := strconv.ParseInt(raw[0], 10, 64)
			if err != nil || limit < minEventLimit || limit > maxEventLimit {
				return eventsQuery{}, false
			}
			query.limit = limit
		case "wait":
			wait, err := time.ParseDuration(raw[0])
			if err != nil || wait < 0 || wait > maxEventWait {
				return eventsQuery{}, false
			}
			query.wait = wait
		default:
			return eventsQuery{}, false
		}
	}
	return query, true
}

// parseEventTypes validates the optional comma-separated types filter. Items
// trim surrounding whitespace; empty, unknown, and duplicate items are
// rejected. The zero value for both flags is returned only for an empty or
// invalid value; omitted means both types are included.
func parseEventTypes(value string) (completed, failed bool, ok bool) {
	if value == "" {
		return false, false, false
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		switch item {
		case outbox.EventTypeCompleted:
			if completed {
				return false, false, false
			}
			completed = true
		case outbox.EventTypeFailed:
			if failed {
				return false, false, false
			}
			failed = true
		default:
			return false, false, false
		}
	}
	return completed, failed, true
}

// finalRead is the authoritative post-wait read shared by timeout and
// lifecycle shutdown: terminal ->200 with the model, missing ->404, store
// failure ->500, otherwise 204 with the observed nonterminal row version. A
// request context cancelled during the read returns without writing.
func (h *Handler) finalRead(w http.ResponseWriter, r *http.Request, hash string) {
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			downloadNotFound(w)
			return
		}
		if r.Context().Err() != nil {
			return
		}
		internalError(w)
		return
	}
	if terminal, _ := terminalOutcome(download.State); terminal {
		writeJSON(w, http.StatusOK, h.model(download))
		return
	}
	waitTimeoutResponse(w, download.RowVersion)
}

// waitTimeoutResponse writes the 204 timeout/shutdown answer: empty body, no
// JSON content type, uncacheable, with the observed nonterminal version.
func waitTimeoutResponse(w http.ResponseWriter, version int64) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(versionHeader, strconv.FormatInt(version, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		unsupportedMediaType(w)
		return
	}
	switch mediaType {
	case "application/json":
		h.submitJSON(w, r)
	case "multipart/form-data":
		h.submitMultipart(w, r)
	default:
		unsupportedMediaType(w)
	}
}

// magnetRequest is the strict JSON magnet schema. Pointer fields distinguish
// an absent optional field from an explicit value; magnet is required.
type magnetRequest struct {
	Magnet   *string `json:"magnet"`
	Category *string `json:"category"`
	Stopped  *bool   `json:"stopped"`
}

func (h *Handler) submitJSON(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.config.MaxRequestBytes)
	var request magnetRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		if tooLarge(err) {
			requestTooLarge(w)
			return
		}
		invalidRequest(w)
		return
	}
	if decoder.More() {
		invalidRequest(w)
		return
	}
	if request.Magnet == nil {
		invalidRequest(w)
		return
	}
	category := ""
	if request.Category != nil {
		category = *request.Category
	}
	stopped := false
	if request.Stopped != nil {
		stopped = *request.Stopped
	}
	download, created, err := h.service.SubmitMagnet(r.Context(), *request.Magnet, category, stopped)
	h.submitResult(w, download, created, err)
}

func (h *Handler) submitMultipart(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.config.MaxRequestBytes)
	if err := r.ParseMultipartForm(int64(h.config.TorrentLimits.MaxInputBytes)); err != nil {
		if tooLarge(err) {
			requestTooLarge(w)
			return
		}
		invalidRequest(w)
		return
	}
	defer r.MultipartForm.RemoveAll()

	category, valid := multipartScalar(r.MultipartForm.Value, "category")
	if !valid {
		invalidRequest(w)
		return
	}
	stopped, valid := multipartStopped(r.MultipartForm.Value)
	if !valid {
		invalidRequest(w)
		return
	}
	for name := range r.MultipartForm.Value {
		if name != "category" && name != "stopped" {
			invalidRequest(w)
			return
		}
	}
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["torrent"]) != 1 {
		invalidRequest(w)
		return
	}
	data, err := readUpload(r.MultipartForm.File["torrent"][0])
	if err != nil {
		invalidRequest(w)
		return
	}
	download, created, err := h.service.SubmitTorrent(r.Context(), data, category, stopped)
	h.submitResult(w, download, created, err)
}

// submitResult maps a service outcome: 201 with Location for a new or revived
// row, 200 with the same Location for an untouched duplicate, 422 for invalid
// submissions, and a stable 500 for anything else.
func (h *Handler) submitResult(w http.ResponseWriter, download domain.Download, created bool, err error) {
	if err != nil {
		if errors.Is(err, submission.ErrInvalidSource) || errors.Is(err, submission.ErrCategoryInvalid) {
			invalidSubmission(w)
			return
		}
		internalError(w)
		return
	}
	w.Header().Set("Location", "/api/v1/downloads/"+download.Hash)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, submitResponse{Created: created, Download: h.model(download)})
}

// multipartScalar extracts an optional scalar value field: absent is valid,
// exactly one value is required when present.
func multipartScalar(values map[string][]string, name string) (string, bool) {
	raw, present := values[name]
	if !present {
		return "", true
	}
	if len(raw) != 1 {
		return "", false
	}
	return raw[0], true
}

// multipartStopped parses the optional stopped scalar, accepting only
// true/false/1/0 case-insensitively. An empty or unknown value is invalid.
func multipartStopped(values map[string][]string) (bool, bool) {
	raw, present := values["stopped"]
	if !present {
		return false, true
	}
	if len(raw) != 1 {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(raw[0])) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}

func readUpload(header *multipart.FileHeader) ([]byte, error) {
	if header == nil {
		return nil, errors.New("missing upload")
	}
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func tooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

// canonicalHash normalizes a hash path segment: exactly 40 hexadecimal
// characters, lowercased. Anything else is invalid hash syntax.
func canonicalHash(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if len(value) != 40 {
		return "", false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return "", false
		}
	}
	return value, true
}

// encodeEventCursor packs a sequence into its opaque cursor form: exactly 9
// bytes, a version byte (1) followed by the unsigned big-endian sequence,
// encoded raw URL-safe base64 without padding. The sequence is the durable
// SQLite AUTOINCREMENT order, never a timestamp or event ID.
func encodeEventCursor(sequence int64) string {
	raw := make([]byte, 9)
	raw[0] = eventCursorVersion
	binary.BigEndian.PutUint64(raw[1:], uint64(sequence))
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeEventCursor unpacks a cursor produced by encodeEventCursor. It uses
// strict canonical base64 decoding (rejecting padding and nonzero trailing
// pad bits), then exact length, version, and sequence-range checks. The
// literal "latest" is input shorthand only and never reaches the decoder.
func decodeEventCursor(value string) (int64, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != 9 {
		return 0, errors.New("event cursor is malformed")
	}
	if raw[0] != eventCursorVersion {
		return 0, errors.New("event cursor version is invalid")
	}
	sequence := binary.BigEndian.Uint64(raw[1:])
	if sequence > math.MaxInt64 {
		return 0, errors.New("event cursor sequence overflows")
	}
	return int64(sequence), nil
}

// eventItem pairs one immutable event with the client cursor equal to its
// sequence. The event is the stored payload nested verbatim.
type eventItem struct {
	Cursor string          `json:"cursor"`
	Event  json.RawMessage `json:"event"`
}

// eventsResponse is the exact event feed page contract: items is always a
// JSON array (never null), next_cursor continues pagination, and has_more
// tells the caller whether another page exists beyond the returned items.
type eventsResponse struct {
	Items      []eventItem `json:"items"`
	NextCursor string      `json:"next_cursor"`
	HasMore    bool        `json:"has_more"`
}

func errorJSON(w http.ResponseWriter, status int, body string) {
	authHeaders(w)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func invalidRequest(w http.ResponseWriter)   { errorJSON(w, http.StatusBadRequest, invalidRequestBody) }
func downloadNotFound(w http.ResponseWriter) { errorJSON(w, http.StatusNotFound, downloadNotFoundBody) }
func requestTooLarge(w http.ResponseWriter) {
	errorJSON(w, http.StatusRequestEntityTooLarge, requestTooLargeBody)
}
func unsupportedMediaType(w http.ResponseWriter) {
	errorJSON(w, http.StatusUnsupportedMediaType, unsupportedMediaTypeBody)
}
func invalidSubmission(w http.ResponseWriter) {
	errorJSON(w, http.StatusUnprocessableEntity, invalidSubmissionBody)
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	errorJSON(w, http.StatusMethodNotAllowed, methodNotAllowedBody)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type submitResponse struct {
	Created  bool          `json:"created"`
	Download downloadModel `json:"download"`
}

// downloadModel is the exact Phase 2 query/submit model. Every field is always
// present; pointer fields are null when absent. error_code and next_retry_at
// are additive: existing consumers keep reading the sanitized error text and
// the unchanged response semantics.
type downloadModel struct {
	Hash            string     `json:"hash"`
	Name            string     `json:"name"`
	Category        string     `json:"category"`
	State           string     `json:"state"`
	Progress        float64    `json:"progress"`
	Version         int64      `json:"version"`
	Terminal        bool       `json:"terminal"`
	Outcome         *string    `json:"outcome"`
	CloudResultPath *string    `json:"cloud_result_path"`
	CopySourcePath  *string    `json:"copy_source_path"`
	ContentPath     *string    `json:"content_path"`
	TotalSize       int64      `json:"total_size"`
	Error           *string    `json:"error"`
	ErrorCode       *string    `json:"error_code"`
	NextRetryAt     *time.Time `json:"next_retry_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	Links           linksView  `json:"links"`
}

type linksView struct {
	Self string `json:"self"`
	Wait string `json:"wait"`
}

func (h *Handler) model(download domain.Download) downloadModel {
	terminal, outcome := terminalOutcome(download.State)
	var cloudResultPath, copySourcePath, contentPath *string
	if download.CloudResultPath != "" {
		cloudResultPath = &download.CloudResultPath
	}
	if download.CopySourcePath != "" {
		copySourcePath = &download.CopySourcePath
	}
	if download.ContentPath != "" {
		contentPath = &download.ContentPath
	}
	var errorText *string
	if text := domain.SanitizeDownloadError(download); text != "" {
		errorText = &text
	}
	var errorCode *string
	if download.LastErrorCode != "" {
		code := download.LastErrorCode
		errorCode = &code
	}
	var nextRetryAt *time.Time
	if download.NextRunAt != nil && (download.LastError != "" || download.LastErrorCode != "") {
		utc := download.NextRunAt.UTC()
		nextRetryAt = &utc
	}
	var completedAt *time.Time
	if download.CompletedAt != nil {
		utc := download.CompletedAt.UTC()
		completedAt = &utc
	}
	return downloadModel{
		Hash:            download.Hash,
		Name:            download.Name,
		Category:        download.Category,
		State:           string(download.State),
		Progress:        projectProgress(download),
		Version:         download.RowVersion,
		Terminal:        terminal,
		Outcome:         outcome,
		CloudResultPath: cloudResultPath,
		CopySourcePath:  copySourcePath,
		ContentPath:     contentPath,
		TotalSize:       download.TotalSize,
		Error:           errorText,
		ErrorCode:       errorCode,
		NextRetryAt:     nextRetryAt,
		CreatedAt:       download.CreatedAt.UTC(),
		UpdatedAt:       download.UpdatedAt.UTC(),
		CompletedAt:     completedAt,
		Links: linksView{
			Self: "/api/v1/downloads/" + download.Hash,
			Wait: "/api/v1/downloads/" + download.Hash + "/wait",
		},
	}
}

// projectProgress uses the qBittorrent-compatible projection when the download
// is projectable (all visible states), and falls back to the bounded persisted
// qbit progress otherwise (hidden deletion states and unprojectable rows).
func projectProgress(download domain.Download) float64 {
	projected, err := domain.Project(download)
	if err == nil {
		return projected.Progress
	}
	return math.Max(0, math.Min(1, download.QbitProgress))
}

// terminalOutcome maps the four terminal outcomes. STOPPED and the request
// states are nonterminal: terminal false and outcome null.
func terminalOutcome(state domain.State) (bool, *string) {
	switch state {
	case domain.StateCompleted:
		return true, stringPointer("completed")
	case domain.StateFailed:
		return true, stringPointer("failed")
	case domain.StateCancelled:
		return true, stringPointer("cancelled")
	case domain.StateDeleted:
		return true, stringPointer("deleted")
	default:
		return false, nil
	}
}

func stringPointer(value string) *string {
	return &value
}
