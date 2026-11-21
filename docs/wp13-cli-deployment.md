# wp13-cli-deployment — implementation brief

## 1 Purpose
Wire CLI/application, harden systemd packaging, operations/install/upgrade docs, end-to-end acceptance.

## 2 Dependencies (other WPs) and what can run in parallel
WP-00 through WP-12. Integration last; packaging/docs can start against agreed contracts.

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
`cmd/vshm/main.go`, `internal/cli/`, `packaging/vshm.service`, `README.md`, `docs/operations.md`, `docs/architecture.md`, `docs/matrix-cli-validation.md`, `Makefile`, `tests/`.

## 5 Requirements (self-contained, from spec)
Commands exactly: `vshm validate-config` (schema/values/duplicate/check definitions, no network/send), `vshm validate-matrix` (non-sending compatibility), `vshm check [ID]` (named or all configured checks, no incident state mutation/send), `vshm once` (every configured check exactly once on the worker pool with `run`'s deadlines/timeout handling, observations recorded through the normal incident/outbox pipeline, one sanitized result line per check plus a summary, then exit — a single full monitoring pass), `vshm status` (last observations/ages, incidents, pending/delivery/discard counts+reasons, storage fault, next check/report), `vshm test-matrix` (only explicit encrypted test send), `vshm run` (foreground diagnosis). Errors nonzero and sanitized. **Resolved open question:** the user confirmed `once` is added to the spec command list (`docs/vshm-spec.md` §6), matching the common DoD's `vshm once`/`run` runtime-validation wording; the earlier spec/DoD inconsistency is closed.

Package `/usr/bin/vshm`; unit `/usr/lib/systemd/system/vshm.service` (the package path required by the spec): User=vshm, Group=vshm, ExecStart=/usr/bin/vshm run, Restart=on-failure, RestartSec=5s, UMask=0077, StateDirectory=vshm, StateDirectoryMode=0700, ReadWritePaths=/var/lib/vshm. Add `NoNewPrivileges=true`, `PrivateTmp=true`, `ProtectSystem=strict`, `ProtectHome=true`, `ProtectKernelTunables=true`, `ProtectKernelModules=true`, `ProtectControlGroups=true`, `LockPersonality=true`, `RestrictSUIDSGID=true`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`; permit read-only systemd system-bus access and network. Dedicated non-root account, no capabilities, no target remediation. Journal sole logging destination, sanitized/bounded. Config/secrets/state path ownership and modes as common security. Setup: create account/directories/files, `/etc/vshm/secrets` root:vshm 0750, install release/unit, run validate-config and validate-matrix then enable/start; Matrix validation failure blocks sending not monitoring. v1 Linux amd64/systemd only. Upgrade: sensitive host-specific backup `/var/lib/vshm`, stop, install, explicit migrations/validation, start/status; never copy crypto identity. Config changes need restart; restart preserves incident state, report keys, outbox.

## 6 Design decisions already fixed
Do not imply target runtime installation. Only systemd supervision restarts vshm. Read-only bus access and networking must coexist with hardening. Keep exact tree and no extra commands that change monitoring targets.

## 7 Open questions — "ASK THE USER before implementing"
Ask user to confirm service-account/group setup, systemd version support for all sandbox directives and read-only D-Bus permission syntax; service should fail safely if directives unsupported rather than drop security silently. ~~Confirm `vshm once` inconsistency in common DoD~~ **Answered:** user confirmed `vshm once` is part of the spec command list (added to `docs/vshm-spec.md` §6); no inconsistency remains. Matrix target gate question belongs to WP-12 and must be answered before enabling sending.

## 8 Acceptance tests (from spec §8 where applicable, plus package-specific)
§8 end-to-end CI uses mocked clock, fake checks, temporary SQLite and fake Matrix transport; no live credentials/endpoints/database/ping target/Matrix account. Explicit release-gate checklist: (1) Go 1.27.3 build/test, Linux amd64 release, CGO_ENABLED=0 C-free `/usr/bin/vshm`, example config and invalid/unknown/duplicate/range/path/secret-permission failures, exact dependency pins; (2) global/per-check interval/timeout, prompt startup, monotonic cadence, missed tick, four workers, non-overlap, one absolute deadline covering all work, one timeout result, cleanup and 2s grace, nonzero on cleanup failure/no leaked worker/child, downtime no observations; (3) each adapter success/failure, TLS, redirects, body cap, malformed WebDAV, single-SELECT validation/redaction, metrics formulas/units/unavailable; (4) every failed observation has one distinct message including repeated failures, no grouping/confirmation/warnings, first recovery only, silent success without incident, persistence restart; (5) atomic observation/state/outbox, ordered durable retries/backoff 1s–1h while pending/eligible, distinct failure/recovery, 24h default expiry, oldest-first eviction, discarded never counted delivered, ambiguous duplicate possibility, 30d delivered/90d observations, low/full disk pruning/WAL checkpoint/recheck/single retry, storage faults, corruption/permission do not prune; (6) report fields, NO RESULT/STALE/unavailable, local numeric offsets, DST gap/fold, one per date, restart dedup/no catch-up; (7) Matrix compatibility/missing account-room-state/unverified E2EE blocks sends while monitoring/persistence continue, explicit test only test send, no plaintext, second agent refused, no secrets in logs/output; (8) systemd non-root and read-only target behavior; only vshm process supervision restarts. Matrix target release gate additionally inspects actual installed version's own help/docs, records exact version/interface and encryption verification in `docs/matrix-cli-validation.md`, runs non-sending validation and approved explicit encrypted integration test; if encryption and explicit success cannot be verified, no send, eligible events remain pending, monitoring continues.

## 9 Definition of done (reference the common DoD in overview.md plus package-specific runtime validation steps)
Complete all eight items in the common Definition of Done in `overview.md`, including evidence and completion report. Package runtime validation: Run built binary `vshm validate-config`, `vshm validate-matrix` (expected clear blocked result if no validated host profile), `vshm check [ID]` fake/local, `vshm once` against a fake/local target (single pass, one sanitized line per check plus summary), `vshm status`, foreground `vshm run` with local fake target; inspect `systemd-analyze verify` where available and validate unit security. Real `test-matrix` only after approval.
