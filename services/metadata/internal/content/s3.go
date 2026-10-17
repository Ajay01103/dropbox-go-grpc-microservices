package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
)

// S3Config carries the S3 endpoint settings for the content server. The key
// scheme MUST stay identical to the upload service's S3BlockGateway
// (blocks/<h0h1>/<h2h3>/<hash>) or downloads would read different bytes than
// the uploader wrote.
type S3Config struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
	// PublicEndpoint is the endpoint clients (browsers, phones) can reach, used
	// only when signing presigned GETs. In dev the service talks to the store
	// over localhost while a phone must use the machine's LAN address; the host
	// is part of the SigV4 canonical request, so it cannot be rewritten after
	// signing — the URL has to be signed against the public host instead.
	// Empty means "same as Endpoint".
	PublicEndpoint string
}

// S3Storage implements BlockFetcher (per-block ranged reads) and the
// consolidated-object presigner (Tier B URLs) against one shared client, so
// connection pooling amortizes TLS handshakes across the worker pool.
type S3Storage struct {
	client *s3.Client
	// presigner is a second client pinned to PublicEndpoint, so presigned URLs
	// carry a host the client can actually resolve. It shares the internal
	// client when no public endpoint is configured.
	presigner *s3.Client
	bucket    string
}

// NewS3Storage builds the shared S3 client. Requires an explicit endpoint
// (RustFS in dev) — no implicit AWS mode.
func NewS3Storage(cfg S3Config) (*S3Storage, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("S3 bucket is required for content storage")
	}
	if cfg.Endpoint == "" {
		return nil, errors.New("S3 endpoint is required for content storage (no implicit AWS mode)")
	}
	client, err := newS3Client(cfg.Endpoint, cfg.Region, cfg.AccessKey, cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	presigner := client
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		publicClient, publicErr := newS3Client(cfg.PublicEndpoint, cfg.Region, cfg.AccessKey, cfg.SecretKey)
		if publicErr != nil {
			return nil, fmt.Errorf("load public S3 config: %w", publicErr)
		}
		presigner = publicClient
	}
	return &S3Storage{
		client:    client,
		presigner: presigner,
		bucket:    cfg.Bucket,
	}, nil
}

// newS3Client builds a path-style client against one explicit endpoint. No
// implicit AWS mode: dev runs RustFS/MinIO, and a typo must fail loudly.
func newS3Client(endpoint, region, accessKey, secretKey string) (*s3.Client, error) {
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if accessKey != "" || secretKey != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}
	loadOptions = append(loadOptions, awsconfig.WithEndpointResolverWithOptions(
		aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{URL: endpoint, SigningRegion: region, HostnameImmutable: true}, nil
		}),
	))
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load S3 config: %w", err)
	}
	return s3.NewFromConfig(awsCfg, func(options *s3.Options) { options.UsePathStyle = true }), nil
}

func blockObjectKey(hash string) (string, error) {
	if len(hash) != sha256.Size*2 {
		return "", fmt.Errorf("invalid block hash %q", hash)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("invalid block hash: %w", err)
	}
	return fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash), nil
}

// ReadBlockRange streams [offset, offset+length) of one block object using
// the S3 Range header. The returned body is the raw S3 stream — the caller
// copies and closes it. Sub-block ranged GETs are what keeps video scrubbing
// (Range: bytes=50000000-) fast without pulling whole blocks.
func (s *S3Storage) ReadBlockRange(ctx context.Context, hash string, offset, length int64) (io.ReadCloser, error) {
	key, err := blockObjectKey(hash)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
			return nil, fmt.Errorf("block missing from storage: %s", hash)
		}
		return nil, fmt.Errorf("get block range %s: %w", hash, err)
	}
	return result.Body, nil
}

// ReadObjectRange streams [offset, offset+length) of a whole consolidated
// object (Tier B) using the S3 Range header. Inline previews read through
// here instead of a presigned URL because the embedders need the byte-range
// handshake on the preview's own origin.
func (s *S3Storage) ReadObjectRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if key == "" {
		return nil, errors.New("object key is empty")
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
			return nil, fmt.Errorf("consolidated object missing from storage: %s", key)
		}
		return nil, fmt.Errorf("get object range %s: %w", key, err)
	}
	return result.Body, nil
}

// BlockExists reports whether a block object is present with exactly the
// expected size. A commit uses it to prove the block list it is about to
// record really is readable before a version row names it: a version row
// pointing at bytes that were never written is unrecoverable, whereas a
// rejected commit can simply be retried.
//
// Size is compared, not just presence: a truncated write would otherwise pass
// and every later read of that version would fail somewhere downstream.
func (s *S3Storage) BlockExists(ctx context.Context, hash string, sizeBytes int64) (bool, error) {
	key, err := blockObjectKey(hash)
	if err != nil {
		return false, err
	}
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return false, nil
		}
		return false, fmt.Errorf("head block %s: %w", hash, err)
	}
	if sizeBytes <= 0 || result.ContentLength == nil {
		return true, nil
	}
	return *result.ContentLength == sizeBytes, nil
}

