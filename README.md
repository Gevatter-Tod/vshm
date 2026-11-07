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

## Toolchain (fixed)

- Go **1.27.4**, `CGO_ENABLED=0`, Linux amd64 only.
- Pinned: `github.com/BurntSushi/toml v1.6.0`, `github.com/go-sql-driver/mysql v1.10.1`,
  `modernc.org/sqlite v1.61.1` (pure Go).

## Build / test