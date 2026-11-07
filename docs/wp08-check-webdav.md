# wp08-check-webdav — implementation brief

## 1 Purpose
Read-only bounded-depth WebDAV listing adapter.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-01. Parallel with WP-04–07,09.

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
`internal/checks/webdav/`; WP-00 model contract; `tests/fixtures/`.

## 5 Requirements (self-contained, from spec)
Only read-only `PROPFIND` with required depth exactly 0 or 1; never unbounded. Require HTTP 207, well-formed XML with DAV `multistatus` root, and at least one `response` containing `href`. Every response must have at least one `propstat` with a 2xx status; if any response has no successful propstat fail. Malformed XML, HTML error, invalid status, timeout fail. Never PUT/DELETE/MOVE or modify target. Optional `credential_file` INI `[basic]` only over verified HTTPS; no Basic credentials over plain HTTP. TLS verification mandatory; redirects disabled for consistency with HTTP checks. Limit response processing; never expose body/credentials/secret paths. Same absolute check deadline covers all network and XML processing.

## 6 Design decisions already fixed
Use standard XML parser with bounded response handling and explicit namespace/local-name checks. Separate HTTP status from XML structure errors; sanitize reasons.

## 7 Open questions — "ASK THE USER before implementing"
Ask user to confirm response-body cap for WebDAV (spec gives HTTP body cap specifically; **proposed — confirm with user** use same 1 MiB safe cap) and whether redirect rejection is required for WebDAV (security-consistent proposal; spec explicitly mandates it only for HTTP). Define malformed/non-2xx propstat parsing edge cases if unclear.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 malformed WebDAV XML/status handling, TLS and redaction. Test 207 valid multistatus, wrong root, malformed XML/HTML, no response/href, each response missing successful propstat, 2xx propstat, wrong HTTP status, depth 0/1 only, timeout, no modifying method, credentials only verified HTTPS.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run tests with local `httptest` server; exercise `vshm check <ID>` against fake local PROPFIND endpoint and inspect captured method/depth.
