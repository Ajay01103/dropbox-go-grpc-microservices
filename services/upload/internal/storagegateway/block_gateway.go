package storagegateway

import "context"

// BlockGateway stores immutable content-addressed blocks.
// This is the only storage path for uploads.
type BlockGateway interface {
	WriteBlock(ctx context.Context, hash string, data []byte) (etag string, err error)
	ReadBlock(ctx context.Context, hash string) ([]byte, error)
	BlockExists(ctx context.Context, hash string) (bool, error)
	DeleteBlock(ctx context.Context, hash string) error
}
