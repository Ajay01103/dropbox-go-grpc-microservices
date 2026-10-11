package service

import (
	"context"
	"strings"

	"github.com/gabriel-vasile/mimetype"

	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// Content classification — the trust boundary of the multi-format preview
// pipeline. The family is decided ONCE here, from the file's own bytes
// (magic-byte sniffing of its first block), never from the client-supplied
// Content-Type header or filename extension. Every downstream consumer trusts
// the stamp on the FileStored event; nobody re-sniffs.
//
// The upload path already has the first block's hash in hand at finalize
// time, so sniffing costs one S3 GET of a single small block — effectively
// free next to the hashing that just happened.

// maxSniffBytes mirrors mimetype's internal lookahead; we always have the
// whole first block in memory, so this is only a sanity bound.
const maxSniffBytes = 1 << 20 // 1 MB

// BlockReader is the minimal gateway surface the classifier needs: fetch one
// block's bytes by hex hash. *storagegateway.S3BlockGateway satisfies it.
type BlockReader interface {
	ReadBlock(ctx context.Context, hash string) ([]byte, error)
}

// SniffContentFamily maps a sniffed MIME type to a preview ContentFamily.
// Split from the S3 fetch so it is unit-testable without a gateway.
//
// mimetype detects image and PDF containers reliably; video containers it
// also covers (mp4/mov/mkv/webm/avi...). Anything without a preview worker
// — text, zip, audio, office docs — is OTHER and never fires a preview job.
func SniffContentFamily(head []byte) eventsv2.ContentFamily {
	if len(head) == 0 {
		return eventsv2.ContentFamily_CONTENT_FAMILY_OTHER
	}
	detected := mimetype.Detect(head)
	family := detected.String()
	switch {
	case strings.HasPrefix(family, "image/"):
		return eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE
	case strings.HasPrefix(family, "video/"):
		return eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO
	case detected.Is("application/pdf"):
		return eventsv2.ContentFamily_CONTENT_FAMILY_PDF
	default:
		return eventsv2.ContentFamily_CONTENT_FAMILY_OTHER
	}
}

// sniffContentFamily reads the file's FIRST block from block storage and
// magic-byte sniffs it. Any failure (missing block, gateway error) fails safe
// to OTHER: the file still uploads fine, it just gets no preview job.
func sniffContentFamily(ctx context.Context, gateway BlockReader, firstBlockHash string) eventsv2.ContentFamily {
	if gateway == nil || firstBlockHash == "" {
		return eventsv2.ContentFamily_CONTENT_FAMILY_OTHER
	}
	block, err := gateway.ReadBlock(ctx, firstBlockHash)
	if err != nil {
		// Fail safe, loud enough to debug but never blocking the upload.
		return eventsv2.ContentFamily_CONTENT_FAMILY_OTHER
	}
	if len(block) > maxSniffBytes {
		block = block[:maxSniffBytes]
	}
	return SniffContentFamily(block)
}
