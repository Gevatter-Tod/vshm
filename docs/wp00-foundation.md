# wp00-foundation — implementation brief

## 1 Purpose
Foundation: exact project skeleton and frozen shared Go model/interfaces, pinned module/build/test conventions, SQLite contract and fakes.

## 2 Dependencies (other WPs) and what can run in parallel
None; agree and freeze contracts before parallel work. Skeleton/build/test scaffold may begin immediately; schema/interface decisions block dependent packages.

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

WP-00 is the owner of these shared contracts; freeze them before dependent WPs implement.

## 4 Files to create/touch (exact paths in the required tree)

### Concrete WP-00 domain model contract
Define types under `internal/model` (field names may be changed only before downstream freeze):
```go
type CheckResult struct { CheckID string; CheckType string; Success bool; StartedAt time.Time; CompletedAt time.Time; Duration time.Duration; ReasonCode string; Reason string }
type CheckConfig interface { CheckID() string; CheckType() string }
type Check interface { Type() string; Run(context.Context, CheckConfig) CheckResult }
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
All methods use `context.Context` where IO/cancellation is possible. `Check.Run` must get a deadline-bearing context created once by scheduler. Models include observation UTC times/duration, incident state/mutation, immutable event ID/sequence/enqueue time, local report date key, delivery state, and sanitized reason. Never serialize credentials, SQL result values, HTTP bodies or crypto state. The above APIs and associated method semantics are concrete proposals since the specification does not prescribe Go signatures: **proposed — confirm with user before freezing**.

### SQLite schema and migrations (concrete proposed CREATE TABLE statements)
Schema details were not fully prescribed by the source; all table names/columns below are **proposed — confirm with user**. Must support WAL, versioning, atomic writes and retention described in WP-02.
```sql
CREATE TABLE schema_version (singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL, applied_at_utc TEXT NOT NULL);
CREATE TABLE check_state (host_name TEXT NOT NULL, check_id TEXT NOT NULL, check_type TEXT NOT NULL, last_status TEXT NOT NULL CHECK(last_status IN ('success','failure')), last_started_at_utc TEXT NOT NULL, last_completed_at_utc TEXT NOT NULL, last_duration_ns INTEGER NOT NULL, last_reason_code TEXT NOT NULL, last_reason TEXT NOT NULL, PRIMARY KEY(host_name,check_id));
CREATE TABLE incidents (incident_id TEXT PRIMARY KEY, host_name TEXT NOT NULL, check_id TEXT NOT NULL, opened_at_utc TEXT NOT NULL, last_failure_at_utc TEXT NOT NULL, last_reason_code TEXT NOT NULL, last_reason TEXT NOT NULL, resolved_at_utc TEXT);
CREATE TABLE results_history (result_seq INTEGER PRIMARY KEY AUTOINCREMENT, host_name TEXT NOT NULL, check_id TEXT NOT NULL, check_type TEXT NOT NULL, success INTEGER NOT NULL CHECK(success IN (0,1)), started_at_utc TEXT NOT NULL, completed_at_utc TEXT NOT NULL, duration_ns INTEGER NOT NULL, reason_code TEXT NOT NULL, reason TEXT NOT NULL);
CREATE INDEX incidents_check_idx ON incidents(host_name,check_id,opened_at_utc);
CREATE UNIQUE INDEX incidents_one_open_per_check_idx ON incidents(host_name,check_id) WHERE resolved_at_utc IS NULL;
CREATE INDEX results_history_completed_idx ON results_history(completed_at_utc);
CREATE TABLE outbox (enqueue_seq INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE, host_name TEXT NOT NULL, event_kind TEXT NOT NULL, check_id TEXT, check_type TEXT, incident_id TEXT, local_report_date TEXT, enqueued_at_utc TEXT NOT NULL, next_attempt_at_utc TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0, delivery_state TEXT NOT NULL CHECK(delivery_state IN ('pending','delivered','expired','evicted','ambiguous')), last_outcome TEXT NOT NULL DEFAULT '', delivered_at_utc TEXT, expires_at_utc TEXT, payload_text TEXT NOT NULL);
CREATE INDEX outbox_pending_order_idx ON outbox(delivery_state,next_attempt_at_utc,enqueue_seq);
CREATE TABLE report_dates (local_date TEXT PRIMARY KEY, event_id TEXT NOT NULL UNIQUE, created_at_utc TEXT NOT NULL);
CREATE TABLE discard_counters (reason TEXT PRIMARY KEY, discarded_count INTEGER NOT NULL CHECK(discarded_count>=0), updated_at_utc TEXT NOT NULL);
```
Migrations are ordered, transactional where SQLite allows, update `schema_version`, preserve existing state; no implicit reset. `payload_text` is preformatted sanitized text only. Review whether expiry timestamp should be derived from original enqueue timestamp/retention policy. Schema is deliberately a proposal, not a claim it appears in spec.

### Reason-code vocabulary (proposal — confirm)
Suggested bounded enum: `systemd_inactive`, `systemd_unavailable`, `http_network`, `http_tls`, `http_status`, `http_assertion`, `http_body_too_large`, `ping_unavailable`, `ping_no_reply`, `mysql_connect`, `mysql_tls`, `mysql_query`, `mysql_no_rows`, `webdav_http_status`, `webdav_xml`, `webdav_propstat`, `timeout`, `internal_error`; discard reasons `age_expiry`, `low_space_eviction`; transport outcomes `confirmed_success`, `confirmed_failure`, `ambiguous`, `compatibility_blocked`. Strings/text must remain sanitized. These exact names are **proposed — confirm with user**.

### Fakes
Provide deterministic fake clock/timers; in-memory Store obeying atomicity, sequence, retention/status basics; fake Transport with confirmed failure/success/ambiguous and captured immutable events; fake Check/Registry. Tests must not need external services or credentials.

### Adapter template
A future adapter adds a typed immutable check config and strict TOML validation; registers one stable `type` in `Registry`; implements `Check.Type()` and `Check.Run(context.Context, CheckConfig) CheckResult`; obeys one absolute deadline, uses read-only behavior, returns exactly one bounded normalized observation, sanitizes errors, and never touches Store/incident/outbox. Add tests for success, each failure/reason, timeout/cancellation, edge cases, output/secret redaction; wire package path under `internal/checks/<type>/` and add config example without secrets. Ask user about spec gaps before implementation.

Create/touch the exact required tree paths: `go.mod`, `go.sum`, `Makefile`, `README.md`, `config/example.toml`, `packaging/vshm.service`, `cmd/vshm/main.go`; directories `internal/cli/`, `internal/config/`, `internal/model/`, `internal/scheduler/`, `internal/checks/systemd/`, `internal/checks/http/`, `internal/checks/ping/`, `internal/checks/mysql/`, `internal/checks/webdav/`, `internal/metrics/`, `internal/incidents/`, `internal/reports/`, `internal/outbox/`, `internal/matrixtransport/`, `internal/storage/`, `internal/logging/`; `tests/fixtures/`; and `docs/architecture.md`, `docs/matrix-cli-validation.md`, `docs/operations.md`.

## 5 Requirements (self-contained, from spec)
Establish Go 1.27.4, exact dependency pins and Linux amd64 CGO-free build. Implement shared domain/signature contract below. Storage is versioned SQLite WAL, bounded timeout/transactions, ordered sequence and migration mechanism. Schema details are not dictated by spec: the concrete schema below is **proposed — confirm with user** and must support all specified atomicity/retention/status/pruning. Do not regard guessed schema as authoritative.

## 6 Design decisions already fixed
Use standard-library SQL interfaces; modernc pure-Go driver. `go test ./...`, `go vet ./...`; exact Makefile targets and CI provider are not named by spec, so suggested `test`/`build` targets are **proposed — confirm with user**. CI checks dependency graph and release target. Adapter packages must not change interfaces/schema; requests go to user.

## 7 Open questions — "ASK THE USER before implementing"
The SQL schema, schema version/migration design, event reason vocabulary, key/sequence policy, index layout, and exact Makefile target names/CI provider are unspecified: **ASK THE USER before implementing**. Suggested enum strings below are proposed, not definitive. Ask whether `Store` is expected to expose transaction-level operation methods or `database/sql`-like primitives; proposed application-facing atomic methods below avoid cross-package transaction leakage. Confirm retention timestamp basis.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
CI acceptance: Go 1.27.4 build/test and Linux amd64 artifact, CGO_ENABLED=0 C-free check, exact TOML/MySQL/SQLite versions, example config parse; temporary SQLite, fake clock/adapters/transport. Verify atomicity, ordered sequence, migrations, retention and failure semantics in dependent integration tests.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run `go version`, `make test`, `make build`, inspect `file`/`ldd` or equivalent for C-free artifact, stage `config/example.toml` at the documented `/etc/vshm/vshm.toml` location and run `./vshm validate-config`; do not assume an undocumented config flag. Local CI only, no live services.
