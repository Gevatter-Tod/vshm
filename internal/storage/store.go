package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
	_ "modernc.org/sqlite"
)

// LockFileName is the name of the single-instance lock file in the state directory.
const LockFileName = "vshm.lock"

// Clock is the time source used by the store.
type Clock = model.Clock

// RealClock is a Clock backed by the system time.
type RealClock struct{}

// Now returns the current system time.
func (RealClock) Now() time.Time { return time.Now() }

// NewTimer returns a timer backed by time.Timer.
func (RealClock) NewTimer(d time.Duration) model.Timer { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time        { return r.t.C }
func (r *realTimer) Stop() bool                 { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

// SQLiteStore implements model.Store on top of SQLite.
type SQLiteStore struct {
	mu       sync.Mutex
	db       *sql.DB
	lockFile *os.File
	path     string
	lockPath string
	opts     Options
	clock    Clock
}

var _ model.Store = (*SQLiteStore)(nil)

// Open acquires the state-directory lock, opens the database at path and creates the schema.
// The lock file is <dir(path)>/vshm.lock. ErrAlreadyRunning is returned if another process holds it.
func Open(ctx context.Context, path string, clock Clock, opts Options) (*SQLiteStore, error) {
	if clock == nil {
		clock = RealClock{}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("open storage: create state dir: %w", err)
	}

	lockPath := filepath.Join(dir, LockFileName)
	lockFile, err := acquireLock(lockPath)
	if err != nil {
		return nil, err
	}

	s := &SQLiteStore{lockFile: lockFile, path: path, lockPath: lockPath, opts: opts.WithDefaults(), clock: clock}
	if err := s.init(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func acquireLock(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open storage: open lock file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("open storage: chmod lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("open storage: acquire lock: %w", err)
	}
	return f, nil
}

func (s *SQLiteStore) init(ctx context.Context) error {
	dsn := "file:" + s.path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open storage: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s.db = db

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("open storage: connect: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("open storage: chmod database: %w", err)
	}

	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("open storage: create schema: %w", err)
		}
		var version string
		err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('schema_version', ?)`, schemaVersion); err != nil {
				return fmt.Errorf("open storage: write schema version: %w", err)
			}
		case err != nil:
			return fmt.Errorf("open storage: read schema version: %w", err)
		case version != schemaVersion:
			return fmt.Errorf("open storage: unsupported schema version %q (want %q)", version, schemaVersion)
		}
		return nil
	})
}

// Close closes the database and releases the process lock.
func (s *SQLiteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []error
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close storage: database: %w", err))
		}
		s.db = nil
	}
	if s.lockFile != nil {
		// Closing the descriptor releases the flock.
		if err := s.lockFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close storage: lock file: %w", err))
		}
		s.lockFile = nil
	}
	return errors.Join(errs...)
}

// tx runs fn in a single transaction: commit on success, rollback on error.
func (s *SQLiteStore) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.db == nil {
		return errors.New("storage closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
