// Package webhook delivers outbox events to configured HTTP endpoints with
// HMAC-SHA256 signatures, bounded retries, and at-least-once semantics.
//
// The dispatcher is process-lifecycle-agnostic: it owns no downloads or
// runtime generations, only the claim/deliver/commit loop against the durable
// outbox repository. Retry scheduling, dead-letter decisions, ordering, and
// lease recovery are owned by the repository; this package only classifies
// each HTTP attempt into a fixed, bounded error category and commits it.
package webhook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/turygo/cd211/internal/outbox"
)

// leaseCommitMargin is the minimum headroom required between the claim lease
// and the per-attempt HTTP timeout so a commit fits inside the lease.
const leaseCommitMargin = 10 * time.Second

// Config controls the dispatcher loop. The plan's fixed values live in outbox
// (outbox.LeaseDuration, outbox.HTTPTimeout, outbox.DefaultWorkers,
// outbox.MaxIdlePoll) and callers supply them explicitly; nothing is
// defaulted here.
type Config struct {
	// Owner uniquely identifies this dispatcher instance as the lease holder.
	Owner string
	// LeaseDuration is the claim lease; must exceed RequestTimeout plus a
	// commit margin.
	LeaseDuration time.Duration
	// RequestTimeout bounds one HTTP attempt.
	RequestTimeout time.Duration
	// WorkerCount is the number of concurrent claim/deliver workers.
	WorkerCount int
	// MaxIdleWait caps how long an idle worker sleeps before re-polling the
	// repository; correctness never depends on in-memory wake signals.
	MaxIdleWait time.Duration
	// PruneInterval is how often idle workers trigger retention pruning.
	// The 90-day retention itself is hardcoded in the store.
	PruneInterval time.Duration
	// Version is the User-Agent suffix; "unknown" when empty.
	Version string
}

// Repository is the narrow durable boundary the dispatcher consumes: the
// shared outbox.Repository contract plus the retention pruning trigger owned
// by the store.
type Repository interface {
	outbox.Repository
	PruneWebhookDeliveries(ctx context.Context, now time.Time) (int64, error)
}

// Dispatcher claims due deliveries, performs signed HTTP POSTs, and commits
// bounded results. Run and Step are safe for process-owned runtime wiring.
type Dispatcher struct {
	config Config
	repo   Repository
	client HTTPClient
	clock  Clock
	logger *slog.Logger
	wake   chan struct{}

	pruneMu   sync.Mutex
	lastPrune time.Time
}

