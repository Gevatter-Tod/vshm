# wp07-check-mysql — implementation brief

## 1 Purpose
Read-only MySQL query health adapter.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00, WP-01. Parallel with WP-04–06,08–09.

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
`internal/checks/mysql/`; WP-00 model contract; `tests/fixtures/`.

## 5 Requirements (self-contained, from spec)
Use `github.com/go-sql-driver/mysql v1.10.1`; TCP must use verified TLS with configured CA and hostname verification. Local Unix socket permitted without network TLS. TCP `tls_ca` required. `port` default 3306. Credential file INI `[client]` user/password/optional database; explicit check database takes precedence. Values private and never logged. Under the one absolute deadline connect and execute. Accept exactly one read-only `SELECT` statement with at most one optional trailing semicolon; reject other leading statements and multiple statements. Dedicated DB account with read-only grants is mandatory security boundary regardless of parser. Success only after execution and fetching at least one row; discard values immediately. Database/TLS/query/timeout failures fail. No SQL text/values/results in logs/messages/reports.

## 6 Design decisions already fixed
Do not claim parser proves read-only: enforce narrow single SELECT shape and require read-only grants. Ensure context includes DNS, connection, query, row retrieval. Don’t log DSN or secrets.

## 7 Open questions — "ASK THE USER before implementing"
**ASK THE USER before implementing**: exact accepted grammar for a single read-only SELECT and comments/CTEs/quoted semicolons is not specified; propose a conservative parser/reject-ambiguous grammar, confirm. Confirm TLS CA path constraints, Unix socket config representation, server CA/hostname details. Mandatory database grants remain user/operator responsibility.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 SQL single-SELECT validation, TLS, query success/failure, secret/result redaction. Test exactly one SELECT, optional semicolon, reject DML/DDL/multiple statements/malformed ambiguity, zero rows fails, row values discarded, TLS verification/CA/hostname, socket, timeout, and fake driver/database without live credentials.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run mocked driver tests; optionally run `vshm check <ID>` against local disposable MySQL read-only user, never production DB. CI uses fake DB; no live dependency.
