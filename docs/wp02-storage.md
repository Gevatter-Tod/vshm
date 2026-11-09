# wp02-storage — implementation brief (simplified)

## 1 Purpose
One small SQLite store for vshm: a handful of tables, one transaction helper, a single-instance file lock, and time-based pruning. Nothing else. All "low disk space" handling from the previous version of this brief is **removed**: a storage error is returned to the caller, logged, and the agent carries on with the next tick.

## 2 Dependencies and parallelism
Depends on WP-00 (model types). Can run in parallel with WP-01 and WP-03–09. WP-10/11/12 call the store methods below.

## 3 Interface provided (WP-00 `Store`, unchanged except one parameter)
```go
type Store interface {
  RecordObservation(ctx context.Context, r CheckResult) error
  RecordObservationAndIncident(ctx context.Context, r CheckResult, m IncidentMutation, ev []OutboxEvent) error
  Snapshot(ctx context.Context, now time.Time) (Snapshot, error)
  EnqueueReport(ctx context.Context, reportKey string, ev OutboxEvent) (inserted bool, err error)
  Pending(ctx context.Context, now time.Time, limit int) ([]OutboxEvent, error)
  MarkDelivery(ctx context.Context, eventID string, out DeliveryOutcome, attemptedAt, nextAttemptAt time.Time, reason string) error
  Prune(ctx context.Context, now time.Time) (PruneResult, error)   // CHANGED: the former int64 "min free bytes" argument is dropped
  Status(ctx context.Context, now time.Time) (StoreStatus, error)
  Close() error
}
```
Constructor: `func Open(ctx context.Context, path string, clock Clock, opts Options) (*SQLiteStore, error)` where `Options{ObservationRetention, DeliveredRetention, PendingMaxAge time.Duration; MaxPending int}` with defaults 90d / 30d / 24h / 1000. `Open` acquires the lock, opens the DB, creates the schema. No separate migration step exists.

## 4 Files
`internal/storage/store.go` (Open, Close, tx helper, lock), `internal/storage/schema.go` (one `const schema string`), `internal/storage/observations.go`, `internal/storage/outbox.go`, `internal/storage/prune.go`, `internal/storage/store_test.go`. Touch `docs/architecture.md` (one paragraph). Target: ≤ 600 lines of non-test Go.

## 5 Schema (fixed, v1 — create with `CREATE TABLE IF NOT EXISTS`, no migration framework)
```sql
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);   -- one row: schema_version=1
CREATE TABLE IF NOT EXISTS observations (
  id INTEGER PRIMARY KEY, check_id TEXT NOT NULL, check_type TEXT NOT NULL,
  success INTEGER NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL, reason_code TEXT NOT NULL, reason TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS observations_completed ON observations(completed_at);
CREATE INDEX IF NOT EXISTS observations_check ON observations(check_id, completed_at);
CREATE TABLE IF NOT EXISTS incidents (
  check_id TEXT PRIMARY KEY, state TEXT NOT NULL, consecutive_failures INTEGER NOT NULL,
  since TEXT NOT NULL, last_change TEXT NOT NULL, last_reason TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS outbox (
  id INTEGER PRIMARY KEY,                -- insertion order == send order
  event_id TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,                    -- 'event' | 'report'
  dedupe_key TEXT UNIQUE,                -- report local date key; NULL for events
  body TEXT NOT NULL,
  enqueued_at TEXT NOT NULL, status TEXT NOT NULL,   -- 'pending' | 'delivered' | 'expired' | 'dropped'
  attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT, finished_at TEXT, last_reason TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(status, next_attempt_at);
```
All timestamps are RFC3339 UTC strings. If `meta.schema_version` exists and is not `1`, `Open` fails with a clear error (future versions add migrations then; not now).

## 6 Behaviour
**Connection.** `modernc.org/sqlite v1.61.1`, DSN `file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)`. `db.SetMaxOpenConns(1)`. Chmod DB and lock file 0600 after creation. One helper: `func (s *SQLiteStore) tx(ctx, fn func(*sql.Tx) error) error` — begin, run, commit, rollback on error. Every method is exactly one `tx` call.

**Lock.** `Open` creates `<statedir>/vshm.lock` and calls `syscall.Flock(fd, LOCK_EX|LOCK_NB)`. `EWOULDBLOCK` → return `ErrAlreadyRunning`. The fd stays open until `Close`. Nothing more.

**RecordObservation.** Insert one row.

**RecordObservationAndIncident.** In one tx: insert observation; `INSERT OR REPLACE` the incident row from `IncidentMutation`; insert each `OutboxEvent` with `kind='event', status='pending', next_attempt_at=enqueued_at`.

