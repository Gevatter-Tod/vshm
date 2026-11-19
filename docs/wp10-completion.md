# WP-10 Incidents — Completion Report

## Overview
WP-10 implements the incident service in `internal/incidents` (the pre-existing
stub had placeholder methods that did nothing). The service is the scheduler's
`Observer`: every normalized `model.CheckResult` the scheduler emits is consumed
by `Service.Observe`, which transactionally persists observation + incident
transition + outbox event(s) via `model.Store.RecordObservationAndIncident`.
`internal/model` public contracts are unchanged. The fake store
(`internal/model/fake`) was made stateful (upsert per check ID, mutation/event
capture) without changing any WP-00 signature, and the CLI now wires the real
service into `vshm once`/`vshm run` instead of the old log-only observer.

## What was built

| File | Status | Description |
|------|--------|-------------|
| `internal/incidents/incidents.go` | Rewritten | `Service` (implements `scheduler.Observer`), transition logic, single-line plain-text payload rendering, sanitization, local-time formatting with numeric UTC offset, unique event IDs |
| `internal/incidents/incidents_test.go` | Created | 13 tests: fake store/clock unit tests + real-SQLite atomicity-rollback and restart-persistence tests |
| `internal/model/fake/store.go` | Touched | Stateful incident upsert per check ID; `Mutations()`/`OutboxEvents()`/`Observations()`/`Incident()` accessors; signatures unchanged |
| `internal/cli/cli.go` | Touched | `newScheduler` takes the observer; `RunOnce`/`RunAgent` build `incidents.Service` on the opened store; log-only observer removed |
| `cmd/vshm/main.go` | Touched | `once` accepts an optional state-dir argument (`vshm once <config> [statedir]`) so the WP-10 runtime validation can use a temp state dir |
| `tests/fixtures/incidents/{healthy,failing}.toml` + `README.md` | Created | Local-HTTP fixtures for the `vshm once` exercise |
| `README.md`, `docs/architecture.md` | Touched | WP-10 status + confirmed decisions; fixed the stale "threshold-based" incident description in architecture.md |
| `docs/wp10-completion.md` | Created | This report |

### Semantics (spec §5, fixed — no thresholds/flap suppression introduced)
- Incident state is keyed by exact `host_name` + stable check ID. The store is
  per-host (single agent per host via the storage process lock), so `host_name`
  is carried on every mutation/event and the check ID keys the incident row.
- Failure: `RecordObservationAndIncident` with `Open=true, LastFailure=CompletedAt`
  plus **exactly one** failure event with unique ID `evt-failure-<check>-<unixnano>`.
  First failure opens the incident; repeats keep the original `since` (store
  preserves it while state is unchanged) and update last-failure time/reason.
- Success with an open incident (read back via `Store.Snapshot`): closes it
  (`Open=false, ResolvedAt=CompletedAt`) and enqueues **one** recovery event whose
  payload carries `incident_started`. Success without an open incident calls
  plain `RecordObservation` — no event, no incident write.
- Downtime alone never transitions: state is re-read from the store on every
  observation, and no observation exists for time the agent was down.
- Per-check serialization is guaranteed by the scheduler (a check never overlaps
  itself), so read-state-then-write is race-free per check; the write itself is
  one atomic store transaction.

### Message format (user-confirmed: single-line key=value, plain text)
```
vshm event=failure host=web-01 check=nginx type=systemd time=2026-11-19T07:54:40+00:00 reason_code=http_failed reason="HTTP check failed: ..."
vshm event=recovery host=web-01 check=nginx type=systemd time=2026-11-19T07:55:40+00:00 incident_started=2026-11-19T07:54:40+00:00 status=ok reason_code=success reason="HTTP 200 OK"
```
- Local timestamps use layout `2006-01-02T15:04:05-07:00` in server-local time →
  always a **numeric** UTC offset (`+00:00` for UTC, never `Z`).
- Reason codes/reasons are passed through unchanged from the observation
  (user-confirmed: no WP-10 vocabulary/remapping), then sanitized: IDs/tokens keep
  only `[a-zA-Z0-9_.-]` (≤64), the quoted reason is single-line, quote-free,
  printable, ≤200 chars.
- Outbox row kind stays `event` (reports use `report`); the failure/recovery kind
  is the payload's `event=` key. Event IDs are unique per (kind, check,
  completion instant).

