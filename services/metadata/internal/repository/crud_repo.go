package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

func (r *FolderRepo) RenameFolder(ctx context.Context, ownerID, folderID, name string) (Folder, error) {
	folder, err := r.GetFolder(ctx, ownerID, folderID)
	if err != nil {
		return Folder{}, err
	}
	id, _ := parseID(folderID)
	owner, _ := parseID(ownerID)
	now := time.Now().UTC()
	if err := r.session.Query(`UPDATE folders_by_id SET name = ?, updated_at = ? WHERE folder_id = ?`, name, now, id).WithContext(ctx).Exec(); err != nil {
		return Folder{}, err
	}
	if err := r.session.Query(`UPDATE folders_by_owner SET name = ?, updated_at = ? WHERE owner_id = ? AND folder_id = ?`, name, now, owner, id).WithContext(ctx).Exec(); err != nil {
		return Folder{}, err
	}
	if folder.ParentID != "" {
		parent, _ := parseID(folder.ParentID)
		if err := r.session.Query(`UPDATE folders_by_parent SET name = ?, updated_at = ? WHERE parent_id = ? AND folder_id = ?`, name, now, parent, id).WithContext(ctx).Exec(); err != nil {
			return Folder{}, err
		}
	}
	folder.FolderName, folder.UpdatedAt = name, now
	return folder, nil
}

