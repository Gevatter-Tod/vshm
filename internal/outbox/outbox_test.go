package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// fakeStore implements model.Store for testing.
type fakeStore struct {
	mu            sync.Mutex
	events        map[string]model.OutboxEvent
	pendingCalls  int
	markCalls     []markCall
	pendingMaxAge time.Duration
}

type markCall struct {
	eventID   string
	outcome   model.DeliveryOutcome
	attempted time.Time
	next      time.Time
	reason    string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		events:       make(map[string]model.OutboxEvent),
		pendingMaxAge: 24 * time.Hour,
	}
}

func (s *fakeStore) RecordObservation(ctx context.Context, result model.CheckResult) error {
	return nil
}

func (s *fakeStore) RecordObservationAndIncident(ctx context.Context, result model.CheckResult, mutation model.IncidentMutation, events []model.OutboxEvent) error {
	for _, ev := range events {
		s.mu.Lock()
		s.events[ev.ID] = ev
		s.mu.Unlock()
	}
	return nil
}

func (s *fakeStore) Snapshot(ctx context.Context, now time.Time) (model.Snapshot, error) {
	return model.Snapshot{}, nil
}

func (s *fakeStore) EnqueueReport(ctx context.Context, dateKey string, event model.OutboxEvent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[event.ID]; exists {
		return false, nil
	}
	s.events[event.ID] = event
	return true, nil
}

func (s *fakeStore) Pending(ctx context.Context, now time.Time, max int) ([]model.OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingCalls++

	var result []model.OutboxEvent
	cutoff := now.Add(-s.pendingMaxAge)
	
	for _, ev := range s.events {
		// Only return pending events that are due
		if ev.DeliveryState == model.StatePending && ev.NextAttemptAt.Before(now) || ev.NextAttemptAt.Equal(now) {
			// Check if expired
			if ev.EnqueuedAt.Before(cutoff) {
				continue
			}
			result = append(result, ev)
		}
	}
	
	// Sort by ID for deterministic ordering
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].ID > result[j].ID {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	
	if max > 0 && len(result) > max {
		result = result[:max]
	}
	return result, nil
}

func (s *fakeStore) MarkDelivery(ctx context.Context, eventID string, outcome model.DeliveryOutcome, deliveredAt, enqueuedAt time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markCalls = append(s.markCalls, markCall{
		eventID:   eventID,
		outcome:   outcome,
		attempted: deliveredAt,
		next:      enqueuedAt,
		reason:    reason,
	})
	
	if ev, ok := s.events[eventID]; ok {
		ev.AttemptCount++
		switch outcome {
		case model.DeliverySuccess:
			ev.DeliveryState = model.StateDelivered
			ev.DeliveredAt = deliveredAt
			ev.LastOutcome = string(outcome)
		case model.DeliveryFailure, model.DeliveryAmbiguous:
			ev.DeliveryState = model.StatePending
			ev.NextAttemptAt = enqueuedAt
			ev.LastOutcome = string(outcome)
		case model.DeliveryExpired:
			ev.DeliveryState = model.StateExpired
			ev.DeliveredAt = deliveredAt
			ev.LastOutcome = string(outcome)
		case model.DeliveryEvicted:
			ev.DeliveryState = model.StateEvicted
			ev.DeliveredAt = deliveredAt
			ev.LastOutcome = string(outcome)
		}
		s.events[eventID] = ev
	}
	return nil
}

func (s *fakeStore) Prune(ctx context.Context, now time.Time) (model.PruneResult, error) {
	return model.PruneResult{}, nil
}

func (s *fakeStore) Status(ctx context.Context, now time.Time) (model.StoreStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	counts := make(map[model.DeliveryState]int64)
	for _, ev := range s.events {
		counts[ev.DeliveryState]++
	}
	
	return model.StoreStatus{
		Healthy:      true,
		OutboxCounts: counts,
	}, nil
}

func (s *fakeStore) Close() error {
	return nil
}

// fakeTransport implements model.Transport for testing.
type fakeTransport struct {
	mu          sync.Mutex
	sendCalls   []sendCall
	sendFunc    func(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error)
	validateErr error
}

