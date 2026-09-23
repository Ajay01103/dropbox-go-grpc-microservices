package thumbnail

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsPermanent(t *testing.T) {
	permanent := []error{
		errSourceTooLarge,
		fmt.Errorf("wrapped: %w", errSourceTooLarge),
		errUnsupported,
		fmt.Errorf("%w: %q", errUnsupported, "image/heic"),
		errNoBlocks,
		&imageDecodeError{err: errors.New("image: unknown format")}, // corrupt/non-image payload
	}
	for _, err := range permanent {
		if !isPermanent(err) {
			t.Errorf("want permanent: %v", err)
		}
	}

	transient := []error{
		errors.New("s3 put: connection reset"),
		errors.New("scylla timeout"),
		nil,
	}
	for _, err := range transient {
		if isPermanent(err) {
			t.Errorf("want transient: %v", err)
		}
	}
}

// TestSizeCapConstant pins the 50 MB source cap decision from the B1 plan.
func TestSizeCapConstant(t *testing.T) {
	if maxSourceBytes != 50<<20 {
		t.Fatalf("maxSourceBytes = %d, want %d", maxSourceBytes, 50<<20)
	}
}

// ---- per-file keys ----------------------------------------------------------

// TestPerFileThumbnailKey pins the key scheme: thumbnails/<file_id>/<sha>.jpg
// — the file's own prefix (purge can delete by prefix safely) plus a content
// hash of the block list (dedup across redeliveries still works).
func TestPerFileThumbnailKey(t *testing.T) {
	blocks := []string{"aabb", "ccdd"}
	k1 := perFileThumbnailKey("file-1", blocks)
	k2 := perFileThumbnailKey("file-2", blocks)
	k1again := perFileThumbnailKey("file-1", blocks)

	if !strings.HasPrefix(k1, "thumbnails/file-1/") || !strings.HasPrefix(k2, "thumbnails/file-2/") {
		t.Fatalf("keys must live under the file's own prefix: %q / %q", k1, k2)
	}
	if k1 != k1again {
		t.Fatalf("key must be deterministic: %q vs %q", k1, k1again)
	}
	if k1 == k2 {
		t.Fatal("different files must not share a key even with identical blocks")
	}
	// The suffix is the content identity: hex(sha256 of the ordered block
	// list, NUL-separated).
	want := hex.EncodeToString(sha256OfBlocks(blocks)) + ".jpg"
	if got := strings.TrimPrefix(k1, "thumbnails/file-1/"); got != want {
		t.Fatalf("suffix %q must equal content hash %q", got, want)
	}
}
