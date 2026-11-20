# vshm — host monitor agent

Very Simple Host Monitor

One independent Linux amd64 agent per server. Observes configured checks,
records observations/incidents in local SQLite, and queues plain‑text events and
daily reports for the host's Matrix account via `matrix-commander-rs` (E2EE
mandatory). No coordinator, UI, API, remediation, or alternate transport.

## Status

WP‑00 (foundation) is complete: project skeleton, frozen shared contracts
(`internal/model`), SQLite schema v1 + migration runner + process lock
(`internal/storage`), sanitized logger, deterministic fakes
(`internal/model/fake`), minimal CLI. Monitoring itself (checks, scheduler,
incidents, reports, outbox, transport) is delivered by WP‑01…WP‑13.

WP‑03 (scheduler) is complete: `internal/scheduler` runs checks on four fixed
workers with monotonic cadence, one absolute deadline per execution, a 2 s
cleanup grace and hourly pruning (see `docs/wp03-completion.md`).

WP‑09 (metrics) is complete: `internal/metrics` collects report‑only Linux
resource metrics (see `docs/wp09-completion.md`). Metrics are sampled when a
daily report is rendered and never open incidents or alerts. Decisions
confirmed with the user: mount stats use the pure‑Go stdlib `syscall.Statfs`
(CGO‑free, no extra dependency); an omitted/empty `[metrics].mounts` defaults
to `["/"]`; the snapshot carries load1/5/15 from `/proc/loadavg`; rendered
values use explicit units (MiB for bytes, seconds for uptime, two decimals for
load) and unavailable sources/mounts render as `UNAVAILABLE(<reason>)`, never
zero or a guessed value.

WP‑10 (incidents) is complete: `internal/incidents` is the scheduler's observer
and transactionally records each observation together with the per‑check
incident transition and its outbox event(s) (see `docs/wp10-completion.md`).
Every failed observation enqueues exactly one distinct failure event (no
thresholds, no grouping, no confirmation checks); the first failure opens the
incident keyed by `host_name` + check ID, repeat failures only update
last‑failure time/reason, the first later success closes the incident and
enqueues one recovery event carrying the incident start time, and a success
without an open incident records only. Messages are plain‑text single‑line
`key=value` (user‑confirmed), with local timestamps rendered with a numeric
UTC offset, and reason codes/reasons passed through from the observation,
sanitized to one bounded line. `vshm once`/`vshm run` wire the service to the
SQLite store, so state survives restarts and downtime alone never transitions.

WP‑11 (daily report) is complete: `internal/reports` renders the daily plain‑text
report (metrics first, then one line per configured check with a leading status
emoticon ✅/❌/❓ and ⌛ when stale; the header carries the only date/time as
host‑local `DD.MM.YYYY HH:MM:SS`) and enqueues it transactionally under a unique
local‑date key, with DST gap/fold‑aware scheduling at a fixed local `HH:MM`
(default 09:00), next‑future‑only scheduling and no catch‑up (see
`docs/wp11-completion.md`).

## Toolchain (fixed)

- Go **1.27.3**, `CGO_ENABLED=0`, Linux amd64 only.
- Pinned: `github.com/BurntSushi/toml v1.6.0`, `github.com/go-sql-driver/mysql v1.10.1`,
  `modernc.org/sqlite v1.61.1` (pure Go).

## Build / test