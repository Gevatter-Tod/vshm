package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Snapshot returns all incident rows, the latest observation per check with 24h
// success/failure counts, and the time of the latest enqueued report.
func (s *SQLiteStore) Snapshot(ctx context.Context, now time.Time) (model.Snapshot, error) {
	snap := model.Snapshot{Time: now}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if snap.Incidents, err = readIncidents(ctx, tx); err != nil {
			return err
		}
		if snap.Checks, err = readChecks(ctx, tx, now); err != nil {
			return err
		}
		var last sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT MAX(enqueued_at) FROM outbox WHERE kind = 'report'`).Scan(&last); err != nil {
			return fmt.Errorf("read last report: %w", err)
		}
		snap.LastReport, err = parseTime(last.String)
		return err
	})
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("snapshot: %w", err)
	}
	return snap, nil
}

func readIncidents(ctx context.Context, tx *sql.Tx) ([]model.IncidentSummary, error) {
	rows, err := tx.QueryContext(ctx, `SELECT check_id, state, since, last_change, last_reason FROM incidents ORDER BY check_id`)
	if err != nil {
		return nil, fmt.Errorf("query incidents: %w", err)
	}
	defer rows.Close()
	var out []model.IncidentSummary
	for rows.Next() {
		var checkID, state, since, lastChange, reason string
		if err := rows.Scan(&checkID, &state, &since, &lastChange, &reason); err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		sinceT, err := parseTime(since)
		if err != nil {
			return nil, fmt.Errorf("parse incident since: %w", err)
		}
		changeT, err := parseTime(lastChange)
		if err != nil {
			return nil, fmt.Errorf("parse incident last_change: %w", err)
		}
		inc := model.IncidentSummary{IncidentID: checkID, CheckID: checkID, Reason: reason}
		if state == "open" {
			inc.OpenedAt = sinceT
			inc.LastFailure = changeT
		} else {
			resolved := sinceT
			inc.ResolvedAt = &resolved
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

func readChecks(ctx context.Context, tx *sql.Tx, now time.Time) ([]model.CheckStatus, error) {
	// SQLite returns the bare columns of the row holding MAX(completed_at).
	rows, err := tx.QueryContext(ctx,
		`SELECT check_id, check_type, success, started_at, MAX(completed_at), duration_ms, reason_code, reason
		 FROM observations GROUP BY check_id ORDER BY check_id`)
	if err != nil {
		return nil, fmt.Errorf("query latest observations: %w", err)
	}
	var checks []model.CheckStatus
	index := map[string]int{}
	for rows.Next() {
		var c model.CheckStatus
		var success int
		var started, completed string
		var durMs int64
		if err := rows.Scan(&c.CheckID, &c.CheckType, &success, &started, &completed, &durMs, &c.ReasonCode, &c.Reason); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan observation: %w", err)
		}
		c.LastStatus = "failure"
		if success == 1 {
			c.LastStatus = "success"
		}
		c.LastDuration = time.Duration(durMs) * time.Millisecond
		if c.LastStarted, err = parseTime(started); err == nil {
			c.LastCompleted, err = parseTime(completed)
		}
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("parse observation time: %w", err)
		}
		index[c.CheckID] = len(checks)
		checks = append(checks, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx,
		`SELECT check_id, success, COUNT(*) FROM observations WHERE completed_at >= ? GROUP BY check_id, success`,
		formatTime(now.Add(-24*time.Hour)))
	if err != nil {
		return nil, fmt.Errorf("query 24h counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var checkID string
		var success int
		var n int64
		if err := rows.Scan(&checkID, &success, &n); err != nil {
			return nil, fmt.Errorf("scan 24h counts: %w", err)
		}
		i, ok := index[checkID]
		if !ok {
			continue
		}
		if success == 1 {
			checks[i].Successes24h = n
		} else {
			checks[i].Failures24h = n
		}
	}
	return checks, rows.Err()
}

// Status reports database size, row counts per outbox status, observation count,
// schema version and lock path.
func (s *SQLiteStore) Status(ctx context.Context, now time.Time) (model.StoreStatus, error) {
	st := model.StoreStatus{
		LockPath: s.lockPath,
		OutboxCounts: map[model.DeliveryState]int64{
			model.StatePending: 0, model.StateDelivered: 0, model.StateExpired: 0, stateDropped: 0,
		},
	}
	if info, err := os.Stat(s.path); err == nil {
		st.DBSizeBytes = info.Size()
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&st.SchemaVersion); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM observations`).Scan(&st.RecordsCount); err != nil {
			return fmt.Errorf("count observations: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT status, COUNT(*) FROM outbox GROUP BY status`)
		if err != nil {
			return fmt.Errorf("count outbox: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var status string
			var n int64
			if err := rows.Scan(&status, &n); err != nil {
				return fmt.Errorf("scan outbox count: %w", err)
			}
			st.OutboxCounts[model.DeliveryState(status)] = n
		}
		return rows.Err()
	})
	if err != nil {
		st.LastError = "status query failed"
		return st, fmt.Errorf("status: %w", err)
	}
	st.Healthy = true
	return st, nil
}
