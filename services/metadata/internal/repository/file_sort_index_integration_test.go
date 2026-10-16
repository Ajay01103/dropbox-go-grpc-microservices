//go:build integration

// Exercises the per-folder updated_at sort index (files_by_folder_updated)
// against real Scylla, end to end over rename, move, soft delete and restore.
//
// The index used to be written only at file creation, so ListFilesByFolder —
// the only reader — went stale in two visible ways: a renamed file kept its old
// name (the listing returned the index's copy of the row), and a moved file
// stayed listed in the folder it had left (its ghost guard only checked that the
// file still existed, not that it still lived there).
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

func TestIntegrationFileSortIndexFollowsRenameMoveAndDelete(t *testing.T) {
	session := purgeTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if _, err := mdb.Migrate(ctx, session, mdb.Keyspace); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewMetadataRepo(session)

	folderA := uuid.NewString()
	folderB := uuid.NewString()
	ownerID := uuid.NewString()
	file, err := repo.CreateFileWithBlockSizes(ctx, folderA, ownerID, "notes.txt", "text/plain", "it-sort-hash",
		[]string{"it-sort-block"}, []int64{1024}, 1024, 0)
	if err != nil {
		t.Fatalf("CreateFileWithBlockSizes: %v", err)
	}
	fileID := file.FileID
	t.Cleanup(func() {
		// Read the file back first: a move leaves the struct's FolderID behind,
		// and the purge addresses projections by the file's current folder.
		current, err := repo.GetFileByID(context.Background(), ownerID, fileID)
		if err != nil {
			current = file
		}
		_ = repo.RemoveFileProjections(context.Background(), current)
	})

	list := func(folderID string) []File {
		t.Helper()
		files, _, err := repo.ListFilesByFolder(ctx, ownerID, folderID, 50, "", false)
		if err != nil {
			t.Fatalf("ListFilesByFolder(%s): %v", folderID, err)
		}
		return files
	}
	indexRows := func(folderID string) int { return indexRowsForFile(t, session, folderID, fileID) }

	// Creation: one file, one index row, listed under its own name.
	if files := list(folderA); len(files) != 1 || files[0].Filename != "notes.txt" {
		t.Fatalf("after create, folder A lists %v, want one notes.txt", filenames(files))
	}
	if got := indexRows(folderA); got != 1 {
		t.Fatalf("after create, %d index rows in A, want 1", got)
	}

	// Rename: the listing must show the new name (hydrated from files_by_id, not
	// the index's copy) and the old clustering row must be gone, not duplicated.
	if err := repo.UpdateFileName(ctx, ownerID, fileID, "renamed.txt"); err != nil {
		t.Fatalf("UpdateFileName: %v", err)
	}
	if files := list(folderA); len(files) != 1 || files[0].Filename != "renamed.txt" {
		t.Fatalf("after rename, folder A lists %v, want one renamed.txt", filenames(files))
	}
	if got := indexRows(folderA); got != 1 {
		t.Fatalf("after rename, %d index rows in A, want 1 (old row not removed)", got)
	}

	// Move: the file leaves A's listing and index, and joins B's.
	if err := repo.MoveFile(ctx, ownerID, fileID, folderB); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if files := list(folderA); len(files) != 0 {
		t.Fatalf("after move, folder A still lists %v", filenames(files))
	}
	if got := indexRows(folderA); got != 0 {
		t.Fatalf("after move, %d index rows left in A, want 0", got)
	}
	files := list(folderB)
	if len(files) != 1 || files[0].Filename != "renamed.txt" {
		t.Fatalf("after move, folder B lists %v, want one renamed.txt", filenames(files))
	}
	if mustTestUUID(t, files[0].FolderID) != mustTestUUID(t, folderB) {
		t.Fatalf("moved file reports folder %s, want %s", files[0].FolderID, folderB)
	}
	if got := indexRows(folderB); got != 1 {
		t.Fatalf("after move, %d index rows in B, want 1", got)
	}

	// Soft delete and restore: the listing follows, and the index keeps exactly
	// one row across the cycle (a delete that did not move its row along would
	// strand the old timestamp for the next rename to miss).
	if _, err := repo.SetFileDeleted(ctx, ownerID, fileID, uuid.NewString(), true); err != nil {
		t.Fatalf("SetFileDeleted(true): %v", err)
	}
	if files := list(folderB); len(files) != 0 {
		t.Fatalf("after soft delete, folder B still lists %v", filenames(files))
	}
	deleted, _, err := repo.ListFilesByFolder(ctx, ownerID, folderB, 50, "", true)
	if err != nil {
		t.Fatalf("ListFilesByFolder(includeDeleted): %v", err)
	}
	if len(deleted) != 1 || !deleted[0].IsDeleted {
		t.Fatalf("includeDeleted listing = %v, want the one deleted file", filenames(deleted))
	}
	if _, err := repo.SetFileDeleted(ctx, ownerID, fileID, "", false); err != nil {
		t.Fatalf("SetFileDeleted(false): %v", err)
	}
	if files := list(folderB); len(files) != 1 {
		t.Fatalf("after restore, folder B lists %v, want one entry", filenames(files))
	}
	if got := indexRows(folderB); got != 1 {
		t.Fatalf("after delete/restore cycle, %d index rows in B, want 1", got)
	}

	// Rename again after the cycle: still one row, so the delete path did not
	// strand a timestamp the rename could not find.
	if err := repo.UpdateFileName(ctx, ownerID, fileID, "final.txt"); err != nil {
		t.Fatalf("UpdateFileName: %v", err)
	}
	if got := indexRows(folderB); got != 1 {
		t.Fatalf("after second rename, %d index rows in B, want 1", got)
	}
	if files := list(folderB); len(files) != 1 || files[0].Filename != "final.txt" {
		t.Fatalf("after second rename, folder B lists %v, want one final.txt", filenames(files))
	}

	// Last, a row left behind by an older revision (or by an interrupted write):
	// the reader must ignore both its stale copy of the row and its placement.
	// This is the exact shape of the reported bug — a wrong timestamp in the
	// index plus a filename that no longer matches the file. The seeded rows are
	// deliberately never cleaned up by the write path, which is why this case
	// runs after every row-count assertion above.
	seedStaleIndexRow(t, session, folderB, fileID, ownerID, "stale-copy.txt", time.Now().UTC().Add(-time.Hour))
	seedStaleIndexRow(t, session, folderA, fileID, ownerID, "left-behind.txt", time.Now().UTC().Add(-2*time.Hour))
	stale := list(folderB)
	if len(stale) != 1 {
		t.Fatalf("a stale duplicate row made folder B list %v, want exactly one entry", filenames(stale))
	}
	if stale[0].Filename != "final.txt" {
		t.Fatalf("listing returned the index's stale filename %q, want the authoritative final.txt", stale[0].Filename)
	}
	if left := list(folderA); len(left) != 0 {
		t.Fatalf("a stale row for the folder the file left made A list %v", filenames(left))
	}
}

