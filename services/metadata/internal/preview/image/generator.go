// Package image is the image-family preview worker: decode/resize/encode
// moved verbatim from the pre-pipeline thumbnail generator. CPU-cheap and
// sub-second, so it runs the most parallel envelope (MaxAckPending 8).
package image

import (
	"context"
	"fmt"
	"image"
	"io"

	"github.com/disintegration/imaging"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// maxSourceBytes caps the source size the worker will accept for an image.
// Sources larger than this are classified permanent — generating (or
// re-generating on every redelivery) would just burn memory.
const maxSourceBytes = 50 << 20 // 50 MB

// Generator decodes images, resizes to 512px and stores the JPEG preview.
type Generator struct {
	storage *common.Storage
	logger  *zap.Logger
}

// Family implements Generator.
func (g *Generator) Family() eventsv2.ContentFamily {
	return eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE
}

// decodeOriented decodes an image and applies its EXIF orientation tag.
//
// Phone (and many camera) JPEGs store the sensor frame unrotated plus a
// rotation/mirror flag; ignoring it is what made portrait photos come out
// sideways. imaging registers the jpeg/png/gif (and bmp/tiff) decoders
// itself, so this call replaces the previous image.Decode + blank decoder
// imports.
func decodeOriented(r io.Reader) (image.Image, error) {
	return imaging.Decode(r, imaging.AutoOrientation(true))
}

// renderThumbnail is the family's whole "source bytes → preview JPEG" step:
// vet the declared dimensions, decode with EXIF orientation applied, flatten
// transparency onto white (JPEG has no alpha channel), fit to the shared 512px
// cap, then encode.
//
// The source must be seekable: the header pass below consumes the start of the
// stream and the decode needs it from the beginning again.
func renderThumbnail(source io.ReadSeeker) ([]byte, error) {
	// Before the decode: an upload's header decides how much the decoder
	// allocates, and the byte cap above cannot see that.
	if err := common.CheckSourcePixels(source); err != nil {
		return nil, err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind source for decode: %w", err)
	}
	img, err := decodeOriented(source)
	if err != nil {
		return nil, common.DecodeError(err)
	}
	// Flatten before resampling: averaging premultiplied transparent pixels
	// into the downsample would leave dark fringes around cut-out edges.
	return common.EncodeJPEG(common.Fit(common.FlattenOnWhite(img), 512))
}

// GenerateToKey produces a thumbnail from the given block hashes and stores
// it under the exact key provided (per-file key, see common.PerFileThumbnailKey).
func (g *Generator) GenerateToKey(ctx context.Context, thumbnailKey string, blockHashes []string) (string, error) {
	exists, err := g.storage.ThumbnailExists(ctx, thumbnailKey)
	if err != nil {
		return "", err
	}
	if exists {
		return thumbnailKey, nil
	}
	// The blocks stream to a scratch file; the decode then reads from the file
	// and the decoded image is the only copy of the picture in memory.
	source, err := g.storage.ReassembleToFile(ctx, "input", blockHashes, maxSourceBytes)
	if err != nil {
		return "", err
	}
	defer source.Close()
	encoded, err := renderThumbnail(source)
	if err != nil {
		return "", err
	}
	if err := g.storage.WriteThumbnail(ctx, thumbnailKey, encoded); err != nil {
		return "", err
	}
	return thumbnailKey, nil
}
