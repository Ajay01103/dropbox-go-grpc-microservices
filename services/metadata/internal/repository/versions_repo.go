package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// ErrNoVersionRow reports that a file has no version-history row at all, which
// is the normal state for every file that predates editing (and for a file
// whose create path never wrote one). Callers treat it as "version 1,
// described by the file's own row".
var ErrNoVersionRow = errors.New("no file version row")

// FileVersion is one immutable entry of a file's version history. A row is
// written exactly once, by the commit that won its version number; nothing
// ever updates it. (file_id, version) is the whole identity of a set of bytes.
type FileVersion struct {
	FileID        string
	Version       int32
	BlockHashList []string
	BlockSize     int64
	SizeBytes     int64
	ContentHash   string
	ContentType   string
	Filename      string
	// SniffedType is pdf | docx | xlsx | "" — derived from the bytes, never
	// from the client-supplied content type. It gates editing per type.
	SniffedType string
	// BaseVersion is the version this one was derived from (0 for version 1
	// written by the create path).
	BaseVersion int32
	// Editor names the client editor that produced the bytes, so a bad
	// round-trip can be found and reverted later.
	Editor    string
	CommitID  string
	CreatedBy string
	CreatedAt time.Time
}

// FileHead points at the version a reader gets when no version is named. It is
// separate from files_by_id so that "which version is current" is one row and
// one compare-and-set, independent of the projections a listing reads.
type FileHead struct {
	FileID         string
	CurrentVersion int32
	ContentHash    string
	UpdatedAt      time.Time
}

const fileVersionColumns = `file_id, version, block_hash_list, block_size, size_bytes, content_hash, content_type, filename, sniffed_type, base_version, editor, commit_id, created_by, created_at`

// GetFileVersion loads one version of a file's history.
func (r *MetadataRepo) GetFileVersion(ctx context.Context, fileID string, version int32) (FileVersion, error) {
	id, err := parseID(fileID)
	if err != nil {
		return FileVersion{}, fmt.Errorf("parse file id: %w", err)
	}
	var (
		storedID, contentHash, contentType, filename, sniffedType, editor, commitID string
		storedVersion, baseVersion                                                 int32
		blockHashes                                                                []string
		blockSize, sizeBytes                                                       int64
		createdBy                                                                  gocql.UUID
		createdAt                                                                  time.Time
	)
	err = r.session.Query(
		`SELECT `+fileVersionColumns+` FROM file_versions WHERE file_id = ? AND version = ?`,
		id, version,
	).WithContext(ctx).Scan(
		&storedID, &storedVersion, &blockHashes, &blockSize, &sizeBytes, &contentHash,
		&contentType, &filename, &sniffedType, &baseVersion, &editor, &commitID, &createdBy, &createdAt,
	)
	if err == gocql.ErrNotFound {
		return FileVersion{}, ErrNoVersionRow
	}
	if err != nil {
		return FileVersion{}, fmt.Errorf("get file version: %w", err)
	}
	return FileVersion{
		FileID: storedID, Version: storedVersion, BlockHashList: blockHashes,
		BlockSize: blockSize, SizeBytes: sizeBytes, ContentHash: contentHash,
		ContentType: contentType, Filename: filename, SniffedType: sniffedType,
		BaseVersion: baseVersion, Editor: editor, CommitID: commitID,
		CreatedBy: createdBy.String(), CreatedAt: createdAt,
	}, nil
}

// GetFileHead loads the head pointer. ErrNoVersionRow means the file predates
// the version history and the caller should fall back to the file row's own
// version (see the lazy-migration note on ErrNoVersionRow).
func (r *MetadataRepo) GetFileHead(ctx context.Context, fileID string) (FileHead, error) {
	id, err := parseID(fileID)
	if err != nil {
		return FileHead{}, fmt.Errorf("parse file id: %w", err)
	}
	var (
		storedID      gocql.UUID
		current       int32
		contentHash   string
		updatedAtTime time.Time
	)
	err = r.session.Query(
		`SELECT file_id, current_version, content_hash, updated_at FROM file_head WHERE file_id = ?`,
		id,
	).WithContext(ctx).Scan(&storedID, &current, &contentHash, &updatedAtTime)
	if err == gocql.ErrNotFound {
		return FileHead{}, ErrNoVersionRow
	}
	if err != nil {
		return FileHead{}, fmt.Errorf("get file head: %w", err)
	}
	return FileHead{FileID: storedID.String(), CurrentVersion: current, ContentHash: contentHash, UpdatedAt: updatedAtTime}, nil
}

