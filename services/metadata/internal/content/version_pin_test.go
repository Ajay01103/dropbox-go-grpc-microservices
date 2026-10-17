package content

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"go.uber.org/zap"
)

// ─── Version pinning and cache validators ───────────────────────────────────
//
// An edit session loads the bytes of one specific version from a ticket that
// names it. These tests cover the two ways that promise is kept: refusing to
// serve anything else under a pinned ticket, and validating a ranged read
// against the version hash so a range can never be stitched together from two
// versions.

func pinnedTestServer(t *testing.T, manifest repository.FileManifestRow) *http.ServeMux {
	t.Helper()
	blocks := map[string][]byte{}
	for _, block := range manifest.Blocks {
		blocks[block.Hash] = bytes.Repeat([]byte(block.Hash[:1]), int(block.SizeBytes))
	}
	server := NewServer(
		&manifestRepoStub{manifest: manifest},
		&fakeFetcher{blocks: blocks},
		"ticket-key",
		zap.NewNop(),
		WithStreamWorkers(2),
	)
	mux := http.NewServeMux()
	server.Handler(mux)
	return mux
}

func manifestWithHash(hash string) repository.FileManifestRow {
	return repository.FileManifestRow{
		FileID:      "file-1",
		TotalSize:   100,
		ContentType: "application/pdf",
		ContentHash: hash,
		Blocks:      []repository.ManifestBlock{{Index: 0, Hash: "a", SizeBytes: 100}},
	}
}

func TestVersionPinnedTicketServesOnlyThatVersion(t *testing.T) {
	mux := pinnedTestServer(t, manifestWithHash("hash-v2"))

	// The editor's ticket names the version it opened (v2 here). If the head
	// has moved on — the stored manifest now describes different bytes — the
	// route must refuse instead of handing over content the commit was not
	// based on.
	stale, err := service.MintVersionedTicket("file-1", "user-1", "inline", "doc.pdf", 1, "hash-v1", time.Now())
	if err != nil {
		t.Fatalf("MintVersionedTicket() error = %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/file-1?ticket="+service.EncodeTicket(stale, "ticket-key"), nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale pinned GET status = %d, want 409 (body %q)", rec.Code, rec.Body.String())
	}

	current, err := service.MintVersionedTicket("file-1", "user-1", "inline", "doc.pdf", 2, "hash-v2", time.Now())
	if err != nil {
		t.Fatalf("MintVersionedTicket() error = %v", err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/file-1?ticket="+service.EncodeTicket(current, "ticket-key"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("current pinned GET status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"hash-v2"` {
		t.Fatalf("ETag = %q, want the version's content hash", got)
	}
}

// TestPinnedTicketRefreshesAStaleManifestCache covers the save-then-reload
// path: the editor commits version N, refetches its session, and loads the new
// pinned ticket while the content server still holds version N-1 in its L1.
// The pinned read must re-read the store instead of answering 409 — the ticket
// is the newer fact.
func TestPinnedTicketRefreshesAStaleManifestCache(t *testing.T) {
	repo := &manifestRepoStub{manifest: manifestWithHash("hash-v1")}
	blocks := map[string][]byte{"a": bytes.Repeat([]byte("x"), 100)}
	server := NewServer(repo, &fakeFetcher{blocks: blocks}, "ticket-key", zap.NewNop(), WithStreamWorkers(1))
	mux := http.NewServeMux()
	server.Handler(mux)

	fetch := func(version int32, hash string) *httptest.ResponseRecorder {
		t.Helper()
		ticket, err := service.MintVersionedTicket("file-1", "user-1", "inline", "doc.pdf", version, hash, time.Now())
		if err != nil {
			t.Fatalf("MintVersionedTicket() error = %v", err)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/file-1?ticket="+service.EncodeTicket(ticket, "ticket-key"), nil))
		return rec
	}

	if rec := fetch(1, "hash-v1"); rec.Code != http.StatusOK {
		t.Fatalf("v1 load status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	// The commit lands: the stored manifest now describes v2 while the cache
	// still describes v1.
	repo.manifest = manifestWithHash("hash-v2")
	rec := fetch(2, "hash-v2")
	if rec.Code != http.StatusOK {
		t.Fatalf("v2 load after commit status = %d, want 200 (a stale cache entry must not refuse the newest pin; body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"hash-v2"` {
		t.Fatalf("ETag = %q, want the reloaded manifest's hash", got)
	}
}

// TestInvalidateManifestServesTheNewVersionImmediately covers the commit from
// the other side: unpinned readers (previews, downloads) must not keep seeing
// the superseded version for the rest of the cache TTL.
func TestInvalidateManifestServesTheNewVersionImmediately(t *testing.T) {
	repo := &manifestRepoStub{manifest: manifestWithHash("hash-v1")}
	blocks := map[string][]byte{"a": bytes.Repeat([]byte("x"), 100)}
	server := NewServer(repo, &fakeFetcher{blocks: blocks}, "ticket-key", zap.NewNop(), WithStreamWorkers(1))
	mux := http.NewServeMux()
	server.Handler(mux)

	ticket, err := service.MintDownloadTicket("file-1", "user-1", "inline", "doc.pdf", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	url := "/content/file-1?ticket=" + service.EncodeTicket(ticket, "ticket-key")
	fetchETag := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		return rec.Header().Get("ETag")
	}

	if got := fetchETag(); got != `"hash-v1"` {
		t.Fatalf("ETag = %q, want hash-v1", got)
	}
	repo.manifest = manifestWithHash("hash-v2")
	if got := fetchETag(); got != `"hash-v1"` {
		t.Fatalf("ETag before invalidation = %q, want the cached hash-v1", got)
	}
	server.InvalidateManifest("file-1")
	if got := fetchETag(); got != `"hash-v2"` {
		t.Fatalf("ETag after invalidation = %q, want hash-v2", got)
	}
}

func TestUnpinnedTicketStillServesWhateverIsCurrent(t *testing.T) {
	// Plain downloads are not version-pinned: they mean "the current bytes",
	// and they must keep working after an edit.
	mux := pinnedTestServer(t, manifestWithHash("hash-v2"))
	ticket, err := service.MintDownloadTicket("file-1", "user-1", "attachment", "doc.pdf", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/file-1?ticket="+service.EncodeTicket(ticket, "ticket-key"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unpinned GET status = %d, want 200", rec.Code)
	}
}

func TestConditionalRequestsUseTheVersionHash(t *testing.T) {
	mux := pinnedTestServer(t, manifestWithHash("hash-v2"))
	ticket, err := service.MintDownloadTicket("file-1", "user-1", "inline", "doc.pdf", time.Now())
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	url := "/content/file-1?ticket=" + service.EncodeTicket(ticket, "ticket-key")

	t.Run("if-none-match on the current version is not modified", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("If-None-Match", `"hash-v2"`)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Fatalf("status = %d, want 304", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("304 carried %d body bytes", rec.Body.Len())
		}
	})

	t.Run("if-none-match on another version serves the body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("If-None-Match", `"hash-v1"`)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (a changed version must send bytes)", rec.Code)
		}
	})

	t.Run("if-range with a matching validator serves the range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Range", "bytes=10-19")
		req.Header.Set("If-Range", `"hash-v2"`)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
	})

	t.Run("if-range with a stale validator sends the whole current version", func(t *testing.T) {
		// This is the case that matters: a partial copy of an older version
		// must never be completed from the new one, so the range is ignored.
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Range", "bytes=10-19")
		req.Header.Set("If-Range", `"hash-v1"`)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (the full current version)", rec.Code)
		}
		if rec.Body.Len() != 100 {
			t.Fatalf("body = %d bytes, want the whole 100 byte file", rec.Body.Len())
		}
		if got := rec.Header().Get("Content-Range"); got != "" {
			t.Fatalf("Content-Range = %q on a 200 response", got)
		}
	})
}

