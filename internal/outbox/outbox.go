// Package outbox provides ordered retry and delivery management.
package outbox

import (
	"context"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Manager manages the outbox.
type Manager struct {
	store model.Store
}

// New creates a new outbox manager.
func New(store model.Store) *Manager {
	return &Manager{store: store}
}

// Enqueue adds an event to the outbox.
func (m *Manager) Enqueue(ctx context.Context, event model.OutboxEvent) error {
	// Placeholder - implement outbox enqueue
	return nil
}

// Pending returns pending events.
func (m *Manager) Pending(ctx context.Context, now time.Time, max int) ([]model.OutboxEvent, error) {
	// Placeholder - implement pending retrieval
	return nil, nil
}

// Retry retries a failed event.
func (m *Manager) Retry(ctx context.Context, eventID string) error {
	// Placeholder - implement retry
	return nil
}

// MarkDelivery marks a delivery outcome.
func (m *Manager) MarkDelivery(ctx context.Context, eventID string, outcome model.DeliveryOutcome, deliveredAt, enqueuedAt time.Time, reason string) error {
	// Placeholder - implement delivery marking
	return nil
}

// Expire expires old events.
func (m *Manager) Expire(ctx context.Context, cutoff time.Time) error {
	// Placeholder - implement expiration
	return nil
}

// Evict evicts events.
func (m *Manager) Evict(ctx context.Context, maxAge int64) error {
	// Placeholder - implement eviction
	return nil
}