**EnqueueReport.** In one tx: `INSERT OR IGNORE` with `kind='report', dedupe_key=reportKey`. Return `inserted = rowsAffected == 1`. This is the whole "report key first, same transaction" requirement.

**Pending.** In one tx: `UPDATE outbox SET status='expired', finished_at=now WHERE status='pending' AND enqueued_at < now-PendingMaxAge`; then `SELECT … WHERE status='pending' AND next_attempt_at <= now ORDER BY id LIMIT ?`. Expiry-before-delivery is thereby guaranteed.

**MarkDelivery.** `Delivered` → status delivered, finished_at. `Failed`/`Ambiguous` → attempts+1, next_attempt_at, last_reason (WP-12 computes the backoff and passes `nextAttemptAt`; the store does not own retry policy). `GaveUp` → status dropped.

**Bounded queue (replaces low-space eviction).** After every outbox insert, in the same tx: `UPDATE outbox SET status='dropped', last_reason='queue_full', finished_at=now WHERE status='pending' AND id NOT IN (SELECT id FROM outbox WHERE status='pending' ORDER BY id DESC LIMIT MaxPending)`. Oldest pending are dropped first; the table can never hold more than `MaxPending` pending rows.

**Prune.** Called by the scheduler once per hour (WP-03) and by `vshm prune`. Loop in batches of 500 until 0 rows affected, one tx per batch:
`DELETE FROM observations WHERE id IN (SELECT id FROM observations WHERE completed_at < ? LIMIT 500)` (cutoff now-90d), and the same pattern for `outbox WHERE status != 'pending' AND finished_at < ?` (cutoff now-30d). Then `PRAGMA wal_checkpoint(TRUNCATE)` once (ignore its result). Incidents and report dedupe rows are never deleted (report rows older than 30d keep their `dedupe_key` because they are deleted only when `status != 'pending'`… **correction:** report rows must keep the key indefinitely, so the outbox delete adds `AND kind = 'event'`; delivered report rows are small and stay). `PruneResult{Observations, OutboxRows int64}`.

**Snapshot / Status.** Single read tx. `Snapshot`: all incident rows + latest observation per check (`SELECT … GROUP BY check_id HAVING MAX(completed_at)`), plus 24h success/failure counts per check. `StoreStatus`: DB file size (`os.Stat`), counts of outbox rows per status, counts of observations, schema version, lock path. Counting `expired` vs `dropped` vs `delivered` rows replaces the former durable discard counters.

**Errors.** Any SQLite error (including `SQLITE_FULL` / `ENOSPC`) is wrapped with the operation name and returned. The store does **not** probe free space, does not retry, does not evict on error, does not VACUUM, never deletes files. Callers log a sanitized message and continue; nothing is ever claimed committed unless `tx` returned nil. No SQL text or row content in logs.

## 7 Fixed decisions (do not ask the user)
Schema above is final for v1. Lock = flock. Batch size 500. Default caps 90d/30d/24h/1000. Capacity handling = bounded pending queue + time pruning only; disk-full is an operator problem surfaced through `vshm status` and journal logs. Matrix crypto-store locking belongs to WP-12.

## 8 Open questions
None. Implement as written; propose changes via a separate note if a WP-00 type does not fit.

## 9 Acceptance tests (`store_test.go`, temp dir, real SQLite, fake clock)
1. Open creates schema, lock file 0600, DB 0600; second `Open` on same dir returns `ErrAlreadyRunning`; after `Close` it succeeds.
2. RecordObservationAndIncident is atomic: inject a failing OutboxEvent (e.g. duplicate event_id) and assert neither observation nor incident row exists.
3. EnqueueReport: same key twice → `true` then `false`, one row.
4. Pending: rows older than 24h come back as `expired` and are not returned; ordering is by id; `next_attempt_at` in the future is skipped.
5. MarkDelivery transitions and attempt counting.
6. Queue bound: insert MaxPending+5 events → exactly MaxPending pending, 5 oldest `dropped` with reason `queue_full`.
7. Prune: observations at 91d deleted, 89d kept; delivered events at 31d deleted; incidents and report rows kept at any age; returns correct counts.
8. Reopen after Close: all rows persist.
9. Status counts per outbox status are correct.

## 10 Definition of done
Common DoD from `overview.md`. Runtime validation: `go test ./internal/storage/`, `vshm status` against a temp state dir, and a second `vshm run` refused while the first holds the lock. Never use live `/var/lib/vshm`.
