# wp03-scheduler — Completion Report

## Summary
WP-03 is complete. `internal/scheduler` runs configured checks on a fixed pool of four workers with a monotonic grid cadence, one absolute deadline per execution, a 2 s cleanup grace and an hourly `Store.Prune`.

## Files
### Created
- `internal/scheduler/scheduler.go`: `Scheduler`, `Entry`, `Observer`, `Pruner`, `Run`, `Once`, `Healthy`, `ErrCleanupTimeout`.
- `internal/scheduler/entries.go`: `EntriesFromConfig` resolves the agent default (30m/10s) and per-check interval/timeout overrides, re-validates the effective limits (1m–7d, 1–300s, timeout < interval) and looks up the adapter.
- `internal/scheduler/registry.go`: thread-safe `model.Registry` implementation.
- `internal/scheduler/scheduler_test.go`, `realclock_test.go`: deterministic fake-clock tests and a real-clock process-group test.
### Modified
- `internal/cli/cli.go`: `RunAgent` and `RunOnce` build and run the scheduler (all five adapter types are registered; observations are only logged until WP-10 provides the `Observer`; `Prune` is called on the store).
- `README.md`, `docs/architecture.md`: status/scheduler notes.

WP-00 public interfaces are unchanged.

## Decisions (documented in the package comment)
- **Cadence**: every check runs immediately at start, then at `start + n*interval`. A tick missed because the process stalled runs once, never replayed; the grid is not shifted.
- **Skip**: a due tick whose previous execution is still queued or running is skipped and logged (`skipping due run of check <id>`); no observation is created.
- **Deadline**: created when a worker starts the execution (`context.WithDeadline`, plus a clock timer as the authoritative trigger). The same context goes to the adapter.
- **Exactly one result**: the scheduler emits one observation per execution. On timeout it emits a `timeout` failure; a late adapter result is discarded. An adapter that returns at/after the deadline also yields only the timeout result. Panics become one `adapter_panic` failure.
- **Normalization**: `CheckID`/`CheckType` come from the config; `StartedAt`/`CompletedAt` (UTC) and `Duration` are measured by the scheduler around the execution.
- **Cleanup ownership**: the adapter releases its connections and kills its subprocess process group when the context is done, before `Run` returns. "Run returned" means "cleanup finished". If that takes more than 2 s after the deadline or shutdown, the scheduler is unhealthy and `Run`/`Once` return `ErrCleanupTimeout`, so the process exits nonzero for systemd (cgroup kill removes leftovers). Adapters that implement `io.Closer` are closed once after the workers stop.
- **Shutdown**: executions interrupted by shutdown are not recorded (no fabricated failure); downtime creates nothing.
- **Prune**: once at startup, then every hour.
- **Results** go to an `Observer` (the WP-10 incident service); the scheduler never writes store/outbox itself except `Prune`.

## Testing
`go build ./... && go vet ./... && go test -race ./...` pass. Scheduler tests (fake clock, 30 repetitions race-clean) cover: prompt start and grid cadence, per-check intervals, no replay of missed ticks, skip-and-log without fabricated failure, four-worker bound, no self-overlap, single deadline, exactly one timeout observation, late result discarded, cleanup-grace failure (unhealthy, `ErrCleanupTimeout`), shutdown without observation, panic handling, normalization, hourly prune, restart without fabricated results, `Once`, adapter close, config default/override resolution and validation. A real-clock test checks the process group of a timed-out subprocess is gone.

Runtime validation (`/tmp`, no public network; the WP-04…08 adapters are still placeholders):
```
$ vshm once c.toml            # systemd + ping(192.168.1.1, 2s timeout)
observation check=sd-a type=systemd status=OK code=success duration=2.26ms
observation check=ping-a type=ping status=FAIL code=timeout duration=2.0005s reason="check exceeded timeout of 2s"
$ vshm run c.toml state       # + "scheduler: pruned observations=0 outbox=0"; SIGINT -> exit 0; no leftover ping process
```

## Known limitations / follow-ups
- `config.Validate` (WP-01) does not check the effective per-check `timeout < interval`; `EntriesFromConfig` does, but `validate-config` will not report it. Suggest adding it to WP-01 validation.
- The placeholder adapters ignore their typed config; WP-04…08 must honour the context deadline and own process-group termination (`Setpgid` + kill `-pgid` on cancel).
- Observations are only logged until WP-10 supplies an `Observer`; WP-13 wires it. Health is exposed through `Scheduler.Healthy()`.
