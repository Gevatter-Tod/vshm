# wp02-storage — Completion Report

## Summary
WP-02 is complete. `internal/storage` provides a single SQLite store with:
- the fixed v1 schema,
- one transaction helper,
- an `flock` single-instance lock,
- a bounded pending queue,
- time-based pruning.

It implements the WP-00 `model.Store` interface, including the changed `Prune(ctx, now)` signature.

## Files Created/Modified

### Created
- `internal/storage/store.go`: `Open`, `Close`, the `tx` helper, the process lock, `RealClock` and the time helpers.
- `internal/storage/observations.go`: `RecordObservation`, `RecordObservationAndIncident` and the incident upsert.
- `internal/storage/outbox.go`: event and report inserts, the bounded queue, `Pending` and `MarkDelivery`.
- `internal/storage/prune.go`: batched `Prune` plus a WAL checkpoint.
- `internal/storage/status.go`: `Snapshot` and `Status`.
- `internal/storage/options.go`: `Options` with defaults (90d / 30d / 24h / 1000) and `ErrAlreadyRunning`.
- `internal/storage/store_test.go`: the acceptance tests.

### Modified
- `internal/storage/schema.go`: reduced to `const schema` and the schema version.
- `internal/model/model.go`:
  - `CheckStatus` gained `Successes24h` and `Failures24h`.
  - `StoreStatus` gained `DBSizeBytes`, `SchemaVersion` and `LockPath`.
  - These are additive and were approved by the user.
- `internal/cli/cli.go`:
  - `Status(ctx, stateDir)` opens `<statedir>/vshm.db` and prints sorted outbox counts.
  - New `RunAgent(ctx, configPath, stateDir)` validates the config, takes the lock and blocks until it is cancelled. Scheduling follows in WP-03.
- `cmd/vshm/main.go`: `run [config] [statedir]` now calls `cli.RunAgent`. It previously called a `cli.Run` that clashed with an existing function.
- `docs/architecture.md`: storage paragraph.

### Removed
- `internal/storage/options_test.go`: it tested a `NewProcessLock` helper that is not part of the brief. Its defaults test moved to `store_test.go`.

## Implementation Details

- **Connection:**
  - Uses `modernc.org/sqlite`.
  - DSN pragmas: WAL, `busy_timeout(5000)`, `synchronous(NORMAL)`, `foreign_keys(ON)`.
  - `SetMaxOpenConns(1)`.
  - The DB and lock files are chmod 0600.
- **Lock:**
  - `<dir(path)>/vshm.lock` with `LOCK_EX|LOCK_NB`. `EWOULDBLOCK` returns `ErrAlreadyRunning`.
  - The descriptor stays open until `Close`.
  - A failed `Open` (for example a wrong schema version) releases the lock.
- **Schema version:** inserted on first open. A different stored value makes `Open` fail with a clear error.
- **Timestamps:** RFC3339 UTC strings with second precision, so they sort correctly as text.
- **Incident row:** `IncidentMutation` has no failure-count or start-time fields, so they are derived from the previous row:
  - `state` is `open` or `closed` from `Open`.
  - `consecutive_failures` is the previous count + 1 on failure, and 0 on success.
  - `since` stays the same while the state is unchanged. On a change it is set to `LastFailure` (opening) or `ResolvedAt` (closing), falling back to `CompletedAt`.
  - `last_change` is the observation's completion time.
- **Outbox inserts:** `next_attempt_at = enqueued_at`. A zero `EnqueuedAt` falls back to `clock.Now()`. The queue bound runs in the same transaction after every insert.
- **MarkDelivery:** the parameters are used as `attemptedAt` and `nextAttemptAt`.

  | Outcome | Effect |
  |---|---|
  | `DeliverySuccess` | `delivered`, `attempts+1`, `finished_at` set |
  | `DeliveryFailure` / `DeliveryAmbiguous` | stays `pending`, `attempts+1`, `next_attempt_at` and `last_reason` updated |
  | `DeliveryEvicted` (gave up) | `dropped` |
  | `DeliveryExpired` | `expired` |

  An unknown outcome or an unknown `event_id` returns an error.
