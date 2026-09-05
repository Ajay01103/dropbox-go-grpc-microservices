package storagegateway

import "context"

// BlockGateway stores immutable content-addressed blocks.
// The legacy StorageGateway remains available while uploads are dual-written.
type BlockGateway interface {
	WriteBlock(ctx context.Context, hash string, data []byte) (etag string, err error)
	ReadBlock(ctx context.Context, hash string) ([]byte, error)
	BlockExists(ctx context.Context, hash string) (bool, error)
	DeleteBlock(ctx context.Context, hash string) error
}