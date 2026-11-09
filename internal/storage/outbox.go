package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// stateDropped is the outbox status for rows dropped by the queue bound or given up by the sender.
const stateDropped model.DeliveryState = "dropped"

func insertEvent(ctx context.Context, tx *sql.Tx, ev model.OutboxEvent, now time.Time) error {
	enqueued := ev.EnqueuedAt
	if enqueued.IsZero() {
		enqueued = now
	}
	at := formatTime(enqueued)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO outbox (event_id, kind, body, enqueued_at, status, next_attempt_at)
		 VALUES (?, 'event', ?, ?, 'pending', ?)`,
		ev.ID, ev.Payload, at, at)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

// boundQueue drops the oldest pending rows so that at most MaxPending remain.
func (s *SQLiteStore) boundQueue(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE outbox SET status = 'dropped', last_reason = 'queue_full', finished_at = ?
		 WHERE status = 'pending' AND id NOT IN (
		   SELECT id FROM outbox WHERE status = 'pending' ORDER BY id DESC LIMIT ?)`,
		formatTime(now), s.opts.MaxPending)
	if err != nil {
		return fmt.Errorf("bound pending queue: %w", err)
	}
	return nil
}

// EnqueueReport inserts a report keyed by reportKey. inserted is false if the key already exists.
func (s *SQLiteStore) EnqueueReport(ctx context.Context, reportKey string, ev model.OutboxEvent) (bool, error) {
	var inserted bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.clock.Now()
		enqueued := ev.EnqueuedAt
		if enqueued.IsZero() {
			enqueued = now
		}
		at := formatTime(enqueued)
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO outbox (event_id, kind, dedupe_key, body, enqueued_at, status, next_attempt_at)
			 VALUES (?, 'report', ?, ?, ?, 'pending', ?)`,
			ev.ID, reportKey, ev.Payload, at, at)
		if err != nil {
			return fmt.Errorf("insert report: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("insert report: %w", err)
		}
		inserted = n == 1
		if !inserted {
			return nil
		}
		return s.boundQueue(ctx, tx, now)
	})
	if err != nil {
		return false, fmt.Errorf("enqueue report: %w", err)
	}
	return inserted, nil
}

// Pending expires pending rows older than PendingMaxAge and then returns up to limit
// due rows in insertion order. A limit <= 0 means no limit.
func (s *SQLiteStore) Pending(ctx context.Context, now time.Time, limit int) ([]model.OutboxEvent, error) {
	if limit <= 0 {
		limit = -1
	}
	var events []model.OutboxEvent
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE outbox SET status = 'expired', finished_at = ? WHERE status = 'pending' AND enqueued_at < ?`,
			formatTime(now), formatTime(now.Add(-s.opts.PendingMaxAge))); err != nil {
			return fmt.Errorf("expire pending: %w", err)
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT id, event_id, kind, dedupe_key, body, enqueued_at, next_attempt_at, attempts, last_reason
			 FROM outbox WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY id LIMIT ?`,
			formatTime(now), limit)
		if err != nil {
			return fmt.Errorf("query pending: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var ev model.OutboxEvent
			var dedupe, next sql.NullString
			var enqueued string
			if err := rows.Scan(&ev.Sequence, &ev.ID, &ev.Kind, &dedupe, &ev.Payload, &enqueued, &next, &ev.AttemptCount, &ev.LastOutcome); err != nil {
				return fmt.Errorf("scan pending: %w", err)
			}
			if ev.EnqueuedAt, err = parseTime(enqueued); err != nil {
				return fmt.Errorf("parse enqueued_at: %w", err)
			}
			if ev.NextAttemptAt, err = parseTime(next.String); err != nil {
				return fmt.Errorf("parse next_attempt_at: %w", err)
			}
			ev.LocalReport = dedupe.String
			ev.DeliveryState = model.StatePending
			ev.ExpiresAt = ev.EnqueuedAt.Add(s.opts.PendingMaxAge)
			events = append(events, ev)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("pending: %w", err)
	}
	return events, nil
}

// MarkDelivery records the outcome of one delivery attempt. Retry policy belongs to
// the caller, which passes nextAttemptAt for failed or ambiguous attempts.
//
//	DeliverySuccess            -> delivered
//	DeliveryFailure/Ambiguous  -> stays pending, attempts+1, next_attempt_at updated
//	DeliveryEvicted (gave up)  -> dropped
//	DeliveryExpired            -> expired
func (s *SQLiteStore) MarkDelivery(ctx context.Context, eventID string, out model.DeliveryOutcome, attemptedAt, nextAttemptAt time.Time, reason string) error {
	var query string
	var args []any
	at := formatTime(attemptedAt)
	switch out {
	case model.DeliverySuccess:
		query = `UPDATE outbox SET status = 'delivered', attempts = attempts + 1, finished_at = ?, last_reason = ? WHERE event_id = ?`
		args = []any{at, reason, eventID}
	case model.DeliveryFailure, model.DeliveryAmbiguous:
		query = `UPDATE outbox SET attempts = attempts + 1, next_attempt_at = ?, last_reason = ? WHERE event_id = ? AND status = 'pending'`
		args = []any{formatTime(nextAttemptAt), reason, eventID}
	case model.DeliveryEvicted:
		query = `UPDATE outbox SET status = 'dropped', attempts = attempts + 1, finished_at = ?, last_reason = ? WHERE event_id = ?`
		args = []any{at, reason, eventID}
	case model.DeliveryExpired:
		query = `UPDATE outbox SET status = 'expired', finished_at = ?, last_reason = ? WHERE event_id = ?`
		args = []any{at, reason, eventID}
	default:
		return fmt.Errorf("mark delivery: unknown outcome %q", out)
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("no matching outbox row")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mark delivery: %w", err)
	}
	return nil
}
