package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// thumbnail_pending is the thumbnail sweeper's work list (B2a table). A row
// is inserted by CreateFile and deleted by the v2 thumbnail worker when
// generation finishes (ready OR permanent failure). A row that survives —
// because the FileStored publish was lost or the worker died — is re-published
// by the thumbnail sweeper. Rows self-clean after their 7d TTL.
//
// The bucket is the UTC hour of created_at, so the sweeper scans a bounded
// window instead of a full-table scan.

// ThumbnailBucket formats the UTC hour bucket key for thumbnail_pending.
func ThumbnailBucket(t time.Time) string { return t.UTC().Format("2006010215") }

// ThumbnailPendingRef is one row of the thumbnail work list.
type ThumbnailPendingRef struct {
	Bucket      string
	FileID      string
	OwnerID     string
	FileVersion int64
	CreatedAt   time.Time
}

// InsertThumbnailPending records that a file awaits thumbnail generation.
// Best-effort by design: the file row is authoritative (thumbnail_status),
// so a failed insert only means the sweeper can't recover this particular
// file — the normal worker path is unaffected. Callers log and continue.
func (r *MetadataRepo) InsertThumbnailPending(ctx context.Context, ref ThumbnailPendingRef) error {
	id, err := parseID(ref.FileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	owner, err := parseID(ref.OwnerID)
	if err != nil {
		return fmt.Errorf("parse owner id: %w", err)
	}
	return r.session.Query(
		`INSERT INTO thumbnail_pending (bucket, file_id, owner_id, file_version, created_at) VALUES (?, ?, ?, ?, ?)`,
		ThumbnailBucket(ref.CreatedAt), id, owner, ref.FileVersion, ref.CreatedAt,
	).WithContext(ctx).Exec()
}

// DeleteThumbnailPending removes the work-list row once thumbnail generation
// has finished for a file (successfully or with a permanent failure). The
// bucket must be the same hour bucket the row was inserted under.
func (r *MetadataRepo) DeleteThumbnailPending(ctx context.Context, bucket, fileID string) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	return r.session.Query(
		`DELETE FROM thumbnail_pending WHERE bucket = ? AND file_id = ?`,
		bucket, id,
	).WithContext(ctx).Exec()
}

// ListThumbnailPending scans the given buckets of the thumbnail work list.
// Filtering by age happens in the caller.
func (r *MetadataRepo) ListThumbnailPending(ctx context.Context, buckets []string) ([]ThumbnailPendingRef, error) {
	var out []ThumbnailPendingRef
	for _, bucket := range buckets {
		iter := r.session.Query(
			`SELECT file_id, owner_id, file_version, created_at FROM thumbnail_pending WHERE bucket = ?`,
			bucket,
		).WithContext(ctx).Iter()
		var fileID, ownerID gocql.UUID
		var version int64
		var createdAt time.Time
		for iter.Scan(&fileID, &ownerID, &version, &createdAt) {
			out = append(out, ThumbnailPendingRef{
				Bucket: bucket, FileID: fileID.String(), OwnerID: ownerID.String(),
				FileVersion: version, CreatedAt: createdAt,
			})
		}
		if err := iter.Close(); err != nil && err != gocql.ErrNotFound {
			return nil, fmt.Errorf("scan thumbnail_pending (%s): %w", bucket, err)
		}
	}
	return out, nil
}
