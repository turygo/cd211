package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/outbox"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const maxDeliveryErrorBytes = 128

var endpointEventTypes = []string{outbox.EventTypeCompleted, outbox.EventTypeFailed}

// Compile-time checks that the store satisfies the outbox consumer contracts.
var (
	_ outbox.Repository         = (*Store)(nil)
	_ outbox.EndpointRepository = (*Store)(nil)
)

// CreateWebhookEndpoint validates the input, generates an HMAC secret, and
// persists the endpoint with its subscriptions. The returned endpoint carries
// the generated secret for the one-time reveal; ordinary reads never do.
func (s *Store) CreateWebhookEndpoint(ctx context.Context, input outbox.EndpointInput, now time.Time) (outbox.Endpoint, error) {
	if now.IsZero() {
		return outbox.Endpoint{}, errors.New("endpoint create time is required")
	}
	input.Name = strings.TrimSpace(input.Name)
	if err := outbox.ValidateEndpointInput(input); err != nil {
		return outbox.Endpoint{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return outbox.Endpoint{}, fmt.Errorf("begin endpoint create: %w", err)
	}
	queries := s.queries.WithTx(tx)
	row, err := queries.InsertEndpoint(ctx, storedb.InsertEndpointParams{
		Name:        input.Name,
		Url:         input.URL,
		HmacSecret:  outbox.NewHMACSecret(),
		BearerToken: nullableString(strings.TrimSpace(input.BearerToken)),
		Enabled:     endpointEnabled(input.Enabled, true),
		CreatedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
	})
	if err != nil {
		_ = tx.Rollback()
		if endpointNameConflict(err) {
			return outbox.Endpoint{}, outbox.ErrNameConflict
		}
		return outbox.Endpoint{}, fmt.Errorf("create webhook endpoint: %w", err)
	}
	if _, err := reconcileSubscriptions(ctx, queries, row.ID, input.SubscribeCompleted, input.SubscribeFailed); err != nil {
		_ = tx.Rollback()
		return outbox.Endpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return outbox.Endpoint{}, fmt.Errorf("commit webhook endpoint create: %w", err)
	}
	return endpointFromDB(row, input.SubscribeCompleted, input.SubscribeFailed, true), nil
}

// UpdateWebhookEndpoint mutates the endpoint identity, resolving the
// preserve-on-empty URL and bearer semantics, and cancels pending/dead
// deliveries of any subscription that was removed.
func (s *Store) UpdateWebhookEndpoint(ctx context.Context, id int64, input outbox.EndpointInput, now time.Time) (outbox.Endpoint, error) {
	if id <= 0 || now.IsZero() {
		return outbox.Endpoint{}, errors.New("endpoint id or update time is invalid")
	}
	input.Name = strings.TrimSpace(input.Name)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return outbox.Endpoint{}, fmt.Errorf("begin endpoint update: %w", err)
	}
	queries := s.queries.WithTx(tx)
	current, err := queries.GetEndpoint(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return outbox.Endpoint{}, outbox.ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return outbox.Endpoint{}, fmt.Errorf("read webhook endpoint: %w", err)
	}
	// An empty URL or bearer input preserves the stored value: resolve the
	// merged identity BEFORE validating so preserve-on-empty updates pass
	// validation. Subscriptions are authoritative as given. A nil Enabled
	// preserves the stored enabled state; a non-nil pointer sets it exactly,
	// atomically with the rest of the write.
	url := input.URL
	if url == "" {
		url = current.Url
	}
	bearer := current.BearerToken
	switch {
	case input.ClearBearerToken:
		bearer = sql.NullString{}
	case input.BearerToken != "":
		bearer = nullableString(strings.TrimSpace(input.BearerToken))
	}
	input.URL = url
	input.BearerToken = nullString(bearer)
	if err := outbox.ValidateEndpointInput(input); err != nil {
		_ = tx.Rollback()
		return outbox.Endpoint{}, err
	}
	row, err := queries.UpdateEndpoint(ctx, storedb.UpdateEndpointParams{
		Name: input.Name, Url: url, BearerToken: bearer,
		Enabled: endpointEnabled(input.Enabled, current.Enabled == 1), UpdatedAt: now.UTC(), ID: id,
	})
	if err != nil {
		_ = tx.Rollback()
		if endpointNameConflict(err) {
			return outbox.Endpoint{}, outbox.ErrNameConflict
		}
		return outbox.Endpoint{}, fmt.Errorf("update webhook endpoint: %w", err)
	}
	removed, err := reconcileSubscriptions(ctx, queries, id, input.SubscribeCompleted, input.SubscribeFailed)
	if err != nil {
		_ = tx.Rollback()
		return outbox.Endpoint{}, err
	}
	for _, eventType := range removed {
		if err := queries.CancelSubscriptionDeliveries(ctx, storedb.CancelSubscriptionDeliveriesParams{
			EndpointID: id, EventType: eventType, UpdatedAt: now.UTC(),
		}); err != nil {
			_ = tx.Rollback()
			return outbox.Endpoint{}, fmt.Errorf("cancel removed subscription deliveries: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return outbox.Endpoint{}, fmt.Errorf("commit webhook endpoint update: %w", err)
	}
	return endpointFromDB(row, input.SubscribeCompleted, input.SubscribeFailed, false), nil
}

// ListWebhookEndpoints returns enabled and soft-deleted-absent endpoints with
// secrets and raw URLs omitted.
func (s *Store) ListWebhookEndpoints(ctx context.Context) ([]outbox.Endpoint, error) {
	rows, err := s.queries.ListEndpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("list webhook endpoints: %w", err)
	}
	subscriptions, err := s.queries.ListEndpointSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list webhook endpoint subscriptions: %w", err)
	}
	byEndpoint := make(map[int64]map[string]bool)
	for _, subscription := range subscriptions {
		if byEndpoint[subscription.EndpointID] == nil {
			byEndpoint[subscription.EndpointID] = make(map[string]bool)
		}
		byEndpoint[subscription.EndpointID][subscription.EventType] = true
	}
	endpoints := make([]outbox.Endpoint, 0, len(rows))
	for _, row := range rows {
		subs := byEndpoint[row.ID]
		endpoints = append(endpoints, endpointFromDB(row, subs[outbox.EventTypeCompleted], subs[outbox.EventTypeFailed], false))
	}
	return endpoints, nil
}

