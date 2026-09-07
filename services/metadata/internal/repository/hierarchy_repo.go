package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

const maxFolderDepth = 20

func encodePageState(state []byte) string {
	if len(state) == 0 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(state)
}

func decodePageState(token string) ([]byte, error) {
	if token == "" {
		return nil, nil
	}
	state, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, errors.New("invalid page token")
	}
	return state, nil
}

func parseID(value string) (gocql.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return gocql.UUID{}, fmt.Errorf("invalid id %q: %w", value, err)
	}
	return gocql.UUID(id), nil
}

// EnsureRootFolder creates the deterministic root folder used during the migration.
func (r *FolderRepo) EnsureRootFolder(ctx context.Context, ownerID string) (Folder, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return Folder{}, err
	}
	now := time.Now().UTC()
	queries := []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO folders_by_owner (owner_id, folder_id, parent_id, name, path_cache, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`, []any{owner, owner, nil, "", "/", now, now, false}},
		{`INSERT INTO folders_by_id (folder_id, owner_id, parent_id, name, path_cache, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`, []any{owner, owner, nil, "", "/", now, now, false}},
	}
	for _, query := range queries {
		if err := r.session.Query(query.statement, query.args...).WithContext(ctx).Exec(); err != nil {
			return Folder{}, fmt.Errorf("ensure root folder: %w", err)
		}
	}
	return r.GetFolder(ctx, ownerID, ownerID)
}

func (r *FolderRepo) GetFolder(ctx context.Context, ownerID, folderID string) (Folder, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return Folder{}, err
	}
	folder, err := parseID(folderID)
	if err != nil {
		return Folder{}, err
	}
	var result Folder
	var storedFolderID, storedOwnerID, storedParentID, storedDeletedBatchID []byte
	err = r.session.Query(`SELECT folder_id, owner_id, parent_id, name, path_cache, created_at, updated_at, is_deleted, deleted_at, deleted_batch_id FROM folders_by_id WHERE folder_id = ?`, folder).
		WithContext(ctx).Scan(&storedFolderID, &storedOwnerID, &storedParentID, &result.FolderName, &result.PathCache, &result.CreatedAt, &result.UpdatedAt, &result.IsDeleted, &result.DeletedAt, &storedDeletedBatchID)
	if err == gocql.ErrNotFound {
		return Folder{}, errors.New("folder not found")
	}
	if err != nil {
		return Folder{}, fmt.Errorf("get folder: %w", err)
	}
	result.FolderID = uuidBytesString(storedFolderID)
	result.OwnerID = uuidBytesString(storedOwnerID)
	result.ParentID = uuidBytesString(storedParentID)
	result.DeletedBatchID = uuidBytesString(storedDeletedBatchID)
	if result.OwnerID != owner.String() {
		return Folder{}, errors.New("folder not found")
	}
	return result, nil
}

func uuidBytesString(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	return gocql.UUID(value).String()
}

func (r *FolderRepo) CreateChildFolder(ctx context.Context, ownerID, parentID, name string) (Folder, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return Folder{}, err
	}
	parent, err := parseID(parentID)
	if err != nil {
		return Folder{}, err
	}
	folderID := gocql.UUID(uuid.New())
	now := time.Now().UTC()
	folder := Folder{FolderID: folderID.String(), OwnerID: owner.String(), ParentID: parent.String(), FolderName: name, CreatedAt: now, UpdatedAt: now}
	for _, query := range []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO folders_by_owner (owner_id, folder_id, parent_id, name, path_cache, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{owner, folderID, parent, name, "", now, now, false}},
		{`INSERT INTO folders_by_id (folder_id, owner_id, parent_id, name, path_cache, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{folderID, owner, parent, name, "", now, now, false}},
		{`INSERT INTO folders_by_parent (parent_id, folder_id, owner_id, name, path_cache, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{parent, folderID, owner, name, "", now, now, false}},
	} {
		if err := r.session.Query(query.statement, query.args...).WithContext(ctx).Exec(); err != nil {
			return Folder{}, fmt.Errorf("create folder: %w", err)
		}
	}
	return folder, nil
}

func (r *FolderRepo) ListChildren(ctx context.Context, ownerID, parentID string, pageSize int, pageToken string) ([]Folder, string, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return nil, "", err
	}
	parent, err := parseID(parentID)
	if err != nil {
		return nil, "", err
	}
	state, err := decodePageState(pageToken)
	if err != nil {
		return nil, "", err
	}
	query := r.session.Query(`SELECT folder_id, owner_id, parent_id, name, path_cache, created_at, updated_at, is_deleted, deleted_at, deleted_batch_id FROM folders_by_parent WHERE parent_id = ?`, parent).PageSize(pageSize).PageState(state).WithContext(ctx)
	iter := query.Iter()
	defer iter.Close()
	folders := make([]Folder, 0, pageSize)
	for {
		var folder Folder
		var folderID, folderOwner, folderParent, batch gocql.UUID
		if !iter.Scan(&folderID, &folderOwner, &folderParent, &folder.FolderName, &folder.PathCache, &folder.CreatedAt, &folder.UpdatedAt, &folder.IsDeleted, &folder.DeletedAt, &batch) {
			break
		}
		if folderOwner != owner {
			continue
		}
		folder.FolderID, folder.OwnerID, folder.ParentID = folderID.String(), folderOwner.String(), folderParent.String()
		if batch != (gocql.UUID{}) {
			folder.DeletedBatchID = batch.String()
		}
		folders = append(folders, folder)
	}
	if err := iter.Close(); err != nil {
		return nil, "", fmt.Errorf("list child folders: %w", err)
	}
	return folders, encodePageState(iter.PageState()), nil
}

func (r *MetadataRepo) GetFileByID(ctx context.Context, ownerID, fileID string) (File, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return File{}, err
	}
	file, err := parseID(fileID)
	if err != nil {
		return File{}, err
	}
	var result File
	var resultFileID, resultFolderID, resultOwnerID, resultDeletedBatchID []byte
	err = r.session.Query(`SELECT file_id, folder_id, owner_id, filename, size_bytes, content_type, content_hash, block_hash_list, version, created_at, updated_at, thumbnail_key, thumbnail_status, is_deleted, deleted_at, deleted_batch_id, current FROM files_by_id WHERE file_id = ?`, file).
		WithContext(ctx).Scan(&resultFileID, &resultFolderID, &resultOwnerID, &result.Filename, &result.SizeBytes, &result.ContentType, &result.ContentHash, &result.BlockHashList, &result.Version, &result.CreatedAt, &result.UpdatedAt, &result.ThumbnailKey, &result.ThumbnailStatus, &result.IsDeleted, &result.DeletedAt, &resultDeletedBatchID, &result.Current)
	result.FileID = uuidBytesString(resultFileID)
	result.FolderID = uuidBytesString(resultFolderID)
	result.OwnerID = uuidBytesString(resultOwnerID)
	result.DeletedBatchID = uuidBytesString(resultDeletedBatchID)
	if err == gocql.ErrNotFound || result.OwnerID != owner.String() {
		return File{}, errors.New("file not found")
	}
	if err != nil {
		return File{}, fmt.Errorf("get file: %w", err)
	}
	return result, nil
}

func (r *MetadataRepo) UpdateFileName(ctx context.Context, ownerID, fileID, name string) error {
	file, err := r.GetFileByID(ctx, ownerID, fileID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	id, _ := parseID(fileID)
	folder, _ := parseID(file.FolderID)
	if err := r.session.Query(`UPDATE files_by_folder SET filename = ?, updated_at = ? WHERE folder_id = ? AND file_id = ?`, name, now, folder, id).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("rename file: %w", err)
	}
	return r.session.Query(`UPDATE files_by_id SET filename = ?, updated_at = ? WHERE file_id = ?`, name, now, id).WithContext(ctx).Exec()
}

func (r *MetadataRepo) MoveFile(ctx context.Context, ownerID, fileID, folderID string) error {
	file, err := r.GetFileByID(ctx, ownerID, fileID)
	if err != nil {
		return err
	}
	oldFolder, err := parseID(file.FolderID)
	if err != nil {
		return err
	}
	newFolder, err := parseID(folderID)
	if err != nil {
		return err
	}
	id, err := parseID(fileID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := r.session.Query(`INSERT INTO files_by_folder (folder_id, file_id, filename, size_bytes, content_type, content_hash, block_hash_list, version, created_at, updated_at, owner_id, parent_folder_id, thumbnail_key, thumbnail_status, is_deleted, deleted_at, deleted_batch_id, current) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, newFolder, id, file.Filename, file.SizeBytes, file.ContentType, file.ContentHash, file.BlockHashList, file.Version, file.CreatedAt, now, ownerID, newFolder, file.ThumbnailKey, file.ThumbnailStatus, file.IsDeleted, file.DeletedAt, file.DeletedBatchID, file.Current).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("move file insert: %w", err)
	}
	if err := r.session.Query(`DELETE FROM files_by_folder WHERE folder_id = ? AND file_id = ?`, oldFolder, id).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("move file delete: %w", err)
	}
	return r.session.Query(`UPDATE files_by_id SET folder_id = ?, updated_at = ? WHERE file_id = ?`, newFolder, now, id).WithContext(ctx).Exec()
}

