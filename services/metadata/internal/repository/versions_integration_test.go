//go:build integration

// Exercises the version history against real Scylla: the commit that wins a
// version number, the head pointer that follows it, and the immutability that
// makes "your previous version is kept" true.
//
//	go test -tags integration ./internal/repository/... -run TestIntegrationCommitVersion
package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	mdb "github.com/Ajay01103/go-dropbox/metadata/db"
)

func versionTestRepo(t *testing.T) (*MetadataRepo, *gocql.Session) {
	t.Helper()

	connectCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := mdb.Connect(connectCtx, mdb.Config{
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

	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer migrateCancel()
	if _, err := mdb.Migrate(migrateCtx, session, mdb.Keyspace); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { dropVersionTestFile(t, session) })
	return NewMetadataRepo(session), session
}

var (
	versionTestMu       sync.Mutex
	versionTestFileID   string
	versionTestFolderID string
)

func dropVersionTestFile(t *testing.T, session *gocql.Session) {
	t.Helper()
	versionTestMu.Lock()
	fileID, folderID := versionTestFileID, versionTestFolderID
	versionTestMu.Unlock()
	if fileID == "" {
		return
	}
	id, err := uuid.Parse(fileID)
	if err != nil {
		return
	}
	folder, err := uuid.Parse(folderID)
	if err != nil {
		return
	}
	_ = session.Query(`DELETE FROM file_versions WHERE file_id = ?`, id).WithContext(context.Background()).Exec()
	_ = session.Query(`DELETE FROM file_head WHERE file_id = ?`, id).WithContext(context.Background()).Exec()
	_ = session.Query(`DELETE FROM file_manifest WHERE file_id = ?`, id).WithContext(context.Background()).Exec()
	_ = session.Query(`DELETE FROM files_by_id WHERE file_id = ?`, id).WithContext(context.Background()).Exec()
	_ = session.Query(`DELETE FROM files_by_folder WHERE folder_id = ? AND file_id = ?`, folder, id).WithContext(context.Background()).Exec()
}

func seedVersionTestFile(t *testing.T, repo *MetadataRepo) File {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	folderID := uuid.NewString()
	file, err := repo.CreateFileWithBlockSizes(
		ctx, folderID, uuid.NewString(), "report.pdf", "application/pdf",
		"client-file-hash", []string{strings.Repeat("11", 32), strings.Repeat("22", 32)},
		[]int64{8, 4}, 12, 0,
	)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	versionTestMu.Lock()
	versionTestFileID, versionTestFolderID = file.FileID, folderID
	versionTestMu.Unlock()
	return file
}

func TestIntegrationCommitVersionWinsOnceAndKeepsHistory(t *testing.T) {
	repo, _ := versionTestRepo(t)
	file := seedVersionTestFile(t, repo)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A file created by the upload path has no version history yet: editing
	// treats it as version 1 described by its own row.
	if _, err := repo.GetFileHead(ctx, file.FileID); err != ErrNoVersionRow {
		t.Fatalf("head of a pre-version file = %v, want ErrNoVersionRow", err)
	}

	edited := []string{strings.Repeat("11", 32), strings.Repeat("33", 32)}
	applied, err := repo.CommitVersionRow(ctx, CommitVersionInput{
		FileID: file.FileID, OwnerID: file.OwnerID, BaseVersion: 1,
		BlockHashes: edited, BlockSize: 8, SizeBytes: 16,
		ContentHash: "hash-v2", ContentType: "application/pdf",
		Filename: "report.pdf", SniffedType: "pdf", Editor: "extend-pdf-editor",
		CommitID: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("commit version: %v", err)
	}
	if !applied {
		t.Fatal("the first commit of a version number must win it")
	}

	head, err := repo.GetFileHead(ctx, file.FileID)
	if err != nil {
		t.Fatalf("get head: %v", err)
	}
	if head.CurrentVersion != 2 || head.ContentHash != "hash-v2" {
		t.Fatalf("head = (v%d, %q), want (v2, hash-v2)", head.CurrentVersion, head.ContentHash)
	}

	version, err := repo.GetFileVersion(ctx, file.FileID, 2)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version.BaseVersion != 1 || version.Editor != "extend-pdf-editor" || len(version.BlockHashList) != 2 {
		t.Fatalf("version row = %+v, want base 1, editor and blocks recorded", version)
	}
	if version.CreatedBy != file.OwnerID {
		t.Fatalf("version author = %q, want the committer", version.CreatedBy)
	}
	if version.CreatedAt.IsZero() {
		t.Fatal("version row has no timestamp")
	}
	if version.SniffedType != "pdf" || version.BlockSize != 8 || version.SizeBytes != 16 {
		t.Fatalf("version row lost the sniffed type, block size or size: %+v", version)
	}
}

func TestIntegrationConcurrentCommitsProduceOneWinner(t *testing.T) {
	repo, _ := versionTestRepo(t)
	file := seedVersionTestFile(t, repo)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Two editors saving the same base version at the same time. Exactly one
	// may win version 2: the other is told it lost, so it can offer a conflicted
	// copy instead of silently overwriting work it never saw.
	start := make(chan struct{})
	results := make([]bool, 2)
	errors := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			applied, err := repo.CommitVersionRow(ctx, CommitVersionInput{
				FileID: file.FileID, OwnerID: file.OwnerID, BaseVersion: 1,
				BlockHashes: []string{strings.Repeat("44", 32)},
				BlockSize:   8, SizeBytes: 8,
				ContentHash: "hash-racer-" + string(rune('a'+index)),
				ContentType: "application/pdf", Filename: "report.pdf",
				SniffedType: "pdf", Editor: "extend-pdf-editor", CommitID: uuid.NewString(),
			})
			results[index], errors[index] = applied, err
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, applied := range results {
		if errors[i] != nil {
			t.Fatalf("racer %d failed: %v", i, errors[i])
		}
		if applied {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d racers won version 2, want exactly 1", winners)
	}

	head, err := repo.GetFileHead(ctx, file.FileID)
	if err != nil {
		t.Fatalf("get head: %v", err)
	}
	if head.CurrentVersion != 2 {
		t.Fatalf("head = v%d after one winner, want v2 (a loser must not advance it)", head.CurrentVersion)
	}

	// Both racers wrote nothing but a version row attempt, and the loser's row
	// does not exist: version 2 has exactly one row owner.
	version, err := repo.GetFileVersion(ctx, file.FileID, 2)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version.ContentHash != head.ContentHash {
		t.Fatalf("version row hash %q does not match the head %q", version.ContentHash, head.ContentHash)
	}
}

func TestIntegrationApplyHeadContentRewritesEveryProjection(t *testing.T) {
	repo, _ := versionTestRepo(t)
	file := seedVersionTestFile(t, repo)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	next := file
	next.BlockHashList = []string{strings.Repeat("55", 32)}
	next.SizeBytes = 10
	next.ContentHash = "hash-v2"
	next.Version = 2
	next.UpdatedAt = time.Now().UTC()
	next.ThumbnailKey = ""
	next.ThumbnailStatus = "pending"

	manifest := FileManifestRow{
		FileID: file.FileID, TotalSize: 10, ContentType: "application/pdf",
		ContentHash: "hash-v2",
		Blocks:      []ManifestBlock{{Index: 0, Hash: next.BlockHashList[0], SizeBytes: 10}},
	}
	if err := repo.ApplyHeadContent(ctx, file, next, manifest); err != nil {
		t.Fatalf("apply head content: %v", err)
	}

	// The manifest is rewritten, not patched: the old two-block partition must
	// be gone, or a reader would reconstruct the previous version's tail.
	stored, err := repo.GetFileManifest(ctx, file.FileID)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if len(stored.Blocks) != 1 || stored.Blocks[0].Hash != next.BlockHashList[0] {
		t.Fatalf("manifest = %+v, want exactly the new single block", stored.Blocks)
	}
	if stored.TotalSize != 10 || stored.ContentHash != "hash-v2" {
		t.Fatalf("manifest header = (%d, %q), want (10, hash-v2)", stored.TotalSize, stored.ContentHash)
	}
}
