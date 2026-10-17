package content

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"go.uber.org/zap"
)

// The preview route exists because the object store is not client-reachable:
// previews are streamed through this origin, scoped by the same HMAC ticket
// machinery as downloads. These tests pin the scope rules — a ticket must name
// the file in the path AND a key inside that file's own directory — and the
// response shape an <img> needs.
type fakeThumbnails struct {
	objects map[string][]byte
}

func (f *fakeThumbnails) OpenThumbnail(_ context.Context, key string) (io.ReadCloser, int64, string, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, 0, "", errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), "image/jpeg", nil
}

func thumbnailMux(t *testing.T) (*http.ServeMux, string, []byte) {
	t.Helper()
	key := "thumbnails/file-1/preview.jpg"
	preview := []byte("\xff\xd8preview bytes")
	server := NewServer(nil, nil, "ticket-key", zap.NewNop(), WithThumbnails(&fakeThumbnails{
		objects: map[string][]byte{key: preview},
	}))
	mux := http.NewServeMux()
	server.Handler(mux)
	return mux, key, preview
}

func thumbnailTicket(t *testing.T, fileID, key string, now time.Time) string {
	t.Helper()
	ticket, err := service.MintThumbnailTicket(fileID, "user-1", key, now)
	if err != nil {
		t.Fatalf("MintThumbnailTicket() error = %v", err)
	}
	return service.EncodeTicket(ticket, "ticket-key")
}

func TestThumbnailRouteServesPreview(t *testing.T) {
	mux, key, preview := thumbnailMux(t)
	encoded := thumbnailTicket(t, "file-1", key, time.Now())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thumbnails/file-1?ticket="+url.QueryEscape(encoded), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), preview) {
		t.Fatal("body did not match the stored preview")
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", got)
	}
	if got := rec.Header().Get("Cache-Control"); got == "" {
		t.Fatal("Cache-Control missing: preview URLs are reused from client caches")
	}
	if got := rec.Header().Get("Content-Length"); got == "" {
		t.Fatal("Content-Length missing")
	}

	// A matching validator must answer 304 without a body: an image list
	// re-reads these on every focus.
	head := httptest.NewRequest(http.MethodGet, "/thumbnails/file-1?ticket="+url.QueryEscape(encoded), nil)
	head.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, head)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("conditional GET = %d with %d bytes, want 304 with 0", rec.Code, rec.Body.Len())
	}

	head = httptest.NewRequest(http.MethodHead, "/thumbnails/file-1?ticket="+url.QueryEscape(encoded), nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, head)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d with %d bytes, want 200 with 0", rec.Code, rec.Body.Len())
	}
}

func TestThumbnailRouteRefusesUnscopedTickets(t *testing.T) {
	mux, key, _ := thumbnailMux(t)
	now := time.Now()

	downloadTicket, err := service.MintDownloadTicket("file-1", "user-1", "attachment", "a.jpg", now)
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}

	tests := []struct {
		name   string
		target string
		want   int
	}{
		{
			name:   "missing ticket",
			target: "/thumbnails/file-1",
			want:   http.StatusBadRequest,
		},
		{
			name:   "ticket whose key belongs to another file",
			target: "/thumbnails/file-2?ticket=" + url.QueryEscape(thumbnailTicket(t, "file-2", key, now)),
			want:   http.StatusUnauthorized,
		},
		{
			name:   "path does not match the ticket's file",
			target: "/thumbnails/file-3?ticket=" + url.QueryEscape(thumbnailTicket(t, "file-1", key, now)),
			want:   http.StatusForbidden,
		},
		{
			name:   "download ticket is not a preview ticket",
			target: "/thumbnails/file-1?ticket=" + url.QueryEscape(service.EncodeTicket(downloadTicket, "ticket-key")),
			want:   http.StatusForbidden,
		},
		{
			name:   "expired ticket",
			target: "/thumbnails/file-1?ticket=" + url.QueryEscape(thumbnailTicket(t, "file-1", key, now.Add(-2*time.Hour))),
			want:   http.StatusUnauthorized,
		},
		{
			name:   "unknown preview object",
			target: "/thumbnails/file-3?ticket=" + url.QueryEscape(thumbnailTicket(t, "file-3", "thumbnails/file-3/gone.jpg", now)),
			want:   http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

// A preview ticket names an object key; VerifyTicket refuses one that does not
// live under the ticket's own file, so a minting bug upstream cannot hand a
// caller another user's preview.
func TestVerifyTicketRejectsForeignThumbnailKey(t *testing.T) {
	now := time.Now()
	ticket, err := service.MintThumbnailTicket("file-1", "user-1", "thumbnails/file-2/preview.jpg", now)
	if err != nil {
		t.Fatalf("MintThumbnailTicket() error = %v", err)
	}
	if _, err := service.VerifyTicket(service.EncodeTicket(ticket, "ticket-key"), "ticket-key", now); err == nil {
		t.Fatal("VerifyTicket() accepted a thumbnail key outside the ticket's file")
	}
}
