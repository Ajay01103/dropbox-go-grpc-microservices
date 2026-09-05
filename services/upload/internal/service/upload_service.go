package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/dgraph-io/ristretto"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	httpclient "net/http"

	metadataPB "github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	metadatapbconnect "github.com/Ajay01103/go-dropbox/metadata/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	"github.com/Ajay01103/go-dropbox/upload/config"
	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
	"github.com/Ajay01103/go-dropbox/upload/internal/storagegateway"
)

// ObjectStoredEvent is the async event published after a finalized upload is persisted.
type ObjectStoredEvent struct {
	SchemaVersion string   `json:"schema_version"`
	FileID        string   `json:"file_id"`
	BlockHashList []string `json:"block_hash_list,omitempty"`
	ContentType   string   `json:"content_type"`
	SizeBytes     int64    `json:"size_bytes"`
	OwnerID       string   `json:"owner_id"`
	StoredAtUnix  int64    `json:"stored_at_unix"`
}

// EventPublisher publishes upload lifecycle events to the async bus.
type EventPublisher interface {
	PublishObjectStored(context.Context, *ObjectStoredEvent) error
}

// NoopEventPublisher keeps the upload path decoupled from the bus when no publisher is configured.
type NoopEventPublisher struct{}

func (NoopEventPublisher) PublishObjectStored(context.Context, *ObjectStoredEvent) error {
	return nil
}

// NATSEventPublisher publishes to JetStream using a durable subject and per-file message dedup.
type NATSEventPublisher struct {
	js      nats.JetStreamContext
	subject string
	conn    *nats.Conn
}

func NewNATSEventPublisher(url, subject string) (*NATSEventPublisher, error) {
	if url == "" {
		return nil, errors.New("nats url is empty")
	}

	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	const streamName = "UPLOAD_EVENTS"
	if _, err := js.StreamInfo(streamName); err != nil {
		if !errors.Is(err, nats.ErrStreamNotFound) {
			nc.Close()
			return nil, fmt.Errorf("inspect jetstream stream %s: %w", streamName, err)
		}

		if _, err := js.AddStream(&nats.StreamConfig{
			Name:      streamName,
			Subjects:  []string{subject},
			Retention: nats.LimitsPolicy,
			Storage:   nats.FileStorage,
			MaxAge:    7 * 24 * time.Hour,
		}); err != nil {
			nc.Close()
			return nil, fmt.Errorf("create jetstream stream %s: %w", streamName, err)
		}
	}

	return &NATSEventPublisher{js: js, subject: subject, conn: nc}, nil
}

func (p *NATSEventPublisher) Close() {
	if p != nil && p.conn != nil {
		p.conn.Drain()
		p.conn.Close()
	}
}

func (p *NATSEventPublisher) PublishObjectStored(ctx context.Context, evt *ObjectStoredEvent) error {
	if p == nil || p.js == nil {
		return nil
	}
	if evt == nil {
		return nil
	}

	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal object stored event: %w", err)
	}

	_, err = p.js.Publish(p.subject, payload, nats.MsgId(evt.FileID))
	return err
}

// UploadService orchestrates chunked file uploads
type UploadService struct {
	sessionRepo    *repository.SessionRepo
	blockRepo      *repository.BlockRepo
	gateway        storagegateway.StorageGateway
	blockGateway   storagegateway.BlockGateway
	blockBackend   string
	redisClient    *redis.Client
	cache          *ristretto.Cache
	eventPublisher EventPublisher
	cfg            config.Config
	logger         *zap.Logger
}

// New creates an UploadService with its dependencies wired
func New(
	sessionRepo *repository.SessionRepo,
	blockRepo *repository.BlockRepo,
	gateway storagegateway.StorageGateway,
	blockGateway storagegateway.BlockGateway,
	redisClient *redis.Client,
	cache *ristretto.Cache,
	cfg config.Config,
	logger *zap.Logger,
	eventPublisher EventPublisher,
	blockBackend string,
) *UploadService {
	if eventPublisher == nil {
		eventPublisher = NoopEventPublisher{}
	}
	return &UploadService{
		sessionRepo:    sessionRepo,
		blockRepo:      blockRepo,
		gateway:        gateway,
		blockGateway:   blockGateway,
		blockBackend:   blockBackend,
		redisClient:    redisClient,
		cache:          cache,
		eventPublisher: eventPublisher,
		cfg:            cfg,
		logger:         logger,
	}
}

// InitUploadResult contains the result of successful upload initialization
type InitUploadResult struct {
	UploadID               string
	ChunkSizeBytes         int64
	AlreadyReceivedOffsets []int64
	AlreadyReceivedChunkIndices []int32
	AlreadyComplete        bool
	ObjectID               string
}

// UploadSession is the persisted upload session state used by helper functions.
type UploadSession = repository.UploadSession

func parseRedisOffset(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse redis offset: %w", err)
	}
	return parsed, nil
}