// GetWebhookEndpoint returns one non-deleted endpoint with secrets and the
// raw URL omitted.
func (s *Store) GetWebhookEndpoint(ctx context.Context, id int64) (outbox.Endpoint, error) {
	row, err := s.queries.GetEndpoint(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Endpoint{}, outbox.ErrNotFound
	}
	if err != nil {
		return outbox.Endpoint{}, fmt.Errorf("get webhook endpoint: %w", err)
	}
	subscriptions, err := s.queries.GetEndpointSubscriptions(ctx, id)
	if err != nil {
		return outbox.Endpoint{}, fmt.Errorf("get webhook endpoint subscriptions: %w", err)
	}
	completed, failed := subscriptionFlags(subscriptions)
	return endpointFromDB(row, completed, failed, false), nil
}

// SetWebhookEndpointEnabled disables or re-enables an endpoint. Disabling
// pauses pending deliveries (claims skip disabled endpoints) without touching
// them; re-enabling resumes them.
func (s *Store) SetWebhookEndpointEnabled(ctx context.Context, id int64, enabled bool, now time.Time) error {
	if id <= 0 || now.IsZero() {
		return errors.New("endpoint id or enable time is invalid")
	}
	value := int64(0)
	if enabled {
		value = 1
	}
	updated, err := s.queries.SetEndpointEnabled(ctx, storedb.SetEndpointEnabledParams{
		Enabled: value, UpdatedAt: now.UTC(), ID: id,
	})
	if err != nil {
		return fmt.Errorf("set webhook endpoint enabled: %w", err)
	}
	if updated == 0 {
		return outbox.ErrNotFound
	}
	return nil
}

