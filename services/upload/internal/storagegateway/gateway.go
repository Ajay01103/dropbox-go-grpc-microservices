package storagegateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// StorageGateway defines the interface for persisting and retrieving file chunks
type StorageGateway interface {
	// WriteChunk persists a chunk to storage
	// Returns error if checksum doesn't match or write fails
	WriteChunk(ctx context.Context, uploadID string, offset int64, data []byte, expectedSha256 string) error

	// ReadChunk retrieves a chunk from storage
	ReadChunk(ctx context.Context, uploadID string, offset int64) ([]byte, error)

	// FinalizeUpload is called after all chunks are received
	// Verifies all chunks exist and returns the final object storage key
	FinalizeUpload(ctx context.Context, uploadID string, totalSize int64) (string, error)

	// AbortUpload cleans up storage for an abandoned upload
	AbortUpload(ctx context.Context, uploadID string) error
}

// LocalFileSystemGateway implements StorageGateway using local filesystem
// For Build Order 1-3, this is sufficient; later extract to distributed storage
type LocalFileSystemGateway struct {
	basePath string
}

// NewLocalFileSystemGateway creates a new local filesystem storage gateway
func NewLocalFileSystemGateway(basePath string) *LocalFileSystemGateway {
	return &LocalFileSystemGateway{basePath: basePath}
}

// WriteChunk writes a chunk to disk and verifies checksum
func (g *LocalFileSystemGateway) WriteChunk(ctx context.Context, uploadID string, offset int64, data []byte, expectedSha256 string) error {
	// Create upload directory if it doesn't exist
	uploadDir := filepath.Join(g.basePath, uploadID)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return fmt.Errorf("create upload dir: %w", err)
	}

	// Verify checksum
	hash := sha256.Sum256(data)
	actualSha256 := hex.EncodeToString(hash[:])
	if actualSha256 != expectedSha256 {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSha256, actualSha256)
	}

	// Write chunk to file
	chunkPath := filepath.Join(uploadDir, fmt.Sprintf("%d.chunk", offset))
	f, err := os.Create(chunkPath)
	if err != nil {
		return fmt.Errorf("create chunk file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write chunk data: %w", err)
	}

	return nil
}

// ReadChunk reads a chunk from disk
func (g *LocalFileSystemGateway) ReadChunk(ctx context.Context, uploadID string, offset int64) ([]byte, error) {
	chunkPath := filepath.Join(g.basePath, uploadID, fmt.Sprintf("%d.chunk", offset))
	data, err := os.ReadFile(chunkPath)
	if err != nil {
		return nil, fmt.Errorf("read chunk file: %w", err)
	}
	return data, nil
}

// FinalizeUpload verifies all chunks exist and combines them
func (g *LocalFileSystemGateway) FinalizeUpload(ctx context.Context, uploadID string, totalSize int64) (string, error) {
	uploadDir := filepath.Join(g.basePath, uploadID)

	// Create a combined file that represents the finalized upload
	finalPath := filepath.Join(uploadDir, "final.bin")
	finalFile, err := os.Create(finalPath)
	if err != nil {
		return "", fmt.Errorf("create final file: %w", err)
	}
	defer finalFile.Close()

	// Read and write all chunks in order
	bytesWritten := int64(0)
	for {
		chunkPath := filepath.Join(uploadDir, fmt.Sprintf("%d.chunk", bytesWritten))
		f, err := os.Open(chunkPath)
		if err != nil {
			if os.IsNotExist(err) {
				// No more chunks
				break
			}
			return "", fmt.Errorf("open chunk: %w", err)
		}

		_, err = io.Copy(finalFile, f)
		f.Close()
		if err != nil {
			return "", fmt.Errorf("copy chunk: %w", err)
		}

		// Get file info to know chunk size
		info, _ := os.Stat(chunkPath)
		bytesWritten += info.Size()

		if bytesWritten >= totalSize {
			break
		}
	}

	if bytesWritten != totalSize {
		return "", fmt.Errorf("incomplete upload: got %d bytes, expected %d", bytesWritten, totalSize)
	}

	// Return storage key (for now, just the relative path)
	return fmt.Sprintf("uploads/%s/final.bin", uploadID), nil
}

// AbortUpload removes the upload directory
func (g *LocalFileSystemGateway) AbortUpload(ctx context.Context, uploadID string) error {
	uploadDir := filepath.Join(g.basePath, uploadID)
	return os.RemoveAll(uploadDir)
}
