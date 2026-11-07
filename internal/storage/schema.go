// Package storage provides SQLite storage with migrations and process locking.
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 1
)

// Schema defines the database tables.
func init() {
	// Schema will be created by CreateDatabase
}

// CreateDatabase initializes the database with schema.
func CreateDatabase(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS schema_version (
		singleton INTEGER PRIMARY KEY CHECK(singleton=1),
		version INTEGER NOT NULL,
		applied_at_utc TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS check_state (
		host_name TEXT NOT NULL,
		check_id TEXT NOT NULL,
		check_type TEXT NOT NULL,
		last_status TEXT NOT NULL CHECK(last_status IN ('success','failure')),
		last_started_at_utc TEXT NOT NULL,
		last_completed_at_utc TEXT NOT NULL,
		last_duration_ns INTEGER NOT NULL,
		last_reason_code TEXT NOT NULL,
		last_reason TEXT NOT NULL,
		PRIMARY KEY(host_name, check_id)
	);

	CREATE TABLE IF NOT EXISTS incidents (
		incident_id TEXT PRIMARY KEY,
		host_name TEXT NOT NULL,
		check_id TEXT NOT NULL,
		opened_at_utc TEXT NOT NULL,
		last_failure_at_utc TEXT NOT NULL,
		last_reason_code TEXT NOT NULL,
		last_reason TEXT NOT NULL,
		resolved_at_utc TEXT
	);

	CREATE INDEX IF NOT EXISTS incidents_check_idx ON incidents(host_name, check_id, opened_at_utc);
	CREATE UNIQUE INDEX IF NOT EXISTS incidents_one_open_per_check_idx ON incidents(host_name, check_id) WHERE resolved_at_utc IS NULL;

	CREATE TABLE IF NOT EXISTS results_history (
		result_seq INTEGER PRIMARY KEY AUTOINCREMENT,
		host_name TEXT NOT NULL,
		check_id TEXT NOT NULL,
		check_type TEXT NOT NULL,
		success INTEGER NOT NULL CHECK(success IN (0,1)),
		started_at_utc TEXT NOT NULL,
		completed_at_utc TEXT NOT NULL,
		duration_ns INTEGER NOT NULL,
		reason_code TEXT NOT NULL,
		reason TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS results_history_completed_idx ON results_history(completed_at_utc);

	CREATE TABLE IF NOT EXISTS outbox (
		enqueue_seq INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL UNIQUE,
		host_name TEXT NOT NULL,
		event_kind TEXT NOT NULL,
		check_id TEXT,
		check_type TEXT,
		incident_id TEXT,
		local_report_date TEXT,
		enqueued_at_utc TEXT NOT NULL,
		next_attempt_at_utc TEXT NOT NULL,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		delivery_state TEXT NOT NULL CHECK(delivery_state IN ('pending','delivered','expired','evicted','ambiguous')),
		last_outcome TEXT NOT NULL DEFAULT '',
		delivered_at_utc TEXT,
		expires_at_utc TEXT,
		payload_text TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS outbox_pending_order_idx ON outbox(delivery_state, next_attempt_at_utc, enqueue_seq);

	CREATE TABLE IF NOT EXISTS report_dates (
		local_date TEXT PRIMARY KEY,
		event_id TEXT NOT NULL UNIQUE,
		created_at_utc TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS discard_counters (
		reason TEXT PRIMARY KEY,
		discarded_count INTEGER NOT NULL CHECK(discarded_count>=0),
		updated_at_utc TEXT NOT NULL
	);
	`

	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	// Set schema version
	_, err = db.Exec("INSERT OR REPLACE INTO schema_version (singleton, version, applied_at_utc) VALUES (1, ?, ?)", schemaVersion, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("failed to set schema version: %w", err)
	}

	return nil
}

// MigrateToVersion upgrades the database to the target version.
func MigrateToVersion(db *sql.DB, targetVersion int) error {
	if targetVersion < schemaVersion {
		return fmt.Errorf("target version %d is less than current version %d", targetVersion, schemaVersion)
	}

	// For now, we only have v1
	if targetVersion == schemaVersion {
		return nil
	}

	// Future migrations would go here
	return nil
}

// Open opens the database at the given path.
func Open(path string) (*sql.DB, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return db, nil
}

// ProcessLock provides a simple process lock using a file.
type ProcessLock struct {
	path   string
	file   *os.File
	mu     sync.Mutex
}

// NewProcessLock creates a new process lock.
func NewProcessLock(path string) (*ProcessLock, error) {
	return &ProcessLock{path: path}, nil
}

// Acquire attempts to acquire the lock.
func (l *ProcessLock) Acquire() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	path := l.path + ".lock"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}

	l.file = file
	return nil
}

// Release releases the lock.
func (l *ProcessLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		l.file.Close()
		l.file = nil
	}

	path := l.path + ".lock"
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to remove lock file: %w", err)
	}

	return nil
}

// IsHeld checks if the lock is held.
func (l *ProcessLock) IsHeld() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.file != nil
}