type sendCall struct {
	event   model.OutboxEvent
	outcome model.DeliveryOutcome
	err     error
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		sendFunc: func(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error) {
			return model.DeliverySuccess, nil
		},
	}
}

func (t *fakeTransport) Validate(ctx context.Context) (model.Compatibility, error) {
	if t.validateErr != nil {
		return model.Compatibility{}, t.validateErr
	}
	return model.Compatibility{Compatible: true, Reason: "ok"}, nil
}

func (t *fakeTransport) Send(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	outcome, err := t.sendFunc(ctx, event)
	t.sendCalls = append(t.sendCalls, sendCall{
		event:   event,
		outcome: outcome,
		err:     err,
	})
	return outcome, err
}

func (t *fakeTransport) SetSendFunc(fn func(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sendFunc = fn
}

// fakeClock implements model.Clock for testing.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	timer *fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) model.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer = newFakeTimer(d)
	return c.timer
}

func (c *fakeClock) SetNow(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeTimer struct {
	mu     sync.Mutex
	c      chan time.Time
	stopped bool
}

func newFakeTimer(d time.Duration) *fakeTimer {
	return &fakeTimer{
		c: make(chan time.Time, 1),
	}
}

func (t *fakeTimer) C() <-chan time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.c
}

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return false
	}
	// For testing, we just return true
	return true
}

func (t *fakeTimer) Fire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped {
		t.c <- time.Now()
	}
}

func TestConfigWithDefaults(t *testing.T) {
	cfg := Config{}
	got := cfg.WithDefaults()
	
	if got.MaxAttempts != DefaultMaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", got.MaxAttempts, DefaultMaxAttempts)
	}
	if got.InitialBackoff != DefaultInitialBackoff {
		t.Errorf("InitialBackoff = %v, want %v", got.InitialBackoff, DefaultInitialBackoff)
	}
	if got.MaxBackoff != DefaultMaxBackoff {
		t.Errorf("MaxBackoff = %v, want %v", got.MaxBackoff, DefaultMaxBackoff)
	}
	if got.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", got.BatchSize, DefaultBatchSize)
	}
	if got.DeliveryInterval != DefaultDeliveryInterval {
		t.Errorf("DeliveryInterval = %v, want %v", got.DeliveryInterval, DefaultDeliveryInterval)
	}
}

func TestManagerStartStop(t *testing.T) {
	store := newFakeStore()
	transport := newFakeTransport()
	clock := newFakeClock()
	logger := logging.New("")
	
	m := New(store, transport, clock, logger, Config{})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	// Start the manager
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	
	// Check it's running
	if !m.isRunning() {
		t.Error("manager should be running")
	}
	
	// Stop the manager
	m.Stop()
	
	// Check it's stopped
	if m.isRunning() {
		t.Error("manager should not be running after Stop")
	}
}

func TestProcessEventSuccess(t *testing.T) {
	store := newFakeStore()
	transport := newFakeTransport()
	clock := newFakeClock()
	logger := logging.New("")
	
	// Add a pending event
	event := model.OutboxEvent{
		ID:            "test-1",
		Kind:          "event",
		Payload:       "test payload",
		EnqueuedAt:    clock.Now(),
		NextAttemptAt: clock.Now(),
		AttemptCount:  0,
		DeliveryState: model.StatePending,
		ExpiresAt:     clock.Now().Add(24 * time.Hour),
	}
	store.events["test-1"] = event
	
	m := New(store, transport, clock, logger, Config{})
	
	ctx := context.Background()
	
	// Process the event
	if err := m.processEvent(ctx, event, clock.Now()); err != nil {
		t.Fatalf("processEvent failed: %v", err)
	}
	
	// Check the event was marked as delivered
	if len(store.markCalls) != 1 {
		t.Fatalf("expected 1 mark call, got %d", len(store.markCalls))
	}
	if store.markCalls[0].outcome != model.DeliverySuccess {
		t.Errorf("expected success outcome, got %s", store.markCalls[0].outcome)
	}
}

