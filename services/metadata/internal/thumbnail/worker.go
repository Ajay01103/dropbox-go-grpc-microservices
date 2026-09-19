package thumbnail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

type ObjectStoredEvent struct {
	SchemaVersion string   `json:"schema_version"`
	FileID        string   `json:"file_id"`
	BlockHashList []string `json:"block_hash_list"`
	ContentType   string   `json:"content_type"`
	OwnerID       string   `json:"owner_id"`
}

type Worker struct {
	subject     string
	storagePath string
	repo        *repository.MetadataRepo
	logger      *zap.Logger
	conn        *nats.Conn
	sub         *nats.Subscription
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

func New(url, subject, storagePath string, s3Config S3Config, repo *repository.MetadataRepo, logger *zap.Logger) (*Worker, error) {
	// Resolve storagePath to absolute so it doesn't depend on the process CWD.
	// This is important on Windows where relative paths like "../upload/uploads"
	// resolve differently depending on which directory the service is started from.
	absPath, err := filepath.Abs(storagePath)
	if err != nil {
		return nil, fmt.Errorf("resolve thumbnail storage path %q: %w", storagePath, err)
	}
	storagePath = absPath

	conn, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	worker := &Worker{
		subject: subject, storagePath: storagePath, repo: repo, logger: logger, conn: conn,
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
			conn.Close()
			return nil, fmt.Errorf("load S3 config: %w", err)
		}
		worker.s3Client = s3.NewFromConfig(awsCfg, func(options *s3.Options) { options.UsePathStyle = true })
		worker.s3Bucket = s3Config.Bucket
	}
	return worker, nil
}

func (w *Worker) Start(ctx context.Context) error {
	js, err := w.conn.JetStream()
	if err != nil {
		return fmt.Errorf("create jetstream context: %w", err)
	}
	sub, err := js.Subscribe(w.subject, w.handle, nats.ConsumerName("metadata-thumbnail-worker"), nats.ManualAck())
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", w.subject, err)
	}
	w.sub = sub
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	return nil
}

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
func (w *Worker) DeleteThumbnail(ctx context.Context, key string) error {
	if key == "" {
		return nil // nothing to delete
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
	if w.sub != nil {
		_ = w.sub.Drain()
	}
	if w.conn != nil {
		w.conn.Close()
	}
	return nil
}

func (w *Worker) handle(msg *nats.Msg) {
	var event ObjectStoredEvent
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		w.logger.Error("invalid object stored event", zap.Error(err))
		_ = msg.Ack()
		return
	}
	if event.SchemaVersion != "v2" || len(event.BlockHashList) == 0 {
		w.logger.Debug("skipping legacy object stored event", zap.String("fileID", event.FileID), zap.String("schemaVersion", event.SchemaVersion))
		_ = msg.Ack()
		return
	}

	status := "failed"
	thumbnailKey := ""
	if event.FileID != "" && event.OwnerID != "" {
		key, err := w.generate(event)
		if err == nil {
			thumbnailKey = key
			status = "ready"
		} else {
			w.logger.Warn("thumbnail generation failed", zap.String("fileID", event.FileID), zap.Error(err))
		}
	}

	if err := w.repo.SetThumbnail(context.Background(), event.OwnerID, event.FileID, thumbnailKey, status); err != nil {
		w.logger.Error("failed to update thumbnail metadata", zap.String("fileID", event.FileID), zap.Error(err))
	}
	_ = msg.Ack()
}

func (w *Worker) generate(event ObjectStoredEvent) (string, error) {
	if !strings.HasPrefix(event.ContentType, "image/") || event.ContentType == "image/svg+xml" {
		return "", fmt.Errorf("thumbnail unsupported for content type %q", event.ContentType)
	}
	thumbnailKey := deterministicThumbnailKey(event.BlockHashList)
	exists, err := w.thumbnailExists(context.Background(), thumbnailKey)
	if err != nil {
		return "", err
	}
	if exists {
		return thumbnailKey, nil
	}
	var source io.Reader
	if len(event.BlockHashList) == 0 {
		return "", fmt.Errorf("no block hashes in object stored event")
	}
	var content bytes.Buffer
	for _, hash := range event.BlockHashList {
		if len(hash) < 4 {
			return "", fmt.Errorf("invalid block hash %q", hash)
		}
		block, err := w.readBlock(context.Background(), hash)
		if err != nil {
			return "", err
		}
		_, _ = content.Write(block)
	}
	source = bytes.NewReader(content.Bytes())

	img, _, err := image.Decode(source)
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	thumbnail := fit(img, 512)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, thumbnail, &jpeg.Options{Quality: 85}); err != nil {
		return "", fmt.Errorf("encode thumbnail: %w", err)
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

func deterministicThumbnailKey(blockHashes []string) string {
	hash := sha256.New()
	for _, blockHash := range blockHashes {
		_, _ = hash.Write([]byte(blockHash))
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("thumbnails/%x.jpg", hash.Sum(nil))
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

func (w *Worker) readBlock(ctx context.Context, hash string) ([]byte, error) {
	if w.s3Client != nil {
		result, err := w.s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(w.s3Bucket),
			Key:    aws.String(fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash)),
		})
		if err == nil {
			defer result.Body.Close()
			data, readErr := io.ReadAll(result.Body)
			if readErr == nil {
				return data, nil
			}
		}
	}
	blockPath := filepath.Join(w.storagePath, "blocks", hash[:2], hash[2:4], hash)
	data, err := os.ReadFile(blockPath)
	if err != nil {
		return nil, fmt.Errorf("read block %s: %w", hash, err)
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
