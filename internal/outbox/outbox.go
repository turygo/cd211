// Package outbox defines the durable event, webhook endpoint, and delivery
// contract shared by the store, the webhook dispatcher, the Web UI, and the
// runtime. It is dependency-light: it imports only the standard library and
// internal/domain.
package outbox

import (
	"context"
	"time"
)

// Event types emitted by the store. Only completed and failed are
// subscribable/deliverable in this release; the others are durable history.
const (
	EventTypeCreated         = "download.created"
	EventTypeStateChanged    = "download.state_changed"
	EventTypeCategoryChanged = "download.category_changed"
	EventTypeCompleted       = "download.completed"
	EventTypeFailed          = "download.failed"
	EventTypeTest            = "webhook.test"
)

// EventSchemaVersion is stamped into every serialized event payload.
const EventSchemaVersion = 1

// Aggregate types used by domain_events.
const (
	AggregateDownload        = "download"
	AggregateWebhookEndpoint = "webhook_endpoint"
)

// DeliveryStatus is the durable lifecycle status of one delivery row.
// Pending means the row is awaiting its next scheduled attempt; Delivering
// marks an active lease/HTTP attempt only and never persists between attempts.
type DeliveryStatus string

const (
	StatusPending    DeliveryStatus = "pending"
	StatusDelivering DeliveryStatus = "delivering"
	StatusSucceeded  DeliveryStatus = "succeeded"
	StatusDead       DeliveryStatus = "dead"
	StatusCancelled  DeliveryStatus = "cancelled"
)

// Valid reports whether s is a persisted delivery status.
func (s DeliveryStatus) Valid() bool {
	switch s {
	case StatusPending, StatusDelivering, StatusSucceeded, StatusDead, StatusCancelled:
		return true
	default:
		return false
	}
}

// Delivery retry and lease protocol constants.
const (
	// LeaseDuration is how long a claimed delivery stays leased; it must
	// exceed the HTTP timeout with a commit margin.
	LeaseDuration = 30 * time.Second
	// HTTPTimeout bounds one outbound webhook request.
	HTTPTimeout = 10 * time.Second
	// RetryDeadline is the at-least-once window: once the next retry would
	// be at or after first_attempt_at + RetryDeadline, the delivery dies.
	RetryDeadline = 24 * time.Hour
	// MaxResponseRead bounds the response body read/discarded per attempt.
	MaxResponseRead = 4 << 10
	// DefaultWorkers is the dispatcher worker count.
	DefaultWorkers = 4
	// MaxIdlePoll bounds the durable next-due idle poll.
	MaxIdlePoll = time.Second
	// PruneRetention is how long succeeded/cancelled deliveries are kept.
	PruneRetention = 90 * 24 * time.Hour
)

// Fixed, bounded delivery error categories persisted instead of raw errors.
// The dispatcher formats "HTTP <status>" itself for non-2xx responses.
const (
	ErrorCategoryTimeout    = "request timeout"
	ErrorCategoryConnection = "connection failed"
	ErrorCategoryTLS        = "TLS failure"
	ErrorCategoryRequest    = "request failed"
)

// TestMessage is the fixed message of webhook.test event payloads.
const TestMessage = "CD211 webhook test"

// Sentinel errors owned by the outbox contract so consumers never need to
// import the store package.
var (
	// ErrNotFound is returned when an endpoint or delivery does not exist,
	// is soft-deleted, or cannot be replayed in its current state.
	ErrNotFound = errOutbox("webhook record not found")
	// ErrClaimLost is returned when a delivery commit fails its CAS because
	// the lease is stale or the row was superseded.
	ErrClaimLost = errOutbox("webhook claim lost")
	// ErrNameConflict is returned when an endpoint name is already taken.
	ErrNameConflict = errOutbox("webhook endpoint name already exists")
)

type outboxError string

func (e outboxError) Error() string { return string(e) }

func errOutbox(message string) error { return outboxError(message) }

// Event is an immutable domain or test event.
type Event struct {
	ID               string
	Type             string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Payload          []byte
	OccurredAt       time.Time
}

// Endpoint is a webhook endpoint. Secret fields (HMACSecret, BearerToken)
// are populated only by CreateWebhookEndpoint and RotateWebhookEndpointSecret
// one-time responses; every ordinary read returns them empty. URL is likewise
// never returned to ordinary callers; DisplayURL carries the redacted form.
type Endpoint struct {
	ID                 int64
	Name               string
	URL                string
	DisplayURL         string
	HMACSecret         string
	BearerToken        string
	Enabled            bool
	SubscribeCompleted bool
	SubscribeFailed    bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
	RowVersion         int64
}

