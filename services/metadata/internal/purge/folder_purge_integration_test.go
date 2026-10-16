//go:build integration

// End-to-end check of the permanent FOLDER delete against the real dev stack
// (ScyllaDB + NATS):
//
//	go test -tags integration ./internal/purge/...
//
// PurgeFolderSubtree is the fan-out behind PermanentlyDeleteFolder. It has to
// do three things a file delete does not: walk the subtree (including
// soft-deleted children), queue one real purge job per file it finds, and then
// drop the folder metadata — in that order, so a failure part-way through
// leaves the folders reachable and the operation retryable rather than
// stranding files whose parent vanished.
//
// The files use a valid hex block hash because the decrement request encodes
// it: an unparseable hash fails the publish, which is exactly why the result
// carries a Failed count instead of assuming success.
package purge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/db"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
)

func purgeIntegrationSession(t *testing.T) *gocql.Session {
	t.Helper()
	connectCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := db.Connect(connectCtx, db.Config{
		// 127.0.0.1, not "localhost": on Windows localhost resolves to ::1
		// first and the container's published CQL port is IPv4-only.
		Hosts: []string{"127.0.0.1"}, Port: 9042, Datacenter: "datacenter1",
		Username: "cassandra", Password: "cassandra",
		Consistency: gocql.LocalQuorum, ReplicationFactor: 1,
	})
	if err != nil {
		t.Skipf("ScyllaDB unavailable: %v", err)
	}
	t.Cleanup(session.Close)
	if _, err := db.Migrate(context.Background(), session, db.Keyspace); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return session
}

