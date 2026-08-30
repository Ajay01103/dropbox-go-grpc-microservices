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

	"go.uber.org/zap"

	"github.com/dgraph-io/ristretto"
	"github.com/gocql/gocql"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Ajay01103/go-notion/upload/config"
	"github.com/Ajay01103/go-notion/upload/db"
	"github.com/Ajay01103/go-notion/upload/gen/pb/pbconnect"
	"github.com/Ajay01103/go-notion/upload/internal/repository"
	"github.com/Ajay01103/go-notion/upload/internal/service"
	"github.com/Ajay01103/go-notion/upload/internal/storagegateway"
	"github.com/Ajay01103/go-notion/upload/server"
	"github.com/Ajay01103/go-notion/pkg/interceptor"
	pkglogger "github.com/Ajay01103/go-notion/pkg/logger"
	pkgredis "github.com/Ajay01103/go-notion/pkg/redisclient"
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
	chunkRepo := repository.NewChunkRepo(session)

	// 7. Setup storage gateway (local filesystem for Build Order 1)
	gateway := storagegateway.NewLocalFileSystemGateway(cfg.UploadStoragePath)

	// 8. Setup service layer
	uploadSvc := service.New(
		sessionRepo,
		chunkRepo,
		gateway,
		redisClient,
		cache,
		cfg,
		logger,
	)

	// 9. Setup Connect RPC server
	uploadHandler := server.New(uploadSvc, logger)

	// Create the handler chain with middleware
	var handler http.Handler
	handler = pbconnect.NewUploadServiceHandler(uploadHandler)
	handler = interceptor.AuthInterceptor(handler, logger)
	handler = corsMiddleware(handler)

	// 10. Start HTTP server with h2c (HTTP/2 Cleartext) support for gRPC
	addr := ":" + cfg.GRPCPort
	mux := http.NewServeMux()
	mux.Handle(pbconnect.NewUploadServiceHandler(uploadHandler))

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
