package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	"github.com/Ajay01103/go-dropbox/upload/gen/pb"
	"github.com/Ajay01103/go-dropbox/upload/gen/pb/pbconnect"
)

// blockSize is the configured upload block size: one UploadChunk message.
const blockSize = 8 << 20

// chunkRecorder is a minimal UploadServiceHandler that only records how many
// bytes the transport let through, so the test measures the message-size cap
// rather than any service logic.
type chunkRecorder struct {
	pbconnect.UnimplementedUploadServiceHandler
	received []byte
}

func (h *chunkRecorder) UploadChunk(_ context.Context, req *pb.UploadChunkRequest) (*pb.UploadChunkAck, error) {
	h.received = req.GetData()
	return &pb.UploadChunkAck{UploadId: req.GetUploadId(), OffsetPersisted: int64(len(req.GetData())), IsFinal: true}, nil
}

// uploadChunkRoundTrip mounts srv the way mount describes, then sends one
// full-size block. It is the regression test for the bug the browser hit:
// connect caps inbound messages at 4 MiB by default, so mounting without a
// cap sized off the block size rejected every full chunk with
// "resource_exhausted: message size … is larger than configured max 4194304"
// before the service ever saw the bytes.
func uploadChunkRoundTrip(t *testing.T, mount func(*http.ServeMux, *connect.Server)) error {
	t.Helper()

	handler := &chunkRecorder{}
	connectSrv := connect.NewServer()
	pbconnect.RegisterUploadServiceHandler(connectSrv, handler)

	mux := http.NewServeMux()
	mount(mux, connectSrv)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	client := pbconnect.NewUploadServiceClient(connect.NewClient(
		connecthttp.NewTransport(http.DefaultClient, httpServer.URL, connecthttp.WithReadMaxBytes(0)),
	))
	req := &pb.UploadChunkRequest{
		UploadId:      "upload-1",
		Offset:        0,
		Data:          make([]byte, blockSize),
		Sha256OfChunk: strings.Repeat("a", 64),
		ChunkIndex:    0,
	}
	// The request really does exceed connect's default cap; without this the
	// "default mount rejects it" subtest could pass for the wrong reason.
	if size := proto.Size(req); size <= connectDefaultReadMaxBytes {
		t.Fatalf("request serializes to %d bytes, want > %d to exercise the cap", size, connectDefaultReadMaxBytes)
	}

	resp, err := client.UploadChunk(t.Context(), req)
	if err != nil {
		return err
	}
	if got := len(handler.received); got != blockSize {
		t.Fatalf("handler received %d bytes, want %d", got, blockSize)
	}
	if resp.GetOffsetPersisted() != int64(blockSize) {
		t.Fatalf("OffsetPersisted = %d, want %d", resp.GetOffsetPersisted(), blockSize)
	}
	return nil
}

func TestMountAcceptsFullSizeChunk(t *testing.T) {
	// The service's own mount must carry one whole block plus its envelope.
	if err := uploadChunkRoundTrip(t, func(mux *http.ServeMux, srv *connect.Server) {
		Mount(mux, srv, blockSize)
	}); err != nil {
		t.Fatalf("full-size chunk rejected by the service mount: %v", err)
	}
}

func TestDefaultMountRejectsFullSizeChunk(t *testing.T) {
	// Pins the failure mode Mount fixes: mounting without a cap sized off the
	// block size is exactly what produced resource_exhausted in the browser.
	err := uploadChunkRoundTrip(t, func(mux *http.ServeMux, srv *connect.Server) {
		connecthttp.Mount(mux, srv)
	})
	if err == nil {
		t.Fatal("default mount accepted a full-size chunk; the regression is no longer reproducible")
	}
	if code := connect.CodeOf(err); code != connect.CodeResourceExhausted {
		t.Fatalf("error code = %v, want %v (%v)", code, connect.CodeResourceExhausted, err)
	}
	if !strings.Contains(err.Error(), "larger than configured max") {
		t.Fatalf("error = %v, want the message-size cap error", err)
	}
}

func TestReadMaxBytesTracksBlockSize(t *testing.T) {
	tests := []struct {
		name           string
		chunkSizeBytes int64
		want           int
	}{
		{name: "block size leaves room for the envelope", chunkSizeBytes: blockSize, want: blockSize + chunkEnvelopeSlack},
		{name: "missing chunk size falls back to connect's default", chunkSizeBytes: 0, want: connectDefaultReadMaxBytes},
		{name: "tiny chunk size never shrinks the cap", chunkSizeBytes: 1, want: connectDefaultReadMaxBytes},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ReadMaxBytes(test.chunkSizeBytes); got != test.want {
				t.Fatalf("ReadMaxBytes(%d) = %d, want %d", test.chunkSizeBytes, got, test.want)
			}
		})
	}
}
