package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrInvalidName    = errors.New("invalid name")
	ErrFolderConflict = errors.New("folder name already exists")
	ErrFolderCycle    = errors.New("folder move would create a cycle")
)

const maxNameLength = 255

func validateName(name string) error {
	if name == "" || len(name) > maxNameLength || strings.ContainsAny(name, "/\\") || strings.TrimSpace(name) != name || name == "." || name == ".." {
		return ErrInvalidName
	}
	return nil
}

func (s *MetadataService) EnsureRootFolder(ctx context.Context, ownerID string) (repository.Folder, error) {
	return s.folderRepo.EnsureRootFolder(ctx, ownerID)
}

func (s *MetadataService) GetFileOwned(ctx context.Context, ownerID, fileID string) (repository.File, error) {
	return s.metadataRepo.GetFileByID(ctx, ownerID, fileID)
}

func (s *MetadataService) ListFilesOwned(ctx context.Context, ownerID, folderID string, pageSize int, pageToken string, includeDeleted bool) ([]repository.File, string, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	if _, err := s.folderRepo.GetFolder(ctx, ownerID, folderID); err != nil {
		return nil, "", err
	}
	return s.metadataRepo.ListFilesByFolder(ctx, ownerID, folderID, pageSize, pageToken, includeDeleted)
}

type TrashResult struct {
	Files   []repository.File
	Folders []repository.Folder
}

// ListTrashOwned walks the user's folder hierarchy because the metadata
// schema is partitioned by folder and does not have a global trash partition.
// Deleted rows remain in their original partitions until the purge job removes
// them, so this also finds files inside deleted folders.
func (s *MetadataService) ListTrashOwned(ctx context.Context, ownerID string, pageSize int) (*TrashResult, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}

	root, err := s.EnsureRootFolder(ctx, ownerID)
	if err != nil {
		return nil, err
	}

	result := &TrashResult{
		Files:   make([]repository.File, 0),
		Folders: make([]repository.Folder, 0),
	}
	if err := s.collectTrash(ctx, ownerID, root.FolderID, result, pageSize); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *MetadataService) collectTrash(ctx context.Context, ownerID, folderID string, result *TrashResult, limit int) error {
	files, _, err := s.ListFilesOwned(ctx, ownerID, folderID, 1000, "", true)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDeleted {
			result.Files = append(result.Files, file)
		}
	}

	children, _, err := s.folderRepo.ListChildren(ctx, ownerID, folderID, 1000, "")
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.IsDeleted {
			result.Folders = append(result.Folders, child)
		}
		if err := s.collectTrash(ctx, ownerID, child.FolderID, result, limit); err != nil {
			return err
		}
		if len(result.Files)+len(result.Folders) >= limit {
			return nil
		}
	}
	return nil
}

func (s *MetadataService) RenameFileOwned(ctx context.Context, ownerID, fileID, name string) (repository.File, error) {
	if err := validateName(name); err != nil {
		return repository.File{}, err
	}
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	previous := folderItemFromFile(file)
	if err := s.metadataRepo.UpdateFileName(ctx, ownerID, fileID, name); err != nil {
		return repository.File{}, err
	}
	file.Filename = name
	file.UpdatedAt = time.Now().UTC()
	s.replaceIndexedItem(ctx, previous, folderItemFromFile(file))
	return file, nil
}

func (s *MetadataService) MoveFileOwned(ctx context.Context, ownerID, fileID, folderID string) (repository.File, error) {
	destination, err := s.resolveDestinationFolder(ctx, ownerID, folderID)
	if err != nil {
		return repository.File{}, err
	}
	if destination.IsDeleted {
		return repository.File{}, errors.New("destination folder is deleted")
	}
	previous, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	if err := s.metadataRepo.MoveFile(ctx, ownerID, fileID, folderID); err != nil {
		return repository.File{}, err
	}
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	file.FolderID = destination.FolderID
	s.replaceIndexedItem(ctx, folderItemFromFile(previous), folderItemFromFile(file))
	return file, nil
}

