package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

// RemoveFileProjections deletes every metadata projection for a purged file
// version. The operation is intentionally idempotent: deleting an absent row
// is treated as success so a retried purge can continue.
func (r *MetadataRepo) RemoveFileProjections(ctx context.Context, file File) error {
	// parseID returns gocql.UUID which is the type gocql requires for uuid columns.
	// Using google/uuid.UUID directly causes "can not marshal uuid.UUID into uuid".
	fileID, err := parseID(file.FileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	folderID, err := parseID(file.FolderID)
	if err != nil {
		return fmt.Errorf("parse folder id: %w", err)
	}
	ownerID, err := parseID(file.OwnerID)
	if err != nil {
		return fmt.Errorf("parse owner id: %w", err)
	}

	queries := []struct {
		statement string
		args      []any
	}{
		// Core file tables
		{`DELETE FROM files_by_folder WHERE folder_id = ? AND file_id = ?`, []any{folderID, fileID}},
		{`DELETE FROM files_by_id WHERE file_id = ?`, []any{fileID}},
		// Note: files_by_folder_updated and folder_items_by_updated are NOT
		// point-deleted here. Both are keyed by updated_at, which drifts
		// (soft delete bumps it in files_by_id without rewriting these
		// projections), so a point-delete can silently miss. They are
		// removed by the residual sweep below instead.
		//
		// The files_by_folder_name / files_by_folder_size deletes that used to
		// live here are gone with the tables themselves (see migration
		// z23_drop_unused_file_sort_indexes): no revision of this service writes
		// them any more, so there is nothing to remove and nothing to fail
		// against a dropped table.

		// Folder item index tables — compound clustering keys must be fully specified.
		// PRIMARY KEY ((folder_id), name, item_type, item_id)
		{`DELETE FROM folder_items_by_name WHERE folder_id = ? AND name = ? AND item_type = ? AND item_id = ?`, []any{folderID, file.Filename, "file", fileID}},
		// PRIMARY KEY ((folder_id), size_bytes, item_type, item_id)
		{`DELETE FROM folder_items_by_size WHERE folder_id = ? AND size_bytes = ? AND item_type = ? AND item_id = ?`, []any{folderID, file.SizeBytes, "file", fileID}},

		// Dense download manifest (z20) — one partition per file.
		{`DELETE FROM file_manifest WHERE file_id = ?`, []any{fileID}},
	}
	for _, q := range queries {
		if err := r.session.Query(q.statement, q.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("remove file projection (%s): %w", q.statement[:40], err)
		}
	}

	// Tier B state (z21 registry + z22 work index). They go together: dropping
	// the registry row stops a presigned consolidated object from serving a
	// purged file, and dropping the work-index entry keeps the stitcher from
	// spending claims on a file that no longer exists. The consolidated object
	// itself is left for the block GC sweep to reap.
	if err := NewStitchRepo(r.session).Delete(ctx, file.FileID); err != nil {
		return fmt.Errorf("remove stitch record: %w", err)
	}

	// Residual sweep for the updated_at-keyed sort indexes: scan the file's
	// rows within the folder partition (one partition per folder, so this is
	// cheap) and delete every one, regardless of which timestamp is stored.
	// Timestamp-independent by construction — no drift can leave a ghost.
	for _, index := range []updatedAtIndex{
		// PK ((folder_id), updated_at, file_id) — the file-side sort index.
		{table: "files_by_folder_updated", idCol: "file_id"},
		// PK ((folder_id), updated_at, item_type, item_id) — a mixed
		// file + folder index, so it needs the item_type narrowing.
		{table: "folder_items_by_updated", idCol: "item_id", filterItemType: true},
	} {
		if err := r.deleteAllByFile(ctx, `folder_id`, index, folderID, fileID, `updated_at`); err != nil {
			return err
		}
	}

	// Delete every recent-activity event for this file. The table clusters by
	// event_at so we must scan the owner partition and delete each matching row.
	// PRIMARY KEY ((owner_id), event_at, item_id)
	iter := r.session.Query(
		`SELECT event_at FROM recent_items_by_owner WHERE owner_id = ? AND item_id = ? ALLOW FILTERING`,
		ownerID, fileID,
	).WithContext(ctx).Iter()
	defer iter.Close()

	// event_at is a timestamp column — must scan into time.Time, not interface{}.
	var eventAts []time.Time
	var eventAt time.Time
	for iter.Scan(&eventAt) {
		eventAts = append(eventAts, eventAt)
	}
	if err := iter.Close(); err != nil {
		return fmt.Errorf("scan recent item events for purge: %w", err)
	}
	for _, ea := range eventAts {
		if err := r.session.Query(
			`DELETE FROM recent_items_by_owner WHERE owner_id = ? AND event_at = ? AND item_id = ?`,
			ownerID, ea, fileID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("delete recent item event: %w", err)
		}
	}

	return nil
}

// FolderSubtree returns the folder itself followed by every descendant, in
// breadth-first order. The walk reads folders_by_parent, so it sees
// soft-deleted children too — a subtree delete has to take those with it.
//
// A folder id is globally unique, so this walk is owner-independent; callers
// still scope every write by the owner they were authorized for.
func (r *MetadataRepo) FolderSubtree(ctx context.Context, folderID string) ([]string, error) {
	root, err := parseID(folderID)
	if err != nil {
		return nil, fmt.Errorf("parse folder id: %w", err)
	}
	seen := map[gocql.UUID]bool{root: true}
	subtree := []string{root.String()}
	for i := 0; i < len(subtree); i++ {
		current, err := parseID(subtree[i])
		if err != nil {
			return nil, fmt.Errorf("parse folder id: %w", err)
		}
		iter := r.session.Query(
			`SELECT folder_id FROM folders_by_parent WHERE parent_id = ?`, current,
		).WithContext(ctx).Iter()
		var child gocql.UUID
		for iter.Scan(&child) {
			if !seen[child] {
				seen[child] = true
				subtree = append(subtree, child.String())
			}
		}
		if err := iter.Close(); err != nil {
			return nil, fmt.Errorf("walk child folders of %s: %w", subtree[i], err)
		}
	}
	return subtree, nil
}

// RemoveFolderProjections removes the folder metadata for an entire subtree:
// the folder rows themselves and every folder-item index row that names one of
// them, including the row that lists the subtree root inside its surviving
// parent. It is idempotent, so a retried purge can run it again.
//
// Files are NOT touched here — each file's own purge removes its rows and its
// object-storage content. What this closes is the other half of a folder
// delete: without it a permanently deleted folder keeps its folders_by_owner,
// folders_by_parent and folder_items_* rows, so it stays visible in the trash
// listing and in its parent's item index even after the caller was told the
// delete succeeded.
func (r *MetadataRepo) RemoveFolderProjections(ctx context.Context, ownerID, folderID string) error {
	owner, err := parseID(ownerID)
	if err != nil {
		return fmt.Errorf("parse owner id: %w", err)
	}
	root, err := parseID(folderID)
	if err != nil {
		return fmt.Errorf("parse folder id: %w", err)
	}
	subtree, err := r.FolderSubtree(ctx, folderID)
	if err != nil {
		return err
	}

	// Read every row before touching anything. The walk discovers folders
	// through folders_by_parent, which the deletions below destroy, so a purge
	// that died half-way could no longer find what it had left behind. Holding
	// the ids and parents up front is what makes the remainder re-runnable.
	folders := make([]purgedFolder, 0, len(subtree))
	for _, subtreeFolderID := range subtree {
		id, parseErr := parseID(subtreeFolderID)
		if parseErr != nil {
			return fmt.Errorf("parse folder id: %w", parseErr)
		}
		stored, rowErr := r.FolderRowByID(ctx, subtreeFolderID)
		switch {
		case rowErr != nil && !isFolderNotFound(rowErr):
			return fmt.Errorf("load folder for purge: %w", rowErr)
		case rowErr == nil && stored.OwnerID != owner.String():
			// Not this owner's folder. The walk follows parent ids and folder
			// ids are globally unique, so reaching one here means the data was
			// never ours to purge — skip it rather than delete by id.
			continue
		}
		folders = append(folders, purgedFolder{id: subtreeFolderID, uuid: id, parent: stored.ParentID, exists: rowErr == nil})
	}
	if len(folders) == 0 {
		return nil
	}

	// Phase 1 — item indexes. These are keyed by ids this function already
	// holds, so they are safe to sweep first, and sweeping them first means a
	// crash leaves the folder rows (the walk's structure) intact for the retry.
	for _, folder := range folders {
		if err := r.deleteFolderItemRows(ctx, folder.uuid, nil); err != nil {
			return err
		}
	}

	// Phase 2 — the subtree root's item row in the parent that SURVIVES the
	// purge. Everything else in that partition must stay, so the sweep is
	// filtered to the subtree's ids.
	if folders[0].uuid == root && folders[0].exists && folders[0].parent != "" {
		parent, parseErr := parseID(folders[0].parent)
		if parseErr != nil {
			return fmt.Errorf("parse parent folder id: %w", parseErr)
		}
		purged := make(map[gocql.UUID]bool, len(folders))
		for _, folder := range folders {
			purged[folder.uuid] = true
		}
		if err := r.deleteFolderItemRows(ctx, parent, purged); err != nil {
			return err
		}
	}

	// Phase 3 — the folder rows themselves. This is the step that makes the
	// subtree unreachable, so it runs only once nothing above can still fail on
	// a row this function needs to find again.
	for _, folder := range folders {
		deletes := []folderDelete{
			{statement: `DELETE FROM folders_by_id WHERE folder_id = ?`, args: []any{folder.uuid}},
			{statement: `DELETE FROM folders_by_owner WHERE owner_id = ? AND folder_id = ?`, args: []any{owner, folder.uuid}},
		}
		// folders_by_parent is keyed by the PARENT, so the row lives in whichever
		// partition the folder is listed under.
		if folder.parent != "" {
			parent, parseErr := parseID(folder.parent)
			if parseErr != nil {
				return fmt.Errorf("parse parent folder id: %w", parseErr)
			}
			deletes = append(deletes, folderDelete{
				statement: `DELETE FROM folders_by_parent WHERE parent_id = ? AND folder_id = ?`,
				args:      []any{parent, folder.uuid},
			})
		}
		for _, del := range deletes {
			if err := r.session.Query(del.statement, del.args...).WithContext(ctx).Exec(); err != nil {
				return fmt.Errorf("remove folder projection (%s): %w", del.statement[:40], err)
			}
		}
	}
	return nil
}

// purgedFolder is one folder the sweep has decided it may delete, with the
// parent id captured while the row was still readable.
type purgedFolder struct {
	id     string
	uuid   gocql.UUID
	parent string
	// exists is false when the row is already gone: a retry after a partial
	// purge still has item rows to clean, but no folder row to delete.
	exists bool
}

// isFolderNotFound reports whether a folder read failed because there is no such
// row. The repository has no sentinel for this (every caller in the service
// layer matches on the message), so the purge matches it the same way.
func isFolderNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

// folderDelete is one point delete against a folder projection table.
type folderDelete struct {
	statement string
	args      []any
}

// folderItemIndex names one of the mixed file+folder item indexes: every one is
// partitioned by folder_id and clusters by (sortColumn, item_type, item_id).
type folderItemIndex struct {
	table      string
	sortColumn string
}

// folderItemIndexes are the tables that index a folder's contents (files and
// subfolders alike).
var folderItemIndexes = []folderItemIndex{
	{table: "folder_items_by_name", sortColumn: "name"},
	{table: "folder_items_by_size", sortColumn: "size_bytes"},
	{table: "folder_items_by_updated", sortColumn: "updated_at"},
}

// deleteFolderItemRows removes rows from one folder's partitions across every
// item index. A nil only removes the whole partition (the folder itself is
// being purged, so everything inside it goes); a set removes just the rows for
// those item ids, leaving the folder's other contents alone.
//
// MapScan is used because the sort column's Go type differs per index (text,
// bigint, timestamp) and the scanned value is passed straight back as a bind
// argument for the delete — reconstructing those keys by hand is what makes a
// point delete here fragile.
func (r *MetadataRepo) deleteFolderItemRows(ctx context.Context, folderID gocql.UUID, only map[gocql.UUID]bool) error {
	for _, index := range folderItemIndexes {
		iter := r.session.Query(
			fmt.Sprintf(`SELECT %s, item_type, item_id FROM %s WHERE folder_id = ?`, index.sortColumn, index.table),
			folderID,
		).WithContext(ctx).Iter()
		type itemRow struct {
			sortKey  any
			itemType string
			itemID   gocql.UUID
		}
		var rows []itemRow
		for {
			row := make(map[string]any, 3)
			if !iter.MapScan(row) {
				break
			}
			itemType, ok := row["item_type"].(string)
			if !ok {
				return fmt.Errorf("scan %s: item_type is %T", index.table, row["item_type"])
			}
			itemID, ok := row["item_id"].(gocql.UUID)
			if !ok {
				return fmt.Errorf("scan %s: item_id is %T", index.table, row["item_id"])
			}
			if only != nil && !only[itemID] {
				continue
			}
			rows = append(rows, itemRow{sortKey: row[index.sortColumn], itemType: itemType, itemID: itemID})
		}
		if err := iter.Close(); err != nil {
			return fmt.Errorf("scan %s for folder purge: %w", index.table, err)
		}
		for _, row := range rows {
			if err := r.session.Query(
				fmt.Sprintf(`DELETE FROM %s WHERE folder_id = ? AND %s = ? AND item_type = ? AND item_id = ?`, index.table, index.sortColumn),
				folderID, row.sortKey, row.itemType, row.itemID,
			).WithContext(ctx).Exec(); err != nil {
				return fmt.Errorf("delete %s row for folder purge: %w", index.table, err)
			}
		}
	}
	return nil
}

// updatedAtIndex describes one updated_at-keyed sort index for the residual
// sweep. Every column the sweep touches has to be named exactly as the table
// declares it: the files_by_folder_* indexes cluster by file_id and carry no
// item_type column, while folder_items_by_* cluster by item_id and mix files
// with folders. Addressing the wrong column fails the whole purge ("Unrecognized
// name item_id"), which is why the column lives in this struct instead of
// being hardcoded in the query builder.
type updatedAtIndex struct {
	table          string
	idCol          string
	filterItemType bool
}

// deleteAllByFile removes every row for one file within a partition of an
// updated_at-keyed sort index. It scans the file's clustering rows with
// ALLOW FILTERING (one partition) and deletes each, so the removal works no
// matter which updated_at value the row was written under.
func (r *MetadataRepo) deleteAllByFile(ctx context.Context, partitionCol string, index updatedAtIndex, folderID, fileID gocql.UUID, tsCol string) error {
	typeFilter := ""
	if index.filterItemType {
		typeFilter = " AND item_type = 'file'"
	}
	iter := r.session.Query(
		fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ?%s AND %s = ? ALLOW FILTERING`, tsCol, index.table, partitionCol, typeFilter, index.idCol),
		folderID, fileID,
	).WithContext(ctx).Iter()
	var ts time.Time
	var stamps []time.Time
	for iter.Scan(&ts) {
		stamps = append(stamps, ts)
	}
	if err := iter.Close(); err != nil {
		return fmt.Errorf("scan %s for purge: %w", index.table, err)
	}
	for _, t := range stamps {
		if err := r.session.Query(
			fmt.Sprintf(`DELETE FROM %s WHERE %s = ? AND %s = ?%s AND %s = ?`, index.table, partitionCol, tsCol, typeFilter, index.idCol),
			folderID, t, fileID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("delete %s row for purge: %w", index.table, err)
		}
	}
	return nil
}
