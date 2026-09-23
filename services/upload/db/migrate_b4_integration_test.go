//go:build integration

package db

// B4 migration integration tests. They run the REAL migration chain against
// a throwaway keyspace (b4mig_<nanos>, dropped on cleanup) with two goals:
//
//  1. Upgrade path: a keyspace populated with v1-shaped data (blocks rows
//     carrying storage_key, by-hash ledger rows) survives the full B4 chain —
//     the drops run last (z-prefix lexical ordering) and the data
//     projections they served are gone afterwards.
//
//  2. Partial-failure re-run: gocqlx records migrations only AFTER all
//     statements in a file succeed, and it does not run them transactionally.
//     We simulate a crash between "statement applied" and "migration
//     recorded" by applying the column drop manually, then boot — the second
//     run must succeed (the callback is a no-op on the missing column) and
//     record the migration. This is the test that fails with a plain
//     `ALTER TABLE ... DROP storage_key` statement in the .cql file.

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

func mustUUID(t *testing.T) gocql.UUID {
	t.Helper()
	u, err := gocql.RandomUUID()
	if err != nil {
		t.Fatalf("random uuid: %v", err)
	}
	return u
}

const (
	b4UploadSkipByHash = "z10_block_decrement_operations_by_hash_drop.cql"
	b4UploadSkipKey    = "z11_blocks_drop_storage_key.cql"
	// b4UploadSkipPostB4 lists every migration file added AFTER the B4 drops.
	// The pre-B4 phase must hide these too: gocqlx's consistency check
	// compares the recorded migration names (sorted) position-by-position
	// against the file list, so any gap between the applied set and the
	// visible files trips "inconsistent migrations" during the recovery boot.
	b4UploadSkipPostB4 = "z16_block_gc_candidates_ttl.cql"
)

func TestB4UploadMigrationsUpgradePathAndPartialFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg := b4TestConfig()
	keyspace, session, cleanup, err := b4CreateTestKeyspace(ctx, cfg)
	if err != nil {
		t.Skipf("scylladb not available: %v", err)
	}
	defer cleanup()

	// Phase 1: build the pre-B4 keyspace
	// All migrations except the two B4 drop files AND everything added after
	// them (post-B4 hardening files) — the pre-B4 world must not see either.
	skip := map[string]bool{b4UploadSkipByHash: true, b4UploadSkipKey: true, b4UploadSkipPostB4: true}
	applied, err := migrateFiltered(ctx, session, keyspace, skip)
	if err != nil {
		t.Fatalf("pre-B4 migrations: %v", err)
	}
	if !applied {
		t.Fatal("pre-B4 migrations should have applied on a fresh keyspace")
	}

	// gocqlx does not await schema agreement (DefaultAwaitSchemaAgreement is
	// disabled), so give system_schema a moment to settle before asserting.
	// The skipFS filter must also hide every migration added AFTER the B4
	// drops (e.g. z16), otherwise gocqlx's consistency check compares the
	// applied set against a file list with fewer entries and fails with
	// "inconsistent migrations" before the partial-failure recovery runs.
	wait := func(table string) {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if ok, err := tableExists(ctx, session, keyspace, table); err == nil && ok {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	wait("block_decrement_operations")
	wait("block_decrement_operations_by_hash")

	// Pre-B4 invariants: the by-hash table and the storage_key column exist.
	if ok, err := tableExists(ctx, session, keyspace, "block_decrement_operations_by_hash"); err != nil || !ok {
		t.Fatalf("pre-B4: by-hash table should exist (exists=%v err=%v)", ok, err)
	}
	if ok, err := columnExists(ctx, session, keyspace, "blocks", "storage_key"); err != nil || !ok {
		t.Fatalf("pre-B4: blocks.storage_key should exist (exists=%v err=%v)", ok, err)
	}

	// Seed v1-shaped data.
	now := time.Now().UTC()
	if err := session.Query(
		`INSERT INTO blocks (block_hash, size_bytes, ref_count, storage_backend, storage_key, etag, created_at, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"aa11bb22cc33dd44ee55ff6600778899aabbccddeeff00112233445566778899", int64(42), int64(3), "local", "blocks/aa/11/aa11...raw", "etag-1", now, nil,
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("seed blocks row: %v", err)
	}
	if err := session.Query(
		`INSERT INTO block_decrement_operations_by_hash (block_hash, job_id, occurrence, status, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"aa11bb22cc33dd44ee55ff6600778899aabbccddeeff00112233445566778899", mustUUID(t), 0, "COUNTER_APPLIED", now,
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("seed by-hash ledger row: %v", err)
	}

	// Phase 2: simulate a partial failure of z11
	// The ALTER applies but the process dies before gocqlx records z11.
	if err := session.Query(`ALTER TABLE blocks DROP storage_key`).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("simulate partial failure (manual drop): %v", err)
	}
	if ok, err := migrationApplied(ctx, session, b4UploadSkipKey); err != nil || ok {
		t.Fatalf("z11 must not be recorded before the recovery boot (recorded=%v err=%v)", ok, err)
	}

	// Phase 3: the recovery boot — the production Migrate call
	// With a plain CQL drop statement this re-runs the ALTER and fails with
	// "column not found", wedging boot. With the callback implementation it
	// must succeed.
	applied, err = Migrate(ctx, session, keyspace)
	if err != nil {
		t.Fatalf("post-partial-failure Migrate failed (boot would be stuck): %v", err)
	}
	if !applied {
		t.Fatal("Migrate should apply the two skipped B4 drop migrations")
	}

	// Phase 4: post-B4 invariants
	if ok, err := tableExists(ctx, session, keyspace, "block_decrement_operations_by_hash"); err != nil || ok {
		t.Fatalf("post-B4: by-hash table must be gone (exists=%v err=%v)", ok, err)
	}
	if ok, err := columnExists(ctx, session, keyspace, "blocks", "storage_key"); err != nil || ok {
		t.Fatalf("post-B4: blocks.storage_key must be gone (exists=%v err=%v)", ok, err)
	}
	for _, name := range []string{b4UploadSkipByHash, b4UploadSkipKey} {
		ok, err := migrationApplied(ctx, session, name)
		if err != nil || !ok {
			t.Fatalf("post-B4: %s must be recorded (recorded=%v err=%v)", name, ok, err)
		}
	}
	// The v1-shaped data projection is gone with the column, but the row
	// itself (keyed by hash) survives — the BlockKey(hash) derivation is the
	// only key source after B4.
	var refCount int64
	if err := session.Query(
		`SELECT ref_count FROM blocks WHERE block_hash = ?`,
		"aa11bb22cc33dd44ee55ff6600778899aabbccddeeff00112233445566778899",
	).WithContext(ctx).Scan(&refCount); err != nil {
		t.Fatalf("seeded blocks row must survive the drops: %v", err)
	}
	if refCount != 3 {
		t.Fatalf("ref_count = %d, want 3", refCount)
	}

	// Phase 5: idempotent re-boot
	applied, err = Migrate(ctx, session, keyspace)
	if err != nil {
		t.Fatalf("second Migrate must be a clean no-op: %v", err)
	}
	if applied {
		t.Fatal("second Migrate must apply zero new migrations")
	}
}
