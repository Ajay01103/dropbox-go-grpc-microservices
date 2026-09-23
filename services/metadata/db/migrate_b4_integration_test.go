//go:build integration

package db

// B4 migration integration tests for the metadata keyspace. Same goals as
// the upload-side tests:
//
//  1. Upgrade path: a keyspace with v1-shaped purge data (legacy states,
//     by-state and by-file-version index rows, has_reconciliation_errors)
//     survives the full B4 chain — z13/z14/z15 run last, by lexical order.
//
//  2. Partial-failure re-run: a simulated crash after the
//     has_reconciliation_errors ALTER applied but before gocqlx recorded
//     z15 must NOT wedge the next boot.

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

const (
	b4MetaSkipByVersion = "z13_purge_drop_jobs_by_file_version.cql"
	b4MetaSkipByState   = "z14_purge_drop_jobs_by_state.cql"
	b4MetaSkipHasErrors = "z15_purge_jobs_drop_has_reconciliation_errors.cql"
	// b4MetaSkipPostB4 lists every migration file added AFTER the B4 drops.
	// The pre-B4 phase must hide these too: gocqlx's consistency check
	// compares the recorded migration names (sorted) position-by-position
	// against the file list, so any gap between the applied set and the
	// visible files trips "inconsistent migrations" during the recovery boot.
	b4MetaSkipPostB4 = "z17_drop_folders_by_user.cql"
	b4MetaSkipZ18    = "z18_folder_ancestor_ids.cql"
)

func TestB4MetadataMigrationsUpgradePathAndPartialFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg := b4TestConfig()
	keyspace, session, cleanup, err := b4CreateTestKeyspace(ctx, cfg)
	if err != nil {
		t.Skipf("scylladb not available: %v", err)
	}
	defer cleanup()

	// ── Phase 1: build the pre-B4 keyspace ───────────────────────────────
	skip := map[string]bool{b4MetaSkipByVersion: true, b4MetaSkipByState: true, b4MetaSkipHasErrors: true, b4MetaSkipPostB4: true, b4MetaSkipZ18: true}
	applied, err := migrateFiltered(ctx, session, keyspace, skip)
	if err != nil {
		t.Fatalf("pre-B4 migrations: %v", err)
	}
	if !applied {
		t.Fatal("pre-B4 migrations should have applied on a fresh keyspace")
	}

	// Pre-B4 invariants.
	for _, table := range []string{"purge_jobs_by_file_version", "purge_jobs_by_state"} {
		if ok, err := tableExists(ctx, session, keyspace, table); err != nil || !ok {
			t.Fatalf("pre-B4: %s should exist (exists=%v err=%v)", table, ok, err)
		}
	}
	if ok, err := columnExists(ctx, session, keyspace, "purge_jobs", "has_reconciliation_errors"); err != nil || !ok {
		t.Fatalf("pre-B4: purge_jobs.has_reconciliation_errors should exist (exists=%v err=%v)", ok, err)
	}

	// Seed v1-shaped data: a job in a LEGACY state (what the pre-deploy
	// backfill exists to fix) plus its index rows.
	now := time.Now().UTC()
	jobID, err := gocql.RandomUUID()
	if err != nil {
		t.Fatal(err)
	}
	fileID, err := gocql.RandomUUID()
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := gocql.RandomUUID()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Query(
		`INSERT INTO purge_jobs (job_id, file_id, folder_id, file_version, owner_id, block_hash_list, reason, state, has_reconciliation_errors, attempts, next_attempt_at, last_error, created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, fileID, ownerID, int32(1), ownerID, []string{"aa11"}, "permanent_delete", "DECREMENTED_WITH_ERRORS", true, 1, now, "old error", now, now, now,
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("seed purge_jobs row: %v", err)
	}
	if err := session.Query(
		`INSERT INTO purge_jobs_by_state (state, updated_at, job_id) VALUES (?, ?, ?)`,
		"DECREMENTED_WITH_ERRORS", now, jobID,
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("seed by_state row: %v", err)
	}
	if err := session.Query(
		`INSERT INTO purge_jobs_by_file_version (file_id, file_version, job_id) VALUES (?, ?, ?)`,
		fileID, int32(1), jobID,
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("seed by_file_version row: %v", err)
	}

	// ── Phase 2: simulate a partial failure of z15 ───────────────────────
	if err := session.Query(`ALTER TABLE purge_jobs DROP has_reconciliation_errors`).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("simulate partial failure (manual drop): %v", err)
	}

	// ── Phase 3: the recovery boot — the production Migrate call ─────────
	applied, err = Migrate(ctx, session, keyspace)
	if err != nil {
		t.Fatalf("post-partial-failure Migrate failed (boot would be stuck): %v", err)
	}
	if !applied {
		t.Fatal("Migrate should apply the three skipped B4 drop migrations")
	}

	// ── Phase 4: post-B4 invariants ──────────────────────────────────────
	for _, table := range []string{"purge_jobs_by_file_version", "purge_jobs_by_state"} {
		if ok, err := tableExists(ctx, session, keyspace, table); err != nil || ok {
			t.Fatalf("post-B4: %s must be gone (exists=%v err=%v)", table, ok, err)
		}
	}
	if ok, err := columnExists(ctx, session, keyspace, "purge_jobs", "has_reconciliation_errors"); err != nil || ok {
		t.Fatalf("post-B4: has_reconciliation_errors must be gone (exists=%v err=%v)", ok, err)
	}
	for _, name := range []string{b4MetaSkipByVersion, b4MetaSkipByState, b4MetaSkipHasErrors} {
		ok, err := migrationApplied(ctx, session, name)
		if err != nil || !ok {
			t.Fatalf("post-B4: %s must be recorded (recorded=%v err=%v)", name, ok, err)
		}
	}

	// The seeded job row survives, still readable through the shrunken
	// projection (its legacy state string is normalized by the repo layer,
	// not the schema — the sweeper's ParsePurgeState safety net covers it).
	var state string
	if err := session.Query(
		`SELECT state FROM purge_jobs WHERE job_id = ?`, jobID,
	).WithContext(ctx).Scan(&state); err != nil {
		t.Fatalf("seeded purge_jobs row must survive the drops: %v", err)
	}
	if state != "DECREMENTED_WITH_ERRORS" {
		t.Fatalf("seeded row state = %q, want the stored legacy string (normalization is a repo concern)", state)
	}

	// ── Phase 5: idempotent re-boot ──────────────────────────────────────
	applied, err = Migrate(ctx, session, keyspace)
	if err != nil {
		t.Fatalf("second Migrate must be a clean no-op: %v", err)
	}
	if applied {
		t.Fatal("second Migrate must apply zero new migrations")
	}
}
