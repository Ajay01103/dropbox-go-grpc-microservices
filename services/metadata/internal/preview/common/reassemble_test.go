package common

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// scratchPrefix is the directory name MkdirTemp uses for a reassembled source.
// The cleanup assertions look for leftovers under it.
const scratchPrefix = "preview-source-"

// testChunk is the response write granularity of the fake block store: the
// handle is flushed in pieces so a client that stops reading is visible to the
// test as a short write.
const testChunk = 64 << 10

// fakeBlockStore is a minimal path-style S3 stand-in: it answers
// GET /<bucket>/blocks/<h0h1>/<h2h3>/<hash> with the registered bytes. It
// speaks just enough of the protocol for the SDK's GetObject, and it records
// how many bytes it managed to hand over per block — which is how the tests
// prove a copy was abandoned part-way rather than read to the end.
type fakeBlockStore struct {
	*httptest.Server

	mu       sync.Mutex
	blocks   map[string][]byte
	requests map[string]int
	handed   map[string]int
	// chunkDelay paces the response so a sampler can observe the transfer
	// while it is happening. Zero makes the handle as fast as the loop allows.
	chunkDelay time.Duration
}

func newFakeBlockStore(blocks map[string][]byte) *fakeBlockStore {
	store := &fakeBlockStore{
		blocks:   blocks,
		requests: make(map[string]int),
		handed:   make(map[string]int),
	}
	store.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := path.Base(r.URL.Path)
		store.mu.Lock()
		store.requests[hash]++
		body, ok := store.blocks[hash]
		store.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// No Content-Length: the response is written in chunks so a client that
		// stops reading is visible here as a short write.
		written, err := 0, error(nil)
		for offset := 0; offset < len(body); offset += testChunk {
			end := min(offset+testChunk, len(body))
			if store.chunkDelay > 0 {
				time.Sleep(store.chunkDelay)
			}
			n, writeErr := w.Write(body[offset:end])
			written += n
			if writeErr != nil {
				err = writeErr
				break
			}
		}
		store.mu.Lock()
		store.handed[hash] = written
		store.mu.Unlock()
		if err != nil {
			return
		}
		w.(http.Flusher).Flush()
	}))
	return store
}

func (f *fakeBlockStore) close() { f.Server.Close() }

// requestCount is how many GetObject calls this block received.
func (f *fakeBlockStore) requestCount(hash string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[hash]
}

// handedOver is how many bytes of this block the server managed to send.
func (f *fakeBlockStore) handedOver(hash string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handed[hash]
}

func newTestStorage(t *testing.T, store *fakeBlockStore) *Storage {
	t.Helper()
	storage, err := NewStorage(S3Config{
		Bucket:    "preview-test",
		Region:    "us-east-1",
		Endpoint:  store.URL,
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
	})
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	return storage
}

// hashOf returns a syntactically valid (64 hex char) block hash that is unique
// per index — blockObjectKey validates the shape before any request goes out.
func hashOf(index int) string { return fmt.Sprintf("%064x", index) }

// scratchRoot points the OS temp directory at a directory of this test's own
// for the rest of the test, and returns it. Every reassembly scratch dir then
// lands inside it, so the "nothing was left behind" assertions can only ever see
// this test's runs — a sibling package's test binary runs in parallel and its
// in-flight scratch dir would otherwise look like a leak.
//
// TMPDIR is the Unix knob, TMP and TEMP the Windows ones. A platform that
// ignores them cannot support the assertion, so the test is skipped rather than
// quietly weakened.
func scratchRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	if !strings.HasPrefix(os.TempDir(), root) {
		t.Skipf("this platform ignores TMPDIR/TMP/TEMP, so scratch dirs cannot be isolated to the test")
	}
	return root
}

// scratchDirs lists the reassembly scratch directories under root, which must be
// a directory created by scratchRoot.
func scratchDirs(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read scratch root: %v", err)
	}
	dirs := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), scratchPrefix) {
			dirs[entry.Name()] = struct{}{}
		}
	}
	return dirs
}

