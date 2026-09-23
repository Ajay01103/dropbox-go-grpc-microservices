package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
	"github.com/Ajay01103/go-dropbox/upload/internal/storagegateway"
	"go.uber.org/zap"
)

// BlockGCWorker periodically drains the block_gc_candidates table: for each
// block whose grace period has elapsed it (1) verifies there are no actively
// in-flight decrements, (2) deletes the physical object from the storage
// backend, (3) removes the row from the blocks table, and (4) removes it from
// block_gc_candidates. Safe to run on multiple instances — all operations are
// idempotent.
type BlockGCWorker struct {
	repo      *repository.BlockRepo
	gateway   storagegateway.BlockGateway
	interval  time.Duration
	batchSize int
	logger    *zap.Logger
}

// NewBlockGCWorker creates a BlockGCWorker.
//   - interval:  how often to scan for eligible candidates (default 5m)
//   - batchSize: max candidates processed per tick (default 100)
func NewBlockGCWorker(
	repo *repository.BlockRepo,
	gateway storagegateway.BlockGateway,
	interval time.Duration,
	batchSize int,
	logger *zap.Logger,
) *BlockGCWorker {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	return &BlockGCWorker{
		repo:      repo,
		gateway:   gateway,
		interval:  interval,
		batchSize: batchSize,
		logger:    logger,
	}
}

// Start runs the GC loop in the background and returns immediately. It stops
// when ctx is cancelled.
func (w *BlockGCWorker) Start(ctx context.Context) {
	go w.loop(ctx)
}

func (w *BlockGCWorker) loop(ctx context.Context) {
	// Run once immediately on startup so blocks that became eligible while the
	// service was offline are cleaned up without waiting a full interval.
	w.runOnce(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *BlockGCWorker) runOnce(ctx context.Context) {
	candidates, err := w.repo.ListEligibleGCCandidates(ctx, time.Now().UTC(), w.batchSize)
	if err != nil {
		w.logger.Error("block gc: list eligible candidates", zap.Error(err))
		return
	}
	if len(candidates) == 0 {
		return
	}
	w.logger.Info("block gc: processing candidates", zap.Int("count", len(candidates)))

	deleted := 0
	skipped := 0
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		ok, err := w.processCandidate(ctx, c.BlockHash)
		if err != nil {
			w.logger.Warn("block gc: failed to process candidate",
				zap.String("blockHash", c.BlockHash), zap.Error(err))
		} else if ok {
			deleted++
		} else {
			skipped++
		}
	}
	w.logger.Info("block gc: tick complete",
		zap.Int("deleted", deleted),
		zap.Int("skipped", skipped),
	)
}

// processCandidate deletes one block from storage and the database.
// Returns (true, nil) when deleted, (false, nil) when intentionally skipped,
// and (false, err) on a transient failure.
//
// Safety is the FenceForDelete LWT, not the read pre-checks: a dedup upload
// can only re-reference the block before the fence applies, and once fenced
// (state='DELETING') IncrementRefCount refuses new references. A crash after
// fencing resumes on the next sweep because the candidate row still exists.
func (w *BlockGCWorker) processCandidate(ctx context.Context, hash string) (bool, error) {
	// Cheap pre-check to skip obviously re-referenced blocks without a CAS.
	block, err := w.repo.GetBlock(ctx, hash)
	if err != nil {
		if repository.IsBlockNotFound(err) {
			// Already physically deleted; clean up the stale candidate row.
			_ = w.repo.RemoveGCCandidate(ctx, hash)
			return true, nil
		}
		return false, err
	}
	if block.RefCount > 0 {
		w.logger.Info("block gc: block re-referenced after zeroing, removing from candidates",
			zap.String("blockHash", hash), zap.Int64("refCount", block.RefCount))
		return false, w.repo.RemoveGCCandidate(ctx, hash)
	}

	// Fence: atomically transition to DELETING. Failure means the block was
	// re-referenced (or already fenced) between the read and now.
	fenced, err := w.repo.FenceForDelete(ctx, hash)
	if err != nil {
		return false, fmt.Errorf("fence block: %w", err)
	}
	if !fenced {
		w.logger.Info("block gc: fence lost (re-referenced or already deleting), skipping",
			zap.String("blockHash", hash))
		return false, nil
	}

	w.logger.Info("block gc: deleting block",
		zap.String("blockHash", hash),
		zap.String("backend", block.StorageBackend))

	if err := w.gateway.DeleteBlock(ctx, hash); err != nil {
		return false, fmt.Errorf("delete block object: %w", err)
	}
	if err := w.repo.DeleteBlockRecord(ctx, hash); err != nil {
		return false, fmt.Errorf("delete block record: %w", err)
	}
	if err := w.repo.RemoveGCCandidate(ctx, hash); err != nil {
		return false, fmt.Errorf("remove gc candidate: %w", err)
	}
	return true, nil
}
