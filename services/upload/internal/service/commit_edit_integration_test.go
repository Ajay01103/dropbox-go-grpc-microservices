//go:build integration

// Exercises the upload side's edit accounting against real Scylla: a staged
// block starts at zero references, and a committed version gives each of its
// blocks exactly as many references as the version holds occurrences of it.
//
// Getting this wrong is quiet and expensive: an inflated count keeps a dead
// block alive forever (it can never reach zero and be collected), and a
// missing count lets a live block be collected out from under a file. Neither
// shows up in a unit test, so it is checked against the real LWT counters.
//
//	go test -tags integration ./internal/service/... -run TestIntegrationEditReferences
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"go.uber.org/zap"

	udb "github.com/Ajay01103/go-dropbox/upload/db"
	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
)

func editTestBlockRepo(t *testing.T) (*repository.BlockRepo, *gocql.Session) {
	t.Helper()

	connectCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := udb.Connect(connectCtx, udb.Config{
		// 127.0.0.1, not "localhost": on Windows localhost resolves to ::1
		// first and the container's published CQL port is IPv4-only.
		Hosts:             []string{"127.0.0.1"},
		Port:              9042,
		Datacenter:        "datacenter1",
		Username:          "cassandra",
		Password:          "cassandra",
		Consistency:       gocql.LocalQuorum,
		ReplicationFactor: 1,
	})
	if err != nil {
		t.Skipf("ScyllaDB unavailable: %v", err)
	}
	t.Cleanup(session.Close)
	return repository.NewBlockRepo(session), session
}

// stagedHash returns a hash that exists in the blocks table at ref_count 0, as
// WriteEditBlock leaves one before a commit claims it.
func stagedHash(t *testing.T, repo *repository.BlockRepo, session *gocql.Session, seed string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(seed))
	hash := hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := repo.InsertStagedBlock(ctx, hash, 8, "etag", "s3"); err != nil {
		t.Fatalf("insert staged block: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = repo.DeleteBlockRecord(cleanupCtx, hash)
		_ = repo.RemoveGCCandidate(cleanupCtx, hash)
	})
	if block, err := repo.GetBlock(ctx, hash); err != nil || block.RefCount != 0 {
		t.Fatalf("staged block = (%+v, %v), want ref_count 0", block, err)
	}
	_ = session
	return hash
}

func TestIntegrationEditReferencesCountStagedAndReusedBlocks(t *testing.T) {
	repo, session := editTestBlockRepo(t)
	svc := &UploadService{blockRepo: repo, logger: zap.NewNop()}
	ctx := context.Background()

	staged := stagedHash(t, repo, session, "staged-block")
	// A block another file already references, exactly as a deduplicated block
	// arrives at an edit: the commit must add one reference for this version
	// without disturbing the other file's.
	existing := stagedHash(t, repo, session, "existing-block")
	if err := repo.IncrementRefCount(ctx, existing); err != nil {
		t.Fatalf("seed existing reference: %v", err)
	}

	// The new version holds the staged block twice and the existing block once.
	reused, err := svc.applyEditReferences(ctx, []string{staged, staged, existing})
	if err != nil {
		t.Fatalf("applyEditReferences: %v", err)
	}
	if reused != 1 {
		t.Fatalf("reused occurrences = %d, want 1 (only the already-referenced block)", reused)
	}

	stagedBlock, err := repo.GetBlock(ctx, staged)
	if err != nil {
		t.Fatalf("read staged block: %v", err)
	}
	if stagedBlock.RefCount != 2 {
		t.Fatalf("staged block ref_count = %d, want 2 (one per occurrence in the version)", stagedBlock.RefCount)
	}
	existingBlock, err := repo.GetBlock(ctx, existing)
	if err != nil {
		t.Fatalf("read existing block: %v", err)
	}
	if existingBlock.RefCount != 2 {
		t.Fatalf("existing block ref_count = %d, want 2 (its own reference plus the version's)", existingBlock.RefCount)
	}
}