// EndpointInput is the mutable endpoint identity. On Update, an empty URL or
// BearerToken preserves the stored value, ClearBearerToken removes the stored
// token, and any non-empty values replace the stored ones. On Create,
// ClearBearerToken is ignored and an empty BearerToken stores no token.
// Enabled is a pointer so existing nil callers keep the backward-compatible
// behavior: nil means the default true on Create and preserve on Update; a
// non-nil pointer sets the exact value atomically with the rest of the write.
type EndpointInput struct {
	Name               string
	URL                string
	SubscribeCompleted bool
	SubscribeFailed    bool
	BearerToken        string
	ClearBearerToken   bool
	Enabled            *bool
}

// Delivery is one event targeted at one endpoint.
type Delivery struct {
	ID             int64
	EventID        string
	EndpointID     int64
	EndpointName   string
	EventType      string
	AggregateType  string
	AggregateID    string
	Status         DeliveryStatus
	AttemptCount   int64
	FirstAttemptAt *time.Time
	NextAttemptAt  *time.Time
	LeaseOwner     string
	LeaseUntil     *time.Time
	LastHTTPStatus int64
	LastError      string
	DeliveredAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RowVersion     int64
}

// Claim is a leased webhook delivery carrying the immutable event payload and
// the endpoint credentials resolved at claim time, plus the lease identity
// required for the commit CAS. FirstAttemptAt is always non-nil after a claim.
type Claim struct {
	DeliveryID     int64
	Owner          string
	Version        int64
	EndpointID     int64
	EventID        string
	EventType      string
	Payload        []byte
	URL            string
	HMACSecret     string
	BearerToken    string
	AttemptCount   int64
	FirstAttemptAt *time.Time
}

// Result is the outcome of one HTTP attempt reported by the dispatcher. The
// dispatcher computes the retry schedule: Status is StatusSucceeded for 2xx,
// StatusPending with NextAttemptAt set for a scheduled retry, or StatusDead
// when the next retry would exceed the 24-hour deadline. StatusDelivering is
// never a committed outcome; it only marks an active lease while an HTTP
// attempt is in flight. Commit validation requires succeeded results to carry
// a non-nil DeliveredAt, pending results a non-nil NextAttemptAt, and dead
// results no NextAttemptAt.
type Result struct {
	Status         DeliveryStatus
	NextAttemptAt  *time.Time
	LastHTTPStatus int64
	LastError      string
	DeliveredAt    *time.Time
}

// DeliveryFilter narrows delivery history listing. An empty EventType or
// Status means "all". Cursor is the opaque value from a previous Page.
type DeliveryFilter struct {
	EndpointID *int64
	EventType  string
	Status     DeliveryStatus
	Cursor     string
	Limit      int
}

// Page reports delivery listing pagination state.
type Page struct {
	NextCursor string
	HasMore    bool
}

// Repository is the narrow durable outbox boundary consumed by the webhook
// dispatcher. *store.Store implements it.
type Repository interface {
	ClaimWebhookDue(context.Context, string, time.Time, time.Duration) (*Claim, error)
	CommitWebhookClaim(context.Context, Claim, Result, time.Time) error
	NextWebhookDue(context.Context, time.Time) (*time.Time, error)
}

// EndpointRepository is the narrow durable boundary consumed by the Web UI.
// *store.Store implements it.
type EndpointRepository interface {
	ListWebhookEndpoints(context.Context) ([]Endpoint, error)
	GetWebhookEndpoint(context.Context, int64) (Endpoint, error)
	CreateWebhookEndpoint(context.Context, EndpointInput, time.Time) (Endpoint, error)
	UpdateWebhookEndpoint(context.Context, int64, EndpointInput, time.Time) (Endpoint, error)
	SetWebhookEndpointEnabled(context.Context, int64, bool, time.Time) error
	RotateWebhookEndpointSecret(context.Context, int64, time.Time) (Endpoint, error)
	DeleteWebhookEndpoint(context.Context, int64, time.Time) error
	ListWebhookDeliveries(context.Context, DeliveryFilter) ([]Delivery, Page, error)
	GetWebhookDelivery(context.Context, int64) (Delivery, error)
	ReplayWebhookDelivery(context.Context, int64, time.Time) (Delivery, error)
	EnqueueTestDelivery(context.Context, int64, time.Time) (Delivery, error)
}
