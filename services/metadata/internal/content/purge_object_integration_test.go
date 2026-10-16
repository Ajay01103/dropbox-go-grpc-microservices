//go:build integration

// End-to-end check of what a permanent file delete does to object storage and
// metadata, against the real dev infrastructure (ScyllaDB + RustFS + NATS):
//
//	go test -tags integration ./internal/content/...
//
// It covers the whole post-decrement step of the purge saga
// (Coordinator.RemoveMetadataAndComplete): the consolidated (Tier B) object is
// removed from the bucket, the file's metadata projections are removed, and a
// failure to delete the object leaves the metadata in place so the retry can
// still find it.
//
// The consolidated object is the one artifact nothing else reaps. The block GC
// only cleans blocks/, the registry row is deleted by this same step, and the
// key is derived from the file id — so before this, every purged file left its
// fully assembled copy behind under consolidated/.
package content

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/purge"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// failingConsolidatedDeleter stands in for an object store that refuses the
// delete, which is how the ordering invariant gets tested without breaking
// RustFS.
type failingConsolidatedDeleter struct{}

func (failingConsolidatedDeleter) DeleteConsolidated(context.Context, string) error {
	return errors.New("object store refused the delete")
}

func TestIntegrationPermanentDeleteRemovesConsolidatedObject(t *testing.T) {
	ctx, repo, session, client, cfg := setupIntegration(t)
	storage, err := NewS3Storage(cfg)
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	ownerID := uuid.NewString()
	folderID := uuid.NewString()
	file, err := repo.CreateFileWithBlockSizes(ctx, folderID, ownerID, "clip.mp4", "video/mp4",
		"it-consolidated-hash", []string{"it-consolidated-block"}, []int64{8 << 20}, 8<<20, 0)
	if err != nil {
		t.Fatalf("CreateFileWithBlockSizes: %v", err)
	}
	if _, err := repo.SetFileDeleted(ctx, ownerID, file.FileID, uuid.NewString(), true); err != nil {
		t.Fatalf("SetFileDeleted: %v", err)
	}
	file, err = repo.GetFileByID(ctx, ownerID, file.FileID)
	if err != nil {
		t.Fatalf("GetFileByID: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, getErr := repo.GetFileByID(cleanupCtx, ownerID, file.FileID); getErr == nil {
			_ = repo.RemoveFileProjections(cleanupCtx, file)
		}
		_, _ = client.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{
			Bucket: aws.String(cfg.Bucket), Key: aws.String(consolidatedKey(file.FileID)),
		})
	})

	// Seed the assembled object the stitcher would have produced.
	objectKey := consolidatedKey(file.FileID)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(cfg.Bucket), Key: aws.String(objectKey),
		Body: bytes.NewReader(bytes.Repeat([]byte("c"), 1<<20)), ContentLength: aws.Int64(1 << 20),
	}); err != nil {
		t.Skipf("RustFS unavailable (put consolidated object): %v", err)
	}
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(cfg.Bucket), Key: aws.String(objectKey),
	}); err != nil {
		t.Fatalf("seed object not readable: %v", err)
	}

	purgeRepo := repository.NewPurgeRepo(session)
	job := repository.PurgeJob{
		JobID: uuid.NewString(), FileID: file.FileID, FolderID: folderID,
		OwnerID: ownerID, BlockHashList: file.BlockHashList,
		State: repository.PurgeDecremented, Reason: "permanent_delete",
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_ = session.Query(`DELETE FROM purge_jobs WHERE job_id = ?`,
			mustParseUUID(t, job.JobID)).WithContext(cleanupCtx).Exec()
	})

	coordinator, err := purge.NewCoordinator("nats://127.0.0.1:4222", purgeRepo, repo, zap.NewNop())
	if err != nil {
		t.Skipf("NATS unavailable: %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	coordinator.SetConsolidatedDeleter(storage)

	// Failure first: an object that cannot be deleted must abort the step
	// BEFORE the metadata goes, so the retry still has the row that names the
	// object. The job stays recoverable instead of reporting a purge that
	// leaked the file's bytes.
	coordinator.SetConsolidatedDeleter(failingConsolidatedDeleter{})
	if err := coordinator.RemoveMetadataAndComplete(ctx, job); err == nil {
		t.Fatal("purge completed even though the consolidated object could not be deleted")
	}
	if _, err := repo.GetFileByID(ctx, ownerID, file.FileID); err != nil {
		t.Fatalf("metadata was removed despite the object delete failing: %v", err)
	}

	// Now the real path.
	coordinator.SetConsolidatedDeleter(storage)
	if err := coordinator.RemoveMetadataAndComplete(ctx, job); err != nil {
		t.Fatalf("RemoveMetadataAndComplete: %v", err)
	}

	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(cfg.Bucket), Key: aws.String(objectKey),
	}); err == nil {
		t.Errorf("consolidated object %s still present after a permanent delete", objectKey)
	}

	if _, err := repo.GetFileByID(ctx, ownerID, file.FileID); err == nil {
		t.Error("files_by_id row still resolves after a permanent delete")
	}
	stitchRepo := repository.NewStitchRepo(session)
	if key, err := stitchRepo.GetStitchedObjectKey(ctx, file.FileID); err != nil || key != "" {
		t.Errorf("stitch registry row still present (key=%q, err=%v)", key, err)
	}
}

// mustParseUUID parses a uuid string with the same failure mode the repository
// uses for its ids (gocql.UUID, not google/uuid).
func mustParseUUID(t *testing.T, value string) gocql.UUID {
	t.Helper()
	parsed, err := gocql.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return parsed
}
