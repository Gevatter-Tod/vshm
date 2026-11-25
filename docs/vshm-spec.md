# vshm — Very Simple Host Monitor

**Status:** authoritative v1 implementation specification. v1 is Linux amd64 with systemd only; OpenWrt and additional architectures/platforms are deferred to v2. This document does not claim that an uninspected `matrix-commander-rs` installation supports a particular command-line interface.

## 1. Product contract

Implement **vshm** in Go as one independent, long-running Linux agent per server, supervised by systemd. Version 1 supports Linux on amd64 only and systemd only. OpenWrt and all additional architectures or init systems are deferred to v2; v1 does not claim compatibility with them. Formal acceptance against a host resource budget is deferred to v2; v1 defines no resource-budget acceptance threshold. Package one statically linked, CGO-free executable at `/usr/bin/vshm`; Go is a build-time toolchain, not a runtime installation dependency. There is no central coordinator, web UI, or remote management API. The agent observes configured checks, persists their results and incident state locally, and sends event messages and one daily report to a configured Matrix room using the server's existing `matrix-commander-rs` installation and account. Each server has its own Matrix account/device and private crypto state; servers may share one configured room. End-to-end encryption (E2EE) is mandatory. Never send plaintext or silently substitute another transport.

The configured identity is `host_name`, never `host_id`. Require an explicit stable `host_name` in configuration; do not infer it from a system hostname. Use it in messages, incident keys, and logs. There is no timezone setting: all scheduling and displayed timestamps use the server's local timezone. No check or report may restart, repair, or otherwise change a monitored target.

Initial checks: systemd service-running, HTTP(S), ping, MySQL read query, and read-only WebDAV listing. Resource statistics are included in daily reports and do not alert. Default check interval is 30 minutes; global interval and per-check overrides are configurable. Daily report time is a fixed, configurable local `HH:MM`; default `09:00`.

### Notification contract

Each failed check observation is an independent failure result and creates exactly one distinct failure event/message; do not group failures across checks or observations. Alert on the first failure immediately after that observation completes—do not run a confirmation check or delay the alert. Every subsequent scheduled observation that fails also gets its own message; there are no separately scheduled warning/reminder messages. The first successful observation after one or more failures closes the incident and creates one recovery message. A successful observation with no open incident creates no event. This rule preserves the failure-result-per-message requirement while distinguishing actual failed observations from periodic warnings. A notification delivery retry is a transport retry, not a new check, failure result, or warning.

## 2. Runtime, configuration, state, and scheduling

- Runtime and build: Go **1.27.3**. Build and test as a Go module; release artifacts target only `GOOS=linux GOARCH=amd64` with `CGO_ENABLED=0`, producing a C-free executable at `/usr/bin/vshm`. TOML is the sole configuration format, parsed with `github.com/BurntSushi/toml v1.6.0`; MySQL uses `github.com/go-sql-driver/mysql v1.10.1`; SQLite uses pure-Go `modernc.org/sqlite v1.61.1`. Pin these exact module versions in `go.mod` and `go.sum`; CI verifies the locked dependency graph. Standard library packages provide CLI, HTTP(S), XML parsing, scheduling, logging and SQL interfaces. No Python, CGO, web server, runtime package installation, or extra transport is permitted.
- Configuration path `/etc/vshm/vshm.toml`; state/database, lock, outbox and per-host Matrix crypto state under `/var/lib/vshm/`. State directory mode `0700`, owned by `vshm:vshm`; SQLite and crypto files mode `0600`. Configuration is `root:vshm`, mode `0640`. MySQL credential files are separate from TOML, owned by `root:vshm`, mode `0640`; reject group/other-writable files and reject secret files readable by unauthorized users. Never put secrets in command arguments, logs, reports, source control, or sample config.
- Configuration is validated at startup and by `vshm validate-config`. Unknown keys, duplicate check IDs, malformed values, missing required monitoring files, insecure credential permissions, and invalid ranges are errors. Matrix account/session/crypto availability and compatibility are not monitoring-configuration requirements and are handled separately. Invalid monitoring configuration prevents agent startup before monitoring begins; it does not alter persisted monitoring state. Matrix compatibility is validated separately and is not startup-critical. Configuration changes take effect only after an explicit systemd restart; restart does not reset incidents, schedules' report-date keys, or outbox. Invalid config makes the new process fail clearly without altering persisted monitoring state.
- Check interval and timeout accept positive duration strings (for example `30m`, `10s`). Allowed interval range is 1 minute–7 days; timeout range is 1–300 seconds and must be less than that check's interval. Global interval default `30m`; global timeout default `10s`; each check may override either. A fixed worker pool of four runs distinct checks concurrently; no check overlaps itself. Each check runs once promptly after startup, then on monotonic cadence. No missed check ticks are replayed; if a prior run is still active at its next due tick, skip that tick and log it, without fabricating a failure. Each check gets exactly one absolute deadline when its worker execution begins. That deadline covers all work for the check, including DNS, connection setup, network I/O, complete query/response processing, and any subprocess work; adapters must propagate the same deadline/context rather than resetting per-operation timeouts. On deadline, produce exactly one failed observation for that execution. Close connections owned by the worker and terminate its owned subprocess process group; allow at most 2 additional seconds solely for cleanup, not for monitoring work. If cleanup has not completed after that grace period, mark the agent unhealthy and exit nonzero so systemd restarts it; do not leave a worker or child process running. Persist completion instants in UTC and render local timestamps with numeric UTC offset.
- A process lock permits one agent per host. A second `run` exits with a clear error. Store an observation only when one actually ran; agent downtime does not fabricate failure or recovery.

