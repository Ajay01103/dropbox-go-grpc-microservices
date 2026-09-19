package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

// gocqlQuery abstracts *gocql.Query for testability.
type gocqlQuery interface {
	WithContext(ctx context.Context) gocqlQuery
	Scan(dest ...interface{}) error
	ScanCAS(dest ...interface{}) (bool, error)
	MapScanCAS(dest map[string]interface{}) (bool, error)
	Exec() error
	Iter() *gocql.Iter
}

// gocqlSession abstracts *gocql.Session for testability.
type gocqlSession interface {
	Query(stmt string, values ...interface{}) gocqlQuery
}

// realSession wraps *gocql.Session so that its Query method satisfies gocqlSession.
type realSession struct{ s *gocql.Session }

func (r *realSession) Query(stmt string, values ...interface{}) gocqlQuery {
	return &realQuery{q: r.s.Query(stmt, values...)}
}

// realQuery wraps *gocql.Query so that it satisfies gocqlQuery.
type realQuery struct{ q *gocql.Query }

func (rq *realQuery) WithContext(ctx context.Context) gocqlQuery {
	return &realQuery{q: rq.q.WithContext(ctx)}
}
func (rq *realQuery) Scan(dest ...interface{}) error              { return rq.q.Scan(dest...) }
func (rq *realQuery) ScanCAS(dest ...interface{}) (bool, error)   { return rq.q.ScanCAS(dest...) }
func (rq *realQuery) MapScanCAS(dest map[string]interface{}) (bool, error) {
	return rq.q.MapScanCAS(dest)
}
func (rq *realQuery) Exec() error          { return rq.q.Exec() }
func (rq *realQuery) Iter() *gocql.Iter   { return rq.q.Iter() }

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
	session gocqlSession
}

func NewBlockRepo(session *gocql.Session) *BlockRepo {
	return &BlockRepo{session: &realSession{s: session}}
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
		// Use MapScanCAS: when the IF condition fails ScyllaDB returns 2 columns
		// ([applied] + ref_count), which ScanCAS() can't handle without dest args.
		casMap := make(map[string]interface{})
		applied, err := r.session.Query(
			`UPDATE blocks SET ref_count = ? WHERE block_hash = ? IF ref_count = ?`,
			block.RefCount+1, hash, block.RefCount,
		).WithContext(ctx).MapScanCAS(casMap)
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
		// Use MapScanCAS: when the IF condition fails ScyllaDB returns 2 columns
		// ([applied] + ref_count), which ScanCAS() can't handle without dest args.
		casMap := make(map[string]interface{})
		applied, err := r.session.Query(
			`UPDATE blocks SET ref_count = ? WHERE block_hash = ? IF ref_count = ?`,
			newCount, hash, block.RefCount,
		).WithContext(ctx).MapScanCAS(casMap)
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
