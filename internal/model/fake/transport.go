package fake

import (
	"context"
	"sync"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Transport is a fake transport for testing.
type Transport struct {
	mu           sync.RWMutex
	compatibility model.Compatibility
	sends        []model.OutboxEvent
	failures     []error
}

// NewTransport creates a new fake transport.
func NewTransport() *Transport {
	return &Transport{
		compatibility: model.Compatibility{Compatible: true},
	}
}

// SetCompatibility sets the compatibility state.
func (t *Transport) SetCompatibility(compatible bool, reason string) {
	t.compatibility.Compatible = compatible
	t.compatibility.Reason = reason
}

// Validate checks transport compatibility.
func (t *Transport) Validate(ctx context.Context) (model.Compatibility, error) {
	return t.compatibility, nil
}

// Send delivers an event.
func (t *Transport) Send(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.sends = append(t.sends, event)

	// Simulate success
	return model.DeliverySuccess, nil
}

// SetFailure sets the transport to fail.
func (t *Transport) SetFailure(err error) {
	t.failures = append(t.failures, err)
}

// SetAmbiguous sets the transport to return ambiguous.
func (t *Transport) SetAmbiguous() {
	t.compatibility.Compatible = false
	t.compatibility.Reason = "ambiguous"
}

// GetSends returns all sent events.
func (t *Transport) GetSends() []model.OutboxEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sends
}

// GetFailures returns all failures.
func (t *Transport) GetFailures() []error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.failures
}
