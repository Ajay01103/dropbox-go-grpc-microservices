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

func (s *MetadataService) RenameFileOwned(ctx context.Context, ownerID, fileID, name string) (repository.File, error) {
	if err := validateName(name); err != nil {
		return repository.File{}, err
	}
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	if err := s.metadataRepo.UpdateFileName(ctx, ownerID, fileID, name); err != nil {
		return repository.File{}, err
	}
	file.Filename = name
	file.UpdatedAt = time.Now().UTC()
	return file, nil
}

func (s *MetadataService) MoveFileOwned(ctx context.Context, ownerID, fileID, folderID string) (repository.File, error) {
	destination, err := s.folderRepo.GetFolder(ctx, ownerID, folderID)
	if err != nil {
		return repository.File{}, err
	}
	if destination.IsDeleted {
		return repository.File{}, errors.New("destination folder is deleted")
	}
	if err := s.metadataRepo.MoveFile(ctx, ownerID, fileID, folderID); err != nil {
		return repository.File{}, err
	}
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	file.FolderID = destination.FolderID
	return file, nil
}

func (s *MetadataService) DeleteFileOwned(ctx context.Context, ownerID, fileID string) (repository.File, error) {
	return s.metadataRepo.SetFileDeleted(ctx, ownerID, fileID, uuid.NewString(), true)
}

func (s *MetadataService) RestoreFileOwned(ctx context.Context, ownerID, fileID string) (repository.File, error) {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return repository.File{}, err
	}
	folder, folderErr := s.folderRepo.GetFolder(ctx, ownerID, file.FolderID)
	if folderErr != nil || folder.IsDeleted {
		root, rootErr := s.EnsureRootFolder(ctx, ownerID)
		if rootErr != nil {
			return repository.File{}, rootErr
		}
		if _, err := s.MoveFileOwned(ctx, ownerID, fileID, root.FolderID); err != nil {
			return repository.File{}, err
		}
	}
	return s.metadataRepo.SetFileDeleted(ctx, ownerID, fileID, "", false)
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
	return s.folderRepo.CreateChildFolder(ctx, ownerID, parent.FolderID, name)
}

func (s *MetadataService) GetFolderOwned(ctx context.Context, ownerID, folderID string) (repository.Folder, error) {
	return s.folderRepo.GetFolder(ctx, ownerID, folderID)
}

func (s *MetadataService) ListFolderContentsOwned(ctx context.Context, ownerID, folderID string, pageSize int, pageToken string) ([]repository.Folder, []repository.File, string, error) {
	folder, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return nil, nil, "", err
	}
	if folder.IsDeleted {
		return nil, nil, "", errors.New("folder is deleted")
	}
	children, next, err := s.folderRepo.ListChildren(ctx, ownerID, folder.FolderID, pageSize, pageToken)
	if err != nil {
		return nil, nil, "", err
	}
	files, fileNext, err := s.ListFilesOwned(ctx, ownerID, folder.FolderID, pageSize, pageToken, false)
	if err != nil {
		return nil, nil, "", err
	}
	if next == "" {
		next = fileNext
	}
	return children, files, next, nil
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
	return s.folderRepo.RenameFolder(ctx, ownerID, folderID, name)
}

func (s *MetadataService) MoveFolderOwned(ctx context.Context, ownerID, folderID, parentID string) (repository.Folder, error) {
	moving, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return repository.Folder{}, err
	}
	destination, err := s.GetFolderOwned(ctx, ownerID, parentID)
	if err != nil {
		return repository.Folder{}, err
	}
	if moving.FolderID == destination.FolderID {
		return repository.Folder{}, ErrFolderCycle
	}
	if destination.IsDeleted {
		return repository.Folder{}, errors.New("destination folder is deleted")
	}
	breadcrumbs, err := s.folderRepo.ListBreadcrumbs(ctx, ownerID, destination.FolderID)
	if err != nil {
		return repository.Folder{}, err
	}
	for _, folder := range breadcrumbs {
		if folder.FolderID == moving.FolderID {
			return repository.Folder{}, ErrFolderCycle
		}
	}
	if len(breadcrumbs) >= 20 {
		return repository.Folder{}, errors.New("maximum folder depth exceeded")
	}
	return s.folderRepo.MoveFolder(ctx, ownerID, folderID, parentID)
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
	if _, err := s.folderRepo.SetFolderDeleted(ctx, ownerID, folderID, batchID, true); err != nil {
		return err
	}
	files, _, err := s.ListFilesOwned(ctx, ownerID, folderID, 1000, "", true)
	if err != nil {
		return err
	}
	for _, file := range files {
		if _, err := s.metadataRepo.SetFileDeleted(ctx, ownerID, file.FileID, batchID, true); err != nil {
			return err
		}
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
	if _, err := s.folderRepo.SetFolderDeleted(ctx, ownerID, folderID, "", false); err != nil {
		return repository.Folder{}, err
	}
	return s.GetFolderOwned(ctx, ownerID, folderID)
}

func (s *MetadataService) Breadcrumbs(ctx context.Context, ownerID, folderID string) ([]repository.Folder, error) {
	return s.folderRepo.ListBreadcrumbs(ctx, ownerID, folderID)
}
