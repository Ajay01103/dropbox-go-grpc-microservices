package content

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"go.uber.org/zap"
)

func TestParseRange(t *testing.T) {
	const total = 1000
	tests := []struct {
		name       string
		header     string
		wantStart  int64
		wantEnd    int64
		wantHeader string
		wantErr    bool
	}{
		{name: "no header means whole file", header: "", wantStart: 0, wantEnd: total - 1},
		{name: "closed range", header: "bytes=100-199", wantStart: 100, wantEnd: 199, wantHeader: "bytes=100-199"},
		{name: "open ended", header: "bytes=900-", wantStart: 900, wantEnd: total - 1, wantHeader: "bytes=900-"},
		{name: "suffix", header: "bytes=-100", wantStart: 900, wantEnd: total - 1, wantHeader: "bytes=-100"},
		{name: "suffix larger than file clamps", header: "bytes=-5000", wantStart: 0, wantEnd: total - 1, wantHeader: "bytes=-5000"},
		{name: "start past end is invalid", header: "bytes=500-100", wantErr: true},
		{name: "end beyond size is invalid", header: "bytes=0-2000", wantErr: true},
		{name: "multi-range unsupported", header: "bytes=0-1,5-6", wantErr: true},
		{name: "garbage", header: "items=0-10", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, gotHeader, err := parseRange(tt.header, total)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRange(%q) error = nil, want error", tt.header)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRange(%q) error = %v", tt.header, err)
			}
			if start != tt.wantStart || end != tt.wantEnd {
				t.Fatalf("parseRange(%q) = (%d, %d), want (%d, %d)", tt.header, start, end, tt.wantStart, tt.wantEnd)
			}
			if gotHeader != tt.wantHeader {
				t.Fatalf("parseRange(%q) headerOut = %q, want %q", tt.header, gotHeader, tt.wantHeader)
			}
		})
	}
}

func manifestWith(sizes ...int64) repository.FileManifestRow {
	blocks := make([]repository.ManifestBlock, len(sizes))
	var total int64
	for i, size := range sizes {
		blocks[i] = repository.ManifestBlock{Index: i, Hash: fmt.Sprintf("%064d", i), SizeBytes: size}
		total += size
	}
	return repository.FileManifestRow{FileID: "f", TotalSize: total, ContentType: "application/octet-stream", Blocks: blocks}
}

func TestManifestOffsets(t *testing.T) {
	// Three blocks of 100 bytes: offsets [0,100) [100,200) [200,300).
	manifest := manifestWith(100, 100, 100)
	tests := []struct {
		name              string
		start, end        int64
		firstIdx, lastIdx int
		firstOff, lastOff int64
	}{
		{name: "whole file", start: 0, end: 299, firstIdx: 0, lastIdx: 2, firstOff: 0, lastOff: 99},
		{name: "single block", start: 100, end: 199, firstIdx: 1, lastIdx: 1, firstOff: 0, lastOff: 99},
		{name: "tail of first block", start: 40, end: 99, firstIdx: 0, lastIdx: 0, firstOff: 40, lastOff: 99},
		{name: "head of middle block", start: 100, end: 140, firstIdx: 1, lastIdx: 1, firstOff: 0, lastOff: 40},
		{name: "spans two blocks mid-file", start: 50, end: 150, firstIdx: 0, lastIdx: 1, firstOff: 50, lastOff: 50},
		{name: "starts at exact boundary", start: 200, end: 299, firstIdx: 2, lastIdx: 2, firstOff: 0, lastOff: 99},
		{name: "video scrub to the tail", start: 250, end: 299, firstIdx: 2, lastIdx: 2, firstOff: 50, lastOff: 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			firstIdx, lastIdx, firstOff, lastOff := manifestOffsets(&manifest, tt.start, tt.end)
			if firstIdx != tt.firstIdx || lastIdx != tt.lastIdx || firstOff != tt.firstOff || lastOff != tt.lastOff {
				t.Fatalf("manifestOffsets(%d, %d) = (%d, %d, %d, %d), want (%d, %d, %d, %d)",
					tt.start, tt.end, firstIdx, lastIdx, firstOff, lastOff, tt.firstIdx, tt.lastIdx, tt.firstOff, tt.lastOff)
			}
		})
	}
}

func TestManifestOffsetsUnevenLastBlock(t *testing.T) {
	// 4 MiB blocks with a short tail — the shape the upload service produces.
	manifest := manifestWith(4<<20, 4<<20, 12345)
	start, end := int64(4<<20), int64((4<<20)+(4<<20)+12344)
	firstIdx, lastIdx, firstOff, lastOff := manifestOffsets(&manifest, start, end)
	if firstIdx != 1 || lastIdx != 2 || firstOff != 0 || lastOff != 12344 {
		t.Fatalf("manifestOffsets = (%d, %d, %d, %d), want (1, 2, 0, 12344)", firstIdx, lastIdx, firstOff, lastOff)
	}
}

