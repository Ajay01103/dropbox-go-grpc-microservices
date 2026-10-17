//go:build integration

// End-to-end verification of the Tier B parking path against the real dev
// infrastructure (ScyllaDB + RustFS):
//
//	go test -tags integration ./internal/content/...
//
// A grown legacy file is the motivating shape: because a file that was written
// as a single block records its own size as its block size, one edit makes it
// two blocks whose first is below S3's 5 MiB multipart minimum. That layout can
// never be assembled server-side, so the worker must park it once (state
// 'skipped', no consolidated object, absent from the sweep work list) rather
// than warn and retry it on every save.
package content

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

func TestIntegrationStitchWorkerParksIneligibleLayout(t *testing.T) {
	ctx, repo, session, client, cfg := setupIntegration(t)

	stitcher, err := NewStitcher(cfg)
	if err != nil {
		t.Fatalf("NewStitcher: %v", err)
	}

	// Two sub-minimum blocks: the shape a legacy single-block file takes after
	// one growing edit. 200 KiB + 40 KiB, both under the 5 MiB part minimum.
	head := make([]byte, 200<<10)
	tail := make([]byte, 40<<10)
	for i := range head {
		head[i] = 'h'
	}
	for i := range tail {
		tail[i] = 't'
	}
	headHash, headBlock := putBlock(t, ctx, client, cfg.Bucket, head)
	tailHash, tailBlock := putBlock(t, ctx, client, cfg.Bucket, tail)
	cleanupBlocks := func() {
		for _, hash := range []string{headHash, tailHash} {
			key, keyErr := blockObjectKey(hash)
			if keyErr != nil {
				continue
			}
			_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
				Bucket: aws.String(cfg.Bucket), Key: aws.String(key),
			})
		}
	}
	t.Cleanup(cleanupBlocks)

	fileID := uuid.NewString()
	manifest := repository.FileManifestRow{
		FileID: fileID, TotalSize: int64(len(head) + len(tail)),
		ContentType: "application/pdf", ContentHash: "parks-without-consolidating",
		Blocks: []repository.ManifestBlock{
			{Index: 0, Hash: headBlock.Hash, SizeBytes: headBlock.SizeBytes},
			{Index: 1, Hash: tailBlock.Hash, SizeBytes: tailBlock.SizeBytes},
		},
	}
	if err := repo.WriteFileManifest(ctx, manifest); err != nil {
		t.Fatalf("WriteFileManifest: %v", err)
	}
	stitchRepo := repository.NewStitchRepo(session)
	t.Cleanup(func() {
		_ = repo.DeleteFileManifest(context.Background(), fileID)
		_ = stitchRepo.Delete(context.Background(), fileID)
		_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(cfg.Bucket), Key: aws.String(consolidatedKey(fileID)),
		})
	})
	if err := stitchRepo.Enqueue(ctx, fileID, time.Now().UTC()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	worker := NewStitchWorker(stitcher, stitchRepo, repo, StitcherConfig{
		Enabled: true, BatchSize: 10, MinAge: 0,
		StaleClaimAfter: 10 * time.Minute, MaxAttempts: 5,
	}, zap.NewNop())
	if err := worker.StitchFile(ctx, fileID); err != nil {
		t.Fatalf("StitchFile on an ineligible layout = %v, want a quiet park", err)
	}

	fileUUID, err := gocql.ParseUUID(fileID)
	if err != nil {
		t.Fatalf("parse file id: %v", err)
	}
	var state string
	if err := session.Query(
		`SELECT state FROM stitched_objects WHERE file_id = ?`, fileUUID,
	).WithContext(ctx).Scan(&state); err != nil {
		t.Fatalf("read stitch state: %v", err)
	}
	if state != string(repository.StitchStateSkipped) {
		t.Fatalf("stitch state = %s, want %s (parked, not failed/pending)", state, repository.StitchStateSkipped)
	}
	if key, err := stitchRepo.GetStitchedObjectKey(ctx, fileID); err != nil || key != "" {
		t.Fatalf("GetStitchedObjectKey = (%q, %v), want none (Tier A serves the file)", key, err)
	}
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(cfg.Bucket), Key: aws.String(consolidatedKey(fileID)),
	}); err == nil {
		t.Fatal("parked file left a consolidated object behind")
	}

	// A parked row must not be re-claimed: that is what keeps a save from
	// re-attempting the same impossible assembly forever.
	if _, ok, err := stitchRepo.ClaimForStitch(ctx, fileID, 10*time.Minute, time.Now().UTC()); err != nil || ok {
		t.Fatalf("ClaimForStitch after parking = (ok=%v, err=%v), want not claimed", ok, err)
	}

	// A committed content change re-enqueues the file, which is how a layout
	// that later becomes eligible gets another chance.
	if err := stitchRepo.Delete(ctx, fileID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := stitchRepo.Enqueue(ctx, fileID, time.Now().UTC()); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if _, ok, err := stitchRepo.ClaimForStitch(ctx, fileID, 10*time.Minute, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("ClaimForStitch after re-enqueue = (ok=%v, err=%v), want claimed", ok, err)
	}
}
