package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// MetadataService orchestrates file metadata operations
type PurgeCoordinator interface {
	Request(context.Context, string, string) (repository.PurgeJob, error)
	Status(context.Context, string, string) (repository.PurgeJob, error)
}

type ThumbnailURLer interface {
	PresignThumbnail(context.Context, string, time.Duration) (string, error)
}

type MetadataService struct {
	metadataRepo      *repository.MetadataRepo
	folderRepo        *repository.FolderRepo
	itemsRepo         *repository.ItemsRepo
	recentItemsRepo   *repository.RecentItemsRepo
	cache             *ristretto.Cache
	cfg               config.Config
	logger            *zap.Logger
	purgeCoordinator  PurgeCoordinator
	thumbnailURLer    ThumbnailURLer
	accessQueue       chan repository.RecentItem
	dualWriteFailures atomic.Uint64
	accessWriteErrors atomic.Uint64
	accessQueueDrops  atomic.Uint64
}

// New creates a MetadataService with its dependencies wired
func New(
	metadataRepo *repository.MetadataRepo,
	folderRepo *repository.FolderRepo,
	itemsRepo *repository.ItemsRepo,
	recentItemsRepo *repository.RecentItemsRepo,
	cache *ristretto.Cache,
	cfg config.Config,
	logger *zap.Logger,
) *MetadataService {
	svc := &MetadataService{
		metadataRepo:    metadataRepo,
		folderRepo:      folderRepo,
		itemsRepo:       itemsRepo,
		recentItemsRepo: recentItemsRepo,
		cache:           cache,
		cfg:             cfg,
		logger:          logger,
		accessQueue:     make(chan repository.RecentItem, 256),
	}
	if recentItemsRepo != nil {
		for i := 0; i < 4; i++ {
			go svc.accessWorker()
		}
	}
	return svc
}

func (s *MetadataService) SetPurgeCoordinator(coordinator PurgeCoordinator) {
	s.purgeCoordinator = coordinator
}

func (s *MetadataService) SetThumbnailURLer(urler ThumbnailURLer) {
	s.thumbnailURLer = urler
}

func (s *MetadataService) RequestPermanentDelete(ctx context.Context, ownerID, fileID string) (repository.PurgeJob, error) {
	if s.purgeCoordinator == nil {
		return repository.PurgeJob{}, errors.New("purge coordinator is unavailable")
	}
	return s.purgeCoordinator.Request(ctx, ownerID, fileID)
}

func (s *MetadataService) GetPurgeJobStatus(ctx context.Context, ownerID, jobID string) (repository.PurgeJob, error) {
	if s.purgeCoordinator == nil {
		return repository.PurgeJob{}, errors.New("purge coordinator is unavailable")
	}
	return s.purgeCoordinator.Status(ctx, ownerID, jobID)
}

// CreateFileRequest holds params for creating a file record
type CreateFileRequest struct {
	FolderID      string
	Filename      string
	SizeBytes     int64
	ContentType   string
	ContentHash   string // SHA256 hash for dedup
	OwnerID       string
	BlockHashList []string
}

// CreateFileResult is the result of successful file creation
type CreateFileResult struct {
	FileID        string
	FolderID      string
	Filename      string
	SizeBytes     int64
	ContentHash   string
	CreatedAt     string
	BlockHashList []string
}

// CreateFile creates a new file metadata record
// Called by Upload Session after successful upload finalization
func (s *MetadataService) CreateFile(ctx context.Context, req CreateFileRequest) (*CreateFileResult, error) {
	if req.Filename == "" || req.FolderID == "" {
		return nil, errors.New("filename and folder_id are required")
	}
	if _, err := s.folderRepo.GetFolder(ctx, req.OwnerID, req.FolderID); err != nil {
		if req.FolderID != req.OwnerID {
			return nil, fmt.Errorf("validate folder: %w", err)
		}
		if _, rootErr := s.EnsureRootFolder(ctx, req.OwnerID); rootErr != nil {
			return nil, fmt.Errorf("ensure root folder: %w", rootErr)
		}
	}

	file, err := s.metadataRepo.CreateFile(
		ctx,
		req.FolderID,
		req.OwnerID,
		req.Filename,
		req.ContentType,
		req.ContentHash,
		req.BlockHashList,
		req.SizeBytes,
	)
	if err != nil {
		s.logger.Error("failed to create file record",
			zap.String("filename", req.Filename),
			zap.String("folderID", req.FolderID),
			zap.Error(err))
		return nil, fmt.Errorf("create file: %w", err)
	}

	s.indexItem(ctx, folderItemFromFile(file))

	s.logger.Info("file record created",
		zap.String("fileID", file.FileID),
		zap.String("filename", file.Filename),
		zap.Int64("sizeBytes", file.SizeBytes))

	return &CreateFileResult{
		FileID:        file.FileID,
		FolderID:      file.FolderID,
		Filename:      file.Filename,
		SizeBytes:     file.SizeBytes,
		ContentHash:   file.ContentHash,
		CreatedAt:     file.CreatedAt.String(),
		BlockHashList: file.BlockHashList,
	}, nil
}

