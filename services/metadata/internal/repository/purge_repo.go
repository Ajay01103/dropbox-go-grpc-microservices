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
	id, err := uuid.Parse(jobID)
	if err != nil {
		return PurgeJob{}, fmt.Errorf("parse purge job id: %w", err)
	}
	var job PurgeJob
	var fileID, folderID, ownerID uuid.UUID
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
	file, err := uuid.Parse(fileID)
	if err != nil {
		return PurgeJob{}, fmt.Errorf("parse file id: %w", err)
	}
	var jobID uuid.UUID
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
	jobID := uuid.NewSHA1(uuid.Nil, []byte(fmt.Sprintf("%s:%d", file.FileID, file.Version)))
	now := time.Now().UTC()
	job := PurgeJob{
		JobID: jobID.String(), FileID: file.FileID, FolderID: file.FolderID,
		FileVersion: file.Version, OwnerID: file.OwnerID, BlockHashList: file.BlockHashList,
		Reason: reason, State: PurgePending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
	fileID, err := uuid.Parse(file.FileID)
	if err != nil {
		return PurgeJob{}, false, err
	}
	folderID, err := uuid.Parse(file.FolderID)
	if err != nil {
		return PurgeJob{}, false, err
	}
	ownerID, err := uuid.Parse(file.OwnerID)
	if err != nil {
		return PurgeJob{}, false, err
	}
	applied, err := r.session.Query(`INSERT INTO purge_jobs (job_id, file_id, folder_id, file_version, owner_id, block_hash_list, reason, state, has_reconciliation_errors, attempts, next_attempt_at, last_error, created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`, jobID, fileID, folderID, file.Version, ownerID, file.BlockHashList, reason, PurgePending, false, 0, now, "", now, now, nil).
		WithContext(ctx).ScanCAS()
	if err != nil {
		return PurgeJob{}, false, fmt.Errorf("create purge job: %w", err)
	}
	if !applied {
		existing, findErr := r.FindByFileVersion(ctx, file.FileID, file.Version)
		return existing, false, findErr
	}
	if err := r.session.Query(`INSERT INTO purge_jobs_by_file_version (file_id, file_version, job_id) VALUES (?, ?, ?)`, fileID, file.Version, jobID).WithContext(ctx).Exec(); err != nil {
		return PurgeJob{}, false, fmt.Errorf("index purge job: %w", err)
	}
	if err := r.indexState(ctx, job); err != nil {
		return PurgeJob{}, false, err
	}
	return job, true, nil
}

func (r *PurgeRepo) UpdateState(ctx context.Context, job PurgeJob, state string, hasErrors bool, lastError string) error {
	now := time.Now().UTC()
	completedAt := job.CompletedAt
	if state == PurgeComplete {
		completedAt = now
	}
	if err := r.session.Query(`UPDATE purge_jobs SET state = ?, has_reconciliation_errors = ?, last_error = ?, updated_at = ?, completed_at = ? WHERE job_id = ?`, state, hasErrors, lastError, now, completedAt, uuid.MustParse(job.JobID)).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update purge job state: %w", err)
	}
	job.State, job.HasReconciliationErrors, job.LastError, job.UpdatedAt, job.CompletedAt = state, hasErrors, lastError, now, completedAt
	return r.indexState(ctx, job)
}

func (r *PurgeRepo) MarkAttempt(ctx context.Context, jobID string, attempt int, next time.Time, lastError string) error {
	return r.session.Query(`UPDATE purge_jobs SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE job_id = ?`, attempt, next, lastError, time.Now().UTC(), uuid.MustParse(jobID)).WithContext(ctx).Exec()
}

func (r *PurgeRepo) indexState(ctx context.Context, job PurgeJob) error {
	return r.session.Query(`INSERT INTO purge_jobs_by_state (state, updated_at, job_id) VALUES (?, ?, ?)`, job.State, job.UpdatedAt, uuid.MustParse(job.JobID)).WithContext(ctx).Exec()
}
