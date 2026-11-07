// Package matrixtransport provides Matrix event delivery.
package matrixtransport

import (
	"context"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Transport handles Matrix event delivery.
type Transport struct {
	cliPath string
}

// New creates a new Matrix transport.
func New(cliPath string) (*Transport, error) {
	if cliPath == "" {
		cliPath = "matrix-commander-rs"
	}
	return &Transport{cliPath: cliPath}, nil
}

// Validate checks transport compatibility.
func (t *Transport) Validate(ctx context.Context) (model.Compatibility, error) {
	// Placeholder - implement validation
	return model.Compatibility{Compatible: true}, nil
}

// Send delivers an event.
func (t *Transport) Send(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error) {
	// Placeholder - implement event delivery
	return model.DeliverySuccess, nil
}
