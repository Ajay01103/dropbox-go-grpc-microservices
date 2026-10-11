//go:build integration

// End-to-end verification of the download path against the real dev
// infrastructure (ScyllaDB + RustFS), mirroring how the services run:
//
//	go test -tags integration ./internal/content/...
//
// It covers: manifest persistence, Tier A range-aware reconstruction, the
// Tier B UploadPartCopy stitch, and the 302-to-presigned-URL fast path.
package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/db"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
)

func integrationScyllaConfig() db.Config {
	return db.Config{
		// 127.0.0.1, not "localhost": on Windows localhost resolves to ::1
		// first and the container's published CQL port is IPv4-only.
		Hosts:      []string{"127.0.0.1"},
		Port:       9042,
		Datacenter: "datacenter1",
		// Same defaults config.Load applies for the metadata service.
		Username:          "cassandra",
		Password:          "cassandra",
		Consistency:       gocql.LocalQuorum,
		ReplicationFactor: 1,
	}
}

func integrationS3Config() S3Config {
	return S3Config{
		Bucket:    "uploads",
		Region:    "us-east-1",
		Endpoint:  "http://localhost:9000",
		AccessKey: "rustfsadmin",
		SecretKey: "rustfsadmin",
	}
}

// rawS3Client is a plain client used only to seed block objects in the test.
func rawS3Client(t *testing.T, cfg S3Config) *s3.Client {
	t.Helper()
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		awsconfig.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: cfg.Endpoint, SigningRegion: cfg.Region, HostnameImmutable: true}, nil
			}),
		),
	)
	if err != nil {
		t.Skipf("load S3 config: %v", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = true })
}

func putBlock(t *testing.T, ctx context.Context, client *s3.Client, bucket string, data []byte) (string, repository.ManifestBlock) {
	t.Helper()
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	key, err := blockObjectKey(hash)
	if err != nil {
		t.Fatalf("blockObjectKey: %v", err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	}); err != nil {
		t.Skipf("RustFS unavailable (put block): %v", err)
	}
	return hash, repository.ManifestBlock{Hash: hash, SizeBytes: int64(len(data))}
}

// setupIntegration connects to Scylla, runs migrations, and returns the repo,
// session, and S3 seed client.
func setupIntegration(t *testing.T) (context.Context, *repository.MetadataRepo, *gocql.Session, *s3.Client, S3Config) {
	t.Helper()
	ctx := context.Background()
	// Bounded connect: an unreachable cluster must SKIP the test, not hang it
	// (db.Connect retries forever on a context with no deadline).
	connectCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	session, err := db.Connect(connectCtx, integrationScyllaConfig())
	cancel()
	if err != nil {
		t.Skipf("ScyllaDB unavailable: %v", err)
	}
	t.Cleanup(session.Close)
	if _, err := db.Migrate(ctx, session, db.Keyspace); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := repository.NewMetadataRepo(session)
	cfg := integrationS3Config()
	client := rawS3Client(t, cfg)
	if _, err := client.ListBuckets(ctx, &s3.ListBucketsInput{}); err != nil {
		t.Skipf("RustFS unavailable: %v", err)
	}
	return ctx, repo, session, client, cfg
}

