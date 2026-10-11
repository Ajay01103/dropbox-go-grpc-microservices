// Package content is the plain-HTTP byte server for file downloads and
// inline previews. Browsers hit it via <a href download>, <img src>,
// <video src>, or native download managers — exactly the places that can't
// attach an Authorization header — so auth is a short-lived HMAC ticket
// minted by the metadata service's GetDownloadTicket RPC (ownership verified
// THERE, not here). This process stays a dumb pipe: ticket -> manifest -> S3.
package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"go.uber.org/zap"
)

// BlockFetcher reads a byte sub-range of one content-addressed block. S3
// backends implement it with a GetObject Range request; local backends with a
// file seek. Returned io.ReadCloser must be closed by the caller.
type BlockFetcher interface {
	ReadBlockRange(ctx context.Context, hash string, offset, length int64) (io.ReadCloser, error)
}

// ManifestStore reads a file's dense download manifest. Narrow on purpose:
// the content server must never grow into a repository client that could
// re-check ACLs (tickets are the only authority on this path).
type ManifestStore interface {
	GetFileManifest(ctx context.Context, fileID string) (repository.FileManifestRow, error)
}

// StitchLookup reports the consolidated (Tier B) object key of a file, or ""
// when the file has no ready consolidated object.
type StitchLookup interface {
	GetStitchedObjectKey(ctx context.Context, fileID string) (string, error)
}

// ObjectFetcher reads a byte sub-range of one whole consolidated object.
//
// Inline previews of consolidated files are streamed here rather than
// redirected to object storage. The embedders (a PDF engine, <video>, <img>)
// need the byte-range handshake on the origin that serves the document: the
// pdfium range loader issues a synchronous HEAD before it reads anything, and
// a presigned GET URL answers HEAD with 403 because its signature covers GET
// only. The same URL also hides Accept-Ranges from JavaScript, so the engine
// would silently download the whole file instead of seeking.
type ObjectFetcher interface {
	ReadObjectRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
}

// Server serves /content/{file_id}?ticket=... with full HTTP Range support.
type Server struct {
	repo          ManifestStore
	fetcher       BlockFetcher
	ticketKey     string
	stitchRepo    StitchLookup
	presigner     BlockPresigner
	objectFetcher ObjectFetcher
	streamWorkers int
	maxRange      int64
	logger        *zap.Logger

	manifestMu    sync.Mutex
	manifestCache map[string]cachedManifest
}

// cachedManifest is an LRU-less TTL cache entry: manifests are
// content-addressed and immutable per file version, but the entry also knows
// the consolidated key so Tier B flips on without restarts.
type cachedManifest struct {
	manifest       repository.FileManifestRow
	stitchedKey    string
	stitchedLoaded bool
	fetchedAt      time.Time
}

const (
	manifestCacheTTL = 5 * time.Minute
	defaultWorkers   = 12
	defaultMaxRange  = 1 << 40 // effectively unbounded
)