func (s *MetadataService) DeleteFileOwned(ctx context.Context, ownerID, fileID string) (repository.File, error) {
	previous, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	file, err := s.metadataRepo.SetFileDeleted(ctx, ownerID, fileID, uuid.NewString(), true)
	if err != nil {
		return repository.File{}, err
	}
	s.replaceIndexedItem(ctx, folderItemFromFile(previous), folderItemFromFile(file))
	return file, nil
}

func (s *MetadataService) RestoreFileOwned(ctx context.Context, ownerID, fileID string) (repository.File, error) {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	folder, folderErr := s.folderRepo.GetFolder(ctx, ownerID, file.FolderID)
	restorePrevious := folderItemFromFile(file)
	if folderErr != nil || folder.IsDeleted {
		root, rootErr := s.EnsureRootFolder(ctx, ownerID)
		if rootErr != nil {
			return repository.File{}, rootErr
		}
		moved, moveErr := s.MoveFileOwned(ctx, ownerID, fileID, root.FolderID)
		if moveErr != nil {
			return repository.File{}, moveErr
		}
		restorePrevious = folderItemFromFile(moved)
	}
	file, err = s.metadataRepo.SetFileDeleted(ctx, ownerID, fileID, "", false)
	if err != nil {
		return repository.File{}, err
	}
	s.replaceIndexedItem(ctx, restorePrevious, folderItemFromFile(file))
	return file, nil
}

func (s *MetadataService) CreateChildFolder(ctx context.Context, ownerID, parentID, name string) (repository.Folder, error) {
	if err := validateName(name); err != nil {
		return repository.Folder{}, err
	}
	if parentID == "" {
		root, err := s.EnsureRootFolder(ctx, ownerID)
		if err != nil {
			return repository.Folder{}, err
		}
		parentID = root.FolderID
	}
	parent, err := s.folderRepo.GetFolder(ctx, ownerID, parentID)
	if err != nil {
		return repository.Folder{}, err
	}
	if parent.IsDeleted {
		return repository.Folder{}, errors.New("parent folder is deleted")
	}
	children, _, err := s.folderRepo.ListChildren(ctx, ownerID, parent.FolderID, 1000, "")
	if err != nil {
		return repository.Folder{}, err
	}
	for _, child := range children {
		if !child.IsDeleted && strings.EqualFold(child.FolderName, name) {
			return repository.Folder{}, ErrFolderConflict
		}
	}
	breadcrumbs, err := s.folderRepo.ListBreadcrumbs(ctx, ownerID, parent.FolderID)
	if err != nil {
		return repository.Folder{}, err
	}
	if len(breadcrumbs) >= 20 {
		return repository.Folder{}, errors.New("maximum folder depth exceeded")
	}
	folder, err := s.folderRepo.CreateChildFolder(ctx, ownerID, parent.FolderID, parent.AncestorIDs, name)
	if err != nil {
		return repository.Folder{}, err
	}
	s.indexItem(ctx, folderItemFromFolder(folder))
	return folder, nil
}

func (s *MetadataService) GetFolderOwned(ctx context.Context, ownerID, folderID string) (repository.Folder, error) {
	return s.folderRepo.GetFolder(ctx, ownerID, folderID)
}

func (s *MetadataService) ListFolderContentsOwned(ctx context.Context, ownerID, folderID string, pageSize int, pageToken string) ([]repository.Folder, []repository.File, string, error) {
	items, next, err := s.ListFolderItemsOwned(ctx, ownerID, folderID, "updated", pageSize, pageToken)
	if err != nil {
		return nil, nil, "", err
	}
	folders := make([]repository.Folder, 0, len(items))
	files := make([]repository.File, 0, len(items))
	for _, item := range items {
		if item.Item.ItemType == "folder" {
			if item.Folder != nil {
				folders = append(folders, *item.Folder)
				continue
			}
			folders = append(folders, repository.Folder{FolderID: item.Item.ItemID, OwnerID: item.Item.OwnerID, ParentID: item.Item.FolderID, FolderName: item.Item.Name, CreatedAt: item.Item.CreatedAt, UpdatedAt: item.Item.UpdatedAt, IsDeleted: item.Item.IsDeleted})
			continue
		}
		if item.File != nil {
			files = append(files, *item.File)
			continue
		}
		files = append(files, repository.File{FileID: item.Item.ItemID, FolderID: item.Item.FolderID, OwnerID: item.Item.OwnerID, Filename: item.Item.Name, SizeBytes: item.Item.SizeBytes, ContentType: item.Item.ContentType, ThumbnailKey: item.Item.ThumbnailKey, CreatedAt: item.Item.CreatedAt, UpdatedAt: item.Item.UpdatedAt, IsDeleted: item.Item.IsDeleted, Current: item.Item.Current})
	}
	return folders, files, next, nil
}

