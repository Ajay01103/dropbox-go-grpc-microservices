package token

import (
	"context"
	"crypto/ed25519"
	"time"
)

// eddsaKeyStoreBackend is an interface for keystore implementations
type eddsaKeyStoreBackend interface {
	loadAllKids(ctx context.Context) ([]string, error)
	loadCurrentKID(ctx context.Context) (string, error)
	loadKeyMeta(ctx context.Context, kid string) (*eddsaKeyMeta, error)
	loadPrivateKey(ctx context.Context, kid string) (ed25519.PrivateKey, error)
	loadPublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error)
	storeKey(ctx context.Context, kid string, privateKey ed25519.PrivateKey, ttl time.Duration) error
	retireKey(ctx context.Context, kid string, ttl time.Duration) error
}
