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
	"image/jpeg"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

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

// ReadBlock fetches a block's bytes by its hex hash.
func (s *Storage) ReadBlock(ctx context.Context, hash string) ([]byte, error) {
	key, err := blockObjectKey(hash)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("read block %s from S3: %w", hash, err)
	}
	defer result.Body.Close()
	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("read block %s body: %w", hash, err)
	}
	return data, nil
}

// ReassembleBlocks concatenates the ordered block list, enforcing a total
// size cap (permanent failure past it — regenerating would just burn memory).
func (s *Storage) ReassembleBlocks(ctx context.Context, blockHashes []string, maxSourceBytes int64) ([]byte, error) {
	if len(blockHashes) == 0 {
		return nil, ErrNoBlocks
	}
	var content bytes.Buffer
	for _, hash := range blockHashes {
		if len(hash) < 4 {
			return nil, fmt.Errorf("invalid block hash %q", hash)
		}
		block, err := s.ReadBlock(ctx, hash)
		if err != nil {
			return nil, err
		}
		content.Write(block)
		if int64(content.Len()) > maxSourceBytes {
			return nil, fmt.Errorf("%w: %d bytes", ErrSourceTooLarge, content.Len())
		}
	}
	return content.Bytes(), nil
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
	// ErrSourceTooLarge: the reassembled source exceeds the family's cap.
	// Permanent — retrying cannot shrink the file.
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

// ─── Shared output normalization ─────────────────────────────────────────────

// Fit scales src down so its longest edge is at most max, preserving aspect
// ratio. Sources already within bounds are returned unchanged. Every family
// runs its raw preview frame/page through this so output sizing is uniform.
func Fit(src image.Image, max int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return src
	}
	if width <= max && height <= max {
		return src
	}
	scale := float64(max) / float64(width)
	if height > width {
		scale = float64(max) / float64(height)
	}
	newWidth, newHeight := int(float64(width)*scale), int(float64(height)*scale)
	if newWidth < 1 {
		newWidth = 1
	}
	if newHeight < 1 {
		newHeight = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	for y := 0; y < newHeight; y++ {
		for x := 0; x < newWidth; x++ {
			sx := bounds.Min.X + x*width/newWidth
			sy := bounds.Min.Y + y*height/newHeight
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// EncodeJPEG encodes an image at the pipeline's standard quality.
func EncodeJPEG(img image.Image) ([]byte, error) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 85}); err != nil {
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