// New validates the configuration and constructs a Dispatcher. All
// dependencies must be non-nil. Config values are not defaulted; the caller
// supplies the plan's fixed values (outbox.LeaseDuration, outbox.HTTPTimeout,
// outbox.DefaultWorkers, outbox.MaxIdlePoll) explicitly.
func New(config Config, repo Repository, client HTTPClient, clock Clock, logger *slog.Logger) (*Dispatcher, error) {
	if strings.TrimSpace(config.Owner) == "" {
		return nil, errors.New("invalid dispatcher config: owner is required")
	}
	if config.RequestTimeout <= 0 || config.LeaseDuration <= config.RequestTimeout+leaseCommitMargin {
		return nil, fmt.Errorf("invalid dispatcher config: lease %s must exceed request timeout %s plus commit margin", config.LeaseDuration, config.RequestTimeout)
	}
	if config.WorkerCount <= 0 || config.MaxIdleWait <= 0 || config.PruneInterval <= 0 {
		return nil, errors.New("invalid dispatcher config: worker count, idle wait, and prune interval must be positive")
	}
	if config.Version == "" {
		config.Version = "unknown"
	}
	if nilDependency(repo) || nilDependency(client) || nilDependency(clock) || logger == nil {
		return nil, errors.New("nil webhook dispatcher dependency")
	}
	return &Dispatcher{
		config: config,
		repo:   repo,
		client: client,
		clock:  clock,
		logger: logger,
		wake:   make(chan struct{}, 1),
	}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Wake asks a sleeping worker to re-poll the repository early. It is an
// optimization only; idle workers poll within MaxIdleWait regardless.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run starts WorkerCount workers and returns after the context is cancelled
// and every worker has exited. An interrupted claim is left to expire and is
// recovered by the repository lease protocol.
func (d *Dispatcher) Run(ctx context.Context) error {
	var workers sync.WaitGroup
	workers.Add(d.config.WorkerCount)
	for range d.config.WorkerCount {
		go func() {
			defer workers.Done()
			d.worker(ctx)
		}()
	}
	workers.Wait()
	return nil
}

// Step claims at most one due delivery, performs the HTTP attempt, and commits
// the bounded result. It returns false when nothing was due. Commit failures
// that are not claim-lost are returned to the caller; the worker loop backs
// off and retries them.
func (d *Dispatcher) Step(ctx context.Context) (claimed bool, err error) {
	now := d.clock.Now()
	claim, err := d.repo.ClaimWebhookDue(ctx, d.config.Owner, now, d.config.LeaseDuration)
	if err != nil || claim == nil {
		return false, err
	}
	started := d.clock.Now()
	result, err := d.deliver(ctx, claim)
	if err != nil {
		// Parent cancellation: leave the lease in place; it expires and the
		// row becomes claimable again.
		return true, err
	}
	if err := d.repo.CommitWebhookClaim(ctx, *claim, result, d.clock.Now()); err != nil {
		if errors.Is(err, outbox.ErrClaimLost) {
			d.log(claim, started, "claim_lost", result)
			return true, nil
		}
		return true, err
	}
	d.log(claim, started, "committed", result)
	return true, nil
}

func (d *Dispatcher) worker(ctx context.Context) {
	for {
		claimed, err := d.Step(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			d.logger.Warn("webhook repository error", "operation", "step", "error", err)
			d.wait(ctx, d.config.MaxIdleWait)
			continue
		}
		if claimed {
			continue
		}
		now := d.clock.Now()
		d.maybePrune(ctx, now)
		due, dueErr := d.repo.NextWebhookDue(ctx, now)
		if ctx.Err() != nil {
			return
		}
		if dueErr != nil {
			d.logger.Warn("webhook repository error", "operation", "next_due", "error", dueErr)
			d.wait(ctx, d.config.MaxIdleWait)
			continue
		}
		d.wait(ctx, idleWait(now, due, d.config.MaxIdleWait))
	}
}

func (d *Dispatcher) wait(ctx context.Context, delay time.Duration) {
	timer := d.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-d.wake:
	case <-timer.C():
	}
}

// idleWait computes how long an idle worker sleeps: never more than max, never
// past the earliest due time.
func idleWait(now time.Time, due *time.Time, max time.Duration) time.Duration {
	if due == nil {
		return max
	}
	wait := due.Sub(now)
	if wait < 0 {
		return 0
	}
	if wait > max {
		return max
	}
	return wait
}

// maybePrune triggers retention pruning at most once per PruneInterval,
// shared across all workers.
func (d *Dispatcher) maybePrune(ctx context.Context, now time.Time) {
	d.pruneMu.Lock()
	if !d.lastPrune.IsZero() && now.Sub(d.lastPrune) < d.config.PruneInterval {
		d.pruneMu.Unlock()
		return
	}
	d.lastPrune = now
	d.pruneMu.Unlock()
	count, err := d.repo.PruneWebhookDeliveries(ctx, now)
	if err != nil {
		d.logger.Warn("webhook prune failed", "result", "error")
		return
	}
	if count > 0 {
		d.logger.Info("webhook deliveries pruned", "count", count)
	}
}

// log emits a secret-safe delivery record: never the endpoint URL, query
// string, bearer token, HMAC secret, or payload.
func (d *Dispatcher) log(claim *outbox.Claim, started time.Time, result string, delivery outbox.Result) {
	attributes := []any{
		"delivery_id", claim.DeliveryID,
		"endpoint_id", claim.EndpointID,
		"event", claim.EventType,
		"attempt", claim.AttemptCount,
		"latency", d.clock.Now().Sub(started),
		"result", result,
	}
	if delivery.LastError != "" {
		attributes = append(attributes, "error", delivery.LastError)
	}
	d.logger.Info("webhook delivery", attributes...)
}