func (s *MetadataService) SetThumbnail(ctx context.Context, fileID, folderID, thumbnailKey, thumbnailStatus string) error {
	if fileID == "" || folderID == "" {
		return errors.New("file_id and folder_id are required")
	}
	if thumbnailStatus == "" {
		thumbnailStatus = "pending"
	}
	if err := s.metadataRepo.SetThumbnail(ctx, folderID, fileID, thumbnailKey, thumbnailStatus); err != nil {
		s.logger.Error("failed to set thumbnail metadata",
			zap.String("fileID", fileID),
			zap.String("folderID", folderID),
			zap.String("thumbnailStatus", thumbnailStatus),
			zap.Error(err),
		)
		return fmt.Errorf("set thumbnail: %w", err)
	}
	if file, err := s.GetFileOwned(ctx, folderID, fileID); err == nil {
		s.replaceIndexedItem(ctx, folderItemFromFile(file), folderItemFromFile(file))
	}
	return nil
}

func (s *MetadataService) GetThumbnailURL(ctx context.Context, ownerID, fileID string) (string, string, error) {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return "", "", err
	}
	if file.ThumbnailStatus != "ready" || file.ThumbnailKey == "" {
		return "", file.ThumbnailStatus, nil
	}
	if s.thumbnailURLer == nil {
		return "", file.ThumbnailStatus, errors.New("thumbnail URL service is unavailable")
	}
	url, err := s.thumbnailURLer.PresignThumbnail(ctx, file.ThumbnailKey, 10*time.Minute)
	if err != nil {
		return "", file.ThumbnailStatus, err
	}
	return url, file.ThumbnailStatus, nil
}

func (s *MetadataService) GetThumbnailStatus(ctx context.Context, fileID, folderID string) (string, string, error) {
	if fileID == "" || folderID == "" {
		return "", "", errors.New("file_id and folder_id are required")
	}
	thumbnailKey, thumbnailStatus, err := s.metadataRepo.GetThumbnailStatus(ctx, folderID, fileID)
	if err != nil {
		return "", "", err
	}
	return thumbnailKey, thumbnailStatus, nil
}

// GetFileResult holds file metadata
type GetFileResult struct {
	FileID        string
	FolderID      string
	Filename      string
	SizeBytes     int64
	ContentType   string
	ContentHash   string
	Version       int32
	CreatedAt     string
	OwnerID       string
	BlockHashList []string
}

// GetFile retrieves file metadata
func (s *MetadataService) GetFile(ctx context.Context, fileID, folderID string) (*GetFileResult, error) {
	file, err := s.metadataRepo.GetFile(ctx, fileID, folderID)
	if err != nil {
		s.logger.Warn("file not found",
			zap.String("fileID", fileID),
			zap.String("folderID", folderID))
		return nil, err
	}

	return &GetFileResult{
		FileID:        file.FileID,
		FolderID:      file.FolderID,
		Filename:      file.Filename,
		SizeBytes:     file.SizeBytes,
		ContentType:   file.ContentType,
		ContentHash:   file.ContentHash,
		Version:       file.Version,
		CreatedAt:     file.CreatedAt.String(),
		OwnerID:       file.OwnerID,
		BlockHashList: file.BlockHashList,
	}, nil
}

// ListFolderResult holds paginated file list
type ListFolderResult struct {
	Files         []FileInfo
	NextPageToken string
}

// FileInfo represents a file in a folder listing
type FileInfo struct {
	FileID        string
	Filename      string
	SizeBytes     int64
	ContentType   string
	CreatedAt     string
	BlockHashList []string
}

// ListFolder returns all files in a folder
func (s *MetadataService) ListFolder(ctx context.Context, folderID string, pageSize int) (*ListFolderResult, error) {
	if folderID == "" {
		return nil, errors.New("folder_id is required")
	}

	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 100 // default
	}

	files, err := s.metadataRepo.ListFolder(ctx, folderID, pageSize)
	if err != nil {
		s.logger.Error("failed to list folder",
			zap.String("folderID", folderID),
			zap.Error(err))
		return nil, err
	}

	fileInfos := make([]FileInfo, len(files))
	for i, f := range files {
		fileInfos[i] = FileInfo{
			FileID:        f.FileID,
			Filename:      f.Filename,
			SizeBytes:     f.SizeBytes,
			ContentType:   f.ContentType,
			CreatedAt:     f.CreatedAt.String(),
			BlockHashList: f.BlockHashList,
		}
	}

	s.logger.Debug("folder listed",
		zap.String("folderID", folderID),
		zap.Int("fileCount", len(files)))

	return &ListFolderResult{
		Files: fileInfos,
	}, nil
}

// DeleteFile removes a file record
func (s *MetadataService) DeleteFile(ctx context.Context, fileID, folderID string) error {
	_, err := s.metadataRepo.SetFileDeleted(ctx, folderID, fileID, uuid.NewString(), true)
	if err != nil {
		s.logger.Error("failed to delete file",
			zap.String("fileID", fileID),
			zap.String("folderID", folderID),
			zap.Error(err))
		return err
	}

	s.logger.Info("file deleted",
		zap.String("fileID", fileID),
		zap.String("folderID", folderID))

	return nil
}

// GetOrCreateRootFolder returns or creates a user's root folder
func (s *MetadataService) GetOrCreateRootFolder(ctx context.Context, userID string) (string, error) {
	root, err := s.EnsureRootFolder(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("ensure root folder: %w", err)
	}
	s.logger.Debug("root folder accessed", zap.String("userID", userID), zap.String("folderID", root.FolderID))
	return root.FolderID, nil
}
