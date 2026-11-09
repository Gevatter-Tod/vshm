# wp09-metrics — implementation brief

## 1 Purpose
Report-only resource metrics collection; never alert.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-01. Parallel with checks, scheduler/report after interfaces frozen.

## 3 Interfaces consumed / provided (reference WP-00 names, repeat signatures the agent needs)
### Shared Go API (WP-00 contract; repeat needed signatures)
```go
type Check interface { Type() string; Run(ctx context.Context, cfg CheckConfig) CheckResult }
type CheckConfig interface { CheckID() string; CheckType() string }
type CheckResult struct { CheckID string; CheckType string; Success bool; StartedAt time.Time; CompletedAt time.Time; Duration time.Duration; ReasonCode string; Reason string }
type Clock interface { Now() time.Time; NewTimer(time.Duration) Timer }
type Timer interface { C() <-chan time.Time; Stop() bool; Reset(time.Duration) bool }
type Store interface {
  RecordObservation(context.Context, CheckResult) error
  RecordObservationAndIncident(context.Context, CheckResult, IncidentMutation, []OutboxEvent) error
  Snapshot(context.Context, time.Time) (Snapshot, error)
  EnqueueReport(context.Context, string, OutboxEvent) (bool, error)
  Pending(context.Context, time.Time, int) ([]OutboxEvent, error)
  MarkDelivery(context.Context, string, DeliveryOutcome, time.Time, time.Time, string) error
  Prune(context.Context, time.Time) (PruneResult, error)
  Status(context.Context, time.Time) (StoreStatus, error)
  Close() error
}
type Transport interface { Validate(context.Context) (Compatibility, error); Send(context.Context, OutboxEvent) (DeliveryOutcome, error) }
type Notifier interface { Enqueue(context.Context, []OutboxEvent) error }
type Reporter interface { Render(context.Context, Snapshot, time.Time) (OutboxEvent, error); Schedule(context.Context) error }
type Registry interface { Register(string, Check) error; Get(string) (Check, bool); Types() []string }
```
These are concrete proposed API signatures to freeze in WP-00; refine only in WP-00 before other packages code. Supporting proposed model types: `IncidentMutation`, `OutboxEvent`, `Snapshot`, `PruneResult`, `StoreStatus`, `Compatibility`, `DeliveryOutcome`; all must be defined in `internal/model` or owned WP-00 packages without secret serialization. The `Check` adapter receives validated immutable typed config and a single context deadline. `CheckResult` timestamps are UTC; `Reason` short/sanitized. Add `context` import as required. Signatures/API shape are **proposed — confirm with user**, as spec defines responsibilities but no Go signatures.

This package consumes WP-00 `CheckResult`, `Clock`, `Store`, `Transport`, `Notifier`, `Reporter`, `Registry`, `CheckConfig`, and related model types as relevant; it provides only its assigned concrete adapter/service. It must not modify shared signatures outside an explicit user-approved WP-00 change.

## 4 Files to create/touch (exact paths in the required tree)
`internal/metrics/`; `internal/model/` only via WP-00 request; `tests/fixtures/`.

## 5 Requirements (self-contained, from spec)
Capture when each daily report is rendered. Linux 1-minute load average; RAM total and available from `/proc/meminfo` `MemTotal`/`MemAvailable`, used=total−available; used/total bytes for each configured mount using `statvfs`; uptime from `/proc/uptime`. Explicit units in report. If source/mount unavailable, return `UNAVAILABLE` plus reason, never zero or guessed value. Metrics are report-only: never create check failures, incidents, or alerts. Mounts configured under optional `[metrics] mounts`.

## 6 Design decisions already fixed
Use injected proc reader and filesystem provider for tests; typed values/availability avoid conflating missing with zero. Keep implementation Linux amd64 only.

## 7 Open questions — "ASK THE USER before implementing"
Ask user about `statvfs` implementation in CGO-free Go and report formatting/units if not finalized; exact text format is missing (proposed — confirm with user). Confirm omitted `[metrics].mounts` default.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 metrics definitions/units/unavailable values; report missing metric `UNAVAILABLE`, no metric alerts. Test exact formulas, mount unavailable, malformed/missing proc fields, units, snapshot time.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run tests against fixtures and local `/proc` plus root mount; execute `vshm status`/local report preview if wired; ensure absent fixture source reports UNAVAILABLE.