### Daily report scheduling

Send at most one report for each local calendar date, at configured local `HH:MM` (default `09:00`). If the time is in a spring-forward DST gap, send once at the first valid local instant after the gap; in a fall-back fold, send once at the first occurrence. At the scheduled report time, insert the unique local-date report key and its outbox event in one SQLite transaction, inserting the key before the event within that transaction. Commit both or neither. At startup, schedule only the next future report time; omit reports missed while stopped and never catch up. This rule and the date key prevent duplicate reports on restart, clock adjustment, or DST transition. User-confirmed exception for the first report only: at agent startup, once the prompt first pass of all checks has completed, the report for the current local date is rendered, enqueued and sent immediately if it has not been sent yet that date; the date key still prevents duplicates, and only reports after this first one follow next-future-only scheduling.

## 3. Configuration schema and complete example

The TOML keys below are the schema. `host_name` is required. `[agent]` is required; `[metrics]` and `[storage]` may be omitted to use defaults. Specify `[storage]` when overriding either storage policy. `storage.pending_max_age` is a positive duration (default `24h`); `storage.min_available_bytes` is a positive integer (default `104857600`, 100 MiB) measured on the filesystem containing `/var/lib/vshm`. (v1 implementation status: both keys are parsed and validated, but the storage layer never probes free disk space, so `min_available_bytes` is not yet enforced — see the implementation-status note in §5.) IDs are stable per-host identifiers matching `[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}` and unique within the file. The `checks` array contains enabled checks; an empty array is valid. All values shown are literal example values, not credentials. HTTP and WebDAV are unauthenticated unless an optional `credential_file` is configured; that file is INI `[basic]` with `username` and `password`, used only for HTTP Basic authentication over verified HTTPS. Never send HTTP Basic credentials over plain HTTP. For MySQL, `credential_file` is INI `[client]` with `user`, `password`, and optional `database`; values are read privately and never logged; a `database` value in the check configuration takes precedence over the optional credential-file database. Credential files are service-readable only, root-owned, and mode `0640` as defined above.

```toml
[agent]
host_name = "web-01"
interval = "30m"
timeout = "10s"
report_time = "09:00"
room_id = "!ops:example.org"

[metrics]
mounts = ["/"]

[storage]
pending_max_age = "24h"
min_available_bytes = 104857600

[[checks]]
check_id = "nginx"
type = "systemd"
target = "nginx.service"

[[checks]]
check_id = "site-health"
type = "http"
url = "https://example.org/health"
expected_status = 200
contains = "ok"

[[checks]]
check_id = "gateway-ping"
type = "ping"
host = "192.0.2.1"

[[checks]]
check_id = "db-readiness"
type = "mysql"
host = "db.example.org"
port = 3306
database = "app"
credential_file = "/etc/vshm/secrets/db-readiness.ini"
tls_ca = "/etc/ssl/certs/ca-certificates.crt"
query = "SELECT 1"

[[checks]]
check_id = "dav-list"
type = "webdav"
url = "https://dav.example.org/shared/"
depth = 1
```

