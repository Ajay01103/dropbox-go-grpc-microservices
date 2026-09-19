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
	PurgePending              = "PENDING"
	PurgeDecrementRequested   = "DECREMENT_REQUESTED"
	PurgeDecremented          = "DECREMENTED"
	PurgeDecrementedWithError = "DECREMENTED_WITH_ERRORS"
	PurgeMetadataRemoved      = "METADATA_REMOVED"
	PurgeComplete             = "COMPLETE"
	PurgeFailed               = "FAILED"
)

type PurgeJob struct {
	JobID                   string
	FileID                  string
	FolderID                string
	FileVersion             int32
	OwnerID                 string
	BlockHashList           []string
	Reason                  string
	State                   string
	HasReconciliationErrors bool
	Attempts                int
	NextAttemptAt           time.Time
	LastError               string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	CompletedAt             time.Time
}

type PurgeRepo struct {
	session *gocql.Session
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
	err = r.session.Query(`SELECT job_id, file_id, folder_id, file_version, owner_id, block_hash_list, reason, state, has_reconciliation_errors, attempts, next_attempt_at, last_error, created_at, updated_at, completed_at FROM purge_jobs WHERE job_id = ?`, id).
		WithContext(ctx).Scan(&id, &fileID, &folderID, &job.FileVersion, &ownerID, &job.BlockHashList, &job.Reason, &job.State, &job.HasReconciliationErrors, &job.Attempts, &job.NextAttemptAt, &job.LastError, &job.CreatedAt, &job.UpdatedAt, &completedAt)
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
	return job, nil
}

func (r *PurgeRepo) FindByFileVersion(ctx context.Context, fileID string, version int32) (PurgeJob, error) {
	file, err := parseID(fileID)
	if err != nil {
		return PurgeJob{}, fmt.Errorf("parse file id: %w", err)
	}
	var jobID gocql.UUID
	err = r.session.Query(`SELECT job_id FROM purge_jobs_by_file_version WHERE file_id = ? AND file_version = ?`, file, version).
		WithContext(ctx).Scan(&jobID)
	if err == gocql.ErrNotFound {
		return PurgeJob{}, errors.New("purge job not found")
	}
	if err != nil {
		return PurgeJob{}, fmt.Errorf("find purge job: %w", err)
	}
	return r.Get(ctx, jobID.String())
}

func (r *PurgeRepo) Create(ctx context.Context, file File, reason string) (PurgeJob, bool, error) {
	jobID := gocql.UUID(uuid.NewSHA1(uuid.Nil, []byte(fmt.Sprintf("%s:%d", file.FileID, file.Version))))
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
	applied, err := r.session.Query(`INSERT INTO purge_jobs (job_id, file_id, folder_id, file_version, owner_id, block_hash_list, reason, state, has_reconciliation_errors, attempts, next_attempt_at, last_error, created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`, jobID, fileID, folderID, file.Version, ownerID, file.BlockHashList, reason, PurgePending, false, 0, now, "", now, now, nil).
		WithContext(ctx).MapScanCAS(make(map[string]interface{}))
	if err != nil {
		return PurgeJob{}, false, fmt.Errorf("create purge job: %w", err)
	}
	if !applied {
		// The primary row is authoritative. The secondary lookup index may be
		// missing if a previous request crashed between these two writes.
		existing, getErr := r.Get(ctx, jobID.String())
		if getErr != nil {
			return PurgeJob{}, false, getErr
		}
		if err := r.indexFileVersion(ctx, fileID, file.Version, jobID); err != nil {
			return PurgeJob{}, false, err
		}
		return existing, false, nil
	}
	if err := r.indexFileVersion(ctx, fileID, file.Version, jobID); err != nil {
		return PurgeJob{}, false, err
	}
	if err := r.indexState(ctx, job); err != nil {
		return PurgeJob{}, false, err
	}
	return job, true, nil
}

func (r *PurgeRepo) indexFileVersion(ctx context.Context, fileID gocql.UUID, version int32, jobID gocql.UUID) error {
	if err := r.session.Query(`INSERT INTO purge_jobs_by_file_version (file_id, file_version, job_id) VALUES (?, ?, ?)`, fileID, version, jobID).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("index purge job: %w", err)
	}
	return nil
}

