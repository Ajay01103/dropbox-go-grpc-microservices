package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v2"
	"github.com/scylladb/gocqlx/v2/migrate"
)

//go:embed migrations/*.cql
var migrationFS embed.FS

// Migrate runs all pending migrations. It is idempotent and safe to call on startup.
// Returns true when new migrations were applied.
//
// B4 note: column drops (z15_purge_jobs_drop_has_reconciliation_errors) are
// implemented as `-- CALL` comment markers handled by migrate.Callback below,
// NOT as CQL statements. CQL has no "DROP COLUMN IF EXISTS", so a plain
// statement would fail on re-run after a partial failure (statement applied,
// migration not recorded) and wedge boot. The Go implementation checks
// system_schema.columns first and skips when the column is already gone,
// which makes every migration file re-runnable.
//
// The keyspace parameter must match the keyspace the session is bound to; it
// is used for system_schema lookups.
func Migrate(ctx context.Context, session *gocql.Session, keyspace string) (bool, error) {
	return migrateFiltered(ctx, session, keyspace, nil)
}

// skipFS hides a set of top-level file names from the underlying fs.FS. The
// B4 migration tests use it to apply only the pre-B4 migration files,
// simulating a keyspace that has not yet seen the drops.
//
// It must implement fs.ReadDirFS: migrate.FromFS lists files with fs.Glob,
// and fs.Glob uses ReadDir (not per-file Open) to enumerate a directory, so
// filtering in Open alone would leave the skipped files visible to Glob.
type skipFS struct {
	inner fs.FS
	skip  map[string]bool
}