`interval` and `timeout` may be set on each check; absent values inherit the agent defaults. HTTP `contains` is optional. MySQL `port` defaults to `3306`; for MySQL checks `credential_file` and a single read-only `query` are **required**, and TLS CA is required for TCP connections (a local Unix `socket` is the TLS-free alternative, mutually exclusive with `host`/`port`/`tls_ca`). WebDAV depth is required and accepts only `0` or `1`. `room_id` is the configured shared room identifier. The Matrix account/session is supplied by the existing `matrix-commander-rs` installation and its per-host state; vshm does not provision accounts or copy crypto identity between servers. No timezone key exists. Storage policy keys are `pending_max_age` and `min_available_bytes` as defined above; they may be omitted for the defaults.

## 4. Check and metric semantics

All check adapters return one normalized observation: success/failure, start/completion UTC time, duration, and short sanitized reason code/text. The configured check ID and non-secret type identify it. Never include unbounded output, HTTP body, SQL text/result values, credentials, or secret-bearing paths in messages or logs.

1. **systemd:** Read the configured unit's `ActiveState` through a stable systemd interface (prefer the system D-Bus API; a safely invoked `systemctl show` property query is the fallback). Success means exactly `active`. Never call start, stop, restart, enable, or disable. If systemd cannot be queried, the observation fails with a sanitized error. (v1 implementation: `systemctl is-active <unit>` only — read-only, bounded subprocess, no D-Bus dependency; decision recorded in `wp04-completion.md`.)
2. **HTTP(S):** GET the configured URL. TLS certificate and hostname verification are mandatory. Redirects are disabled. Success requires the exact `expected_status` (default `200`) and, when configured, the literal `contains` substring in a bounded response body. Cap body reads at 1 MiB; a response exceeding the cap fails. Do not expose response body in result/message. Network, TLS, status, assertion, and timeout failures are failures.
3. **Ping:** use the system `ping` executable with one probe, within the check's absolute deadline. Success requires a valid reply. The agent and ping executable run without capabilities; ping is unprivileged only. Do not grant `CAP_NET_RAW` to vshm or to ping as a fallback. If the executable is absent, an unprivileged ping socket is unavailable, or the probe is otherwise not permitted, return a failed observation; never silently skip the check or weaken the systemd sandbox.
4. **MySQL:** use the pinned Go MySQL driver over verified TLS for TCP, with the configured CA and hostname verification; local Unix socket connections are permitted without network TLS. Connect and execute under the same absolute check deadline. Accept exactly one read-only `SELECT` statement (one optional trailing semicolon); reject other leading statements or multiple statements. Database grants on a dedicated read-only account are the security boundary and are mandatory regardless of parser checks. Success requires execution and retrieval of at least one row; discard values immediately. No SQL values or result summaries enter logs, messages, or reports. Database, TLS, query, and timeout errors fail the check.
5. **WebDAV listing:** send only read-only `PROPFIND` with configured depth `0` or `1`; never use unbounded depth. Require HTTP `207`, well-formed XML with a DAV `multistatus` root, and at least one `response` containing `href`. A response is successful only if at least one `propstat` has a 2xx status; if any response has no successful `propstat`, fail the check. Malformed XML, HTML error pages, invalid status, or timeout fail. Do not issue PUT, DELETE, MOVE, or other modifying requests. Redirects are followed (user-confirmed WP-08 decision; 307/308 preserve the PROPFIND method, and the Go client strips `Authorization` on cross-host redirects).

