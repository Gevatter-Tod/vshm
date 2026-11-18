# wp01-config — implementation brief

## 1 Purpose
TOML schema, strict validation/defaults, secure INI secrets and `validate-config`.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00. Parallel with WP-02 and WPs 04–09 after typed shared config is frozen.

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
`internal/config/`; `internal/model/` only via WP-00 request; `cmd/vshm/main.go` / `internal/cli/` command hook coordinated with WP-13; `config/example.toml`; `tests/fixtures/`.

## 5 Requirements (self-contained, from spec)
`/etc/vshm/vshm.toml`; `[agent]` required and `host_name` required explicit stable identity (no hostname inference), `interval=30m`, `timeout=10s`, `report_time=09:00`, `room_id`. No timezone key. `[metrics]` optional, `mounts=[]` default (**proposed — confirm with user**, since the spec defines configured mounts but not the omitted-key default). `[storage]` optional: `pending_max_age=24h`, `max_pending=1000` positive integer. `[[checks]]` list may be empty; IDs match `[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}`, unique. Per-check `interval`/`timeout` override global. Durations positive; interval 1m–7d; timeout 1–300s and less than effective interval. Unknown keys, malformed values, bad paths, missing required monitoring files, duplicate IDs, invalid ranges fail clearly. Validation has no network/send side effects, matrix readiness is separate and not required to start. Invalid config must not mutate persisted state.

Check schema: systemd `check_id,type,target`; HTTP `check_id,type,url,expected_status` default 200, optional `contains`, optional `credential_file`; ping `check_id,type,host`; MySQL `check_id,type,host,port` default 3306, `database`, `credential_file`, `tls_ca`, `query`; WebDAV `check_id,type,url,depth` required 0/1, optional `credential_file`. HTTP/WebDAV Basic INI `[basic] username,password`, only verified HTTPS; reject credentials over HTTP. MySQL INI `[client] user,password`, optional database; explicit check `database` overrides file database. TCP MySQL requires `tls_ca`; local Unix socket permitted without network TLS. Config secrets never stored in general model serialization/logging. Config root:vshm 0640. Credentials separate, root:vshm 0640, not group/other-writable, readable only by authorized service group; fail closed on ownership/mode/permission issues. Do not include real credentials in example.

## 6 Design decisions already fixed
Use BurntSushi/toml v1.6.0, strict unknown-key detection; validate structure before loading any state/mutating DB. Secret contents held only in non-serializable private runtime config. Preserve exact keys/defaults. Matrix account/session/crypto checks belong to `validate-matrix`, not this command.

## 7 Open questions — "ASK THE USER before implementing"
Ask user to confirm exact secret readability rule implementation (e.g. permitted root and vshm group only; reject other-read/world-read while root:vshm:0640 is allowed), symlink/hardlink treatment and required-file path policy; spec states root:vshm 0640 and unauthorized readability rejection but does not define symlink behavior. Confirm `[metrics].mounts` omitted default; type-specific URL/path validation details if ambiguous.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8: example TOML validates/matches schema; bad/unknown keys, duplicate IDs, ranges, paths and secret permissions fail clearly; exact dependency pins. Test defaults/overrides, empty checks, required `host_name`, no network, HTTP credential on HTTP rejected, MySQL database precedence, secure files. Invalid config leaves persisted monitoring state unchanged.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Build and run stage `config/example.toml` at `/etc/vshm/vshm.toml` in an isolated fixture root/environment and run `vshm validate-config` (no config override flag is specified); run negative fixtures for unknown key, duplicate ID and insecure secret; no external services.
