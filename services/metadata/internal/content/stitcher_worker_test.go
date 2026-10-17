package content

import (
	"errors"
	"strings"
	"testing"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// ─── Tier B eligibility ─────────────────────────────────────────────────────
//
// A file is consolidated by creating one multipart object whose parts are
// server-side copies of its blocks. S3 requires every part but the last to be
// at least 5 MiB, and a part is exactly one block, so a manifest with a
// sub-minimum non-final block can never be assembled. Detecting that before the
// multipart call is what keeps such files (notably every file whose recorded
// block size is its own original size, below 5 MiB) from being retried on every
// sweep and warning on every save.

func manifestBlock(hash string, size int64) repository.ManifestBlock {
	return repository.ManifestBlock{Hash: hash, SizeBytes: size}
}

func TestFirstIneligibleBlock(t *testing.T) {
	const min = minStitchPartSize
	cases := []struct {
		name   string
		blocks []repository.ManifestBlock
		want   int
	}{
		{name: "no blocks", blocks: nil, want: -1},
		{
			name:   "single small block is exempt as the final part",
			blocks: []repository.ManifestBlock{manifestBlock("a", 1606)},
			want:   -1,
		},
		{
			name:   "single large block qualifies",
			blocks: []repository.ManifestBlock{manifestBlock("a", min)},
			want:   -1,
		},
		{
			name:   "two full blocks qualify",
			blocks: []repository.ManifestBlock{manifestBlock("a", min), manifestBlock("b", min)},
			want:   -1,
		},
		{
			name:   "small first block of a grown legacy file",
			blocks: []repository.ManifestBlock{manifestBlock("a", 857218), manifestBlock("b", 61310)},
			want:   0,
		},
		{
			name: "small middle block",
			blocks: []repository.ManifestBlock{
				manifestBlock("a", min), manifestBlock("b", 4<<20), manifestBlock("c", 100),
			},
			want: 1,
		},
		{
			name:   "small final block is exempt",
			blocks: []repository.ManifestBlock{manifestBlock("a", min), manifestBlock("b", 1024)},
			want:   -1,
		},
		{
			name:   "just under the minimum is refused",
			blocks: []repository.ManifestBlock{manifestBlock("a", min-1), manifestBlock("b", min)},
			want:   0,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := firstIneligibleBlock(testCase.blocks, min); got != testCase.want {
				t.Fatalf("firstIneligibleBlock(%d blocks) = %d, want %d", len(testCase.blocks), got, testCase.want)
			}
		})
	}
}

// TestStitchRefusesIneligibleLayoutWithoutTouchingTheStore pins the contract
// the worker relies on: a sub-minimum non-final block is classified as
// permanent ineligibility, not as a transient S3 failure, and is rejected
// before an upload is created. The zero-value Stitcher has no client on
// purpose — reaching it would panic, which is exactly what must not happen
// for this layout.
func TestStitchRefusesIneligibleLayoutWithoutTouchingTheStore(t *testing.T) {
	blocks := []repository.ManifestBlock{manifestBlock("a", 1606), manifestBlock("b", 1024)}
	stitcher := &Stitcher{}
	key, etag, err := stitcher.Stitch(t.Context(), "file-1", blocks, minStitchPartSize)
	if err == nil {
		t.Fatalf("Stitch() of an ineligible layout = (%q, %q, nil), want an error", key, etag)
	}
	if !errors.Is(err, ErrStitchNotEligible) {
		t.Fatalf("Stitch() error = %v, want it to wrap ErrStitchNotEligible", err)
	}
	if !strings.Contains(err.Error(), "minimum part size") {
		t.Fatalf("Stitch() error = %v, want it to name the part-size minimum", err)
	}
}
