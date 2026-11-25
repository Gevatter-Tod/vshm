# WP-06 Ping Check — Completion Report

## Summary
Implemented the unprivileged single-probe ping check adapter per `wp06-check-ping.md` in
`internal/checks/ping/ping.go` (260 lines) with tests in `internal/checks/ping/ping_test.go` (186 lines).

## Files
- `internal/checks/ping/ping.go` — adapter
- `internal/checks/ping/ping_test.go` — 11 unit tests, all passing

## Implementation Details

### Adapter (`internal/checks/ping/ping.go`)
- **System `ping` executable, one probe**: `exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", host)` — argument vector only, no shell.
- **Unprivileged**: no capabilities requested anywhere; the binary and the child run without `CAP_NET_RAW`. The systemd unit grants no capabilities (see `packaging/vshm.service`).
- **Process group cleanup**: `SysProcAttr{Setpgid: true}`; cancellation of the context kills the owned process group via `CommandContext`. The subprocess runs under a deadline tightened by a 50 ms margin so the group kill lands inside the check's absolute deadline (WP-03 cleanup grace).
- **Bounded, sanitized output**: `CombinedOutput` result is never stored; only the exit code is used. `sanitizeReason` caps reasons at 256 chars and collapses whitespace/newlines.
- **Missing executable / permission / no-reply / timeout are failures, never skips**: distinct reason codes — `executable_not_found` (ENOENT), `timeout` (`context.DeadlineExceeded`), `ping_failed` (non-zero exit or other error, incl. unprivileged-socket denial), `invalid_config` (nil/wrong config type or already-elapsed deadline), `success`.
- **One absolute deadline**: `Run` propagates the incoming context deadline to the subprocess; no per-operation timeouts are reset.
- **Testability**: `executableRunner` interface with `realRunner` in production and `fakeRunner` in tests (`New` / `NewWithRunner` constructors).

### Reason codes
| Code | Meaning |
|------|---------|
| `success` | valid reply within the deadline |
| `ping_failed` | non-zero exit, no reply, permission/probe failure |
| `timeout` | context deadline exceeded |
| `executable_not_found` | `ping` binary absent (ENOENT) |
| `invalid_config` | nil or wrong config type |

### Tests (`internal/checks/ping/ping_test.go`, 11 tests)
Success, non-zero exit (`ping_failed`), missing executable (`executable_not_found`), deadline exceeded and tight-deadline (`timeout`), nil/wrong config (`invalid_config`), `Type()`, `Setpgid` process-group attribute set, reason sanitization, no-reply failure, timeout classification, duration/timestamps tracked.

```bash
go test ./internal/checks/ping/... -count=1   # ok — 11/11 pass
go build ./... && go vet ./...                 # clean
```

## Decisions
- The §7 open question (ping implementation/flags) was resolved for the target Linux image as
  iputils-style `ping -c 1 -W 1` (one probe, 1 s per-reply wait), which is unprivileged via the
  `ping_group_range` sysctl — no capabilities involved.
- Success requires a valid reply (exit code 0), not merely a successful launch.

## Definition of Done
- ✅ `internal/checks/ping/` exists with adapter + tests
- ✅ `go build ./...`, `go vet ./...` clean; 11/11 unit tests pass (no live ping needed in CI — runner is injected)
- ✅ Read-only, unprivileged, bounded, sanitized; single result per run under one absolute deadline
- ✅ Completion report documents the actual implementation

## Known Limitations
- No live-ping runtime validation in CI; on a host where unprivileged ping is permitted,
  `vshm check <ping-id>` against loopback exercises the real binary (otherwise the adapter
  correctly returns a failure observation, not a skip).