// TestReassembleToFileCopiesEveryBlockInOrder pins the reconstruction contract:
// one block per entry of the list, in the list's order, with a repeated hash
// fetched and appended twice (the hash list is the source of truth for order,
// not the set of distinct blocks).
func TestReassembleToFileCopiesEveryBlockInOrder(t *testing.T) {
	first := []byte("first-block-")
	second := []byte("second-block")
	hashes := []string{hashOf(1), hashOf(2), hashOf(1)}
	store := newFakeBlockStore(map[string][]byte{
		hashOf(1): first,
		hashOf(2): second,
	})
	defer store.close()
	storage := newTestStorage(t, store)

	source, err := storage.ReassembleToFile(context.Background(), "input", hashes, 1<<20)
	if err != nil {
		t.Fatalf("ReassembleToFile: %v", err)
	}
	defer source.Close()

	got, err := os.ReadFile(source.Name())
	if err != nil {
		t.Fatalf("read scratch file: %v", err)
	}
	want := string(first) + string(second) + string(first)
	if string(got) != want {
		t.Fatalf("reassembled %q, want %q", got, want)
	}
	// The file is returned positioned at the start, so a caller can read it
	// straight away, and Rewind makes a second pass possible.
	if streamed, err := os.ReadFile(source.Name()); err != nil || string(streamed) != want {
		t.Fatalf("source not readable as a file: %q, %v", streamed, err)
	}
	if err := source.Rewind(); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	head := make([]byte, len(first))
	if _, err := source.Read(head); err != nil {
		t.Fatalf("read after rewind: %v", err)
	}
	if string(head) != string(first) {
		t.Fatalf("after rewind read %q, want %q", head, first)
	}

	for _, hash := range []string{hashOf(1), hashOf(2)} {
		if got := store.requestCount(hash); got < 1 {
			t.Fatalf("block %s was never fetched (%d requests)", hash, got)
		}
	}
	// Each entry in the list is its own fetch: the list has three entries.
	if got := store.requestCount(hashOf(1)); got != 2 {
		t.Fatalf("repeated block fetched %d times, want 2", got)
	}
}

// TestReassembleToFileEnforcesTheCapMidCopy is the streaming property: a source
// past the cap fails with a permanent ErrSourceTooLarge, and the oversized block
// is abandoned part-way — the whole object is never pulled into the worker.
// A buffered implementation would have read (and held) every byte before the
// check fired.
func TestReassembleToFileEnforcesTheCapMidCopy(t *testing.T) {
	const cap = 1 << 20 // 1 MB
	oversized := make([]byte, 32<<20)
	for i := range oversized {
		oversized[i] = byte(i)
	}
	hash := hashOf(7)
	store := newFakeBlockStore(map[string][]byte{hash: oversized})
	defer store.close()
	storage := newTestStorage(t, store)

	root := scratchRoot(t)
	_, err := storage.ReassembleToFile(context.Background(), "input", []string{hash}, cap)
	if err == nil {
		t.Fatal("ReassembleToFile accepted a source past the cap")
	}
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("error %v does not wrap ErrSourceTooLarge", err)
	}
	if !IsPermanent(err) {
		t.Fatalf("cap failure %v must be permanent (a retry cannot shrink the file)", err)
	}
	if handed := store.handedOver(hash); handed >= len(oversized) {
		t.Fatalf("server handed over %d of %d bytes: the block was read to the end before the cap check",
			handed, len(oversized))
	}

	assertNoScratchDirsLeft(t, root)
}

// TestReassembleToFileFailsClosed covers the remaining failure paths: a block
// that is not in storage, an empty block list, and a scratch name that would
// escape the scratch directory. None may leave a scratch directory behind.
func TestReassembleToFileFailsClosed(t *testing.T) {
	store := newFakeBlockStore(map[string][]byte{hashOf(1): []byte("block")})
	defer store.close()
	storage := newTestStorage(t, store)
	root := scratchRoot(t)

	cases := []struct {
		name   string
		file   string
		hashes []string
		want   error
	}{
		{name: "missing block", file: "input", hashes: []string{hashOf(9)}},
		{name: "empty block list", file: "input"},
		{name: "path escape", file: "../escape", hashes: []string{hashOf(1)}},
		{name: "separator in name", file: "sub/input", hashes: []string{hashOf(1)}},
		{name: "malformed hash", file: "input", hashes: []string{"not-a-hash"}},
		{name: "too few hashes", file: "input", hashes: []string{hashOf(1)[:8]}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := storage.ReassembleToFile(context.Background(), tc.file, tc.hashes, 1<<20)
			if err == nil {
				t.Fatal("expected an error")
			}
			switch {
			case tc.name == "empty block list":
				if !errors.Is(err, ErrNoBlocks) {
					t.Fatalf("error %v does not wrap ErrNoBlocks", err)
				}
			case tc.name == "path escape" || tc.name == "separator in name":
				// Rejected before any request goes out.
				if store.requestCount(hashOf(1)) != 0 {
					t.Fatal("an unsafe scratch name must be rejected before fetching blocks")
				}
			default:
				if IsPermanent(err) {
					t.Fatalf("error %v must stay transient", err)
				}
			}
			assertNoScratchDirsLeft(t, root)
		})
	}
}

