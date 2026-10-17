package content

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"go.uber.org/zap"
)

// minStitchPartSize is S3's multipart minimum for every part but the last.
// Server-side assembly (UploadPartCopy) cannot merge blocks into parts, so a
// file whose non-final blocks are smaller than this can never be consolidated.
const minStitchPartSize = 5 << 20

// firstIneligibleBlock returns the index of the first non-final block below the
// multipart part minimum, or -1 when every block qualifies and the file can be
// stitched.
//
// The final block is exempt, which is why a small single-block file stitches
// fine while the same bytes split across two blocks cannot.
func firstIneligibleBlock(blocks []repository.ManifestBlock, minPartSize int64) int {
	for i, block := range blocks {
		if i == len(blocks)-1 {
			return -1
		}
		if block.SizeBytes < minPartSize {
			return i
		}
	}
	return -1
}

// StitcherConfig tunes the Tier B sweep.
type StitcherConfig struct {
	Enabled         bool
	Interval        time.Duration
	BatchSize       int
	MinAge          time.Duration
	StaleClaimAfter time.Duration
	// MaxAttempts bounds retries per file so a transient S3 failure cannot be
	// retried forever. Layouts that can never be consolidated are parked
	// outright (see StitchStateSkipped) and never reach this bound.
	// Zero means the default.
	MaxAttempts int
}

// DefaultStitcherConfig is the safe default sweep cadence.
func DefaultStitcherConfig() StitcherConfig {
	return StitcherConfig{
		Enabled:         true,
		Interval:        time.Minute,
		BatchSize:       5,
		MinAge:          30 * time.Second,
		StaleClaimAfter: 10 * time.Minute,
		MaxAttempts:     5,
	}
}

// StitchWorker periodically claims non-ready files from stitched_objects and
// performs the server-side UploadPartCopy assembly. Files younger than
// MinAge are skipped (a fresh upload's first download is its most likely
// download; let Tier A serve it while the write path settles) and stale
// claims of crashed workers are re-claimed.
type StitchWorker struct {
	stitch   *Stitcher
	repo     *repository.StitchRepo
	manifest *repository.MetadataRepo
	cfg      StitcherConfig
	logger   *zap.Logger
}

// NewStitchWorker wires the Tier B worker.
func NewStitchWorker(stitch *Stitcher, repo *repository.StitchRepo, manifest *repository.MetadataRepo, cfg StitcherConfig, logger *zap.Logger) *StitchWorker {
	return &StitchWorker{stitch: stitch, repo: repo, manifest: manifest, cfg: cfg, logger: logger}
}

// Start launches the sweep loop; it stops when ctx is canceled.
func (w *StitchWorker) Start(ctx context.Context) {
	if !w.cfg.Enabled {
		w.logger.Info("stitcher disabled; every download rides live reconstruction")
		return
	}
	go func() {
		ticker := time.NewTicker(w.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.sweepOnce(ctx); err != nil {
					w.logger.Warn("stitch sweep errored", zap.Error(err))
				}
			}
		}
	}()
}

