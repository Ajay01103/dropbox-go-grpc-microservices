package thumbnail

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/pkg/events"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
)

// lookback bounds how far back the sweeper scans thumbnail_pending buckets.
// A pending row only needs a window comfortably larger than StaleAfter plus
// sweep-interval jitter; the 7d TTL eventually cleans anything that slips
// past it (and its file row's status governs correctness, not this row).
const lookback = 2 * time.Hour

// ThumbnailSweeperConfig carries the sweeper knobs (all config-driven).
type ThumbnailSweeperConfig struct {
	Interval   time.Duration // THUMBNAIL_SWEEP_INTERVAL
	StaleAfter time.Duration // THUMBNAIL_SWEEP_STALE_AFTER
}

// ThumbnailSweeper repairs lost v2 FileStored publishes: it scans the
// thumbnail_pending work list and re-publishes FileStored for files still in
// thumbnail_status='pending' past the stale threshold.
//
// MsgId rule (deliberately DIFFERENT from the purge sweeper's REQUESTED
// re-drive): the republish reuses the ORIGINAL MsgIDFileStored(file, version).
// A FileStored republish is meant to be a no-op if the original secretly
// succeeded — same reasoning as a PENDING purge re-drive — so NATS dedup
// swallowing it is the desired outcome. The purge sweeper's REQUESTED
// re-drives are genuine loss-retries and therefore need fresh, attempt-
// suffixed MsgIds; this one doesn't.
type ThumbnailSweeper struct {
	repo *repository.MetadataRepo
	conn *nats.Conn
	js   jetstream.JetStream
	cfg  ThumbnailSweeperConfig
	log  *zap.Logger
	stop chan struct{}
}

func NewThumbnailSweeper(url string, repo *repository.MetadataRepo, cfg ThumbnailSweeperConfig, log *zap.Logger) (*ThumbnailSweeper, error) {
	if url == "" {
		return nil, nil // NATS-less dev mode: no sweeper
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 5 * time.Minute
	}
	conn, err := natsx.Connect(url)
	if err != nil {
		return nil, err
	}
	return &ThumbnailSweeper{
		repo: repo, conn: conn, cfg: cfg, log: log, stop: make(chan struct{}),
	}, nil
}

func (s *ThumbnailSweeper) Start(ctx context.Context) error {
	js, err := jetstream.New(s.conn)
	if err != nil {
		return err
	}
	s.js = js
	go func() {
		ticker := time.NewTicker(s.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-ticker.C:
				if err := s.sweep(ctx); err != nil {
					s.log.Warn("thumbnail sweeper pass failed", zap.Error(err))
				}
			}
		}
	}()
	return nil
}

func (s *ThumbnailSweeper) Stop() {
	close(s.stop)
	s.conn.Close()
}

// sweep performs one pass over the pending work list.
func (s *ThumbnailSweeper) sweep(ctx context.Context) error {
	now := time.Now().UTC()
	buckets := make([]string, 0, int(lookback/time.Hour)+1)
	for i := 0; i <= int(lookback/time.Hour); i++ {
		buckets = append(buckets, repository.ThumbnailBucket(now.Add(-time.Duration(i)*time.Hour)))
	}
	refs, err := s.repo.ListThumbnailPending(ctx, buckets)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if now.Sub(ref.CreatedAt) < s.cfg.StaleAfter {
			continue // too fresh; give the normal path time
		}
		if err := s.sweepOne(ctx, ref); err != nil {
			s.log.Warn("thumbnail sweeper: row failed",
				zap.String("fileID", ref.FileID), zap.Error(err))
		}
	}
	return nil
}

func (s *ThumbnailSweeper) sweepOne(ctx context.Context, ref repository.ThumbnailPendingRef) error {
	file, err := s.repo.GetFileByID(ctx, ref.OwnerID, ref.FileID)
	if err != nil {
		if strings.Contains(err.Error(), "file not found") {
			// File row is gone (e.g. purged while pending): clear the row.
			return s.repo.DeleteThumbnailPending(ctx, ref.Bucket, ref.FileID)
		}
		return err
	}
	if file.ThumbnailStatus != "pending" {
		// Generation already finished (this also self-heals the rare
		// hour-boundary bucket mismatch on the worker's delete).
		return s.repo.DeleteThumbnailPending(ctx, ref.Bucket, ref.FileID)
	}

	// Rebuild the event from the authoritative file row and republish with
	// the ORIGINAL MsgId (see the type comment for why no :r<attempt>).
	// Block hashes are stored as hex strings in the file row; the proto
	// carries raw 32-byte digests, so decode them here to match the
	// upload service's publish format.
	hashes := make([][]byte, 0, len(file.BlockHashList))
	for _, h := range file.BlockHashList {
		b, err := hex.DecodeString(h)
		if err != nil {
			return fmt.Errorf("decode stored block hash %q: %w", h, err)
		}
		hashes = append(hashes, b)
	}
	evt := &eventsv2.FileStored{
		FileId:      file.FileID,
		FileVersion: int64(file.Version),
		OwnerId:     file.OwnerID,
		ContentType: file.ContentType,
		SizeBytes:   file.SizeBytes,
		BlockHashes: hashes,
		StoredAt:    timestamppb.New(file.CreatedAt),
	}
	if err := natsx.PublishProto(ctx, s.js, events.SubjFileStored,
		events.MsgIDFileStored(file.FileID, int64(file.Version)), evt); err != nil {
		return err
	}
	s.log.Info("thumbnail sweeper republished FileStored",
		zap.String("fileID", file.FileID), zap.Int64("version", int64(file.Version)))
	return nil
}
