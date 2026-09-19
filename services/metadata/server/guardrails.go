package server

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"
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

func (rl *RateLimiter) Interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(rl.WrapUnary)
}

func (rl *RateLimiter) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		userID, err := interceptor.UserIDFromContext(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		if !rl.allow(userID.String()) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errRateLimitExceeded)
		}
		return next(ctx, req)
	}
}

var errRateLimitExceeded = &rateLimitError{}

type rateLimitError struct{}

func (*rateLimitError) Error() string { return "rate limit exceeded, slow down" }

func TimeoutInterceptor(timeout time.Duration) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
				return next(ctx, req)
			}
			requestCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(requestCtx, req)
		}
	}
}
