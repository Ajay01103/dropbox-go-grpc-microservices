package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

// UploadSession represents an upload session record
type UploadSession struct {
	UploadID    string    `db:"upload_id"`
	UserID      string    `db:"user_id"`
	Filename    string    `db:"filename"`
	TotalSize   int64     `db:"total_size"`
	ChunkSize   int64     `db:"chunk_size"`
	ContentType string    `db:"content_type"`
	Status      string    `db:"status"` // pending | in_progress | completed | aborted
	CreatedAt   time.Time `db:"created_at"`
	ExpiresAt   time.Time `db:"expires_at"`
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
func (r *SessionRepo) CreateSession(ctx context.Context, userID, filename, contentType string, totalSize, chunkSize int64, ttlSeconds int64) (UploadSession, error) {
	uploadID := uuid.New().String()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second)

	uploadSession := UploadSession{
		UploadID:    uploadID,
		UserID:      userID,
		Filename:    filename,
		TotalSize:   totalSize,
		ChunkSize:   chunkSize,
		ContentType: contentType,
		Status:      "pending",
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
	}

	if err := r.session.Query(
		`INSERT INTO upload_sessions (upload_id, user_id, filename, total_size, chunk_size, content_type, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uploadID, userID, filename, totalSize, chunkSize, contentType, "pending", now, expiresAt,
	).WithContext(ctx).Exec(); err != nil {
		return UploadSession{}, fmt.Errorf("insert upload session: %w", err)
	}

	return uploadSession, nil
}

// GetSession retrieves an upload session by ID
func (r *SessionRepo) GetSession(ctx context.Context, uploadID string) (UploadSession, error) {
	var session UploadSession
	err := r.session.Query(
		`SELECT upload_id, user_id, filename, total_size, chunk_size, content_type, status, created_at, expires_at
		FROM upload_sessions WHERE upload_id = ? LIMIT 1`,
		uploadID,
	).WithContext(ctx).Scan(
		&session.UploadID, &session.UserID, &session.Filename, &session.TotalSize,
		&session.ChunkSize, &session.ContentType, &session.Status, &session.CreatedAt, &session.ExpiresAt,
	)
	if err == gocql.ErrNotFound {
		return UploadSession{}, errors.New("session not found")
	}
	if err != nil {
		return UploadSession{}, fmt.Errorf("get session: %w", err)
	}
	return session, nil
}

// UpdateSessionStatus updates the status of an upload session
func (r *SessionRepo) UpdateSessionStatus(ctx context.Context, uploadID, status string) error {
	return r.session.Query(
		`UPDATE upload_sessions SET status = ? WHERE upload_id = ?`,
		status, uploadID,
	).WithContext(ctx).Exec()
}

// UploadChunk represents a persisted chunk record
type UploadChunk struct {
	UploadID        string    `db:"upload_id"`
	Offset          int64     `db:"offset"`
	SHA256Checksum  string    `db:"sha256_checksum"`
	PersistedAt     time.Time `db:"persisted_at"`
}

// ChunkRepo provides data access for upload chunks using ScyllaDB
type ChunkRepo struct {
	session *gocql.Session
}

// NewChunkRepo creates a ChunkRepo backed by a ScyllaDB session
func NewChunkRepo(session *gocql.Session) *ChunkRepo {
	return &ChunkRepo{session: session}
}

// PersistChunk records a persisted chunk with its offset and checksum
func (r *ChunkRepo) PersistChunk(ctx context.Context, uploadID string, offset int64, sha256Checksum string) error {
	now := time.Now().UTC()
	return r.session.Query(
		`INSERT INTO upload_chunks (upload_id, offset, sha256_checksum, persisted_at)
		VALUES (?, ?, ?, ?)`,
		uploadID, offset, sha256Checksum, now,
	).WithContext(ctx).Exec()
}

// GetMaxPersistedOffset returns the maximum offset persisted for a given upload
func (r *ChunkRepo) GetMaxPersistedOffset(ctx context.Context, uploadID string) (int64, error) {
	var maxOffset int64
	iter := r.session.Query(
		`SELECT MAX(offset) FROM upload_chunks WHERE upload_id = ?`,
		uploadID,
	).WithContext(ctx).Iter()
	defer iter.Close()

	if iter.Scan(&maxOffset) {
		return maxOffset, nil
	}

	if err := iter.Close(); err != nil {
		return 0, fmt.Errorf("get max offset: %w", err)
	}

	// No chunks found, return 0
	return 0, nil
}

// GetPersistedChunk retrieves a specific chunk by upload_id and offset
func (r *ChunkRepo) GetPersistedChunk(ctx context.Context, uploadID string, offset int64) (UploadChunk, error) {
	var chunk UploadChunk
	err := r.session.Query(
		`SELECT upload_id, offset, sha256_checksum, persisted_at
		FROM upload_chunks WHERE upload_id = ? AND offset = ? LIMIT 1`,
		uploadID, offset,
	).WithContext(ctx).Scan(
		&chunk.UploadID, &chunk.Offset, &chunk.SHA256Checksum, &chunk.PersistedAt,
	)
	if err == gocql.ErrNotFound {
		return UploadChunk{}, errors.New("chunk not found")
	}
	if err != nil {
		return UploadChunk{}, fmt.Errorf("get chunk: %w", err)
	}
	return chunk, nil
}

// ListChunks returns all chunks for a given upload in order
func (r *ChunkRepo) ListChunks(ctx context.Context, uploadID string) ([]UploadChunk, error) {
	var chunks []UploadChunk
	iter := r.session.Query(
		`SELECT upload_id, offset, sha256_checksum, persisted_at
		FROM upload_chunks WHERE upload_id = ?`,
		uploadID,
	).WithContext(ctx).Iter()
	defer iter.Close()

	var chunk UploadChunk
	for iter.Scan(&chunk.UploadID, &chunk.Offset, &chunk.SHA256Checksum, &chunk.PersistedAt) {
		chunks = append(chunks, chunk)
	}

	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("list chunks: %w", err)
	}

	return chunks, nil
}