func (s skipFS) Open(name string) (fs.File, error) {
	if s.skip[name] {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return s.inner.Open(name)
}

func (s skipFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(s.inner, name)
	if err != nil {
		return nil, err
	}
	out := make([]fs.DirEntry, 0, len(entries))
	for _, e := range entries {
		if !s.skip[e.Name()] {
			out = append(out, e)
		}
	}
	return out, nil
}

// migrateFiltered is Migrate with an optional set of migration file names to
// hide. Test-only concern, but it lives here so the callback wiring — the
// production path — is exactly what the tests exercise.
func migrateFiltered(ctx context.Context, session *gocql.Session, keyspace string, skip map[string]bool) (bool, error) {
	xsession, err := gocqlx.WrapSession(session, nil)
	if err != nil {
		return false, fmt.Errorf("wrap session: %w", err)
	}

	before, err := countApplied(ctx, xsession)
	if err != nil {
		return false, err
	}

	var source fs.FS = migrationFS
	if skip != nil {
		source = skipFS{inner: migrationFS, skip: skip}
	}
	migrationsDir, err := fs.Sub(source, "migrations")
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
		if event == migrate.CallComment {
			switch name {
			case "ensure_file_metadata_columns":
				return ensureFileMetadataColumns(callbackCtx, callbackSession, keyspace)
			case "drop_purge_jobs_has_reconciliation_errors":
				return dropColumnIfPresent(callbackCtx, callbackSession, keyspace, "purge_jobs", "has_reconciliation_errors")
			case "ensure_folder_ancestor_ids":
				return ensureFolderAncestorIDs(callbackCtx, callbackSession, keyspace)
			}
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

// dropColumnIfPresent drops one column if it still exists, and is a no-op
// otherwise. This is what makes a column-drop migration re-runnable: gocqlx
// runs statements one at a time (not transactionally), so a migration whose
// ALTER already applied but whose recording was interrupted would otherwise
// fail on every subsequent boot with "column not found". See
// z15_purge_jobs_drop_has_reconciliation_errors.cql.
func dropColumnIfPresent(ctx context.Context, session gocqlx.Session, keyspace, table, column string) error {
	var existing string
	err := session.ContextQuery(ctx,
		`SELECT column_name FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ? AND column_name = ?`, nil).
		Bind(keyspace, table, column).GetRelease(&existing)
	if err == nil {
		// Column still present — drop it.
		if err := session.ContextQuery(ctx,
			fmt.Sprintf("ALTER TABLE %s DROP %s", table, column), nil).ExecRelease(); err != nil {
			return fmt.Errorf("drop %s.%s: %w", table, column, err)
		}
		return nil
	}
	if err == gocql.ErrNotFound {
		return nil // already dropped (partial-failure re-run) — no-op
	}
	return fmt.Errorf("check %s.%s: %w", table, column, err)
}

func ensureFileMetadataColumns(ctx context.Context, session gocqlx.Session, keyspace string) error {
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
			Bind(keyspace, "files_by_folder", column).GetRelease(&existing)
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

// ensureFolderAncestorIDs adds the ancestor_ids list<uuid> column to the three
// folder tables. Like ensureFileMetadataColumns, it checks system_schema.columns
// first so a partial-failure re-run is a no-op instead of a boot-wedging error.
func ensureFolderAncestorIDs(ctx context.Context, session gocqlx.Session, keyspace string) error {
	for _, table := range []string{"folders_by_owner", "folders_by_id", "folders_by_parent"} {
		var existing string
		err := session.ContextQuery(ctx,
			`SELECT column_name FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ? AND column_name = ?`, nil).
			Bind(keyspace, table, "ancestor_ids").GetRelease(&existing)
		if err == nil {
			continue
		}
		if err != gocql.ErrNotFound {
			return fmt.Errorf("check %s.ancestor_ids: %w", table, err)
		}
		if err := session.ContextQuery(ctx, fmt.Sprintf("ALTER TABLE %s ADD ancestor_ids list<uuid>", table), nil).ExecRelease(); err != nil {
			return fmt.Errorf("add %s.ancestor_ids: %w", table, err)
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

// b4TestConfig is the local-dev Scylla connection used by the B4 migration
// integration tests (build tag: integration).
func b4TestConfig() Config {
	return Config{
		Hosts:             []string{"localhost"},
		Port:              9042,
		Username:          "",
		Password:          "",
		Consistency:       gocql.LocalQuorum,
		Datacenter:        "datacenter1",
		ReplicationFactor: 1,
	}
}

// b4CreateTestKeyspace creates a throwaway keyspace for one test run and
// returns a cleanup func that drops it. Name collision with the real dev
// keyspaces (metadata_ks) is impossible by construction.
func b4CreateTestKeyspace(ctx context.Context, cfg Config) (string, *gocql.Session, func(), error) {
	bootstrap, err := newSession(cfg, "")
	if err != nil {
		return "", nil, nil, fmt.Errorf("open bootstrap session: %w", err)
	}
	keyspace := fmt.Sprintf("b4mig_%d", time.Now().UnixNano())
	q := fmt.Sprintf(`CREATE KEYSPACE %s WITH replication = {'class': 'NetworkTopologyStrategy', '%s': 1}`, keyspace, cfg.Datacenter)
	if err := bootstrap.Query(q).WithContext(ctx).Exec(); err != nil {
		bootstrap.Close()
		return "", nil, nil, fmt.Errorf("create test keyspace: %w", err)
	}
	session, err := newSession(cfg, keyspace)
	if err != nil {
		_ = bootstrap.Query(fmt.Sprintf("DROP KEYSPACE %s", keyspace)).Exec()
		bootstrap.Close()
		return "", nil, nil, fmt.Errorf("open test keyspace session: %w", err)
	}
	cleanup := func() {
		session.Close()
		_ = bootstrap.Query(fmt.Sprintf("DROP KEYSPACE %s", keyspace)).WithContext(context.Background()).Exec()
		bootstrap.Close()
	}
	return keyspace, session, cleanup, nil
}

// columnExists reports whether keyspace.table.column exists right now.
func columnExists(ctx context.Context, session *gocql.Session, keyspace, table, column string) (bool, error) {
	var name string
	err := session.Query(
		`SELECT column_name FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ? AND column_name = ?`,
		keyspace, table, column).WithContext(ctx).Scan(&name)
	if err == gocql.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// tableExists reports whether keyspace.table exists right now.
func tableExists(ctx context.Context, session *gocql.Session, keyspace, table string) (bool, error) {
	var name string
	err := session.Query(
		`SELECT table_name FROM system_schema.tables WHERE keyspace_name = ? AND table_name = ?`,
		keyspace, table).WithContext(ctx).Scan(&name)
	if err == gocql.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// migrationApplied reports whether gocqlx recorded the migration file.
func migrationApplied(ctx context.Context, session *gocql.Session, name string) (bool, error) {
	iter := session.Query(`SELECT name FROM gocqlx_migrate`).WithContext(ctx).Iter()
	defer iter.Close()
	var got string
	for iter.Scan(&got) {
		if got == name {
			return true, nil
		}
	}
	return false, iter.Close()
}