**Resource metrics, report-only:** sample Linux load average over 1 minute; RAM used and total from `/proc/meminfo` (`MemTotal` and `MemAvailable`, with used = total − available); used and total bytes for each configured mount using `statvfs`; and uptime from `/proc/uptime`. Report values with explicit units. If a source or mount is unavailable, report `UNAVAILABLE` and a reason, never zero or a guessed value. Snapshot metrics when each daily report is rendered; metrics never open incidents or send failure alerts.

## 5. Incidents, reports, and durable Matrix delivery

### Incidents and messages

Key incident state by exact configured `host_name` plus stable check ID. Each observed failed result is transactionally recorded and enqueues its own uniquely identified failure event, regardless of whether that incident is already open. First failure opens the incident. Further failures update last-failure time/reason but do not create a new incident; they do create their required individual failure events. The first later success closes the open incident and transactionally enqueues one recovery event. Success without an open incident is recorded without notification. Restart preserves state; process downtime alone never opens or closes an incident.

Messages are plain-text (non-Markdown) and identify host, check ID/type, event kind, local timestamp with UTC offset, sanitized reason/status, and incident start time for recovery. Never group events from multiple checks or observations into one message. Report events are distinct daily report messages. If the configured account is not joined to the room or room access/encryption is unavailable, delivery fails closed; eligible events remain pending subject to configured expiry and low-space eviction.

### Daily report content

Include each configured check's latest status, last observation time and age, short sanitized outcome/reason, and each resource metric. Use `NO RESULT` before a check has run. Mark a result `STALE` if age exceeds that check's interval plus timeout, while retaining its last observation time. State the host-local report timestamp with numeric UTC offset. A missing metric is `UNAVAILABLE`, not zero.

### SQLite, outbox, and bounded storage behavior

Use a versioned SQLite schema with migrations. Enable WAL mode, configure a bounded busy timeout, use bounded transactions and short-lived consistent-read snapshots, and checkpoint during pruning as specified below. In one transaction, persist each observation, incident transition, and corresponding event enqueue; use a deterministic increasing sequence for ordering. Reports are rendered from a consistent snapshot and enqueued transactionally with their unique local-date key. A report-date key remains persisted even if low-space eviction later discards that report event; the daily report is not regenerated for that local date. Outbox events are not grouped or coalesced. A single sender owns the Matrix CLI and crypto store under an exclusive lock; it sends pending events in enqueue order. On delivery failure, retry with exponential backoff starting at 1 second, capped at 1 hour, subject to the expiry and eviction rules below. Retries do not promise unconditional eventual delivery. Retain delivered outbox records for 30 days, observations for 90 days, current incident state and daily-report date keys indefinitely.

Before attempting delivery, expire every pending event whose age, measured from its original enqueue timestamp, is greater than `pending_max_age` (default 24 hours). Expose discard reason and count in local status and sanitized logs; maintain durable aggregate discard counters by reason and update them in the same SQLite transaction that removes the affected pending row(s). If that transaction cannot commit, report a storage fault in bounded process/journal status and do not claim either durable discard accounting or successful pruning. Discarded events are not delivered. On startup and immediately before each storage write transaction, check available bytes accessible to the vshm process on the filesystem containing the state database. If available space is below `min_available_bytes` (default 100 MiB), discard pending events oldest-first until the threshold is reached or no pending events remain; this preserves newer pending information preferentially. Expiry is applied first. Incident state, observations, and report-date keys are independent of pending-event eviction and are not deleted by this policy. The durable aggregate counters distinguish age expiry from low-space eviction; a discarded event is never described as delivered. Delivered records and ordinary retention follow their stated retention periods.

SQLite logical deletion does not guarantee immediate filesystem space recovery. Deleted rows make database pages reusable for later SQLite writes, but do not necessarily shrink the database file or return bytes to the filesystem; do not run `VACUUM` as an emergency recovery action. Use SQLite WAL mode with bounded transactions and pruning batches, close read snapshots promptly, and checkpoint after each committed pruning batch; attempt a WAL truncate checkpoint when safe. WAL contents, open SQLite readers, filesystem allocation, and unrelated state files can prevent immediate reclamation. Recheck filesystem availability after checkpointing and stop pruning when the threshold is met or no pending items remain. If the delete-and-counter transaction or checkpoint cannot complete because the disk is full/busy, stop pruning and report the storage fault; never claim rows are discarded unless the transaction committed. Reuse available database pages on subsequent writes; there is no promise that low-space pruning itself returns filesystem bytes. Do not promise that deleting pending rows always frees space; do not delete incident state or unrelated files as a space-recovery shortcut. Pruning is performed in enqueue order, one bounded row/batch at a time, with each row considered at most once per trigger; it is not an unbounded retry loop. If a pruning transaction itself cannot commit, stop pruning and surface the storage fault.

