package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

const pruneBatch = 500

// Prune deletes observations older than ObservationRetention and finished event rows
// older than DeliveredRetention, in batches of 500 with one transaction per batch.
// Incidents and report rows (which carry the dedupe key) are never deleted.
func (s *SQLiteStore) Prune(ctx context.Context, now time.Time) (model.PruneResult, error) {
	var res model.PruneResult
	var err error

	res.Observations, err = s.pruneBatches(ctx,
		`DELETE FROM observations WHERE id IN (SELECT id FROM observations WHERE completed_at < ? LIMIT ?)`,
		formatTime(now.Add(-s.opts.ObservationRetention)))
	if err != nil {
		return res, fmt.Errorf("prune observations: %w", err)
	}

	res.OutboxRows, err = s.pruneBatches(ctx,
		`DELETE FROM outbox WHERE id IN (SELECT id FROM outbox
		   WHERE kind = 'event' AND status != 'pending' AND finished_at < ? LIMIT ?)`,
		formatTime(now.Add(-s.opts.DeliveredRetention)))
	if err != nil {
		return res, fmt.Errorf("prune outbox: %w", err)
	}

	// Checkpoint result is intentionally ignored.
	_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return res, nil
}

func (s *SQLiteStore) pruneBatches(ctx context.Context, query, cutoff string) (int64, error) {
	var total int64
	for {
		var n int64
		err := s.tx(ctx, func(tx *sql.Tx) error {
			r, err := tx.ExecContext(ctx, query, cutoff, pruneBatch)
			if err != nil {
				return err
			}
			n, err = r.RowsAffected()
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			return total, nil
		}
	}
}
