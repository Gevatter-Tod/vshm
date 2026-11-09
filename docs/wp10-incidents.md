# wp10-incidents — implementation brief

## 1 Purpose
Transactional incident state transitions and one failure/recovery event per qualifying observation.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-02. Parallel with scheduler, check adapters, report and outbox.

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
`internal/incidents/`; WP-00 model/store/transport signatures; `tests/fixtures/`.

## 5 Requirements (self-contained, from spec)
Key by exact `host_name` plus stable check ID. Each observed failure persisted transactionally and enqueues exactly one distinct, uniquely identified failure event regardless of incident already open. First failure opens incident; repeat failures update last failure time/reason, do not open duplicate incident but each gets own event. First later success closes incident and enqueues one recovery; success without open incident records only. Restart preserves state; downtime alone does not transition. No confirmation checks, delay, grouped messages, separately scheduled warnings/reminders. A send retry is not a new result/event. Plain-text (not Markdown), each message identifies host, check ID/type, event kind, local timestamp with numeric UTC offset, sanitized reason/status; recovery includes incident start time. One event per failed observation, separate events across checks. Atomic observation + state + event.

## 6 Design decisions already fixed
No unexplained flap suppression: spec explicitly alerts every actual failed observation. The prompt’s mention of threshold/flap is not specification; do not introduce thresholds. Sanitization and local timestamp formatting shared.

## 7 Open questions — "ASK THE USER before implementing"
**ASK THE USER before implementing**: exact message wording/line format and proposed reason-code vocabulary are unspecified. No confirmation/flap threshold exists; if user expects a threshold it conflicts with fixed immediate per-failure contract and needs explicit spec change. Schema transaction shape depends on approved WP-00.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 every failed observation one event including repeats; no grouping/warnings/confirmation; recovery once; success without open incident silent; persistence across restart; messages sanitized/no secrets. Test transitions/atomic failure rollback, timestamps and repeated distinct events.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run unit tests with fake Store/Clock; run `vshm once` against scripted fake checker and inspect `vshm status` and fake transport event ledger; verify no sends for healthy observations.