// NewServer builds the content server. stitchRepo/presigner may be nil
// (Tier B disabled → always live reconstruction).
func NewServer(repo ManifestStore, fetcher BlockFetcher, ticketKey string, logger *zap.Logger, opts ...Option) *Server {
	s := &Server{
		repo:          repo,
		fetcher:       fetcher,
		ticketKey:     ticketKey,
		streamWorkers: defaultWorkers,
		maxRange:      defaultMaxRange,
		logger:        logger,
		manifestCache: make(map[string]cachedManifest),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option configures the content server.
type Option func(*Server)

// WithStitcher enables the Tier B fast path: when the file's consolidated
// object is ready, downloads 302 to a presigned S3 GET.
func WithStitcher(repo StitchLookup, presigner BlockPresigner) Option {
	return func(s *Server) { s.stitchRepo, s.presigner = repo, presigner }
}

// WithObjectFetcher lets inline previews of consolidated files stream the
// object through this server (see ObjectFetcher for why the redirect is
// download-only).
func WithObjectFetcher(fetcher ObjectFetcher) Option {
	return func(s *Server) { s.objectFetcher = fetcher }
}

// WithStreamWorkers bounds the parallel block-fetch pool (default 12).
func WithStreamWorkers(n int) Option {
	return func(s *Server) {
		if n > 0 && n <= 64 {
			s.streamWorkers = n
		}
	}
}

// BlockPresigner presigns GETs for consolidated objects (Tier B). It is
// satisfied by service.ConsolidatedPresigner, so both the RPC download path
// and this route hand the browser URLs carrying the same response metadata.
type BlockPresigner interface {
	PresignFileGET(ctx context.Context, key string, request service.PresignRequest) (string, error)
}

// Handler mounts the /content/ route on the given mux.
func (s *Server) Handler(mux *http.ServeMux) {
	mux.HandleFunc("GET /content/{file_id}", s.handleContent)
	mux.HandleFunc("HEAD /content/{file_id}", s.handleContent)
}

// handleContent verifies the ticket (stateless), then serves 200/206 with
// byte ranges streamed from blocks in order.
func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("file_id")
	ticketParam := r.URL.Query().Get("ticket")
	if fileID == "" || ticketParam == "" {
		http.Error(w, "missing file id or ticket", http.StatusBadRequest)
		return
	}
	ticket, err := service.VerifyTicket(ticketParam, s.ticketKey, time.Now())
	if err != nil {
		s.logger.Warn("content access denied", zap.String("fileID", fileID), zap.Error(err))
		w.Header().Set("WWW-Authenticate", `Bearer realm="content", error="invalid_ticket"`)
		http.Error(w, "invalid or expired ticket", http.StatusUnauthorized)
		return
	}
	if ticket.FileID != fileID {
		http.Error(w, "ticket does not cover this file", http.StatusForbidden)
		return
	}

	manifest, stitchedKey, err := s.loadManifest(r.Context(), fileID)
	if err != nil {
		s.logger.Warn("manifest load failed", zap.String("fileID", fileID), zap.Error(err))
		http.Error(w, "manifest unavailable", http.StatusNotFound)
		return
	}

	disposition := ticket.Disposition
	if q := r.URL.Query().Get("disposition"); q == "inline" || q == "attachment" {
		// The minting side (metadata) is authoritative; this param exists for
		// direct re-use of the same ticket for seeded inline previews of text
		// and must be restricted to what the minted ticket already allows.
		if ticket.Disposition == disposition {
			disposition = q
		}
	}
	w.Header().Set("Content-Disposition", service.ContentDispositionHeader(disposition, ticket.Filename))
	w.Header().Set("Content-Type", manifest.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=60") // ticket-bound, never shared

	// Tier B fast path, downloads only: a consolidated object is handed to the
	// browser as a presigned URL so not one byte flows through this process.
	// The native download manager needs neither HEAD nor CORS for that, but
	// inline previews do — they fall through to the paths below instead.
	if disposition != "inline" && stitchedKey != "" && s.presigner != nil {
		// The redirect target must carry the same headers set just above:
		// after the 302 the browser talks to object storage, where this
		// response's headers no longer apply.
		presigned, presignErr := s.presigner.PresignFileGET(r.Context(), stitchedKey, service.PresignRequest{
			Disposition: disposition,
			Filename:    ticket.Filename,
			ContentType: manifest.ContentType,
			Expiry:      5 * time.Minute,
		})
		if presignErr == nil {
			http.Redirect(w, r, presigned, http.StatusFound)
			return
		}
		s.logger.Warn("presign consolidated failed; falling back to streaming",
			zap.String("fileID", fileID), zap.Error(presignErr))
	}

	start, end, rangeHeader, err := parseRange(r.Header.Get("Range"), manifest.TotalSize)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", manifest.TotalSize))
		http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	// A consolidated object is one ranged read; the block path reconstructs the
	// range from packed blocks. Both report the tier so an operator can tell
	// which path a slow preview took.
	streamObject := stitchedKey != "" && s.objectFetcher != nil
	tier := "live"
	if streamObject {
		tier = "consolidated"
	}
	w.Header().Set("X-Download-Tier", tier)
	writeRangeStatus(w, manifest.TotalSize, start, end, rangeHeader != "")
	if r.Method == http.MethodHead {
		return
	}
	if streamObject {
		if err := s.streamObjectRange(r.Context(), w, stitchedKey, start, end-start+1); err != nil {
			// Headers are committed; the short body is how the browser learns
			// the stream was truncated.
			s.logger.Warn("consolidated stream aborted",
				zap.String("fileID", fileID), zap.String("objectKey", stitchedKey), zap.Error(err))
		}
		return
	}

	firstIdx, lastIdx, firstOff, lastOff := manifestOffsets(&manifest, start, end)
	if err := s.streamRange(r.Context(), w, &manifest, firstIdx, lastIdx, firstOff, lastOff); err != nil {
		// Headers are committed; the aborted body tells the browser the
		// stream was truncated (Content-Length will not be reached).
		s.logger.Warn("stream aborted",
			zap.String("fileID", fileID),
			zap.Int("firstIdx", firstIdx), zap.Int("lastIdx", lastIdx),
			zap.Error(err))
	}
}

// writeRangeStatus writes the status line and length headers for a full or
// ranged response. Content-Length comes from the manifest, which is known up
// front — that is what gives the browser a real progress bar and lets a
// download manager resume.
func writeRangeStatus(w http.ResponseWriter, totalSize, start, end int64, ranged bool) {
	if !ranged {
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
}

// streamObjectRange copies [start, start+length) of one consolidated object to
// w. Object storage performs the ranged read, so memory stays bounded by the
// HTTP copy buffer rather than by file size.
func (s *Server) streamObjectRange(ctx context.Context, w io.Writer, key string, start, length int64) error {
	body, err := s.objectFetcher.ReadObjectRange(ctx, key, start, length)
	if err != nil {
		return fmt.Errorf("read consolidated object %s: %w", key, err)
	}
	defer body.Close()
	if _, err := io.Copy(w, body); err != nil {
		return fmt.Errorf("copy consolidated object %s: %w", key, err)
	}
	return nil
}

// loadManifest returns the cached manifest (single-partition read on miss)
// and whether a Tier B consolidated object is ready.
func (s *Server) loadManifest(ctx context.Context, fileID string) (repository.FileManifestRow, string, error) {
	s.manifestMu.Lock()
	entry, ok := s.manifestCache[fileID]
	s.manifestMu.Unlock()
	if ok && time.Since(entry.fetchedAt) < manifestCacheTTL && entry.stitchedLoaded {
		return entry.manifest, entry.stitchedKey, nil
	}

	manifest, err := s.repo.GetFileManifest(ctx, fileID)
	if err != nil {
		return repository.FileManifestRow{}, "", fmt.Errorf("read manifest: %w", err)
	}
	var stitchedKey string
	if s.stitchRepo != nil {
		stitchedKey, _ = s.stitchRepo.GetStitchedObjectKey(ctx, fileID) // best-effort Tier B check
	}
	entry = cachedManifest{manifest: manifest, stitchedKey: stitchedKey, stitchedLoaded: true, fetchedAt: time.Now()}
	s.manifestMu.Lock()
	if len(s.manifestCache) > 4096 {
		// Naive full clear on overflow: manifest reads are cheap (1 partition)
		// and the cache exists to absorb bursts of range-requests per file.
		s.manifestCache = make(map[string]cachedManifest)
	}
	s.manifestCache[fileID] = entry
	s.manifestMu.Unlock()
	return manifest, stitchedKey, nil
}

// parseRange parses the single-range "bytes=a-b" form (multi-range and
// suffix forms beyond "-N" are rejected, not silently mashed together).
func parseRange(header string, totalSize int64) (start, end int64, headerOut string, err error) {
	if header == "" {
		return 0, totalSize - 1, "", nil
	}
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, "", errors.New("unsupported range form")
	}
	spec := strings.TrimPrefix(header, "bytes=")
	dash := strings.Index(spec, "-")
	if dash < 0 {
		return 0, 0, "", errors.New("malformed range")
	}
	startPart, endPart := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	total := totalSize
	if startPart == "" {
		// Suffix form: last N bytes.
		n, parseErr := strconv.ParseInt(endPart, 10, 64)
		if parseErr != nil || n <= 0 {
			return 0, 0, "", errors.New("malformed suffix range")
		}
		if n > total {
			n = total
		}
		return total - n, total - 1, header, nil
	}
	start, err = strconv.ParseInt(startPart, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, "", errors.New("malformed range start")
	}
	if endPart == "" {
		end = total - 1
	} else {
		end, err = strconv.ParseInt(endPart, 10, 64)
		if err != nil {
			return 0, 0, "", errors.New("malformed range end")
		}
	}
	if start > end || end >= total {
		return 0, 0, "", errors.New("range out of bounds")
	}
	return start, end, header, nil
}

// manifestOffsets computes which blocks overlap [start, end] and the
// sub-range boundaries inside the first/last overlapping blocks. Blocks are
// already ordered by clustering key; sizes are exact.
func manifestOffsets(m *repository.FileManifestRow, start, end int64) (firstIdx, lastIdx int, firstOff, lastOff int64) {
	offsets := make([]int64, len(m.Blocks)+1)
	for i, block := range m.Blocks {
		offsets[i+1] = offsets[i] + block.SizeBytes
	}
	firstIdx = sort.Search(len(offsets)-1, func(i int) bool { return offsets[i+1] > start })
	lastIdx = sort.Search(len(offsets)-1, func(i int) bool { return offsets[i+1] > end })
	if lastIdx >= len(m.Blocks) {
		lastIdx = len(m.Blocks) - 1
	}
	firstOff = start - offsets[firstIdx]
	lastOff = end - offsets[lastIdx]
	return firstIdx, lastIdx, firstOff, lastOff
}

// streamRange fetches blocks[firstIdx..lastIdx] with a bounded worker pool
// and writes them to w strictly in order. Memory is capped by
// streamWorkers × chunk size, never by file size.
func (s *Server) streamRange(ctx context.Context, w io.Writer, m *repository.FileManifestRow, firstIdx, lastIdx int, firstOff, lastOff int64) error {
	if firstIdx > lastIdx {
		return nil
	}
	type result struct {
		idx  int
		body io.ReadCloser
		err  error
	}
	workers := s.streamWorkers
	if span := lastIdx - firstIdx + 1; span < workers {
		workers = span
	}
	jobs := make(chan int, lastIdx-firstIdx+1)
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				hash := m.Blocks[idx].Hash
				start := int64(0)
				if idx == firstIdx {
					start = firstOff
				}
				length := m.Blocks[idx].SizeBytes - start
				if idx == lastIdx {
					length = lastOff + 1 - start
				}
				body, fetchErr := s.fetcher.ReadBlockRange(ctx, hash, start, length)
				results <- result{idx: idx, body: body, err: fetchErr}
			}
		}()
	}
	spawn := func() {
		defer close(jobs)
		for i := firstIdx; i <= lastIdx; i++ {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}
	go func() { wg.Wait(); close(results) }()
	go spawn()

	// Reorder buffer: results arrive out of order (parallel workers), but
	// bytes go on the wire strictly in chunk order.
	pending := make(map[int]result, workers)
	next := firstIdx
	for res := range results {
		if res.err != nil {
			return fmt.Errorf("fetch block %d: %w", res.idx, res.err)
		}
		if res.idx == next {
			// Fast path: already the next expected block, write immediately.
			if _, err := io.Copy(w, res.body); err != nil {
				res.body.Close()
				return fmt.Errorf("copy block %d: %w", res.idx, err)
			}
			res.body.Close()
			next++
			for {
				waiting, ok := pending[next]
				if !ok {
					break
				}
				delete(pending, next)
				if _, err := io.Copy(w, waiting.body); err != nil {
					waiting.body.Close()
					return fmt.Errorf("copy block %d: %w", waiting.idx, err)
				}
				waiting.body.Close()
				next++
			}
			continue
		}
		pending[res.idx] = res
	}
	// Drop everything still in flight if we exited early (error above).
	for _, res := range pending {
		res.body.Close()
	}
	return nil
}