// fakeFetcher serves deterministic block bytes and records the ranges asked
// for, so the ordered, sub-range behaviour of streamRange is observable.
type fakeFetcher struct {
	blocks map[string][]byte

	mu     sync.Mutex
	ranges []string
}

func (f *fakeFetcher) ReadBlockRange(_ context.Context, hash string, offset, length int64) (io.ReadCloser, error) {
	f.mu.Lock()
	f.ranges = append(f.ranges, fmt.Sprintf("%s:%d+%d", hash, offset, length))
	f.mu.Unlock()

	data, ok := f.blocks[hash]
	if !ok {
		return nil, fmt.Errorf("unknown block %s", hash)
	}
	if offset < 0 || offset+length > int64(len(data)) {
		return nil, fmt.Errorf("range out of bounds for %s", hash)
	}
	return io.NopCloser(bytes.NewReader(data[offset : offset+length])), nil
}

func TestStreamRangeWritesBytesInOrder(t *testing.T) {
	// Blocks contain their own hash so ordering is verifiable from the output.
	blocks := map[string][]byte{}
	manifest := repository.FileManifestRow{FileID: "f"}
	var expected bytes.Buffer
	for i := 0; i < 8; i++ {
		hash := fmt.Sprintf("%064d", i)
		content := bytes.Repeat([]byte{byte('a' + i)}, 50)
		blocks[hash] = content
		manifest.Blocks = append(manifest.Blocks, repository.ManifestBlock{Index: i, Hash: hash, SizeBytes: int64(len(content))})
		manifest.TotalSize += int64(len(content))
		expected.Write(content)
	}

	server := NewServer(nil, &fakeFetcher{blocks: blocks}, "key", zap.NewNop(), WithStreamWorkers(4))
	var got bytes.Buffer
	if err := server.streamRange(context.Background(), &got, &manifest, 0, len(manifest.Blocks)-1, 0, 49); err != nil {
		t.Fatalf("streamRange() error = %v", err)
	}
	if !bytes.Equal(got.Bytes(), expected.Bytes()) {
		t.Fatalf("streamRange() wrote %d bytes out of order", got.Len())
	}
}

func TestStreamRangeSubBlockRequests(t *testing.T) {
	blocks := map[string][]byte{
		"a": bytes.Repeat([]byte("a"), 100),
		"b": bytes.Repeat([]byte("b"), 100),
		"c": bytes.Repeat([]byte("c"), 100),
	}
	manifest := repository.FileManifestRow{
		FileID:    "f",
		TotalSize: 300,
		Blocks: []repository.ManifestBlock{
			{Index: 0, Hash: "a", SizeBytes: 100},
			{Index: 1, Hash: "b", SizeBytes: 100},
			{Index: 2, Hash: "c", SizeBytes: 100},
		},
	}
	fetcher := &fakeFetcher{blocks: blocks}
	server := NewServer(nil, fetcher, "key", zap.NewNop(), WithStreamWorkers(2))

	// Range 50-150 only needs the tail of block 0 and the head of block 1.
	var got bytes.Buffer
	if err := server.streamRange(context.Background(), &got, &manifest, 0, 1, 50, 50); err != nil {
		t.Fatalf("streamRange() error = %v", err)
	}
	want := append(bytes.Repeat([]byte("a"), 50), bytes.Repeat([]byte("b"), 51)...)
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("streamRange() = %q, want %q", got.Bytes(), want)
	}

	sort.Strings(fetcher.ranges)
	gotRanges := strings.Join(fetcher.ranges, ",")
	wantRanges := "a:50+50,b:0+51"
	if gotRanges != wantRanges {
		t.Fatalf("S3 range requests = %s, want %s (full blocks must not be pulled)", gotRanges, wantRanges)
	}
}

