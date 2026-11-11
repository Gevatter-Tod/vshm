# wp03-scheduler — implementation brief

## 1 Purpose
Fixed worker scheduler, monotonic cadence, absolute deadlines, cleanup and result handoff.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-02, WP-10; scheduler can be built with WPs 04–09/12 in parallel after shared APIs freeze.

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
`internal/scheduler/`; `internal/model/` changes only via WP-00 request; `tests/fixtures/`; `internal/cli/` coordinated with WP-13.

## 5 Requirements (self-contained, from spec)
Global interval default 30m, timeout 10s, per-check overrides; intervals 1m–7d, timeouts 1–300s and less than interval. Four fixed workers, distinct checks concurrently, never overlap same check. Every check runs promptly after startup then monotonic cadence. No missed ticks replay; skip due tick if previous execution still active and log skip, no fabricated failure. Exactly one absolute deadline created at worker execution start spans DNS, connect, I/O, complete query/response, subprocess; same context propagated, no reset. On deadline exactly one failed observation. Close owned connections and terminate owned subprocess process group. Cleanup-only grace max 2s; if cleanup not finished mark unhealthy and exit nonzero for systemd restart, leaving no worker/child. Persist only executions actually run; downtime never creates failure/recovery. Results go to incident service; no direct store/outbox writes. call Store.Prune once per hour. Scheduler owns the lifecycle and cleanup of check adapters.

## 6 Design decisions already fixed
Inject clock/timer and checker interfaces. Schedule based on monotonic clock; store actual timestamps UTC. Single timeout result guard prevents racing adapter result and timeout from double emission. Skip logs sanitized. Next run always takes place at the defined date and time. If the last run has not finished yet, just skip the next run.

## 7 Open questions — "ASK THE USER before implementing"
Ask user only if adapter cleanup ownership or scheduler timer semantics need definition; fixed pool/deadline/2s grace are normative. Clarify process termination mechanism if Go APIs/platform leave ambiguity, but do not weaken behavior.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8: default/per-check interval and timeout; prompt initial run, monotonic cadence, missed tick, four-worker limit/no overlap, one deadline over all work, exactly one timeout result, connection/process group cleanup within 2s, cleanup failure nonzero/no children, downtime no fabricated results.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run deterministic fake-clock/fake-check tests; run `vshm once` or short `vshm run` against local fake adapter; inspect logs and status. Use no public network.
