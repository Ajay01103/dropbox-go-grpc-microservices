//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

// TestFolderAncestorIDs verifies the z18 migration and the ancestor_ids
// lifecycle end to end against a throwaway Scylla keyspace:
//
//  1. the migration adds ancestor_ids to all three folder tables and is
//     idempotent on re-run (partial-failure safe, like the other CALLs),
//  2. a seeded pre-migration folder row (NULL ancestor_ids — a state the
//     live code path can no longer produce once the migration ships) is
//     still readable after the column exists,
//  3. writing and reading back ancestors round-trips in order.
func TestFolderAncestorIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg := b4TestConfig()
	keyspace, session, cleanup, err := b4CreateTestKeyspace(ctx, cfg)
	if err != nil {
		t.Skipf("scylladb not available: %v", err)
	}
	defer cleanup()

	// ── Phase 1: run migrations up to (excluding) z18, seed a pre-migration row ──
	if _, err := migrateFiltered(ctx, session, keyspace, map[string]bool{"z18_folder_ancestor_ids.cql": true}); err != nil {
		t.Fatalf("pre-z18 migrations: %v", err)
	}

	owner, err := gocql.RandomUUID()
	if err != nil {
		t.Fatal(err)
	}
	folder, err := gocql.RandomUUID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := session.Query(
		`INSERT INTO folders_by_id (folder_id, owner_id, parent_id, name, path_cache, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		folder, owner, owner, "pre-migration folder", "/", now, now, false,
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("seed pre-migration folder row: %v", err)
	}

	// ── Phase 2: apply z18 — column appears, seeded row survives ──────────
	applied, err := migrateFiltered(ctx, session, keyspace, nil)
	if err != nil {
		t.Fatalf("z18 migration failed: %v", err)
	}
	if !applied {
		t.Fatal("z18 should have applied on this keyspace")
	}
	for _, table := range []string{"folders_by_owner", "folders_by_id", "folders_by_parent"} {
		ok, err := columnExists(ctx, session, keyspace, table, "ancestor_ids")
		if err != nil || !ok {
			t.Fatalf("post-z18: %s.ancestor_ids should exist (exists=%v err=%v)", table, ok, err)
		}
	}
	var name string
	var ancestors []gocql.UUID
	if err := session.Query(
		`SELECT name, ancestor_ids FROM folders_by_id WHERE folder_id = ?`, folder,
	).WithContext(ctx).Scan(&name, &ancestors); err != nil {
		t.Fatalf("pre-migration row must survive z18: %v", err)
	}
	if name != "pre-migration folder" || len(ancestors) != 0 {
		t.Fatalf("pre-migration row: name=%q ancestors=%v, want preserved name and empty ancestor_ids", name, ancestors)
	}

	// ── Phase 3: ancestors round-trip in order ────────────────────────────
	// Simulate a folder at /root/parent/this: chain = [owner(root), parent].
	parent, err := gocql.RandomUUID()
	if err != nil {
		t.Fatal(err)
	}
	chain := []gocql.UUID{owner, parent}
	if err := session.Query(
		`UPDATE folders_by_id SET ancestor_ids = ? WHERE folder_id = ?`, chain, folder,
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("write ancestors: %v", err)
	}
	if err := session.Query(
		`SELECT ancestor_ids FROM folders_by_id WHERE folder_id = ?`, folder,
	).WithContext(ctx).Scan(&ancestors); err != nil {
		t.Fatalf("read ancestors: %v", err)
	}
	if len(ancestors) != 2 || ancestors[0] != owner || ancestors[1] != parent {
		t.Fatalf("ancestor round-trip preserved order: got %v, want [%v %v]", ancestors, owner, parent)
	}

	// ── Phase 4: idempotent re-boot ───────────────────────────────────────
	applied, err = migrateFiltered(ctx, session, keyspace, nil)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if applied {
		t.Fatal("second Migrate must apply zero new migrations")
	}
}
