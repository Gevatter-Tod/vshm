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
Fixed configurable local HH:MM default 09:00; no timezone config. At most one report per local calendar date. At scheduled instant insert unique local-date key and its report outbox event in one transaction, key before event, commit both/neither. At startup schedule only next future report; omit downtime-missed report/no catchup. DST gap => first valid local instant after gap; fold => first occurrence. Key prevents duplicates on restart, clock adjustment, DST transition. Report is a separate daily plain-text message; snapshot resource metrics at render time. Include every configured check’s latest status (emoticon), age since the last observation, short sanitized outcome/reason, and resource metrics; `NO RESULT` before any run. Mark `STALE` (⌛) when age > that check’s interval + timeout. The report header is the only line with a date/time (host-local `DD.MM.YYYY HH:MM:SS`, no offset); no other line carries a timestamp. Missing metric UNAVAILABLE, not zero. Report-date key remains even if event later evicted; no regeneration.

Report text format (user-confirmed, supersedes the numeric-offset wording above): plain text, metrics block first, then one line per configured check. The header line is the only line carrying a date/time: host-local `DD.MM.YYYY HH:MM:SS` with no UTC offset; check lines and the metrics block carry no timestamps (checks show only `age`). Each check line starts with a status emoticon — ✅ success, ❌ failure, ❓ with `NO RESULT` before any run — and carries ⌛ when STALE (age > that check's interval + timeout). Example:

```
daily report host=my-server at=20.11.2026 22:46:31
metrics:
  load1=0.10 load5=0.11 load15=0.09
  ...
✅ check systemd-check (systemd): age=1s reason_code=success reason=...
❌⌛ check http-check (http): age=1d0h0m0s reason_code=http_status reason=HTTP 503
❓ check ping-check (ping): NO RESULT
```

## 6 Design decisions already fixed
Use local calendar and explicit DST gap/fold resolution; storage snapshot is short-lived consistent snapshot, close promptly; enqueue within same transaction with report key.

## 7 Open questions — asked and answered by the user
- **Report text/order/format**: decided — metrics block before check lines; host-local `DD.MM.YYYY HH:MM:SS` timestamps without UTC offset (overrides the "numeric UTC offset" wording in §5/§8); status as leading emoticon (✅/❌/❓ NO RESULT), ⌛ appended when STALE. See §5 for the full example.
- **Local-date key representation**: confirmed — local calendar date in `2006-01-02` form as the outbox dedupe key.
- **Timezone database**: schedules and rendered timestamps rely on the host's local timezone data (`time.Local`); no timezone config exists.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 report fields/NO RESULT/STALE/unavailable/DD.MM.YYYY local timestamps/emoticon status/DST gap-fold/one date/restart dedup/no catchup. Test fake clock and zones, gap/fold, clock movement, downtime, transactional rollback and eviction keeps date key.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run deterministic timezone tests; render preview from fixture SQLite/metrics, run `vshm run` across simulated due time and inspect fake outbox (no Matrix necessary).
