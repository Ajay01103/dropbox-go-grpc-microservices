package tokencache

import (
	"testing"

	"github.com/dgraph-io/ristretto"
)

func TestInvalidateJWKSDeletesCacheEntry(t *testing.T) {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e4,
		MaxCost:     1 << 20,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}

	key := JWKSCacheKey()
	cache.SetWithTTL(key, []byte("{}"), JWKSCost, JWKSTTL)
	cache.Wait()
	if _, ok := cache.Get(key); !ok {
		t.Fatal("expected jwks entry to exist before invalidation")
	}

	InvalidateJWKS(cache)
	cache.Wait()
	if _, ok := cache.Get(key); ok {
		t.Fatal("expected jwks entry to be deleted after invalidation")
	}
}