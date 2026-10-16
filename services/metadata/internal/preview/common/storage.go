// Package common holds the shared plumbing of the multi-format preview
// pipeline: S3 storage access, block reading, the per-file key scheme, and
// the family-agnostic error classification. One preview worker package per
// content family (image/video/pdf) builds on top of it; only the
// "bytes in → preview JPEG out" step differs per family.
package common

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	xdraw "golang.org/x/image/draw"

	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// S3Config carries the S3 endpoint settings (RustFS in dev, S3 in prod).
type S3Config struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

// Storage is the S3-only storage surface of the preview pipeline. There is
// deliberately no local-filesystem fallback: files live in block storage
// (blocks/<h0h1>/<h2h3>/<hash>) and previews in thumbnails/<file_id>/.
type Storage struct {
	client *s3.Client
	bucket string
}

// NewStorage builds the shared S3 client. Requires an endpoint: the preview
// workers never talk to real AWS without an explicit endpoint config.
func NewStorage(cfg S3Config) (*Storage, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("S3 bucket is required for preview storage")
	}
	if cfg.Endpoint == "" {
		return nil, errors.New("S3 endpoint is required for preview storage (no implicit AWS mode)")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	loadOptions = append(loadOptions, awsconfig.WithEndpointResolverWithOptions(
		aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{URL: cfg.Endpoint, SigningRegion: cfg.Region, HostnameImmutable: true}, nil
		}),
	))
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load S3 config: %w", err)
	}
	return &Storage{
		client: s3.NewFromConfig(awsCfg, func(options *s3.Options) { options.UsePathStyle = true }),
		bucket: cfg.Bucket,
	}, nil
}

// blockObjectKey mirrors the upload service's S3BlockGateway key scheme —
// the two MUST stay identical or previews would read different bytes than
// the uploader wrote.
func blockObjectKey(hash string) (string, error) {
	if len(hash) != sha256.Size*2 {
		return "", fmt.Errorf("invalid block hash %q", hash)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("invalid block hash: %w", err)
	}
	return fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash), nil
}

// SourceFile is a reassembled block sequence materialized in a scratch file.
//
// Reassembly never holds the object in the heap: a video can be hundreds of
// megabytes, and every consumer of the reassembled bytes either wants a
// seekable path on disk (ffmpeg, pdftoppm) or a reader it can rewind (the
// image decoder's header pass). Close removes the file and its scratch
// directory, so a worker that returns on an error path still leaks nothing.
type SourceFile struct {
	*os.File
	dir string
}

// Rewind returns the read position to the start of the file. Callers need it
// after any earlier pass over the source (the image worker peeks at the header
// before decoding).
func (f *SourceFile) Rewind() error {
	if _, err := f.File.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind source file: %w", err)
	}
	return nil
}

// Close closes the file and removes the scratch directory that holds it.
func (f *SourceFile) Close() error {
	err := f.File.Close()
	if removeErr := os.RemoveAll(f.dir); err == nil {
		err = removeErr
	}
	return err
}

// ReassembleToFile streams the ordered block list into a new scratch file named
// name, enforcing a total size cap (permanent failure past it — regenerating
// would just burn memory). The returned file is positioned at the start.
//
// Each block is copied straight from its S3 response body into the file, so
// peak memory is one copy buffer rather than the whole object. The previous
// buffered form grew a bytes.Buffer to the source's size — up to 500 MB for one
// video, per in-flight job — and then wrote it to disk anyway.
func (s *Storage) ReassembleToFile(ctx context.Context, name string, blockHashes []string, maxSourceBytes int64) (*SourceFile, error) {
	if len(blockHashes) == 0 {
		return nil, ErrNoBlocks
	}
	if err := validateScratchName(name); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "preview-source-")
	if err != nil {
		return nil, fmt.Errorf("create scratch dir: %w", err)
	}
	// O_RDWR: consumers read the file back (the image worker decodes from it),
	// not just hand its path to a subprocess.
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("create scratch file: %w", err)
	}
	source := &SourceFile{File: file, dir: dir}
	if err := s.streamBlocks(ctx, blockHashes, maxSourceBytes, file); err != nil {
		_ = source.Close()
		return nil, err
	}
	if err := source.Rewind(); err != nil {
		_ = source.Close()
		return nil, err
	}
	return source, nil
}

// streamBlocks copies each block's object body into w in order, failing with
// ErrSourceTooLarge as soon as the running total would exceed maxSourceBytes.
// The cap is enforced mid-copy (see capWriter), so an oversized source is
// abandoned part-way instead of being fully transferred first.
func (s *Storage) streamBlocks(ctx context.Context, blockHashes []string, maxSourceBytes int64, w io.Writer) error {
	for _, hash := range blockHashes {
		key, err := blockObjectKey(hash)
		if err != nil {
			return err
		}
		result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("read block %s from S3: %w", hash, err)
		}
		_, copyErr := io.Copy(&capWriter{w: w, limit: maxSourceBytes}, result.Body)
		closeErr := result.Body.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return fmt.Errorf("close block %s body: %w", hash, closeErr)
		}
	}
	return nil
}

