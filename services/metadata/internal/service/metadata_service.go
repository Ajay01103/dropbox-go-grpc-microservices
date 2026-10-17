package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	"github.com/Ajay01103/go-dropbox/metadata/internal/purge"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// MetadataService orchestrates file metadata operations
type PurgeCoordinator interface {
	Request(context.Context, string, string) (repository.PurgeJob, error)
	Status(context.Context, string, string) (repository.PurgeJob, error)
	// PurgeFolderSubtree permanently deletes a soft-deleted folder, every
	// folder beneath it, and every file they hold.
	PurgeFolderSubtree(context.Context, string, string) (purge.FolderPurgeResult, error)
}

// StitchLookup reports whether a file has a consolidated (Tier B) object and
// its key. Nil disables the Tier B fast path.
type StitchLookup interface {
	GetStitchedObjectKey(ctx context.Context, fileID string) (string, error)
}

// PresignRequest is the response metadata a presigned object GET must carry.
//
// Object storage cannot infer any of it. The consolidated object is a
// server-side copy of the file's blocks, so it has no filename and no real
// content type, and whether the caller wants a saved file or an inline
// preview is decided by the ticket that minted this URL. Browsers obey those
// response headers and nothing else once the URL is cross-origin: the
// <a download> attribute is ignored for another origin, so without
// Content-Disposition a "download" would open the file in a tab instead.
type PresignRequest struct {
	// Disposition is "attachment" (download) or "inline" (preview).
	Disposition string
	// Filename is the name the browser saves the file under.
	Filename string
	// ContentType is the file's real type, not the object's.
	ContentType string
	// Expiry bounds the URL's validity.
	Expiry time.Duration
}

// ConsolidatedPresigner presigns a GET for the consolidated object key.
// Separate from ThumbnailURLer because the two features fail/roll out
// independently.
type ConsolidatedPresigner interface {
	PresignFileGET(ctx context.Context, key string, request PresignRequest) (string, error)
}

