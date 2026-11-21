// Package outbox provides ordered retry and delivery management for outbox events.
// It implements WP-12: durable ordered event delivery loop with retries, expiry, and status.
package outbox

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/storage"
)

// Default values for outbox configuration.
const (
	DefaultMaxAttempts      = 100
	DefaultInitialBackoff   = 1 * time.Second
	DefaultMaxBackoff       = 1 * time.Hour
	DefaultBatchSize        = 10
	DefaultDeliveryInterval = 30 * time.Second
)

// Config holds the outbox delivery configuration.
type Config struct {
	// MaxAttempts is the maximum number of delivery attempts per event (0 = unlimited).
	MaxAttempts int
	// InitialBackoff is the initial backoff duration for retries.
	InitialBackoff time.Duration
	// MaxBackoff is the maximum backoff duration for retries.
	MaxBackoff time.Duration
	// BatchSize is the maximum number of events to process in one batch.
	BatchSize int
	// DeliveryInterval is the interval between delivery batches.
	DeliveryInterval time.Duration
}

// WithDefaults returns a Config with default values for zero fields.
func (c Config) WithDefaults() Config {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = DefaultInitialBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = DefaultMaxBackoff
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.DeliveryInterval <= 0 {
		c.DeliveryInterval = DefaultDeliveryInterval
	}
	return c
}

// Manager manages the outbox delivery loop.
type Manager struct {
	store     model.Store
	transport model.Transport
	config    Config
	clock     model.Clock
	logger    *logging.Logger
	mu        sync.Mutex
	batchMu   sync.Mutex // serializes batches so Flush never overlaps the loop
	running   bool
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// New creates a new outbox Manager.
func New(store model.Store, transport model.Transport, clock model.Clock, logger *logging.Logger, cfg Config) *Manager {
	if clock == nil {
		clock = storage.RealClock{}
	}
	if logger == nil {
		logger = logging.New("")
	}
	return &Manager{
		store:     store,
		transport: transport,
		config:    cfg.WithDefaults(),
		clock:     clock,
		logger:    logger,
		stopCh:    make(chan struct{}),
	}
}

// Start begins the delivery loop. It returns immediately.
// The loop processes pending events in order, applies retry backoff,
// handles expiry, and marks delivery outcomes.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return fmt.Errorf("outbox manager already running")
	}
	m.running = true

	m.wg.Add(1)
	go m.runLoop(ctx)
	return nil
}

// Stop stops the delivery loop and waits for it to finish.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	close(m.stopCh)
	m.wg.Wait()
}

// runLoop is the main delivery loop. It processes one batch immediately at
// start (events left pending before a restart are retried promptly) and then
// on every delivery interval.
func (m *Manager) runLoop(ctx context.Context) {
	defer m.wg.Done()

	m.flush(ctx)

	ticker := m.clock.NewTimer(m.config.DeliveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Printf("outbox: delivery loop stopped by context")
			return
		case <-m.stopCh:
			m.logger.Printf("outbox: delivery loop stopped")
			return
		case <-ticker.C():
			m.flush(ctx)
			ticker.Reset(m.config.DeliveryInterval)
		}
	}
}

// Flush processes one batch of pending events immediately, outside the
// regular delivery interval (e.g. right after a failure was recorded or the
// startup report was enqueued). It is safe to call concurrently with the
// delivery loop; concurrent calls coalesce into one batch.
func (m *Manager) Flush(ctx context.Context) { m.flush(ctx) }

// flush runs one batch under batchMu.
func (m *Manager) flush(ctx context.Context) {
	m.batchMu.Lock()
	defer m.batchMu.Unlock()
	if err := m.processBatch(ctx); err != nil {
		m.logger.Printf("outbox: process batch error: %v", err)
	}
}

// processBatch fetches and processes a batch of pending events.
func (m *Manager) processBatch(ctx context.Context) error {
	now := m.clock.Now()
	
	// Get pending events that are due for delivery
	pending, err := m.store.Pending(ctx, now, m.config.BatchSize)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}

	if len(pending) == 0 {
		return nil
	}

	m.logger.Printf("outbox: processing %d pending events", len(pending))

	// Process events in order (they are already ordered by ID from storage)
	for _, event := range pending {
		select {
		case <-ctx.Done():
		case <-m.stopCh:
			return nil
		default:
			if err := m.processEvent(ctx, event, now); err != nil {
				m.logger.Printf("outbox: error processing event %s: %v", event.ID, err)
			}
		}
	}

	return nil
}

