package storagegateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeUploadVerifiesFinalFileAndRemovesChunks(t *testing.T) {
	basePath := t.TempDir()
	gateway := NewLocalFileSystemGateway(basePath)
	firstChunk := []byte("hello ")
	secondChunk := []byte("world")

	writeChunk := func(offset int64, data []byte) {
		hash := sha256.Sum256(data)
		if err := gateway.WriteChunk(context.Background(), "upload-1", offset, data, hex.EncodeToString(hash[:])); err != nil {
			t.Fatalf("write chunk at %d: %v", offset, err)
		}
	}
	writeChunk(0, firstChunk)
	writeChunk(int64(len(firstChunk)), secondChunk)

	storageKey, err := gateway.FinalizeUpload(context.Background(), "upload-1", int64(len(firstChunk)+len(secondChunk)))
	if err != nil {
		t.Fatalf("finalize upload: %v", err)
	}
	if storageKey != "uploads/upload-1/final.bin" {
		t.Fatalf("unexpected storage key: %s", storageKey)
	}

	uploadDir := filepath.Join(basePath, "upload-1")
	finalData, err := os.ReadFile(filepath.Join(uploadDir, "final.bin"))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(finalData) != "hello world" {
		t.Fatalf("unexpected final file contents: %q", finalData)
	}

	for _, chunkName := range []string{"0.chunk", "6.chunk"} {
		if _, err := os.Stat(filepath.Join(uploadDir, chunkName)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat error: %v", chunkName, err)
		}
	}
}
