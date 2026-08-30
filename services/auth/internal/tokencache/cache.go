package tokencache

import (
	"time"

	"github.com/dgraph-io/ristretto"
)

const (
	SessionStateCost = 1
	CurrentUserCost  = 1
	GlobalVerCost    = 1
	JWKSCost         = 1
	TheftCountCost   = 1

	SessionStateTTL = 15 * time.Minute // Must be at least the default access token lifetime; LWT validates gen
	CurrentUserTTL  = 15 * time.Minute
	GlobalVerTTL    = 5 * time.Minute // Separate from session; invalidation independent
	JWKSTTL         = 55 * time.Minute // Slightly under the HTTP Cache-Control max-age
	TheftCountTTL   = 1 * time.Hour
	TheftThreshold  = 3 // Bump global_ver after N theft events in the TTL window

	prefixSess = "sess:"
	prefixCur  = "cur:"
	prefixGver = "gver:"
	prefixJWKS = "jwks:"
	prefixTheft = "theft:"
)

const JWKSKey = prefixJWKS + "current"

type CurrentUserEntry struct {
	UserID string
	Email  string
	Name   string
}

func NewRistrettoCache() (*ristretto.Cache, error) {
	return ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e6,
		MaxCost:     50 << 20,
		BufferItems: 64,
		Metrics:     true,
	})
}

func SessionKey(sessionID string) string {
	return prefixSess + sessionID
}

func CurrentUserKey(userID string) string {
	return prefixCur + userID
}

func GlobalVerKey(userID string) string {
	return prefixGver + userID
}

func JWKSCacheKey() string {
	return JWKSKey
}

func InvalidateJWKS(cache *ristretto.Cache) {
	if cache == nil {
		return
	}
	cache.Del(JWKSCacheKey())
}

func TheftCounterKey(userID string) string {
	return prefixTheft + userID
}
