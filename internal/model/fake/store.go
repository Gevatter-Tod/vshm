package fake

import (
	"context"
	"sync"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Store is a fake store for testing.
type Store struct {
	mu           sync.RWMutex
	observations []model.CheckResult
	incidents    []model.IncidentSummary
	outbox       []model.OutboxEvent
	reportDates  map[string]string
	discardCount map[string]int64
}

// NewStore creates a new fake store.
func NewStore() *Store {
	return &Store{
		reportDates: make(map[string]string),
		discardCount: make(map[string]int64),
	}
}

// RecordObservation stores a check result.
func (s *Store) RecordObservation(ctx context.Context, result model.CheckResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.observations = append(s.observations, result)
	return nil
}

// RecordObservationAndIncident stores a result and creates an incident if needed.
func (s *Store) RecordObservationAndIncident(ctx context.Context, result model.CheckResult, mutation model.IncidentMutation, events []model.OutboxEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.observations = append(s.observations, result)
	s.incidents = append(s.incidents, model.IncidentSummary{
		IncidentID:   "test-incident",
		HostName:     mutation.HostName,
		CheckID:      mutation.CheckID,
		OpenedAt:     mutation.LastFailure,
		LastFailure:  mutation.LastFailure,
		ReasonCode:   mutation.ReasonCode,
		Reason:       mutation.Reason,
		ResolvedAt:   mutation.ResolvedAt,
	})
	s.outbox = append(s.outbox, events...)
	return nil
}

// Snapshot returns current system state.
func (s *Store) Snapshot(ctx context.Context, now time.Time) (model.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return model.Snapshot{
		Checks:     make([]model.CheckStatus, 0),
		Incidents:  s.incidents,
		DiscardCounts: s.discardCount,
	}, nil
}

// EnqueueReport adds a report to the outbox.
func (s *Store) EnqueueReport(ctx context.Context, dateKey string, event model.OutboxEvent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reportDates[dateKey] = event.ID
	return true, nil
}

// Pending returns events ready for delivery.
func (s *Store) Pending(ctx context.Context, now time.Time, max int) ([]model.OutboxEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.outbox, nil
}

// MarkDelivery updates delivery status.
func (s *Store) MarkDelivery(ctx context.Context, eventID string, outcome model.DeliveryOutcome, deliveredAt, enqueuedAt time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, event := range s.outbox {
		if event.ID == eventID {
			s.outbox[i].DeliveryState = model.DeliveryState(outcome)
			s.outbox[i].LastOutcome = reason
			s.outbox[i].DeliveredAt = deliveredAt
			return nil
		}
	}
	return nil
}

// Prune removes old records.
func (s *Store) Prune(ctx context.Context, cutoff time.Time, maxAge int64) (model.PruneResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := int64(0)
	s.observations = s.observations[:0]
	s.incidents = s.incidents[:0]
	s.outbox = s.outbox[:0]
	return model.PruneResult{RecordsRemoved: removed}, nil
}

// Status returns storage health.
func (s *Store) Status(ctx context.Context, now time.Time) (model.StoreStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return model.StoreStatus{
		Healthy:     true,
		RecordsCount: int64(len(s.observations)),
	}, nil
}

// Close releases resources.
func (s *Store) Close() error {
	return nil
}
