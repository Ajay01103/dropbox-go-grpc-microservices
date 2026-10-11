// Package image is the image-family preview worker: decode/resize/encode
// moved verbatim from the pre-pipeline thumbnail generator. CPU-cheap and
// sub-second, so it runs the most parallel envelope (MaxAckPending 8).
package image

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"
	_ "image/png"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// maxSourceBytes caps the amount of block data the worker will buffer to
// decode an image. Sources larger than this are classified permanent —
// generating (or re-generating on every redelivery) would just burn memory.
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
	sourceBytes, err := g.storage.ReassembleBlocks(ctx, blockHashes, maxSourceBytes)
	if err != nil {
		return "", err
	}
	source := bytes.NewReader(sourceBytes)

	img, _, err := image.Decode(source)
	if err != nil {
		return "", common.DecodeError(err)
	}
	thumbnail := common.Fit(img, 512)
	encoded, err := common.EncodeJPEG(thumbnail)
	if err != nil {
		return "", err
	}
	if err := g.storage.WriteThumbnail(ctx, thumbnailKey, encoded); err != nil {
		return "", err
	}
	return thumbnailKey, nil
}
