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

	previousCallback := migrate.Callback
	migrate.Callback = func(callbackCtx context.Context, callbackSession gocqlx.Session, event migrate.CallbackEvent, name string) error {
		if previousCallback != nil {
			if err := previousCallback(callbackCtx, callbackSession, event, name); err != nil {
				return err
			}
		}
		if event == migrate.CallComment && name == "ensure_file_metadata_columns" {
			return ensureFileMetadataColumns(callbackCtx, callbackSession)
		}
		return nil
	}
	defer func() { migrate.Callback = previousCallback }()

	if err := migrate.FromFS(ctx, xsession, migrationsDir); err != nil {
		return false, fmt.Errorf("run migrations: %w", err)
	}

	after, err := countApplied(ctx, xsession)
	if err != nil {
		return false, err
	}

	return after > before, nil
}

func ensureFileMetadataColumns(ctx context.Context, session gocqlx.Session) error {
	columns := map[string]string{
		"updated_at":       "timestamp",
		"is_deleted":       "boolean",
		"deleted_at":       "timestamp",
		"current":          "boolean",
		"deleted_batch_id": "uuid",
	}
	for column, columnType := range columns {
		var existing string
		err := session.ContextQuery(ctx, `SELECT column_name FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ? AND column_name = ?`, nil).
			Bind(Keyspace, "files_by_folder", column).GetRelease(&existing)
		if err == nil {
			continue
		}
		if err != gocql.ErrNotFound {
			return fmt.Errorf("check metadata column %s: %w", column, err)
		}
		if err := session.ContextQuery(ctx, fmt.Sprintf("ALTER TABLE files_by_folder ADD %s %s", column, columnType), nil).ExecRelease(); err != nil {
			return fmt.Errorf("add metadata column %s: %w", column, err)
		}
	}
	return nil
}

func countApplied(ctx context.Context, session gocqlx.Session) (int, error) {
	migs, err := migrate.List(ctx, session)
	if err != nil {
		return 0, fmt.Errorf("list applied migrations: %w", err)
	}

	return len(migs), nil
}
