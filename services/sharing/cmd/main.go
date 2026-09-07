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
	"github.com/dgraph-io/ristretto"
	"github.com/gocql/gocql"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	pkglogger "github.com/Ajay01103/go-dropbox/pkg/logger"
	pkgredis "github.com/Ajay01103/go-dropbox/pkg/redisclient"
	"github.com/Ajay01103/go-dropbox/sharing/config"
	"github.com/Ajay01103/go-dropbox/sharing/db"
	"github.com/Ajay01103/go-dropbox/sharing/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/sharing/internal/repository"
	"github.com/Ajay01103/go-dropbox/sharing/internal/service"
	"github.com/Ajay01103/go-dropbox/sharing/server"
)

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
		fmt.Fprintf(os.Stderr, "sharing service exited with error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger := pkglogger.New()
	defer logger.Sync()

	undo := zap.ReplaceGlobals(logger)
	defer undo()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("cannot load config", zap.Error(err))
		return fmt.Errorf("load config: %w", err)
	}

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

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	migrated, err := db.Migrate(ctx, session)
	cancel()
	if err != nil {
		logger.Error("cannot run migrations", zap.Error(err))
		return fmt.Errorf("run migrations: %w", err)
	}
	if migrated {
		logger.Info("database migrations applied successfully")
	} else {
		logger.Debug("database schema is up-to-date")
	}

	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e4,
		MaxCost:     1e6,
		BufferItems: 64,
	})
	if err != nil {
		logger.Error("cannot create in-memory cache", zap.Error(err))
		return fmt.Errorf("create cache: %w", err)
	}
	defer cache.Close()

	redisClient, err := pkgredis.NewClientFromURL(cfg.RedisURL)
	if err != nil {
		logger.Error("cannot create redis client", zap.Error(err))
		return fmt.Errorf("create redis client: %w", err)
	}
	defer redisClient.Close()

	shareRepo := repository.NewShareRepo(session)
	sharingSvc := service.New(shareRepo, redisClient, cfg, logger)
	sharingHandler := server.New(sharingSvc, logger)

	loggingInterceptor := interceptor.NewLoggingInterceptor(logger)
	mux := http.NewServeMux()
	path, handler := pbconnect.NewSharingServiceHandler(
		sharingHandler,
		connect.WithInterceptors(loggingInterceptor),
	)
	mux.Handle(path, corsMiddleware(handler))

	addr := ":" + cfg.GRPCPort
	h2cServer := &http.Server{
		Addr:    addr,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	errChan := make(chan error, 1)
	go func() {
		logger.Info("sharing service starting", zap.String("addr", addr))
		errChan <- h2cServer.ListenAndServe()
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
		shutdownErr := h2cServer.Shutdown(ctx)
		cancel()
		if shutdownErr != nil {
			logger.Error("shutdown error", zap.Error(shutdownErr))
			return shutdownErr
		}
	}

	logger.Info("sharing service stopped")
	return nil
}