// RotateWebhookEndpointSecret replaces the HMAC secret and returns the
// endpoint with the replacement for the one-time reveal.
func (s *Store) RotateWebhookEndpointSecret(ctx context.Context, id int64, now time.Time) (outbox.Endpoint, error) {
	if id <= 0 || now.IsZero() {
		return outbox.Endpoint{}, errors.New("endpoint id or rotate time is invalid")
	}
	row, err := s.queries.RotateEndpointSecret(ctx, storedb.RotateEndpointSecretParams{
		HmacSecret: outbox.NewHMACSecret(), UpdatedAt: now.UTC(), ID: id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Endpoint{}, outbox.ErrNotFound
	}
	if err != nil {
		return outbox.Endpoint{}, fmt.Errorf("rotate webhook endpoint secret: %w", err)
	}
	subscriptions, err := s.queries.GetEndpointSubscriptions(ctx, id)
	if err != nil {
		return outbox.Endpoint{}, fmt.Errorf("get webhook endpoint subscriptions: %w", err)
	}
	completed, failed := subscriptionFlags(subscriptions)
	return endpointFromDB(row, completed, failed, true), nil
}

// DeleteWebhookEndpoint soft-deletes an endpoint, disables it, and cancels its
// pending/dead deliveries. It is idempotent: deleting an already deleted
// endpoint succeeds; deleting a missing endpoint returns outbox.ErrNotFound.
func (s *Store) DeleteWebhookEndpoint(ctx context.Context, id int64, now time.Time) error {
	if id <= 0 || now.IsZero() {
		return errors.New("endpoint id or delete time is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin endpoint delete: %w", err)
	}
	queries := s.queries.WithTx(tx)
	updated, err := queries.DeleteEndpoint(ctx, storedb.DeleteEndpointParams{
		DeletedAt: sql.NullTime{Time: now.UTC(), Valid: true}, UpdatedAt: now.UTC(), ID: id,
	})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete webhook endpoint: %w", err)
	}
	if updated == 0 {
		exists, err := queries.EndpointExists(ctx, id)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check webhook endpoint existence: %w", err)
		}
		if !exists {
			_ = tx.Rollback()
			return outbox.ErrNotFound
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit idempotent webhook endpoint delete: %w", err)
		}
		return nil
	}
	if err := queries.CancelEndpointDeliveries(ctx, storedb.CancelEndpointDeliveriesParams{
		EndpointID: id, UpdatedAt: now.UTC(),
	}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("cancel deleted endpoint deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit webhook endpoint delete: %w", err)
	}
	return nil
}