func buildMetadataCreateRequest(session UploadSession, blockHashes []string) *metadataPB.CreateFileRequest {
	return &metadataPB.CreateFileRequest{
		FolderId:    session.UserID,
		Filename:    session.Filename,
		SizeBytes:   session.TotalSize,
		ContentType: session.ContentType,
		ContentHash: session.ContentHash,
		BlockHashList: blockHashes,
	}
}

// InitUpload creates a new upload session
func (s *UploadService) InitUpload(ctx context.Context, userID, filename, contentType string, totalSizeBytes int64, sha256OfFullFile string) (*InitUploadResult, error) {
	if filename == "" || totalSizeBytes <= 0 {
		return nil, errors.New("invalid filename or total size")
	}

	session, err := s.sessionRepo.CreateSession(
		ctx,
		userID,
		filename,
		contentType,
		totalSizeBytes,
		s.cfg.ChunkSizeBytes,
		sha256OfFullFile,
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

	if s.cfg.MetadataURL != "" {
		s.logger.Debug("metadata callback URL configured", zap.String("metadataURL", s.cfg.MetadataURL))
	}

	s.logger.Info("upload session initialized",
		zap.String("uploadID", uploadID),
		zap.String("userID", userID),
		zap.String("filename", filename),
		zap.Int64("totalSize", totalSizeBytes),
	)

	return &InitUploadResult{
		UploadID:                    uploadID,
		ChunkSizeBytes:              s.cfg.ChunkSizeBytes,
		AlreadyReceivedOffsets:      []int64{},
		AlreadyReceivedChunkIndices: []int32{},
		AlreadyComplete:              false,
		ObjectID:                     "",
	}, nil
}

// ChunkAckResult contains the result of a chunk write
type ChunkAckResult struct {
	UploadID        string
	OffsetPersisted int64
	IsFinal         bool
	Deduplicated    bool
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
	if session.BlockSizeBytes <= 0 || offset%session.BlockSizeBytes != 0 {
		return nil, fmt.Errorf("invalid chunk offset: %d", offset)
	}
	isLastChunk := (offset + int64(len(data))) == session.TotalSize
	if !isLastChunk && int64(len(data)) != session.BlockSizeBytes {
		return nil, fmt.Errorf("invalid block size: got %d, expected %d", len(data), session.BlockSizeBytes)
	}

	chunkIndex := int(offset / session.BlockSizeBytes)
	deduplicated := false
	if s.blockRepo != nil && s.blockGateway != nil {
		knownHash, alreadyRecorded := session.ChunkBlockMap[chunkIndex]
		if !alreadyRecorded {
			block, lookupErr := s.blockRepo.GetBlock(ctx, actualSha256)
			if lookupErr == nil {
				exists, existsErr := s.blockGateway.BlockExists(ctx, actualSha256)
				if existsErr != nil {
					return nil, fmt.Errorf("check block storage: %w", existsErr)
				}
				if !exists {
					if _, writeErr := s.blockGateway.WriteBlock(ctx, actualSha256, data); writeErr != nil {
						return nil, fmt.Errorf("repair missing block storage: %w", writeErr)
					}
				} else {
					deduplicated = true
				}
				if err := s.blockRepo.IncrementRefCount(ctx, actualSha256); err != nil {
					return nil, fmt.Errorf("increment block reference: %w", err)
				}
				_ = block
			} else if !repository.IsBlockNotFound(lookupErr) {
				return nil, fmt.Errorf("lookup block: %w", lookupErr)
			} else {
				etag, writeErr := s.blockGateway.WriteBlock(ctx, actualSha256, data)
				if writeErr != nil {
					return nil, fmt.Errorf("write block: %w", writeErr)
				}
				if err := s.blockRepo.InsertBlock(ctx, actualSha256, int64(len(data)), etag, s.blockBackend); err != nil {
					return nil, fmt.Errorf("insert block: %w", err)
				}
			}
			if err := s.sessionRepo.RecordBlockForChunk(ctx, uploadID, chunkIndex, actualSha256); err != nil {
				return nil, fmt.Errorf("record block for chunk: %w", err)
			}
		} else if knownHash != actualSha256 {
			return nil, fmt.Errorf("chunk index %d already maps to a different block", chunkIndex)
		}
	}

	// ─── DUAL-WRITE PATTERN: Scylla first, then Redis ───

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
	complete := isLastChunk
	if s.blockRepo != nil && s.blockGateway != nil {
		fresh, refreshErr := s.sessionRepo.GetSession(ctx, uploadID)
		if refreshErr != nil {
			return nil, fmt.Errorf("refresh upload session: %w", refreshErr)
		}
		expectedChunks := int((fresh.TotalSize + fresh.BlockSizeBytes - 1) / fresh.BlockSizeBytes)
		complete = len(fresh.ChunkBlockMap) == expectedChunks
	}
	if complete {
		if err := s.finalizeUpload(ctx, uploadID); err != nil {
			s.logger.Error("failed to finalize upload",
				zap.String("uploadID", uploadID),
				zap.Error(err))
			return nil, fmt.Errorf("finalize upload: %w", err)
		}
	}

	return &ChunkAckResult{
		UploadID:        uploadID,
		OffsetPersisted: offset + int64(len(data)),
		IsFinal:         complete,
		Deduplicated:    deduplicated,
	}, nil
}

// GetUploadStatusResult contains upload status information
type GetUploadStatusResult struct {
	UploadID            string
	LastPersistedOffset int64
	TotalSizeBytes      int64
	Status              string
	ReceivedChunkIndices []int32
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
		lastOffset, parseErr := parseRedisOffset(offsetStr)
		if parseErr != nil {
			s.logger.Warn("invalid redis offset, falling back to Scylla", zap.String("uploadID", uploadID), zap.String("value", offsetStr), zap.Error(parseErr))
		} else {
			return &GetUploadStatusResult{
				UploadID:            uploadID,
				LastPersistedOffset: lastOffset,
				TotalSizeBytes:      session.TotalSize,
				Status:              session.Status,
				ReceivedChunkIndices: receivedChunkIndices(session),
			}, nil
		}
	}
	if err != nil {
		s.logger.Info("Redis miss or error for upload status, falling back to Scylla scan",
			zap.String("uploadID", uploadID),
			zap.Error(err))
	}

	return &GetUploadStatusResult{
		UploadID:            uploadID,
		LastPersistedOffset: 0,
		TotalSizeBytes:      session.TotalSize,
		Status:              session.Status,
		ReceivedChunkIndices: receivedChunkIndices(session),
	}, nil
}