// InsertFileVersionIfAbsent writes a version row only if nobody else owns that
// version number yet. applied=false means another commit won the race, which
// is the whole conflict signal — the loser must not retry blindly.
func (r *MetadataRepo) InsertFileVersionIfAbsent(ctx context.Context, version FileVersion) (bool, error) {
	id, err := parseID(version.FileID)
	if err != nil {
		return false, fmt.Errorf("parse file id: %w", err)
	}
	owner, err := parseID(version.CreatedBy)
	if err != nil {
		return false, fmt.Errorf("parse created_by: %w", err)
	}
	createdAt := version.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	// MapScanCAS, not ScanCAS: a refused conditional INSERT returns [applied]
	// plus every column of the row that already owns the version number, and
	// ScanCAS with no destinations cannot absorb those extra columns (it fails
	// with "not enough columns to scan into" — see block_repo.go for the same
	// trap). The map form tolerates both shapes.
	casMap := make(map[string]interface{})
	applied, err := r.session.Query(
		`INSERT INTO file_versions (`+fileVersionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`,
		id, version.Version, version.BlockHashList, version.BlockSize, version.SizeBytes,
		version.ContentHash, version.ContentType, version.Filename, version.SniffedType,
		version.BaseVersion, version.Editor, version.CommitID, owner, createdAt,
	).WithContext(ctx).MapScanCAS(casMap)
	if err != nil {
		return false, fmt.Errorf("insert file version: %w", err)
	}
	return applied, nil
}

// AdvanceFileHead moves the head pointer forward to version. It is a
// compare-and-set on the observed value with a bounded retry, because the head
// may be advanced concurrently by another commit (in which case there is
// nothing left to do: a head ahead of our version means our row is history).
func (r *MetadataRepo) AdvanceFileHead(ctx context.Context, fileID string, version int32, contentHash string) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	now := time.Now().UTC()
	for attempt := 0; attempt < 5; attempt++ {
		head, headErr := r.GetFileHead(ctx, fileID)
		if errors.Is(headErr, ErrNoVersionRow) {
			// First version this file ever records: the insert is the CAS.
			casMap := make(map[string]interface{})
			applied, insertErr := r.session.Query(
				`INSERT INTO file_head (file_id, current_version, content_hash, updated_at) VALUES (?, ?, ?, ?) IF NOT EXISTS`,
				id, version, contentHash, now,
			).WithContext(ctx).MapScanCAS(casMap)
			if insertErr != nil {
				return fmt.Errorf("insert file head: %w", insertErr)
			}
			if applied {
				return nil
			}
			continue // someone inserted a head concurrently; re-read
		}
		if headErr != nil {
			return headErr
		}
		if head.CurrentVersion >= version {
			return nil // already at or past us — nothing to move
		}
		casMap := make(map[string]interface{})
		applied, casErr := r.session.Query(
			`UPDATE file_head SET current_version = ?, content_hash = ?, updated_at = ? WHERE file_id = ? IF current_version = ?`,
			version, contentHash, now, id, head.CurrentVersion,
		).WithContext(ctx).MapScanCAS(casMap)
		if casErr != nil {
			return fmt.Errorf("advance file head: %w", casErr)
		}
		if applied {
			return nil
		}
	}
	return errors.New("advance file head: too many concurrent updates")
}

// CommitVersionInput is one save: the bytes the editor produced, described as
// the ordered block list the commit must record.
type CommitVersionInput struct {
	FileID      string
	OwnerID     string
	BaseVersion int32
	BlockHashes []string
	BlockSize   int64
	SizeBytes   int64
	ContentHash string
	ContentType string
	Filename    string
	SniffedType string
	Editor      string
	CommitID    string
}

// CommitVersionRow is the arbiter of a save: it wins (file_id, base+1) with
// INSERT IF NOT EXISTS and, only if it won, advances the head pointer.
//
// Two editors saving the same base version therefore produce exactly one new
// version; the loser is told the head it lost to and can offer a conflicted
// copy instead of silently overwriting work. Returns applied=false with no
// side effects when another commit already took that version number.
func (r *MetadataRepo) CommitVersionRow(ctx context.Context, in CommitVersionInput) (bool, error) {
	version := in.BaseVersion + 1
	applied, err := r.InsertFileVersionIfAbsent(ctx, FileVersion{
		FileID:        in.FileID,
		Version:       version,
		BlockHashList: in.BlockHashes,
		BlockSize:     in.BlockSize,
		SizeBytes:     in.SizeBytes,
		ContentHash:   in.ContentHash,
		ContentType:   in.ContentType,
		Filename:      in.Filename,
		SniffedType:   in.SniffedType,
		BaseVersion:   in.BaseVersion,
		Editor:        in.Editor,
		CommitID:      in.CommitID,
		CreatedBy:     in.OwnerID,
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}
	if err := r.AdvanceFileHead(ctx, in.FileID, version, in.ContentHash); err != nil {
		// The version row is committed; the head repair path (and the next
		// commit) moves the pointer. Surfacing this as a conflict would be a
		// lie — the bytes are stored and immutable.
		return true, err
	}
	return true, nil
}

// ApplyHeadContent rewrites every projection that describes the file's CURRENT
// content to match a newly committed version: the authoritative row and its
// folder index, the folder's updated-at sort index, and the dense download
// manifest. History rows and the head pointer are not touched here.
//
// This is deliberately all-or-mostly-nothing in the caller's order (authoritative
// row first): a reader that sees the new block list also sees the new size and
// hash, and the manifest is rewritten to match rather than patched.
func (r *MetadataRepo) ApplyHeadContent(ctx context.Context, previous, next File, manifest FileManifestRow) error {
	id, err := parseID(next.FileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	folder, err := parseID(next.FolderID)
	if err != nil {
		return fmt.Errorf("parse folder id: %w", err)
	}
	if err := r.session.Query(
		`UPDATE files_by_id SET block_hash_list = ?, size_bytes = ?, content_hash = ?, content_type = ?, version = ?, updated_at = ?, thumbnail_key = ?, thumbnail_status = ?, content_family = ? WHERE file_id = ?`,
		next.BlockHashList, next.SizeBytes, next.ContentHash, next.ContentType,
		next.Version, next.UpdatedAt, next.ThumbnailKey, next.ThumbnailStatus, next.ContentFamily, id,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update file content: %w", err)
	}
	if err := r.session.Query(
		`UPDATE files_by_folder SET block_hash_list = ?, size_bytes = ?, content_hash = ?, content_type = ?, version = ?, updated_at = ?, thumbnail_key = ?, thumbnail_status = ?, content_family = ? WHERE folder_id = ? AND file_id = ?`,
		next.BlockHashList, next.SizeBytes, next.ContentHash, next.ContentType,
		next.Version, next.UpdatedAt, next.ThumbnailKey, next.ThumbnailStatus, next.ContentFamily, folder, id,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update folder file content: %w", err)
	}
	// The folder's updated-at sort index carries a copy of the file's fields,
	// so it has to be moved to the new timestamp like any other update.
	if err := r.replaceFilesByFolderUpdated(ctx, previous, next); err != nil {
		return err
	}
	// The manifest is one row per chunk: writing the new list without clearing
	// the partition first would leave the tail of a now-shorter file in place.
	if err := r.DeleteFileManifest(ctx, next.FileID); err != nil {
		return err
	}
	return r.WriteFileManifest(ctx, manifest)
}
