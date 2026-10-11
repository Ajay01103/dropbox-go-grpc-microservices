package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/gocql/gocql"
)

// ManifestBlock is one entry of a file's dense download manifest: the
// content-addressed block hash plus its exact byte size. Chunk index i is
// ordered; chunk sizes are needed to compute byte offsets for Range requests
// without per-block GetBlock lookups (the N+1 a bare block_hash_list forces).
type ManifestBlock struct {
	Index     int
	Hash      string
	SizeBytes int64
}

// FileManifestRow is the header + ordered blocks of a file's download
// manifest, stored densely in the file_manifest table (one row per chunk).
// Content-addressed and immutable per file version; written once at
// CreateFile and removed by the purge coordinator.
type FileManifestRow struct {
	FileID      string
	TotalSize   int64
	ContentType string
	ContentHash string
	Blocks      []ManifestBlock
}

// WriteFileManifest replaces the manifest of fileID in a single batch.
// The header fields are duplicated onto every row so a reader needs only the
// one partition and no separate header lookup.
func (r *MetadataRepo) WriteFileManifest(ctx context.Context, manifest FileManifestRow) error {
	fileID, err := parseID(manifest.FileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	if len(manifest.Blocks) == 0 {
		return errors.New("manifest must have at least one block")
	}
	batch := r.session.NewBatch(gocql.LoggedBatch).WithContext(ctx)
	for _, block := range manifest.Blocks {
		batch.Query(
			`INSERT INTO file_manifest (file_id, chunk_index, hash, size_bytes, total_size, content_type, content_hash) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fileID, block.Index, block.Hash, block.SizeBytes, manifest.TotalSize, manifest.ContentType, manifest.ContentHash,
		)
	}
	if err := r.session.ExecuteBatch(batch); err != nil {
		return fmt.Errorf("write file manifest: %w", err)
	}
	return nil
}

// GetFileManifest loads the header + ordered blocks of a file's manifest.
// Returns an error wrapping gocql.ErrNotFound when no manifest exists.
func (r *MetadataRepo) GetFileManifest(ctx context.Context, fileID string) (FileManifestRow, error) {
	id, err := parseID(fileID)
	if err != nil {
		return FileManifestRow{}, fmt.Errorf("parse file id: %w", err)
	}
	iter := r.session.Query(
		`SELECT chunk_index, hash, size_bytes, total_size, content_type, content_hash FROM file_manifest WHERE file_id = ?`,
		id,
	).WithContext(ctx).Iter()
	defer iter.Close()

	var manifest FileManifestRow
	manifest.FileID = fileID
	var chunkIndex int
	var blockHash string
	var blockSize int64
	for iter.Scan(&chunkIndex, &blockHash, &blockSize, &manifest.TotalSize, &manifest.ContentType, &manifest.ContentHash) {
		manifest.Blocks = append(manifest.Blocks, ManifestBlock{Index: chunkIndex, Hash: blockHash, SizeBytes: blockSize})
	}
	if err := iter.Close(); err != nil {
		return FileManifestRow{}, fmt.Errorf("read file manifest: %w", err)
	}
	if len(manifest.Blocks) == 0 {
		return FileManifestRow{}, fmt.Errorf("file manifest: %w", gocql.ErrNotFound)
	}
	return manifest, nil
}

// DeleteFileManifest removes (idempotently) the manifest partition of a
// purged file so manifest rows never outlive their file.
func (r *MetadataRepo) DeleteFileManifest(ctx context.Context, fileID string) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	return r.session.Query(`DELETE FROM file_manifest WHERE file_id = ?`, id).WithContext(ctx).Exec()
}
