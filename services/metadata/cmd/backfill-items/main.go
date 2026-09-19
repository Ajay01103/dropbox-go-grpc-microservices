package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gocql/gocql"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	"github.com/Ajay01103/go-dropbox/metadata/db"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("load config", zap.Error(err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	session, err := db.Connect(ctx, db.Config{
		Hosts: cfg.ScyllaHosts, Port: cfg.ScyllaPort, Username: cfg.ScyllaUsername,
		Password: cfg.ScyllaPassword, Consistency: gocql.LocalQuorum,
		Datacenter: cfg.ScyllaDatacenter, ReplicationFactor: cfg.ScyllaReplicationFactor,
	})
	if err != nil {
		logger.Fatal("connect to scylladb", zap.Error(err))
	}
	defer session.Close()

	items := repository.NewItemsRepo(session)
	recent := repository.NewRecentItemsRepo(session)
	logger.Info("starting metadata item backfill", zap.Int("workers", 4), zap.Duration("interval", 20*time.Millisecond))
	if err := items.Backfill(ctx, recent, 4, 20*time.Millisecond); err != nil {
		logger.Fatal("backfill metadata items", zap.Error(err))
	}
	fmt.Println("metadata item backfill complete")
}