// sweepOnce claims up to BatchSize candidates and stitches them. Candidacy
// is derived from the stitched_objects scan (a 'stitching'/'failed' row is a
// candidate) but MintForStitch is the atomic single-winner claim.
func (w *StitchWorker) sweepOnce(ctx context.Context) error {
	records, err := w.repo.ListPending(ctx, w.cfg.BatchSize)
	if err != nil {
		return fmt.Errorf("list pending stitch candidates: %w", err)
	}
	now := time.Now().UTC()
	claimed := 0
	maxAttempts := w.cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	for _, rec := range records {
		// Freshly enqueued/claimed rows wait out MinAge so a file that is still
		// being written or has just been uploaded is served by Tier A while the
		// write path settles; failed rows retry immediately.
		fresh := rec.State == repository.StitchStatePending || rec.State == repository.StitchStateStitching
		if fresh && now.Sub(rec.ClaimedAt) < w.cfg.MinAge {
			continue
		}
		// Give up after MaxAttempts: a transient failure must not cost an S3
		// round-trip on every sweep forever. The file keeps working on Tier A
		// live reconstruction.
		if rec.State == repository.StitchStateFailed && rec.Attempts >= maxAttempts {
			continue
		}
		fileID := rec.FileID
		_, ok, claimErr := w.repo.ClaimForStitch(ctx, fileID, w.cfg.StaleClaimAfter, now)
		if claimErr != nil || !ok {
			continue
		}
		claimed++
		manifest, manifestErr := w.manifest.GetFileManifest(ctx, fileID)
		if manifestErr != nil {
			_ = w.repo.MarkFailed(ctx, fileID, now)
			w.logger.Warn("stitch skipped: manifest unavailable",
				zap.String("fileID", fileID), zap.Error(manifestErr))
			continue
		}
		// Eligibility first: a layout S3 cannot assemble is parked once instead
		// of being handed to the multipart API five times per content change.
		if w.parkIfIneligible(ctx, fileID, manifest, now) {
			continue
		}
		key, etag, stitchErr := w.stitch.Stitch(ctx, fileID, manifest.Blocks, minStitchPartSize)
		if stitchErr != nil {
			if errors.Is(stitchErr, ErrStitchNotEligible) {
				// The manifest changed under us (a commit landed between the check
				// and the stitch). Park it the same way; the next content change
				// re-enqueues.
				_ = w.repo.MarkSkipped(ctx, fileID, now)
				w.logger.Info("file parked on Tier A: layout cannot be consolidated server-side",
					zap.String("fileID", fileID), zap.Error(stitchErr))
				continue
			}
			_ = w.repo.MarkFailed(ctx, fileID, now)
			w.logger.Warn("stitch failed",
				zap.String("fileID", fileID),
				zap.Int("attempt", rec.Attempts+1),
				zap.Error(stitchErr))
			continue
		}
		if markErr := w.repo.MarkReady(ctx, fileID, key, etag, manifest.TotalSize, now); markErr != nil {
			w.logger.Warn("stitch completed but row not marked ready",
				zap.String("fileID", fileID), zap.Error(markErr))
			continue
		}
		w.logger.Info("file consolidated (Tier B ready)",
			zap.String("fileID", fileID),
			zap.Int64("totalSize", manifest.TotalSize),
			zap.Int("blockCount", len(manifest.Blocks)))
	}
	if claimed > 0 {
		w.logger.Debug("stitch sweep claimed", zap.Int("claimed", claimed))
	}
	return nil
}

// parkIfIneligible checks whether S3's multipart API could ever assemble this
// manifest and, when it could not, parks the file on Tier A (StitchStateSkipped)
// so no sweep or save ever retries it: the sweeper stops reading the row, and
// only a committed content change re-enqueues the file. Reports whether the
// file was parked, in which case the caller must not stitch it.
func (w *StitchWorker) parkIfIneligible(ctx context.Context, fileID string, manifest repository.FileManifestRow, now time.Time) bool {
	idx := firstIneligibleBlock(manifest.Blocks, minStitchPartSize)
	if idx < 0 {
		return false
	}
	if err := w.repo.MarkSkipped(ctx, fileID, now); err != nil {
		w.logger.Warn("mark stitch skipped failed", zap.String("fileID", fileID), zap.Error(err))
	}
	w.logger.Info("file parked on Tier A: layout cannot be consolidated server-side",
		zap.String("fileID", fileID),
		zap.Int("blockIndex", idx),
		zap.Int64("blockSizeBytes", manifest.Blocks[idx].SizeBytes),
		zap.Int64("minPartSize", minStitchPartSize))
	return true
}

// StitchFile stitches one file now. Exposed for tests and manual triggers.
func (w *StitchWorker) StitchFile(ctx context.Context, fileID string) error {
	now := time.Now().UTC()
	if _, ok, err := w.repo.ClaimForStitch(ctx, fileID, w.cfg.StaleClaimAfter, now); err != nil || !ok {
		if err != nil {
			return err
		}
		return nil // already ready or claimed
	}
	manifest, err := w.manifest.GetFileManifest(ctx, fileID)
	if err != nil {
		_ = w.repo.MarkFailed(ctx, fileID, now)
		return fmt.Errorf("load manifest: %w", err)
	}
	if w.parkIfIneligible(ctx, fileID, manifest, now) {
		return nil
	}
	key, etag, err := w.stitch.Stitch(ctx, fileID, manifest.Blocks, minStitchPartSize)
	if err != nil {
		if errors.Is(err, ErrStitchNotEligible) {
			_ = w.repo.MarkSkipped(ctx, fileID, now)
			w.logger.Info("file parked on Tier A: layout cannot be consolidated server-side",
				zap.String("fileID", fileID), zap.Error(err))
			return nil
		}
		_ = w.repo.MarkFailed(ctx, fileID, now)
		return fmt.Errorf("stitch: %w", err)
	}
	if err := w.repo.MarkReady(ctx, fileID, key, etag, manifest.TotalSize, now); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	return nil
}