func TestEtagMatchesHandlesListsAndWeakValidators(t *testing.T) {
	cases := []struct {
		header string
		match  bool
	}{
		{header: `"hash-v2"`, match: true},
		{header: `"hash-v1", "hash-v2"`, match: true},
		{header: "W/\"hash-v2\"", match: true},
		{header: "*", match: true},
		{header: `"hash-v1"`, match: false},
		{header: "Wed, 21 Oct 2015 07:28:00 GMT", match: false},
	}
	for _, testCase := range cases {
		if got := etagMatches(testCase.header, `"hash-v2"`); got != testCase.match {
			t.Fatalf("etagMatches(%q) = %v, want %v", testCase.header, got, testCase.match)
		}
	}
}

func TestVersionedTicketCarriesThePinThroughEncoding(t *testing.T) {
	ticket, err := service.MintVersionedTicket("file-1", "user-1", "inline", "doc.pdf", 7, strings.Repeat("ab", 32), time.Now())
	if err != nil {
		t.Fatalf("MintVersionedTicket() error = %v", err)
	}
	decoded, err := service.VerifyTicket(service.EncodeTicket(ticket, "key"), "key", time.Now())
	if err != nil {
		t.Fatalf("VerifyTicket() error = %v", err)
	}
	if decoded.Version != 7 || decoded.ContentHash != strings.Repeat("ab", 32) {
		t.Fatalf("decoded pin = (v%d, %q), want (v7, hash)", decoded.Version, decoded.ContentHash)
	}
}
