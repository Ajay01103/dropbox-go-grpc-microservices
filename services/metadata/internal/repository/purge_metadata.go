package repository

import (
	"context"
	"fmt"
	"time"
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
		{`DELETE FROM files_by_folder_updated WHERE folder_id = ? AND updated_at = ? AND file_id = ?`, []any{folderID, file.UpdatedAt, fileID}},
		{`DELETE FROM files_by_folder_name WHERE folder_id = ? AND filename = ? AND file_id = ?`, []any{folderID, file.Filename, fileID}},
		{`DELETE FROM files_by_folder_size WHERE folder_id = ? AND size_bytes = ? AND file_id = ?`, []any{folderID, file.SizeBytes, fileID}},

		// Folder item index tables — compound clustering keys must be fully specified.
		// PRIMARY KEY ((folder_id), name, item_type, item_id)
		{`DELETE FROM folder_items_by_name WHERE folder_id = ? AND name = ? AND item_type = ? AND item_id = ?`, []any{folderID, file.Filename, "file", fileID}},
		// PRIMARY KEY ((folder_id), updated_at, item_type, item_id)
		{`DELETE FROM folder_items_by_updated WHERE folder_id = ? AND updated_at = ? AND item_type = ? AND item_id = ?`, []any{folderID, file.UpdatedAt, "file", fileID}},
		// PRIMARY KEY ((folder_id), size_bytes, item_type, item_id)
		{`DELETE FROM folder_items_by_size WHERE folder_id = ? AND size_bytes = ? AND item_type = ? AND item_id = ?`, []any{folderID, file.SizeBytes, "file", fileID}},
	}
	for _, q := range queries {
		if err := r.session.Query(q.statement, q.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("remove file projection (%s): %w", q.statement[:40], err)
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