func TestContentHandlerServesRangesWithTicket(t *testing.T) {
	content := bytes.Repeat([]byte("z"), 300)
	blocks := map[string][]byte{
		"a": content[0:100],
		"b": content[100:200],
		"c": content[200:300],
	}
	manifest := repository.FileManifestRow{
		FileID: "file-1", TotalSize: 300, ContentType: "application/octet-stream",
		Blocks: []repository.ManifestBlock{
			{Index: 0, Hash: "a", SizeBytes: 100},
			{Index: 1, Hash: "b", SizeBytes: 100},
			{Index: 2, Hash: "c", SizeBytes: 100},
		},
	}
	repo := &manifestRepoStub{manifest: manifest}
	server := NewServer(repo, &fakeFetcher{blocks: blocks}, "ticket-key", zap.NewNop(), WithStreamWorkers(3))
	mux := http.NewServeMux()
	server.Handler(mux)

	ticket, err := service.MintDownloadTicket("file-1", "user-1", "attachment", "thing.bin", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	url := "/content/file-1?ticket=" + service.EncodeTicket(ticket, "ticket-key")

	// Full content.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("full GET status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatal("full GET body did not match the assembled file")
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "300" {
		t.Fatalf("Content-Length = %q, want 300", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("Content-Disposition = %q, want attachment", got)
	}

	// Range request.
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=90-210")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range GET status = %d, want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), content[90:211]) {
		t.Fatalf("range GET body length = %d, want 121", rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 90-210/300" {
		t.Fatalf("Content-Range = %q, want bytes 90-210/300", got)
	}

	// HEAD returns headers without a body (download managers probe first).
	head := httptest.NewRequest(http.MethodHead, url, nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, head)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = status %d with %d body bytes, want 200 with 0", rec.Code, rec.Body.Len())
	}
}

func TestContentHandlerRejectsBadTickets(t *testing.T) {
	manifest := repository.FileManifestRow{FileID: "file-1", TotalSize: 10, ContentType: "text/plain"}
	manifest.Blocks = []repository.ManifestBlock{{Index: 0, Hash: "a", SizeBytes: 10}}
	server := NewServer(&manifestRepoStub{manifest: manifest}, &fakeFetcher{blocks: map[string][]byte{"a": []byte("0123456789")}}, "ticket-key", zap.NewNop())
	mux := http.NewServeMux()
	server.Handler(mux)

	valid, err := service.MintDownloadTicket("file-1", "user-1", "attachment", "a.txt", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	otherFile, err := service.MintDownloadTicket("file-2", "user-1", "attachment", "b.txt", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}

	tests := []struct {
		name     string
		query    string
		wantCode int
	}{
		{name: "missing ticket", query: "", wantCode: http.StatusBadRequest},
		{name: "garbage ticket", query: "?ticket=nope", wantCode: http.StatusUnauthorized},
		{name: "wrong signing key", query: "?ticket=" + service.EncodeTicket(valid, "other-key"), wantCode: http.StatusUnauthorized},
		{name: "ticket for another file", query: "?ticket=" + service.EncodeTicket(otherFile, "ticket-key"), wantCode: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/file-1"+tt.query, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}

// stitchRepoStub reports a consolidated object for one file, mirroring what
// StitchRepo does once the background stitcher has finished.
type stitchRepoStub struct {
	key string
}

func (s *stitchRepoStub) GetStitchedObjectKey(context.Context, string) (string, error) {
	return s.key, nil
}

// objectFetcherStub serves bytes out of a consolidated object and records the
// ranges asked for, so the object path is observable (including how a ranged
// read is sliced, and that a sub-range never pulls the whole object).
type objectFetcherStub struct {
	object []byte

	mu     sync.Mutex
	ranges []string
}

func (o *objectFetcherStub) ReadObjectRange(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	o.mu.Lock()
	o.ranges = append(o.ranges, fmt.Sprintf("%s:%d+%d", key, offset, length))
	o.mu.Unlock()
	if offset < 0 || offset+length > int64(len(o.object)) {
		return nil, fmt.Errorf("range out of bounds for %s", key)
	}
	return io.NopCloser(bytes.NewReader(o.object[offset : offset+length])), nil
}

// presignerStub hands back a fixed URL and records the request it was asked
// to sign, so a test can tell a redirect apart from a streamed response.
type presignerStub struct {
	url     string
	request service.PresignRequest
}

func (p *presignerStub) PresignFileGET(_ context.Context, _ string, request service.PresignRequest) (string, error) {
	p.request = request
	return p.url, nil
}

// Inline previews must be served from this origin even when the file has a
// consolidated object. A presigned S3 URL answers HEAD with 403 (its signature
// covers GET) and hides Accept-Ranges from JavaScript, and the PDF engine
// probes with HEAD before reading a single byte — so a redirect here would
// break the viewer rather than speed it up.
func TestContentHandlerStreamsConsolidatedObjectForInlinePreview(t *testing.T) {
	content := bytes.Repeat([]byte("consolidated"), 100)
	manifest := repository.FileManifestRow{
		FileID: "file-1", TotalSize: int64(len(content)), ContentType: "application/pdf",
		Blocks: []repository.ManifestBlock{{Index: 0, Hash: "a", SizeBytes: int64(len(content))}},
	}
	object := &objectFetcherStub{object: content}
	presigner := &presignerStub{url: "http://storage.example/consolidated/file-1?sig=1"}
	server := NewServer(&manifestRepoStub{manifest: manifest}, &fakeFetcher{blocks: map[string][]byte{"a": content}},
		"ticket-key", zap.NewNop(),
		WithStitcher(&stitchRepoStub{key: "consolidated/file-1"}, presigner),
		WithObjectFetcher(object))
	mux := http.NewServeMux()
	server.Handler(mux)

	ticket, err := service.MintDownloadTicket("file-1", "user-1", "inline", "report.pdf", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	url := "/content/file-1?ticket=" + service.EncodeTicket(ticket, "ticket-key")

	// Full preview: streamed, not redirected.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a redirect would be %d) body %q", rec.Code, http.StatusFound, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatal("streamed object bytes did not match the consolidated object")
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes (the viewer's range probe reads this)", got)
	}
	if got := rec.Header().Get("X-Download-Tier"); got != "consolidated" {
		t.Fatalf("X-Download-Tier = %q, want consolidated", got)
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "inline") {
		t.Fatalf("Content-Disposition = %q, want inline", disposition)
	}

	// HEAD is what the PDF engine calls first; it must answer, not 403.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, url, nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = status %d with %d body bytes, want 200 with 0", rec.Code, rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Length"); got != fmt.Sprint(len(content)) {
		t.Fatalf("HEAD Content-Length = %q, want %d", got, len(content))
	}
	if len(object.ranges) != 1 {
		t.Fatalf("object reads after HEAD = %v, want just the earlier full read (HEAD reads no bytes)", object.ranges)
	}

	// Ranged preview pulls only the requested slice of the object.
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=10-19")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), content[10:20]) {
		t.Fatalf("range body = %q, want %q", rec.Body.Bytes(), content[10:20])
	}
	if got := rec.Header().Get("Content-Range"); got != fmt.Sprintf("bytes 10-19/%d", len(content)) {
		t.Fatalf("Content-Range = %q", got)
	}
	if len(object.ranges) != 2 || object.ranges[1] != "consolidated/file-1:10+10" {
		t.Fatalf("object reads = %v, want the range read to ask for 10+10 only", object.ranges)
	}
}

// The same consolidated file is still redirected for a download: the browser's
// native download manager wants no CORS and no byte-range handshake, so it goes
// straight to object storage.
func TestContentHandlerRedirectsConsolidatedDownload(t *testing.T) {
	content := bytes.Repeat([]byte("consolidated"), 100)
	manifest := repository.FileManifestRow{
		FileID: "file-1", TotalSize: int64(len(content)), ContentType: "application/pdf",
		Blocks: []repository.ManifestBlock{{Index: 0, Hash: "a", SizeBytes: int64(len(content))}},
	}
	object := &objectFetcherStub{object: content}
	presigner := &presignerStub{url: "http://storage.example/consolidated/file-1?sig=1"}
	server := NewServer(&manifestRepoStub{manifest: manifest}, &fakeFetcher{blocks: map[string][]byte{"a": content}},
		"ticket-key", zap.NewNop(),
		WithStitcher(&stitchRepoStub{key: "consolidated/file-1"}, presigner),
		WithObjectFetcher(object))
	mux := http.NewServeMux()
	server.Handler(mux)

	ticket, err := service.MintDownloadTicket("file-1", "user-1", "attachment", "report.pdf", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/file-1?ticket="+service.EncodeTicket(ticket, "ticket-key"), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != presigner.url {
		t.Fatalf("Location = %q, want %q", got, presigner.url)
	}
	if presigner.request.Disposition != "attachment" {
		t.Fatalf("presign disposition = %q, want attachment", presigner.request.Disposition)
	}
	// The response metadata has to travel in the signature: after the 302 the
	// browser talks to object storage, where this server's headers are gone and
	// the <a download> attribute is cross-origin and therefore ignored. A field
	// dropped here opens the download in a tab, unnamed and mistyped.
	if presigner.request.Filename != "report.pdf" {
		t.Fatalf("presign filename = %q, want the ticket's filename", presigner.request.Filename)
	}
	if presigner.request.ContentType != "application/pdf" {
		t.Fatalf("presign content type = %q, want the manifest's content type", presigner.request.ContentType)
	}
	if len(object.ranges) != 0 {
		t.Fatalf("object reads = %v, want none (a download must not flow through this server)", object.ranges)
	}
}

// manifestRepoStub stands in for the manifest store; the content server must
// only ever read one partition per file.
type manifestRepoStub struct {
	manifest repository.FileManifestRow
	err      error
}

func (m *manifestRepoStub) GetFileManifest(context.Context, string) (repository.FileManifestRow, error) {
	return m.manifest, m.err
}
