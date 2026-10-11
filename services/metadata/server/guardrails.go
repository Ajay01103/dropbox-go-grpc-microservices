package server

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	"github.com/dgraph-io/ristretto"
)

type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	at     time.Time
}

type RateLimiter struct {
	cache *ristretto.Cache
	rps   float64
	burst float64
	ttl   time.Duration
}

func NewRateLimiter(rps float64, burst int, ttl time.Duration) (*RateLimiter, error) {
	cache, err := ristretto.NewCache(&ristretto.Config{NumCounters: 1e6, MaxCost: 1 << 20, BufferItems: 64})
	if err != nil {
		return nil, err
	}
	return &RateLimiter{cache: cache, rps: rps, burst: float64(burst), ttl: ttl}, nil
}

func (rl *RateLimiter) limiterFor(userID string) *tokenBucket {
	if value, ok := rl.cache.Get(userID); ok {
		return value.(*tokenBucket)
	}
	bucket := &tokenBucket{tokens: rl.burst, at: time.Now()}
	rl.cache.SetWithTTL(userID, bucket, 1, rl.ttl)
	return bucket
}

func (rl *RateLimiter) allow(userID string) bool {
	bucket := rl.limiterFor(userID)
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	now := time.Now()
	bucket.tokens += now.Sub(bucket.at).Seconds() * rl.rps
	if bucket.tokens > rl.burst {
		bucket.tokens = rl.burst
	}
	bucket.at = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func (rl *RateLimiter) Interceptor() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			userID, err := interceptor.UserIDFromContext(ctx)
			if err != nil {
				return newConnectError(connect.CodeUnauthenticated, err)
			}
			if !rl.allow(userID.String()) {
				return newConnectError(connect.CodeResourceExhausted, errRateLimitExceeded)
			}
			return next(ctx, spec, stream)
		}
	}
}

var errRateLimitExceeded = &rateLimitError{}

type rateLimitError struct{}

func (*rateLimitError) Error() string { return "rate limit exceeded, slow down" }

func TimeoutInterceptor(timeout time.Duration) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
				return next(ctx, spec, stream)
			}
			requestCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(requestCtx, spec, stream)
		}
	}
}
