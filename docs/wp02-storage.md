# wp02-storage — implementation brief

## 1 Purpose
SQLite store, schema migrations, retention, single-instance lock, bounded storage/expiry/low-space policy.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00. Parallel with config/check adapters/scheduler once schema and signatures frozen; integrate WP-10/11/12 atomics.

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
`internal/storage/`; `internal/model/` changes only by proposal; `tests/fixtures/`; `docs/architecture.md`.

## 5 Requirements (self-contained, from spec)
Implement exact WP-00 proposed schema only after user approves. SQLite pure-Go modernc v1.61.1, WAL, bounded busy timeout, bounded transactions and short consistent read snapshots; migrations versioned. Persist UTC completion/enqueue times; increasing deterministic event sequence. Atomic observation + incident + event per failure/recovery; report-date unique key + event in same transaction, key first. Delivered event retention 30d; observations 90d; incident current state/report keys indefinite. Process lock ensures one `run`; second fails clearly. State dir 0700 vshm:vshm, DB/lock 0600.

Before startup and each write transaction check accessible bytes on DB filesystem; reading failure = visible fault/no destructive action. Expire pending events older than configured pending_max_age (default 24h) before delivery; expiry first. If free bytes below configured 100 MiB default threshold, evict oldest pending first, one bounded row/batch at a time, each row at most once per trigger, until threshold or none. Maintain durable discard aggregate counters by reason, transactionally with deletes; distinguish age expiry vs low-space eviction. Commit failure means do not claim deletion/accounting. Checkpoint after each committed pruning batch, attempt WAL truncate when safe, recheck space. No VACUUM/emergency file deletion; logical deletion may not release filesystem bytes. Preserve incident state, observations and report keys; delivered/other state not eviction candidates.

On observation/report/outbox storage-capacity error (SQLite full, ENOSPC, quota), expire old rows and force pruning, discard oldest pending first and continue while below threshold/no rows, checkpoint/recheck, then retry original failed transaction exactly once. If no pending row, still checkpoint/recheck. Retry once even if pruning/checkpoint fails; report storage fault on pruning/checkpoint failure or retry failure. Do not prune on corruption, malformed DB, permission, non-capacity errors. Preserve prior committed state. Ordered retries/expiry policy shared with WP-12.

## 6 Design decisions already fixed
Centralize all DB writes through store transactions; make committed success explicit. Keep snapshots short. SQLite logical delete != filesystem free. No deleting unrelated files or incidents for recovery. Fault/status reporting sanitized and bounded. Lock protects agent process; Matrix sender/crypto lock is separately coordinated by WP-12.

## 7 Open questions — "ASK THE USER before implementing"
Ask user to approve schema and operation-level transaction API from WP-00; SQLite table details are absent in spec. Confirm how `min_available_bytes` filesystem probe will be implemented/tested, quotas and busy/full classification, batch size, checkpoint conditions and lock file mechanism. These implementation choices are proposed — confirm with user.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8: temp SQLite, transactional observation/state/outbox and report key; restart persistence; 30-/90-day retention; pending survives restart then 24h expiry/oldest-first eviction; counts distinguish discarded vs delivered; low/full disk pruning/checkpoint/recheck/one retry; corruption/permission does not prune; storage faults; second instance refused.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run storage tests with temporary DB and fault-injected capacity/busy/corruption/filesystem providers; run `vshm status` and `vshm run` with temporary state path; prove concurrent second `run` refusal. Never use live `/var/lib/vshm` in tests.
