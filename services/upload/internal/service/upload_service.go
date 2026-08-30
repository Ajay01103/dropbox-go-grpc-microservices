package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-notion/upload/config"
	"github.com/Ajay01103/go-notion/upload/internal/repository"
	"github.com/Ajay01103/go-notion/upload/internal/storagegateway"
)

// UploadService orchestrates chunked file uploads
type UploadService struct {
	sessionRepo    *repository.SessionRepo
	chunkRepo      *repository.ChunkRepo
	gateway        storagegateway.StorageGateway
	redisClient    *redis.Client
	cache          *ristretto.Cache
	cfg            config.Config
	logger         *zap.Logger
}

// New creates an UploadService with its dependencies wired
func New(
	sessionRepo *repository.SessionRepo,
	chunkRepo *repository.ChunkRepo,
	gateway storagegateway.StorageGateway,
	redisClient *redis.Client,
	cache *ristretto.Cache,
	cfg config.Config,
	logger *zap.Logger,
) *UploadService {
	return &UploadService{
		sessionRepo:    sessionRepo,
		chunkRepo:      chunkRepo,
		gateway:        gateway,
		redisClient:    redisClient,
		cache:          cache,
		cfg:            cfg,
		logger:         logger,
	}
}

// InitUploadResult contains the result of successful upload initialization
type InitUploadResult struct {
	UploadID                string
	ChunkSizeBytes          int64
	AlreadyReceivedOffsets  []int64
	AlreadyComplete         bool
	ObjectID                string
}

// InitUpload creates a new upload session
// Checks for dedup hit if full-file SHA256 is provided (Build Order 4)
func (s *UploadService) InitUpload(ctx context.Context, userID, filename, contentType string, totalSizeBytes int64, sha256OfFullFile string) (*InitUploadResult, error) {
	// Validate input
	if filename == "" || totalSizeBytes <= 0 {
		return nil, errors.New("invalid filename or total size")
	}

	// For Build Order 4: Check dedup
	// (Skip for Build Order 1, but structure is ready)
	if sha256OfFullFile != "" {
		// TODO: Query objects_by_hash for dedup hit (Build Order 4)
		// For now, proceed to normal upload
	}

	// Create session
	session, err := s.sessionRepo.CreateSession(
		ctx,
		userID,
		filename,
		contentType,
		totalSizeBytes,
		s.cfg.ChunkSizeBytes,
		s.cfg.SessionTTLSeconds,
	)
	if err != nil {
		s.logger.Error("failed to create session", zap.Error(err))
		return nil, fmt.Errorf("create session: %w", err)
	}

	uploadID := session.UploadID

	// Write to Redis for fast status lookups
	// TTL: 24 hours (matching session expiry)
	redisKey := fmt.Sprintf("upload:%s:last_offset", uploadID)
	_ = s.redisClient.SetEx(ctx, redisKey, "0", time.Duration(s.cfg.SessionTTLSeconds)*time.Second).Err()

	s.logger.Info("upload session initialized",
		zap.String("uploadID", uploadID),
		zap.String("userID", userID),
		zap.String("filename", filename),
		zap.Int64("totalSize", totalSizeBytes),
	)

	return &InitUploadResult{
		UploadID:               uploadID,
		ChunkSizeBytes:         s.cfg.ChunkSizeBytes,
		AlreadyReceivedOffsets: []int64{},
		AlreadyComplete:        false,
		ObjectID:               "",
	}, nil
}

// ChunkAckResult contains the result of a chunk write
type ChunkAckResult struct {
	UploadID           string
	OffsetPersisted    int64
	IsFinal            bool
}

// ReceiveChunk persists a chunk to storage and updates session state
// Implements dual-write pattern: Scylla first (durability), then Redis (speed)
func (s *UploadService) ReceiveChunk(ctx context.Context, uploadID string, offset int64, data []byte, sha256Chunk string) (*ChunkAckResult, error) {
	// Validate input
	if uploadID == "" || offset < 0 {
		return nil, errors.New("invalid uploadID or offset")
	}

	// Get session to verify upload is active
	session, err := s.sessionRepo.GetSession(ctx, uploadID)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	if session.Status != "pending" && session.Status != "in_progress" {
		return nil, fmt.Errorf("upload session not active: status=%s", session.Status)
	}

	// Verify checksum matches
	hash := sha256.Sum256(data)
	actualSha256 := hex.EncodeToString(hash[:])
	if actualSha256 != sha256Chunk {
		return nil, fmt.Errorf("checksum mismatch for chunk at offset %d", offset)
	}

	// Verify chunk size (must be exactly chunk_size except final chunk)
	isLastChunk := (offset + int64(len(data))) == session.TotalSize
	if !isLastChunk && int64(len(data)) != session.ChunkSize {
		return nil, fmt.Errorf("invalid chunk size: got %d, expected %d", len(data), session.ChunkSize)
	}

	// ─── DUAL-WRITE PATTERN: Scylla first, then Redis ───

	// 1. Write chunk to storage gateway
	if err := s.gateway.WriteChunk(ctx, uploadID, offset, data, sha256Chunk); err != nil {
		s.logger.Error("failed to write chunk to storage",
			zap.String("uploadID", uploadID),
			zap.Int64("offset", offset),
			zap.Error(err))
		return nil, fmt.Errorf("write chunk: %w", err)
	}

	// 2. Persist chunk metadata to Scylla (SOURCE OF TRUTH for durability)
	if err := s.chunkRepo.PersistChunk(ctx, uploadID, offset, sha256Chunk); err != nil {
		s.logger.Error("failed to persist chunk metadata",
			zap.String("uploadID", uploadID),
			zap.Int64("offset", offset),
			zap.Error(err))
		return nil, fmt.Errorf("persist chunk metadata: %w", err)
	}

	// 3. Update Redis with last_persisted_offset (CACHE LAYER for speed)
	// If Redis write fails, we still ack the chunk (Scylla is source of truth)
	redisKey := fmt.Sprintf("upload:%s:last_offset", uploadID)
	if err := s.redisClient.SetEx(ctx, redisKey, fmt.Sprintf("%d", offset), time.Duration(s.cfg.SessionTTLSeconds)*time.Second).Err(); err != nil {
		s.logger.Warn("failed to update Redis last_offset (non-fatal)",
			zap.String("uploadID", uploadID),
			zap.Int64("offset", offset),
			zap.Error(err))
		// Continue anyway; Scylla is the source of truth
	}

	// Update session status to in_progress if not already
	if session.Status == "pending" {
		_ = s.sessionRepo.UpdateSessionStatus(ctx, uploadID, "in_progress")
	}

	s.logger.Debug("chunk persisted",
		zap.String("uploadID", uploadID),
		zap.Int64("offset", offset),
		zap.Int("chunkSize", len(data)),
		zap.Bool("isFinal", isLastChunk))

	// If this is the final chunk, finalize the upload
	if isLastChunk {
		if err := s.finalizeUpload(ctx, uploadID, session.TotalSize); err != nil {
			s.logger.Error("failed to finalize upload",
				zap.String("uploadID", uploadID),
				zap.Error(err))
			return nil, fmt.Errorf("finalize upload: %w", err)
		}
	}

	return &ChunkAckResult{
		UploadID:        uploadID,
		OffsetPersisted: offset + int64(len(data)),
		IsFinal:         isLastChunk,
	}, nil
}