type MetadataService struct {
	metadataRepo     *repository.MetadataRepo
	folderRepo       *repository.FolderRepo
	itemsRepo        *repository.ItemsRepo
	recentItemsRepo  *repository.RecentItemsRepo
	cache            *ristretto.Cache
	cfg              config.Config
	logger           *zap.Logger
	purgeCoordinator PurgeCoordinator
	stitchRepo       StitchLookup
	presigner        ConsolidatedPresigner
	// Edit path: the content store a commit is verified against, and the
	// consolidated-object invalidator that stops Tier B serving the version a
	// commit just superseded. Nil blockReader disables editing (see
	// editing.go).
	blockReader         BlockReader
	consolidated        ConsolidatedInvalidator
	stitchInvalidator   StitchInvalidator
	manifestInvalidator ManifestInvalidator
	accessQueue         chan repository.RecentItem
	dualWriteFailures   atomic.Uint64
	accessWriteErrors   atomic.Uint64
	accessQueueDrops    atomic.Uint64
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

// SetStitchLookup wires the Tier B registry. Nil disables the presigned-
// consolidated-object fast path (every download rides Tier A).
func (s *MetadataService) SetStitchLookup(lookup StitchLookup) {
	s.stitchRepo = lookup
}

// SetConsolidatedPresigner wires the Tier B presigner. Used only for
// 'ready' consolidated objects.
func (s *MetadataService) SetConsolidatedPresigner(presigner ConsolidatedPresigner) {
	s.presigner = presigner
}

func (s *MetadataService) RequestPermanentDelete(ctx context.Context, ownerID, fileID string) (repository.PurgeJob, error) {
	if s.purgeCoordinator == nil {
		return repository.PurgeJob{}, errors.New("purge coordinator is unavailable")
	}
	return s.purgeCoordinator.Request(ctx, ownerID, fileID)
}

// RequestPermanentDeleteFolder permanently deletes a soft-deleted folder along
// with everything inside it. Unlike RequestPermanentDelete — one file, one job —
// this fans out, so it reports counts instead of a job: the caller has to be
// able to see a partial purge rather than assume success.
func (s *MetadataService) RequestPermanentDeleteFolder(ctx context.Context, ownerID, folderID string) (purge.FolderPurgeResult, error) {
	if s.purgeCoordinator == nil {
		return purge.FolderPurgeResult{}, errors.New("purge coordinator is unavailable")
	}
	return s.purgeCoordinator.PurgeFolderSubtree(ctx, ownerID, folderID)
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
	// BlockSizes[i] is the exact byte size of BlockHashList[i]; when present
	// it feeds the dense download manifest and saves a per-block size lookup
	// on every read. Nil for legacy callers.
	BlockSizes []int64
	// ContentFamily is classified at upload time (magic bytes) and trusted
	// here. It gates preview work: only IMAGE/VIDEO/PDF start as pending.
	ContentFamily eventsv2.ContentFamily
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

	file, err := s.metadataRepo.CreateFileWithBlockSizes(
		ctx,
		req.FolderID,
		req.OwnerID,
		req.Filename,
		req.ContentType,
		req.ContentHash,
		req.BlockHashList,
		req.BlockSizes,
		req.SizeBytes,
		req.ContentFamily,
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

// GetThumbnailURL mints a preview URL served by this service's own HTTP port
// (`/thumbnails/{file_id}?ticket=...`), not a presigned object-store URL.
//
// The object store is deliberately not exposed to clients: in dev it binds
// loopback (so a presigned LAN URL is unreachable from a phone) and in
// production it is private. Streaming the preview here keeps one client-facing
// origin — the same one downloads already use — and a preview ticket is worth
// minutes of caching, which is what an image list needs.
func (s *MetadataService) GetThumbnailURL(ctx context.Context, ownerID, fileID string) (string, string, error) {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return "", "", err
	}
	if file.ThumbnailStatus != "ready" || file.ThumbnailKey == "" {
		return "", file.ThumbnailStatus, nil
	}
	ticket, err := MintThumbnailTicket(fileID, ownerID, file.ThumbnailKey, time.Now())
	if err != nil {
		return "", file.ThumbnailStatus, err
	}
	encoded := EncodeTicket(ticket, s.cfg.DownloadTicketKey)
	base := strings.TrimRight(s.cfg.ClientContentBaseURL(), "/")
	return fmt.Sprintf("%s/thumbnails/%s?ticket=%s",
		base, url.PathEscape(fileID), url.QueryEscape(encoded)), file.ThumbnailStatus, nil
}

// ThumbnailURLResult is one entry of a batch thumbnail lookup. Error is set
// instead of URL/ThumbnailStatus when this file could not be resolved.
type ThumbnailURLResult struct {
	FileID          string
	URL             string
	ThumbnailStatus string
	Error           string
}

// MaxThumbnailURLBatch bounds one batch request. A folder page is 50 files;
// the cap keeps a hostile caller from forcing unbounded ownership reads and
// presigns inside the request timeout.
const MaxThumbnailURLBatch = 100

// GetThumbnailURLs resolves many files' preview URLs in one call, so a grid of
// cards costs one round trip instead of one per card.
//
// Each file is resolved independently: an unknown or unowned id is reported as
// that entry's Error rather than failing the batch, because one unreadable
// card must not blank the whole grid. Ownership and the ready check stay in
// GetThumbnailURL — the batch must never become a second, weaker auth path.
func (s *MetadataService) GetThumbnailURLs(ctx context.Context, ownerID string, fileIDs []string) ([]ThumbnailURLResult, error) {
	if len(fileIDs) == 0 {
		return nil, errors.New("file_ids is required")
	}
	if len(fileIDs) > MaxThumbnailURLBatch {
		return nil, fmt.Errorf("too many file ids: %d (max %d)", len(fileIDs), MaxThumbnailURLBatch)
	}
	results := make([]ThumbnailURLResult, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		url, status, err := s.GetThumbnailURL(ctx, ownerID, fileID)
		entry := ThumbnailURLResult{FileID: fileID, URL: url, ThumbnailStatus: status}
		if err != nil {
			entry.URL = ""
			entry.ThumbnailStatus = ""
			entry.Error = err.Error()
		}
		results = append(results, entry)
	}
	return results, nil
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