// ListWebhookDeliveries returns delivery history newest-first with exact
// endpoint/event/status filters and opaque cursor pagination. Limit defaults
// to 50 and is capped at 100; larger values are rejected. A malformed cursor
// or unknown status is rejected rather than silently broadening the query.
func (s *Store) ListWebhookDeliveries(ctx context.Context, filter outbox.DeliveryFilter) ([]outbox.Delivery, outbox.Page, error) {
	limit := filter.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, outbox.Page{}, errors.New("delivery limit must be between 1 and 100")
	}
	if filter.EndpointID != nil && *filter.EndpointID <= 0 {
		return nil, outbox.Page{}, errors.New("delivery endpoint filter is invalid")
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return nil, outbox.Page{}, errors.New("delivery status filter is invalid")
	}
	params := storedb.ListDeliveriesParams{
		EndpointID: sql.NullInt64{},
		EventType:  nullableString(filter.EventType),
		Status:     nullableString(string(filter.Status)),
		CursorID:   -1,
		Limit:      int64(limit + 1),
	}
	if filter.EndpointID != nil {
		params.EndpointID = sql.NullInt64{Int64: *filter.EndpointID, Valid: true}
	}
	if filter.Cursor != "" {
		cursorTime, cursorID, err := outbox.DecodeCursor(filter.Cursor)
		if err != nil {
			return nil, outbox.Page{}, errors.New("delivery cursor is invalid")
		}
		params.CursorTime = sql.NullTime{Time: cursorTime, Valid: true}
		params.CursorID = cursorID
	}
	rows, err := s.queries.ListDeliveries(ctx, params)
	if err != nil {
		return nil, outbox.Page{}, fmt.Errorf("list webhook deliveries: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	deliveries := make([]outbox.Delivery, 0, len(rows))
	for _, row := range rows {
		delivery, err := deliveryFromDB(row)
		if err != nil {
			return nil, outbox.Page{}, err
		}
		deliveries = append(deliveries, delivery)
	}
	page := outbox.Page{}
	if hasMore {
		last := deliveries[len(deliveries)-1]
		page.NextCursor = outbox.EncodeCursor(last.CreatedAt, last.ID)
		page.HasMore = true
	}
	return deliveries, page, nil
}

// LatestEventSequence returns the current high-water: the maximum durable
// domain event sequence, or 0 when no events exist. It is the authoritative
// snapshot bound that event feed scans are capped by.
func (s *Store) LatestEventSequence(ctx context.Context) (int64, error) {
	sequence, err := s.queries.LatestEventSequence(ctx)
	if err != nil {
		return 0, fmt.Errorf("read latest event sequence: %w", err)
	}
	return sequence, nil
}

// ListDownloadEvents returns completed/failed download events with durable
// sequence strictly greater than AfterSequence and at most ThroughSequence,
// in ascending sequence order, bounded by Limit (1..outbox.MaxEventFeedLimit;
// 501 is the HTTP lookahead bound). AggregateID optionally filters one
// download hash. The query validates nonnegative/ordered bounds, the limit,
// and that at least one event type is requested; rows are converted exactly,
// preserving the immutable payload bytes.
func (s *Store) ListDownloadEvents(ctx context.Context, query outbox.EventQuery) ([]outbox.Event, error) {
	if query.AfterSequence < 0 {
		return nil, errors.New("event feed after sequence is negative")
	}
	if query.ThroughSequence < 0 {
		return nil, errors.New("event feed through sequence is negative")
	}
	if query.AfterSequence > query.ThroughSequence {
		return nil, errors.New("event feed sequence range is inverted")
	}
	if query.Limit < 1 || query.Limit > outbox.MaxEventFeedLimit {
		return nil, errors.New("event feed limit must be between 1 and " + strconv.Itoa(outbox.MaxEventFeedLimit))
	}
	if !query.IncludeCompleted && !query.IncludeFailed {
		return nil, errors.New("event feed requires at least one event type")
	}
	completedType := ""
	if query.IncludeCompleted {
		completedType = outbox.EventTypeCompleted
	}
	failedType := ""
	if query.IncludeFailed {
		failedType = outbox.EventTypeFailed
	}
	rows, err := s.queries.ListDownloadEvents(ctx, storedb.ListDownloadEventsParams{
		AfterSequence:   query.AfterSequence,
		ThroughSequence: query.ThroughSequence,
		CompletedType:   nullableString(completedType),
		FailedType:      nullableString(failedType),
		AggregateID:     nullableString(query.AggregateID),
		Limit:           query.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list download events: %w", err)
	}
	events := make([]outbox.Event, 0, len(rows))
	for _, row := range rows {
		event, err := eventFromDB(row)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

// GetWebhookDelivery returns one delivery row.
func (s *Store) GetWebhookDelivery(ctx context.Context, id int64) (outbox.Delivery, error) {
	row, err := s.queries.GetDelivery(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Delivery{}, outbox.ErrNotFound
	}
	if err != nil {
		return outbox.Delivery{}, fmt.Errorf("get webhook delivery: %w", err)
	}
	return deliveryFromDB(row)
}

// ReplayWebhookDelivery reopens a dead delivery on an enabled, non-deleted
// endpoint, resetting its attempts, lease, and error state and starting a
// fresh 24-hour window. The (event_id, endpoint_id) row is not duplicated.
func (s *Store) ReplayWebhookDelivery(ctx context.Context, id int64, now time.Time) (outbox.Delivery, error) {
	if id <= 0 || now.IsZero() {
		return outbox.Delivery{}, errors.New("delivery id or replay time is invalid")
	}
	row, err := s.queries.ReplayDelivery(ctx, storedb.ReplayDeliveryParams{Now: sql.NullTime{Time: now.UTC(), Valid: true}, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Delivery{}, outbox.ErrNotFound
	}
	if err != nil {
		return outbox.Delivery{}, fmt.Errorf("replay webhook delivery: %w", err)
	}
	return deliveryFromDB(row)
}

// EnqueueTestDelivery persists a durable webhook.test event and exactly one
// targeted delivery through the normal outbox path, bypassing subscriptions.
// It works on disabled (but non-deleted) endpoints; the delivery then follows
// the same pause rules as ordinary deliveries.
func (s *Store) EnqueueTestDelivery(ctx context.Context, endpointID int64, now time.Time) (outbox.Delivery, error) {
	if endpointID <= 0 || now.IsZero() {
		return outbox.Delivery{}, errors.New("endpoint id or test time is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return outbox.Delivery{}, fmt.Errorf("begin test delivery: %w", err)
	}
	queries := s.queries.WithTx(tx)
	endpoint, err := queries.GetEndpoint(ctx, endpointID)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return outbox.Delivery{}, outbox.ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return outbox.Delivery{}, fmt.Errorf("read test endpoint: %w", err)
	}
	eventID := outbox.NewEventID()
	payload, err := outbox.BuildTestPayload(eventID, endpoint.ID, endpoint.Name, now)
	if err != nil {
		_ = tx.Rollback()
		return outbox.Delivery{}, err
	}
	aggregateID := strconv.FormatInt(endpoint.ID, 10)
	if err := queries.InsertEvent(ctx, storedb.InsertEventParams{
		ID:               eventID,
		Type:             outbox.EventTypeTest,
		AggregateType:    outbox.AggregateWebhookEndpoint,
		AggregateID:      aggregateID,
		AggregateVersion: endpoint.RowVersion,
		Payload:          payload,
		OccurredAt:       now.UTC(),
	}); err != nil {
		_ = tx.Rollback()
		return outbox.Delivery{}, fmt.Errorf("insert webhook.test event: %w", err)
	}
	delivery, err := queries.InsertDelivery(ctx, storedb.InsertDeliveryParams{
		EventID:       eventID,
		EndpointID:    endpoint.ID,
		EndpointName:  endpoint.Name,
		EventType:     outbox.EventTypeTest,
		AggregateType: outbox.AggregateWebhookEndpoint,
		AggregateID:   aggregateID,
		NextAttemptAt: sql.NullTime{Time: now.UTC(), Valid: true},
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	})
	if err != nil {
		_ = tx.Rollback()
		return outbox.Delivery{}, fmt.Errorf("insert webhook.test delivery: %w", err)
	}
	if err := s.commitEventTx(tx, true); err != nil {
		return outbox.Delivery{}, fmt.Errorf("commit webhook.test delivery: %w", err)
	}
	return deliveryFromDB(delivery)
}

// ClaimWebhookDue atomically leases the earliest due delivery: it skips
// disabled/deleted endpoints and any row whose endpoint+aggregate still has an
// earlier pending/delivering delivery, sets the lease, increments the attempt
// count, and stamps first_attempt_at when absent. It returns nil when nothing
// is claimable.
func (s *Store) ClaimWebhookDue(ctx context.Context, owner string, now time.Time, leaseDuration time.Duration) (*outbox.Claim, error) {
	if strings.TrimSpace(owner) == "" || leaseDuration <= 0 || now.IsZero() {
		return nil, errors.New("claim owner, time, or lease duration is invalid")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin webhook claim: %w", err)
	}
	queries := s.queries.WithTx(tx)
	row, err := queries.ClaimWebhookDue(ctx, storedb.ClaimWebhookDueParams{
		Now:        now,
		Owner:      sql.NullString{String: owner, Valid: true},
		LeaseUntil: sql.NullTime{Time: now.Add(leaseDuration).UTC(), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return nil, nil
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("claim due webhook delivery: %w", err)
	}
	delivery, err := deliveryFromDB(row)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	event, err := queries.GetEvent(ctx, delivery.EventID)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("read claimed event: %w", err)
	}
	endpoint, err := queries.GetEndpointRaw(ctx, delivery.EndpointID)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("read claimed endpoint: %w", err)
	}
	if endpoint.DeletedAt.Valid {
		_ = tx.Rollback()
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit webhook claim: %w", err)
	}
	return &outbox.Claim{
		DeliveryID:     delivery.ID,
		Owner:          owner,
		Version:        delivery.RowVersion,
		EndpointID:     delivery.EndpointID,
		EventID:        delivery.EventID,
		EventType:      delivery.EventType,
		Payload:        event.Payload,
		URL:            endpoint.Url,
		HMACSecret:     endpoint.HmacSecret,
		BearerToken:    nullString(endpoint.BearerToken),
		AttemptCount:   delivery.AttemptCount,
		FirstAttemptAt: delivery.FirstAttemptAt,
	}, nil
}

// CommitWebhookClaim is the CAS commit of one claimed HTTP attempt. It
// requires the lease identity (id, owner, row version) and the row to still be
// delivering. A succeeded/pending-retry/dead outcome is persisted as given by
// the dispatcher; if the endpoint was soft-deleted meanwhile, the outcome
// resolves to cancelled regardless of the HTTP result. A stale claim returns
// outbox.ErrClaimLost.
func (s *Store) CommitWebhookClaim(ctx context.Context, claim outbox.Claim, result outbox.Result, now time.Time) error {
	if claim.DeliveryID <= 0 || claim.EndpointID <= 0 || strings.TrimSpace(claim.Owner) == "" || claim.Version < 0 || now.IsZero() {
		return outbox.ErrClaimLost
	}
	switch result.Status {
	case outbox.StatusSucceeded:
		if result.DeliveredAt == nil || result.DeliveredAt.IsZero() {
			return errors.New("succeeded delivery requires a delivered time")
		}
	case outbox.StatusPending:
		if result.NextAttemptAt == nil || result.NextAttemptAt.IsZero() {
			return errors.New("pending delivery requires a next attempt time")
		}
	case outbox.StatusDead:
		if result.NextAttemptAt != nil {
			return errors.New("dead delivery must not schedule a next attempt")
		}
	default:
		return errors.New("delivery commit status is invalid")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin webhook commit: %w", err)
	}
	queries := s.queries.WithTx(tx)
	deletedAt, err := queries.GetEndpointDeletedAt(ctx, claim.EndpointID)
	if errors.Is(err, sql.ErrNoRows) {
		// The endpoint row cannot be gone under the foreign key, but treat a
		// missing endpoint as deleted so the delivery resolves to cancelled.
		deletedAt = sql.NullTime{Valid: true}
	} else if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read committed endpoint state: %w", err)
	}
	status := string(result.Status)
	nextAttempt := nullableTime(result.NextAttemptAt)
	deliveredAt := nullableTime(result.DeliveredAt)
	if deletedAt.Valid {
		status = string(outbox.StatusCancelled)
		nextAttempt = sql.NullTime{}
		deliveredAt = sql.NullTime{}
	}
	lastStatus := sql.NullInt64{}
	if result.LastHTTPStatus > 0 {
		lastStatus = sql.NullInt64{Int64: result.LastHTTPStatus, Valid: true}
	}
	updated, err := queries.CommitWebhookClaim(ctx, storedb.CommitWebhookClaimParams{
		ID:                 claim.DeliveryID,
		LeaseOwner:         sql.NullString{String: claim.Owner, Valid: true},
		ExpectedRowVersion: claim.Version,
		Status:             status,
		LastHttpStatus:     lastStatus,
		LastError:          nullableString(boundDeliveryError(result.LastError)),
		NextAttemptAt:      nextAttempt,
		DeliveredAt:        deliveredAt,
		Now:                now.UTC(),
	})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("commit webhook claim: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		return outbox.ErrClaimLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit webhook claim transaction: %w", err)
	}
	return nil
}

// NextWebhookDue returns the earliest time at which a webhook delivery can be
// claimed, considering leases and per-aggregate ordering and excluding
// disabled/deleted endpoints. Eligibility matches ClaimWebhookDue exactly, so
// a later row blocked by an earlier pending/delivering one can never produce
// a due time of its own and busy-loop the dispatcher.
func (s *Store) NextWebhookDue(ctx context.Context, now time.Time) (*time.Time, error) {
	if now.IsZero() {
		return nil, errors.New("due time is required")
	}
	row, err := s.queries.NextWebhookDue(ctx, sql.NullTime{Time: now.UTC(), Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get next webhook due: %w", err)
	}
	if !row.Valid {
		return nil, nil
	}
	if row.Time.IsZero() {
		return nil, errors.New("stored next webhook due is invalid")
	}
	due := row.Time.UTC()
	return &due, nil
}

// PruneWebhookDeliveries deletes succeeded/cancelled deliveries whose
// terminal updated_at is older than now - outbox.PruneRetention. Events,
// endpoint metadata, subscriptions, pending rows, and dead rows are kept.
func (s *Store) PruneWebhookDeliveries(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, errors.New("prune time is required")
	}
	cutoff := now.UTC().Add(-outbox.PruneRetention)
	rows, err := s.queries.PruneDeliveries(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune webhook deliveries: %w", err)
	}
	return rows, nil
}

// commitEventTx commits a transaction and, only when the transaction inserted
// at least one domain_events row, notifies the process event signal. It is
// the commit path for every event-emitting mutation: notification is strictly
// post-commit, so failed inserts, rollbacks, and commit failures never wake
// waiters, and no-op mutations (which never reach it with emitted true) do
// not either.
func (s *Store) commitEventTx(tx *sql.Tx, emitted bool) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	if emitted {
		s.eventSignal.Notify()
	}
	return nil
}

// emitDownloadEvent inserts one immutable download event and, for the
// subscribable completed/failed types, fans out one delivery per enabled
// non-deleted subscribed endpoint, all in the caller's transaction. It must be
// invoked at most once per mutation and only for real changes.
func emitDownloadEvent(ctx context.Context, queries *storedb.Queries, eventType string, previousState domain.State, after domain.Download) error {
	eventID := outbox.NewEventID()
	payload, err := outbox.BuildDownloadPayload(eventID, eventType, previousState, after)
	if err != nil {
		return fmt.Errorf("serialize %s event: %w", eventType, err)
	}
	occurredAt := after.UpdatedAt.UTC()
	if err := queries.InsertEvent(ctx, storedb.InsertEventParams{
		ID:               eventID,
		Type:             eventType,
		AggregateType:    outbox.AggregateDownload,
		AggregateID:      after.Hash,
		AggregateVersion: after.RowVersion,
		Payload:          payload,
		OccurredAt:       occurredAt,
	}); err != nil {
		return fmt.Errorf("insert %s event: %w", eventType, err)
	}
	if eventType != outbox.EventTypeCompleted && eventType != outbox.EventTypeFailed {
		return nil
	}
	endpoints, err := queries.ListSubscribedEndpoints(ctx, eventType)
	if err != nil {
		return fmt.Errorf("list %s subscribers: %w", eventType, err)
	}
	for _, endpoint := range endpoints {
		if _, err := queries.InsertDelivery(ctx, storedb.InsertDeliveryParams{
			EventID:       eventID,
			EndpointID:    endpoint.ID,
			EndpointName:  endpoint.Name,
			EventType:     eventType,
			AggregateType: outbox.AggregateDownload,
			AggregateID:   after.Hash,
			NextAttemptAt: sql.NullTime{Time: occurredAt, Valid: true},
			CreatedAt:     occurredAt,
			UpdatedAt:     occurredAt,
		}); err != nil {
			return fmt.Errorf("fan out %s delivery: %w", eventType, err)
		}
	}
	return nil
}

// reconcileSubscriptions updates subscription rows to match the requested set
// and returns the event types whose subscription was removed, so callers can
// cancel their pending/dead deliveries.
func reconcileSubscriptions(ctx context.Context, queries *storedb.Queries, endpointID int64, completed, failed bool) ([]string, error) {
	current, err := queries.GetEndpointSubscriptions(ctx, endpointID)
	if err != nil {
		return nil, fmt.Errorf("list endpoint subscriptions: %w", err)
	}
	have := make(map[string]bool)
	for _, eventType := range current {
		have[eventType] = true
	}
	removed := make([]string, 0, 2)
	for _, eventType := range endpointEventTypes {
		want := eventType == outbox.EventTypeCompleted && completed || eventType == outbox.EventTypeFailed && failed
		switch {
		case want && !have[eventType]:
			if err := queries.InsertSubscription(ctx, storedb.InsertSubscriptionParams{EndpointID: endpointID, EventType: eventType}); err != nil {
				return nil, fmt.Errorf("insert endpoint subscription: %w", err)
			}
		case !want && have[eventType]:
			if err := queries.DeleteSubscription(ctx, storedb.DeleteSubscriptionParams{EndpointID: endpointID, EventType: eventType}); err != nil {
				return nil, fmt.Errorf("delete endpoint subscription: %w", err)
			}
			removed = append(removed, eventType)
		}
	}
	return removed, nil
}

func subscriptionFlags(eventTypes []string) (completed, failed bool) {
	for _, eventType := range eventTypes {
		switch eventType {
		case outbox.EventTypeCompleted:
			completed = true
		case outbox.EventTypeFailed:
			failed = true
		}
	}
	return completed, failed
}

func endpointFromDB(row storedb.WebhookEndpoint, completed, failed, withSecret bool) outbox.Endpoint {
	endpoint := outbox.Endpoint{
		ID:                 row.ID,
		Name:               row.Name,
		DisplayURL:         outbox.DisplayURL(row.Url),
		Enabled:            row.Enabled == 1,
		SubscribeCompleted: completed,
		SubscribeFailed:    failed,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		DeletedAt:          nullTime(row.DeletedAt),
		RowVersion:         row.RowVersion,
	}
	if withSecret {
		endpoint.HMACSecret = row.HmacSecret
	}
	return endpoint
}

func deliveryFromDB(row storedb.WebhookDelivery) (outbox.Delivery, error) {
	status := outbox.DeliveryStatus(row.Status)
	if !status.Valid() || row.AttemptCount < 0 || row.RowVersion < 0 ||
		row.EventID == "" || row.EndpointID <= 0 || row.EventType == "" ||
		row.AggregateType == "" || row.AggregateID == "" ||
		row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) ||
		!validNullableTime(row.FirstAttemptAt) || !validNullableTime(row.NextAttemptAt) ||
		!validNullableTime(row.LeaseUntil) || !validNullableTime(row.DeliveredAt) ||
		(row.LastHttpStatus.Valid && row.LastHttpStatus.Int64 < 0) ||
		(nullString(row.LeaseOwner) == "") != !row.LeaseUntil.Valid {
		return outbox.Delivery{}, errors.New("stored webhook delivery is invalid")
	}
	lastStatus := int64(0)
	if row.LastHttpStatus.Valid {
		lastStatus = row.LastHttpStatus.Int64
	}
	return outbox.Delivery{
		ID:             row.ID,
		EventID:        row.EventID,
		EndpointID:     row.EndpointID,
		EndpointName:   row.EndpointName,
		EventType:      row.EventType,
		AggregateType:  row.AggregateType,
		AggregateID:    row.AggregateID,
		Status:         status,
		AttemptCount:   row.AttemptCount,
		FirstAttemptAt: nullTime(row.FirstAttemptAt),
		NextAttemptAt:  nullTime(row.NextAttemptAt),
		LeaseOwner:     nullString(row.LeaseOwner),
		LeaseUntil:     nullTime(row.LeaseUntil),
		LastHTTPStatus: lastStatus,
		LastError:      nullString(row.LastError),
		DeliveredAt:    nullTime(row.DeliveredAt),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		RowVersion:     row.RowVersion,
	}, nil
}

func eventFromDB(row storedb.DomainEvent) (outbox.Event, error) {
	if row.Sequence < 1 || row.ID == "" || row.Type == "" ||
		row.AggregateType == "" || row.AggregateID == "" ||
		row.AggregateVersion < 0 || row.OccurredAt.IsZero() || len(row.Payload) == 0 {
		return outbox.Event{}, errors.New("stored domain event is invalid")
	}
	return outbox.Event{
		Sequence:         row.Sequence,
		ID:               row.ID,
		Type:             row.Type,
		AggregateType:    row.AggregateType,
		AggregateID:      row.AggregateID,
		AggregateVersion: row.AggregateVersion,
		Payload:          row.Payload,
		OccurredAt:       row.OccurredAt,
	}, nil
}

// endpointEnabled resolves the persisted enabled flag for a create/update
// write: a non-nil input pointer sets the exact value; nil falls back to the
// given default (true on create, the stored value on update).
func endpointEnabled(input *bool, fallback bool) int64 {
	if input != nil {
		if *input {
			return 1
		}
		return 0
	}
	if fallback {
		return 1
	}
	return 0
}

func endpointNameConflict(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

func boundDeliveryError(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= maxDeliveryErrorBytes {
		return trimmed
	}
	cut := trimmed[:maxDeliveryErrorBytes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
