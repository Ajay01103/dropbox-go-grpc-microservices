//go:build integration

// Exercises the edit path end to end against real Scylla: session parameters,
// the block verification a commit insists on, the version it wins, and the
// conflict it reports when another save got there first.
//
// The content store is faked (blocks served from memory) so the test needs
// neither S3 nor uploaded bytes; everything else is the real service and the
// real repository.
//
//	go test -tags integration ./internal/service/... -run TestIntegration
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	mdb "github.com/Ajay01103/go-dropbox/metadata/db"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// editTestSession connects to the dev cluster, applying migrations first, and
// skips the test when the cluster is not running.
func editTestSession(t *testing.T) *gocql.Session {
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
	return session
}

func editIntegrationService(t *testing.T) (*MetadataService, *repository.MetadataRepo, *fakeBlockReader, *gocql.Session) {
	t.Helper()
	session := editTestSession(t)
	repo := repository.NewMetadataRepo(session)
	reader := &fakeBlockReader{blocks: map[string][]byte{}}
	cfg := config.Config{
		EditPDFEnabled:    true,
		EditDocxEnabled:   true,
		EditXlsxEnabled:   true,
		EditPDFMaxBytes:   50 << 20,
		EditDocxMaxBytes:  25 << 20,
		EditXlsxMaxBytes:  25 << 20,
		DownloadTicketKey: "integration-ticket-key",
		ContentBaseURL:    "http://localhost:50053",
	}
	svc := New(repo, repository.NewFolderRepo(session), nil, nil, nil, cfg, zap.NewNop())
	svc.SetBlockReader(reader, nil)
	return svc, repo, reader, session
}

// seedBlockSize is the block size the seeded file was written with. A save
// must re-split at exactly this size, so every commit below uses it too — the
// metadata service refuses a block size it did not write the file with.
const seedBlockSize = 129

// pdfBlock builds one full-size block whose bytes still sniff as a PDF.
func pdfBlock(body string) []byte {
	prefix := "%PDF-1.7 " + body
	if len(prefix) >= seedBlockSize {
		return []byte(prefix[:seedBlockSize])
	}
	return []byte(prefix + strings.Repeat(" ", seedBlockSize-len(prefix)))
}

// seedPdfFile stores a two-block PDF and creates the file row the upload path
// would have created, so the edit path starts from a pre-version-history file.
func seedPdfFile(t *testing.T, repo *repository.MetadataRepo, reader *fakeBlockReader, session *gocql.Session) (repository.File, []byte, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := []byte("%PDF-1.7\n" + strings.Repeat("a", seedBlockSize-9))
	second := []byte("trailer-bytes")
	hashes := []string{hashOf(first), hashOf(second)}
	reader.blocks[hashes[0]] = first
	reader.blocks[hashes[1]] = second

	file, err := repo.CreateFileWithBlockSizes(
		ctx, uuid.NewString(), uuid.NewString(), "report.pdf", "application/pdf",
		"client-file-hash", hashes, []int64{int64(len(first)), int64(len(second))},
		int64(len(first)+len(second)), 0,
	)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	fileID, _ := uuid.Parse(file.FileID)
	folderID, _ := uuid.Parse(file.FolderID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = repo.DeleteFileManifest(cleanupCtx, file.FileID)
		_ = session.Query(`DELETE FROM file_versions WHERE file_id = ?`, fileID).WithContext(cleanupCtx).Exec()
		_ = session.Query(`DELETE FROM file_head WHERE file_id = ?`, fileID).WithContext(cleanupCtx).Exec()
		_ = session.Query(`DELETE FROM files_by_id WHERE file_id = ?`, fileID).WithContext(cleanupCtx).Exec()
		_ = session.Query(`DELETE FROM files_by_folder WHERE folder_id = ? AND file_id = ?`, folderID, fileID).WithContext(cleanupCtx).Exec()
	})
	return file, first, second
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestIntegrationGetEditSessionDescribesTheVersionBeingEdited(t *testing.T) {
	svc, repo, reader, session := editIntegrationService(t)
	file, first, _ := seedPdfFile(t, repo, reader, session)

	result, err := svc.GetEditSession(context.Background(), file.OwnerID, file.FileID)
	if err != nil {
		t.Fatalf("GetEditSession: %v", err)
	}
	if !result.CanEdit || result.UnavailableReason != "" {
		t.Fatalf("session refused a small PDF: %+v", result)
	}
	if result.Version != 1 {
		t.Fatalf("session version = %d, want 1 (the file's only version)", result.Version)
	}
	if result.SniffedType != EditTypePDF {
		t.Fatalf("sniffed type = %q, want pdf", result.SniffedType)
	}
	// The block size is derived from the dense manifest, which is what lets a
	// save re-split the file identically and reuse unchanged blocks.
	if result.BlockSize != int64(len(first)) {
		t.Fatalf("session block size = %d, want the first block's size %d", result.BlockSize, len(first))
	}
	if result.MaxEditBytes != svc.cfg.EditPDFMaxBytes {
		t.Fatalf("max edit bytes = %d, want the pdf cap", result.MaxEditBytes)
	}
	if !strings.Contains(result.TicketURL, "/content/"+file.FileID) {
		t.Fatalf("ticket URL = %q, want the content route for this file", result.TicketURL)
	}
	if len(result.BlockHashes) != 2 || result.ContentHash == "" {
		t.Fatalf("session did not carry the version's blocks and hash: %+v", result)
	}
}

