package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// GCCandidate represents a block that has been zeroed and is awaiting deletion.
type GCCandidate struct {
	BlockHash  string
	ZeroedAt   time.Time
	EligibleAt time.Time
}

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

// ListEligibleGCCandidates returns up to limit blocks whose eligible_at is on
// or before now and which are therefore ready for physical deletion.
//
// block_gc_candidates has block_hash as its sole partition key so there is no
// way to push the eligible_at filter down to ScyllaDB without ALLOW FILTERING
// (which can be arbitrarily slow) or a secondary index. Instead we do a
// full-table scan and discard ineligible rows in Go. The table is intentionally
// kept small — rows are inserted only when a block's ref_count hits zero and
// are deleted immediately after the block is physically removed — so this scan
// is cheap in practice.
func (r *BlockRepo) ListEligibleGCCandidates(ctx context.Context, now time.Time, limit int) ([]GCCandidate, error) {
	iter := r.session.Query(
		`SELECT block_hash, zeroed_at, eligible_at FROM block_gc_candidates`,
	).WithContext(ctx).Iter()
	defer iter.Close()

	candidates := make([]GCCandidate, 0, limit)
	var c GCCandidate
	for iter.Scan(&c.BlockHash, &c.ZeroedAt, &c.EligibleAt) {
		if !c.EligibleAt.After(now) {
			candidates = append(candidates, GCCandidate{
				BlockHash:  c.BlockHash,
				ZeroedAt:   c.ZeroedAt,
				EligibleAt: c.EligibleAt,
			})
			if len(candidates) >= limit {
				break
			}
		}
	}
	if err := iter.Close(); err != nil && err != gocql.ErrNotFound {
		return nil, fmt.Errorf("scan gc candidates: %w", err)
	}
	return candidates, nil
}

// DeleteBlockRecord removes the block's metadata row from the blocks table.
// It is called after the physical S3/local object has been deleted.
func (r *BlockRepo) DeleteBlockRecord(ctx context.Context, hash string) error {
	if hash == "" {
		return fmt.Errorf("block hash is required")
	}
	return r.session.Query(`DELETE FROM blocks WHERE block_hash = ?`, hash).WithContext(ctx).Exec()
}

// HasInFlightDecrement returns true only if there is a decrement operation for
// this block that is currently CLAIMED and whose claim has not yet gone stale.
//
// Historical rows (COUNTER_APPLIED, COMPLETE, FAILED) are never deleted from
// block_decrement_operations_by_hash, so we cannot treat any non-terminal
// status as in-flight — we must check specifically for an active CLAIMED row.
// A row is only considered actively claimed when its updated_at is within the
// last staleAfter window; beyond that the decrement worker will take it over or
// it will be redelivered, and it is safe to proceed with GC.
func (r *BlockRepo) HasInFlightDecrement(ctx context.Context, hash string, staleAfter time.Duration) (bool, error) {
	iter := r.session.Query(
		`SELECT status, updated_at FROM block_decrement_operations_by_hash WHERE block_hash = ?`,
		hash,
	).WithContext(ctx).Iter()
	defer iter.Close()

	var status string
	var updatedAt time.Time
	for iter.Scan(&status, &updatedAt) {
		if status == BlockDecrementClaimed && time.Since(updatedAt) < staleAfter {
			if err := iter.Close(); err != nil && err != gocql.ErrNotFound {
				return false, fmt.Errorf("scan block decrement operations: %w", err)
			}
			return true, nil
		}
	}
	if err := iter.Close(); err != nil && err != gocql.ErrNotFound {
		return false, fmt.Errorf("scan block decrement operations: %w", err)
	}
	return false, nil
}