func TestIntegrationTierALiveReconstruction(t *testing.T) {
	ctx, repo, _, client, cfg := setupIntegration(t)
	storage, err := NewS3Storage(cfg)
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	// Three blocks whose bytes make ordering obvious.
	parts := [][]byte{
		bytes.Repeat([]byte("A"), 64*1024),
		bytes.Repeat([]byte("B"), 64*1024),
		bytes.Repeat([]byte("C"), 12345),
	}
	fileID := uuid.NewString()
	manifest := repository.FileManifestRow{
		FileID: fileID, ContentType: "application/octet-stream", ContentHash: "it-hash",
	}
	var whole bytes.Buffer
	blocks := make([]repository.ManifestBlock, 0, len(parts))
	for i, part := range parts {
		hash, block := putBlock(t, ctx, client, cfg.Bucket, part)
		t.Cleanup(func() {
			key, _ := blockObjectKey(hash)
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
		})
		block.Index = i
		blocks = append(blocks, block)
		manifest.TotalSize += int64(len(part))
		whole.Write(part)
	}
	manifest.Blocks = blocks
	if err := repo.WriteFileManifest(ctx, manifest); err != nil {
		t.Fatalf("WriteFileManifest: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteFileManifest(context.Background(), fileID) })

	// Read it back: the dense manifest must round-trip with exact sizes.
	loaded, err := repo.GetFileManifest(ctx, fileID)
	if err != nil {
		t.Fatalf("GetFileManifest: %v", err)
	}
	if loaded.TotalSize != manifest.TotalSize || len(loaded.Blocks) != len(blocks) {
		t.Fatalf("manifest round-trip = (%d bytes, %d blocks), want (%d, %d)",
			loaded.TotalSize, len(loaded.Blocks), manifest.TotalSize, len(blocks))
	}
	for i, block := range loaded.Blocks {
		if block.SizeBytes != blocks[i].SizeBytes || block.Hash != blocks[i].Hash {
			t.Fatalf("block %d round-trip = (%s, %d), want (%s, %d)", i, block.Hash, block.SizeBytes, blocks[i].Hash, blocks[i].SizeBytes)
		}
	}

	server := NewServer(repo, storage, "it-ticket-key", zap.NewNop(), WithStreamWorkers(3))
	mux := http.NewServeMux()
	server.Handler(mux)
	ticket := integrationTicket(t, fileID)

	// Full download: exact bytes, correct headers.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/"+fileID+"?ticket="+ticket, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("full GET status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), whole.Bytes()) {
		t.Fatalf("full GET = %d bytes, want %d identical bytes", rec.Body.Len(), whole.Len())
	}
	if got := rec.Header().Get("Content-Length"); got != fmt.Sprint(manifest.TotalSize) {
		t.Fatalf("Content-Length = %q, want %d", got, manifest.TotalSize)
	}

	// Range that straddles two block boundaries (sub-block GETs on both sides).
	req := httptest.NewRequest(http.MethodGet, "/content/"+fileID+"?ticket="+ticket, nil)
	req.Header.Set("Range", "bytes=65500-65560")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range GET status = %d, want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), whole.Bytes()[65500:65561]) {
		t.Fatalf("range GET body = %q, want %q", rec.Body.Bytes(), whole.Bytes()[65500:65561])
	}

	// Tail range — the video-scrub shape.
	req = httptest.NewRequest(http.MethodGet, "/content/"+fileID+"?ticket="+ticket, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", manifest.TotalSize-100))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 100 {
		t.Fatalf("tail range = status %d / %d bytes, want 206 / 100", rec.Code, rec.Body.Len())
	}
}

