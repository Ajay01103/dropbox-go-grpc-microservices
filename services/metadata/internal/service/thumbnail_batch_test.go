package service

import (
	"context"
	"strings"
	"testing"
)

// TestGetThumbnailURLsBatchGuards pins the batch envelope. Both guards return
// before any ownership read or presign, so the zero-value service is enough to
// exercise them; a resolved batch is covered by the GetThumbnailURL contract
// it delegates to.
func TestGetThumbnailURLsBatchGuards(t *testing.T) {
	var svc MetadataService

	if _, err := svc.GetThumbnailURLs(context.Background(), "owner-1", nil); err == nil {
		t.Fatal("empty batch must be rejected")
	}

	overCap := make([]string, MaxThumbnailURLBatch+1)
	for i := range overCap {
		overCap[i] = "file-id"
	}
	_, err := svc.GetThumbnailURLs(context.Background(), "owner-1", overCap)
	if err == nil || !strings.Contains(err.Error(), "too many file ids") {
		t.Fatalf("over-cap batch: err = %v, want a size error", err)
	}
}
