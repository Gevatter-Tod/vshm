// Package metrics provides system metrics collection.
package metrics

import (
	"context"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Collector collects system metrics.
type Collector struct {
}

// New creates a new metrics collector.
func New() *Collector {
	return &Collector{}
}

// Collect collects system metrics.
func (c *Collector) Collect(ctx context.Context, now time.Time) (model.Snapshot, error) {
	// Placeholder - implement metrics collection
	return model.Snapshot{}, nil
}