// GetUploadStatusResult contains upload status information
type GetUploadStatusResult struct {
	UploadID           string
	LastPersistedOffset int64
	TotalSizeBytes      int64
	Status              string
}

// GetUploadStatus returns the current upload status and last persisted offset
// Implements graceful fallback: Redis first (fast), then Scylla scan (durable)
func (s *UploadService) GetUploadStatus(ctx context.Context, uploadID string) (*GetUploadStatusResult, error) {
	// Get session info
	session, err := s.sessionRepo.GetSession(ctx, uploadID)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	// Try Redis first (fast path)
	redisKey := fmt.Sprintf("upload:%s:last_offset", uploadID)
	offsetStr, err := s.redisClient.Get(ctx, redisKey).Result()
	if err == nil && offsetStr != "" {
		// Parse offset
		var lastOffset int64
		fmt.Sscanf(offsetStr, "%d", &lastOffset)
		return &GetUploadStatusResult{
			UploadID:            uploadID,
			LastPersistedOffset: lastOffset,
			TotalSizeBytes:      session.TotalSize,
			Status:              session.Status,
		}, nil
	}

	// Redis miss or error: fall back to Scylla scan (durable path)
	s.logger.Info("Redis miss or error for upload status, falling back to Scylla scan",
		zap.String("uploadID", uploadID),
		zap.Error(err))

	lastOffset, err := s.chunkRepo.GetMaxPersistedOffset(ctx, uploadID)
	if err != nil {
		s.logger.Error("failed to scan Scylla for max offset",
			zap.String("uploadID", uploadID),
			zap.Error(err))
		return nil, fmt.Errorf("get max offset: %w", err)
	}

	return &GetUploadStatusResult{
		UploadID:            uploadID,
		LastPersistedOffset: lastOffset,
		TotalSizeBytes:      session.TotalSize,
		Status:              session.Status,
	}, nil
}

// finalizeUpload is called after the last chunk is received
// Verifies all chunks are present and marks session as completed
func (s *UploadService) finalizeUpload(ctx context.Context, uploadID string, totalSize int64) error {
	// Verify all chunks exist by calling the storage gateway
	storageKey, err := s.gateway.FinalizeUpload(ctx, uploadID, totalSize)
	if err != nil {
		s.logger.Error("storage gateway finalization failed",
			zap.String("uploadID", uploadID),
			zap.Error(err))
		return fmt.Errorf("finalize in storage: %w", err)
	}

	// Mark session as completed
	if err := s.sessionRepo.UpdateSessionStatus(ctx, uploadID, "completed"); err != nil {
		s.logger.Error("failed to mark session as completed",
			zap.String("uploadID", uploadID),
			zap.Error(err))
		return fmt.Errorf("update session status: %w", err)
	}

	s.logger.Info("upload finalized",
		zap.String("uploadID", uploadID),
		zap.String("storageKey", storageKey))

	// TODO: Build Order 2 - Publish ObjectStored event to event bus
	// This will trigger metadata service to create file record
	// and async workers (scan, thumbnail, search)

	return nil
}

// AbortUpload cancels an upload and cleans up resources
func (s *UploadService) AbortUpload(ctx context.Context, uploadID string) error {
	// Update session status
	if err := s.sessionRepo.UpdateSessionStatus(ctx, uploadID, "aborted"); err != nil {
		s.logger.Warn("failed to mark session as aborted",
			zap.String("uploadID", uploadID),
			zap.Error(err))
	}

	// Clean up storage
	if err := s.gateway.AbortUpload(ctx, uploadID); err != nil {
		s.logger.Warn("failed to clean up storage",
			zap.String("uploadID", uploadID),
			zap.Error(err))
	}

	// Clean up Redis
	redisKey := fmt.Sprintf("upload:%s:last_offset", uploadID)
	_ = s.redisClient.Del(ctx, redisKey)

	s.logger.Info("upload aborted",
		zap.String("uploadID", uploadID))

	return nil
}
