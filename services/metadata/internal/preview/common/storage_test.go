package common

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

func TestIsPermanent(t *testing.T) {
	permanent := []error{
		ErrSourceTooLarge,
		fmt.Errorf("wrapped: %w", ErrSourceTooLarge),
		ErrUnsupported,
		fmt.Errorf("%w: %q", ErrUnsupported, "image/heic"),
		ErrNoBlocks,
		&imageDecodeError{err: errors.New("image: unknown format")}, // corrupt/non-image payload
		Permanent(errors.New("ffprobe: moov atom not found")),
		fmt.Errorf("outer: %w", Permanent(errors.New("encrypted pdf"))),
	}
	for _, err := range permanent {
		if !IsPermanent(err) {
			t.Errorf("want permanent: %v", err)
		}
	}

	transient := []error{
		errors.New("s3 put: connection reset"),
		errors.New("scylla timeout"),
		&sandboxedError{timeout: true, err: errors.New("ffmpeg exceeded timeout")}, // our own kill = transient
		fmt.Errorf("ffmpeg frame extraction timed out: %w", &sandboxedError{timeout: true, err: errors.New("x")}),
		nil,
	}
	for _, err := range transient {
		if IsPermanent(err) {
			t.Errorf("want transient: %v", err)
		}
	}
}

// TestSizeCaps pins the per-family source caps from the plan (§10): image 50MB,
// video 500MB, pdf 100MB. They live in the family packages.
func TestFamilyName(t *testing.T) {
	cases := map[eventsv2.ContentFamily]string{
		eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE:       "image",
		eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO:       "video",
		eventsv2.ContentFamily_CONTENT_FAMILY_PDF:         "pdf",
		eventsv2.ContentFamily_CONTENT_FAMILY_OTHER:       "other",
		eventsv2.ContentFamily_CONTENT_FAMILY_UNSPECIFIED: "unspecified",
	}
	for f, want := range cases {
		if got := FamilyName(f); got != want {
			t.Errorf("FamilyName(%d) = %q, want %q", int32(f), got, want)
		}
	}
}

// TestPerFileThumbnailKey pins the key scheme: thumbnails/<file_id>/<sha>.jpg
// — the file's own prefix (purge can delete by prefix safely) plus a content
// hash of the block list (dedup across redeliveries still works). The key is
// family-independent: a file belongs to exactly one family.
func TestPerFileThumbnailKey(t *testing.T) {
	blocks := []string{"aabb", "ccdd"}
	k1 := PerFileThumbnailKey("file-1", blocks)
	k2 := PerFileThumbnailKey("file-2", blocks)
	k1again := PerFileThumbnailKey("file-1", blocks)

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
	// list, NUL-separated) + ".jpg".
	suffix := strings.TrimPrefix(k1, "thumbnails/file-1/")
	if want := hexOfSha256OfBlocks(blocks) + ".jpg"; suffix != want {
		t.Fatalf("suffix %q must equal content hash %q", suffix, want)
	}
}

func hexOfSha256OfBlocks(blocks []string) string {
	return fmt.Sprintf("%x", Sha256OfBlocks(blocks))
}

// TestHashesToStrings pins the raw-digest → hex conversion used at the
// consumer edge.
func TestHashesToStrings(t *testing.T) {
	in := [][]byte{{0xAB, 0xCD}, {}}
	out := HashesToStrings(in)
	if len(out) != 2 || out[0] != "abcd" || out[1] != "" {
		t.Fatalf("HashesToStrings = %v", out)
	}
}