## Open questions asked and answers (also in README)
1. **Message wording/line format** → single-line `key=value` plain text, one line
   per event, fields as above (host, check ID/type, event kind, local timestamp
   with numeric UTC offset, sanitized reason/status, `incident_started` on
   recovery).
2. **Reason-code vocabulary** → pass through the observation's `reason_code`/
   sanitized `reason` unchanged; WP-10 introduces no mapping.

## Verification evidence
- `go build ./...` — OK. `go vet ./...` (`make lint`) — OK.
  `go test ./...` — all packages OK; `internal/incidents` 13/13 PASS:
  first failure opens incident + one event; repeat failures → distinct events,
  no duplicate incident, original start kept, last-failure updated; recovery
  once with `incident_started`, second success silent; success without open
  incident silent (observation only); separate events across checks; payload
  sanitization (newlines/quotes/length); numeric-offset formatting; event-ID
  shape; constructor validation; plus real-SQLite tests: atomic rollback on
  duplicate event ID (observation count unchanged), state survives store
  close/reopen ("restart") with recovery carrying the original incident start,
  downtime alone fabricates no observation/incident/event.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/wp10/vshm ./cmd/vshm` →
  `file`: "ELF 64-bit LSB executable, x86-64, statically linked" (C-free).
- `vshm validate-config tests/fixtures/incidents/healthy.toml` → exit 0.
- Runtime exercise (local `python3 -m http.server 18099` as the target only —
  a /tmp test harness, not part of the product; the binary contains no Python):
  `vshm once failing.toml` ×2 → `vshm once healthy.toml` ×2 against one state dir.
  Resulting outbox ledger (3 pending rows, 4th run added none):
  ```
  evt-failure-local-http-1795074880773404547  event pending  vshm event=failure  host=wp10-test check=local-http type=http time=2026-11-19T07:54:40+00:00 reason_code=http_failed reason="HTTP check failed: unexpected status code: expected 500, got 200"
  evt-failure-local-http-1795074940453902660  event pending  vshm event=failure  host=wp10-test check=local-http type=http time=2026-11-19T07:55:40+00:00 reason_code=http_failed reason="..."
  evt-recovery-local-http-1795074940456872490 event pending  vshm event=recovery host=wp10-test check=local-http type=http time=2026-11-19T07:55:40+00:00 incident_started=2026-11-19T07:54:40+00:00 status=ok reason_code=success reason="HTTP 200 OK"
  ```
  `incidents` row afterwards: `('local-http','closed',0,'2026-11-19T07:55:40Z',...)`;
  journal showed `incidents: opened incident ... reason_code=http_failed` and
  `incidents: recovery ... incident_started=2026-11-19T07:54:40+00:00`.
  `vshm status /tmp/wp10-state` → Healthy: true, Observations: 4, pending: 3.
  Fresh state dir + one healthy `once` → observations: 1, outbox rows: 0,
  incident rows: 0 (**no events for healthy observations**).
- Acceptance mapping (§8): every failed observation → exactly one distinct
  message incl. repeats; no grouping/confirmation/warnings; recovery once;
  success without open incident silent; persistence across restart; messages
  sanitized single-line plain text with all required fields. Transition/atomic
  rollback, timestamps and repeated distinct events are covered by the tests
  above.

## Out-of-scope changes (noted per DoD)
- `internal/cli/cli.go` + `cmd/vshm/main.go`: wired the WP-10 observer into
  `once`/`run` (the scheduler doc already designated WP-10 as the observer) and
  added the optional state-dir arg to `once` for validation. Full CLI command
  surface (`check`, `validate-matrix`, …) remains WP-13.
- `internal/model/fake/store.go`: fake became stateful to model incident state;
  no WP-00 signature changed.
- `docs/architecture.md`: one-line fix — incidents were described as
  "threshold-based", which contradicts the fixed no-threshold contract.

## Known limitations
- Incident state is read via `Store.Snapshot` before each write (the frozen WP-00
  `Store` interface offers no per-check read); safe because a check never
  overlaps itself, but it is one extra read transaction per observation.
- The v1 `incidents` table stores `since` (opened-at while open, resolved-at once
  closed), so a *closed* incident's `OpenedAt` reads back as zero in snapshots;
  the recovery message is unaffected because the start time is captured while the
  incident is still open. Changing that would be a WP-02 schema change —
  reported here, not done silently.
- Delivery of the enqueued events (retries, Matrix CLI) is WP-12; events sit
  pending until then, subject to the existing 24 h expiry / bounded queue.