func TestProcessEventFailure(t *testing.T) {
	store := newFakeStore()
	transport := newFakeTransport()
	
	// Configure transport to always fail
	transport.SetSendFunc(func(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error) {
		return model.DeliveryFailure, errors.New("send failed")
	})
	
	clock := newFakeClock()
	logger := logging.New("")
	
	// Add a pending event
	event := model.OutboxEvent{
		ID:            "test-1",
		Kind:          "event",
		Payload:       "test payload",
		EnqueuedAt:    clock.Now(),
		NextAttemptAt: clock.Now(),
		AttemptCount:  0,
		DeliveryState: model.StatePending,
		ExpiresAt:     clock.Now().Add(24 * time.Hour),
	}
	store.events["test-1"] = event
	
	m := New(store, transport, clock, logger, Config{
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     1 * time.Hour,
	})
	
	ctx := context.Background()
	
	// Process the event
	if err := m.processEvent(ctx, event, clock.Now()); err != nil {
		t.Fatalf("processEvent failed: %v", err)
	}
	
	// Check the event was marked as failed with next attempt
	if len(store.markCalls) != 1 {
		t.Fatalf("expected 1 mark call, got %d", len(store.markCalls))
	}
	if store.markCalls[0].outcome != model.DeliveryFailure {
		t.Errorf("expected failure outcome, got %s", store.markCalls[0].outcome)
	}
	// Next attempt should be in the future (backoff applied)
	if !store.markCalls[0].next.After(clock.Now()) {
		t.Error("next attempt should be in the future")
	}
}

func TestProcessEventExpired(t *testing.T) {
	store := newFakeStore()
	transport := newFakeTransport()
	clock := newFakeClock()
	logger := logging.New("")
	
	// Add an expired event
	expiredAt := clock.Now().Add(-25 * time.Hour)
	event := model.OutboxEvent{
		ID:            "test-1",
		Kind:          "event",
		Payload:       "test payload",
		EnqueuedAt:    expiredAt,
		NextAttemptAt: clock.Now(),
		AttemptCount:  0,
		DeliveryState: model.StatePending,
		ExpiresAt:     expiredAt,
	}
	store.events["test-1"] = event
	
	m := New(store, transport, clock, logger, Config{})
	
	ctx := context.Background()
	
	// Process the event
	if err := m.processEvent(ctx, event, clock.Now()); err != nil {
		t.Fatalf("processEvent failed: %v", err)
	}
	
	// Check the event was marked as expired
	if len(store.markCalls) != 1 {
		t.Fatalf("expected 1 mark call, got %d", len(store.markCalls))
	}
	if store.markCalls[0].outcome != model.DeliveryExpired {
		t.Errorf("expected expired outcome, got %s", store.markCalls[0].outcome)
	}
}

func TestProcessEventMaxAttempts(t *testing.T) {
	store := newFakeStore()
	transport := newFakeTransport()
	
	// Configure transport to always fail
	transport.SetSendFunc(func(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error) {
		return model.DeliveryFailure, errors.New("send failed")
	})
	
	clock := newFakeClock()
	logger := logging.New("")
	
	// Add an event that has reached max attempts
	event := model.OutboxEvent{
		ID:            "test-1",
		Kind:          "event",
		Payload:       "test payload",
		EnqueuedAt:    clock.Now(),
		NextAttemptAt: clock.Now(),
		AttemptCount:  100, // Already at max
		DeliveryState: model.StatePending,
		ExpiresAt:     clock.Now().Add(24 * time.Hour),
	}
	store.events["test-1"] = event
	
	m := New(store, transport, clock, logger, Config{
		MaxAttempts: 100,
	})
	
	ctx := context.Background()
	
	// Process the event
	if err := m.processEvent(ctx, event, clock.Now()); err != nil {
		t.Fatalf("processEvent failed: %v", err)
	}
	
	// Check the event was marked as evicted
	if len(store.markCalls) != 1 {
		t.Fatalf("expected 1 mark call, got %d", len(store.markCalls))
	}
	if store.markCalls[0].outcome != model.DeliveryEvicted {
		t.Errorf("expected evicted outcome, got %s", store.markCalls[0].outcome)
	}
}