func (s *MetadataService) RenameFolderOwned(ctx context.Context, ownerID, folderID, name string) (repository.Folder, error) {
	if err := validateName(name); err != nil {
		return repository.Folder{}, err
	}
	folder, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return repository.Folder{}, err
	}
	if folder.ParentID != "" {
		children, _, listErr := s.folderRepo.ListChildren(ctx, ownerID, folder.ParentID, 1000, "")
		if listErr != nil {
			return repository.Folder{}, listErr
		}
		for _, child := range children {
			if child.FolderID != folderID && !child.IsDeleted && strings.EqualFold(child.FolderName, name) {
				return repository.Folder{}, ErrFolderConflict
			}
		}
	}
	previous := folderItemFromFolder(folder)
	renamed, err := s.folderRepo.RenameFolder(ctx, ownerID, folderID, name)
	if err != nil {
		return repository.Folder{}, err
	}
	s.replaceIndexedItem(ctx, previous, folderItemFromFolder(renamed))
	return renamed, nil
}

// isInSubtree reports whether folderID is the destination itself or one of
// its ancestors-in-reverse (i.e. moving folderID under destination would
// create a cycle). Pure so the cycle rules are unit-testable without Scylla.
func isInSubtree(folderID string, destination repository.Folder) bool {
	if folderID == destination.FolderID {
		return true
	}
	for _, ancestor := range destination.AncestorIDs {
		if ancestor == folderID {
			return true
		}
	}
	return false
}

func (s *MetadataService) MoveFolderOwned(ctx context.Context, ownerID, folderID, parentID string) (repository.Folder, error) {
	moving, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return repository.Folder{}, err
	}
	destination, err := s.resolveDestinationFolder(ctx, ownerID, parentID)
	if err != nil {
		return repository.Folder{}, err
	}
	// Validate everything BEFORE any writes: a rejected move must never leave
	// a half-applied recomputeDescendantAncestors behind.
	if moving.FolderID == destination.FolderID {
		return repository.Folder{}, ErrFolderCycle
	}
	if destination.IsDeleted {
		return repository.Folder{}, errors.New("destination folder is deleted")
	}
	if isInSubtree(moving.FolderID, destination) {
		return repository.Folder{}, ErrFolderCycle
	}
	if len(destination.AncestorIDs) >= 20 { // maxFolderDepth — kept in sync with the repository constant
		return repository.Folder{}, errors.New("maximum folder depth exceeded")
	}
	moved, err := s.folderRepo.MoveFolder(ctx, ownerID, folderID, destination.FolderID, destination.AncestorIDs)
	if err != nil {
		return repository.Folder{}, err
	}
	// Rewrite ancestor_ids for every descendant of the moved folder. This is
	// NOT atomic across partitions: a crash mid-walk leaves some descendants
	// with stale ancestor_ids (wrong breadcrumbs) until a later repair. Same
	// risk class as cascadeFolderDelete; accepted for moves, which are rare.
	if err := s.recomputeDescendantAncestors(ctx, ownerID, moved); err != nil {
		return repository.Folder{}, err
	}
	movedItem := folderItemFromFolder(moved)
	previousItem := folderItemFromFolder(moving)
	s.replaceIndexedItem(ctx, previousItem, movedItem)
	return moved, nil
}

// recomputeDescendantAncestors walks the moved folder's subtree and rewrites
// each descendant's ancestor_ids for its new position. Cost is proportional
// to the descendant FOLDER count (not depth — a wide shallow subtree costs
// the same as a deep narrow one), which is the right side to pay on since
// moves are rare and breadcrumb reads are common.
func (s *MetadataService) recomputeDescendantAncestors(ctx context.Context, ownerID string, moved repository.Folder) error {
	return s.recomputeDescendantsFrom(ctx, ownerID, moved.FolderID, moved.AncestorIDs)
}