func TestIntegrationEditReferencesClearsTheGCCandidate(t *testing.T) {
	repo, session := editTestBlockRepo(t)
	svc := &UploadService{blockRepo: repo, logger: zap.NewNop()}
	ctx := context.Background()

	hash := stagedHash(t, repo, session, "staged-then-committed")
	// WriteEditBlock registers a staged block as a GC candidate in case the
	// save never lands; a commit must unregister it, or the block GC could
	// delete bytes a live version references.
	if err := repo.MarkGCCandidate(ctx, hash, time.Millisecond); err != nil {
		t.Fatalf("mark gc candidate: %v", err)
	}
	if _, err := svc.applyEditReferences(ctx, []string{hash}); err != nil {
		t.Fatalf("applyEditReferences: %v", err)
	}

	candidates, err := repo.ListEligibleGCCandidates(ctx, time.Now().Add(time.Hour), 1000)
	if err != nil {
		t.Fatalf("list gc candidates: %v", err)
	}
	for _, candidate := range candidates {
		if candidate.BlockHash == hash {
			t.Fatalf("committed block %s is still a GC candidate", hash)
		}
	}
}

func TestIntegrationValidationRejectsOnlyMalformedSaves(t *testing.T) {
	svc := &UploadService{logger: zap.NewNop()}
	block := hex.EncodeToString(make([]byte, 32))

	valid := CommitEditRequest{
		FileID: "file-1", BaseVersion: 1, SizeBytes: 10, BlockSize: 4,
		BlockHashes: []string{block, block, block},
	}
	if err := svc.validateCommitEdit(valid); err != nil {
		t.Fatalf("validateCommitEdit rejected a well-formed save: %v", err)
	}
	cases := []struct {
		name    string
		mutate  func(*CommitEditRequest)
		wantErr bool
	}{
		{name: "no file", mutate: func(r *CommitEditRequest) { r.FileID = "" }, wantErr: true},
		{name: "no base", mutate: func(r *CommitEditRequest) { r.BaseVersion = 0 }, wantErr: true},
		{name: "size does not add up", mutate: func(r *CommitEditRequest) { r.SizeBytes = 13 }, wantErr: true},
		{name: "hash is not a digest", mutate: func(r *CommitEditRequest) { r.BlockHashes[0] = "nope" }, wantErr: true},
		{
			name: "declared block size disagrees",
			mutate: func(r *CommitEditRequest) {
				r.BlockSizes = []int64{4, 4, 3}
			},
			wantErr: true,
		},
		{
			name: "declared sizes match the derivation",
			mutate: func(r *CommitEditRequest) {
				r.BlockSizes = []int64{4, 4, 2}
			},
			wantErr: false,
		},
	}
	for _, testCase := range cases {
		request := valid
		request.BlockHashes = append([]string{}, valid.BlockHashes...)
		testCase.mutate(&request)
		err := svc.validateCommitEdit(request)
		if testCase.wantErr && err == nil {
			t.Fatalf("%s: accepted a malformed save", testCase.name)
		}
		if !testCase.wantErr && err != nil {
			t.Fatalf("%s: rejected a valid save: %v", testCase.name, err)
		}
	}
}

func TestIntegrationBlockSizesForEditAddsUpToTheSize(t *testing.T) {
	// The derived sizes are what the metadata service validates each block
	// against: every non-final block is the block size, the last is the
	// remainder.
	request := CommitEditRequest{SizeBytes: 10, BlockSize: 4}
	request.BlockHashes = []string{"a", "b", "c"}
	sizes := blockSizesForEdit(request)
	var total int64
	for _, size := range sizes {
		total += size
	}
	if total != request.SizeBytes {
		t.Fatalf("block sizes %v add up to %d, want %d", sizes, total, request.SizeBytes)
	}
	if fmt.Sprint(sizes) != "[4 4 2]" {
		t.Fatalf("block sizes = %v, want [4 4 2]", sizes)
	}
}
