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

func (r *MetadataRepo) ListFilesByFolder(ctx context.Context, ownerID, folderID string, pageSize int, pageToken string, includeDeleted bool) ([]File, string, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return nil, "", err
	}
	folder, err := parseID(folderID)
	if err != nil {
		return nil, "", err
	}
	if includeDeleted {
		return r.listFilesByFolderIncludingDeleted(ctx, ownerID, folder, pageSize)
	}

	state, err := decodePageState(pageToken)
	if err != nil {
		return nil, "", err
	}
	iter := r.session.Query(`SELECT updated_at, file_id, owner_id, filename, size_bytes, content_type, content_hash, version, created_at, is_deleted, current FROM files_by_folder_updated WHERE folder_id = ?`, folder).PageSize(pageSize).PageState(state).WithContext(ctx).Iter()
	defer iter.Close()
	files := make([]File, 0, pageSize)
	for {
		var updatedAt, createdAt time.Time
		var id, fileOwner gocql.UUID
		var file File
		if !iter.Scan(&updatedAt, &id, &fileOwner, &file.Filename, &file.SizeBytes, &file.ContentType, &file.ContentHash, &file.Version, &createdAt, &file.IsDeleted, &file.Current) {
			break
		}
		if fileOwner != owner || (!includeDeleted && file.IsDeleted) || !file.Current {
			continue
		}
		// Ghost-row guard: the authoritative row must still resolve in
		// files_by_id. A purged file's updated_at-keyed row can outlive the
		// purge if a timestamp drift made the point-delete miss; without this
		// check such a row would surface as a live file in listings.
		if !includeDeleted {
			if _, getErr := r.GetFileByID(ctx, ownerID, id.String()); getErr != nil {
				continue
			}
		}
		file.FileID, file.FolderID, file.OwnerID = id.String(), folder.String(), fileOwner.String()
		file.CreatedAt, file.UpdatedAt = createdAt, updatedAt
		files = append(files, file)
	}
	if err := iter.Close(); err != nil {
		return nil, "", fmt.Errorf("list files: %w", err)
	}
	return files, encodePageState(iter.PageState()), nil
}

// listFilesByFolderIncludingDeleted reads the folder's authoritative file
// index and hydrates each record from files_by_id. The updated-time index is
// optimized for active listings and is not rewritten by soft deletion.
func (r *MetadataRepo) listFilesByFolderIncludingDeleted(ctx context.Context, ownerID string, folder gocql.UUID, pageSize int) ([]File, string, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return nil, "", err
	}
	iter := r.session.Query(`SELECT file_id, owner_id FROM files_by_folder WHERE folder_id = ?`, folder).PageSize(pageSize).WithContext(ctx).Iter()
	defer iter.Close()

	files := make([]File, 0, pageSize)
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
		if len(files) >= pageSize {
			break
		}
	}
	if err := iter.Close(); err != nil {
		return nil, "", fmt.Errorf("list files including deleted: %w", err)
	}
	return files, "", nil
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
