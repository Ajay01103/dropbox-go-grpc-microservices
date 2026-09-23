package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

const (
	BlockDecrementClaimed        = "CLAIMED"
	BlockDecrementCounterApplied = "COUNTER_APPLIED"
	BlockDecrementComplete       = "COMPLETE"
	BlockDecrementFailed         = "FAILED"
)

type BlockDecrementOperation struct {
	JobID             string
	Occurrence        int
	BlockHash         string
	Status            string
	NewRefCount       int64
	BecameGCCandidate bool
	Error             string
	ClaimedAt         time.Time
	UpdatedAt         time.Time
}

type BlockDecrementClaim struct {
	Operation BlockDecrementOperation
	Claimed   bool
	WasReplay bool
}

// isTerminal returns true when a block decrement operation has reached a state
// that requires no further work — the caller should use the recorded result
// and ACK the message.
func isTerminal(status string) bool {
	return status == BlockDecrementCounterApplied ||
		status == BlockDecrementComplete ||
		status == BlockDecrementFailed
}

// ClaimBlockDecrement atomically claims a new operation row, or resumes work
// on a stale one.
//
// Return semantics:
//   - Claimed=true  → this caller now owns the operation and must do the work.
//   - Claimed=false, WasReplay=true, terminal status → already done; ACK.
//   - Claimed=false, WasReplay=true, CLAIMED status  → held by another delivery;
//     NAK with delay equal to the remaining stale window.
func (r *BlockRepo) ClaimBlockDecrement(ctx context.Context, jobID string, occurrence int, blockHash string, staleAfter time.Duration) (BlockDecrementClaim, error) {
	jobUUID, err := gocql.ParseUUID(jobID)
	if err != nil {
		return BlockDecrementClaim{}, fmt.Errorf("parse decrement job id: %w", err)
	}
	if occurrence < 0 || blockHash == "" {
		return BlockDecrementClaim{}, fmt.Errorf("occurrence and block hash are required")
	}

	now := time.Now().UTC()

	// --- fast path: insert a brand-new claim ---
	casMap := make(map[string]interface{})
	applied, err := r.session.Query(`
		INSERT INTO block_decrement_operations
		(job_id, occurrence, block_hash, status, new_ref_count, became_gc_candidate, error, claimed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`,
		jobUUID, occurrence, blockHash, BlockDecrementClaimed, int64(0), false, "", now, now,
	).WithContext(ctx).MapScanCAS(casMap)
	if err != nil {
		return BlockDecrementClaim{}, fmt.Errorf("claim decrement operation: %w", err)
	}
	if applied {
		return BlockDecrementClaim{Operation: BlockDecrementOperation{
			JobID:      jobID,
			Occurrence: occurrence,
			BlockHash:  blockHash,
			Status:     BlockDecrementClaimed,
			ClaimedAt:  now,
			UpdatedAt:  now,
		}, Claimed: true}, nil
	}

	// --- row already exists: read it ---
	var operation BlockDecrementOperation
	err = r.session.Query(`
		SELECT block_hash, status, new_ref_count, became_gc_candidate, error, claimed_at, updated_at
		FROM block_decrement_operations WHERE job_id = ? AND occurrence = ?`,
		jobUUID, occurrence).
		WithContext(ctx).Scan(
		&operation.BlockHash, &operation.Status, &operation.NewRefCount,
		&operation.BecameGCCandidate, &operation.Error, &operation.ClaimedAt, &operation.UpdatedAt)
	if err != nil {
		return BlockDecrementClaim{}, fmt.Errorf("read decrement operation: %w", err)
	}
	operation.JobID, operation.Occurrence = jobID, occurrence

	// Terminal states need no further work — return the recorded result.
	if isTerminal(operation.Status) {
		return BlockDecrementClaim{Operation: operation, WasReplay: true}, nil
	}

	// Row is CLAIMED. If it has gone stale, try a conditional take-over.
	if operation.Status == BlockDecrementClaimed && time.Since(operation.UpdatedAt) >= staleAfter {
		takeoverMap := make(map[string]interface{})
		tookOver, err := r.session.Query(
			`UPDATE block_decrement_operations SET claimed_at = ?, updated_at = ? WHERE job_id = ? AND occurrence = ? IF updated_at = ?`,
			now, now, jobUUID, occurrence, operation.UpdatedAt).
			WithContext(ctx).MapScanCAS(takeoverMap)
		if err != nil {
			return BlockDecrementClaim{}, fmt.Errorf("resume stale decrement operation: %w", err)
		}
		if tookOver {
			operation.ClaimedAt, operation.UpdatedAt = now, now
			return BlockDecrementClaim{Operation: operation, Claimed: true, WasReplay: true}, nil
		}
		// Another worker beat us to the take-over — re-read to get fresh state.
		err = r.session.Query(`
			SELECT block_hash, status, new_ref_count, became_gc_candidate, error, claimed_at, updated_at
			FROM block_decrement_operations WHERE job_id = ? AND occurrence = ?`,
			jobUUID, occurrence).
			WithContext(ctx).Scan(
			&operation.BlockHash, &operation.Status, &operation.NewRefCount,
			&operation.BecameGCCandidate, &operation.Error, &operation.ClaimedAt, &operation.UpdatedAt)
		if err != nil {
			return BlockDecrementClaim{}, fmt.Errorf("re-read decrement operation after takeover race: %w", err)
		}
		operation.JobID, operation.Occurrence = jobID, occurrence
		if isTerminal(operation.Status) {
			return BlockDecrementClaim{Operation: operation, WasReplay: true}, nil
		}
	}

	// Row is CLAIMED and not yet stale — caller must back off.
	return BlockDecrementClaim{Operation: operation, WasReplay: true}, nil
}

func (r *BlockRepo) UpdateBlockDecrement(ctx context.Context, operation BlockDecrementOperation) error {
	jobID, err := gocql.ParseUUID(operation.JobID)
	if err != nil {
		return fmt.Errorf("parse decrement job id: %w", err)
	}
	now := time.Now().UTC()
	if err := r.session.Query(
		`UPDATE block_decrement_operations SET status = ?, new_ref_count = ?, became_gc_candidate = ?, error = ?, updated_at = ? WHERE job_id = ? AND occurrence = ?`,
		operation.Status, operation.NewRefCount, operation.BecameGCCandidate, operation.Error, now, jobID, operation.Occurrence).
		WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update decrement operation: %w", err)
	}
	return nil
}
