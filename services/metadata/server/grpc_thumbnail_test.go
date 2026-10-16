package server

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"

	"github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
)

// authenticatedCtx mimics what the auth interceptor puts on the request
// context, so the handler can be exercised without a live JWKS or database.
func authenticatedCtx() context.Context {
	return context.WithValue(context.Background(), interceptor.ContextKeyUserID, uuid.NewString())
}

// TestGetThumbnailURLsRequestGuards covers the batch handler's contract. Every
// case returns before the service touches its (nil) repositories, so this runs
// without ScyllaDB. It also pins that GetThumbnailURLs is actually served by
// MetadataServer — registration would fail to compile otherwise, but a
// handler that silently delegated to the Unimplemented stub would not.
func TestGetThumbnailURLsRequestGuards(t *testing.T) {
	srv := New(&service.MetadataService{}, nil)

	t.Run("unauthenticated", func(t *testing.T) {
		_, err := srv.GetThumbnailURLs(context.Background(), &pb.GetThumbnailURLsRequest{
			FileIds: []string{"file-1"},
		})
		assertConnectCode(t, err, connect.CodeUnauthenticated)
	})

	t.Run("empty batch", func(t *testing.T) {
		_, err := srv.GetThumbnailURLs(authenticatedCtx(), &pb.GetThumbnailURLsRequest{})
		assertConnectCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("over cap", func(t *testing.T) {
		fileIDs := make([]string, service.MaxThumbnailURLBatch+1)
		for i := range fileIDs {
			fileIDs[i] = "file-id"
		}
		_, err := srv.GetThumbnailURLs(authenticatedCtx(), &pb.GetThumbnailURLsRequest{FileIds: fileIDs})
		assertConnectCode(t, err, connect.CodeInvalidArgument)
	})
}

func assertConnectCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v, got nil error", want)
	}
	if got := connect.CodeOf(err); got != want {
		t.Fatalf("want code %v, got %v (%v)", want, got, err)
	}
}
