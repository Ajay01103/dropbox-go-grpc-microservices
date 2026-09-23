package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

// File represents a file metadata record
type File struct {
	FileID          string    `db:"file_id"`
	FolderID        string    `db:"folder_id"`
	Filename        string    `db:"filename"`
	SizeBytes       int64     `db:"size_bytes"`
	ContentType     string    `db:"content_type"`
	ContentHash     string    `db:"content_hash"` // SHA256 for dedup
	Version         int32     `db:"version"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
	OwnerID         string    `db:"owner_id"`
	ParentFolderID  string    `db:"parent_folder_id"`
	ThumbnailKey    string    `db:"thumbnail_key"`
	ThumbnailStatus string    `db:"thumbnail_status"`
	BlockHashList   []string  `db:"block_hash_list"`
	IsDeleted       bool      `db:"is_deleted"`
	DeletedAt       time.Time `db:"deleted_at"`
	DeletedBatchID  string    `db:"deleted_batch_id"`
	Current         bool      `db:"current"`
}

// MetadataRepo provides data access for file metadata using ScyllaDB
type MetadataRepo struct {
	session *gocql.Session
}

// NewMetadataRepo creates a MetadataRepo backed by a ScyllaDB session
func NewMetadataRepo(session *gocql.Session) *MetadataRepo {
	return &MetadataRepo{session: session}
}

// CreateFile inserts a new file record
func (r *MetadataRepo) CreateFile(ctx context.Context, folderID, ownerID, filename, contentType, contentHash string, blockHashList []string, sizeBytes int64) (File, error) {
	fileID := uuid.New().String()
	now := time.Now().UTC()

	file := File{
		FileID:        fileID,
		FolderID:      folderID,
		Filename:      filename,
		SizeBytes:     sizeBytes,
		ContentType:   contentType,
		ContentHash:   contentHash,
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
		OwnerID:       ownerID,
		BlockHashList: blockHashList,
		Current:       true,
	}

	if err := r.session.Query(
		`INSERT INTO files_by_folder (folder_id, file_id, filename, size_bytes, content_type, content_hash, block_hash_list, version, created_at, updated_at, owner_id, parent_folder_id, thumbnail_key, thumbnail_status, is_deleted, current)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		folderID, fileID, filename, sizeBytes, contentType, contentHash, blockHashList, 1, now, now, ownerID, folderID, "", "pending", false, true,
	).WithContext(ctx).Exec(); err != nil {
		return File{}, fmt.Errorf("insert file: %w", err)
	}
	if err := r.session.Query(
		`INSERT INTO files_by_id (file_id, folder_id, owner_id, filename, size_bytes, content_type, content_hash, block_hash_list, version, created_at, updated_at, thumbnail_key, thumbnail_status, is_deleted, current)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, folderID, ownerID, filename, sizeBytes, contentType, contentHash, blockHashList, 1, now, now, "", "pending", false, true,
	).WithContext(ctx).Exec(); err != nil {
		return File{}, fmt.Errorf("index file: %w", err)
	}
	if err := r.session.Query(
		`INSERT INTO files_by_folder_updated (folder_id, updated_at, file_id, owner_id, filename, size_bytes, content_type, content_hash, version, created_at, is_deleted, current)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		folderID, now, fileID, ownerID, filename, sizeBytes, contentType, contentHash, 1, now, false, true,
	).WithContext(ctx).Exec(); err != nil {
		return File{}, fmt.Errorf("index file sort order: %w", err)
	}

	// Thumbnail sweeper work list. Best-effort: the file row is authoritative
	// (thumbnail_status), so a failed insert only means THIS file can't be
	// recovered by the sweeper — the normal worker path is unaffected.
	_ = r.InsertThumbnailPending(ctx, ThumbnailPendingRef{
		FileID: fileID, OwnerID: ownerID, FileVersion: 1, CreatedAt: now,
	})

	return file, nil
}

// GetFile retrieves a single file by ID
func (r *MetadataRepo) GetFile(ctx context.Context, fileID, folderID string) (File, error) {
	var file File
	err := r.session.Query(
		`SELECT file_id, folder_id, filename, size_bytes, content_type, content_hash, version, created_at, owner_id, parent_folder_id, thumbnail_key, thumbnail_status, block_hash_list
		FROM files_by_folder WHERE folder_id = ? AND file_id = ? LIMIT 1`,
		folderID, fileID,
	).WithContext(ctx).Scan(
		&file.FileID, &file.FolderID, &file.Filename, &file.SizeBytes,
		&file.ContentType, &file.ContentHash, &file.Version, &file.CreatedAt,
		&file.OwnerID, &file.ParentFolderID, &file.ThumbnailKey, &file.ThumbnailStatus, &file.BlockHashList,
	)
	if err == gocql.ErrNotFound {
		return File{}, errors.New("file not found")
	}
	if err != nil {
		return File{}, fmt.Errorf("get file: %w", err)
	}
	return file, nil
}

// ListFolder returns all files in a folder with pagination
func (r *MetadataRepo) ListFolder(ctx context.Context, folderID string, limit int) ([]File, error) {
	var files []File
	iter := r.session.Query(
		`SELECT file_id, folder_id, filename, size_bytes, content_type, content_hash, version, created_at, owner_id, parent_folder_id, thumbnail_key, thumbnail_status, block_hash_list
		FROM files_by_folder WHERE folder_id = ? LIMIT ?`,
		folderID, limit,
	).WithContext(ctx).Iter()
	defer iter.Close()

	var file File
	for iter.Scan(
		&file.FileID, &file.FolderID, &file.Filename, &file.SizeBytes,
		&file.ContentType, &file.ContentHash, &file.Version, &file.CreatedAt,
		&file.OwnerID, &file.ParentFolderID, &file.ThumbnailKey, &file.ThumbnailStatus, &file.BlockHashList,
	) {
		files = append(files, file)
	}

	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("list folder: %w", err)
	}

	return files, nil
}

