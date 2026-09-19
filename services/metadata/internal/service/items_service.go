package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"go.uber.org/zap"
)

type ItemResult struct {
	Item   repository.FolderItem
	File   *repository.File
	Folder *repository.Folder
}

func folderItemFromFile(file repository.File) repository.FolderItem {
	return repository.FolderItem{
		FolderID: file.FolderID, ItemType: "file", ItemID: file.FileID,
		Name: file.Filename, SizeBytes: file.SizeBytes, ContentType: file.ContentType,
		ThumbnailKey: file.ThumbnailKey, OwnerID: file.OwnerID, CreatedAt: file.CreatedAt,
		UpdatedAt: file.UpdatedAt, IsDeleted: file.IsDeleted, Current: file.Current,
	}
}

func folderItemFromFolder(folder repository.Folder) repository.FolderItem {
	return repository.FolderItem{
		FolderID: folder.ParentID, ItemType: "folder", ItemID: folder.FolderID,
		Name: folder.FolderName, OwnerID: folder.OwnerID, CreatedAt: folder.CreatedAt,
		UpdatedAt: folder.UpdatedAt, IsDeleted: folder.IsDeleted, Current: !folder.IsDeleted,
	}
}

func recentItemFromFolderItem(item repository.FolderItem, eventAt time.Time) repository.RecentItem {
	return repository.RecentItem{
		OwnerID: item.OwnerID, EventAt: eventAt, ItemID: item.ItemID, ItemType: item.ItemType,
		FolderID: item.FolderID, Name: item.Name, ContentType: item.ContentType,
		SizeBytes: item.SizeBytes, IsDeleted: item.IsDeleted,
	}
}

func (s *MetadataService) indexItem(ctx context.Context, item repository.FolderItem) {
	if s.itemsRepo != nil {
		if err := s.itemsRepo.UpsertItem(ctx, item); err != nil {
			s.dualWriteFailures.Add(1)
			s.logger.Error("folder item dual-write failed", zap.String("itemType", item.ItemType), zap.String("itemID", item.ItemID), zap.Error(err))
		}
	}
	if s.recentItemsRepo != nil {
		if err := s.recentItemsRepo.RecordEvent(ctx, recentItemFromFolderItem(item, time.Now().UTC())); err != nil {
			s.dualWriteFailures.Add(1)
			s.logger.Error("recent item dual-write failed", zap.String("itemType", item.ItemType), zap.String("itemID", item.ItemID), zap.Error(err))
		}
	}
}

func (s *MetadataService) replaceIndexedItem(ctx context.Context, previous, next repository.FolderItem) {
	if s.itemsRepo != nil {
		if err := s.itemsRepo.ReplaceItem(ctx, previous, next); err != nil {
			s.dualWriteFailures.Add(1)
			s.logger.Error("folder item replacement dual-write failed", zap.String("itemType", next.ItemType), zap.String("itemID", next.ItemID), zap.Error(err))
		}
	}
	if s.recentItemsRepo != nil {
		if err := s.recentItemsRepo.RecordEvent(ctx, recentItemFromFolderItem(next, time.Now().UTC())); err != nil {
			s.dualWriteFailures.Add(1)
			s.logger.Error("recent item dual-write failed", zap.String("itemType", next.ItemType), zap.String("itemID", next.ItemID), zap.Error(err))
		}
	}
}

func (s *MetadataService) accessWorker() {
	for item := range s.accessQueue {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.recentItemsRepo.RecordEvent(ctx, item); err != nil {
			s.accessWriteErrors.Add(1)
			s.logger.Error("recent access event failed", zap.Error(err))
		}
		cancel()
	}
}

func (s *MetadataService) enqueueAccess(item repository.RecentItem) {
	if s.recentItemsRepo == nil {
		return
	}
	select {
	case s.accessQueue <- item:
	default:
		s.accessQueueDrops.Add(1)
		s.logger.Warn("recent access queue full")
	}
}

type folderItemsCacheEntry struct {
	items []ItemResult
	next  string
}

func (s *MetadataService) ListFolderItemsOwned(ctx context.Context, ownerID, folderID, sortBy string, pageSize int, pageToken string) ([]ItemResult, string, error) {
	folder, err := s.GetFolderOwned(ctx, ownerID, folderID)
	if err != nil {
		return nil, "", err
	}
	if folder.IsDeleted {
		return nil, "", fmt.Errorf("folder is deleted")
	}
	if pageToken == "" && s.cache != nil {
		if cached, ok := s.cache.Get(fmt.Sprintf("folder-items:%s:%s:%d", folderID, sortBy, pageSize)); ok {
			entry := cached.(folderItemsCacheEntry)
			return entry.items, entry.next, nil
		}
	}
	items, next, err := s.itemsRepo.ListItems(ctx, folderID, sortBy, pageSize, pageToken)
	if err != nil {
		return nil, "", err
	}
	result := make([]ItemResult, 0, len(items))
	for _, item := range items {
		if item.OwnerID != ownerID || item.IsDeleted || !item.Current {
			continue
		}
		if item.ItemType == "file" {
			file, fileErr := s.metadataRepo.GetFileByID(ctx, ownerID, item.ItemID)
			if fileErr != nil || file.IsDeleted || !file.Current {
				continue
			}
			result = append(result, ItemResult{Item: folderItemFromFile(file), File: &file})
			continue
		}
		result = append(result, ItemResult{Item: item})
	}
	if pageToken == "" && s.cache != nil {
		s.cache.SetWithTTL(fmt.Sprintf("folder-items:%s:%s:%d", folderID, sortBy, pageSize), folderItemsCacheEntry{items: result, next: next}, 1, 10*time.Second)
	}
	return result, next, nil
}

func (s *MetadataService) ListRecentItemsOwned(ctx context.Context, ownerID string, pageSize int, pageToken string) ([]ItemResult, string, error) {
	if s.recentItemsRepo == nil {
		return nil, "", fmt.Errorf("recent items unavailable")
	}
	events, next, err := s.recentItemsRepo.ListRecentItems(ctx, ownerID, pageSize, pageToken)
	if err != nil {
		return nil, "", err
	}
	result := make([]ItemResult, 0, len(events))
	for _, event := range events {
		if event.IsDeleted {
			continue
		}
		if event.ItemType == "folder" {
			folder, err := s.folderRepo.GetFolder(ctx, ownerID, event.ItemID)
			if err != nil || folder.IsDeleted {
				continue
			}
			result = append(result, ItemResult{Item: folderItemFromFolder(folder), Folder: &folder})
			continue
		}
		file, err := s.metadataRepo.GetFileByID(ctx, ownerID, event.ItemID)
		if err != nil || file.IsDeleted || !file.Current {
			continue
		}
		result = append(result, ItemResult{Item: folderItemFromFile(file), File: &file})
	}
	return result, next, nil
}

func (s *MetadataService) RecordFileAccess(ctx context.Context, ownerID, fileID string) error {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return err
	}
	s.enqueueAccess(recentItemFromFolderItem(folderItemFromFile(file), time.Now().UTC()))
	return nil
}