// indexRowsForFile counts the index rows a single file owns in a folder
// partition — the measure of whether the sort index still holds one row per file
// or has accumulated stale clustering positions.
func indexRowsForFile(t *testing.T, session *gocql.Session, folderID, fileID string) int {
	t.Helper()
	iter := session.Query(
		`SELECT updated_at FROM files_by_folder_updated WHERE folder_id = ? AND file_id = ? ALLOW FILTERING`,
		mustTestUUID(t, folderID), mustTestUUID(t, fileID),
	).Iter()
	defer iter.Close()

	count := 0
	var updatedAt time.Time
	for iter.Scan(&updatedAt) {
		count++
	}
	if err := iter.Close(); err != nil {
		t.Fatalf("count index rows: %v", err)
	}
	return count
}

// seedStaleIndexRow writes a row straight into the sort index at an arbitrary
// timestamp, mimicking what an older revision of the service left behind.
func seedStaleIndexRow(t *testing.T, session *gocql.Session, folderID, fileID, ownerID, filename string, updatedAt time.Time) {
	t.Helper()
	if err := session.Query(
		`INSERT INTO files_by_folder_updated (folder_id, updated_at, file_id, owner_id, filename, size_bytes, content_type, content_hash, version, created_at, is_deleted, current)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mustTestUUID(t, folderID), updatedAt, mustTestUUID(t, fileID), mustTestUUID(t, ownerID),
		filename, 1024, "text/plain", "it-sort-hash", 1, updatedAt, false, true,
	).Exec(); err != nil {
		t.Fatalf("seed stale index row: %v", err)
	}
}

// filenames renders a listing for failure messages.
func filenames(files []File) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Filename)
	}
	return names
}
