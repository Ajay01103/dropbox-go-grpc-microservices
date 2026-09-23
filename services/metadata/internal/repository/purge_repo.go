package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

const (
	PurgePending            = "PENDING"
	PurgeDecrementRequested = "DECREMENT_REQUESTED"
	PurgeDecremented        = "DECREMENTED"
	PurgeComplete           = "COMPLETE"
	PurgeFailed             = "FAILED"
)

// legacyStates maps pre-B4 state strings to their modern equivalents so a
// job the pre-deploy backfill missed is handled consistently everywhere
// (Get normalizes on read; the sweeper and the purge-status API route
// through the same function).
//
//   - DECREMENTED_WITH_ERRORS → DECREMENTED: v2 collapses error detail into
//     invalid_ops; the coordinator re-runs metadata removal either way.
//   - METADATA_REMOVED → COMPLETE: the only remaining step was re-marking
//     the state; metadata removal itself is idempotent.
var legacyStates = map[string]string{
	"DECREMENTED_WITH_ERRORS": PurgeDecremented,
	"METADATA_REMOVED":        PurgeComplete,
}

// ParsePurgeState canonicalizes a purge job state string: the two legacy
// pre-B4 states map onto their modern equivalents; known modern states and
// unknown strings pass through unchanged (callers decide how to handle the
// latter — the sweeper logs and skips them).
func ParsePurgeState(state string) string {
	if mapped, ok := legacyStates[state]; ok {
		return mapped
	}
	return state
}

type PurgeJob struct {
	JobID         string
	FileID        string
	FolderID      string
	FileVersion   int32
	OwnerID       string
	BlockHashList []string
	Reason        string
	State         string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   time.Time
	// v2 batch accounting (B2a columns; written by the v2 coordinator, unused
	// by v1 — BatchCount stays 0 for v1 jobs).
	BatchCount  int
	BatchesDone []int
	InvalidOps  []int
}

type PurgeRepo struct {
	session *gocql.Session
}

// purgeActiveBucket formats the hour bucket key used by purge_jobs_active.
func purgeActiveBucket(t time.Time) string {
	return t.UTC().Format("2006010215")
}

