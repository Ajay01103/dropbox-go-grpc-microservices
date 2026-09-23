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
func (rq *realQuery) Scan(dest ...interface{}) error            { return rq.q.Scan(dest...) }
func (rq *realQuery) ScanCAS(dest ...interface{}) (bool, error) { return rq.q.ScanCAS(dest...) }
func (rq *realQuery) MapScanCAS(dest map[string]interface{}) (bool, error) {
	return rq.q.MapScanCAS(dest)
}
func (rq *realQuery) Exec() error       { return rq.q.Exec() }
func (rq *realQuery) Iter() *gocql.Iter { return rq.q.Iter() }

type Block struct {
	Hash           string
	SizeBytes      int64
	RefCount       int64
	StorageBackend string
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
		`SELECT block_hash, size_bytes, ref_count, storage_backend, etag, created_at
		FROM blocks WHERE block_hash = ? LIMIT 1`, hash,
	).WithContext(ctx).Scan(
		&block.Hash, &block.SizeBytes, &block.RefCount, &block.StorageBackend,
		&block.ETag, &block.CreatedAt,
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
	return r.session.Query(
		`INSERT INTO blocks (block_hash, size_bytes, ref_count, storage_backend, etag, created_at)
		VALUES (?, ?, 1, ?, ?, ?) IF NOT EXISTS`,
		hash, sizeBytes, backend, etag, now,
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
		//
		// State guard: existing rows have state = null, which we treat as ACTIVE
		// (no keyless backfill exists). A block in state='DELETING' is being GC'd
		// and must not accept new references — the dedup upload backs off and
		// surfaces a retryable error.
		casMap := make(map[string]interface{})
		applied, err := r.session.Query(
			`UPDATE blocks SET ref_count = ? WHERE block_hash = ? IF ref_count = ? AND state = null`,
			block.RefCount+1, hash, block.RefCount,
		).WithContext(ctx).MapScanCAS(casMap)
		if err != nil {
			return fmt.Errorf("increment block ref_count: %w", err)
		}
		if applied {
			return nil
		}
		if state, ok := casMap["state"].(string); ok && state == BlockStateDeleting {
			return &ErrBlockDeleting{Hash: hash}
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

// Block states for the GC fence. We deliberately never write 'ACTIVE':
// existing rows have state = null and a CQL keyless backfill is impossible,
// so null is treated as ACTIVE everywhere and 'DELETING' is the only written
// state. A fence fails closed unless someone re-referenced the block.
const (
	// BlockStateDeleting marks a block fenced for physical deletion.
	BlockStateDeleting = "DELETING"
)

// ErrBlockDeleting is returned by IncrementRefCount when a dedup upload hits a
// block that is currently fenced for deletion. Callers (the upload service)
// should translate this into a short client retry: GC's fence will fail in
// the meantime because the block is re-referenced, or the row is gone.
type ErrBlockDeleting struct{ Hash string }

func (e *ErrBlockDeleting) Error() string {
	return fmt.Sprintf("block %s is being deleted; retry shortly", e.Hash)
}

// IsBlockDeleting reports whether err is (or wraps) an ErrBlockDeleting.
func IsBlockDeleting(err error) bool {
	var e *ErrBlockDeleting
	return errors.As(err, &e)
}

// FenceForDelete atomically marks a zero-ref block as being deleted. It fails
// (applied=false) if the block was re-referenced (ref_count != 0) or is
// already fenced/re-referenced — the GC worker then re-checks and skips.
//
// Null-safe: the condition is `state = null`, matching every pre-B2a row and
// everything InsertBlock writes (which never sets state). A fenced row has
// state='DELETING', so it can never be fenced twice.
func (r *BlockRepo) FenceForDelete(ctx context.Context, hash string) (bool, error) {
	if hash == "" {
		return false, errors.New("block hash is required")
	}
	casMap := make(map[string]interface{})
	applied, err := r.session.Query(
		`UPDATE blocks SET state = ? WHERE block_hash = ? IF ref_count = 0 AND state = null`,
		BlockStateDeleting, hash,
	).WithContext(ctx).MapScanCAS(casMap)
	if err != nil {
		return false, fmt.Errorf("fence block for delete: %w", err)
	}
	return applied, nil
}

func IsBlockNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "block not found")
}
