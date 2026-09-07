package storagegateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3BlockGateway struct {
	client *s3.Client
	bucket string
	keyFn  func(string) (string, error)
}

type S3BlockGatewayConfig struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

func NewS3BlockGateway(ctx context.Context, cfg S3BlockGatewayConfig) (*S3BlockGateway, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("S3 bucket is required")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	if cfg.Endpoint != "" {
		loadOptions = append(loadOptions, awsconfig.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: cfg.Endpoint, SigningRegion: cfg.Region, HostnameImmutable: true}, nil
			}),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &S3BlockGateway{
		client: s3.NewFromConfig(awsCfg, func(options *s3.Options) { options.UsePathStyle = true }),
		bucket: cfg.Bucket,
		keyFn:  blockObjectKey,
	}, nil
}

func blockObjectKey(hash string) (string, error) {
	if len(hash) != sha256.Size*2 {
		return "", errors.New("invalid block hash")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("invalid block hash: %w", err)
	}
	return fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash), nil
}

func (g *S3BlockGateway) objectKey(hash string) (string, error) {
	if g == nil || g.client == nil {
		return "", errors.New("S3 block gateway is not initialized")
	}
	return g.keyFn(hash)
}

func (g *S3BlockGateway) WriteBlock(ctx context.Context, hash string, data []byte) (string, error) {
	key, err := g.objectKey(hash)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != hash {
		return "", errors.New("block hash mismatch")
	}
	if exists, err := g.BlockExists(ctx, hash); err != nil {
		return "", err
	} else if exists {
		return hash, nil
	}
	_, err = g.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(g.bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return "", fmt.Errorf("put block: %w", err)
	}
	return hash, nil
}

func (g *S3BlockGateway) ReadBlock(ctx context.Context, hash string) ([]byte, error) {
	key, err := g.objectKey(hash)
	if err != nil {
		return nil, err
	}
	result, err := g.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get block: %w", err)
	}
	defer result.Body.Close()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(result.Body); err != nil {
		return nil, fmt.Errorf("read block: %w", err)
	}
	return buffer.Bytes(), nil
}

func (g *S3BlockGateway) BlockExists(ctx context.Context, hash string) (bool, error) {
	key, err := g.objectKey(hash)
	if err != nil {
		return false, err
	}
	_, err = g.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return false, nil
		}
		return false, fmt.Errorf("head block: %w", err)
	}
	return true, nil
}

func (g *S3BlockGateway) DeleteBlock(ctx context.Context, hash string) error {
	key, err := g.objectKey(hash)
	if err != nil {
		return err
	}
	_, err = g.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("delete block: %w", err)
	}
	return nil
}

func (g *S3BlockGateway) PresignBlockGET(ctx context.Context, hash string, expiry time.Duration) (string, error) {
	key, err := g.objectKey(hash)
	if err != nil {
		return "", err
	}
	presigner := s3.NewPresignClient(g.client)
	result, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)}, func(options *s3.PresignOptions) {
		options.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("presign block: %w", err)
	}
	return result.URL, nil
}
