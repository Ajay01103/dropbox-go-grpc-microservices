//go:build integration

// Exercises the exact RPC the SPA calls on Download: ownership must be
// enforced there (the only ACL gate on the byte path), and the returned URL
// must point at the content route for Tier A or at a presigned S3 GET once
// the file has a consolidated object.
package content

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/dgraph-io/ristretto"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	"github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"github.com/Ajay01103/go-dropbox/metadata/server"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
)

func TestIntegrationDownloadTicketRPC(t *testing.T) {
	ctx, repo, session, _, _ := setupIntegration(t)
	folderRepo := repository.NewFolderRepo(session)

	cache, err := ristretto.NewCache(&ristretto.Config{NumCounters: 1e3, MaxCost: 1e5, BufferItems: 8})
	if err != nil {
		t.Fatalf("ristretto: %v", err)
	}
	defer cache.Close()

	cfg := config.Config{DownloadTicketKey: "it-rpc-key", ContentBaseURL: "http://localhost:50053"}
	svc := service.New(repo, folderRepo, nil, nil, cache, cfg, zap.NewNop())
	metadataServer := server.New(svc, zap.NewNop())

	ownerID := uuid.NewString()
	otherID := uuid.NewString()
	folderID := uuid.NewString()
	file, err := repo.CreateFileWithBlockSizes(ctx, folderID, ownerID, "movie.mp4", "video/mp4", "it-content-hash",
		[]string{"aa", "bb"}, []int64{5, 7}, 12, 0)
	if err != nil {
		t.Fatalf("CreateFileWithBlockSizes: %v", err)
	}
	// RemoveFileProjections, not DeleteFile: DeleteFile only clears the
	// files_by_folder row, so the files_by_id row would survive every run and
	// show up in the dev keyspace's file list as a stray test file.
	t.Cleanup(func() {
		_ = repo.RemoveFileProjections(context.Background(), file)
	})

	// The owner gets a ticket URL pointing at the content route, plus the
	// metadata the browser needs for the download.
	ownerCtx := context.WithValue(ctx, interceptor.ContextKeyUserID, ownerID)
	resp, err := metadataServer.GetDownloadTicket(ownerCtx, &pb.GetDownloadTicketRequest{
		FileId: file.FileID, Disposition: "attachment",
	})
	if err != nil {
		t.Fatalf("GetDownloadTicket(owner) error = %v", err)
	}
	msg := resp
	if msg.GetTicket() == "" {
		t.Fatal("GetDownloadTicket(owner) returned an empty ticket")
	}
	if !strings.Contains(msg.GetUrl(), "/content/"+file.FileID) {
		t.Fatalf("URL = %q, want it to target /content/%s", msg.GetUrl(), file.FileID)
	}
	if !strings.Contains(msg.GetUrl(), "ticket=") {
		t.Fatalf("URL = %q, want the ticket embedded", msg.GetUrl())
	}
	if msg.GetFilename() != "movie.mp4" || msg.GetContentType() != "video/mp4" || msg.GetSizeBytes() != 12 {
		t.Fatalf("ticket metadata = (%q, %q, %d), want (movie.mp4, video/mp4, 12)",
			msg.GetFilename(), msg.GetContentType(), msg.GetSizeBytes())
	}
	if msg.GetExpiresInSeconds() <= 0 {
		t.Fatalf("ExpiresInSeconds = %d, want > 0", msg.GetExpiresInSeconds())
	}

	// A different user must NOT be able to mint a ticket for this file.
	otherCtx := context.WithValue(ctx, interceptor.ContextKeyUserID, otherID)
	if _, err := metadataServer.GetDownloadTicket(otherCtx, &pb.GetDownloadTicketRequest{
		FileId: file.FileID,
	}); err == nil {
		t.Fatal("GetDownloadTicket(non-owner) succeeded, want an error")
	} else if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetDownloadTicket(non-owner) code = %v, want not_found", connect.CodeOf(err))
	}

	// Same ownership gate on the manifest RPC.
	manifestResp, err := metadataServer.GetFileManifest(ownerCtx, &pb.GetFileManifestRequest{FileId: file.FileID})
	if err != nil {
		t.Fatalf("GetFileManifest(owner) error = %v", err)
	}
	if len(manifestResp.GetBlocks()) != 2 || manifestResp.GetBlocks()[0].GetSizeBytes() != 5 {
		t.Fatalf("manifest = %d blocks, first size %d, want 2 blocks / 5 bytes",
			len(manifestResp.GetBlocks()), manifestResp.GetBlocks()[0].GetSizeBytes())
	}
	if _, err := metadataServer.GetFileManifest(otherCtx, &pb.GetFileManifestRequest{FileId: file.FileID}); err == nil {
		t.Fatal("GetFileManifest(non-owner) succeeded, want an error")
	}

	// Once the file is consolidated, the same RPC switches to the Tier B
	// presigned-URL form (no /content hop at all).
	stitchRepo := repository.NewStitchRepo(session)
	if err := stitchRepo.Enqueue(ctx, file.FileID, time.Now().UTC()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, ok, err := stitchRepo.ClaimForStitch(ctx, file.FileID, 10*time.Minute, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("ClaimForStitch = (ok=%v, err=%v)", ok, err)
	}
	if err := stitchRepo.MarkReady(ctx, file.FileID, "consolidated/"+file.FileID, "\"etag\"", 12, time.Now().UTC()); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	svc.SetStitchLookup(stitchRepo)
	presigner := &presignStub{url: "http://localhost:9000/uploads/consolidated/x?sig=1"}
	svc.SetConsolidatedPresigner(presigner)

	presignedResp, err := metadataServer.GetDownloadTicket(ownerCtx, &pb.GetDownloadTicketRequest{
		FileId: file.FileID, Disposition: "attachment",
	})
	if err != nil {
		t.Fatalf("GetDownloadTicket(owner, stitched) error = %v", err)
	}
	if got := presignedResp.GetUrl(); got != "http://localhost:9000/uploads/consolidated/x?sig=1" {
		t.Fatalf("stitched URL = %q, want the presigned URL", got)
	}
	// A presigned URL is cross-origin, so the page cannot override the
	// response: the disposition and type must travel in the URL's signed
	// response parameters or the browser opens the file instead of saving it.
	if presigner.request.Disposition != "attachment" {
		t.Fatalf("presign disposition = %q, want attachment", presigner.request.Disposition)
	}
	if presigner.request.Filename != "movie.mp4" {
		t.Fatalf("presign filename = %q, want the file's name", presigner.request.Filename)
	}
	if presigner.request.ContentType != "video/mp4" {
		t.Fatalf("presign content type = %q, want video/mp4", presigner.request.ContentType)
	}

	// A preview of the SAME consolidated file must not get the presigned URL:
	// the PDF engine probes with HEAD (403 on a GET-signed URL) and reads
	// Accept-Ranges from JavaScript, so previews stay on the content origin.
	previewResp, err := metadataServer.GetDownloadTicket(ownerCtx, &pb.GetDownloadTicketRequest{
		FileId: file.FileID, Disposition: "inline",
	})
	if err != nil {
		t.Fatalf("GetDownloadTicket(owner, inline) error = %v", err)
	}
	if !strings.Contains(previewResp.GetUrl(), "/content/"+file.FileID) {
		t.Fatalf("inline stitched URL = %q, want the content route", previewResp.GetUrl())
	}
	if !strings.Contains(previewResp.GetUrl(), "disposition=inline") {
		t.Fatalf("inline stitched URL = %q, want the inline disposition preserved", previewResp.GetUrl())
	}
}

// presignStub stands in for the S3 presigner on the metadata side. It records
// the response metadata it was handed so the RPC test can assert the download
// is dispositioned as an attachment rather than left to the object store.
type presignStub struct {
	url     string
	request service.PresignRequest
}

func (p *presignStub) PresignFileGET(_ context.Context, _ string, request service.PresignRequest) (string, error) {
	p.request = request
	return p.url, nil
}
