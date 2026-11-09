# wp06-check-ping — implementation brief

## 1 Purpose
Single-probe unprivileged system ping check adapter.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-01. Parallel with WP-04,05,07–09.

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
`internal/checks/ping/`; WP-00 model contract; `tests/fixtures/`.

## 5 Requirements (self-contained, from spec)
Use system `ping` executable with one probe within same absolute check deadline. Success requires a valid reply, not merely a launch. Missing executable, unavailable unprivileged ping socket, permission/probe failure => failed observation, never skip. Run agent and executable without capabilities. Never grant `CAP_NET_RAW` to either; no capability fallback and never weaken systemd sandbox. Subprocess invocation uses argument vector, no shell, bounded output, sanitized. Kill owned process group during deadline cleanup as WP-03.

## 6 Design decisions already fixed
Inject executable runner for tests; classify absent/permission/no-reply/timeout safely; do not depend on capabilities.

## 7 Open questions — "ASK THE USER before implementing"
Ask user about supported ping implementations/portable flags and what constitutes a validated reply for the target Linux image; spec requires system `ping`, one probe, reply, unprivileged operation but pins no implementation. Must not solve by capabilities.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 ping success/failure and sandbox; no live ping required in CI. Tests mock valid reply, no reply, absent executable, permission/unprivileged socket unavailable, timeout, bounded output, no capability assumption.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run mock-runner tests; if local host permits unprivileged ping, `vshm check <ID>` against loopback/local target; otherwise document failed/unavailable target but verify adapter returns failure, not skip.
