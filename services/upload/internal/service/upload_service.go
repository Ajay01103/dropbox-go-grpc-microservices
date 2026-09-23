package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/dgraph-io/ristretto"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"

	httpclient "net/http"

	metadataPB "github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	metadatapbconnect "github.com/Ajay01103/go-dropbox/metadata/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/pkg/events"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
	"github.com/Ajay01103/go-dropbox/upload/config"
	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
	"github.com/Ajay01103/go-dropbox/upload/internal/storagegateway"
)

// FileStoredEvent is the upload-side input to the event publisher: the fields
// the FileStored proto needs (version is always 1 today).
type FileStoredEvent struct {
	FileID        string
	BlockHashList []string
	ContentType   string
	SizeBytes     int64
	OwnerID       string
	StoredAtUnix  int64
}

// EventPublisher publishes upload lifecycle events to the async bus (the v2
// FileStored proto on files.v2.stored — the only topology since B4).
type EventPublisher interface {
	PublishFileStored(context.Context, *FileStoredEvent) error
}

// noopLoggingPublisher is the deliberate non-test fallback used when NATS is
// down and NATS_REQUIRED=false (dev). It is NOT silent: it warns loudly so
// nobody mistakes a dead event bus for a healthy one — durable state (the
// thumbnail pending row) is written before publish, so the thumbnail
// sweeper repairs the gap, but nothing else on the bus will work.
type noopLoggingPublisher struct{ logger *zap.Logger }

func (p noopLoggingPublisher) PublishFileStored(ctx context.Context, evt *FileStoredEvent) error {
	p.logger.Warn("EVENT DROPPED: no NATS publisher available (NATS down, NATS_REQUIRED=false) — "+
		"thumbnail generation and any future bus consumers will not see this file",
		zap.String("fileID", evt.FileID))
	return nil
}

// NewNoopLoggingPublisher builds the non-test fallback publisher used when
// NATS is down and NATS_REQUIRED=false. It warns loudly on every publish.
func NewNoopLoggingPublisher(logger *zap.Logger) EventPublisher {
	return noopLoggingPublisher{logger: logger}
}

// NATSEventPublisher publishes the protobuf FileStored event to
// files.v2.stored with the message conventions (Nats-Msg-Id, X-Type) and a
// bounded retry instead of log-and-forget.
type NATSEventPublisher struct {
	jsv2 jetstream.JetStream
	conn *nats.Conn
}

// NewNATSEventPublisher connects and ensures the v2 topology (streams are
// idempotent creates, safe on every boot).
func NewNATSEventPublisher(ctx context.Context, url string) (*NATSEventPublisher, error) {
	if url == "" {
		return nil, errors.New("nats url is empty")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats (v2): %w", err)
	}
	jsv2, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create jetstream (v2): %w", err)
	}
	if err := natsx.EnsureTopology(ctx, jsv2, 1); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure v2 topology: %w", err)
	}
	return &NATSEventPublisher{jsv2: jsv2, conn: nc}, nil
}

func (p *NATSEventPublisher) Close() {
	if p != nil && p.conn != nil {
		p.conn.Drain()
	}
}

// PublishFileStored implements the v2 publish path with bounded retry:
// 3 tries at 200ms / 1s / 3s. A final failure is returned to the caller,
// which logs a warning — the thumbnail sweeper repairs the gap because the
// metadata row is created before publish with thumbnail_status='pending'.
func (p *NATSEventPublisher) PublishFileStored(ctx context.Context, evt *FileStoredEvent) error {
	if p == nil || p.jsv2 == nil || evt == nil {
		return nil
	}
	hashes := make([][]byte, 0, len(evt.BlockHashList))
	for _, h := range evt.BlockHashList {
		b, err := hex.DecodeString(h)
		if err != nil {
			return fmt.Errorf("decode block hash: %w", err)
		}
		hashes = append(hashes, b)
	}
	msg := &eventsv2.FileStored{
		FileId:      evt.FileID,
		FileVersion: 1, // creation is always version 1 today
		OwnerId:     evt.OwnerID,
		ContentType: evt.ContentType,
		SizeBytes:   evt.SizeBytes,
		BlockHashes: hashes,
		StoredAt:    timestamppb.New(time.Unix(evt.StoredAtUnix, 0).UTC()),
	}
	msgID := events.MsgIDFileStored(evt.FileID, 1)

	const tries = 3
	var lastErr error
	for i, delay := range []time.Duration{0, 200 * time.Millisecond, 1 * time.Second, 3 * time.Second} {
		if i == tries+1 {
			break
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		lastErr = natsx.PublishProto(ctx, p.jsv2, events.SubjFileStored, msgID, msg)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("publish FileStored after %d tries: %w", tries, lastErr)
}

// UploadService orchestrates chunked file uploads
type UploadService struct {
	sessionRepo    *repository.SessionRepo
	blockRepo      *repository.BlockRepo
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
	blockGateway storagegateway.BlockGateway,
	redisClient *redis.Client,
	cache *ristretto.Cache,
	cfg config.Config,
	logger *zap.Logger,
	eventPublisher EventPublisher,
	blockBackend string,
) *UploadService {
	if eventPublisher == nil {
		eventPublisher = noopLoggingPublisher{logger: logger}
	}
	return &UploadService{
		sessionRepo:    sessionRepo,
		blockRepo:      blockRepo,
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
	UploadID                    string
	ChunkSizeBytes              int64
	AlreadyReceivedOffsets      []int64
	AlreadyReceivedChunkIndices []int32
	AlreadyComplete             bool
	ObjectID                    string
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
		FolderId:      session.UserID,
		Filename:      session.Filename,
		SizeBytes:     session.TotalSize,
		ContentType:   session.ContentType,
		ContentHash:   session.ContentHash,
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
		AlreadyComplete:             false,
		ObjectID:                    "",
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
					if repository.IsBlockDeleting(err) {
						// The block is fenced for GC deletion. The fence will fail
						// on GC's next look (we hold the data) or the row is
						// gone; a short client retry re-uploads cleanly.
						return nil, connect.NewError(connect.CodeUnavailable,
							fmt.Errorf("block is being deleted, retry shortly: %s", actualSha256))
					}
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
	UploadID             string
	LastPersistedOffset  int64
	TotalSizeBytes       int64
	Status               string
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
				UploadID:             uploadID,
				LastPersistedOffset:  lastOffset,
				TotalSizeBytes:       session.TotalSize,
				Status:               session.Status,
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
		UploadID:             uploadID,
		LastPersistedOffset:  0,
		TotalSizeBytes:       session.TotalSize,
		Status:               session.Status,
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

	if err := s.publishFileStoredEvent(ctx, fileID, session, blockHashes); err != nil {
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

func (s *UploadService) publishFileStoredEvent(ctx context.Context, fileID string, session UploadSession, blockHashes []string) error {
	if s.eventPublisher == nil {
		return nil
	}
	if fileID == "" || session.UserID == "" || session.Filename == "" {
		return nil
	}

	evt := &FileStoredEvent{
		FileID:        fileID,
		BlockHashList: blockHashes,
		ContentType:   session.ContentType,
		SizeBytes:     session.TotalSize,
		OwnerID:       session.UserID,
		StoredAtUnix:  time.Now().Unix(),
	}

	if err := s.eventPublisher.PublishFileStored(ctx, evt); err != nil {
		return fmt.Errorf("publish file stored event: %w", err)
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

	s.logger.Info("upload aborted",
		zap.String("uploadID", uploadID))

	return nil
}
