package db

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

const Keyspace = "auth_ks"

type Config struct {
	Hosts             []string
	Port              int
	Username          string
	Password          string
	Consistency       gocql.Consistency
	Datacenter        string
	ReplicationFactor int
}

func Connect(ctx context.Context, cfg Config) (*gocql.Session, error) {
	if err := waitForReady(ctx, cfg); err != nil {
		return nil, fmt.Errorf("wait for scylladb: %w", err)
	}

	if err := bootstrapKeyspace(ctx, cfg); err != nil {
		return nil, fmt.Errorf("bootstrap keyspace: %w", err)
	}

	return newSession(cfg, Keyspace)
}

func waitForReady(ctx context.Context, cfg Config) error {
	const retryInterval = 2 * time.Second

	for {
		session, err := newSession(cfg, "")
		if err == nil {
			pingErr := session.Query("SELECT cluster_name FROM system.local").
				WithContext(ctx).
				RetryPolicy(&gocql.ExponentialBackoffRetryPolicy{NumRetries: 2}).
				Exec()
			session.Close()
			if pingErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("scylladb not ready: %w", ctx.Err())
		case <-time.After(retryInterval):
		}
	}
}

func bootstrapKeyspace(ctx context.Context, cfg Config) error {
	session, err := newSession(cfg, "")
	if err != nil {
		return fmt.Errorf("open bootstrap session: %w", err)
	}
	defer session.Close()

	var name string
	err = session.Query(
		`SELECT keyspace_name FROM system_schema.keyspaces WHERE keyspace_name = ? LIMIT 1`,
		Keyspace,
	).WithContext(ctx).Scan(&name)
	if err == nil {
		return nil
	}
	if err != gocql.ErrNotFound {
		return fmt.Errorf("check keyspace: %w", err)
	}

	rf := cfg.ReplicationFactor
	if rf < 1 {
		return fmt.Errorf("invalid replication factor %d; must be >= 1", rf)
	}
	dc := cfg.Datacenter
	if dc == "" {
		dc = "datacenter1"
	}

	q := fmt.Sprintf(
		`CREATE KEYSPACE %s WITH replication = {'class': 'NetworkTopologyStrategy', '%s': %d}`,
		Keyspace,
		dc,
		rf,
	)
	if err := session.Query(q).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("create keyspace: %w", err)
	}

	return nil
}

func newSession(cfg Config, keyspace string) (*gocql.Session, error) {
	cluster := gocql.NewCluster(cfg.Hosts...)
	cluster.Port = cfg.Port
	cluster.Keyspace = keyspace
	cluster.Authenticator = gocql.PasswordAuthenticator{
		Username: cfg.Username,
		Password: cfg.Password,
	}
	cluster.Consistency = cfg.Consistency
	cluster.Timeout = 10 * time.Second
	cluster.ConnectTimeout = 10 * time.Second
	cluster.SocketKeepalive = 30 * time.Second
	cluster.MaxWaitSchemaAgreement = 30 * time.Second
	cluster.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(
		gocql.RoundRobinHostPolicy(),
	)
	cluster.RetryPolicy = &gocql.SimpleRetryPolicy{NumRetries: 0}

	return gocql.NewSession(*cluster)
}
