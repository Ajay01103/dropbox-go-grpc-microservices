package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// RemoveFileProjections deletes every metadata projection for a purged file
// version. The operation is intentionally idempotent: deleting an absent row
// is treated as success so a retried purge can continue.
func (r *MetadataRepo) RemoveFileProjections(ctx context.Context, file File) error {
	fileID, err := uuid.Parse(file.FileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	folderID, err := uuid.Parse(file.FolderID)
	if err != nil {
		return fmt.Errorf("parse folder id: %w", err)
	}

	queries := []struct {
		statement string
		args      []any
	}{
		{`DELETE FROM files_by_folder WHERE folder_id = ? AND file_id = ?`, []any{folderID, fileID}},
		{`DELETE FROM files_by_id WHERE file_id = ?`, []any{fileID}},
		{`DELETE FROM files_by_folder_updated WHERE folder_id = ? AND updated_at = ? AND file_id = ?`, []any{folderID, file.UpdatedAt, fileID}},
		{`DELETE FROM files_by_folder_name WHERE folder_id = ? AND filename = ? AND file_id = ?`, []any{folderID, file.Filename, fileID}},
		{`DELETE FROM files_by_folder_size WHERE folder_id = ? AND size_bytes = ? AND file_id = ?`, []any{folderID, file.SizeBytes, fileID}},
	}
	for _, query := range queries {
		if err := r.session.Query(query.statement, query.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("remove file projection: %w", err)
		}
	}
	return nil
}
