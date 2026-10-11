// Package video is the video-family preview worker: ffmpeg/ffprobe run as
// SANDBOXED SUBPROCESSES (see common/sandbox.go) so a decode failure or a
// hostile container crashes a disposable child, never the worker. Heavy
// (CPU/memory, seconds per job), so its envelope is the most conservative
// (MaxAckPending 2, AckWait 5m, steeper backoff).
package video

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

const (
	// maxVideoSourceBytes: videos are bigger; separate cap from image's 50MB.
	maxVideoSourceBytes = 500 << 20
	ffmpegTimeout       = 20 * time.Second
	ffprobeTimeout      = 8 * time.Second
	// frameSeekFraction: grab a frame 10% into the video — skips the black
	// opening frames many containers start with.
	frameSeekFraction = 0.10
)

// Generator extracts a single frame via ffmpeg and stores it as the JPEG
// preview, normalized through the shared Fit/EncodeJPEG helpers.
type Generator struct {
	storage *common.Storage
	logger  *zap.Logger
	ffmpeg  string // resolved path
	ffprobe string // resolved path
}

// newVideoGenerator builds the video generator, failing fast if the external
// binaries are missing — a missing dependency must surface at startup, not on
// the first video upload in production.
func newVideoGenerator(storage *common.Storage, logger *zap.Logger) (*Generator, error) {
	ffmpeg, err := execLookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("video preview worker requires ffmpeg on PATH: %w", err)
	}
	ffprobe, err := execLookPath("ffprobe")
	if err != nil {
		return nil, fmt.Errorf("video preview worker requires ffprobe on PATH: %w", err)
	}
	return &Generator{storage: storage, logger: logger, ffmpeg: ffmpeg, ffprobe: ffprobe}, nil
}

// Family implements Generator.
func (g *Generator) Family() eventsv2.ContentFamily {
	return eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO
}

// GenerateToKey reassembles the video to a seekable scratch file, probes its
// duration, extracts one frame at frameSeekFraction, and stores the resized
// JPEG. Scratch is removed on every exit path.
func (g *Generator) GenerateToKey(ctx context.Context, thumbnailKey string, blockHashes []string) (string, error) {
	exists, err := g.storage.ThumbnailExists(ctx, thumbnailKey)
	if err != nil {
		return "", err
	}
	if exists {
		return thumbnailKey, nil
	}

	videoBytes, err := g.storage.ReassembleBlocks(ctx, blockHashes, maxVideoSourceBytes)
	if err != nil {
		return "", err
	}

	scratch, err := common.ScratchDir("video")
	if err != nil {
		return "", fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	srcPath := filepath.Join(scratch, "input")
	outPath := filepath.Join(scratch, "frame.jpg")
	if err := os.WriteFile(srcPath, videoBytes, 0600); err != nil {
		return "", fmt.Errorf("write scratch video: %w", err)
	}

	// 1. Probe duration. Non-zero exit / no duration = corrupt or unsupported
	// container → permanent (a retry cannot fix the bytes).
	duration, err := g.probeDuration(ctx, srcPath)
	if err != nil {
		return "", common.Permanent(fmt.Errorf("probe video: %w", err))
	}

	// 2. Extract one frame. A sandbox timeout is transient (resource blip);
	// a non-zero ffmpeg exit with real bytes is permanent.
	seek := strconv.FormatFloat(duration*frameSeekFraction, 'f', 3, 64)
	sandbox := common.SandboxedExec{Timeout: ffmpegTimeout, ScratchDir: scratch}
	if _, err := sandbox.Run(ctx, g.ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-ss", seek,
		"-i", srcPath,
		"-frames:v", "1",
		"-q:v", "3",
		"-y", outPath,
	); err != nil {
		if common.IsTimeout(err) {
			return "", fmt.Errorf("ffmpeg frame extraction timed out: %w", err)
		}
		return "", common.Permanent(fmt.Errorf("ffmpeg frame extraction failed: %w", err))
	}

	frame, err := os.ReadFile(outPath)
	if err != nil {
		return "", common.Permanent(fmt.Errorf("read extracted frame: %w", err))
	}

	// 3. Normalize: decode + shared fit so output sizing/format is uniform
	// across families.
	img, _, err := image.Decode(bytes.NewReader(frame))
	if err != nil {
		return "", common.Permanent(fmt.Errorf("decode extracted frame: %w", err))
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

// probeDuration runs ffprobe and parses format=duration (seconds).
func (g *Generator) probeDuration(ctx context.Context, srcPath string) (float64, error) {
	sandbox := common.SandboxedExec{Timeout: ffprobeTimeout}
	out, err := sandbox.Run(ctx, g.ffprobe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		srcPath,
	)
	if err != nil {
		return 0, err
	}
	d, parseErr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if parseErr != nil || d <= 0 {
		return 0, fmt.Errorf("ffprobe returned no usable duration (%q)", strings.TrimSpace(string(out)))
	}
	return d, nil
}
