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

	"connectrpc.com/connect"
	"github.com/dgraph-io/ristretto"
	"github.com/gocql/gocql"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Ajay01103/go-notion/auth/config"
	"github.com/Ajay01103/go-notion/auth/db"
	"github.com/Ajay01103/go-notion/auth/gen/pb/pbconnect"
	"github.com/Ajay01103/go-notion/auth/internal/repository"
	"github.com/Ajay01103/go-notion/auth/internal/scyllastore"
	"github.com/Ajay01103/go-notion/auth/internal/service"
	"github.com/Ajay01103/go-notion/auth/internal/tokencache"
	"github.com/Ajay01103/go-notion/auth/server"
	"github.com/Ajay01103/go-notion/pkg/interceptor"
	pkglogger "github.com/Ajay01103/go-notion/pkg/logger"
	"github.com/Ajay01103/go-notion/pkg/token"
)

// corsMiddleware allows Next.js or any other frontend to access Connect endpoints.
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
		fmt.Fprintf(os.Stderr, "auth service exited with error: %v\n", err)
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

	// 2. Connect performs readiness checks, keyspace bootstrap, and session creation.
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

	// 3. Run migrations.
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	hasMigrations, err := db.Migrate(ctx, session)
	cancel()
	if err != nil {
		logger.Error("cannot run migrations", zap.Error(err))
		return fmt.Errorf("run migrations: %w", err)
	}
	if hasMigrations {
		logger.Info("database migrations applied successfully")
	} else {
		logger.Debug("database schema is up-to-date")
	}

	// 4. Setup dependencies
	userRepo := repository.NewUserRepo(session)

	// Session-based stores (new model)
	sessionStore := scyllastore.NewSessionStore(session)
	revocationStore := scyllastore.NewRevocationStore(session)

	// Shared auth cache for session state and revocation lookups
	authCache, err := tokencache.NewRistrettoCache()
	if err != nil {
		logger.Error("cannot create token cache", zap.Error(err))
		return fmt.Errorf("create token cache: %w", err)
	}

	eddsaKeyRetention := cfg.EDDSASigningKeyRetentionDuration
	if eddsaKeyRetention < cfg.RefreshTokenDuration {
		logger.Warn(
			"eddsa signing key retention is shorter than refresh token duration; clamping to refresh duration",
			zap.Duration("eddsaSigningKeyRetention", eddsaKeyRetention),
			zap.Duration("refreshTokenDuration", cfg.RefreshTokenDuration),
		)
		eddsaKeyRetention = cfg.RefreshTokenDuration
	}

	for _, warning := range ValidateTokenDurations(cfg, logger) {
		logger.Warn(warning)
	}

	eddsaMaker, err := token.NewEDDSAMakerWithScylla(session, eddsaKeyRetention, tokencache.JWKSCacheKey(), authCache)
	if err != nil {
		logger.Error("cannot create EdDSA token maker", zap.Error(err))
		return fmt.Errorf("create eddsa token maker: %w", err)
	}
	var tokenMaker token.TokenMaker = eddsaMaker

	jwksCtx, jwksCancel := context.WithCancel(context.Background())
	defer jwksCancel()
	go startJWKSRefreshLoop(jwksCtx, session, eddsaMaker, authCache, logger)
	if err := refreshJWKSCache(context.Background(), session, eddsaMaker, authCache, logger); err != nil {
		logger.Warn("initial jwks cache population failed", zap.Error(err))
	}

	authService := service.New(userRepo, tokenMaker, sessionStore, revocationStore, authCache, cfg, logger)
	authServer := server.New(authService)

	loggingInterceptor := interceptor.NewLoggingInterceptor(logger)

	// 5. Start ConnectRPC server (HTTP/2 with h2c)
	mux := http.NewServeMux()

	// JWKS endpoint for token validation by other services
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(tokencache.JWKSTTL.Seconds())))

		if cached, ok := authCache.Get(tokencache.JWKSCacheKey()); ok {
			if jwksBytes, ok := cached.([]byte); ok {
				_, _ = w.Write(jwksBytes)
				return
			}
		}

		// Cache miss: try to load serialized JWKS from ScyllaDB table first
		var jwksText string
		if err := session.Query(`SELECT jwks_json FROM jwks_public_keys WHERE id = ?`, "current").WithContext(r.Context()).Scan(&jwksText); err == nil {
			jwksBytes := []byte(jwksText)
			authCache.SetWithTTL(tokencache.JWKSCacheKey(), jwksBytes, tokencache.JWKSCost, tokencache.JWKSTTL)
			authCache.Wait()
			_, _ = w.Write(jwksBytes)
			return
		} else if !errors.Is(err, gocql.ErrNotFound) {
			logger.Warn("jwks scylla lookup failed; falling back to in-memory export", zap.Error(err))
		}
		// If not found in Scylla, export from EDDSAMaker (in-memory keys loaded at startup)
		jwksData, err := eddsaMaker.ExportPublicKeys()
		if err != nil {
			logger.Error("export jwks", zap.Error(err))
			http.Error(w, "Failed to export JWKS", http.StatusInternalServerError)
			return
		}

		authCache.SetWithTTL(tokencache.JWKSCacheKey(), jwksData, tokencache.JWKSCost, tokencache.JWKSTTL)
		authCache.Wait()
		_, _ = w.Write(jwksData)
	})

	path, handler := pbconnect.NewAuthServiceHandler(
		authServer,
		connect.WithInterceptors(loggingInterceptor),
	)
	mux.Handle(path, corsMiddleware(handler))

	addr := fmt.Sprintf(":%s", cfg.GRPCPort)
	srv := &http.Server{
		Addr:    addr,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}
	serverErrCh := make(chan error, 1)

	go func() {
		logger.Info("AUTH SERVICE started at ConnectRPC server", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case err := <-serverErrCh:
		logger.Error("server terminated unexpectedly", zap.Error(err))
		return fmt.Errorf("listen and serve: %w", err)
	case sig := <-quit:
		logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	}

	logger.Info("shutting down server...")

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server forced to shutdown", zap.Error(err))
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped")

	return nil
}