func receivedChunkIndices(session UploadSession) []int32 {
	indices := session.ReceivedChunkIndices()
	result := make([]int32, len(indices))
	for i, index := range indices {
		result[i] = int32(index)
	}
	return result
}

// finalizeUpload is called after the last chunk is received
// Verifies all chunks are present and marks session as completed
func (s *UploadService) finalizeUpload(ctx context.Context, uploadID string) error {
	session, err := s.sessionRepo.GetSession(ctx, uploadID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	blockHashes := orderedBlockHashes(session)
	fileID, err := s.createMetadataRecord(ctx, session, blockHashes)
	if err != nil {
		return fmt.Errorf("create metadata record: %w", err)
	}

	if err := s.publishObjectStoredEvent(ctx, fileID, session, blockHashes); err != nil {
		s.logger.Warn("object stored event publish failed; reconciliation will repair",
			zap.String("uploadID", uploadID),
			zap.String("fileID", fileID),
			zap.Error(err),
		)
	}
	if err := s.sessionRepo.UpdateSessionStatus(ctx, uploadID, "completed"); err != nil {
		return fmt.Errorf("mark upload completed: %w", err)
	}

	s.logger.Info("upload finalized",
		zap.String("uploadID", uploadID),
		zap.Int("blockCount", len(blockHashes)))

	return nil
}

func orderedBlockHashes(session UploadSession) []string {
	count := int((session.TotalSize + session.BlockSizeBytes - 1) / session.BlockSizeBytes)
	result := make([]string, 0, count)
	for index := 0; index < count; index++ {
		if hash, ok := session.ChunkBlockMap[index]; ok {
			result = append(result, hash)
		}
	}
	return result
}

func (s *UploadService) createMetadataRecord(ctx context.Context, session UploadSession, blockHashes []string) (string, error) {
	if s.cfg.MetadataURL == "" {
		return "", nil
	}
	if session.UserID == "" || session.Filename == "" {
		return "", nil
	}
	token, err := interceptor.AccessTokenFromContext(ctx)
	if err != nil {
		s.logger.Error("no access token in context for metadata call",
			zap.String("uploadID", session.UploadID),
			zap.Error(err))
		return "", fmt.Errorf("missing access token for metadata call: %w", err)
	}
	client := metadatapbconnect.NewMetadataServiceClient(httpclient.DefaultClient, s.cfg.MetadataURL)
	req := connect.NewRequest(buildMetadataCreateRequest(session, blockHashes))
	req.Header().Set("Authorization", "Bearer "+token)
	resp, err := client.CreateFile(ctx, req)
	if err != nil {
		s.logger.Error("metadata create file failed",
			zap.String("uploadID", session.UploadID),
			zap.String("filename", session.Filename),
			zap.Error(err))
		return "", err
	}
	return resp.Msg.GetFileId(), nil
}

func (s *UploadService) publishObjectStoredEvent(ctx context.Context, fileID string, session UploadSession, blockHashes []string) error {
	if s.eventPublisher == nil {
		return nil
	}
	if fileID == "" || session.UserID == "" || session.Filename == "" {
		return nil
	}

	evt := &ObjectStoredEvent{
		SchemaVersion: "v2",
		FileID:        fileID,
		BlockHashList: blockHashes,
		ContentType:   session.ContentType,
		SizeBytes:     session.TotalSize,
		OwnerID:       session.UserID,
		StoredAtUnix:  time.Now().Unix(),
	}

	if err := s.eventPublisher.PublishObjectStored(ctx, evt); err != nil {
		return fmt.Errorf("publish object stored event: %w", err)
	}
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
	s.logger.Info("upload aborted",
		zap.String("uploadID", uploadID))

	return nil
}
