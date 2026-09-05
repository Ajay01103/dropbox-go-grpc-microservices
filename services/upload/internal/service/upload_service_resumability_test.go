package service

import (
	"testing"
	"time"
)

func TestParseRedisOffset(t *testing.T) {
	if got, err := parseRedisOffset("160"); err != nil || got != 160 {
		t.Fatalf("parseRedisOffset(\"160\") = (%d, %v), want (160, nil)", got, err)
	}

	if got, err := parseRedisOffset("invalid"); err == nil || got != 0 {
		t.Fatalf("parseRedisOffset(\"invalid\") = (%d, %v), want (0, error)", got, err)
	}
}

func TestBuildMetadataCreateRequest(t *testing.T) {
	session := UploadSession{
		UploadID:    "up-123",
		UserID:      "user-456",
		Filename:    "report.pdf",
		TotalSize:   1024,
		ContentType: "application/pdf",
		ContentHash: "abc123",
		Status:      "completed",
		CreatedAt:   time.Now().UTC(),
	}

	req := buildMetadataCreateRequest(session, nil)
	if req.GetFolderId() != "user-456" {
		t.Fatalf("FolderId = %q, want %q", req.GetFolderId(), "user-456")
	}
	if req.GetFilename() != "report.pdf" {
		t.Fatalf("Filename = %q, want %q", req.GetFilename(), "report.pdf")
	}
}
