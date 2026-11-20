# WP-11 Daily Report — Completion Report

## Overview
WP-11 implements the daily report service in `internal/reports` (the pre-existing
stub had placeholder `Generate`/`Schedule` methods). The `Service` implements the
frozen WP-00 `model.Reporter` interface (`Render` + `Schedule`): it renders the
plain-text daily report from a store snapshot plus metrics sampled at render time,
and enqueues it transactionally under a unique local-date dedupe key. Scheduling is
a fixed configurable local `HH:MM` (default 09:00) with explicit DST gap/fold
resolution. `internal/model` public contracts are unchanged. Full CLI wiring of the
schedule loop into `vshm run` remains WP-13.

## What was built

| File | Status | Description |
|------|--------|-------------|
| `internal/reports/reports.go` | Rewritten | `Service` (implements `model.Reporter`): `NextReport` DST-aware local scheduling (gap → first valid instant after gap, fold → first occurrence), `Render` (metrics first, then per-check lines), `Schedule` loop (next future report only, no catch-up), `runReport` with local-date dedupe key via `Store.EnqueueReport` |
| `internal/reports/reports_test.go` | Created | 5 tests: HH:MM validation, next-report in UTC (incl. no catch-up), DST gap (2025-03-09 02:30 America/New_York → 03:00 EDT), DST fold (2025-11-02 01:30 → first occurrence EDT), render format (emoticons, NO RESULT, STALE, header-only timestamp), real-SQLite dedup across duplicate runs / restart / next day |
| `internal/metrics/metrics.go` | Touched | Rendered metrics block carries no timestamp line (user-confirmed: the report header is the only line with a date/time); stored `CollectedAt` remains UTC |
| `internal/metrics/metrics_test.go` | Touched | Render assertions updated (no `collected_at` line; UTC instant retained) |
| `docs/wp11-daily-report.md` | Touched | §5/§7/§8 updated with the user-confirmed message format and answered open questions |
| `README.md` | Touched | WP-11 status added |
| `docs/wp11-completion.md` | Created | This report |

### Message format (user-confirmed; documented in `docs/wp11-daily-report.md` §5)
Plain text; metrics block first, then one line per configured check. The header is
the only line with a date/time: host-local `DD.MM.YYYY HH:MM:SS`, no UTC offset.
Each check line starts with a status emoticon — ✅ success, ❌ failure, ❓ with
`NO RESULT` before any run — and carries ⌛ when STALE (age > that check's
interval + timeout). Check lines show `age`, `reason_code` and sanitized `reason`,
no timestamp. Missing metrics render as `UNAVAILABLE(<reason>)`, never zero.

```
daily report host=my-server at=20.11.2026 22:51:03
metrics:
  load1=0.16 load5=0.16 load15=0.11
  mem_total=30621.2 MiB mem_available=30358.4 MiB mem_used=262.8 MiB
  mount /: total=1873590.4 MiB available=561307.0 MiB used=1217038.2 MiB
  uptime=1431s
✅ check systemd-check (systemd): age=0s reason_code=success reason=unit "multi-user.target" is active
✅ check http-check (http): age=0s reason_code=success reason=HTTP 200 OK
❌⌛ check db-check (mysql): age=2h5m0s reason_code=query_failed reason=query_failed: connection refused
❓ check ping-check (ping): NO RESULT
```

The payload is stored verbatim in the outbox and sent unchanged by the Matrix
transport (WP-12 submits one preformatted event). The check-line template is
shared via `reports.FormatCheckLine`, which the `vshm once` observer in
`internal/cli` also uses, so CLI output and report lines are identical by
construction.

### Scheduling / dedup semantics
- Local-date key `2006-01-02` (e.g. `2025-11-20`) is inserted with the report event
  in one `EnqueueReport` transaction (INSERT OR IGNORE on the unique dedupe key):
  both or neither; duplicates on restart, clock adjustment or DST return
  `inserted=false` and enqueue nothing. The date key remains even if the event is
  later evicted; no regeneration.
- At startup only the next strictly-future local report instant is scheduled;
  downtime-missed reports are never caught up.
- DST: a report time inside a spring-forward gap fires at the first valid local
  instant after the gap; inside a fall-back fold at its first occurrence
  (resolved by scanning the local wall clock, deterministic for host tzdata).

## Open questions asked and answers
1. **Report text/order/format** → metrics before checks; header-only host-local
   `DD.MM.YYYY HH:MM:SS` timestamp without UTC offset (overrides the spec's
   "numeric UTC offset" wording — user decision, documented in the WP-11 doc);
   status as leading emoticon (✅/❌/❓), ⌛ when STALE.
2. **Local-date key representation** → local calendar date `2006-01-02` as the
   outbox dedupe key.
3. **Timezone database** → schedules and rendered timestamps rely on host-local
   `time.Local` (host tzdata); no timezone config exists.

## Verification evidence
- `go build ./...` — OK. `go vet ./...` (`make lint`) — OK.
  `go test ./...` — all packages OK; `internal/reports` 5/5 PASS (HH:MM validation;
  next-report UTC incl. exact-boundary and no-catch-up cases; DST gap → 03:00 EDT;
  DST fold → first occurrence EDT; render format incl. header-only date assertion;
  SQLite dedup: duplicate run and post-restart run add no second report, next day
  adds exactly one under the new date key).
- `make build` (`CGO_ENABLED=0`) — OK, static C-free binary.
- `vshm validate-config` on the sample config — exit 0.
- Runtime exercise: `vshm once` against a sample config with systemd/http/ping
  checks into a temp state dir (3 checks run, 0 failed), then rendered the report
  from that store snapshot — output matches the format block above (recorded in
  this report; the preview harness was temporary and removed afterwards).
- Acceptance mapping (§8): report fields present; `NO RESULT` before first run;
  `STALE` via ⌛ when age > interval+timeout; unavailable metrics `UNAVAILABLE`;
  DST gap/fold; one report per local date; restart dedup; no catch-up after
  downtime — all covered by the tests above.

## Out-of-scope changes (noted per DoD)
- `internal/metrics/metrics.go`: the rendered metrics block no longer prints a
  `collected_at` line (user-confirmed: header-only timestamps). WP-09's stored
  UTC `CollectedAt` and all metric semantics are unchanged.

## Known limitations
- The `Schedule` loop is implemented and unit-tested at the `runReport`/`NextReport`
  level but not yet started by `vshm run` — that wiring is WP-13.
- Delivery of the enqueued report (retries, Matrix CLI) is WP-12; the report sits
  pending until then, subject to the existing 24 h expiry / bounded queue.
