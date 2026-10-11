// Package preview wires the multi-format preview pipeline: one worker per
// content family (image/video/pdf), each with its own consumer config,
// generator, and sweeper, so the families never share a failure domain or
// resource envelope.
package preview

import (
	"context"
	"io"
	"time"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/image"
	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/pdf"
	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/video"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// Storage exposes the shared S3 plumbing for other services in this process
// (purge coordinator's ThumbnailDeleter, presigning for the RPC layer).
type Storage = common.Storage

// NewStorage builds the shared preview storage client.
func NewStorage(cfg common.S3Config) (*Storage, error) { return common.NewStorage(cfg) }

// S3Config re-exports the shared config type.
type S3Config = common.S3Config

// Presigner is what the metadata service needs to serve GetThumbnailURL.
type Presigner interface {
	PresignThumbnail(ctx context.Context, key string, expiry time.Duration) (string, error)
}

// Deleter is what the purge coordinator needs to remove preview objects.
type Deleter interface {
	DeleteThumbnail(ctx context.Context, fileID, key string) error
}

// closerFunc adapts a func to io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// StartAll boots every family's worker + sweeper and returns one closer for
// the whole pipeline. A family whose external binary is missing (ffmpeg /
// pdftoppm) or whose NATS connect fails is DEGRADED with a loud warning —
// the other families keep running; a missing preview tool must never take
// metadata boot down with it.
func StartAll(ctx context.Context, cfg config.Config, repo *repository.MetadataRepo, log *zap.Logger) (io.Closer, error) {
	if cfg.NATSURL == "" {
		log.Warn("NATS unconfigured: preview pipeline disabled")
		return closerFunc(func() error { return nil }), nil
	}

	storage, err := NewStorage(common.S3Config{
		Bucket:    cfg.S3Bucket,
		Region:    cfg.S3Region,
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
	})
	if err != nil {
		return nil, err
	}

	sweeperCfg := common.SweeperConfig{
		Interval:   cfg.ThumbnailSweepInterval,
		StaleAfter: cfg.ThumbnailSweepStaleAfter,
	}

	type familyStart struct {
		name  string
		start func(context.Context) (io.Closer, error)
	}

	starts := []familyStart{
		{name: "image", start: func(c context.Context) (io.Closer, error) {
			return startImage(c, cfg, storage, repo, log, sweeperCfg)
		}},
		{name: "video", start: func(c context.Context) (io.Closer, error) {
			return startVideo(c, cfg, storage, repo, log, sweeperCfg)
		}},
		{name: "pdf", start: func(c context.Context) (io.Closer, error) {
			return startPDF(c, cfg, storage, repo, log, sweeperCfg)
		}},
	}

	var closers []io.Closer
	for _, fs := range starts {
		closer, err := fs.start(ctx)
		if err != nil {
			log.Warn("preview family unavailable; other families continue",
				zap.String("family", fs.name), zap.Error(err))
			continue
		}
		if closer != nil {
			closers = append(closers, closer)
		}
	}

	return closerFunc(func() error {
		for _, c := range closers {
			_ = c.Close()
		}
		return nil
	}), nil
}

func startImage(ctx context.Context, cfg config.Config, storage *Storage, repo *repository.MetadataRepo, log *zap.Logger, sweeperCfg common.SweeperConfig) (io.Closer, error) {
	worker, err := image.New(cfg.NATSURL, storage, repo, log)
	if err != nil {
		return nil, err
	}
	if err := worker.Start(ctx); err != nil {
		worker.Close()
		return nil, err
	}
	log.Info("image preview worker started", zap.String("subject", "files.v2.stored.image"))
	var closer io.Closer = worker
	sweeper, err := common.NewSweeper(cfg.NATSURL, repo, eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE, sweeperCfg, log)
	if err != nil {
		log.Warn("image preview sweeper unavailable", zap.Error(err))
	} else if sweeper != nil {
		if err := sweeper.Start(ctx); err != nil {
			log.Warn("image preview sweeper failed to start", zap.Error(err))
		} else {
			log.Info("image preview sweeper started")
			prev := closer
			closer = closerFunc(func() error { _ = prev.Close(); sweeper.Stop(); return nil })
		}
	}
	return closer, nil
}

func startVideo(ctx context.Context, cfg config.Config, storage *Storage, repo *repository.MetadataRepo, log *zap.Logger, sweeperCfg common.SweeperConfig) (io.Closer, error) {
	worker, err := video.New(cfg.NATSURL, storage, repo, log)
	if err != nil {
		return nil, err
	}
	if err := worker.Start(ctx); err != nil {
		worker.Close()
		return nil, err
	}
	log.Info("video preview worker started", zap.String("subject", "files.v2.stored.video"))
	var closer io.Closer = worker
	sweeper, err := common.NewSweeper(cfg.NATSURL, repo, eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO, sweeperCfg, log)
	if err != nil {
		log.Warn("video preview sweeper unavailable", zap.Error(err))
	} else if sweeper != nil {
		if err := sweeper.Start(ctx); err != nil {
			log.Warn("video preview sweeper failed to start", zap.Error(err))
		} else {
			log.Info("video preview sweeper started")
			prev := closer
			closer = closerFunc(func() error { _ = prev.Close(); sweeper.Stop(); return nil })
		}
	}
	return closer, nil
}

func startPDF(ctx context.Context, cfg config.Config, storage *Storage, repo *repository.MetadataRepo, log *zap.Logger, sweeperCfg common.SweeperConfig) (io.Closer, error) {
	worker, err := pdf.New(cfg.NATSURL, storage, repo, log)
	if err != nil {
		return nil, err
	}
	if err := worker.Start(ctx); err != nil {
		worker.Close()
		return nil, err
	}
	log.Info("pdf preview worker started", zap.String("subject", "files.v2.stored.pdf"))
	var closer io.Closer = worker
	sweeper, err := common.NewSweeper(cfg.NATSURL, repo, eventsv2.ContentFamily_CONTENT_FAMILY_PDF, sweeperCfg, log)
	if err != nil {
		log.Warn("pdf preview sweeper unavailable", zap.Error(err))
	} else if sweeper != nil {
		if err := sweeper.Start(ctx); err != nil {
			log.Warn("pdf preview sweeper failed to start", zap.Error(err))
		} else {
			log.Info("pdf preview sweeper started")
			prev := closer
			closer = closerFunc(func() error { _ = prev.Close(); sweeper.Stop(); return nil })
		}
	}
	return closer, nil
}
