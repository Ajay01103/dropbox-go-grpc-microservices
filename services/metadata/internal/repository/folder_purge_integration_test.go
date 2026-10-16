//go:build integration

// Exercises the folder half of a permanent delete against real Scylla.
//
// A permanent folder delete used to stop at the folder row: the subtree stayed
// in folders_by_parent/folders_by_owner, the item indexes kept naming the
// deleted folders, and the trash listing therefore kept showing a folder the
// caller had been told was gone. This test seeds the same rows the service
// writes (folders_by_* through the folder repo, folder_items_* through the
// items repo) and asserts every one of them is removed — including the row
// that lists the subtree root inside its SURVIVING parent, which is the one a
// naive "sweep the subtree's own partitions" pass leaves behind.
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

func TestIntegrationRemoveFolderProjections(t *testing.T) {
	session := purgeTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := mdb.Migrate(ctx, session, mdb.Keyspace); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewMetadataRepo(session)
	folderRepo := NewFolderRepo(session)
	itemsRepo := NewItemsRepo(session)

	owner := uuid.NewString()
	root, err := folderRepo.EnsureRootFolder(ctx, owner)
	if err != nil {
		t.Fatalf("EnsureRootFolder: %v", err)
	}
	// The subtree under test: root/Doomed/Doomed/nested.
	doomed, err := folderRepo.CreateChildFolder(ctx, owner, root.FolderID, root.AncestorIDs, "Doomed")
	if err != nil {
		t.Fatalf("CreateChildFolder(Doomed): %v", err)
	}
	nested, err := folderRepo.CreateChildFolder(ctx, owner, doomed.FolderID, doomed.AncestorIDs, "nested")
	if err != nil {
		t.Fatalf("CreateChildFolder(nested): %v", err)
	}
	// A sibling that must survive untouched.
	kept, err := folderRepo.CreateChildFolder(ctx, owner, root.FolderID, root.AncestorIDs, "Kept")
	if err != nil {
		t.Fatalf("CreateChildFolder(Kept): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, folderID := range []string{root.FolderID, doomed.FolderID, nested.FolderID, kept.FolderID} {
			id, parseErr := gocql.ParseUUID(folderID)
			if parseErr == nil {
				_ = session.Query(`DELETE FROM folders_by_id WHERE folder_id = ?`, id).WithContext(cleanupCtx).Exec()
			}
		}
		// folders_by_parent is keyed by the PARENT, so a child only leaves the
		// table if its own parent partition is swept. Without this the shared
		// dev cluster accumulates orphan rows that no folders_by_id row backs.
		for _, edge := range [][2]string{{root.FolderID, doomed.FolderID}, {doomed.FolderID, nested.FolderID}, {root.FolderID, kept.FolderID}} {
			parent, parentErr := gocql.ParseUUID(edge[0])
			child, childErr := gocql.ParseUUID(edge[1])
			if parentErr == nil && childErr == nil {
				_ = session.Query(`DELETE FROM folders_by_parent WHERE parent_id = ? AND folder_id = ?`, parent, child).WithContext(cleanupCtx).Exec()
			}
		}
		_ = session.Query(`DELETE FROM folders_by_owner WHERE owner_id = ?`, mustTestUUID(t, owner)).WithContext(cleanupCtx).Exec()
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	// The service indexes every folder inside its parent's partition; the root
	// partition is the one that must keep listing "Kept" afterwards.
	for _, folder := range []Folder{doomed, nested, kept} {
		item := FolderItem{
			FolderID: folder.ParentID, ItemType: "folder", ItemID: folder.FolderID,
			Name: folder.FolderName, OwnerID: owner, CreatedAt: now, UpdatedAt: now, Current: true,
		}
		if err := itemsRepo.UpsertItem(ctx, item); err != nil {
			t.Fatalf("index folder %s: %v", folder.FolderName, err)
		}
	}

	// Three files in the nested folder, plus the item rows the listing reads.
	var fileIDs []string
	for i := 0; i < 3; i++ {
		file, err := repo.CreateFileWithBlockSizes(ctx, nested.FolderID, owner, "clip.mp4", "video/mp4",
			"it-folder-purge-hash", []string{"it-folder-purge-block"}, []int64{8 << 20}, 8<<20, 0)
		if err != nil {
			t.Fatalf("CreateFileWithBlockSizes: %v", err)
		}
		fileIDs = append(fileIDs, file.FileID)
		if err := itemsRepo.UpsertItem(ctx, FolderItem{
			FolderID: nested.FolderID, ItemType: "file", ItemID: file.FileID,
			Name: file.Filename, SizeBytes: file.SizeBytes, ContentType: file.ContentType,
			OwnerID: owner, CreatedAt: now, UpdatedAt: now, Current: true,
		}); err != nil {
			t.Fatalf("index file %s: %v", file.FileID, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, fileID := range fileIDs {
			if file, getErr := repo.GetFileByID(cleanupCtx, owner, fileID); getErr == nil {
				_ = repo.RemoveFileProjections(cleanupCtx, file)
			}
		}
	})

	// The walk has to find the whole subtree, root first (its parent is the one
	// outside the subtree that still references it).
	subtree, err := repo.FolderSubtree(ctx, doomed.FolderID)
	if err != nil {
		t.Fatalf("FolderSubtree: %v", err)
	}
	want := []string{doomed.FolderID, nested.FolderID}
	if len(subtree) != len(want) || subtree[0] != want[0] || subtree[1] != want[1] {
		t.Fatalf("FolderSubtree = %v, want %v", subtree, want)
	}

	// A purge must not work from a paged listing: with a limit the walk stops
	// early, and the uncapped form the purge uses returns everything.
	limited, _, err := repo.listFilesByFolderIncludingDeleted(ctx, owner, mustTestUUID(t, nested.FolderID), 1)
	if err != nil {
		t.Fatalf("paged listing: %v", err)
	}
	all, err := repo.ListAllFilesByFolder(ctx, owner, nested.FolderID)
	if err != nil {
		t.Fatalf("ListAllFilesByFolder: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("paged listing returned %d files, want 1", len(limited))
	}
	if len(all) != len(fileIDs) {
		t.Fatalf("uncapped listing returned %d files, want %d", len(all), len(fileIDs))
	}

	if err := repo.RemoveFolderProjections(ctx, owner, doomed.FolderID); err != nil {
		t.Fatalf("RemoveFolderProjections: %v", err)
	}
	// Idempotent: a retried purge re-runs the same removal.
	if err := repo.RemoveFolderProjections(ctx, owner, doomed.FolderID); err != nil {
		t.Fatalf("RemoveFolderProjections (retry): %v", err)
	}

	// The folders are gone, the sibling is not.
	for _, gone := range []Folder{doomed, nested} {
		if _, err := folderRepo.GetFolder(ctx, owner, gone.FolderID); err == nil {
			t.Errorf("folder %s still resolves after purge", gone.FolderName)
		}
	}
	if _, err := folderRepo.GetFolder(ctx, owner, kept.FolderID); err != nil {
		t.Errorf("sibling folder %s was collateral damage: %v", kept.FolderName, err)
	}
	children, _, err := folderRepo.ListChildren(ctx, owner, root.FolderID, 100, "")
	if err != nil {
		t.Fatalf("ListChildren(root): %v", err)
	}
	if len(children) != 1 || children[0].FolderID != kept.FolderID {
		t.Errorf("root children = %v, want just the surviving sibling", children)
	}

	// Row-level sweep: nothing may remain under either deleted folder id, and
	// the surviving parent must keep exactly its own item.
	rows := []struct {
		name      string
		statement string
		args      []any
	}{
		{"folders_by_id", `SELECT folder_id FROM folders_by_id WHERE folder_id = ?`, []any{mustTestUUID(t, doomed.FolderID)}},
		{"folders_by_id (nested)", `SELECT folder_id FROM folders_by_id WHERE folder_id = ?`, []any{mustTestUUID(t, nested.FolderID)}},
		{"folders_by_owner", `SELECT folder_id FROM folders_by_owner WHERE owner_id = ? AND folder_id = ?`, []any{mustTestUUID(t, owner), mustTestUUID(t, doomed.FolderID)}},
		{"folders_by_parent (root listing)", `SELECT folder_id FROM folders_by_parent WHERE parent_id = ? AND folder_id = ?`, []any{mustTestUUID(t, root.FolderID), mustTestUUID(t, doomed.FolderID)}},
		{"folders_by_parent (own children)", `SELECT folder_id FROM folders_by_parent WHERE parent_id = ?`, []any{mustTestUUID(t, doomed.FolderID)}},
		{"folder_items_by_name (parent partition)", `SELECT item_id FROM folder_items_by_name WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{mustTestUUID(t, doomed.FolderID), mustTestUUID(t, nested.FolderID)}},
		{"folder_items_by_updated (parent partition)", `SELECT item_id FROM folder_items_by_updated WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{mustTestUUID(t, doomed.FolderID), mustTestUUID(t, nested.FolderID)}},
		{"folder_items_by_size (parent partition)", `SELECT item_id FROM folder_items_by_size WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{mustTestUUID(t, doomed.FolderID), mustTestUUID(t, nested.FolderID)}},
		{"folder_items_by_name (own partition)", `SELECT item_id FROM folder_items_by_name WHERE folder_id = ?`, []any{mustTestUUID(t, nested.FolderID)}},
		{"folder_items_by_updated (own partition)", `SELECT item_id FROM folder_items_by_updated WHERE folder_id = ?`, []any{mustTestUUID(t, nested.FolderID)}},
		{"folder_items_by_size (own partition)", `SELECT item_id FROM folder_items_by_size WHERE folder_id = ?`, []any{mustTestUUID(t, nested.FolderID)}},
		// The gap this test exists for: the subtree root's own row in the
		// partition of the parent that survives the purge.
		{"folder_items_by_name (surviving parent)", `SELECT item_id FROM folder_items_by_name WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{mustTestUUID(t, root.FolderID), mustTestUUID(t, doomed.FolderID)}},
		{"folder_items_by_updated (surviving parent)", `SELECT item_id FROM folder_items_by_updated WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{mustTestUUID(t, root.FolderID), mustTestUUID(t, doomed.FolderID)}},
		{"folder_items_by_size (surviving parent)", `SELECT item_id FROM folder_items_by_size WHERE folder_id = ? AND item_id = ? ALLOW FILTERING`, []any{mustTestUUID(t, root.FolderID), mustTestUUID(t, doomed.FolderID)}},
	}
	for _, check := range rows {
		if remaining := countRows(t, session, check.statement, check.args, ctx); remaining != 0 {
			t.Errorf("%s still holds %d row(s) for the purged subtree", check.name, remaining)
		}
	}

	// ...while the sibling's index row for root is still there. A sweep that
	// dropped the whole parent partition would pass every check above.
	if remaining := countRows(t, session,
		`SELECT item_id FROM folder_items_by_name WHERE folder_id = ?`,
		[]any{mustTestUUID(t, root.FolderID)}, ctx); remaining != 1 {
		t.Errorf("root item index holds %d row(s), want the surviving sibling only", remaining)
	}

	// Files are the coordinator's business, not this sweep's: the folder purge
	// must leave the authoritative file rows for the queued file jobs to find
	// (and their content to revoke), not strand them by deleting the record
	// the job was built from.
	for _, fileID := range fileIDs {
		if _, err := repo.GetFileByID(ctx, owner, fileID); err != nil {
			t.Errorf("file %s was dropped by the folder sweep: %v", fileID, err)
		}
	}
}

func countRows(t *testing.T, session *gocql.Session, statement string, args []any, ctx context.Context) int {
	t.Helper()
	iter := session.Query(statement, args...).WithContext(ctx).Iter()
	remaining := 0
	var scanned gocql.UUID
	for iter.Scan(&scanned) {
		remaining++
	}
	if err := iter.Close(); err != nil {
		t.Fatalf("count rows (%s): %v", statement, err)
	}
	return remaining
}