// TestSourceFileCloseRemovesScratchDir pins the cleanup contract every caller
// relies on with a defer: closing the source takes its directory with it.
func TestSourceFileCloseRemovesScratchDir(t *testing.T) {
	hash := hashOf(1)
	store := newFakeBlockStore(map[string][]byte{hash: []byte("block")})
	defer store.close()
	storage := newTestStorage(t, store)
	root := scratchRoot(t)

	source, err := storage.ReassembleToFile(context.Background(), "input", []string{hash}, 1<<20)
	if err != nil {
		t.Fatalf("ReassembleToFile: %v", err)
	}
	dir := filepath.Dir(source.Name())
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("scratch dir missing while open: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir survived Close (stat err = %v)", err)
	}
	assertNoScratchDirsLeft(t, root)
}

// TestReassembleToFileKeepsMemoryBoundedByTheCopyBuffer measures the property
// the streamed reassembly exists for: a source several blocks long must not make
// the heap grow with the source.
//
// The source here is 32 MB in four 8 MB blocks (the production block size), held
// back on the wire so the sampler can watch the whole transfer. The claim under
// test is an inequality: peak heap growth must stay under ONE block, which a
// buffered reassembly cannot satisfy — it holds the entire source, so peak
// growth would be at least the source size (and the old growable bytes.Buffer
// doubled its way up, allocating about twice that on the way).
func TestReassembleToFileKeepsMemoryBoundedByTheCopyBuffer(t *testing.T) {
	const (
		blockBytes = 8 << 20
		blocks     = 4
	)
	root := scratchRoot(t)
	store := newFakeBlockStore(map[string][]byte{})
	store.chunkDelay = 2 * time.Millisecond
	defer store.close()
	storage := newTestStorage(t, store)

	hashes := make([]string, 0, blocks)
	for i := 1; i <= blocks; i++ {
		// Distinct content per block: a repeated hash would be a repeat of the
		// same object, which is a different test.
		payload := make([]byte, blockBytes)
		for j := range payload {
			payload[j] = byte(i)
		}
		hash := hashOf(i)
		store.blocks[hash] = payload
		hashes = append(hashes, hash)
	}

	stop := make(chan struct{})
	sampled := make(chan uint64, 1)
	go func() {
		var ms runtime.MemStats
		var peak uint64
		for {
			select {
			case <-stop:
				sampled <- peak
				return
			default:
			}
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak {
				peak = ms.HeapAlloc
			}
			time.Sleep(time.Millisecond)
		}
	}()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	source, err := storage.ReassembleToFile(context.Background(), "input", hashes, 1<<30)
	if err != nil {
		t.Fatalf("ReassembleToFile: %v", err)
	}
	defer source.Close()
	runtime.ReadMemStats(&after)
	close(stop)
	peak := <-sampled

	info, err := source.Stat()
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	if info.Size() != int64(blocks)*blockBytes {
		t.Fatalf("source file is %d bytes, want %d", info.Size(), int64(blocks)*blockBytes)
	}

	var peakGrowth uint64
	if peak > before.HeapAlloc {
		peakGrowth = peak - before.HeapAlloc
	}
	t.Logf("source %d MiB: peak heap growth %d KiB, total allocated %d KiB",
		info.Size()>>20, peakGrowth>>10, (after.TotalAlloc-before.TotalAlloc)>>10)

	if err := source.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if peakGrowth >= blockBytes {
		t.Fatalf("peak heap growth %d bytes for a %d byte source: the reassembly is holding source-sized data",
			peakGrowth, info.Size())
	}
	assertNoScratchDirsLeft(t, root)
}

// assertNoScratchDirsLeft fails when a run left a scratch directory in the
// test's own temp root.
func assertNoScratchDirsLeft(t *testing.T, root string) {
	t.Helper()
	for dir := range scratchDirs(t, root) {
		t.Fatalf("scratch dir %s was left behind", dir)
	}
}
