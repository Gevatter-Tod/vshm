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
}

// NewStore creates a new fake store.
func NewStore() *Store {
	return &Store{
		reportDates: make(map[string]string),
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
		IncidentID:  "test-incident",
		HostName:    mutation.HostName,
		CheckID:     mutation.CheckID,
		OpenedAt:    mutation.LastFailure,
		LastFailure: mutation.LastFailure,
		ReasonCode:  mutation.ReasonCode,
		Reason:      mutation.Reason,
		ResolvedAt:  mutation.ResolvedAt,
	})
	s.outbox = append(s.outbox, events...)
	return nil
}

// Snapshot returns current system state.
func (s *Store) Snapshot(ctx context.Context, now time.Time) (model.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return model.Snapshot{
		Checks:    make([]model.CheckStatus, 0),
		Incidents: s.incidents,
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
func (s *Store) Prune(ctx context.Context, now time.Time) (model.PruneResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	observationCutoff := now.Add(-90 * 24 * time.Hour)
	observations := s.observations[:0]
	for _, observation := range s.observations {
		if observation.CompletedAt.Before(observationCutoff) {
			continue
		}
		observations = append(observations, observation)
	}
	removedObservations := int64(len(s.observations) - len(observations))
	s.observations = observations

	outboxCutoff := now.Add(-30 * 24 * time.Hour)
	outbox := s.outbox[:0]
	for _, event := range s.outbox {
		if event.Kind != "report" && event.DeliveryState != model.StatePending && event.DeliveredAt.Before(outboxCutoff) {
			continue
		}
		outbox = append(outbox, event)
	}
	removedOutboxRows := int64(len(s.outbox) - len(outbox))
	s.outbox = outbox
	return model.PruneResult{Observations: removedObservations, OutboxRows: removedOutboxRows}, nil
}

// Status returns storage health.
func (s *Store) Status(ctx context.Context, now time.Time) (model.StoreStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return model.StoreStatus{
		Healthy:      true,
		RecordsCount: int64(len(s.observations)),
		OutboxCounts: countOutboxStates(s.outbox),
	}, nil
}

func countOutboxStates(events []model.OutboxEvent) map[model.DeliveryState]int64 {
	counts := map[model.DeliveryState]int64{
		model.StatePending:   0,
		model.StateDelivered: 0,
		model.StateExpired:   0,
		model.StateEvicted:   0,
		model.StateAmbiguous: 0,
	}
	for _, event := range events {
		counts[event.DeliveryState]++
	}
	return counts
}

// Close releases resources.
func (s *Store) Close() error {
	return nil
}
