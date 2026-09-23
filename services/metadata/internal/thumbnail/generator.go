package thumbnail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// maxSourceBytes caps the amount of block data the worker will buffer to
// decode an image. Sources larger than this are classified permanent —
// generating (or re-generating on every redelivery) would just burn memory.
const maxSourceBytes = 50 << 20 // 50 MB

// Worker is the thumbnail storage plumbing: block reading, image decoding,
// key generation, S3/local persistence, presigning and deletion. The
// JetStream consumer (consumer.go) drives this from the files.v2.stored proto.
type Worker struct {
	storagePath string
	repo        *repository.MetadataRepo
	logger      *zap.Logger
	s3Client    *s3.Client
	s3Bucket    string
}

type S3Config struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

func New(storagePath string, s3Config S3Config, repo *repository.MetadataRepo, logger *zap.Logger) (*Worker, error) {
	// Resolve storagePath to absolute so it doesn't depend on the process CWD.
	// This is important on Windows where relative paths like "../upload/uploads"
	// resolve differently depending on which directory the service is started from.
	absPath, err := filepath.Abs(storagePath)
	if err != nil {
		return nil, fmt.Errorf("resolve thumbnail storage path %q: %w", storagePath, err)
	}
	storagePath = absPath

	worker := &Worker{
		storagePath: storagePath, repo: repo, logger: logger,
	}
	if s3Config.Endpoint != "" {
		loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(s3Config.Region)}
		if s3Config.AccessKey != "" || s3Config.SecretKey != "" {
			loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(s3Config.AccessKey, s3Config.SecretKey, ""),
			))
		}
		loadOptions = append(loadOptions, awsconfig.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: s3Config.Endpoint, SigningRegion: s3Config.Region, HostnameImmutable: true}, nil
			}),
		))
		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
		if err != nil {
			return nil, fmt.Errorf("load S3 config: %w", err)
		}
		worker.s3Client = s3.NewFromConfig(awsCfg, func(options *s3.Options) { options.UsePathStyle = true })
		worker.s3Bucket = s3Config.Bucket
	}
	return worker, nil
}

// PresignThumbnail returns a presigned GET URL for a thumbnail key.
func (w *Worker) PresignThumbnail(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if w.s3Client == nil {
		return "", errors.New("S3 thumbnail storage is not configured")
	}
	if key == "" {
		return "", errors.New("thumbnail key is empty")
	}
	presigner := s3.NewPresignClient(w.s3Client)
	result, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(w.s3Bucket),
		Key:    aws.String(key),
	}, func(options *s3.PresignOptions) { options.Expires = expiry })
	if err != nil {
		return "", fmt.Errorf("presign thumbnail: %w", err)
	}
	return result.URL, nil
}