func (s *MetadataService) recomputeDescendantsFrom(ctx context.Context, ownerID, parentID string, ancestorChain []string) error {
	children, _, err := s.folderRepo.ListChildren(ctx, ownerID, parentID, 1000, "")
	if err != nil {
		return err
	}
	for _, child := range children {
		chain := make([]string, 0, len(ancestorChain)+1)
		chain = append(chain, ancestorChain...)
		chain = append(chain, parentID)
		if err := s.folderRepo.UpdateFolderAncestors(ctx, ownerID, child.FolderID, chain); err != nil {
			return err
		}
		if err := s.recomputeDescendantsFrom(ctx, ownerID, child.FolderID, chain); err != nil {
			return err
		}
	}
	return nil
}

// resolveDestinationFolder maps an empty destination id (the UI's "My files"
// root drop zone sends "") to the owner's deterministic root folder.
func (s *MetadataService) resolveDestinationFolder(ctx context.Context, ownerID, folderID string) (repository.Folder, error) {
	if folderID == "" {
		return s.EnsureRootFolder(ctx, ownerID)
	}
	return s.folderRepo.GetFolder(ctx, ownerID, folderID)
}

func (s *MetadataService) DeleteFolderOwned(ctx context.Context, ownerID, folderID string) (repository.Folder, error) {
	folder, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return repository.Folder{}, err
	}
	if folder.ParentID == "" {
		return repository.Folder{}, errors.New("root folder cannot be deleted")
	}
	batchID := uuid.NewString()
	if err := s.cascadeFolderDelete(ctx, ownerID, folder.FolderID, batchID); err != nil {
		return repository.Folder{}, err
	}
	return s.GetFolderOwned(ctx, ownerID, folderID)
}

func (s *MetadataService) cascadeFolderDelete(ctx context.Context, ownerID, folderID, batchID string) error {
	previousFolder, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return err
	}
	folder, err := s.folderRepo.SetFolderDeleted(ctx, ownerID, folderID, batchID, true)
	if err != nil {
		return err
	}
	s.replaceIndexedItem(ctx, folderItemFromFolder(previousFolder), folderItemFromFolder(folder))
	files, _, err := s.ListFilesOwned(ctx, ownerID, folderID, 1000, "", true)
	if err != nil {
		return err
	}
	for _, file := range files {
		previous := folderItemFromFile(file)
		updated, err := s.metadataRepo.SetFileDeleted(ctx, ownerID, file.FileID, batchID, true)
		if err != nil {
			return err
		}
		s.replaceIndexedItem(ctx, previous, folderItemFromFile(updated))
	}
	children, _, err := s.folderRepo.ListChildren(ctx, ownerID, folderID, 1000, "")
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.IsDeleted {
			continue
		}
		if err := s.cascadeFolderDelete(ctx, ownerID, child.FolderID, batchID); err != nil {
			return err
		}
	}
	return nil
}

func (s *MetadataService) RestoreFolderOwned(ctx context.Context, ownerID, folderID string) (repository.Folder, error) {
	folder, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return repository.Folder{}, err
	}
	if folder.ParentID != "" {
		parent, parentErr := s.GetFolderOwned(ctx, ownerID, folder.ParentID)
		if parentErr != nil || parent.IsDeleted {
			return repository.Folder{}, errors.New("cannot restore folder into deleted parent")
		}
	}
	updated, err := s.folderRepo.SetFolderDeleted(ctx, ownerID, folderID, "", false)
	if err != nil {
		return repository.Folder{}, err
	}
	s.replaceIndexedItem(ctx, folderItemFromFolder(folder), folderItemFromFolder(updated))
	return updated, nil
}

func (s *MetadataService) Breadcrumbs(ctx context.Context, ownerID, folderID string) ([]repository.Folder, error) {
	return s.folderRepo.ListBreadcrumbs(ctx, ownerID, folderID)
}
