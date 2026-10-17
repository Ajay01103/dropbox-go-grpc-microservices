package content

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
)

// The host is part of a SigV4 canonical request, so a presigned URL can only be
// made device-reachable by signing it against the public endpoint. These tests
// pin that choice: a phone on the LAN must receive its own address, and an
// unset public endpoint must fall back to the internal one.
func TestPresignFileGETUsesPublicEndpoint(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		publicEndpoint string
		wantHost       string
	}{
		{name: "public endpoint configured", publicEndpoint: "http://192.168.1.20:9000", wantHost: "http://192.168.1.20:9000/"},
		{name: "falls back to internal endpoint", publicEndpoint: "", wantHost: "http://localhost:9000/"},
		{name: "identical endpoints are not rebuilt", publicEndpoint: "http://localhost:9000", wantHost: "http://localhost:9000/"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage, err := NewS3Storage(S3Config{
				Bucket:         "uploads",
				Region:         "us-east-1",
				Endpoint:       "http://localhost:9000",
				PublicEndpoint: tc.publicEndpoint,
				AccessKey:      "test-access-key",
				SecretKey:      "test-secret-key",
			})
			if err != nil {
				t.Fatalf("new storage: %v", err)
			}

			signed, err := storage.PresignFileGET(context.Background(), "files/abc", service.PresignRequest{
				Disposition: "attachment",
				Filename:    "report.pdf",
				ContentType: "application/pdf",
				Expiry:      time.Minute,
			})
			if err != nil {
				t.Fatalf("presign: %v", err)
			}
			if !strings.HasPrefix(signed, tc.wantHost) {
				t.Fatalf("presigned URL %q does not start with %q", signed, tc.wantHost)
			}
			if !strings.Contains(signed, "X-Amz-Signature=") {
				t.Fatalf("presigned URL %q has no signature", signed)
			}
		})
	}
}
