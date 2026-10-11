package content

import (
	"context"
	"fmt"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"go.uber.org/zap"
)

// StitcherConfig tunes the Tier B sweep.
type StitcherConfig struct {
	Enabled         bool
	Interval        time.Duration
	BatchSize       int
	MinAge          time.Duration
	StaleClaimAfter time.Duration
	// MaxAttempts bounds retries per file so a permanently ineligible file
	// (e.g. legacy 4 MiB blocks, below the multipart part minimum) stops
	// costing an S3 round-trip on every sweep. Zero means the default.
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
		// Give up after MaxAttempts: a file whose block layout can never be
		// stitched (legacy sub-5 MiB blocks) must not be retried forever. It
		// keeps working on Tier A live reconstruction.
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
		key, etag, stitchErr := w.stitch.Stitch(ctx, fileID, manifest.Blocks, 5<<20)
		if stitchErr != nil {
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
	key, etag, err := w.stitch.Stitch(ctx, fileID, manifest.Blocks, 5<<20)
	if err != nil {
		_ = w.repo.MarkFailed(ctx, fileID, now)
		return fmt.Errorf("stitch: %w", err)
	}
	if err := w.repo.MarkReady(ctx, fileID, key, etag, manifest.TotalSize, now); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	return nil
}