// capWriter enforces the reassembly byte cap while bytes stream through it: the
// write that would cross limit fails with ErrSourceTooLarge, which is all a
// caller needs to classify the failure as permanent.
type capWriter struct {
	w       io.Writer
	limit   int64
	written int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.written+int64(len(p)) > c.limit {
		return 0, fmt.Errorf("%w: %d bytes", ErrSourceTooLarge, c.written+int64(len(p)))
	}
	n, err := c.w.Write(p)
	c.written += int64(n)
	return n, err
}

// validateScratchName keeps a caller-supplied scratch file name inside the
// scratch directory. Names are literals today ("input", "input.pdf"); this is
// what stops a future caller from turning one into a path.
func validateScratchName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid scratch file name %q", name)
	}
	return nil
}

// WriteThumbnail stores preview bytes under the exact key (JPEG for all
// families today).
func (s *Storage) WriteThumbnail(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentType:   aws.String("image/jpeg"),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return fmt.Errorf("upload preview to S3: %w", err)
	}
	return nil
}

// ThumbnailExists reports whether a preview object is already stored.
func (s *Storage) ThumbnailExists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
		return false, nil
	}
	return false, fmt.Errorf("check preview in S3: %w", err)
}

// PresignThumbnail returns a presigned GET URL for a preview key.
func (s *Storage) PresignThumbnail(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if key == "" {
		return "", errors.New("thumbnail key is empty")
	}
	presigner := s3.NewPresignClient(s.client)
	result, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(options *s3.PresignOptions) { options.Expires = expiry })
	if err != nil {
		return "", fmt.Errorf("presign thumbnail: %w", err)
	}
	return result.URL, nil
}

// DeleteThumbnail removes a preview object from storage. It is called by the
// purge coordinator when permanently deleting a file. The operation is
// idempotent: a missing object is treated as already deleted.
//
// SAFETY RULE: only keys under thumbnails/<fileID>/ are ever deleted. The key
// comes from the file's own row, but the prefix is re-verified here so a
// malformed row can never cause a cross-file deletion.
func (s *Storage) DeleteThumbnail(ctx context.Context, fileID, key string) error {
	if key == "" {
		return nil // nothing to delete
	}
	if fileID == "" || !strings.HasPrefix(key, "thumbnails/"+fileID+"/") {
		// Malformed row: never delete.
		return nil
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return nil
		}
		return fmt.Errorf("delete thumbnail from S3: %w", err)
	}
	return nil
}

// ─── Key scheme ──────────────────────────────────────────────────────────────

// PerFileThumbnailKey builds the per-file preview key:
// thumbnails/<file_id>/<content_sha>.jpg. The suffix is the content identity
// (hash of the ordered block list), so the exists-check still dedups across
// redeliveries. A file belongs to exactly one family, so no family component
// is needed in the key; the per-file prefix is what makes purge deletes safe.
func PerFileThumbnailKey(fileID string, blockHashes []string) string {
	return fmt.Sprintf("thumbnails/%s/%s.jpg", fileID, hex.EncodeToString(Sha256OfBlocks(blockHashes)))
}

// Sha256OfBlocks hashes the ordered block-hash list (NUL-separated).
func Sha256OfBlocks(blockHashes []string) []byte {
	hash := sha256.New()
	for _, blockHash := range blockHashes {
		_, _ = hash.Write([]byte(blockHash))
		hash.Write([]byte{0})
	}
	return hash.Sum(nil)
}

// ─── Error classification (family-agnostic) ─────────────────────────────────

var (
	// ErrSourceTooLarge: the source exceeds the family's byte cap, or its
	// header declares more pixels than the pipeline will decode (see
	// CheckSourcePixels). Permanent — retrying cannot shrink the file.
	ErrSourceTooLarge = errors.New("preview source exceeds size cap")
	// ErrUnsupported: the family's worker does not handle this input at all.
	ErrUnsupported = errors.New("preview unsupported for content family")
	// ErrNoBlocks: the event carried no block hashes.
	ErrNoBlocks = errors.New("no block hashes in object stored event")
)

// ErrPermanent wraps failures that will never succeed on a retry (corrupt
// payload, unsupported container, encrypted document, missing binary input).
type ErrPermanent struct{ err error }

func (e *ErrPermanent) Error() string { return e.err.Error() }
func (e *ErrPermanent) Unwrap() error { return e.err }

// Permanent marks an error permanent for IsPermanent.
func Permanent(err error) error { return &ErrPermanent{err: err} }

