# vshm Architecture

vshm is a single-binary host monitoring agent that runs under systemd. It consists of:

- **CLI** (`cmd/vshm/main.go`, `internal/cli`): the only entry point. Commands exactly:
  `validate-config` (schema/values/duplicates/ranges/paths/secret permissions, no network/send),
  `validate-matrix` (non-sending compatibility), `check [ID]` (named or all checks once, no incident
  mutation, no send, nothing persisted), `once` (one full monitoring pass on the worker pool —
  observations recorded through the incident/outbox pipeline, one sanitized line per check plus a
  summary, then exit), `status` (observations/ages, incidents, outbox counts+reasons, storage fault),
  `test-matrix` (the only sending command: explicit encrypted test), `run` (foreground agent).
  `run` wires the scheduler (WP-03), the daily report scheduler (WP-11) and the outbox delivery loop
  (WP-12) on the fail-closed Matrix transport: Matrix validation is never a startup prerequisite —
  delivery fails closed while monitoring and persistence continue. Errors exit nonzero, sanitized.
  See `docs/operations.md` and `docs/wp13-completion.md`.
- **Config** (`internal/config`): TOML configuration with validation and secrets handling.
- **Storage** (`internal/storage`): SQLite-backed persistence with process locking, schema management, and time-based pruning.
- **Scheduler** (`internal/scheduler`): Monotonic cadence with four concurrent workers, non-overlapping checks, and deadline enforcement. Adapters own cleanup of their connections/process groups on context cancellation; if cleanup exceeds the 2 s grace the scheduler reports `ErrCleanupTimeout` and the agent exits nonzero. See `docs/wp03-completion.md`.
- **Check Adapters** (`internal/checks`): Read-only check implementations (systemd, HTTP, ping, MySQL, WebDAV).
- **Incidents** (`internal/incidents`): Per-observation incident state transitions (no thresholds/flap suppression): every failed observation is recorded and enqueues exactly one distinct failure event; the first failure opens the incident, the first later success closes it and enqueues one recovery event; a success without an open incident is silent. Observation + incident transition + event(s) are written in one store transaction. See `docs/wp10-completion.md`.
- **Reports** (`internal/reports`): Daily report generation with DST-aware scheduling.
- **Outbox** (`internal/outbox`): Ordered retry queue with expiry and bounded capacity.
- **Matrix Transport** (`internal/matrixtransport`): Encrypted Matrix event delivery via CLI.
- **Metrics** (`internal/metrics`): Linux system metrics collection for reporting.
- **Logging** (`internal/logging`): Sanitized, bounded journal logging.

All components share the `internal/model` contracts established in WP-00.

## Storage

`storage.Open(ctx, <statedir>/vshm.db, clock, opts)` takes an exclusive `flock` on `<statedir>/vshm.lock` (a second instance gets `ErrAlreadyRunning`), opens SQLite in WAL mode with a single connection, and creates the fixed v1 schema (`meta`, `observations`, `incidents`, `outbox`) with `CREATE TABLE IF NOT EXISTS`. A `schema_version` other than `1` is rejected. The DB and lock files are mode 0600. Every store method runs in one transaction. Observations, the incident row and outbox events are written atomically. Daily reports are deduplicated through the `outbox.dedupe_key` unique column. At most `MaxPending` (default 1000) rows can be pending: when the limit is exceeded, the oldest are marked `dropped` with reason `queue_full`. `Pending` first expires rows older than `PendingMaxAge` (default 24h). `Prune` deletes observations older than 90 days and finished event rows older than 30 days, in batches of 500. Incidents and report rows are kept. The store never probes free disk space. SQLite errors, including disk-full, are wrapped and returned to the caller, which logs them and continues. `vshm status [statedir]` shows the DB size, schema version, observation count and outbox counts per status.

## Deployment

Single C-free binary at `/usr/bin/vshm` (built with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`,
Go 1.27.3, pinned deps). Supervised by `packaging/vshm.service` installed at
`/usr/lib/systemd/system/vshm.service`: non-root `vshm:vshm`, `ExecStart=/usr/bin/vshm run`,
`Restart=on-failure`/`RestartSec=5s`, `UMask=0077`, `StateDirectory=vshm` (0700),
`ReadWritePaths=/var/lib/vshm`, plus `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`,
`ProtectHome`, `ProtectKernelTunables/Modules`, `ProtectControlGroups`, `LockPersonality`,
`RestrictSUIDSGID`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` (AF_UNIX keeps the
read-only system bus reachable; AF_INET/AF_INET6 permit Matrix delivery). Journal is the sole
logging destination. Only systemd supervision restarts the agent; it never remediates monitored
targets. Install/upgrade runbook: `docs/operations.md`. End-to-end acceptance: `tests/e2e_test.go`.
