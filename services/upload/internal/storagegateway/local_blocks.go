package storagegateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var blockHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// LocalBlockGateway stores blocks under a hash-derived path and is useful for
// local development and deterministic unit tests.
type LocalBlockGateway struct {
	basePath string
}

func NewLocalBlockGateway(basePath string) *LocalBlockGateway {
	return &LocalBlockGateway{basePath: basePath}
}

func (g *LocalBlockGateway) blockPath(hash string) (string, error) {
	if !blockHashPattern.MatchString(hash) {
		return "", errors.New("invalid block hash")
	}
	return filepath.Join(g.basePath, "blocks", hash[:2], hash[2:4], hash), nil
}

func (g *LocalBlockGateway) WriteBlock(ctx context.Context, hash string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != hash {
		return "", fmt.Errorf("block hash mismatch: expected %s", hash)
	}

	path, err := g.blockPath(hash)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect block: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create block directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".block-")
	if err != nil {
		return "", fmt.Errorf("create temporary block: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write block: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close block: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return hash, nil
		}
		return "", fmt.Errorf("publish block: %w", err)
	}
	return hash, nil
}

func (g *LocalBlockGateway) ReadBlock(ctx context.Context, hash string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := g.blockPath(hash)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read block: %w", err)
	}
	return data, nil
}

func (g *LocalBlockGateway) BlockExists(ctx context.Context, hash string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path, err := g.blockPath(hash)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect block: %w", err)
	}
	return true, nil
}

func (g *LocalBlockGateway) DeleteBlock(ctx context.Context, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := g.blockPath(hash)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete block: %w", err)
	}
	return nil
}