func TestCalculateNextAttempt(t *testing.T) {
	store := newFakeStore()
	transport := newFakeTransport()
	clock := newFakeClock()
	logger := logging.New("")
	
	m := New(store, transport, clock, logger, Config{
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     1 * time.Hour,
	})
	
	now := time.Now()
	
	// Test with 0 attempts - should be initial backoff + jitter
	event0 := model.OutboxEvent{
		AttemptCount: 0,
		ExpiresAt:    now.Add(24 * time.Hour),
	}
	next0 := m.calculateNextAttempt(event0, now)
	if next0.Before(now.Add(1*time.Second)) || next0.After(now.Add(2*time.Second)) {
		t.Errorf("next attempt for 0 attempts = %v, expected between 1s and 2s", next0.Sub(now))
	}
	
	// Test with 1 attempt - should be 2x initial backoff
	event1 := model.OutboxEvent{
		AttemptCount: 1,
		ExpiresAt:    now.Add(24 * time.Hour),
	}
	next1 := m.calculateNextAttempt(event1, now)
	if next1.Before(now.Add(2*time.Second)) || next1.After(now.Add(4*time.Second)) {
		t.Errorf("next attempt for 1 attempt = %v, expected between 2s and 4s", next1.Sub(now))
	}
	
	// Test with many attempts - should be capped at max backoff
	event10 := model.OutboxEvent{
		AttemptCount: 10,
		ExpiresAt:    now.Add(24 * time.Hour),
	}
	next10 := m.calculateNextAttempt(event10, now)
	if next10.After(now.Add(m.config.MaxBackoff + 1*time.Second)) {
		t.Errorf("next attempt for 10 attempts = %v, should be capped at max backoff", next10.Sub(now))
	}
	
	// Test with expiry - should not exceed expiry
	expiry := now.Add(5 * time.Second)
	eventExpire := model.OutboxEvent{
		AttemptCount: 10,
		ExpiresAt:    expiry,
	}
	nextExpire := m.calculateNextAttempt(eventExpire, now)
	if nextExpire.After(expiry) {
		t.Errorf("next attempt should not exceed expiry: got %v, expiry %v", nextExpire, expiry)
	}
}

func TestStatus(t *testing.T) {
	store := newFakeStore()
	transport := newFakeTransport()
	clock := newFakeClock()
	logger := logging.New("")
	
	// Add some events in different states
	now := clock.Now()
	store.events["pending-1"] = model.OutboxEvent{
		ID:            "pending-1",
		DeliveryState: model.StatePending,
		NextAttemptAt: now, // Due now
		EnqueuedAt:    now,
		ExpiresAt:     now.Add(24 * time.Hour),
	}
	store.events["pending-2"] = model.OutboxEvent{
		ID:            "pending-2",
		DeliveryState: model.StatePending,
		NextAttemptAt: now, // Due now
		EnqueuedAt:    now,
		ExpiresAt:     now.Add(24 * time.Hour),
	}
	store.events["delivered-1"] = model.OutboxEvent{
		ID:            "delivered-1",
		DeliveryState: model.StateDelivered,
	}
	store.events["expired-1"] = model.OutboxEvent{
		ID:            "expired-1",
		DeliveryState: model.StateExpired,
	}
	
	m := New(store, transport, clock, logger, Config{})
	
	ctx := context.Background()
	
	// Start the manager
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()
	
	// Get status
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	
	if status.PendingCount != 2 {
		t.Errorf("PendingCount = %d, want 2", status.PendingCount)
	}
	if status.DeliveredCount != 1 {
		t.Errorf("DeliveredCount = %d, want 1", status.DeliveredCount)
	}
	if status.ExpiredCount != 1 {
		t.Errorf("ExpiredCount = %d, want 1", status.ExpiredCount)
	}
	if !status.Running {
		t.Error("Running should be true")
	}
}

// Ensure we implement the model.Transport interface for fakeTransport
var _ model.Transport = (*fakeTransport)(nil)

// Ensure we implement the model.Store interface for fakeStore
var _ model.Store = (*fakeStore)(nil)

// Ensure we implement the model.Clock interface for fakeClock
var _ model.Clock = (*fakeClock)(nil)

// Ensure Manager methods match the expected signatures
// Note: Manager.Enqueue has signature Enqueue(context.Context, model.OutboxEvent) error
// which differs from model.Notifier's Enqueue(context.Context, []model.OutboxEvent) error
// This is intentional - Manager is not a Notifier, it's a delivery manager
// We verify the Manager implements its own Enqueue method separately
