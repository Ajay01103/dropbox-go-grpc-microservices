package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

type Block struct {
	Hash           string
	SizeBytes      int64
	RefCount       int64
	StorageBackend string
	StorageKey     string
	ETag           string
	CreatedAt      time.Time
}

type BlockRepo struct {
	session *gocql.Session
}

func NewBlockRepo(session *gocql.Session) *BlockRepo {
	return &BlockRepo{session: session}
}

func (r *BlockRepo) GetBlock(ctx context.Context, hash string) (Block, error) {
	var block Block
	err := r.session.Query(
		`SELECT block_hash, size_bytes, ref_count, storage_backend, storage_key, etag, created_at
		FROM blocks WHERE block_hash = ? LIMIT 1`, hash,
	).WithContext(ctx).Scan(
		&block.Hash, &block.SizeBytes, &block.RefCount, &block.StorageBackend,
		&block.StorageKey, &block.ETag, &block.CreatedAt,
	)
	if err == gocql.ErrNotFound {
		return Block{}, errors.New("block not found")
	}
	if err != nil {
		return Block{}, fmt.Errorf("get block: %w", err)
	}
	return block, nil
}

func (r *BlockRepo) InsertBlock(ctx context.Context, hash string, sizeBytes int64, etag, backend string) error {
	if len(hash) < 4 || sizeBytes < 0 {
		return errors.New("block hash and non-negative size are required")
	}
	now := time.Now().UTC()
	key := fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash)
	return r.session.Query(
		`INSERT INTO blocks (block_hash, size_bytes, ref_count, storage_backend, storage_key, etag, created_at)
		VALUES (?, ?, 1, ?, ?, ?, ?) IF NOT EXISTS`,
		hash, sizeBytes, backend, key, etag, now,
	).WithContext(ctx).Exec()
}

func (r *BlockRepo) IncrementRefCount(ctx context.Context, hash string) error {
	if hash == "" {
		return errors.New("block hash is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		block, err := r.GetBlock(ctx, hash)
		if err != nil {
			return err
		}
		applied, err := r.session.Query(
			`UPDATE blocks SET ref_count = ? WHERE block_hash = ? IF ref_count = ?`, block.RefCount+1, hash, block.RefCount,
		).WithContext(ctx).ScanCAS()
		if err != nil {
			return fmt.Errorf("increment block ref_count: %w", err)
		}
		if applied {
			return nil
		}
	}
	return errors.New("increment block ref_count conflicted too many times")
}

func (r *BlockRepo) DecrementRefCount(ctx context.Context, hash string) (int64, error) {
	if hash == "" {
		return 0, errors.New("block hash is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		block, err := r.GetBlock(ctx, hash)
		if err != nil {
			return 0, err
		}
		if block.RefCount <= 0 {
			return 0, nil
		}
		newCount := block.RefCount - 1
		applied, err := r.session.Query(
			`UPDATE blocks SET ref_count = ? WHERE block_hash = ? IF ref_count = ?`, newCount, hash, block.RefCount,
		).WithContext(ctx).ScanCAS()
		if err != nil {
			return 0, fmt.Errorf("decrement block ref_count: %w", err)
		}
		if applied {
			return newCount, nil
		}
	}
	return 0, errors.New("decrement block ref_count conflicted too many times")
}

func IsBlockNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "block not found")
}
