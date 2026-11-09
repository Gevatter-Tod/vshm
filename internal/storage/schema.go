// Package storage provides the vshm SQLite store: schema, transactions,
// a single-instance process lock, a bounded pending queue and time-based pruning.
package storage

const schemaVersion = "1"

// schema is the fixed v1 schema. It is applied with CREATE ... IF NOT EXISTS on every Open.
const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
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
  id INTEGER PRIMARY KEY,
  event_id TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,
  dedupe_key TEXT UNIQUE,
  body TEXT NOT NULL,
  enqueued_at TEXT NOT NULL, status TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT, finished_at TEXT, last_reason TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(status, next_attempt_at);
`