If an observation/report/outbox write fails with a storage-capacity error (including SQLite full, filesystem `ENOSPC`, or quota exhaustion), expire outdated events and force a pruning pass: discard the oldest pending event first if any exist, then continue oldest-first while the filesystem remains below `min_available_bytes` or until no pending events remain. Checkpoint as above, then retry that failed transaction exactly once. If no pending events exist, still perform the checkpoint/recheck before the single retry. Regardless of whether pruning or checkpointing succeeds, make exactly one retry of the original failed transaction after the bounded pruning attempt. If pruning/checkpointing fails, or the single retry fails, expose a storage fault in status and sanitized logs; never claim the observation, report, incident transition, or event was saved unless its transaction committed. Do not invoke pruning for corruption, malformed database, permission, or other non-capacity errors; surface those as storage faults. On any failure, preserve the prior committed state and do not fabricate a successful save. A disk-space reading failure is itself a visible storage fault; it does not authorize destructive cleanup.

**Implementation status (v1):** Pending-age expiry is implemented: pending rows older than `pending_max_age` are marked `expired` per-row (visible via `vshm status` counts and `last_reason`), and the bounded queue drops oldest pending rows with reason `queue_full`; these per-row statuses are counted in status but there are no separate durable aggregate discard counters. The following parts of the two paragraphs above are **not implemented in v1**: the free-disk-space probe before write transactions, oldest-first low-space eviction below `min_available_bytes`, WAL checkpoint/recheck on capacity errors, and the exactly-one retry of a failed storage write. In v1 the store never probes free disk space; `min_available_bytes` is accepted by configuration but unused; SQLite errors including disk-full/`ENOSPC` are wrapped and surfaced to the caller as storage faults (visible in status and journal); the caller logs and continues without pruning or retry. The per-date report dedupe in v1 is the `outbox.dedupe_key` unique column (no separate `report_dates` table).

Mark an event delivered only after explicit successful completion under the validated CLI interface. A timeout or interrupted process after possible Matrix acceptance is ambiguous: retain it for retry unless it expires or is evicted under the rules above, and expose the ambiguous outcome in status/logs. A duplicate message may result. Delivery is **best effort with durable retries while an event remains pending and eligible**, not exactly-once and not guaranteed eventual delivery. A transport retry reuses the same event ID in internal state, but no Matrix-side idempotency is assumed. Status distinguishes pending, delivered, expired, evicted, and ambiguous outcomes.

### Matrix compatibility and fail-closed contract

Use only the existing `matrix-commander-rs` executable, installed and maintained outside vshm. Do not assume a binary name, version, flags, encryption defaults, state path, or exit-code meaning. Implement an isolated adapter against the actual installed release only after recording its version and inspecting that release's own documentation and help output. `vshm validate-matrix` performs non-sending compatibility checks: executable is present, installed version matches the implemented adapter profile, account/room/state configuration is available, required encrypted-send capability and explicit success reporting are supported, and crypto state is writable only by `vshm`. Unknown version, unsupported interface, absent E2EE capability, or unverified encryption is a compatibility failure: queued events remain pending and no send is attempted. Such Matrix compatibility, account, room, or encryption failures block delivery only. Even when delivery is blocked, the agent continues monitoring, local persistence, incident processing, and report generation; Matrix availability or validation is not a monitoring-startup prerequisite. Status and logs report sanitized failure categories only, never plaintext, credentials, crypto material, or secret-bearing diagnostics.