func (r *PurgeRepo) UpdateState(ctx context.Context, job PurgeJob, state string, hasErrors bool, lastError string) error {
	now := time.Now().UTC()
	completedAt := job.CompletedAt
	if state == PurgeComplete {
		completedAt = now
	}
	jobID, err := parseID(job.JobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	if err := r.session.Query(`UPDATE purge_jobs SET state = ?, has_reconciliation_errors = ?, last_error = ?, updated_at = ?, completed_at = ? WHERE job_id = ?`, state, hasErrors, lastError, now, completedAt, jobID).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update purge job state: %w", err)
	}
	job.State, job.HasReconciliationErrors, job.LastError, job.UpdatedAt, job.CompletedAt = state, hasErrors, lastError, now, completedAt
	return r.indexState(ctx, job)
}

func (r *PurgeRepo) MarkAttempt(ctx context.Context, jobID string, attempt int, next time.Time, lastError string) error {
	id, err := parseID(jobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	return r.session.Query(`UPDATE purge_jobs SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE job_id = ?`, attempt, next, lastError, time.Now().UTC(), id).WithContext(ctx).Exec()
}

func (r *PurgeRepo) indexState(ctx context.Context, job PurgeJob) error {
	id, err := parseID(job.JobID)
	if err != nil {
		return fmt.Errorf("parse purge job id: %w", err)
	}
	return r.session.Query(`INSERT INTO purge_jobs_by_state (state, updated_at, job_id) VALUES (?, ?, ?)`, job.State, job.UpdatedAt, id).WithContext(ctx).Exec()
}

// DeleteCompletedJob removes all rows for a finished purge job from every
// table it was written to. Call this only after the job reaches COMPLETE.
//
// Tables cleaned up:
//   - purge_jobs                  (main record, keyed by job_id)
//   - purge_jobs_by_file_version  (lookup index, keyed by file_id + file_version)
//   - purge_jobs_by_state         (all historical state rows for this job)
//
// The operation is idempotent — absent rows are silently ignored.
func (r *PurgeRepo) DeleteCompletedJob(ctx context.Context, job PurgeJob) error {
	jobID, err := parseID(job.JobID)
	if err != nil {
		return fmt.Errorf("parse purge job id for cleanup: %w", err)
	}
	fileID, err := parseID(job.FileID)
	if err != nil {
		return fmt.Errorf("parse file id for cleanup: %w", err)
	}

	// 1. Delete the main purge_jobs row.
	if err := r.session.Query(
		`DELETE FROM purge_jobs WHERE job_id = ?`, jobID,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete purge_jobs row: %w", err)
	}

	// 2. Delete the file-version lookup index.
	if err := r.session.Query(
		`DELETE FROM purge_jobs_by_file_version WHERE file_id = ? AND file_version = ?`,
		fileID, job.FileVersion,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete purge_jobs_by_file_version row: %w", err)
	}

	// 3. Collect and delete every state-transition row for this job.
	//    purge_jobs_by_state PRIMARY KEY: ((state), updated_at, job_id)
	//    We cannot filter by job_id without ALLOW FILTERING since it is not the
	//    partition key, so we iterate the known states instead.
	knownStates := []string{
		PurgePending,
		PurgeDecrementRequested,
		PurgeDecremented,
		PurgeDecrementedWithError,
		PurgeMetadataRemoved,
		PurgeComplete,
		PurgeFailed,
	}
	for _, state := range knownStates {
		// Scan for rows with this state and our job_id.
		// updated_at is a timestamp column — gocql requires a concrete time.Time
		// target; scanning into interface{} causes "can not unmarshal timestamp".
		iter := r.session.Query(
			`SELECT updated_at FROM purge_jobs_by_state WHERE state = ? AND job_id = ? ALLOW FILTERING`,
			state, jobID,
		).WithContext(ctx).Iter()
		var timestamps []time.Time
		var ts time.Time
		for iter.Scan(&ts) {
			timestamps = append(timestamps, ts)
		}
		if err := iter.Close(); err != nil {
			return fmt.Errorf("scan purge_jobs_by_state (%s): %w", state, err)
		}
		for _, updatedAt := range timestamps {
			if err := r.session.Query(
				`DELETE FROM purge_jobs_by_state WHERE state = ? AND updated_at = ? AND job_id = ?`,
				state, updatedAt, jobID,
			).WithContext(ctx).Exec(); err != nil {
				return fmt.Errorf("delete purge_jobs_by_state row (%s): %w", state, err)
			}
		}
	}
	return nil
}
