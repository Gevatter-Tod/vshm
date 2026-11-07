# wp04-check-systemd — implementation brief

## 1 Purpose
Read-only systemd unit ActiveState check adapter.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-01. Parallel with WP-05–09.

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
  Prune(context.Context, time.Time, int64) (PruneResult, error)
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
`internal/checks/systemd/`; `internal/model/` contract from WP-00; `tests/fixtures/`. Implement WP-00 `Check` signature repeated below.

## 5 Requirements (self-contained, from spec)
Take configured unit; read its `ActiveState` via stable systemd interface, prefer system D-Bus API, safely invoked `systemctl show` property query is fallback. Success exactly state `active`; inactive/failed/unknown/query error is one failed normalized observation with sanitized reason. Never start/stop/restart/enable/disable or otherwise change target. One incoming absolute deadline covers bus or subprocess. Subprocess argument vector only, bounded, sanitized, owned process cleanup per WP-03. Do not expose unit diagnostics beyond short sanitized reason; unit itself is configured nonsecret identity.

## 6 Design decisions already fixed
Encapsulate query backend for tests; distinguish target inactive from inability to query with proposed reason codes pending WP-00 vocabulary. Stay read-only.

## 7 Open questions — "ASK THE USER before implementing"
**ASK THE USER before implementing**: systemd D-Bus vs safely invoked `systemctl show` fallback selection and allowed property command/version semantics are underspecified. Spec prefers D-Bus but permits fallback. Confirm environment test target; no assumptions about unavailable D-Bus. Reason codes from WP-00 are proposed.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 check success/failure; systemd integration non-root and target not remediated; sanitized output/logs. Fake backend tests active success, non-active, query failure, deadline and prove no modifying calls.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run unit tests with fake bus/process; local integration `vshm check <ID>` against a disposable/local systemd unit if available (no state change), capture result; otherwise record fake-backend runtime.
