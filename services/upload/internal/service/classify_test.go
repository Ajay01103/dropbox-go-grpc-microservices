package service

import (
	"context"
	"testing"

	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// TestSniffContentFamily pins the classification table: magic bytes decide
// the family, never client-supplied MIME. Detection works on file HEADS
// (first bytes of the first block), the same data sniffContentFamily fetches.
func TestSniffContentFamily(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want eventsv2.ContentFamily
	}{
		// JPEG magic: FF D8 FF
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE},
		// PNG magic
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R'}, eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE},
		// GIF
		{"gif", []byte("GIF89a...."), eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE},
		// mp4: ftyp box (ftyp at offset 4)
		{"mp4", append([]byte{0x00, 0x00, 0x00, 0x18}, []byte("ftypisom\x00\x00\x02\x00isomiso2avc1mp41")...), eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO},
		// webm: EBML header with DocType "webm" (mimetype needs the DocType
		// element to distinguish webm from other EBML containers like mkv).
		{"webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 0x01, 0x42, 0xF7, 0x81, 0x01, 0x42, 0xF2, 0x81, 0x04, 0x42, 0xF3, 0x81, 0x08, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}, eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO},
		// PDF
		{"pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), eventsv2.ContentFamily_CONTENT_FAMILY_PDF},
		// Everything else: OTHER, no preview job ever fires.
		{"text", []byte("hello world, definitely not media"), eventsv2.ContentFamily_CONTENT_FAMILY_OTHER},
		{"json", []byte(`{"key": "value"}`), eventsv2.ContentFamily_CONTENT_FAMILY_OTHER},
		{"zip", []byte{'P', 'K', 0x03, 0x04, 0x14, 0x00}, eventsv2.ContentFamily_CONTENT_FAMILY_OTHER},
		{"empty", nil, eventsv2.ContentFamily_CONTENT_FAMILY_OTHER},
	}
	for _, tc := range cases {
		if got := SniffContentFamily(tc.head); got != tc.want {
			t.Errorf("%s: SniffContentFamily = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSniffContentFamilyFailsSafe exercises the nil/empty guards: a gateway
// hiccup must yield OTHER (no preview job), never an error and never a
// wrong-family event on the bus.
func TestSniffContentFamilyFailsSafe(t *testing.T) {
	if got := sniffContentFamily(t.Context(), nil, "aabb"); got != eventsv2.ContentFamily_CONTENT_FAMILY_OTHER {
		t.Errorf("nil gateway = %v, want OTHER", got)
	}
	if got := sniffContentFamily(t.Context(), stubBlockReader{}, ""); got != eventsv2.ContentFamily_CONTENT_FAMILY_OTHER {
		t.Errorf("empty hash = %v, want OTHER", got)
	}
	if got := sniffContentFamily(t.Context(), stubBlockReader{}, "aabb"); got != eventsv2.ContentFamily_CONTENT_FAMILY_OTHER {
		t.Errorf("erroring gateway = %v, want OTHER", got)
	}
}

type stubBlockReader struct{}

func (stubBlockReader) ReadBlock(_ context.Context, _ string) ([]byte, error) {
	return nil, errStubRead
}

var errStubRead = errStub{}

type errStub struct{}

func (errStub) Error() string { return "stub read failure" }
