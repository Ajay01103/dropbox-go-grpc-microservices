package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"go.uber.org/zap"

	"github.com/dgraph-io/ristretto"
	"github.com/gocql/gocql"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	"github.com/Ajay01103/go-dropbox/metadata/db"
	"github.com/Ajay01103/go-dropbox/metadata/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/metadata/internal/content"
	"github.com/Ajay01103/go-dropbox/metadata/internal/preview"
	"github.com/Ajay01103/go-dropbox/metadata/internal/purge"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"github.com/Ajay01103/go-dropbox/metadata/server"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	"github.com/Ajay01103/go-dropbox/pkg/jwks"
	pkglogger "github.com/Ajay01103/go-dropbox/pkg/logger"
)

// corsMiddleware allows Next.js or other frontends to access Connect endpoints
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:3000")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
		// Range/If-* are preflight-triggering headers only in exotic forms
		// (multi-range); allowing them keeps a viewer that sends them working
		// instead of failing on a preflight it did not expect to need.
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, Connect-Protocol-Version, Authorization, Range, If-Range, If-None-Match")
		// Response headers a fetch-based viewer reads off a /content response.
		// Without this the browser hides Content-Range/Content-Length from JS,
		// so a PDF/DOCX viewer cannot size or seek the resource it is loading
		// (plain <img>/<video> tags still work, which is why this went unnoticed).
		w.Header().Set("Access-Control-Expose-Headers",
			"Accept-Ranges, Content-Range, Content-Length, Content-Disposition, Content-Type, ETag, X-Download-Tier")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "metadata service exited with error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger := pkglogger.New()
	defer logger.Sync()

	undo := zap.ReplaceGlobals(logger)
	defer undo()

	// 1. Load config
	cfg, err := config.Load()
	if err != nil {
		logger.Error("cannot load config", zap.Error(err))
		return fmt.Errorf("load config: %w", err)
	}

	verifierContext, verifierCancel := context.WithTimeout(context.Background(), 15*time.Second)
	jwksCache, err := jwks.New(verifierContext, jwks.WithJWKSURL(cfg.JWKSURL))
	verifierCancel()
	if err != nil {
		logger.Error("cannot initialize auth JWKS cache", zap.Error(err), zap.String("jwksURL", cfg.JWKSURL))
		return fmt.Errorf("initialize auth jwks: %w", err)
	}
	authVerifier := jwks.NewVerifier(jwksCache, cfg.JWKSIssuer, cfg.JWKSAudience...)
	authInterceptor := interceptor.NewAuthInterceptor(authVerifier)
	loggingInterceptor := interceptor.NewLoggingInterceptor(logger)
	logger.Info("metadata auth verifier initialized", zap.String("jwksURL", cfg.JWKSURL))

	// 2. Connect to ScyllaDB with readiness checks and keyspace bootstrap
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	session, err := db.Connect(ctx, db.Config{
		Hosts:             cfg.ScyllaHosts,
		Port:              cfg.ScyllaPort,
		Username:          cfg.ScyllaUsername,
		Password:          cfg.ScyllaPassword,
		Consistency:       gocql.LocalQuorum,
		Datacenter:        cfg.ScyllaDatacenter,
		ReplicationFactor: cfg.ScyllaReplicationFactor,
	})
	cancel()

	if err != nil {
		logger.Error("cannot connect to scylladb", zap.Error(err))
		return fmt.Errorf("connect to scylladb: %w", err)
	}
	defer session.Close()

	logger.Info("connected to scylladb",
		zap.Strings("hosts", cfg.ScyllaHosts),
		zap.String("datacenter", cfg.ScyllaDatacenter))

	// 3. Run migrations
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	migrated, err := db.Migrate(ctx, session, db.Keyspace)
	cancel()

	if err != nil {
		logger.Error("cannot run migrations", zap.Error(err))
		return fmt.Errorf("run migrations: %w", err)
	}

	if migrated {
		logger.Info("migrations applied")
	} else {
		logger.Info("migrations already up-to-date")
	}

	// 4. Setup caching (Ristretto for L1 cache)
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e4,
		MaxCost:     1e6,
		BufferItems: 64,
	})
	if err != nil {
		logger.Error("cannot create cache", zap.Error(err))
		return fmt.Errorf("create cache: %w", err)
	}
	defer cache.Close()

	rateLimiter, err := server.NewRateLimiter(20, 40, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("create metadata rate limiter: %w", err)
	}

	// 5. Setup repositories
	metadataRepo := repository.NewMetadataRepo(session)
	folderRepo := repository.NewFolderRepo(session)
	itemsRepo := repository.NewItemsRepo(session)
	recentItemsRepo := repository.NewRecentItemsRepo(session)
	purgeRepo := repository.NewPurgeRepo(session)

	// 6. Setup service layer
	metadataSvc := service.New(
		metadataRepo,
		folderRepo,
		itemsRepo,
		recentItemsRepo,
		cache,
		cfg,
		logger,
	)

	// Preview pipeline: one worker + sweeper per content family
	// (image/video/pdf), sharing S3 storage plumbing. Individual families
	// degrade gracefully (missing ffmpeg/pdftoppm, NATS hiccup) without
	// blocking metadata boot. The storage client also serves presigned
	// thumbnail URLs (GetThumbnailURL) and the purge coordinator's deletions.
	previewStorage, err := preview.NewStorage(preview.S3Config{
		Bucket:    cfg.S3Bucket,
		Region:    cfg.S3Region,
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
	})
	if err != nil {
		logger.Warn("preview storage unavailable; continuing without thumbnail URLs", zap.Error(err))
	} else {
		metadataSvc.SetThumbnailURLer(previewStorage)
		previewCloser, err := preview.StartAll(context.Background(), cfg, metadataRepo, logger)
		if err != nil {
			logger.Warn("preview pipeline failed to start", zap.Error(err))
		} else {
			defer previewCloser.Close()
		}
	}

	purgeCoordinator, err := purge.NewCoordinator(cfg.NATSURL, purgeRepo, metadataRepo, logger)
	if err != nil {
		logger.Warn("purge coordinator unavailable; permanent deletion disabled", zap.Error(err))
	} else {
		// Wire in preview deletion so thumbnails are removed when a file is
		// permanently deleted.
		if previewStorage != nil {
			purgeCoordinator.SetThumbnailDeleter(previewStorage)
		}
		if err := purgeCoordinator.Start(context.Background()); err != nil {
			purgeCoordinator.Close()
			logger.Warn("purge coordinator failed to start; permanent deletion disabled", zap.Error(err))
		} else {
			metadataSvc.SetPurgeCoordinator(purgeCoordinator)
			defer purgeCoordinator.Close()
			logger.Info("purge coordinator started")

			// Recovery sweeper: re-drives purge jobs that stalled (crash
			// between insert and publish, lost request/completion messages).
			// The repo satisfies the work-list/claim parts of the deps
			// interface; the coordinator satisfies publish/metadata-removal.
			sweeperCfg := purge.SweeperConfig{
				Interval:          cfg.PurgeRecoveryInterval,
				InitialRetryDelay: cfg.PurgeInitialRetryDelay,
				MaxRetryDelay:     cfg.PurgeMaxRetryDelay,
				BackoffMultiplier: cfg.PurgeRetryBackoffMultiplier,
				MaxAttempts:       cfg.PurgeMaxAttempts,
			}
			sweeper := purge.NewSweeper(struct {
				*repository.PurgeRepo
				*purge.Coordinator
			}{purgeRepo, purgeCoordinator}, sweeperCfg, logger)
			sweeper.Start(context.Background())
			logger.Info("purge sweeper started",
				zap.Duration("interval", sweeperCfg.Interval),
				zap.Int("maxAttempts", sweeperCfg.MaxAttempts))
		}
	}

	// 7. Setup Connect RPC server
	metadataHandler := server.New(metadataSvc, logger)

	// 7b. Content server (Tier A: ticket-verified range-aware block streaming)
	// and Tier B stitcher (server-side UploadPartCopy consolidation).
	// Both live here so they reuse this process's S3 client and Scylla
	// session; the /content route speaks plain HTTP — the browser's native
	// <a download>/<img>/<video> path that can't attach an Authorization
	// header — while /content's auth is the short-lived HMAC download
	// ticket minted by GetDownloadTicket (ownership checked THERE).
	var stitchRepo *repository.StitchRepo
	var contentStorage *content.S3Storage
	var contentServer *content.Server
	if cfg.S3Endpoint != "" {
		storage, storageErr := content.NewS3Storage(content.S3Config{
			Bucket:    cfg.S3Bucket,
			Region:    cfg.S3Region,
			Endpoint:  cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
		})
		if storageErr != nil {
			logger.Warn("content storage unavailable; download tickets disabled", zap.Error(storageErr))
		} else {
			contentStorage = storage
			stitchRepo = repository.NewStitchRepo(session)
			metadataSvc.SetStitchLookup(stitchRepo)
			metadataSvc.SetConsolidatedPresigner(contentStorage)
		}
	}

	if contentStorage != nil {
		contentServer = content.NewServer(metadataRepo, contentStorage, cfg.DownloadTicketKey, logger, content.WithStreamWorkers(12))
		if cfg.StitchEnabled && cfg.S3Endpoint != "" {
			stitcher, stitcherErr := content.NewStitcher(content.S3Config{
				Bucket:    cfg.S3Bucket,
				Region:    cfg.S3Region,
				Endpoint:  cfg.S3Endpoint,
				AccessKey: cfg.S3AccessKey,
				SecretKey: cfg.S3SecretKey,
			})
			if stitcherErr != nil {
				logger.Warn("stitcher unavailable; Tier B disabled", zap.Error(stitcherErr))
			} else {
				// Downloads redirect to the consolidated object; inline previews
				// stream it here so HEAD/Accept-Ranges/Range all answer on the
				// content origin the viewers are embedded from.
				content.WithStitcher(stitchRepo, contentStorage)(contentServer)
				content.WithObjectFetcher(contentStorage)(contentServer)
				stitchWorker := content.NewStitchWorker(stitcher, stitchRepo, metadataRepo, content.StitcherConfig{
					Enabled:         true,
					Interval:        cfg.StitchInterval,
					BatchSize:       cfg.StitchBatchSize,
					MinAge:          time.Duration(cfg.StitchMinAgeSecs) * time.Second,
					StaleClaimAfter: 10 * time.Minute,
					MaxAttempts:     content.DefaultStitcherConfig().MaxAttempts,
				}, logger)
				stitchCtx, stitchCancel := context.WithCancel(context.Background())
				defer stitchCancel()
				stitchWorker.Start(stitchCtx)
				logger.Info("Tier B stitcher enabled",
					zap.Duration("interval", cfg.StitchInterval),
					zap.Int("batchSize", cfg.StitchBatchSize))
			}
		}
	}

	// 8. Start HTTP server with h2c (HTTP/2 Cleartext) support for gRPC
	addr := ":" + cfg.GRPCPort
	mux := http.NewServeMux()
	srv := connect.NewServer(
		loggingInterceptor,
		authInterceptor.Wrap,
		rateLimiter.Interceptor(),
		server.TimeoutInterceptor(3*time.Second),
	)
	pbconnect.RegisterMetadataServiceHandler(srv, metadataHandler)

	pbconnect.RegisterFileServiceHandler(srv, metadataHandler)

	pbconnect.RegisterFolderServiceHandler(srv, metadataHandler)
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(0))

	if contentServer != nil {
		contentServer.Handler(mux)
		logger.Info("content server mounted", zap.String("route", "/content/{file_id}"),
			zap.String("baseURL", cfg.ContentBaseURL))
	}

	httpServer := &http.Server{
		Addr: addr,
		Handler: h2c.NewHandler(
			corsMiddleware(mux),
			&http2.Server{},
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// Graceful shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	errChan := make(chan error, 1)
	go func() {
		logger.Info("metadata service starting", zap.String("addr", addr))
		errChan <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errChan:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", zap.Error(err))
			return err
		}
	case sig := <-sigChan:
		logger.Info("received signal, shutting down", zap.String("signal", sig.String()))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := httpServer.Shutdown(ctx)
		cancel()
		if err != nil {
			logger.Error("shutdown error", zap.Error(err))
			return err
		}
	}

	logger.Info("metadata service stopped")
	return nil
}
