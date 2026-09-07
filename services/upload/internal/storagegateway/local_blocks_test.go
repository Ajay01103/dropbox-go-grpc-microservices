package storagegateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestLocalBlockGatewayRoundTripAndDedup(t *testing.T) {
	gateway := NewLocalBlockGateway(t.TempDir())
	data := []byte("content-addressed block")
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])

	firstETag, err := gateway.WriteBlock(context.Background(), hash, data)
	if err != nil {
		t.Fatalf("WriteBlock() error = %v", err)
	}
	secondETag, err := gateway.WriteBlock(context.Background(), hash, data)
	if err != nil {
		t.Fatalf("duplicate WriteBlock() error = %v", err)
	}
	if firstETag != secondETag {
		t.Fatalf("ETags differ: %q != %q", firstETag, secondETag)
	}

	exists, err := gateway.BlockExists(context.Background(), hash)
	if err != nil || !exists {
		t.Fatalf("BlockExists() = (%t, %v), want (true, nil)", exists, err)
	}
	got, err := gateway.ReadBlock(context.Background(), hash)
	if err != nil || string(got) != string(data) {
		t.Fatalf("ReadBlock() = (%q, %v), want (%q, nil)", got, err, data)
	}
	if err := gateway.DeleteBlock(context.Background(), hash); err != nil {
		t.Fatalf("DeleteBlock() error = %v", err)
	}
}