func TestIntegrationCommitVersionAddsAVersionAndKeepsTheOldOne(t *testing.T) {
	svc, repo, reader, session := editIntegrationService(t)
	file, first, _ := seedPdfFile(t, repo, reader, session)
	ctx := context.Background()

	editSession, err := svc.GetEditSession(ctx, file.OwnerID, file.FileID)
	if err != nil {
		t.Fatalf("GetEditSession: %v", err)
	}

	// A save that appends to the document: the first block stays byte-identical
	// (so the client never re-sends it), the tail is new.
	editedTail := []byte("%PDF-1.7 edited tail")
	edited := append(append([]byte{}, first...), editedTail...)
	newHashes := []string{hashOf(first), hashOf(editedTail)}
	reader.blocks[newHashes[1]] = editedTail

	prepared, err := svc.PrepareCommit(ctx, file.OwnerID, file.FileID, editSession.Version, newHashes)
	if err != nil {
		t.Fatalf("PrepareCommit: %v", err)
	}
	if !prepared.BaseIsHead || prepared.HeadVersion != 1 {
		t.Fatalf("prepare = %+v, want base 1 to still be head", prepared)
	}
	if len(prepared.AlreadyReferenced) != 1 || prepared.AlreadyReferenced[0] != newHashes[0] {
		t.Fatalf("already referenced = %v, want only the unchanged block", prepared.AlreadyReferenced)
	}

	result, err := svc.CommitVersion(ctx, CommitVersionRequest{
		FileID: file.FileID, OwnerID: file.OwnerID, BaseVersion: editSession.Version,
		CommitID: uuid.NewString(), SizeBytes: int64(len(edited)),
		BlockSize: int64(len(first)), BlockHashes: newHashes,
		Editor: "extend-pdf-editor", Filename: "report.pdf",
	})
	if err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
	if !result.Committed || result.Version != 2 {
		t.Fatalf("commit result = %+v, want version 2 committed", result)
	}

	// The head, the projections and the manifest describe the new version.
	updated, err := repo.GetFileByID(ctx, file.OwnerID, file.FileID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if updated.Version != 2 || updated.SizeBytes != int64(len(edited)) || updated.ContentHash != result.ContentHash {
		t.Fatalf("file row = (v%d, %d bytes, %q), want the committed version", updated.Version, updated.SizeBytes, updated.ContentHash)
	}
	manifest, err := repo.GetFileManifest(ctx, file.FileID)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if len(manifest.Blocks) != 2 || manifest.Blocks[1].Hash != newHashes[1] {
		t.Fatalf("manifest blocks = %+v, want the new block list", manifest.Blocks)
	}

	// The version row records what the commit claims, and version 1's bytes are
	// still exactly what they were: an edit never rewrites history.
	stored, err := repo.GetFileVersion(ctx, file.FileID, 2)
	if err != nil {
		t.Fatalf("get version 2: %v", err)
	}
	if stored.BaseVersion != 1 || stored.SniffedType != EditTypePDF || stored.Editor != "extend-pdf-editor" {
		t.Fatalf("version row = %+v, want base, sniffed type and editor recorded", stored)
	}
	if hashOf(reader.blocks[editSession.BlockHashes[0]]) != editSession.BlockHashes[0] {
		t.Fatal("the original blocks are no longer readable after the commit")
	}
}

func TestIntegrationConcurrentSavesProduceOneConflict(t *testing.T) {
	svc, repo, reader, session := editIntegrationService(t)
	file, _, _ := seedPdfFile(t, repo, reader, session)
	ctx := context.Background()

	editSession, err := svc.GetEditSession(ctx, file.OwnerID, file.FileID)
	if err != nil {
		t.Fatalf("GetEditSession: %v", err)
	}

	commit := func(body string) (CommitVersionResult, error) {
		block := pdfBlock(body)
		reader.blocks[hashOf(block)] = block
		return svc.CommitVersion(ctx, CommitVersionRequest{
			FileID: file.FileID, OwnerID: file.OwnerID, BaseVersion: editSession.Version,
			CommitID: uuid.NewString(), SizeBytes: int64(len(block)),
		BlockSize: seedBlockSize, BlockHashes: []string{hashOf(block)},
		Editor: "extend-pdf-editor", Filename: "report.pdf",
	})
}

	first, err := commit("first saver")
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if !first.Committed {
		t.Fatalf("first commit = %+v, want committed", first)
	}

	// The second save still believes the file is version 1. It must be refused
	// and told where the file actually is — never applied on top.
	second, err := commit("second saver")
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if second.Committed || !second.Conflict {
		t.Fatalf("second commit = %+v, want a conflict", second)
	}
	if second.HeadVersion != first.Version || second.HeadContentHash != first.ContentHash {
		t.Fatalf("conflict reports head = (v%d, %q), want the winner", second.HeadVersion, second.HeadContentHash)
	}
}

func TestIntegrationCommitRetryOfTheSameBytesIsNotAConflict(t *testing.T) {
	svc, repo, reader, session := editIntegrationService(t)
	file, _, _ := seedPdfFile(t, repo, reader, session)
	ctx := context.Background()

	editSession, err := svc.GetEditSession(ctx, file.OwnerID, file.FileID)
	if err != nil {
		t.Fatalf("GetEditSession: %v", err)
	}
	block := pdfBlock("retried save")
	reader.blocks[hashOf(block)] = block
	request := CommitVersionRequest{
		FileID: file.FileID, OwnerID: file.OwnerID, BaseVersion: editSession.Version,
		CommitID: uuid.NewString(), SizeBytes: int64(len(block)),
		BlockSize: seedBlockSize, BlockHashes: []string{hashOf(block)},
		Editor: "extend-pdf-editor", Filename: "report.pdf",
	}

	first, err := svc.CommitVersion(ctx, request)
	if err != nil || !first.Committed {
		t.Fatalf("first commit = (%+v, %v), want committed", first, err)
	}
	// A client that retries after losing the response gets a conflict; when the
	// bytes are the ones the head already holds, that is success, not a loss.
	retry, err := svc.CommitVersion(ctx, request)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.Conflict || !retry.Committed || retry.Version != first.Version {
		storedHead, _ := repo.GetFileHead(ctx, file.FileID)
		storedVersion, _ := repo.GetFileVersion(ctx, file.FileID, 2)
		t.Fatalf("retry = %+v, want the same committed version %d (first=%q, head=%q, versionRow=%q)",
			retry, first.Version, first.ContentHash, storedHead.ContentHash, storedVersion.ContentHash)
	}
}

func TestIntegrationCommitRejectsATypeChange(t *testing.T) {
	svc, repo, reader, session := editIntegrationService(t)
	file, _, _ := seedPdfFile(t, repo, reader, session)
	ctx := context.Background()

	editSession, err := svc.GetEditSession(ctx, file.OwnerID, file.FileID)
	if err != nil {
		t.Fatalf("GetEditSession: %v", err)
	}
	// A DOCX payload committed over a PDF: the bytes and the file disagree, so
	// no version may be written.
	zipPayload := append([]byte("PK\x03\x04"), []byte("word/document.xml")...)
	reader.blocks[hashOf(zipPayload)] = zipPayload

	_, err = svc.CommitVersion(ctx, CommitVersionRequest{
		FileID: file.FileID, OwnerID: file.OwnerID, BaseVersion: editSession.Version,
		CommitID: uuid.NewString(), SizeBytes: int64(len(zipPayload)),
		BlockSize: int64(len(zipPayload)), BlockHashes: []string{hashOf(zipPayload)},
		Editor: "extend-docx-editor", Filename: "report.pdf",
	})
	if !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("type change error = %v, want ErrInvalidEdit", err)
	}

	head, headErr := repo.GetFileHead(ctx, file.FileID)
	if headErr == nil && head.CurrentVersion != editSession.Version {
		t.Fatalf("head moved to v%d after a rejected commit, want v%d", head.CurrentVersion, editSession.Version)
	}
	if headErr != nil && !errors.Is(headErr, repository.ErrNoVersionRow) {
		t.Fatalf("get head: %v", headErr)
	}
}