func TestIntegrationPurgeFolderSubtree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session := purgeIntegrationSession(t)
	metadataRepo := repository.NewMetadataRepo(session)
	folderRepo := repository.NewFolderRepo(session)
	purgeRepo := repository.NewPurgeRepo(session)

	// The decree-request publish goes through the real dec stream, so the
	// topology has to exist (idempotent create, same call the service makes).
	conn, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Skipf("NATS unavailable: %v", err)
	}
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	if err := natsx.EnsureTopology(ctx, js, 1); err != nil {
		t.Fatalf("ensure topology: %v", err)
	}

	owner := uuid.NewString()
	root, err := folderRepo.EnsureRootFolder(ctx, owner)
	if err != nil {
		t.Fatalf("EnsureRootFolder: %v", err)
	}
	doomed, err := folderRepo.CreateChildFolder(ctx, owner, root.FolderID, root.AncestorIDs, "Something")
	if err != nil {
		t.Fatalf("CreateChildFolder(doomed): %v", err)
	}
	nested, err := folderRepo.CreateChildFolder(ctx, owner, doomed.FolderID, doomed.AncestorIDs, "nested")
	if err != nil {
		t.Fatalf("CreateChildFolder(nested): %v", err)
	}
	live, err := folderRepo.CreateChildFolder(ctx, owner, root.FolderID, root.AncestorIDs, "Live")
	if err != nil {
		t.Fatalf("CreateChildFolder(live): %v", err)
	}

	// A real block hash: the decrement request hex-decodes every hash.
	sum := sha256.Sum256([]byte("folder-purge-block"))
	blockHash := hex.EncodeToString(sum[:])
	file, err := metadataRepo.CreateFileWithBlockSizes(ctx, nested.FolderID, owner, "clip.mp4", "video/mp4",
		"it-folder-purge-hash", []string{blockHash}, []int64{8 << 20}, 8<<20, 0)
	if err != nil {
		t.Fatalf("CreateFileWithBlockSizes: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if current, getErr := metadataRepo.GetFileByID(cleanupCtx, owner, file.FileID); getErr == nil {
			_ = metadataRepo.RemoveFileProjections(cleanupCtx, current)
		}
		for _, folderID := range []string{root.FolderID, doomed.FolderID, nested.FolderID, live.FolderID} {
			if id, parseErr := gocql.ParseUUID(folderID); parseErr == nil {
				_ = session.Query(`DELETE FROM folders_by_id WHERE folder_id = ?`, id).WithContext(cleanupCtx).Exec()
			}
		}
		// folders_by_parent is keyed by the PARENT, so a child only leaves the
		// table if its own parent partition is swept. Deleting just folders_by_id
		// would strand rows that earlier runs of this test left behind: they are
		// unreachable (no folders_by_id row) but still poisonous data in a shared
		// dev cluster.
		for _, edge := range [][2]string{{root.FolderID, doomed.FolderID}, {doomed.FolderID, nested.FolderID}, {root.FolderID, live.FolderID}} {
			parent, parentErr := gocql.ParseUUID(edge[0])
			child, childErr := gocql.ParseUUID(edge[1])
			if parentErr == nil && childErr == nil {
				_ = session.Query(`DELETE FROM folders_by_parent WHERE parent_id = ? AND folder_id = ?`, parent, child).WithContext(cleanupCtx).Exec()
			}
		}
		_ = session.Query(`DELETE FROM folders_by_owner WHERE owner_id = ?`, mustUUID(owner)).WithContext(cleanupCtx).Exec()
	})

	// Queueing a file requires it to be in the trash, and the subtree delete
	// marks the whole tree with one batch — the same state the UI's folder
	// delete leaves behind.
	batchID := uuid.NewString()
	if _, err := folderRepo.SetFolderDeleted(ctx, owner, nested.FolderID, batchID, true); err != nil {
		t.Fatalf("SetFolderDeleted(nested): %v", err)
	}
	if _, err := folderRepo.SetFolderDeleted(ctx, owner, doomed.FolderID, batchID, true); err != nil {
		t.Fatalf("SetFolderDeleted(doomed): %v", err)
	}
	if _, err := metadataRepo.SetFileDeleted(ctx, owner, file.FileID, batchID, true); err != nil {
		t.Fatalf("SetFileDeleted: %v", err)
	}
	file, err = metadataRepo.GetFileByID(ctx, owner, file.FileID)
	if err != nil {
		t.Fatalf("GetFileByID: %v", err)
	}

	coordinator, err := NewCoordinator("nats://127.0.0.1:4222", purgeRepo, metadataRepo, zap.NewNop())
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })

	// The guards first: only what is already in the trash may be destroyed,
	// and the workspace root never may.
	if _, err := coordinator.PurgeFolderSubtree(ctx, owner, live.FolderID); err == nil {
		t.Error("purged a folder that was never soft-deleted")
	} else if !strings.Contains(err.Error(), "soft-deleted") {
		t.Errorf("live-folder guard returned %v, want a soft-delete complaint", err)
	}
	if _, err := coordinator.PurgeFolderSubtree(ctx, owner, root.FolderID); err == nil {
		t.Error("purged the workspace root")
	}
	if _, err := folderRepo.GetFolder(ctx, owner, live.FolderID); err != nil {
		t.Errorf("a rejected purge still removed the live folder: %v", err)
	}

	// The deletion under test.
	result, err := coordinator.PurgeFolderSubtree(ctx, owner, doomed.FolderID)
	if err != nil {
		t.Fatalf("PurgeFolderSubtree: %v", err)
	}
	if result.Folders != 2 {
		t.Errorf("folder count = %d, want 2 (the folder and its child)", result.Folders)
	}
	if result.Files != 1 {
		t.Errorf("file count = %d, want 1", result.Files)
	}
	if result.Failed != 0 {
		t.Errorf("failed count = %d, want 0: %v", result.Failed, err)
	}

	// A purge JOB per file, in the state the coordinator leaves after it
	// publishes the decrement request — the file's content is revoked
	// asynchronously from here.
	jobID := uuid.NewSHA1(uuid.Nil, fmt.Appendf(nil, "%s:%d", file.FileID, file.Version))
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_ = session.Query(`DELETE FROM purge_jobs WHERE job_id = ?`, gocql.UUID(jobID)).WithContext(cleanupCtx).Exec()
		_ = session.Query(`DELETE FROM purge_jobs_active WHERE bucket = ? AND job_id = ?`,
			time.Now().UTC().Format("2006010215"), gocql.UUID(jobID)).WithContext(cleanupCtx).Exec()
	})
	job, err := purgeRepo.Get(ctx, jobID.String())
	if err != nil {
		t.Fatalf("no purge job for the folder's file: %v", err)
	}
	if job.Reason != "permanent_delete" {
		t.Errorf("job reason = %q, want permanent_delete", job.Reason)
	}
	// A live decrement worker in the environment may already have consumed the
	// request and moved the job on, so both pre-deletion states are accepted;
	// what must never happen is a job that never left PENDING (the file's
	// content would never be revoked) or one that is already terminal.
	if job.State != repository.PurgeDecrementRequested && job.State != repository.PurgeDecremented {
		t.Errorf("job state = %q, want %q or %q", job.State, repository.PurgeDecrementRequested, repository.PurgeDecremented)
	}
	if len(job.BlockHashList) != 1 || job.BlockHashList[0] != blockHash {
		t.Errorf("job block list = %v, want the file's single block", job.BlockHashList)
	}

	// The folders are gone from metadata, and the sibling is untouched.
	for _, gone := range []repository.Folder{doomed, nested} {
		if _, err := folderRepo.GetFolder(ctx, owner, gone.FolderID); err == nil {
			t.Errorf("folder %s survived the purge", gone.FolderName)
		}
	}
	children, _, err := folderRepo.ListChildren(ctx, owner, root.FolderID, 100, "")
	if err != nil {
		t.Fatalf("ListChildren(root): %v", err)
	}
	if len(children) != 1 || children[0].FolderID != live.FolderID {
		t.Errorf("root children = %v, want just the surviving folder", children)
	}

	// Idempotent: the UI can re-send the same request after a partial purge.
	repeat, err := coordinator.PurgeFolderSubtree(ctx, owner, doomed.FolderID)
	if err != nil {
		t.Fatalf("PurgeFolderSubtree (retry): %v", err)
	}
	if repeat.Folders != 0 || repeat.Failed != 0 {
		t.Errorf("retry = %+v, want no folders left and no failures", repeat)
	}
}

func mustUUID(value string) gocql.UUID {
	parsed, err := gocql.ParseUUID(value)
	if err != nil {
		panic(fmt.Sprintf("parse uuid %q: %v", value, err))
	}
	return parsed
}