func (r *FolderRepo) MoveFolder(ctx context.Context, ownerID, folderID, parentID string, destinationAncestors []string) (Folder, error) {
	folder, err := r.GetFolder(ctx, ownerID, folderID)
	if err != nil {
		return Folder{}, err
	}
	id, err := parseID(folderID)
	if err != nil {
		return Folder{}, err
	}
	owner, err := parseID(ownerID)
	if err != nil {
		return Folder{}, err
	}
	parent, err := parseID(parentID)
	if err != nil {
		return Folder{}, err
	}
	// New ancestor chain: destination's ancestors + the destination itself.
	newAncestors := make([]gocql.UUID, 0, len(destinationAncestors)+1)
	for _, a := range destinationAncestors {
		parsed, err := parseID(a)
		if err != nil {
			return Folder{}, fmt.Errorf("invalid ancestor id %q: %w", a, err)
		}
		newAncestors = append(newAncestors, parsed)
	}
	newAncestors = append(newAncestors, parent)

	now := time.Now().UTC()
	if folder.ParentID != "" {
		oldParent, _ := parseID(folder.ParentID)
		if err := r.session.Query(`DELETE FROM folders_by_parent WHERE parent_id = ? AND folder_id = ?`, oldParent, id).WithContext(ctx).Exec(); err != nil {
			return Folder{}, err
		}
	}
	if err := r.session.Query(`INSERT INTO folders_by_parent (parent_id, folder_id, owner_id, name, path_cache, created_at, updated_at, is_deleted, ancestor_ids) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, parent, id, owner, folder.FolderName, folder.PathCache, folder.CreatedAt, now, folder.IsDeleted, newAncestors).WithContext(ctx).Exec(); err != nil {
		return Folder{}, err
	}
	if err := r.session.Query(`UPDATE folders_by_id SET parent_id = ?, ancestor_ids = ?, updated_at = ? WHERE folder_id = ?`, parent, newAncestors, now, id).WithContext(ctx).Exec(); err != nil {
		return Folder{}, err
	}
	if err := r.session.Query(`UPDATE folders_by_owner SET parent_id = ?, ancestor_ids = ?, updated_at = ? WHERE owner_id = ? AND folder_id = ?`, parent, newAncestors, now, owner, id).WithContext(ctx).Exec(); err != nil {
		return Folder{}, err
	}
	folder.ParentID, folder.UpdatedAt = parent.String(), now
	folder.AncestorIDs = ancestorIDStrings(newAncestors)
	return folder, nil
}

func (r *FolderRepo) SetFolderDeleted(ctx context.Context, ownerID, folderID, batchID string, deleted bool) (Folder, error) {
	folder, err := r.GetFolder(ctx, ownerID, folderID)
	if err != nil {
		return Folder{}, err
	}
	id, _ := parseID(folderID)
	owner, _ := parseID(ownerID)
	now := time.Now().UTC()
	var deletedAt any
	var batch any
	if deleted {
		deletedAt, batch = now, batchID
	}
	for _, query := range []struct {
		statement string
		args      []any
	}{
		{`UPDATE folders_by_id SET is_deleted = ?, deleted_at = ?, deleted_batch_id = ?, updated_at = ? WHERE folder_id = ?`, []any{deleted, deletedAt, batch, now, id}},
		{`UPDATE folders_by_owner SET is_deleted = ?, deleted_at = ?, deleted_batch_id = ?, updated_at = ? WHERE owner_id = ? AND folder_id = ?`, []any{deleted, deletedAt, batch, now, owner, id}},
	} {
		if err := r.session.Query(query.statement, query.args...).WithContext(ctx).Exec(); err != nil {
			return Folder{}, err
		}
	}
	if folder.ParentID != "" {
		parent, _ := parseID(folder.ParentID)
		if err := r.session.Query(`UPDATE folders_by_parent SET is_deleted = ?, deleted_at = ?, deleted_batch_id = ?, updated_at = ? WHERE parent_id = ? AND folder_id = ?`, deleted, deletedAt, batch, now, parent, id).WithContext(ctx).Exec(); err != nil {
			return Folder{}, err
		}
	}
	folder.IsDeleted, folder.UpdatedAt, folder.DeletedBatchID = deleted, now, batchID
	if deleted {
		folder.DeletedAt = now
	} else {
		folder.DeletedAt = time.Time{}
	}
	return folder, nil
}

// ListFilesByFolder returns one page of a folder's files, newest first.
//
// files_by_folder_updated supplies the ORDER and the ids; each file's own row in
// files_by_id supplies everything the caller sees. The split is deliberate: the
// sort index is a projection, and a projection can lag — a soft delete bumps the
// file's updated_at, and earlier revisions of this service never rewrote the
// index at all. A listing that trusted the index's columns would show a renamed
// file's old name, a file that moved away as still present, and a purged file as
// live. Hydrating from the authoritative row leaves the index able to be wrong
// only about ordering and membership, and the placement check below turns even
// that into a skip rather than a ghost.
//
// A page can therefore come back with fewer than pageSize files: rows that are
// stale, deleted, moved away, purged, or duplicated are skipped after the scan
// has consumed their slot. PageState still advances past them, so the next page
// reaches the real remainder.
func (r *MetadataRepo) ListFilesByFolder(ctx context.Context, ownerID, folderID string, pageSize int, pageToken string, includeDeleted bool) ([]File, string, error) {
	if _, err := parseID(ownerID); err != nil {
		return nil, "", err
	}
	folder, err := parseID(folderID)
	if err != nil {
		return nil, "", err
	}
	// A listing of deleted files cannot come from the sort index: soft deletion
	// does not rewrite its clustering key, so an equality scan on is_deleted
	// would be a full-partition filter. The authoritative folder index carries
	// every row and is hydrated directly.
	if includeDeleted {
		return r.listFilesByFolderIncludingDeleted(ctx, ownerID, folder, pageSize)
	}

	state, err := decodePageState(pageToken)
	if err != nil {
		return nil, "", err
	}
	// Only the id is read: the clustering order IS the sort, and every other
	// column here would be a stale copy of what files_by_id already holds.
	iter := r.session.Query(`SELECT file_id FROM files_by_folder_updated WHERE folder_id = ?`, folder).
		PageSize(pageSize).PageState(state).WithContext(ctx).Iter()
	defer iter.Close()

	files := make([]File, 0, pageSize)
	// One file can own more than one row: Cassandra cannot update updated_at in
	// place, so a rename or move leaves the old clustering row behind until its
	// writer removes it. The first row wins — the file's data comes from
	// files_by_id, so a duplicate only costs a page slot.
	seen := make(map[gocql.UUID]struct{}, pageSize)
	for {
		var id gocql.UUID
		if !iter.Scan(&id) {
			break
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		// Hydration doubles as the liveness and ownership check: GetFileByID
		// returns no row for a purged file and rejects another owner's file.
		file, getErr := r.GetFileByID(ctx, ownerID, id.String())
		if getErr != nil {
			continue
		}
		// Placement check: the file must still live in this folder. The move path
		// removes a file's old-folder rows, but a row left by an interrupted
		// write (or by an older revision) must never list a file in a folder it
		// has left.
		if stored, parseErr := parseID(file.FolderID); parseErr != nil || stored != folder {
			continue
		}
		if !file.Current || file.IsDeleted {
			continue
		}
		files = append(files, file)
	}
	if err := iter.Close(); err != nil {
		return nil, "", fmt.Errorf("list files: %w", err)
	}
	return files, encodePageState(iter.PageState()), nil
}

// filesByFolderUpdatedInsert writes one row of a folder's updated_at-keyed file
// index — the ordering projection ListFilesByFolder reads. Nothing else consumes
// these rows.
//
// INVARIANT: a file's row carries the same updated_at as the file's own
// files_by_id.updated_at. Cassandra cannot update a clustering column in place,
// so every writer that bumps a file's updated_at must also move its row here
// (delete the old clustering position, insert the new one — see
// replaceFilesByFolderUpdated). A writer that skips that step leaves the older
// row behind: listings stay correct because they hydrate and de-duplicate, but
// the file can appear twice and its ordering stops tracking its updated_at.
//
// CreateFileWithBlockSizes, UpdateFileName, MoveFile and SetFileDeleted are the
// only writers of files_by_id.updated_at, and all four maintain this index.
const filesByFolderUpdatedInsert = `INSERT INTO files_by_folder_updated (folder_id, updated_at, file_id, owner_id, filename, size_bytes, content_type, content_hash, version, created_at, is_deleted, current) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// insertFilesByFolderUpdated writes a file's row in its folder's sort index at
// updatedAt.
func (r *MetadataRepo) insertFilesByFolderUpdated(ctx context.Context, file File, updatedAt time.Time) error {
	folder, err := parseID(file.FolderID)
	if err != nil {
		return fmt.Errorf("parse folder id: %w", err)
	}
	id, err := parseID(file.FileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	owner, err := parseID(file.OwnerID)
	if err != nil {
		return fmt.Errorf("parse owner id: %w", err)
	}
	if err := r.session.Query(filesByFolderUpdatedInsert,
		folder, updatedAt, id, owner, file.Filename, file.SizeBytes, file.ContentType,
		file.ContentHash, file.Version, file.CreatedAt, file.IsDeleted, file.Current,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("index file sort order: %w", err)
	}
	return nil
}

// deleteFilesByFolderUpdated removes a file's row from one folder's sort index.
// updatedAt must be the value the row was written under — the file's
// files_by_id.updated_at as read before the write — because updated_at is the
// clustering key.
func (r *MetadataRepo) deleteFilesByFolderUpdated(ctx context.Context, folderID, fileID string, updatedAt time.Time) error {
	folder, err := parseID(folderID)
	if err != nil {
		return fmt.Errorf("parse folder id: %w", err)
	}
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	if err := r.session.Query(
		`DELETE FROM files_by_folder_updated WHERE folder_id = ? AND updated_at = ? AND file_id = ?`,
		folder, updatedAt, id,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("remove file sort order row: %w", err)
	}
	return nil
}

// replaceFilesByFolderUpdated moves a file's ordering row to next's folder and
// timestamp. previous is the file as read before the mutation — its FolderID and
// UpdatedAt are the row's current clustering position — and next is the file
// after it.
//
// The insert happens first: a crash between the two steps then leaves a stale
// duplicate (which readers skip) instead of no row at all.
func (r *MetadataRepo) replaceFilesByFolderUpdated(ctx context.Context, previous, next File) error {
	if previous.FolderID == next.FolderID && previous.UpdatedAt.Equal(next.UpdatedAt) {
		return nil
	}
	if err := r.insertFilesByFolderUpdated(ctx, next, next.UpdatedAt); err != nil {
		return err
	}
	return r.deleteFilesByFolderUpdated(ctx, previous.FolderID, previous.FileID, previous.UpdatedAt)
}

// unboundedFetchSize is the driver fetch granularity when a caller asks for the
// whole partition. It bounds memory per round trip; it does not bound the
// result.
const unboundedFetchSize = 500

// listFilesByFolderIncludingDeleted reads the folder's authoritative file
// index and hydrates each record from files_by_id. The updated-time index is
// optimized for active listings and is not rewritten by soft deletion.
//
// A limit collects at most that many files; 0 means the whole partition. The
// unlimited form is what a permanent purge needs: stopping at one page would
// leave a folder's excess files behind, which is exactly the kind of silent
// partial delete that is indistinguishable from success to the caller.
func (r *MetadataRepo) listFilesByFolderIncludingDeleted(ctx context.Context, ownerID string, folder gocql.UUID, limit int) ([]File, string, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return nil, "", err
	}
	fetchSize := limit
	if fetchSize <= 0 {
		fetchSize = unboundedFetchSize
	}
	iter := r.session.Query(`SELECT file_id, owner_id FROM files_by_folder WHERE folder_id = ?`, folder).PageSize(fetchSize).WithContext(ctx).Iter()
	defer iter.Close()

	files := make([]File, 0, fetchSize)
	for {
		var id, fileOwner gocql.UUID
		if !iter.Scan(&id, &fileOwner) {
			break
		}
		if fileOwner != owner {
			continue
		}
		file, getErr := r.GetFileByID(ctx, ownerID, id.String())
		if getErr != nil || !file.Current {
			continue
		}
		files = append(files, file)
		if limit > 0 && len(files) >= limit {
			break
		}
	}
	if err := iter.Close(); err != nil {
		return nil, "", fmt.Errorf("list files including deleted: %w", err)
	}
	return files, "", nil
}

// ListAllFilesByFolder returns every file in the folder's authoritative index,
// deleted or not, with no page cap. A permanent purge cannot work from a
// paged listing: whatever the page cap drops is content the caller believes it
// deleted.
func (r *MetadataRepo) ListAllFilesByFolder(ctx context.Context, ownerID, folderID string) ([]File, error) {
	folder, err := parseID(folderID)
	if err != nil {
		return nil, fmt.Errorf("parse folder id: %w", err)
	}
	files, _, err := r.listFilesByFolderIncludingDeleted(ctx, ownerID, folder, 0)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (r *FolderRepo) ListBreadcrumbs(ctx context.Context, ownerID, folderID string) ([]Folder, error) {
	self, err := r.GetFolder(ctx, ownerID, folderID)
	if err != nil {
		return nil, err
	}
	// Self-heal: pre-migration rows have an empty ancestor_ids. Fall back to
	// the parent-chain walk, then rewrite this row so subsequent reads are O(1).
	if len(self.AncestorIDs) == 0 && self.ParentID != "" {
		chain, chainErr := r.breadcrumbsByChainWalk(ctx, ownerID, self)
		if chainErr != nil {
			return nil, chainErr
		}
		if healErr := r.UpdateFolderAncestors(ctx, ownerID, self.FolderID, folderIDs(chain[:len(chain)-1])); healErr != nil {
			// Non-fatal: the correct chain is still returned; the row heals on a later write.
			_ = healErr
		}
		return chain, nil
	}

	result := make([]Folder, 0, len(self.AncestorIDs)+1)
	if len(self.AncestorIDs) > 0 {
		ancestors, err := r.GetFoldersByIDs(ctx, ownerID, self.AncestorIDs)
		if err != nil {
			return nil, err
		}
		// Scylla's IN query returns rows in unspecified order; re-sort into
		// ancestor_ids order (root -> direct parent).
		byID := make(map[string]Folder, len(ancestors))
		for _, f := range ancestors {
			byID[f.FolderID] = f
		}
		for _, id := range self.AncestorIDs {
			if f, ok := byID[id]; ok {
				result = append(result, f)
			}
		}
	}
	return append(result, self), nil
}

// breadcrumbsByChainWalk resolves the ancestor chain by walking ParentID
// links one GetFolder at a time. Used only as the fallback for pre-migration
// rows with empty ancestor_ids; bounded by maxFolderDepth.
func (r *FolderRepo) breadcrumbsByChainWalk(ctx context.Context, ownerID string, self Folder) ([]Folder, error) {
	result := make([]Folder, 0, maxFolderDepth)
	current := self.ParentID
	for depth := 0; depth < maxFolderDepth && current != ""; depth++ {
		folder, err := r.GetFolder(ctx, ownerID, current)
		if err != nil {
			return nil, err
		}
		result = append([]Folder{folder}, result...)
		current = folder.ParentID
	}
	if current != "" {
		return nil, errors.New("folder depth exceeds limit")
	}
	return append(result, self), nil
}

// folderIDs extracts the FolderID of each folder, preserving order.
func folderIDs(folders []Folder) []string {
	ids := make([]string, len(folders))
	for i, f := range folders {
		ids[i] = f.FolderID
	}
	return ids
}

// UpdateFolderAncestors rewrites ancestor_ids for a folder in all three folder
// tables. Used by the lazy self-heal and by MoveFolder.
func (r *FolderRepo) UpdateFolderAncestors(ctx context.Context, ownerID, folderID string, ancestors []string) error {
	id, err := parseID(folderID)
	if err != nil {
		return err
	}
	owner, err := parseID(ownerID)
	if err != nil {
		return err
	}
	uuids := make([]gocql.UUID, 0, len(ancestors))
	for _, a := range ancestors {
		parsed, err := parseID(a)
		if err != nil {
			return fmt.Errorf("invalid ancestor id %q: %w", a, err)
		}
		uuids = append(uuids, parsed)
	}
	now := time.Now().UTC()
	if err := r.session.Query(`UPDATE folders_by_id SET ancestor_ids = ?, updated_at = ? WHERE folder_id = ?`, uuids, now, id).WithContext(ctx).Exec(); err != nil {
		return err
	}
	return r.session.Query(`UPDATE folders_by_owner SET ancestor_ids = ?, updated_at = ? WHERE owner_id = ? AND folder_id = ?`, uuids, now, owner, id).WithContext(ctx).Exec()
}
