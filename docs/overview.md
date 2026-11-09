# vshm implementation briefs — overview

## Project summary and fixed stack
vshm is one independent Linux amd64 v1 host-monitor agent per server, run in foreground under systemd. It observes configured checks, records observations/incidents locally in SQLite, and queues plain-text event and daily-report messages for the existing per-host Matrix account via the externally installed `matrix-commander-rs`. There is no coordinator, UI, remote API, self-remediation, or alternate transport. E2EE is mandatory; never send plaintext. OpenWrt, other architectures/init systems, and formal host resource-budget acceptance are deferred to v2. Do not infer `host_name`: it is required, stable, and used in messages, incident keys, and logs. There is no timezone setting; schedules use server-local time and persisted instants are UTC.

Use Go **1.27.3**, module dependencies pinned exactly: `github.com/BurntSushi/toml v1.6.0`, `github.com/go-sql-driver/mysql v1.10.1`, and pure-Go `modernc.org/sqlite v1.61.1`; pin `go.mod` and `go.sum`, and CI verifies the locked graph. Release only Linux amd64 with `CGO_ENABLED=0`, C-free executable `/usr/bin/vshm`. TOML is the only config format. Standard library handles CLI, HTTP(S), XML, scheduling, logging and SQL interfaces. No Python, CGO, web server, target runtime installation, or additional transport.

## Required project tree
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

## Shared engineering constraints
Follow the exact public interfaces and storage schema owned by WP-00. Other packages consume them but do not silently alter them; report a proposed incompatible change separately. Package boundaries: immutable validated check config and one absolute context deadline into check adapters; normalized observations out; adapters cannot write incidents/outbox. Scheduler owns monotonic cadence, fixed four-worker concurrency, non-overlap, deadlines and cleanup. Storage owns schema, transactions, time-based pruning, a bounded pending queue and the process lock. Incident service atomically persists observations/transitions/events. Report service snapshots and transactionally enqueues local-date keyed reports. Outbox owns ordered retries/expiry/eviction/retention. Matrix transport submits one preformatted event through a verified encrypted CLI profile and reports confirmed success/failure/ambiguity. CLI calls application services. Models must not serialize secrets or query values.

Use bounded, sanitized journal logs only. Never log secrets, credentials, secret-bearing paths, SQL text/results, HTTP body, unbounded subprocess output, crypto material, or diagnostic plaintext. Terminal errors are sanitized and nonzero on failure. Use argument vectors for subprocesses, never shell interpolation; bound execution and diagnostics. Successful completion must not be claimed without commit or explicit transport success. On errors preserve committed state and surface storage/transport status as applicable. No check/report modifies targets; systemd target service is read-only. Never grant `CAP_NET_RAW` to vshm or ping; no fallback capability or sandbox weakening.

## Security baseline
Config `/etc/vshm/vshm.toml` root:vshm mode 0640. Separate credential files `/etc/vshm/secrets/*.ini`, root:vshm 0640, not group/other writable and not readable by unauthorized users; secret files never appear in command args/logs/reports/source/sample. State dir `/var/lib/vshm` owned vshm:vshm mode 0700; SQLite and host-private crypto files mode 0600. Agent runs non-root vshm:vshm. Each host owns its Matrix account/device and private crypto state: never copy it across hosts. Monitoring config validation/startup does not require Matrix readiness; Matrix incompatibility blocks delivery only. Configuration applies only after explicit service restart; invalid config must fail before monitoring and not mutate saved monitoring state.

## Work package index
| ID | File | Scope | Dependencies | Parallelizable |
|---|---|---|---|---|
| WP-00 | `wp00-foundation.md` | skeleton, pinned toolchain, model/interfaces, SQLite contract, fakes | none | foundational first; model/schema decisions block implementation |
| WP-01 | `wp01-config.md` | TOML, validation, secrets, validate-config | WP-00 | with WPs 02–09 once interfaces agreed |
| WP-02 | `wp02-storage.md` | SQLite, migrations, retention, locking | WP-00 | with WP-01, adapters, scheduler; must integrate transaction contract |
| WP-03 | `wp03-scheduler.md` | cadence/workers/deadlines/result dispatch | WP-00, WP-02, WP-10 | with check adapters, metrics, reports, outbox |
| WP-04 | `wp04-check-systemd.md` | systemd read-only adapter | WP-00, WP-01 | with WP-05–09 |
| WP-05 | `wp05-check-http.md` | HTTP adapter | WP-00, WP-01 | with WP-04,06–09 |
| WP-06 | `wp06-check-ping.md` | unprivileged ping adapter | WP-00, WP-01 | with WP-04,05,07–09 |
| WP-07 | `wp07-check-mysql.md` | read-only MySQL adapter | WP-00, WP-01 | with WP-04–06,08–09 |
| WP-08 | `wp08-check-webdav.md` | read-only WebDAV adapter | WP-00, WP-01 | with WP-04–07,09 |
| WP-09 | `wp09-metrics.md` | report-only Linux metrics | WP-00, WP-01 | with adapters and reports |
| WP-10 | `wp10-incidents.md` | transition/threshold semantics/event enqueue | WP-00, WP-02 | with adapters/scheduler/report/outbox |
| WP-11 | `wp11-daily-report.md` | DST-aware schedule, report content/key | WP-00, WP-02, WP-09 | with checks/incident/outbox |
| WP-12 | `wp12-outbox-matrix.md` | retry and verified Matrix CLI | WP-00, WP-02 | with checks/incident/report; target CLI inspection gates sending |
| WP-13 | `wp13-cli-deployment.md` | command wiring, systemd, docs, E2E | WP-00–12 | integration last; packaging can begin alongside component work |

## Dependency graph and suggested order
WP-00 → WP-01 and WP-02. Then WP-03 and WP-04–09 can proceed in parallel against the frozen WP-00 contracts. WP-10 integrates storage and observation results; WP-11 uses storage and metrics; WP-12 integrates durable outbox and performs target-specific Matrix validation. WP-13 wires all components and acceptance tests. Integration may be staged, but never change public contracts silently.

## Adding a future check type
A new check is registered by its stable `type` in the registry and validated into immutable, typed config; receives `CheckConfig` plus one absolute deadline context; implements the WP-00 `Check` signature; returns exactly one normalized observation with UTC start/completion, duration, success/failure and short sanitized reason; does only read-only target operations; propagates the same deadline through DNS/connect/I/O/query/process work; has no incident/outbox writes. Add its TOML keys/defaults/ranges and secret-file handling to config validation, register the adapter, add fake-based success/failure/deadline/redaction tests and required project-tree path, then wire CLI/scheduler. Ask user about any underspecified semantics before implementation.

## Common Definition of Done
1. All files listed in the package's "files to create/touch" exist in the required tree; no out-of-scope changes without noting it.
2. `go build ./...` and `go vet ./...` pass; `make lint` (if defined) passes.
3. Unit tests for the package exist and `go test ./...` passes for the whole repo.
4. Runtime validation: the binary is built and actually run — at minimum `vshm validate-config` with a sample config, and for runnable packages a short `vshm once`/`vshm run` exercise against a fake/local target — with output recorded in the completion report.
5. Acceptance criteria are each ticked off with evidence.
6. Open questions in the package doc were asked and answered by the user; chosen answers are documented in code/README.
7. Public interfaces from WP-00 are unchanged; if a change is unavoidable, report it as a separate request.
8. A short completion report: what was built, how tested, known limitations.