func (r *MetadataRepo) SetFileDeleted(ctx context.Context, ownerID, fileID, batchID string, deleted bool) (File, error) {
	file, err := r.GetFileByID(ctx, ownerID, fileID)
	if err != nil {
		return File{}, err
	}
	id, _ := parseID(fileID)
	folder, _ := parseID(file.FolderID)
	now := time.Now().UTC()
	var deletedAt any
	var batch any
	if deleted {
		deletedAt, batch = now, batchID
	}
	if err := r.session.Query(`UPDATE files_by_folder SET is_deleted = ?, deleted_at = ?, deleted_batch_id = ?, updated_at = ? WHERE folder_id = ? AND file_id = ?`, deleted, deletedAt, batch, now, folder, id).WithContext(ctx).Exec(); err != nil {
		return File{}, err
	}
	if err := r.session.Query(`UPDATE files_by_id SET is_deleted = ?, deleted_at = ?, deleted_batch_id = ?, updated_at = ? WHERE file_id = ?`, deleted, deletedAt, batch, now, id).WithContext(ctx).Exec(); err != nil {
		return File{}, err
	}
	file.IsDeleted, file.UpdatedAt = deleted, now
	file.DeletedBatchID = batchID
	if deleted {
		file.DeletedAt = now
	} else {
		file.DeletedAt = time.Time{}
	}
	return file, nil
}