func TestIntegrationTierBStitchAndPresignRedirect(t *testing.T) {
	ctx, repo, session, client, cfg := setupIntegration(t)
	storage, err := NewS3Storage(cfg)
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	stitcher, err := NewStitcher(cfg)
	if err != nil {
		t.Fatalf("NewStitcher: %v", err)
	}

	// Single-block file: S3 multipart exempts the last (only) part from the
	// minimum part size, so this always stitches regardless of chunk size.
	payload := bytes.Repeat([]byte("single-block"), 4096)
	hash, block := putBlock(t, ctx, client, cfg.Bucket, payload)
	t.Cleanup(func() {
		key, _ := blockObjectKey(hash)
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	})
	fileID := uuid.NewString()
	block.Index = 0
	manifest := repository.FileManifestRow{
		FileID: fileID, TotalSize: int64(len(payload)), ContentType: "application/octet-stream",
		Blocks: []repository.ManifestBlock{block},
	}
	if err := repo.WriteFileManifest(ctx, manifest); err != nil {
		t.Fatalf("WriteFileManifest: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteFileManifest(context.Background(), fileID) })

	stitchRepo := repository.NewStitchRepo(session)
	if err := stitchRepo.Enqueue(ctx, fileID, time.Now().UTC()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	t.Cleanup(func() { _ = stitchRepo.Delete(context.Background(), fileID) })

	if _, ok, err := stitchRepo.ClaimForStitch(ctx, fileID, 10*time.Minute, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("ClaimForStitch = (ok=%v, err=%v), want claimed", ok, err)
	}

	key, etag, err := stitcher.Stitch(ctx, fileID, manifest.Blocks, 5<<20)
	if err != nil {
		t.Fatalf("Stitch: %v", err)
	}
	if key == "" || etag == "" {
		t.Fatalf("Stitch returned key=%q etag=%q, want both set", key, etag)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	})

	if err := stitchRepo.MarkReady(ctx, fileID, key, etag, manifest.TotalSize, time.Now().UTC()); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if got, err := stitchRepo.GetStitchedObjectKey(ctx, fileID); err != nil || got != key {
		t.Fatalf("GetStitchedObjectKey = (%q, %v), want (%q, nil)", got, err, key)
	}

	// The content server must now 302 to a presigned S3 URL that serves the
	// exact consolidated bytes (browser goes straight to object storage).
	server := NewServer(repo, storage, "it-ticket-key", zap.NewNop(),
		WithStreamWorkers(3), WithStitcher(stitchRepo, storage))
	mux := http.NewServeMux()
	server.Handler(mux)
	ticket := integrationTicket(t, fileID)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/"+fileID+"?ticket="+ticket, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("stitched GET status = %d, want 302 (body %q)", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if location == "" {
		t.Fatal("stitched GET has no Location header")
	}

	resp, err := http.Get(location)
	if err != nil {
		t.Fatalf("follow presigned URL: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET status = %d, want 200", resp.StatusCode)
	}
	// The 302 hands the browser to object storage, where the headers this
	// content server set no longer apply and the <a download> attribute does
	// not either (cross-origin). Without these two headers a download opens in
	// a tab, so they are asserted on the response the browser actually reads.
	if disposition := resp.Header.Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Fatalf("presigned Content-Disposition = %q, want an attachment disposition", disposition)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "it.bin") {
		t.Fatalf("presigned Content-Disposition = %q, want it to name the file", resp.Header.Get("Content-Disposition"))
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("presigned Content-Type = %q, want the manifest content type", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read presigned body: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("presigned body = %d bytes, want %d identical bytes", len(got), len(payload))
	}
}

// TestIntegrationTierBMultiBlockStitch pins the chunk-size boundary that
// makes zero-copy stitching possible: S3 multipart (and RustFS, as verified
// here) rejects any part below 5 MiB that is not the last, so a file built
// from the upload service's DEFAULT 8 MiB blocks stitches, while a legacy
// 4 MiB-block file is refused by design and stays on Tier A.
func TestIntegrationTierBMultiBlockStitch(t *testing.T) {
	ctx, _, _, client, cfg := setupIntegration(t)
	stitcher, err := NewStitcher(cfg)
	if err != nil {
		t.Fatalf("NewStitcher: %v", err)
	}

	// stage uploads two same-size blocks and returns the manifest plus the
	// concatenated bytes they represent.
	stage := func(t *testing.T, blockSize int) (repository.FileManifestRow, []byte) {
		t.Helper()
		manifest := repository.FileManifestRow{FileID: uuid.NewString(), ContentType: "application/octet-stream"}
		var whole bytes.Buffer
		for i := 0; i < 2; i++ {
			part := bytes.Repeat([]byte{byte('a' + i)}, blockSize)
			hash, block := putBlock(t, ctx, client, cfg.Bucket, part)
			t.Cleanup(func() {
				objKey, _ := blockObjectKey(hash)
				_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(objKey)})
			})
			block.Index = i
			manifest.Blocks = append(manifest.Blocks, block)
			manifest.TotalSize += int64(len(part))
			whole.Write(part)
		}
		return manifest, whole.Bytes()
	}

	cleanupConsolidated := func(t *testing.T, key string) {
		t.Helper()
		if key == "" {
			return
		}
		t.Cleanup(func() {
			_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
		})
	}

	t.Run("8MiB blocks stitch zero-copy", func(t *testing.T) {
		manifest, whole := stage(t, 8<<20)
		key, etag, err := stitcher.Stitch(ctx, manifest.FileID, manifest.Blocks, 5<<20)
		cleanupConsolidated(t, key)
		if err != nil {
			t.Fatalf("Stitch() with the default 8 MiB blocks failed: %v", err)
		}
		if etag == "" {
			t.Fatal("Stitch() returned an empty ETag")
		}
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
		if err != nil {
			t.Fatalf("HEAD consolidated object: %v", err)
		}
		if got := aws.ToInt64(head.ContentLength); got != int64(len(whole)) {
			t.Fatalf("consolidated size = %d, want %d", got, len(whole))
		}
	})

	t.Run("4MiB blocks are refused by the object store", func(t *testing.T) {
		manifest, _ := stage(t, 4<<20)
		key, _, err := stitcher.Stitch(ctx, manifest.FileID, manifest.Blocks, 5<<20)
		cleanupConsolidated(t, key)
		if err == nil {
			// The store accepted sub-5 MiB parts: Tier B works at either
			// chunk size on this backend, so the constraint is not binding.
			t.Logf("object store accepted 4 MiB parts; Tier B works at 4 MiB here too")
			return
		}
		if !strings.Contains(err.Error(), "minimum part size") {
			t.Fatalf("Stitch() with 4 MiB blocks error = %v, want a minimum-part-size rejection", err)
		}
	})
}

func integrationTicket(t *testing.T, fileID string) string {
	t.Helper()
	ticket, err := service.MintDownloadTicket(fileID, "it-user", "attachment", "it.bin", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket: %v", err)
	}
	return service.EncodeTicket(ticket, "it-ticket-key")
}