`vshm test-matrix` is an explicit operator-requested integration test using the verified encrypted path and configured room; it is the only test command that sends a message. Before enabling delivery on a target host, validate documented version/help commands supported by that installed binary, run `vshm validate-matrix`, and perform the explicit encrypted test in a disposable or approved room. The adapter must verify encrypted delivery using a supported explicit CLI result or an independently documented encrypted-event verification method. If this cannot be verified, fail closed for sending while monitoring continues. Do not invent CLI flags or claim compatibility until this target-specific validation is recorded in `docs/matrix-cli-validation.md`. All subprocess calls use argument vectors, never shell interpolation; timeouts and diagnostics are bounded and sanitized. Each host's crypto state is private and host-specific. vshm never falls back to plaintext or another transport.

## 6. CLI, deployment, security, and maintenance

Commands: `vshm validate-config` checks schema, values, duplicate IDs and check definitions without network activity or sending; `vshm validate-matrix` performs the non-sending checks above; `vshm check [ID]` runs the named check, or all configured checks when ID is omitted, without changing incident state or sending; `vshm once` runs every configured check exactly once on the agent's worker pool with the same per-check deadlines and timeout handling as `run`, records the observations through the normal incident/outbox pipeline (so incident state and outbox events persist), prints one sanitized result line per check plus a summary count, and then exits — a single full monitoring pass for validation and diagnosis, unlike `vshm check [ID]` which does not change incident state or send; when the pass is done, pending events are delivered immediately (fail-closed if the Matrix transport is not validated); `vshm status` shows last observations and ages, open incidents, pending/delivery/discard counts and reasons, any storage fault, next check and next report; `vshm test-matrix` sends only the explicit encrypted test; `vshm run` runs the agent in foreground for diagnosis: all checks run promptly at startup, failure/recovery events are sent as soon as they are recorded, and the report for the current local date is sent immediately once the startup pass completes (the per-date key still prevents duplicates); afterwards checks and reports follow the configured schedule. Errors have nonzero exit codes and sanitized terminal output.

Run as dedicated non-root `vshm:vshm` account. Package paths:

```text
/etc/vshm/vshm.toml                  # root:vshm 0640
/etc/vshm/secrets/*.ini              # root:vshm 0640, not group/other writable
/var/lib/vshm/                       # vshm:vshm 0700; SQLite, lock, Matrix crypto state
/usr/lib/systemd/system/vshm.service # packaged unit
```

The unit uses `User=vshm`, `Group=vshm`, `ExecStart=/usr/bin/vshm run`, `Restart=on-failure`, `RestartSec=5s`, `UMask=0077`, `StateDirectory=vshm`, `StateDirectoryMode=0700`, and `ReadWritePaths=/var/lib/vshm`. Apply `NoNewPrivileges=true`, `PrivateTmp=true`, `ProtectSystem=strict`, `ProtectHome=true`, `ProtectKernelTunables=true`, `ProtectKernelModules=true`, `ProtectControlGroups=true`, `LockPersonality=true`, `RestrictSUIDSGID=true`, and `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`; permit read-only systemd system-bus access and network connections. Do not run as root or grant vshm or ping `CAP_NET_RAW`; no capability fallback is permitted. Journal is the sole logging destination; logs are bounded/sanitized and rely on host journald rotation. Monitoring-target service state is never changed.

Installation: create the service account and protected directories/files, install the release executable as `/usr/bin/vshm` and the systemd unit, create `/etc/vshm/secrets/` root:`vshm` mode `0750`, run `vshm validate-config` and `vshm validate-matrix`, then enable/start the service. No language runtime or package installation is needed on the target. Matrix validation failure prevents sending but does not prevent `run` from starting monitoring. Ping must work unprivileged; do not grant capabilities. v1 installation is limited to Linux amd64 with systemd. Upgrade: back up `/var/lib/vshm` as sensitive host-specific data, stop vshm, install the new package, run database migrations/validation, then start and verify status. Never copy Matrix crypto state to another host. Configuration changes require restart; upgrades preserve database state and apply only explicit schema migrations.

## 7. Required project tree

