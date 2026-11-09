# wp12-outbox-matrix — implementation brief

## 1 Purpose
Durable ordered event delivery loop, retries/expiry/status, and fail-closed encrypted Matrix adapter.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-02. Parallel with check/incident/report components; integration with WP-13.

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
`internal/outbox/`, `internal/matrixtransport/`, `docs/matrix-cli-validation.md`; WP-00 `Transport`/`Store` signatures; tests/fixtures.

## 5 Requirements (self-contained, from spec)
Outbox keeps separate events (no grouping/coalescing), single sender in enqueue order, sender and Matrix crypto store under exclusive lock. Retry failure exponential backoff starting 1 second capped at 1 hour, eligible only while pending and unexpired. Before send expire pending older than `pending_max_age` from original enqueue time (default 24h). Delivered records retained 30 days. Status distinguishes pending/delivered/expired/evicted/ambiguous. Transport retry reuses internal event ID; no Matrix idempotency promise. Best effort durable retries, not exactly once/eventual guarantee. Mark delivered only on explicit success. Timeout/interruption after possible acceptance is ambiguous: keep eligible pending for retry; duplicate can result; surface ambiguity.

Only existing externally managed `matrix-commander-rs`, E2EE mandatory. **No CLI binary name, version, flags, encryption defaults/state path/exit meaning may be assumed. Inspect real target installation and its own version/help/docs before adapter.** Build isolated profile only after validated version/interface evidence. `vshm validate-matrix` non-sending: executable present, exact installed version matches profile, account/room/state configured, encrypted-send capability and explicit success reporting supported, crypto state writable only by vshm. Unknown/unsupported/unverified encryption, account/room/state failure => no send, pending remains; blocks delivery only, never monitoring/startup/local persistence/report. `vshm test-matrix` is only test command that sends; explicit operator-requested encrypted test to configured approved/disposable room. Verify encryption through supported explicit CLI result or independently documented encrypted-event verification. If cannot prove, fail closed. Record version, inspected evidence, interface and encrypted verification in `docs/matrix-cli-validation.md` before delivery enabled. No plaintext fallback/other transport. Subprocess argument vectors, bounded timeouts/diagnostics, sanitized categories; no credentials/crypto/plaintext in logs. Host-specific crypto state private, mode 0600, never copied.

## 6 Design decisions already fixed
**No implementation of real adapter until target is inspected. First build fake transport.** Ask user for target host/environment, exact installed `matrix-commander-rs` version, executable path, own documentation/help output, account/room setup, private state path/ownership, documented encrypted send command, explicit result semantics, and acceptable disposable/approved room plus independent E2EE verification evidence. Do not invent flags. Target-specific gate is mandatory.

## 7 Open questions — "ASK THE USER before implementing"
Matrix CLI behavior is wholly unspecified; **ASK THE USER before implementing any real matrix-commander-rs command or assuming CLI compatibility**. Also ask about report/failure message text only if not resolved in WP-10/11 and approved transport lock path.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 matrix compatibility/unsupported or unverified E2EE/missing account-room-state blocks sends while monitoring continues; explicit test only test send; no plaintext; second agent refused; logs redacted. Retry/order/backoff/expiry/eviction/ambiguous/retention tested with fake transport. Release gate: target version/help/docs inspection, exact profile recorded, non-sending validate, approved encrypted test and verifiable evidence.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run fake-transport unit/integration tests without live account. Real host: first inspect documented version/help; run `vshm validate-matrix`; only with explicit user approval use `vshm test-matrix` in approved disposable room; record evidence. Never send during automated tests.