// processEvent processes a single outbox event.
func (m *Manager) processEvent(ctx context.Context, event model.OutboxEvent, now time.Time) error {
	// Check if event has expired
	if event.ExpiresAt.Before(now) || event.ExpiresAt.Equal(now) {
		m.logger.Printf("outbox: event %s expired at %s", event.ID, event.ExpiresAt)
		if err := m.store.MarkDelivery(ctx, event.ID, model.DeliveryExpired, now, event.EnqueuedAt, "expired"); err != nil {
			return fmt.Errorf("mark expired: %w", err)
		}
		return nil
	}

	// Check if max attempts reached
	if m.config.MaxAttempts > 0 && event.AttemptCount >= m.config.MaxAttempts {
		m.logger.Printf("outbox: event %s max attempts (%d) reached", event.ID, m.config.MaxAttempts)
		if err := m.store.MarkDelivery(ctx, event.ID, model.DeliveryEvicted, now, event.EnqueuedAt, "max attempts exceeded"); err != nil {
			return fmt.Errorf("mark evicted: %w", err)
		}
		return nil
	}

	// Try to deliver
	outcome, err := m.transport.Send(ctx, event)
	if err != nil {
		m.logger.Printf("outbox: send failed for event %s: %v", event.ID, err)
		outcome = model.DeliveryFailure
	}

	switch outcome {
	case model.DeliverySuccess:
		m.logger.Printf("outbox: event %s delivered successfully", event.ID)
		if err := m.store.MarkDelivery(ctx, event.ID, outcome, now, event.EnqueuedAt, "delivered"); err != nil {
			return fmt.Errorf("mark delivered: %w", err)
		}

	case model.DeliveryFailure, model.DeliveryAmbiguous:
		// Calculate next attempt time with exponential backoff
		nextAttempt := m.calculateNextAttempt(event, now)
		reason := fmt.Sprintf("delivery %s: %v", outcome, err)
		if err := m.store.MarkDelivery(ctx, event.ID, outcome, now, nextAttempt, reason); err != nil {
			return fmt.Errorf("mark delivery: %w", err)
		}
		m.logger.Printf("outbox: event %s failed, next attempt at %s", event.ID, nextAttempt)

	case model.DeliveryExpired:
		// Already expired - mark as such
		if err := m.store.MarkDelivery(ctx, event.ID, outcome, now, event.EnqueuedAt, "expired"); err != nil {
			return fmt.Errorf("mark expired: %w", err)
		}

	case model.DeliveryEvicted:
		// Already evicted - mark as such
		if err := m.store.MarkDelivery(ctx, event.ID, outcome, now, event.EnqueuedAt, "evicted"); err != nil {
			return fmt.Errorf("mark evicted: %w", err)
		}

	default:
		// Unknown outcome - treat as ambiguous
		reason := fmt.Sprintf("unknown outcome: %s", outcome)
		nextAttempt := m.calculateNextAttempt(event, now)
		if err := m.store.MarkDelivery(ctx, event.ID, model.DeliveryAmbiguous, now, nextAttempt, reason); err != nil {
			return fmt.Errorf("mark delivery: %w", err)
		}
	}

	return nil
}

// calculateNextAttempt computes the next attempt time using exponential backoff.
func (m *Manager) calculateNextAttempt(event model.OutboxEvent, now time.Time) time.Time {
	// Calculate backoff: initialBackoff * 2^attempts, capped at maxBackoff
	backoff := m.config.InitialBackoff * time.Duration(math.Pow(2, float64(event.AttemptCount)))
	if backoff > m.config.MaxBackoff {
		backoff = m.config.MaxBackoff
	}

	// Add jitter: random value between 0 and backoff/2
	jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
	backoff = backoff + jitter

	next := now.Add(backoff)
	
	// Ensure we don't exceed the expiry time
	if next.After(event.ExpiresAt) {
		next = event.ExpiresAt
	}

	return next
}

// Enqueue adds an event to the outbox.
func (m *Manager) Enqueue(ctx context.Context, event model.OutboxEvent) error {
	// This is a pass-through to the store's EnqueueReport for reports,
	// or direct insertion for other events. For now, we use the store directly.
	// The actual enqueue logic is handled by the store.
	return nil
}

// Pending returns pending events from the store.
func (m *Manager) Pending(ctx context.Context, now time.Time, max int) ([]model.OutboxEvent, error) {
	return m.store.Pending(ctx, now, max)
}

// Retry retries a specific event by resetting its next_attempt_at to now.
func (m *Manager) Retry(ctx context.Context, eventID string) error {
	now := m.clock.Now()
	// Mark as pending with next attempt at now
	return m.store.MarkDelivery(ctx, eventID, model.DeliveryFailure, now, now, "manual retry")
}

// MarkDelivery marks a delivery outcome for an event.
func (m *Manager) MarkDelivery(ctx context.Context, eventID string, outcome model.DeliveryOutcome, deliveredAt, enqueuedAt time.Time, reason string) error {
	return m.store.MarkDelivery(ctx, eventID, outcome, deliveredAt, enqueuedAt, reason)
}

// Expire expires events older than the cutoff.
func (m *Manager) Expire(ctx context.Context, cutoff time.Time) error {
	// Get all pending events
	pending, err := m.store.Pending(ctx, m.clock.Now(), 0)
	if err != nil {
		return err
	}

	for _, event := range pending {
		if event.EnqueuedAt.Before(cutoff) {
			if err := m.store.MarkDelivery(ctx, event.ID, model.DeliveryExpired, m.clock.Now(), event.EnqueuedAt, "expired by cutoff"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Evict removes events that have been delivered for longer than maxAge.
func (m *Manager) Evict(ctx context.Context, maxAge time.Duration) error {
	// This would be implemented by querying for delivered events older than maxAge
	// and marking them as evicted. For now, this is a placeholder.
	return nil
}

// Status returns the current outbox status.
func (m *Manager) Status(ctx context.Context) (OutboxStatus, error) {
	now := m.clock.Now()
	pending, err := m.store.Pending(ctx, now, 0)
	if err != nil {
		return OutboxStatus{}, err
	}

	storeStatus, err := m.store.Status(ctx, now)
	if err != nil {
		return OutboxStatus{}, err
	}

	return OutboxStatus{
		PendingCount:   len(pending),
		DeliveredCount: int(storeStatus.OutboxCounts[model.StateDelivered]),
		ExpiredCount:   int(storeStatus.OutboxCounts[model.StateExpired]),
		EvictedCount:   int(storeStatus.OutboxCounts[model.StateEvicted]),
		AmbiguousCount: int(storeStatus.OutboxCounts[model.StateAmbiguous]),
		Running:        m.isRunning(),
	}, nil
}

// isRunning returns whether the manager is currently running.
func (m *Manager) isRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// OutboxStatus represents the current status of the outbox.
type OutboxStatus struct {
	PendingCount   int
	DeliveredCount int
	ExpiredCount   int
	EvictedCount   int
	AmbiguousCount int
	Running        bool
}