```text
vshm/
  go.mod
  go.sum
  Makefile
  README.md
  config/example.toml
  packaging/vshm.service
  cmd/vshm/main.go
  internal/
    cli/
    config/
    model/
    scheduler/
    checks/
      systemd/
      http/
      ping/
      mysql/
      webdav/
    metrics/
    incidents/
    reports/
    outbox/
    matrixtransport/
    storage/
    logging/
  tests/
    fixtures/
  docs/
    architecture.md
    matrix-cli-validation.md
    operations.md
```

Component boundaries: Go check adapters accept validated immutable config and one absolute context deadline, return normalized observations, and cannot write incidents/outbox. Scheduler owns monotonic cadence, per-check deadline and cleanup, fixed four-worker concurrency and no-overlap. Storage owns SQLite schema, transactions, migrations, bounded pruning, and process lock. Incident service atomically records observations/state/events. Report service formats local-time reports and atomically enqueues the per-date event. Outbox owns ordered durable retries, pending expiry/eviction, and retention. Matrix adapter submits one preformatted event through the validated encrypted CLI path and returns confirmed success, confirmed failure, or ambiguous outcome. CLI invokes application services and prints sanitized output. Models never serialize secrets or query results.

## 8. Acceptance tests and release gates

CI uses mocked clock, fake check adapters, temporary SQLite and fake Matrix transport; no live credentials, external endpoints, database, ping target, or Matrix account are required. Required tests:

- Go 1.27.3 build/test and Linux amd64 release artifact; verify `/usr/bin/vshm` is C-free with `CGO_ENABLED=0`; example TOML validates and matches schema; bad/unknown keys, duplicate IDs, ranges, paths and secret permissions fail clearly. Verify exact pinned TOML, MySQL, and pure-Go SQLite module versions.
- Default and per-check interval/timeout; prompt startup check; monotonic cadence; missed-tick skip; four-worker bound; no overlap per check; one absolute worker deadline includes all network/query/subprocess work; exact single timeout observation; owned connections/process groups are cleaned within the 2-second cleanup-only grace; cleanup failure exits nonzero and leaves no worker/child; downtime does not fabricate observations.
- Each check's specified success and failure conditions, TLS verification, redirect handling (HTTP rejects redirects; WebDAV follows them per the user-confirmed WP-08 decision — 307/308 preserve the PROPFIND method), body cap, malformed WebDAV XML/status handling, SQL single-SELECT validation and secret/result redaction; resource metric definitions, units and unavailable values.
- Every actual failed observation enqueues exactly one distinct message, including repeated failures; checks are not grouped; no confirmation check or scheduled warning; recovery once on first success after failure; success without open incident does not notify; persistent state survives restart.
- Transactional observation/state/outbox; ordered durable retry and capped backoff while pending and eligible; failure and recovery remain separate events; pending events survive restart subject to 24-hour default expiry (low-space eviction is deferred in v1 — see the §5 implementation-status note); expiry/queue-bound drops/eviction are visible per status and never counted as delivery; ambiguous send can retry and duplicate; 30-/90-day retention; storage faults are surfaced in status/logs (low/full-disk pruning, WAL checkpoint/recheck, and the one storage-write retry are deferred in v1 — see §5).
- Report fields, `NO RESULT`, `STALE`, unavailable metrics, numeric local UTC offsets, DST gap/fold, one report per local date, restart dedup and no catch-up after downtime.
- Matrix compatibility failure, unsupported/unverified encryption, or missing account/room/state blocks delivery and causes no send while monitoring and persistence continue; explicit encrypted test is the only test send; no plaintext path exists; second agent instance is refused; logs/output contain no secrets.
- systemd integration is non-root and cannot remediate monitored targets; only vshm process supervision restarts.

Release gate for Matrix is target-specific: inspect the installed `matrix-commander-rs` version's own help and documentation, implement only a verified adapter profile, record the exact validated version/interface and verification evidence in `docs/matrix-cli-validation.md`, run non-sending validation and an approved explicit encrypted integration test. If E2EE send and verifiable success cannot be established on that installation, sending fails closed and eligible notifications remain pending subject to expiry/eviction; the vshm agent still starts and monitors. This is a delivery compatibility gate, not permission to invent or silently assume CLI behavior.