func ValidateTokenDurations(cfg config.Config, _ *zap.Logger) []string {
	var warnings []string
	if cfg.AccessTokenDuration > tokencache.SessionStateTTL {
		warnings = append(warnings, fmt.Sprintf("access token duration %s exceeds session cache TTL %s", cfg.AccessTokenDuration, tokencache.SessionStateTTL))
	}
	return warnings
}

func refreshJWKSCache(ctx context.Context, session *gocql.Session, eddsaMaker *token.EDDSAMaker, authCache *ristretto.Cache, logger *zap.Logger) error {
	if authCache == nil {
		return errors.New("auth cache is nil")
	}

	var jwksText string
	if err := session.Query(`SELECT jwks_json FROM jwks_public_keys WHERE id = ?`, "current").WithContext(ctx).Scan(&jwksText); err == nil {
		jwksBytes := []byte(jwksText)
		authCache.SetWithTTL(tokencache.JWKSCacheKey(), jwksBytes, tokencache.JWKSCost, tokencache.JWKSTTL)
		authCache.Wait()
		return nil
	} else {
		if logger != nil {
			if errors.Is(err, gocql.ErrNotFound) {
				logger.Debug("jwks row not found in scylla; using in-memory exporter")
			} else {
				logger.Warn("jwks refresh from scylla failed; using in-memory exporter", zap.Error(err))
			}
		}
	}

	jwksData, err := eddsaMaker.ExportPublicKeys()
	if err != nil {
		return fmt.Errorf("export jwks: %w", err)
	}
	authCache.SetWithTTL(tokencache.JWKSCacheKey(), jwksData, tokencache.JWKSCost, tokencache.JWKSTTL)
	authCache.Wait()
	return nil
}

func startJWKSRefreshLoop(ctx context.Context, session *gocql.Session, eddsaMaker *token.EDDSAMaker, authCache *ristretto.Cache, logger *zap.Logger) {
	ticker := time.NewTicker(tokencache.JWKSTTL / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := refreshJWKSCache(refreshCtx, session, eddsaMaker, authCache, logger); err != nil && logger != nil {
				logger.Warn("periodic jwks cache refresh failed", zap.Error(err))
			}
			cancel()
		}
	}
}
