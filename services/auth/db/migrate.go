package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v2"
	"github.com/scylladb/gocqlx/v2/migrate"
)

//go:embed migrations/*.cql
var migrationFS embed.FS

// Migrate runs all pending migrations. It is idempotent and safe to call on startup.
// Returns true when new migrations were applied.
func Migrate(ctx context.Context, session *gocql.Session) (bool, error) {
	xsession, err := gocqlx.WrapSession(session, nil)
	if err != nil {
		return false, fmt.Errorf("wrap session: %w", err)
	}

	before, err := countApplied(ctx, xsession)
	if err != nil {
		return false, err
	}

	migrationsDir, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return false, fmt.Errorf("open migrations dir: %w", err)
	}

	if err := migrate.FromFS(ctx, xsession, migrationsDir); err != nil {
		return false, fmt.Errorf("run migrations: %w", err)
	}

	after, err := countApplied(ctx, xsession)
	if err != nil {
		return false, err
	}

	return after > before, nil
}

func countApplied(ctx context.Context, session gocqlx.Session) (int, error) {
	migs, err := migrate.List(ctx, session)
	if err != nil {
		return 0, fmt.Errorf("list applied migrations: %w", err)
	}

	return len(migs), nil
}
