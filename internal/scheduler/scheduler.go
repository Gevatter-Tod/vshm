// Package scheduler provides monotonic check scheduling with fixed concurrency.
package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Scheduler manages check execution with monotonic cadence.
type Scheduler struct {
	checks    []model.Check
	config    Config
	clock     model.Clock
	mu        sync.Mutex
	workers   []worker
}

// Config is the scheduler configuration.
type Config struct {
	Concurrency int
	Interval    time.Duration
}

// New creates a new scheduler.
func New(clock model.Clock) *Scheduler {
	return &Scheduler{
		clock: clock,
	}
}

// SetChecks sets the checks to schedule.
func (s *Scheduler) SetChecks(checks []model.Check) {
	s.checks = checks
}

// SetConfig sets the scheduler configuration.
func (s *Scheduler) SetConfig(cfg Config) {
	s.config = cfg
}

// Run starts the scheduler.
func (s *Scheduler) Run(ctx context.Context) error {
	// Placeholder - implement scheduler logic
	return nil
}

// worker represents a scheduler worker.
type worker struct {
	index int
	check model.Check
}