// imageDecodeError is kept as its own type so legacy classification (image
// decode failures) still works without re-wrapping every call site.
type imageDecodeError struct{ err error }

func (e *imageDecodeError) Error() string { return e.err.Error() }
func (e *imageDecodeError) Unwrap() error { return e.err }

// DecodeError marks a decoder failure as permanent.
func DecodeError(err error) error { return &imageDecodeError{err: err} }

// IsPermanent classifies preview-generation errors: permanent failures get
// marked failed and acked; everything else is transient and retried on the
// family's backoff schedule. Shared across all families.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSourceTooLarge) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNoBlocks) {
		return true
	}
	var perm *ErrPermanent
	if errors.As(err, &perm) {
		return true
	}
	var decodeErr *imageDecodeError
	if errors.As(err, &decodeErr) {
		return true
	}
	return false
}

// ─── Decode guard ────────────────────────────────────────────────────────────

// maxSourcePixels caps the pixel count an image header may declare.
//
// A source byte cap does not bound decode-time memory: a few-KB PNG/JPEG header
// can declare billions of pixels, and the decoder allocates the whole pixel
// buffer before any content is validated. 100 megapixels is above every
// consumer camera, so no plausible photograph is refused, and it bounds one
// decode to ~400 MB at 4 bytes per pixel.
const maxSourcePixels = 100_000_000

// CheckSourcePixels refuses an image whose header declares more pixels than
// the pipeline will decode, reading only the header. It must run BEFORE the
// decode — the point is to decline the allocation, not to survive it. Callers
// pass the same bytes they are about to decode; the bytes are already in
// memory, so the extra header pass is cheap.
func CheckSourcePixels(r io.Reader) error {
	cfg, format, err := image.DecodeConfig(r)
	if err != nil {
		return DecodeError(fmt.Errorf("read image header: %w", err))
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return DecodeError(fmt.Errorf("image %s declares invalid dimensions %dx%d", format, cfg.Width, cfg.Height))
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxSourcePixels {
		return fmt.Errorf("%w: %s declares %d pixels (%dx%d), cap is %d",
			ErrSourceTooLarge, format, pixels, cfg.Width, cfg.Height, maxSourcePixels)
	}
	return nil
}

// ─── Shared output normalization ─────────────────────────────────────────────

// opaquer is the Opaque() half of the standard image types. image.Image does
// not expose it, but every decoder produces a type that has it.
type opaquer interface{ Opaque() bool }

// Fit scales src down so its longest edge is at most maxEdge, preserving
// aspect ratio. Sources already within bounds are returned unchanged. Every
// family runs its raw preview frame/page through this so output sizing is
// uniform.
//
// The scale is one CatmullRom resample over the whole image. A per-pixel
// nearest-neighbour loop (the previous implementation) both aliased hard on
// photographic content and paid a virtual At() call for every output pixel;
// the interpolator does the same work in a tight typed loop.
func Fit(src image.Image, maxEdge int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return src
	}
	if width <= maxEdge && height <= maxEdge {
		return src
	}
	scale := float64(maxEdge) / float64(width)
	if height > width {
		scale = float64(maxEdge) / float64(height)
	}
	newWidth := max(1, int(math.Round(float64(width)*scale)))
	newHeight := max(1, int(math.Round(float64(height)*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, xdraw.Src, nil)
	return dst
}

// FlattenOnWhite composites src over an opaque white background.
//
// JPEG has no alpha channel: jpeg.Encode treats a fully transparent pixel as
// black, so a transparent PNG/GIF preview would come out as a black rectangle.
// Opaque sources (every JPEG, and any already-flattened image) are returned
// unchanged, so this costs nothing on the common path.
func FlattenOnWhite(src image.Image) image.Image {
	if opaque, ok := src.(opaquer); ok && opaque.Opaque() {
		return src
	}
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Over)
	return dst
}

// EncodeJPEG encodes an image at the pipeline's standard quality, flattening
// any transparency onto white first (see FlattenOnWhite).
func EncodeJPEG(img image.Image) ([]byte, error) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, FlattenOnWhite(img), &jpeg.Options{Quality: 85}); err != nil {
		return nil, DecodeError(fmt.Errorf("encode thumbnail: %w", err))
	}
	return encoded.Bytes(), nil
}

// FamilyName returns a stable lowercase label for logs/metrics.
func FamilyName(f eventsv2.ContentFamily) string {
	switch f {
	case eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE:
		return "image"
	case eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO:
		return "video"
	case eventsv2.ContentFamily_CONTENT_FAMILY_PDF:
		return "pdf"
	case eventsv2.ContentFamily_CONTENT_FAMILY_OTHER:
		return "other"
	default:
		return "unspecified"
	}
}
