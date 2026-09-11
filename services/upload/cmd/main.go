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

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/dgraph-io/ristretto"
	"github.com/gocql/gocql"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	"github.com/Ajay01103/go-dropbox/pkg/jwks"
	pkglogger "github.com/Ajay01103/go-dropbox/pkg/logger"
	pkgredis "github.com/Ajay01103/go-dropbox/pkg/redisclient"
	"github.com/Ajay01103/go-dropbox/upload/config"
	"github.com/Ajay01103/go-dropbox/upload/db"
	"github.com/Ajay01103/go-dropbox/upload/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
	"github.com/Ajay01103/go-dropbox/upload/internal/service"
	"github.com/Ajay01103/go-dropbox/upload/internal/storagegateway"
	"github.com/Ajay01103/go-dropbox/upload/internal/worker"
	"github.com/Ajay01103/go-dropbox/upload/server"
)

// corsMiddleware allows Next.js or other frontends to access Connect endpoints
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:3000")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Connect-Protocol-Version, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "upload service exited with error: %v\n", err)
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
	logger.Info("upload auth verifier initialized", zap.String("jwksURL", cfg.JWKSURL))

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
	migrated, err := db.Migrate(ctx, session)
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

	// 5. Setup Redis for session state tracking
	redisClient, err := pkgredis.NewClientFromURL(cfg.RedisURL)
	if err != nil {
		logger.Error("cannot create redis client", zap.Error(err))
		return fmt.Errorf("create redis client: %w", err)
	}
	defer redisClient.Close()

	logger.Info("redis client initialized", zap.String("url", cfg.RedisURL))

	// 6. Setup repositories
	sessionRepo := repository.NewSessionRepo(session)
	blockRepo := repository.NewBlockRepo(session)

	// 7. Setup storage gateway (local filesystem for Build Order 1)
	gateway := storagegateway.NewLocalFileSystemGateway(cfg.UploadStoragePath)
	var blockGateway storagegateway.BlockGateway = storagegateway.NewLocalBlockGateway(cfg.UploadStoragePath)
	blockBackend := "local"
	if cfg.S3Endpoint != "" {
		configuredGateway, gatewayErr := storagegateway.NewS3BlockGateway(context.Background(), storagegateway.S3BlockGatewayConfig{
			Bucket: cfg.S3Bucket, Region: cfg.S3Region, Endpoint: cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		})
		if gatewayErr != nil {
			return fmt.Errorf("initialize S3 block gateway: %w", gatewayErr)
		}
		blockGateway = configuredGateway
		blockBackend = cfg.S3StorageBackend
		logger.Info("using S3-compatible block storage", zap.String("endpoint", cfg.S3Endpoint), zap.String("bucket", cfg.S3Bucket))
	}

	// 8. Setup async event publisher
	eventPublisher, err := service.NewNATSEventPublisher(cfg.NATSURL, cfg.NATSEventSubject)
	if err != nil {
		logger.Warn("nats publisher unavailable; continuing without async event bus",
			zap.String("natsURL", cfg.NATSURL),
			zap.Error(err),
		)
		eventPublisher = nil
	} else {
		defer eventPublisher.Close()
	}

	// 9. Start the durable block-reference decrement worker.
	decrementWorker, err := worker.NewBlockDecrementWorker(
		cfg.NATSURL,
		cfg.NATSBlockRefsRequestedSubject,
		cfg.NATSBlockRefsCompletedSubject,
		cfg.BlockLedgerStaleClaimThreshold,
		blockRepo,
		logger,
	)
	if err != nil {
		logger.Warn("block decrement worker unavailable; continuing without ledger consumer", zap.Error(err))
	} else {
		if err := decrementWorker.Start(context.Background()); err != nil {
			_ = decrementWorker.Close()
			logger.Warn("block decrement worker failed to start", zap.Error(err))
		} else {
			defer decrementWorker.Close()
		}
	}

	// 10. Setup service layer
	uploadSvc := service.New(
		sessionRepo,
		blockRepo,
		gateway,
		blockGateway,
		redisClient,
		cache,
		cfg,
		logger,
		eventPublisher,
		blockBackend,
	)

	// 11. Setup Connect RPC server
	uploadHandler := server.New(uploadSvc, logger)

	// 12. Start HTTP server with h2c (HTTP/2 Cleartext) support for gRPC
	addr := ":" + cfg.GRPCPort
	mux := http.NewServeMux()
	uploadPath, uploadHandlerHTTP := pbconnect.NewUploadServiceHandler(
		uploadHandler,
		connect.WithInterceptors(loggingInterceptor),
		connect.WithInterceptors(authInterceptor),
	)
	mux.Handle(uploadPath, uploadHandlerHTTP)

	server := &http.Server{
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
		logger.Info("upload service starting", zap.String("addr", addr))
		errChan <- server.ListenAndServe()
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
		err := server.Shutdown(ctx)
		cancel()
		if err != nil {
			logger.Error("shutdown error", zap.Error(err))
			return err
		}
	}

	logger.Info("upload service stopped")
	return nil
}