- **Pending:** expires rows first, then selects due rows `ORDER BY id LIMIT ?`. A limit of 0 or less means no limit.
- **Prune:**
  - Runs one transaction per 500-row batch.
  - The outbox delete is limited to `kind = 'event' AND status != 'pending'`, so report dedupe rows are kept indefinitely.
  - The `wal_checkpoint(TRUNCATE)` result is ignored.
- **Snapshot:**
  - All incident rows.
  - The latest observation per check, using SQLite bare columns with `MAX(completed_at)`.
  - 24h success/failure counts.
  - `LastReport` from the newest report row.
- **Status:**
  - DB file size, schema version, observation count (`RecordsCount`) and lock path.
  - Outbox counts are keyed by the database status (`pending`, `delivered`, `expired`, `dropped`).
- **Errors:** wrapped with the operation name and returned. There is no free-space probing, retrying, evicting on error or VACUUM, and SQL text and row content are never logged.

## Testing

`go test ./internal/storage/` uses a temp dir, real SQLite and `fake.Clock`. All tests pass.

| # | Acceptance criterion | Test | Result |
|---|---|---|---|
| 1 | Schema created, DB and lock file 0600, second `Open` fails with `ErrAlreadyRunning`, `Open` succeeds after `Close` | `TestOpenSchemaPermissionsAndLock`, `TestOpenRejectsUnknownSchemaVersion` | ✅ |
| 2 | Atomicity: a duplicate `event_id` leaves no observation, incident or outbox row | `TestRecordObservationAndIncidentAtomic` | ✅ |
| 3 | Same report key twice returns `true` then `false`, one row | `TestEnqueueReportDedupe` | ✅ |
| 4 | Rows older than 24h become `expired` and are not returned; order is by id; future `next_attempt_at` is skipped | `TestPending` | ✅ |
| 5 | `MarkDelivery` transitions and attempt counting | `TestMarkDelivery` | ✅ |
| 6 | MaxPending+5 inserts leave exactly MaxPending pending; the 5 oldest are `dropped` with `queue_full` | `TestQueueBound` | ✅ |
| 7 | Prune: 91d observations deleted, 89d kept; 31d delivered events deleted, 29d kept; pending, report rows and incidents kept; correct counts (more than 1 batch) | `TestPrune` | ✅ |
| 8 | Rows and report keys persist after Close and reopen | `TestReopenPersists` | ✅ |
| 9 | Status counts per outbox status are correct | `TestStatus` | ✅ |

There are also tests for option defaults (`TestOptionsWithDefaults`) and incident state tracking with Snapshot contents (`TestIncidentRowTracksState`).

`go build ./...`, `go vet ./...` and `go test ./...` pass for the whole repo.

### Runtime validation
Run against a temp state dir (`/tmp/vshm-wp02-state/s`), never `/var/lib/vshm`:

```text
$ vshm validate-config config/example.toml
Configuration validated successfully at config/example.toml

$ vshm run config/example.toml /tmp/vshm-wp02-state/s      # first instance
vshm running for host my-server

$ vshm run config/example.toml /tmp/vshm-wp02-state/s      # second instance
run: failed to open storage: vshm is already running        # exit 1

$ vshm status /tmp/vshm-wp02-state/s                        # after stopping the first
Storage Status:
  Healthy: true
  Schema Version: 1
  Database Size: 4096 bytes
  Lock File: /tmp/vshm-wp02-state/s/vshm.lock
  Observations: 0
  Outbox Counts:
    delivered: 0
    dropped: 0
    expired: 0
    pending: 0

$ ls -la /tmp/vshm-wp02-state/s
-rw------- vshm.db
-rw------- vshm.lock
```

## Known Limitations
- `vshm status` takes the same process lock, so it cannot run while `vshm run` holds it.
- `tests/fixtures/test_config.toml` fails to parse (`checks` type mismatch). It fails the same way at HEAD, so it is a WP-01 issue outside this package. Runtime validation used `config/example.toml`.
- The config's `[storage]` section is not yet mapped onto `storage.Options`, so the CLI uses the defaults. The leftover `min_available_bytes` key in WP-01 is no longer used by storage.
- Non-test storage code is about 760 lines including comments and blank lines, above the brief's 600-line target.
- Some files outside this package are not gofmt-formatted. They are pre-existing and were not touched.
- The Snapshot `HostName` is left empty because the store does not know the host name; callers fill it in.
