package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

// UploadSession represents an upload session record
type UploadSession struct {
	UploadID       string
	UserID         string
	Filename       string
	TotalSize      int64
	BlockSizeBytes int64
	ChunkBlockMap  map[int]string
	UploadedBitmap []byte
	ContentType    string
	ContentHash    string
	Status         string // pending | in_progress | completed | aborted
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// SessionRepo provides data access for upload sessions using ScyllaDB
type SessionRepo struct {
	session *gocql.Session
}

// NewSessionRepo creates a SessionRepo backed by a ScyllaDB session
func NewSessionRepo(session *gocql.Session) *SessionRepo {
	return &SessionRepo{session: session}
}

// CreateSession creates a new upload session
func (r *SessionRepo) CreateSession(ctx context.Context, userID, filename, contentType string, totalSize, chunkSize int64, contentHash string, ttlSeconds int64) (UploadSession, error) {
	uploadID := uuid.New().String()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second)

	uploadSession := UploadSession{
		UploadID:       uploadID,
		UserID:         userID,
		Filename:       filename,
		TotalSize:      totalSize,
		BlockSizeBytes: chunkSize,
		ChunkBlockMap:  map[int]string{},
		UploadedBitmap: []byte{},
		ContentType:    contentType,
		ContentHash:    contentHash,
		Status:         "pending",
		CreatedAt:      now,
		ExpiresAt:      expiresAt,
	}

	if err := r.session.Query(
		`INSERT INTO upload_sessions (upload_id, user_id, filename, total_size, block_size_bytes, chunk_block_map, uploaded_bitmap, content_type, content_hash, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uploadID, userID, filename, totalSize, chunkSize, map[int]string{}, []byte{}, contentType, contentHash, "pending", now, expiresAt,
	).WithContext(ctx).Exec(); err != nil {
		return UploadSession{}, fmt.Errorf("insert upload session: %w", err)
	}

	return uploadSession, nil
}

// GetSession retrieves an upload session by ID
func (r *SessionRepo) GetSession(ctx context.Context, uploadID string) (UploadSession, error) {
	var session UploadSession
	err := r.session.Query(
		`SELECT upload_id, user_id, filename, total_size, block_size_bytes, chunk_block_map, uploaded_bitmap, content_type, content_hash, status, created_at, expires_at
		FROM upload_sessions WHERE upload_id = ? LIMIT 1`,
		uploadID,
	).WithContext(ctx).Scan(
		&session.UploadID, &session.UserID, &session.Filename, &session.TotalSize,
		&session.BlockSizeBytes, &session.ChunkBlockMap, &session.UploadedBitmap,
		&session.ContentType, &session.ContentHash, &session.Status, &session.CreatedAt, &session.ExpiresAt,
	)
	if err == gocql.ErrNotFound {
		return UploadSession{}, errors.New("session not found")
	}
	if err != nil {
		return UploadSession{}, fmt.Errorf("get session: %w", err)
	}
	return session, nil
}

// RecordBlockForChunk stores the durable chunk-to-block mapping and marks the
// chunk index as received. Callers should serialize updates for one upload.
func (r *SessionRepo) RecordBlockForChunk(ctx context.Context, uploadID string, chunkIndex int, blockHash string) error {
	if chunkIndex < 0 || blockHash == "" {
		return errors.New("chunk index and block hash are required")
	}
	session, err := r.GetSession(ctx, uploadID)
	if err != nil {
		return err
	}
	if session.ChunkBlockMap == nil {
		session.ChunkBlockMap = make(map[int]string)
	}
	session.ChunkBlockMap[chunkIndex] = blockHash
	session.UploadedBitmap = SetBitmapBit(session.UploadedBitmap, chunkIndex)
	return r.session.Query(
		`UPDATE upload_sessions SET chunk_block_map = ?, uploaded_bitmap = ? WHERE upload_id = ?`,
		session.ChunkBlockMap, session.UploadedBitmap, uploadID,
	).WithContext(ctx).Exec()
}

// ReceivedChunkIndices decodes the durable bitmap for status responses.
func (s UploadSession) ReceivedChunkIndices() []int {
	indices := make([]int, 0, len(s.ChunkBlockMap))
	for index := range s.ChunkBlockMap {
		if HasBitmapBit(s.UploadedBitmap, index) {
			indices = append(indices, index)
		}
	}
	sort.Ints(indices)
	return indices
}

// UpdateSessionStatus updates the status of an upload session
func (r *SessionRepo) UpdateSessionStatus(ctx context.Context, uploadID, status string) error {
	return r.session.Query(
		`UPDATE upload_sessions SET status = ? WHERE upload_id = ?`,
		status, uploadID,
	).WithContext(ctx).Exec()
}
