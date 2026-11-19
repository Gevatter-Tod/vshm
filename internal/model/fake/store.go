package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Store is a fake store for testing. It keeps the current incident state per
// check (upserted, like the real store) so state survives across service
// instances, records every mutation it was asked to apply, and captures all
// enqueued outbox events. All model.Store signatures are unchanged.
type Store struct {
	mu           sync.RWMutex
	observations []model.CheckResult
	incidents    map[string]model.IncidentSummary
	mutations    []model.IncidentMutation
	outbox       []model.OutboxEvent
	reportDates  map[string]string
}

// NewStore creates a new fake store.
func NewStore() *Store {
	return &Store{
		incidents:   make(map[string]model.IncidentSummary),
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

// RecordObservationAndIncident stores a result and applies the incident mutation.
func (s *Store) RecordObservationAndIncident(ctx context.Context, result model.CheckResult, mutation model.IncidentMutation, events []model.OutboxEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.observations = append(s.observations, result)
	s.mutations = append(s.mutations, mutation)
	checkID := mutation.CheckID
	if checkID == "" {
		checkID = result.CheckID
	}
	inc, ok := s.incidents[checkID]
	if !ok {
		inc = model.IncidentSummary{IncidentID: checkID, HostName: mutation.HostName, CheckID: checkID}
	}
	if mutation.Open {
		if inc.OpenedAt.IsZero() {
			inc.OpenedAt = mutation.LastFailure
		}
		inc.LastFailure = mutation.LastFailure
		inc.ResolvedAt = nil
	} else if mutation.ResolvedAt != nil {
		inc.ResolvedAt = mutation.ResolvedAt
		if inc.OpenedAt.IsZero() {
			inc.OpenedAt = *mutation.ResolvedAt
		}
	}
	inc.HostName = mutation.HostName
	inc.ReasonCode = mutation.ReasonCode
	inc.Reason = mutation.Reason
	s.incidents[checkID] = inc
	s.outbox = append(s.outbox, events...)
	return nil
}

// Snapshot returns current system state.
func (s *Store) Snapshot(ctx context.Context, now time.Time) (model.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	incidents := make([]model.IncidentSummary, 0, len(s.incidents))
	for _, inc := range s.incidents {
		incidents = append(incidents, inc)
	}
	sort.Slice(incidents, func(i, j int) bool { return incidents[i].CheckID < incidents[j].CheckID })
	return model.Snapshot{
		HostName:  "",
		Time:      now,
		Checks:    make([]model.CheckStatus, 0),
		Incidents: incidents,
	}, nil
}

// Observations returns all recorded observations in order.
func (s *Store) Observations() []model.CheckResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.CheckResult(nil), s.observations...)
}

// Mutations returns all incident mutations passed to RecordObservationAndIncident.
func (s *Store) Mutations() []model.IncidentMutation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.IncidentMutation(nil), s.mutations...)
}

// OutboxEvents returns all enqueued outbox events in order.
func (s *Store) OutboxEvents() []model.OutboxEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.OutboxEvent(nil), s.outbox...)
}

// Incident returns the current incident summary for checkID.
func (s *Store) Incident(checkID string) (model.IncidentSummary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inc, ok := s.incidents[checkID]
	return inc, ok
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