// DeleteThumbnail removes a thumbnail object from storage. It is called by the
// purge coordinator when permanently deleting a file. The operation is
// idempotent: a missing object is treated as already deleted.
//
// SAFETY RULE: only keys under thumbnails/<fileID>/ are ever deleted. The key
// comes from the file's own row, but the prefix is re-verified here so a
// malformed row can never cause a cross-file deletion.
func (w *Worker) DeleteThumbnail(ctx context.Context, fileID, key string) error {
	if key == "" {
		return nil // nothing to delete
	}
	if fileID == "" || !strings.HasPrefix(key, "thumbnails/"+fileID+"/") {
		// Malformed row: never delete.
		return nil
	}
	if w.s3Client != nil {
		_, err := w.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(w.s3Bucket),
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
	// Local filesystem fallback.
	thumbnailPath := filepath.Join(w.storagePath, filepath.FromSlash(key))
	if err := os.Remove(thumbnailPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete local thumbnail: %w", err)
	}
	return nil
}

func (w *Worker) Close() error {
	return nil
}

// isPermanent classifies generateToKey() errors: permanent failures get
// marked failed and acked; everything else is treated as transient and
// retried.
func isPermanent(err error) bool {
	if errors.Is(err, errSourceTooLarge) || errors.Is(err, errUnsupported) || errors.Is(err, errNoBlocks) {
		return true
	}
	var decodeErr *imageDecodeError
	if errors.As(err, &decodeErr) {
		return true
	}
	return false
}

var (
	errSourceTooLarge = errors.New("image source exceeds size cap")
	errUnsupported    = errors.New("thumbnail unsupported for content type")
	errNoBlocks       = errors.New("no block hashes in object stored event")
)

// imageDecodeError wraps decode failures so they classify as permanent
// (a corrupt or non-image payload will never decode on a retry).
type imageDecodeError struct{ err error }

func (e *imageDecodeError) Error() string { return e.err.Error() }
func (e *imageDecodeError) Unwrap() error { return e.err }

// generateToKey generates a thumbnail from the given block hashes and stores
// it under the exact key provided. v2 passes a per-file key
// (thumbnails/<file_id>/<content_sha>.jpg) so a purge can delete exactly that
// file's keys without touching other files' thumbnails.
func (w *Worker) generateToKey(thumbnailKey string, blockHashes []string) (string, error) {
	exists, err := w.thumbnailExists(context.Background(), thumbnailKey)
	if err != nil {
		return "", err
	}
	if exists {
		return thumbnailKey, nil
	}
	if len(blockHashes) == 0 {
		return "", errNoBlocks
	}
	var content bytes.Buffer
	for _, hash := range blockHashes {
		if len(hash) < 4 {
			return "", fmt.Errorf("invalid block hash %q", hash)
		}
		block, err := w.readBlock(context.Background(), hash)
		if err != nil {
			return "", err
		}
		content.Write(block)
		if content.Len() > maxSourceBytes {
			return "", fmt.Errorf("%w: %d bytes", errSourceTooLarge, content.Len())
		}
	}
	source := bytes.NewReader(content.Bytes())

	img, _, err := image.Decode(source)
	if err != nil {
		return "", &imageDecodeError{err: fmt.Errorf("decode image: %w", err)}
	}
	thumbnail := fit(img, 512)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, thumbnail, &jpeg.Options{Quality: 85}); err != nil {
		return "", &imageDecodeError{err: fmt.Errorf("encode thumbnail: %w", err)}
	}
	if w.s3Client != nil {
		_, err := w.s3Client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket:        aws.String(w.s3Bucket),
			Key:           aws.String(thumbnailKey),
			Body:          bytes.NewReader(encoded.Bytes()),
			ContentType:   aws.String("image/jpeg"),
			ContentLength: aws.Int64(int64(encoded.Len())),
		})
		if err != nil {
			return "", fmt.Errorf("upload thumbnail to S3: %w", err)
		}
	} else {
		thumbnailDir := filepath.Join(w.storagePath, "thumbnails")
		if err := os.MkdirAll(thumbnailDir, 0755); err != nil {
			return "", fmt.Errorf("create thumbnail directory: %w", err)
		}
		thumbnailPath := filepath.Join(w.storagePath, filepath.FromSlash(thumbnailKey))
		if err := os.WriteFile(thumbnailPath, encoded.Bytes(), 0644); err != nil {
			return "", fmt.Errorf("write thumbnail: %w", err)
		}
	}
	return thumbnailKey, nil
}

// sha256OfBlocks hashes the ordered block-hash list (NUL-separated) — the
// content identity used by the v2 per-file key's suffix.
func sha256OfBlocks(blockHashes []string) []byte {
	hash := sha256.New()
	for _, blockHash := range blockHashes {
		_, _ = hash.Write([]byte(blockHash))
		hash.Write([]byte{0})
	}
	return hash.Sum(nil)
}

func (w *Worker) thumbnailExists(ctx context.Context, key string) (bool, error) {
	if w.s3Client != nil {
		_, err := w.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(w.s3Bucket),
			Key:    aws.String(key),
		})
		if err == nil {
			return true, nil
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return false, nil
		}
		return false, fmt.Errorf("check thumbnail in S3: %w", err)
	}
	_, err := os.Stat(filepath.Join(w.storagePath, filepath.FromSlash(key)))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("check thumbnail: %w", err)
}

// readBlock fetches a block's bytes by its hex hash. When S3 is configured
// it is the ONLY source — there is no silent local fallback, which previously
// masked real S3 errors as confusing local "path not found" failures.
func (w *Worker) readBlock(ctx context.Context, hash string) ([]byte, error) {
	if w.s3Client == nil {
		blockPath := filepath.Join(w.storagePath, "blocks", hash[:2], hash[2:4], hash)
		data, err := os.ReadFile(blockPath)
		if err != nil {
			return nil, fmt.Errorf("read block %s: %w", hash, err)
		}
		return data, nil
	}
	result, err := w.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(w.s3Bucket),
		Key:    aws.String(fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash)),
	})
	if err != nil {
		return nil, fmt.Errorf("read block %s from S3: %w", hash, err)
	}
	defer result.Body.Close()
	data, readErr := io.ReadAll(result.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read block %s body: %w", hash, readErr)
	}
	return data, nil
}

func fit(src image.Image, max int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= max && height <= max {
		return src
	}
	scale := float64(max) / float64(width)
	if height > width {
		scale = float64(max) / float64(height)
	}
	newWidth, newHeight := int(float64(width)*scale), int(float64(height)*scale)
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
