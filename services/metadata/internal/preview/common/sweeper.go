package common

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

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/pkg/events"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
)

// lookback bounds how far back the sweeper scans the preview pending work
// list. A pending row only needs a window comfortably larger than StaleAfter
// plus sweep-interval jitter; the 7d TTL eventually cleans anything that
// slips past it (and its file row's status governs correctness, not this row).
const lookback = 2 * time.Hour

// SweeperConfig carries the sweeper knobs (all config-driven).
type SweeperConfig struct {
	Interval   time.Duration // THUMBNAIL_SWEEP_INTERVAL
	StaleAfter time.Duration // THUMBNAIL_SWEEP_STALE_AFTER
}

// Sweeper repairs lost FileStored publishes for ONE family: it scans the
// preview pending work list and re-publishes FileStored for files still in
// thumbnail_status='pending' past the stale threshold, on the subject derived
// from the AUTHORITATIVE file row's content_family (so a repaired event still
// lands on the correct family's subject even if the pending row's family
// column is stale).
//
// MsgId rule: the republish reuses the ORIGINAL MsgIDFileStored(file, version)
// — a FileStored republish is meant to be a no-op if the original secretly
// succeeded, so NATS dedup swallowing it is the desired outcome.
//
// One sweeper instance per family (§9 of the plan): a stuck video sweep pass
// must not delay image sweeping.
type Sweeper struct {
	repo   *repository.MetadataRepo
	conn   *nats.Conn
	js     jetstream.JetStream
	cfg    SweeperConfig
	log    *zap.Logger
	stop   chan struct{}
	family eventsv2.ContentFamily
	name   string
}

// NewSweeper builds one family's sweeper. Returns (nil, nil) when NATS is
// unconfigured (NATS-less dev mode: no sweeper).
func NewSweeper(url string, repo *repository.MetadataRepo, family eventsv2.ContentFamily, cfg SweeperConfig, log *zap.Logger) (*Sweeper, error) {
	if url == "" {
		return nil, nil
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
	return &Sweeper{
		repo: repo, conn: conn, cfg: cfg, log: log,
		stop: make(chan struct{}), family: family,
		name: "preview-sweeper-" + FamilyName(family),
	}, nil
}

// Start launches the sweep loop.
func (s *Sweeper) Start(ctx context.Context) error {
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
					s.log.Warn("preview sweeper pass failed",
						zap.String("family", FamilyName(s.family)), zap.Error(err))
				}
			}
		}
	}()
	return nil
}

// Stop halts the loop and closes the connection.
func (s *Sweeper) Stop() {
	close(s.stop)
	s.conn.Close()
}

// sweep performs one pass over the pending work list.
func (s *Sweeper) sweep(ctx context.Context) error {
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
			s.log.Warn("preview sweeper: row failed",
				zap.String("family", FamilyName(s.family)),
				zap.String("fileID", ref.FileID), zap.Error(err))
		}
	}
	return nil
}

// sweepOne republishes FileStored for one pending file — but only if the
// file's own family matches this sweeper's (the shared pending list carries
// all families; each family's sweeper re-drives only its own).
func (s *Sweeper) sweepOne(ctx context.Context, ref repository.ThumbnailPendingRef) error {
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
	// Defensive family match (§12.4): the pending row's family column is
	// advisory; the file row's content_family is authoritative. If they
	// disagree with THIS sweeper's family, leave the row for the right one.
	rowFamily := eventsv2.ContentFamily(file.ContentFamily)
	if rowFamily != s.family {
		if rowFamily == eventsv2.ContentFamily_CONTENT_FAMILY_UNSPECIFIED {
			// Pre-z19 row: classify-free repair would risk feeding the wrong
			// worker; leave it — it ages out with the row TTL.
			return nil
		}
		return nil
	}

	// Rebuild the event from the authoritative file row and republish with
	// the ORIGINAL MsgId (dedup makes a republish a no-op if the original
	// secretly succeeded). Block hashes are stored as hex strings in the file
	// row; the proto carries raw 32-byte digests.
	hashes := make([][]byte, 0, len(file.BlockHashList))
	for _, h := range file.BlockHashList {
		b, err := hex.DecodeString(h)
		if err != nil {
			return fmt.Errorf("decode stored block hash %q: %w", h, err)
		}
		hashes = append(hashes, b)
	}
	evt := &eventsv2.FileStored{
		FileId:        file.FileID,
		FileVersion:   int64(file.Version),
		OwnerId:       file.OwnerID,
		ContentType:   file.ContentType,
		SizeBytes:     file.SizeBytes,
		BlockHashes:   hashes,
		StoredAt:      timestamppb.New(file.CreatedAt),
		ContentFamily: s.family,
	}
	if err := natsx.PublishProto(ctx, s.js, events.SubjFileStoredForFamily(s.family),
		events.MsgIDFileStored(file.FileID, int64(file.Version)), evt); err != nil {
		return err
	}
	s.log.Info("preview sweeper republished FileStored",
		zap.String("family", FamilyName(s.family)),
		zap.String("fileID", file.FileID), zap.Int64("version", int64(file.Version)))
	return nil
}
