package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

func (r *BlockRepo) MarkGCCandidate(ctx context.Context, hash string, gracePeriod time.Duration) error {
	if hash == "" {
		return fmt.Errorf("block hash is required")
	}
	now := time.Now().UTC()
	eligibleAt := now.Add(gracePeriod)
	return r.session.Query(`INSERT INTO block_gc_candidates (block_hash, zeroed_at, eligible_at) VALUES (?, ?, ?)`, hash, now, eligibleAt).WithContext(ctx).Exec()
}

func (r *BlockRepo) RemoveGCCandidate(ctx context.Context, hash string) error {
	return r.session.Query(`DELETE FROM block_gc_candidates WHERE block_hash = ?`, hash).WithContext(ctx).Exec()
}

func (r *BlockRepo) HasInFlightDecrement(ctx context.Context, hash string) (bool, error) {
	iter := r.session.Query(`SELECT status FROM block_decrement_operations_by_hash WHERE block_hash = ?`, hash).WithContext(ctx).Iter()
	defer iter.Close()
	var status string
	for iter.Scan(&status) {
		if status != BlockDecrementComplete && status != BlockDecrementFailed {
			return true, nil
		}
	}
	if err := iter.Close(); err != nil && err != gocql.ErrNotFound {
		return false, fmt.Errorf("scan block decrement operations: %w", err)
	}
	return false, nil
}
