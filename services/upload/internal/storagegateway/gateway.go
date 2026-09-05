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
	HashFinalizedObject(ctx context.Context, uploadID string) (string, error)

	// AbortUpload cleans up storage for an abandoned upload
	AbortUpload(ctx context.Context, uploadID string) error
}

func (g *LocalFileSystemGateway) HashFinalizedObject(ctx context.Context, uploadID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(filepath.Join(g.basePath, uploadID, "final.bin"))
	if err != nil {
		return "", fmt.Errorf("open final file for hashing: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, file, make([]byte, 1024*1024)); err != nil {
		return "", fmt.Errorf("hash final file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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

	// Read and write all chunks in order
	bytesWritten := int64(0)
	chunkPaths := make([]string, 0)
	for {
		if err := ctx.Err(); err != nil {
			_ = finalFile.Close()
			return "", fmt.Errorf("finalize upload canceled: %w", err)
		}

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
		chunkPaths = append(chunkPaths, chunkPath)

		// Get file info to know chunk size
		info, err := os.Stat(chunkPath)
		if err != nil {
			_ = finalFile.Close()
			return "", fmt.Errorf("stat chunk: %w", err)
		}
		bytesWritten += info.Size()

		if bytesWritten >= totalSize {
			break
		}
	}

	if bytesWritten != totalSize {
		_ = finalFile.Close()
		return "", fmt.Errorf("incomplete upload: got %d bytes, expected %d", bytesWritten, totalSize)
	}

	if err := finalFile.Close(); err != nil {
		return "", fmt.Errorf("close final file: %w", err)
	}

	finalInfo, err := os.Stat(finalPath)
	if err != nil {
		return "", fmt.Errorf("stat final file: %w", err)
	}
	if finalInfo.Size() != totalSize {
		return "", fmt.Errorf("final file size mismatch: got %d bytes, expected %d", finalInfo.Size(), totalSize)
	}

	for _, chunkPath := range chunkPaths {
		if err := os.Remove(chunkPath); err != nil {
			return "", fmt.Errorf("remove chunk %s: %w", filepath.Base(chunkPath), err)
		}
	}

	// Return storage key (for now, just the relative path)
	return fmt.Sprintf("uploads/%s/final.bin", uploadID), nil
}

// AbortUpload removes the upload directory
func (g *LocalFileSystemGateway) AbortUpload(ctx context.Context, uploadID string) error {
	uploadDir := filepath.Join(g.basePath, uploadID)
	return os.RemoveAll(uploadDir)
}