// PresignFileGET returns a short-lived presigned GET URL for a consolidated
// (Tier B) object key, carrying the response headers the browser needs.
//
// The consolidated object is a plain copy of the file's blocks, so the store
// knows neither its name nor its type, and a cross-origin URL cannot be
// overridden from the page that opened it. The signed
// response-content-disposition / response-content-type parameters are what
// make the object store answer with the same headers the content route would
// have set — without them a download opens in a tab and an image preview
// arrives as binary/octet-stream.
func (s *S3Storage) PresignFileGET(ctx context.Context, key string, request service.PresignRequest) (string, error) {
	if key == "" {
		return "", errors.New("object key is empty")
	}
	input := &s3.GetObjectInput{
		Bucket:                     aws.String(s.bucket),
		Key:                        aws.String(key),
		ResponseContentDisposition: aws.String(service.ContentDispositionHeader(request.Disposition, request.Filename)),
	}
	if request.ContentType != "" {
		input.ResponseContentType = aws.String(request.ContentType)
	}
	presigner := s3.NewPresignClient(s.presigner)
	result, err := presigner.PresignGetObject(ctx, input, func(options *s3.PresignOptions) {
		options.Expires = request.Expiry
	})
	if err != nil {
		return "", fmt.Errorf("presign object %s: %w", key, err)
	}
	return result.URL, nil
}

// DeleteConsolidated removes a file's assembled (Tier B) object. This is the
// only deletion that can free it: the block GC reaps blocks/, thumbnails live
// under their own prefix, and the consolidated object is keyed by file id
// alone, so once the registry row is gone nothing else points at it.
//
// DeleteObject is idempotent in S3 (a missing key is success), which is what a
// retried purge needs.
func (s *S3Storage) DeleteConsolidated(ctx context.Context, fileID string) error {
	if fileID == "" {
		return errors.New("file id is empty")
	}
	key := consolidatedKey(fileID)
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete consolidated object %s: %w", key, err)
	}
	return nil
}

// ErrStitchNotEligible reports that a manifest can never be assembled by S3's
// server-side copy API: a non-final block is below the multipart part minimum.
// It is a permanent property of the layout, not a transient failure, so callers
// park the file on Tier A instead of retrying it.
var ErrStitchNotEligible = errors.New("file layout cannot be consolidated server-side")

// Stitcher consolidates a file's block objects into one S3 object using
// CreateMultipartUpload + UploadPartCopy — pure server-side S3→S3 copies,
// zero bytes through this service. Multipart parts must be ≥5 MiB except the
// last, and one part is one block, so a file can only be consolidated when its
// non-final blocks clear that minimum: files written with a smaller block size
// (every legacy single-block file records its own size as its block size) stay
// on Tier A. See ErrStitchNotEligible.
type Stitcher struct {
	client *s3.Client
	bucket string
}

// NewStitcher builds the Tier B assembly worker.
func NewStitcher(cfg S3Config) (*Stitcher, error) {
	storage, err := NewS3Storage(cfg)
	if err != nil {
		return nil, err
	}
	return &Stitcher{client: storage.client, bucket: cfg.Bucket}, nil
}

// consolidatedKey: one object per file, namespaced so it can never collide
// with the blocks/ or thumbnails/ trees.
func consolidatedKey(fileID string) string {
	return "consolidated/" + fileID
}

// Stitch performs the multipart copy of every block into one object.
// blocks are the ordered manifest entries. Returns the object key and ETag.
//
// An ineligible layout (a non-final block below minPartSize) is refused before
// any upload is created and wraps ErrStitchNotEligible: no key, no part copy,
// and nothing left behind in the store.
func (s *Stitcher) Stitch(ctx context.Context, fileID string, blocks []repository.ManifestBlock, minPartSize int64) (string, string, error) {
	if len(blocks) == 0 {
		return "", "", errors.New("no blocks to stitch")
	}
	// Refuse an ineligible layout before creating the multipart upload: the
	// error is permanent (the worker parks the file on Tier A), so there is no
	// reason to leave an upload session behind for the store to reap.
	if idx := firstIneligibleBlock(blocks, minPartSize); idx >= 0 {
		return "", "", fmt.Errorf("%w: block %d (%d bytes) smaller than minimum part size %d",
			ErrStitchNotEligible, idx, blocks[idx].SizeBytes, minPartSize)
	}
	key := consolidatedKey(fileID)

	create, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", "", fmt.Errorf("create multipart upload: %w", err)
	}
	abort := func() {
		_, _ = s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: create.UploadId,
		})
	}

	parts := make([]types.CompletedPart, 0, len(blocks))
	partNumber := int32(1)
	for _, block := range blocks {
		key2, err := blockObjectKey(block.Hash)
		if err != nil {
			abort()
			return "", "", err
		}
		pcopy, err := s.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket:     aws.String(s.bucket),
			Key:        aws.String(key),
			UploadId:   create.UploadId,
			PartNumber: aws.Int32(partNumber),
			CopySource: aws.String(fmt.Sprintf("/%s/%s", s.bucket, key2)),
		})
		if err != nil {
			abort()
			return "", "", fmt.Errorf("upload part copy %d (block %s): %w", partNumber, block.Hash, err)
		}
		parts = append(parts, types.CompletedPart{
			PartNumber: aws.Int32(partNumber),
			ETag:       pcopy.CopyPartResult.ETag,
		})
		partNumber++
	}

	if _, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(key),
		UploadId:        create.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	}); err != nil {
		abort()
		return "", "", fmt.Errorf("complete multipart upload: %w", err)
	}

	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return key, "", fmt.Errorf("verify consolidated object: %w", err)
	}
	return key, aws.ToString(head.ETag), nil
}