// DeleteFile removes a file record
func (r *MetadataRepo) DeleteFile(ctx context.Context, folderID, fileID string) error {
	return r.session.Query(
		`DELETE FROM files_by_folder WHERE folder_id = ? AND file_id = ?`,
		folderID, fileID,
	).WithContext(ctx).Exec()
}

func (r *MetadataRepo) SetThumbnail(ctx context.Context, folderID, fileID, thumbnailKey, thumbnailStatus string) error {
	file, err := r.GetFileByID(ctx, folderID, fileID)
	if err != nil {
		return err
	}
	return r.SetThumbnailByFolder(ctx, file.FolderID, fileID, thumbnailKey, thumbnailStatus)
}

func (r *MetadataRepo) SetThumbnailByFolder(ctx context.Context, folderID, fileID, thumbnailKey, thumbnailStatus string) error {
	if err := r.session.Query(
		`UPDATE files_by_folder SET thumbnail_key = ?, thumbnail_status = ? WHERE folder_id = ? AND file_id = ?`,
		thumbnailKey, thumbnailStatus, folderID, fileID,
	).WithContext(ctx).Exec(); err != nil {
		return err
	}
	id, err := parseID(fileID)
	if err != nil {
		return err
	}
	return r.session.Query(`UPDATE files_by_id SET thumbnail_key = ?, thumbnail_status = ? WHERE file_id = ?`, thumbnailKey, thumbnailStatus, id).WithContext(ctx).Exec()
}

// SetThumbnailLWT sets thumbnail fields with lightweight-transaction guards
// (IF EXISTS) on both projections and reports whether both rows were applied.
//
// It exists for the thumbnail worker: if a file row has vanished between the
// ObjectStored event and thumbnail completion (soft delete / purge raced the
// generation), the update is not applied and the caller can Ack the message
// instead of resurrecting a ghost row with a blind UPDATE.
//
// applied=false means at least one projection was missing. Unlike
// SetThumbnailByFolder this does NOT return an error for missing rows —
// missing is an expected, benign outcome here.
func (r *MetadataRepo) SetThumbnailLWT(ctx context.Context, folderID, fileID, thumbnailKey, thumbnailStatus string) (applied bool, err error) {
	id, idErr := parseID(fileID)
	if idErr != nil {
		return false, fmt.Errorf("parse file id: %w", idErr)
	}
	folderUUID, folderErr := parseID(folderID)
	if folderErr != nil {
		return false, fmt.Errorf("parse folder id: %w", folderErr)
	}

	folderApplied, err := r.session.Query(
		`UPDATE files_by_folder SET thumbnail_key = ?, thumbnail_status = ? WHERE folder_id = ? AND file_id = ? IF EXISTS`,
		thumbnailKey, thumbnailStatus, folderUUID, id,
	).WithContext(ctx).MapScanCAS(map[string]interface{}{})
	if err != nil {
		return false, fmt.Errorf("set thumbnail (files_by_folder): %w", err)
	}
	idApplied, err := r.session.Query(
		`UPDATE files_by_id SET thumbnail_key = ?, thumbnail_status = ? WHERE file_id = ? IF EXISTS`,
		thumbnailKey, thumbnailStatus, id,
	).WithContext(ctx).MapScanCAS(map[string]interface{}{})
	if err != nil {
		return false, fmt.Errorf("set thumbnail (files_by_id): %w", err)
	}
	return folderApplied && idApplied, nil
}

func (r *MetadataRepo) GetThumbnailStatus(ctx context.Context, folderID, fileID string) (string, string, error) {
	file, err := r.GetFileByID(ctx, folderID, fileID)
	if err != nil {
		return "", "", err
	}
	var thumbnailKey, thumbnailStatus string
	err = r.session.Query(
		`SELECT thumbnail_key, thumbnail_status FROM files_by_folder WHERE folder_id = ? AND file_id = ? LIMIT 1`,
		file.FolderID, fileID,
	).WithContext(ctx).Scan(&thumbnailKey, &thumbnailStatus)
	if err == gocql.ErrNotFound {
		return "", "", errors.New("file not found")
	}
	if err != nil {
		return "", "", fmt.Errorf("get thumbnail status: %w", err)
	}
	return thumbnailKey, thumbnailStatus, nil
}

// Folder represents a folder record
type Folder struct {
	FolderID       string    `db:"folder_id"`
	UserID         string    `db:"user_id"`
	OwnerID        string    `db:"owner_id"`
	ParentID       string    `db:"parent_id"`
	FolderName     string    `db:"folder_name"`
	PathCache      string    `db:"path_cache"`
	// AncestorIDs is the ordered chain of ancestor folder ids, root first and
	// direct parent last, excluding the folder itself. Empty for the root.
	// Backed by the ancestor_ids list<uuid> column (z18 migration); used by
	// breadcrumb resolution so a deep folder is O(1) reads, not O(depth).
	AncestorIDs    []string  `db:"ancestor_ids"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
	IsDeleted      bool      `db:"is_deleted"`
	DeletedAt      time.Time `db:"deleted_at"`
	DeletedBatchID string    `db:"deleted_batch_id"`
}

// FolderRepo provides data access for folder metadata using ScyllaDB
type FolderRepo struct {
	session *gocql.Session
}

// NewFolderRepo creates a FolderRepo backed by a ScyllaDB session
func NewFolderRepo(session *gocql.Session) *FolderRepo {
	return &FolderRepo{session: session}
}
