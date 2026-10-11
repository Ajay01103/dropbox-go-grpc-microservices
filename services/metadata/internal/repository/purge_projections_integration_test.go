//go:build integration

// Exercises RemoveFileProjections against real Scylla. The point is the query
// SHAPE, not the purge state machine: the residual sweep over the
// updated_at-keyed sort indexes used to address files_by_folder_updated by
// item_id, a column that table does not have. Every permanent delete therefore
// failed with "Unrecognized name item_id", and the sweeper spent its whole
// retry budget on an error no retry could fix before marking the job
// permanently failed.
//
//	go test -tags integration ./internal/repository/...
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	mdb "github.com/Ajay01103/go-dropbox/metadata/db"
)

func purgeTestSession(t *testing.T) *gocql.Session {
	t.Helper()

	// Bounded connect: an unreachable cluster must SKIP the test, not hang it
	// (db.Connect retries forever on a context with no deadline).
	connectCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := mdb.Connect(connectCtx, mdb.Config{
		// 127.0.0.1, not "localhost": on Windows localhost resolves to ::1
		// first and the container's published CQL port is IPv4-only.
		Hosts:             []string{"127.0.0.1"},
		Port:              9042,
		Datacenter:        "datacenter1",
		Username:          "cassandra",
		Password:          "cassandra",
		Consistency:       gocql.LocalQuorum,
		ReplicationFactor: 1,
	})
	if err != nil {
		t.Skipf("ScyllaDB unavailable: %v", err)
	}
	t.Cleanup(session.Close)
	return session
}

func TestIntegrationRemoveFileProjections(t *testing.T) {
	session := purgeTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := mdb.Migrate(ctx, session, mdb.Keyspace); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewMetadataRepo(session)

	folderID := uuid.NewString()
	ownerID := uuid.NewString()
	file, err := repo.CreateFileWithBlockSizes(ctx, folderID, ownerID, "clip.mp4", "video/mp4", "it-purge-hash",
		[]string{"it-purge-block"}, []int64{8 << 20}, 8<<20, 0)
	if err != nil {
		t.Fatalf("CreateFileWithBlockSizes: %v", err)
	}
	fileUUID := mustTestUUID(t, file.FileID)
	folderUUID := mustTestUUID(t, folderID)
	ownerUUID := mustTestUUID(t, ownerID)
	t.Cleanup(func() {
		_ = repo.RemoveFileProjections(context.Background(), file)
	})

	// CreateFileWithBlockSizes writes files_by_folder, files_by_id,
	// files_by_folder_updated, file_manifest and a Tier B registry row. The
	// remaining projections are written by the folder-items indexer and the
	// activity feed; seed one of each so both residual scans have something to
	// find and something to delete.
	eventAt := time.Now().UTC()
	for _, insert := range []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO folder_items_by_updated (folder_id, updated_at, item_type, item_id, owner_id, name, size_bytes, content_type, created_at, is_deleted, current)
		  VALUES (?, ?, 'file', ?, ?, ?, ?, ?, ?, false, true)`,
			[]any{folderUUID, eventAt, fileUUID, ownerUUID, "clip.mp4", int64(8 << 20), "video/mp4", eventAt}},
		{`INSERT INTO folder_items_by_name (folder_id, name, item_type, item_id, owner_id, size_bytes, content_type, created_at, updated_at, is_deleted, current)
		  VALUES (?, ?, 'file', ?, ?, ?, ?, ?, ?, false, true)`,
			[]any{folderUUID, "clip.mp4", fileUUID, ownerUUID, int64(8 << 20), "video/mp4", eventAt, eventAt}},
		{`INSERT INTO folder_items_by_size (folder_id, size_bytes, item_type, item_id, owner_id, name, content_type, created_at, updated_at, is_deleted, current)
		  VALUES (?, ?, 'file', ?, ?, ?, ?, ?, ?, false, true)`,
			[]any{folderUUID, int64(8 << 20), fileUUID, ownerUUID, "clip.mp4", "video/mp4", eventAt, eventAt}},
	} {
		if err := session.Query(insert.statement, insert.args...).WithContext(ctx).Exec(); err != nil {
			t.Fatalf("seed projection: %v", err)
		}
	}
	if err := session.Query(
		`INSERT INTO recent_items_by_owner (owner_id, event_at, item_id, item_type, folder_id, name, content_type, size_bytes, is_deleted)
		 VALUES (?, ?, ?, 'file', ?, ?, ?, ?, false)`,
		ownerUUID, eventAt, fileUUID, folderUUID, "clip.mp4", "video/mp4", int64(8<<20),
	).WithContext(ctx).Exec(); err != nil {
		t.Fatalf("seed recent item: %v", err)
	}

	// The deletion under test. Before the fix this returned
	// "scan files_by_folder_updated for purge: Unrecognized name item_id".
	if err := repo.RemoveFileProjections(ctx, file); err != nil {
		t.Fatalf("RemoveFileProjections: %v", err)
	}

	// Idempotent: a retried purge re-runs the same deletion.
	if err := repo.RemoveFileProjections(ctx, file); err != nil {
		t.Fatalf("RemoveFileProjections (retry): %v", err)
	}

	// Every projection the purge is responsible for must be gone. The sweep
	// queries here are intentionally id-less: they ask "does ANY row for this
	// folder still name this file", which is the ghost the purge exists to
	// prevent.
	checks := []struct {
		name      string
		statement string
		args      []any
	}{
		{"files_by_folder", `SELECT file_id FROM files_by_folder WHERE folder_id = ? AND file_id = ?`, []any{folderUUID, fileUUID}},
		{"files_by_id", `SELECT file_id FROM files_by_id WHERE file_id = ?`, []any{fileUUID}},
		{"files_by_folder_updated", `SELECT file_id FROM files_by_folder_updated WHERE folder_id = ? AND file_id = ? ALLOW FILTERING`, []any{folderUUID, fileUUID}},
		{"folder_items_by_updated", `SELECT item_id FROM folder_items_by_updated WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{folderUUID, fileUUID}},
		{"folder_items_by_name", `SELECT item_id FROM folder_items_by_name WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{folderUUID, fileUUID}},
		{"folder_items_by_size", `SELECT item_id FROM folder_items_by_size WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{folderUUID, fileUUID}},
		{"file_manifest", `SELECT file_id FROM file_manifest WHERE file_id = ?`, []any{fileUUID}},
		{"stitched_objects", `SELECT file_id FROM stitched_objects WHERE file_id = ?`, []any{fileUUID}},
		// The work index has to go with the registry row, or the stitcher keeps
		// re-claiming a file that no longer exists.
		{"stitched_objects_by_state", `SELECT file_id FROM stitched_objects_by_state WHERE state = 'pending' AND file_id = ?`, []any{fileUUID}},
		{"recent_items_by_owner", `SELECT item_id FROM recent_items_by_owner WHERE owner_id = ? ALLOW FILTERING`, []any{ownerUUID}},
	}
	for _, check := range checks {
		iter := session.Query(check.statement, check.args...).WithContext(ctx).Iter()
		remaining := 0
		var scanned gocql.UUID
		for iter.Scan(&scanned) {
			remaining++
		}
		if err := iter.Close(); err != nil {
			t.Fatalf("%s: %v", check.name, err)
		}
		if remaining != 0 {
			t.Errorf("%s still holds %d row(s) for the purged file", check.name, remaining)
		}
	}
}

func mustTestUUID(t *testing.T, value string) gocql.UUID {
	t.Helper()
	parsed, err := gocql.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return parsed
}