// upsertActiveRow maintains the sweeper work-list row for a job. It is a
// dual-write companion to the authoritative purge_jobs row; failure is
// returned, not swallowed, so callers decide (Create/UpdateState propagate;
// the sweeper logs).
func (r *PurgeRepo) upsertActiveRow(ctx context.Context, job PurgeJob) error {
	id, err := parseID(job.JobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	return r.session.Query(
		`INSERT INTO purge_jobs_active (bucket, job_id, state, next_attempt_at) VALUES (?, ?, ?, ?)`,
		purgeActiveBucket(job.CreatedAt), id, job.State, job.NextAttemptAt,
	).WithContext(ctx).Exec()
}

// deleteActiveRow removes the sweeper work-list row when a job reaches
// COMPLETE. FAILED rows are deliberately kept (30d TTL) for alerting.
func (r *PurgeRepo) deleteActiveRow(ctx context.Context, job PurgeJob) error {
	id, err := parseID(job.JobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	return r.session.Query(
		`DELETE FROM purge_jobs_active WHERE bucket = ? AND job_id = ?`,
		purgeActiveBucket(job.CreatedAt), id,
	).WithContext(ctx).Exec()
}

func NewPurgeRepo(session *gocql.Session) *PurgeRepo {
	return &PurgeRepo{session: session}
}

func (r *PurgeRepo) Get(ctx context.Context, jobID string) (PurgeJob, error) {
	id, err := parseID(jobID)
	if err != nil {
		return PurgeJob{}, fmt.Errorf("parse purge job id: %w", err)
	}
	var job PurgeJob
	var fileID, folderID, ownerID gocql.UUID
	var completedAt time.Time
	var batchCount *int // null for v1-era jobs (column added in B2a)
	err = r.session.Query(`SELECT job_id, file_id, folder_id, file_version, owner_id, block_hash_list, reason, state, attempts, next_attempt_at, last_error, created_at, updated_at, completed_at, batch_count, batches_done, invalid_ops FROM purge_jobs WHERE job_id = ?`, id).
		WithContext(ctx).Scan(&id, &fileID, &folderID, &job.FileVersion, &ownerID, &job.BlockHashList, &job.Reason, &job.State, &job.Attempts, &job.NextAttemptAt, &job.LastError, &job.CreatedAt, &job.UpdatedAt, &completedAt, &batchCount, &job.BatchesDone, &job.InvalidOps)
	if err == gocql.ErrNotFound {
		return PurgeJob{}, errors.New("purge job not found")
	}
	if err != nil {
		return PurgeJob{}, fmt.Errorf("get purge job: %w", err)
	}
	job.JobID = id.String()
	job.FileID = fileID.String()
	job.FolderID = folderID.String()
	job.OwnerID = ownerID.String()
	job.CompletedAt = completedAt
	// Normalize legacy pre-B4 states on read: every reader (sweeper,
	// status API, coordinator) sees the canonical state.
	job.State = ParsePurgeState(job.State)
	if batchCount != nil {
		job.BatchCount = *batchCount
	}
	return job, nil
}

func (r *PurgeRepo) Create(ctx context.Context, file File, reason string) (PurgeJob, bool, error) {
	jobID := gocql.UUID(uuid.NewSHA1(uuid.Nil, fmt.Appendf(nil, "%s:%d", file.FileID, file.Version)))
	now := time.Now().UTC()
	job := PurgeJob{
		JobID: jobID.String(), FileID: file.FileID, FolderID: file.FolderID,
		FileVersion: file.Version, OwnerID: file.OwnerID, BlockHashList: file.BlockHashList,
		Reason: reason, State: PurgePending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
	fileID, err := parseID(file.FileID)
	if err != nil {
		return PurgeJob{}, false, err
	}
	folderID, err := parseID(file.FolderID)
	if err != nil {
		return PurgeJob{}, false, err
	}
	ownerID, err := parseID(file.OwnerID)
	if err != nil {
		return PurgeJob{}, false, err
	}
	applied, err := r.session.Query(`INSERT INTO purge_jobs (job_id, file_id, folder_id, file_version, owner_id, block_hash_list, reason, state, attempts, next_attempt_at, last_error, created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`, jobID, fileID, folderID, file.Version, ownerID, file.BlockHashList, reason, PurgePending, 0, now, "", now, now, nil).
		WithContext(ctx).MapScanCAS(make(map[string]interface{}))
	if err != nil {
		return PurgeJob{}, false, fmt.Errorf("create purge job: %w", err)
	}
	if !applied {
		// The primary row is authoritative; return the existing job.
		existing, getErr := r.Get(ctx, jobID.String())
		if getErr != nil {
			return PurgeJob{}, false, getErr
		}
		return existing, false, nil
	}
	if err := r.upsertActiveRow(ctx, job); err != nil {
		return PurgeJob{}, false, err
	}
	return job, true, nil
}

func (r *PurgeRepo) UpdateState(ctx context.Context, job PurgeJob, state string, lastError string) error {
	now := time.Now().UTC()
	completedAt := job.CompletedAt
	if state == PurgeComplete {
		completedAt = now
	}
	jobID, err := parseID(job.JobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	if err := r.session.Query(`UPDATE purge_jobs SET state = ?, last_error = ?, updated_at = ?, completed_at = ? WHERE job_id = ?`, state, lastError, now, completedAt, jobID).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update purge job state: %w", err)
	}
	job.State, job.LastError, job.UpdatedAt, job.CompletedAt = state, lastError, now, completedAt
	// Sweeper work-list dual-write. Terminal rows leave the work list:
	// COMPLETE is deleted outright; FAILED is upserted so it stays visible
	// for alerting until its TTL expires.
	if state == PurgeComplete {
		return r.deleteActiveRow(ctx, job)
	}
	return r.upsertActiveRow(ctx, job)
}

// ActiveJobRef is a sweeper work-list entry from purge_jobs_active.
type ActiveJobRef struct {
	Bucket        string
	JobID         string
	State         string
	NextAttemptAt time.Time
}

// ListDueActive scans the recent buckets of purge_jobs_active and returns
// entries whose next_attempt_at is due. Filtering is done in Go (no ALLOW
// FILTERING). The caller determines the bucket window; see purge.DueBuckets
// for the sizing rule derived from the retry config.
func (r *PurgeRepo) ListDueActive(ctx context.Context, buckets []string, now time.Time) ([]ActiveJobRef, error) {
	var out []ActiveJobRef
	for _, bucket := range buckets {
		iter := r.session.Query(
			`SELECT job_id, state, next_attempt_at FROM purge_jobs_active WHERE bucket = ?`, bucket,
		).WithContext(ctx).Iter()
		var jobID gocql.UUID
		var state string
		var nextAttempt time.Time
		for iter.Scan(&jobID, &state, &nextAttempt) {
			if !nextAttempt.After(now) {
				out = append(out, ActiveJobRef{
					Bucket: bucket, JobID: jobID.String(),
					State: state, NextAttemptAt: nextAttempt,
				})
			}
		}
		if err := iter.Close(); err != nil && err != gocql.ErrNotFound {
			return nil, fmt.Errorf("scan purge_jobs_active (%s): %w", bucket, err)
		}
	}
	return out, nil
}

// ClaimJob atomically advances a job's retry bookkeeping. It applies only if
// the row still has expectedAttempts attempts, which makes concurrent
// sweepers safe: exactly one claim wins, the loser sees applied=false.
// The caller then Get()s the (post-claim) row and dispatches.
func (r *PurgeRepo) ClaimJob(ctx context.Context, jobID string, expectedAttempts int, nextAttemptAt time.Time) (bool, error) {
	id, err := parseID(jobID)
	if err != nil {
		return false, fmt.Errorf("parse purge job id: %w", err)
	}
	now := time.Now().UTC()
	casMap := make(map[string]interface{})
	applied, err := r.session.Query(
		`UPDATE purge_jobs SET attempts = ?, next_attempt_at = ?, updated_at = ? WHERE job_id = ? IF attempts = ?`,
		expectedAttempts+1, nextAttemptAt, now, id, expectedAttempts,
	).WithContext(ctx).MapScanCAS(casMap)
	if err != nil {
		return false, fmt.Errorf("claim purge job: %w", err)
	}
	return applied, nil
}

// SetBatchCount records the number of v2 decrement batches a job was split
// into. Called by the v2 coordinator before publishing the first batch, so a
// crash mid-publish leaves batch_count set and the sweeper can re-drive the
// missing batches.
func (r *PurgeRepo) SetBatchCount(ctx context.Context, jobID string, n int) error {
	id, err := parseID(jobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	if err := r.session.Query(`UPDATE purge_jobs SET batch_count = ? WHERE job_id = ?`, n, id).
		WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("set purge job batch_count: %w", err)
	}
	return nil
}

// RecordBatch records one completed v2 decrement batch: unions batchIndex into
// batches_done and the invalid occurrences into invalid_ops (CQL set adds are
// commutative, so concurrent duplicate completions converge without a CAS),
// then reports whether every batch has landed. A false-but-allDone race is
// impossible in the dangerous direction: a caller that misses allDone loses
// only until the sweeper re-drives; a caller that sees allDone spuriously
// cannot — membership was read in the same statement batch as the update.
func (r *PurgeRepo) RecordBatch(ctx context.Context, jobID string, batchIndex int, invalid []int) (bool, error) {
	id, err := parseID(jobID)
	if err != nil {
		return false, fmt.Errorf("parse purge job id: %w", err)
	}
	if invalid == nil {
		invalid = []int{}
	}
	if err := r.session.Query(
		`UPDATE purge_jobs SET batches_done = batches_done + ?, invalid_ops = invalid_ops + ? WHERE job_id = ?`,
		[]int{batchIndex}, invalid, id,
	).WithContext(ctx).Exec(); err != nil {
		return false, fmt.Errorf("record purge batch: %w", err)
	}

	// Re-read the accounting columns; the row may legitimately be gone if the
	// job TTL'd out between the update and this read.
	var batchCount *int
	var batchesDone []int
	err = r.session.Query(
		`SELECT batch_count, batches_done FROM purge_jobs WHERE job_id = ?`, id,
	).WithContext(ctx).Scan(&batchCount, &batchesDone)
	if err == gocql.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("re-read purge batch accounting: %w", err)
	}
	if batchCount == nil || *batchCount <= 0 {
		return false, nil // v1-shaped job; never completes via batch accounting
	}
	return len(batchesDone) >= *batchCount, nil
}

// RecordError stores the most recent dispatch error on the job row. Best-
// effort by design: the claim already advanced attempts/next_attempt_at, so
// the job retries with backoff whether or not this write lands.
func (r *PurgeRepo) RecordError(ctx context.Context, jobID string, lastError string) error {
	id, err := parseID(jobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	return r.session.Query(
		`UPDATE purge_jobs SET last_error = ?, updated_at = ? WHERE job_id = ?`,
		lastError, time.Now().UTC(), id,
	).WithContext(ctx).Exec()
}

func (r *PurgeRepo) MarkAttempt(ctx context.Context, jobID string, attempt int, next time.Time, lastError string) error {
	id, err := parseID(jobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	return r.session.Query(`UPDATE purge_jobs SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE job_id = ?`, attempt, next, lastError, time.Now().UTC(), id).WithContext(ctx).Exec()
}
