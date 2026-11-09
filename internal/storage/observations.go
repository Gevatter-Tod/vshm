package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// RecordObservation stores one check result.
func (s *SQLiteStore) RecordObservation(ctx context.Context, r model.CheckResult) error {
	if err := s.tx(ctx, func(tx *sql.Tx) error { return insertObservation(ctx, tx, r) }); err != nil {
		return fmt.Errorf("record observation: %w", err)
	}
	return nil
}

// RecordObservationAndIncident atomically stores the observation, the incident row and the outbox events.
func (s *SQLiteStore) RecordObservationAndIncident(ctx context.Context, r model.CheckResult, m model.IncidentMutation, events []model.OutboxEvent) error {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := insertObservation(ctx, tx, r); err != nil {
			return err
		}
		if err := upsertIncident(ctx, tx, r, m); err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		now := s.clock.Now()
		for _, ev := range events {
			if err := insertEvent(ctx, tx, ev, now); err != nil {
				return err
			}
		}
		return s.boundQueue(ctx, tx, now)
	})
	if err != nil {
		return fmt.Errorf("record observation and incident: %w", err)
	}
	return nil
}

func insertObservation(ctx context.Context, tx *sql.Tx, r model.CheckResult) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO observations (check_id, check_type, success, started_at, completed_at, duration_ms, reason_code, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.CheckID, r.CheckType, boolToInt(r.Success), formatTime(r.StartedAt), formatTime(r.CompletedAt),
		r.Duration.Milliseconds(), r.ReasonCode, r.Reason)
	if err != nil {
		return fmt.Errorf("insert observation: %w", err)
	}
	return nil
}

// upsertIncident replaces the per-check incident row. `since` is kept while the
// state is unchanged; consecutive_failures is derived from the previous row.
func upsertIncident(ctx context.Context, tx *sql.Tx, r model.CheckResult, m model.IncidentMutation) error {
	checkID := m.CheckID
	if checkID == "" {
		checkID = r.CheckID
	}
	state := "closed"
	changedAt := r.CompletedAt
	if m.Open {
		state = "open"
		if !m.LastFailure.IsZero() {
			changedAt = m.LastFailure
		}
	} else if m.ResolvedAt != nil {
		changedAt = *m.ResolvedAt
	}
	reason := m.Reason
	if reason == "" {
		reason = r.Reason
	}

	var prevState, prevSince string
	var prevFailures int64
	err := tx.QueryRowContext(ctx, `SELECT state, since, consecutive_failures FROM incidents WHERE check_id = ?`, checkID).
		Scan(&prevState, &prevSince, &prevFailures)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read incident: %w", err)
	}

	since := formatTime(changedAt)
	if prevState == state && prevSince != "" {
		since = prevSince
	}
	failures := int64(0)
	if !r.Success {
		failures = prevFailures + 1
	}

	_, err = tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO incidents (check_id, state, consecutive_failures, since, last_change, last_reason)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		checkID, state, failures, since, formatTime(r.CompletedAt), reason)
	if err != nil {
		return fmt.Errorf("write incident: %w", err)
	}
	return nil
}
