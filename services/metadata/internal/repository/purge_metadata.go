package repository

import (
	"context"
	"fmt"
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
		{`DELETE FROM files_by_folder_name WHERE folder_id = ? AND filename = ? AND file_id = ?`, []any{folderID, file.Filename, fileID}},
		{`DELETE FROM files_by_folder_size WHERE folder_id = ? AND size_bytes = ? AND file_id = ?`, []any{folderID, file.SizeBytes, fileID}},

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
