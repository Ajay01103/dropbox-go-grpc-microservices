// Package pdf is the PDF-family preview worker: first-page rasterization via
// pdftoppm (Poppler) as a SANDBOXED subprocess (see common/sandbox.go).
// Poppler's CLI tools do not execute embedded JavaScript by default — that
// property is part of why an external tool is chosen over an in-process PDF
// parser with a broad parsing surface.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

const (
	maxPDFSourceBytes = 100 << 20
	pdfRenderTimeout  = 15 * time.Second
)

// Generator rasterizes page 1 of a PDF to a 512px-capped JPEG preview.
type Generator struct {
	storage  *common.Storage
	logger   *zap.Logger
	pdftoppm string // resolved path
}

// newPDFGenerator fails fast if pdftoppm is missing — surface the missing
// dependency at boot, not on the first PDF upload in production.
func newPDFGenerator(storage *common.Storage, logger *zap.Logger) (*Generator, error) {
	bin, err := execLookPath("pdftoppm")
	if err != nil {
		return nil, fmt.Errorf("pdf preview worker requires pdftoppm on PATH: %w", err)
	}
	return &Generator{storage: storage, logger: logger, pdftoppm: bin}, nil
}

// Family implements Generator.
func (g *Generator) Family() eventsv2.ContentFamily {
	return eventsv2.ContentFamily_CONTENT_FAMILY_PDF
}

// GenerateToKey reassembles the PDF to a scratch file and rasterizes page 1.
// Encrypted/password-protected, zero-page and corrupt PDFs are permanent
// failures — no retry opens a locked or malformed file.
func (g *Generator) GenerateToKey(ctx context.Context, thumbnailKey string, blockHashes []string) (string, error) {
	exists, err := g.storage.ThumbnailExists(ctx, thumbnailKey)
	if err != nil {
		return "", err
	}
	if exists {
		return thumbnailKey, nil
	}

	pdfBytes, err := g.storage.ReassembleBlocks(ctx, blockHashes, maxPDFSourceBytes)
	if err != nil {
		return "", err
	}

	scratch, err := common.ScratchDir("pdf")
	if err != nil {
		return "", fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	srcPath := filepath.Join(scratch, "input.pdf")
	if err := os.WriteFile(srcPath, pdfBytes, 0600); err != nil {
		return "", fmt.Errorf("write scratch pdf: %w", err)
	}

	// -f 1 -l 1: first page only. -r 150: reasonable raster density before
	// the shared Fit() normalization. outPrefix makes pdftoppm write
	// <prefix>-1.jpg (or -01.jpg depending on version); we glob for it.
	sandbox := common.SandboxedExec{Timeout: pdfRenderTimeout, ScratchDir: scratch}
	if _, err := sandbox.Run(ctx, g.pdftoppm,
		"-jpeg",
		"-f", "1", "-l", "1",
		"-r", "150",
		"-scale-to", "1024", // raster bigger than the final 512 fit; Fit shrinks
		srcPath,
		filepath.Join(scratch, "page"),
	); err != nil {
		if common.IsTimeout(err) {
			return "", fmt.Errorf("pdftoppm rasterization timed out: %w", err)
		}
		// Non-zero exit: encrypted, corrupt, or otherwise unrenderable —
		// permanent.
		return "", common.Permanent(fmt.Errorf("pdftoppm rasterization failed: %w", err))
	}

	pagePath, err := findPage(scratch)
	if err != nil {
		// Tool exited 0 but wrote nothing: zero-page or pathological PDF.
		return "", common.Permanent(err)
	}

	page, err := os.ReadFile(pagePath)
	if err != nil {
		return "", common.Permanent(fmt.Errorf("read rasterized page: %w", err))
	}

	img, _, err := image.Decode(bytes.NewReader(page))
	if err != nil {
		return "", common.Permanent(fmt.Errorf("decode rasterized page: %w", err))
	}
	encoded, err := common.EncodeJPEG(common.Fit(img, 512))
	if err != nil {
		return "", err
	}
	if err := g.storage.WriteThumbnail(ctx, thumbnailKey, encoded); err != nil {
		return "", err
	}
	return thumbnailKey, nil
}

// findPage locates the single rasterized page pdftoppm wrote in scratch.
func findPage(scratch string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(scratch, "page*.jpg"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("pdftoppm produced no page image (empty or zero-page PDF)")
	}
	// Prefer the lowest-numbered page in case a version emitted extras.
	best := matches[0]
	for _, m := range matches[1:] {
		if m < best {
			best = m
		}
	}
	return best, nil
}
