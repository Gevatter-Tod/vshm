# vshm Architecture

vshm is a single-binary host monitoring agent that runs under systemd. It consists of:

- **CLI** (`cmd/vshm/main.go`): Entry point with subcommands for validation, running, and status.
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