func TestIntegrationCommitRejectsBlocksThatWereNeverStored(t *testing.T) {
	svc, repo, reader, session := editIntegrationService(t)
	file, _, _ := seedPdfFile(t, repo, reader, session)
	ctx := context.Background()

	editSession, err := svc.GetEditSession(ctx, file.OwnerID, file.FileID)
	if err != nil {
		t.Fatalf("GetEditSession: %v", err)
	}
	// The client claims a block it never uploaded: a version row naming bytes
	// nobody can read back would be unrecoverable, so the commit refuses.
	phantom := pdfBlock("nothing ever stored this")
	_, err = svc.CommitVersion(ctx, CommitVersionRequest{
		FileID: file.FileID, OwnerID: file.OwnerID, BaseVersion: editSession.Version,
		CommitID: uuid.NewString(), SizeBytes: int64(len(phantom)),
		BlockSize: seedBlockSize, BlockHashes: []string{hashOf(phantom)},
		Editor: "extend-pdf-editor", Filename: "report.pdf",
	})
	if !errors.Is(err, ErrEditBlockUnavailable) {
		t.Fatalf("missing block error = %v, want ErrEditBlockUnavailable", err)
	}
	if _, err := repo.GetFileVersion(ctx, file.FileID, editSession.Version+1); !errors.Is(err, repository.ErrNoVersionRow) {
		t.Fatalf("a rejected commit left a version row behind: %v", err)
	}
}
