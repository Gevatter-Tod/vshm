# wp05-check-http — implementation brief

## 1 Purpose
HTTP(S) GET health-check adapter.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-01. Parallel with WP-04,06–09.

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
`internal/checks/http/`; WP-00 model contract; tests in `tests/fixtures/`.

## 5 Requirements (self-contained, from spec)
GET configured URL under one absolute incoming deadline. TLS cert and hostname verification mandatory. Disable redirects. Exact expected status (default 200), plus optional literal `contains` in bounded body. Read cap 1 MiB; response exceeding cap fails (detect >cap, do not silently truncate as success). Network/TLS/status/assertion/timeout errors fail. Never include body, credentials, or secret-bearing paths in result/log/message. Optional `credential_file` INI `[basic]` username/password is permitted only with verified HTTPS; reject use over plain HTTP; never log/serialize it. One check deadline includes DNS, TLS/connect, body read. No secrets in URL diagnostics.

## 6 Design decisions already fixed
Use standard-library HTTP client with redirect rejection and context from scheduler; classify sanitized failures; constrain body allocation. Status/result must carry short safe reason only.

## 7 Open questions — "ASK THE USER before implementing"
Ask user if URL normalization, unbounded content handling, or whether a body exactly 1 MiB is accepted (proposed interpretation: at most 1 MiB accepted, >1 MiB fails). Confirm policy for HTTP auth credential file to HTTPS redirects (redirects disabled).

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 HTTP success/failure, TLS verification, redirect rejection, body cap, redaction. Test exact/default status, literal contains, body over cap, malformed/network/TLS failure, redirect failure, timeout, HTTP-basic plain HTTP rejection and no secret leaks.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run tests against `httptest` local HTTPS server with test CA and local HTTP; run `vshm check <ID>` against local fake target; record that TLS verification remains on.
