# wp11-daily-report — implementation brief

## 1 Purpose
Daily report composition and local-time schedule with durable per-date deduplication.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-02, WP-09. Parallel with adapters, incidents, outbox.

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
`internal/reports/`; `internal/metrics/`; WP-00 store/model signatures; tests/fixtures.

## 5 Requirements (self-contained, from spec)
Fixed configurable local HH:MM default 09:00; no timezone config. At most one report per local calendar date. At scheduled instant insert unique local-date key and its report outbox event in one transaction, key before event, commit both/neither. At startup schedule only next future report; omit downtime-missed report/no catchup. DST gap => first valid local instant after gap; fold => first occurrence. Key prevents duplicates on restart, clock adjustment, DST transition. Report is a separate daily plain-text message; snapshot resource metrics at render time. Include every configured check’s latest status, last observation time and age, short sanitized outcome/reason, and resource metrics; `NO RESULT` before any run. Mark `STALE` when age > that check’s interval + timeout, retain observation timestamp. Include host-local report timestamp numeric UTC offset. Missing metric UNAVAILABLE, not zero. Report-date key remains even if event later evicted; no regeneration.

## 6 Design decisions already fixed
Use local calendar and explicit DST gap/fold resolution; storage snapshot is short-lived consistent snapshot, close promptly; enqueue within same transaction with report key.

## 7 Open questions — "ASK THE USER before implementing"
**ASK THE USER before implementing**: report exact text/order/format is absent (proposed — confirm with user); confirm stable local-date key representation if schema not approved. DST behavior is normative, but ask if host timezone database assumptions require documenting.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 report fields/NO RESULT/STALE/unavailable/numeric offset/DST gap-fold/one date/restart dedup/no catchup. Test fake clock and zones, gap/fold, clock movement, downtime, transactional rollback and eviction keeps date key.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run deterministic timezone tests; render preview from fixture SQLite/metrics, run `vshm run` across simulated due time and inspect fake outbox (no Matrix necessary).
