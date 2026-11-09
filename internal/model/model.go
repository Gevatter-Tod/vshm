// Package model defines the shared domain types and interfaces for vshm.
// These are the frozen contracts established in WP-00.
package model

import (
	"context"
	"time"
)

// CheckResult represents the result of a single check execution.
type CheckResult struct {
	CheckID     string
	CheckType   string
	Success     bool
	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
	ReasonCode  string
	Reason      string
}

// CheckConfig is the validated configuration for a check.
// Implementations must provide CheckID and CheckType.
type CheckConfig interface {
	CheckID() string
	CheckType() string
}

// Check is the interface for all check adapters.
// Implementations must be read-only on targets and return exactly one result.
type Check interface {
	Type() string
	Run(ctx context.Context, cfg CheckConfig) CheckResult
}

// Clock provides time for testing.
type Clock interface {
	Now() time.Time
	NewTimer(duration time.Duration) Timer
}

// Timer is a clock timer.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(duration time.Duration) bool
}

// Store is the interface for persistent storage.
type Store interface {
	// RecordObservation stores a check result.
	RecordObservation(ctx context.Context, result CheckResult) error
	// RecordObservationAndIncident stores a result and creates an incident if needed.
	RecordObservationAndIncident(ctx context.Context, result CheckResult, mutation IncidentMutation, events []OutboxEvent) error
	// Snapshot returns current system state.
	Snapshot(ctx context.Context, now time.Time) (Snapshot, error)
	// EnqueueReport adds a report to the outbox.
	EnqueueReport(ctx context.Context, dateKey string, event OutboxEvent) (bool, error)
	// Pending returns events ready for delivery.
	Pending(ctx context.Context, now time.Time, max int) ([]OutboxEvent, error)
	// MarkDelivery updates delivery status.
	MarkDelivery(ctx context.Context, eventID string, outcome DeliveryOutcome, deliveredAt, enqueuedAt time.Time, reason string) error
	// Prune removes old records.
	Prune(ctx context.Context, now time.Time) (PruneResult, error)
	// Status returns storage health.
	Status(ctx context.Context, now time.Time) (StoreStatus, error)
	// Close releases resources.
	Close() error
}

// Transport handles Matrix event delivery.
type Transport interface {
	// Validate checks transport compatibility.
	Validate(ctx context.Context) (Compatibility, error)
	// Send delivers an event.
	Send(ctx context.Context, event OutboxEvent) (DeliveryOutcome, error)
}

// Notifier queues events for later processing.
type Notifier interface {
	Enqueue(ctx context.Context, events []OutboxEvent) error
}

// Reporter renders snapshots into events.
type Reporter interface {
	Render(ctx context.Context, snapshot Snapshot, now time.Time) (OutboxEvent, error)
	Schedule(ctx context.Context) error
}

// Registry manages check adapters.
type Registry interface {
	Register(checkType string, check Check) error
	Get(checkType string) (Check, bool)
	Types() []string
}

// IncidentMutation describes an incident state change.
type IncidentMutation struct {
	HostName    string
	CheckID     string
	Open        bool
	LastFailure time.Time
	ReasonCode  string
	Reason      string
	ResolvedAt  *time.Time
}

// OutboxEvent represents a queued message.
type OutboxEvent struct {
	ID            string
	Sequence      int64
	HostName      string
	Kind          string
	CheckID       string
	CheckType     string
	IncidentID    string
	LocalReport   string
	EnqueuedAt    time.Time
	NextAttemptAt time.Time
	AttemptCount  int
	DeliveryState DeliveryState
	LastOutcome   string
	DeliveredAt   time.Time
	ExpiresAt     time.Time
	Payload       string
}

// Snapshot represents system state at a point in time.
type Snapshot struct {
	HostName   string
	Time       time.Time
	Checks     []CheckStatus
	Incidents  []IncidentSummary
	LastReport time.Time
}

// CheckStatus is the current status of a check.
type CheckStatus struct {
	CheckID       string
	CheckType     string
	LastStatus    string
	LastStarted   time.Time
	LastCompleted time.Time
	LastDuration  time.Duration
	ReasonCode    string
	Reason        string
	Successes24h  int64
	Failures24h   int64
}

// IncidentSummary is a summary of an incident.
type IncidentSummary struct {
	IncidentID  string
	HostName    string
	CheckID     string
	OpenedAt    time.Time
	LastFailure time.Time
	ReasonCode  string
	Reason      string
	ResolvedAt  *time.Time
}

// DeliveryOutcome represents the result of a delivery attempt.
type DeliveryOutcome string

const (
	DeliverySuccess   DeliveryOutcome = "success"
	DeliveryFailure   DeliveryOutcome = "failure"
	DeliveryAmbiguous DeliveryOutcome = "ambiguous"
	DeliveryExpired   DeliveryOutcome = "expired"
	DeliveryEvicted   DeliveryOutcome = "evicted"
)

// DeliveryState is the current state of an outbox event.
type DeliveryState string

const (
	StatePending   DeliveryState = "pending"
	StateDelivered DeliveryState = "delivered"
	StateExpired   DeliveryState = "expired"
	StateEvicted   DeliveryState = "evicted"
	StateAmbiguous DeliveryState = "ambiguous"
)

// PruneResult describes pruning operation results.
type PruneResult struct {
	Observations int64
	OutboxRows   int64
}

// StoreStatus describes storage health.
type StoreStatus struct {
	Healthy       bool
	LastError     string
	RecordsCount  int64
	OutboxCounts  map[DeliveryState]int64
	DBSizeBytes   int64
	SchemaVersion string
	LockPath      string
}

// Compatibility describes transport compatibility.
type Compatibility struct {
	Compatible bool
	Reason     string
}
