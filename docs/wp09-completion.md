# WP-09 Metrics — Completion Report

## Overview
WP-09 implements report-only Linux resource metrics in `internal/metrics`. The
pre-existing stub was inconsistent (did not compile: unused variable, mock types
mismatched the interfaces, cached one-shot proc reads, treated missing
`MemAvailable` as zero, added an unpinned `prometheus/procfs` dependency, and
inferred a hostname — which the spec forbids). It was rewritten from scratch
against the frozen WP-00 contracts. `internal/model` was not touched.

## What was built

| File | Status | Description |
|------|--------|-------------|
| `internal/metrics/metrics.go` | Rewritten | Collector, typed snapshot, proc reader, `syscall.Statfs` provider, renderer |
| `internal/metrics/metrics_test.go` | Rewritten | 20 tests: fakes, fixtures, formulas, unavailable handling, render, real host |
| `tests/fixtures/metrics/procfs-malformed/` | Created | Malformed `loadavg`/`meminfo`/`uptime` fixtures |
| `tests/fixtures/metrics/procfs-no-memavailable/meminfo` | Created | Fixture missing `MemAvailable` (must be UNAVAILABLE, not zero) |
| `go.mod` / `go.sum` | Touched | Removed unpinned `github.com/prometheus/procfs`; direct deps are again exactly the three pinned modules |
| `README.md` | Touched | WP-09 status + documented user-confirmed decisions |
| `config/example.toml` | Touched | `[metrics]` comment/example aligned to the confirmed `["/"]` default |
| `internal/checks/systemd/{config.go,systemd.go,systemd_test.go}` | Touched (out-of-scope fix, see below) | Made the pre-existing WP-04 test compile/pass so `go vet ./...` and `go test ./...` are green repo-wide |

### Design
- `ProcReader` (injected; `ProcFS{Root}` reads `loadavg`/`meminfo`/`uptime`, so
  tests point it at `tests/fixtures/metrics/procfs*`) and `Statvfs` (injected;
  `RealStatvfs` uses pure-Go stdlib `syscall.Statfs` — CGO-free, no new dep).
- `Collector.Collect(ctx, mounts, now) Snapshot` — `now` is the report-render
  instant, stored as `CollectedAt` in UTC. Each source is independent; a failed
  source marks only its own metric unavailable with a bounded reason code
  (`read_failed`/`parse_failed`/`missing_field`/`statvfs_failed`/`cancelled`)
  and a sanitized ≤120-char single-line reason. `Collect` never fails the report.
- Formulas: memory `used = MemTotal − MemAvailable` (kB→bytes; both fields
  required, missing field ⇒ UNAVAILABLE); mount `total = f_blocks·f_bsize`,
  `free = f_bfree·f_bsize`, `available = f_bavail·f_bsize`, `used = total − free`;
  load1/5/15 from `/proc/loadavg`; uptime seconds from `/proc/uptime`.
- `Render(Snapshot)` emits plain text with explicit units — MiB for bytes,
  seconds for uptime, two decimals for load — and `UNAVAILABLE(<reason>)` for
  missing sources/mounts, never zero or a guessed value.
- Report-only: the package produces only `Snapshot`/text; it has no Store,
  incident, or outbox access, so it cannot create check failures, incidents, or
  alerts. No host_name inference (comes from required config at the report layer).

## Open questions asked and answers (also in README)
1. **statvfs CGO-free** → stdlib `syscall.Statfs` (pure Go on linux/amd64; keeps
   the binary C-free and the dep list pinned).
2. **Omitted `[metrics].mounts` default** → `["/"]` (root mount always reported);
   applied via `ResolveMounts` in the metrics package, so WP-01 code is unchanged.
3. **Report format** → plain-text block, explicit units; load1/5/15; MiB for
   bytes; no percentages.
4. **Load scope** → capture and report 1/5/15-minute load (1-minute is the
   required sample).

## Verification evidence
- `go build ./...` — OK. `go vet ./...` (`make lint`) — OK.
  `go test ./...` — all packages OK, incl. `internal/metrics` 20/20 PASS.
  (`make test` uses `-race`, which needs a CGO toolchain; this environment has
  `CGO_ENABLED=0` and no gcc, so the race detector cannot run here — plain
  `go test ./...` passes.)
- `CGO_ENABLED=0 go build -o vshm ./cmd/vshm` → `file`: "statically linked …
  not a dynamic executable" (C-free Linux amd64).
- `./vshm validate-config /tmp/vshm-sample.toml` (with `[metrics] mounts=["/"]`) →
  "Configuration validated successfully", exit 0; same with `[metrics]` omitted;
  config without `host_name` fails with sanitized error, exit 1.
- Live exercise against real `/proc` and root mount (`go run` of a temporary
  preview main + `TestCollectRealSystem`), output:
  ```
  metrics:
    collected_at=2026-11-18T21:10:23Z
    load1=0.55 load5=0.50 load15=0.61
    mem_total=30621.2 MiB mem_available=29358.3 MiB mem_used=1262.9 MiB
    mount /: total=1873590.4 MiB available=565022.7 MiB used=1213322.5 MiB
    mount /definitely/not/a/mount: UNAVAILABLE(statvfs_failed: no such file or directory)
    uptime=4051s
  ```
- Acceptance criteria (§8 metrics definitions/units/unavailable; report-only):
  exact formulas, mount unavailable, malformed/missing proc fields, units, and
  snapshot time are all covered by tests; unavailable metrics render as
  `UNAVAILABLE` and never as zero; no metric path can create incidents/alerts.

## Out-of-scope changes (noted per DoD)
- `internal/checks/systemd`: added `config.go` (`NewSystemdConfig`) and taught
  `Run` to accept `*systemdConfig` in addition to `*scheduler.Spec`, because the
  WP-04 test file referenced `NewSystemdConfig`, which did not exist — the repo
  did not compile its tests before this WP. `systemd_test.go` was also gofmt'ed.
  No WP-00 public interface changed.
- `config/example.toml` comment/example updated to the confirmed `["/"]` mounts
  default (WP-01's `validateMetrics` still defaults to `[]`; behavior is decided
  in the metrics package).

## Known limitations
- Metrics are not yet wired into a CLI command or the daily report (that is WP-11,
  whose `internal/reports` is still a placeholder); `Render` output is ready to embed.
- Mount "used" follows df semantics (total − free, includes reserved blocks);
  available-to-unprivileged (`f_bavail`) is reported separately.
- Uptime is rendered as whole seconds.
