// Package blockkey derives the physical storage object key for a block from
// its SHA-256 hex hash. It is the single source of truth shared by the S3
// and local storage gateways.
//
// This is a storage-layout function, not part of the event contract — hence
// its own package instead of pkg/events.
package blockkey

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidHash is returned for anything that is not a lowercase 64-char
// SHA-256 hex digest.
var ErrInvalidHash = errors.New("invalid block hash")

// For returns the canonical object key for a block hash:
// blocks/<h[0:2]>/<h[2:4]>/<hash>. Both backends use the same layout (S3 as
// slash-separated object key, local as filesystem path segments).
func For(hash string) (string, error) {
	if len(hash) != sha256.Size*2 {
		return "", fmt.Errorf("%w: wrong length %d", ErrInvalidHash, len(hash))
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("%w: not hex: %v", ErrInvalidHash, err)
	}
	for _, c := range hash {
		if c >= 'A' && c <= 'F' {
			return "", fmt.Errorf("%w: uppercase hex", ErrInvalidHash)
		}
	}
	return "blocks/" + hash[:2] + "/" + hash[2:4] + "/" + hash, nil
}
