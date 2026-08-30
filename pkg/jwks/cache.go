package jwks

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lestrrat-go/httprc"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"golang.org/x/sync/singleflight"
)

type Cache struct {
	cfg      *Config
	jwkCache *jwk.Cache
	sf       singleflight.Group
}

func New(ctx context.Context, opts ...Option) (*Cache, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.JWKSURL == "" {
		return nil, fmt.Errorf("jwks: JWKSURL is required")
	}
	if cfg.ScyllaStore != nil && cfg.ServiceID == "" {
		return nil, fmt.Errorf("jwks: ServiceID is required when ScyllaDB persistence is enabled")
	}

	var transport http.RoundTripper = http.DefaultTransport
	if cfg.ScyllaStore != nil {
		transport = newScyllaDBTransport(transport, cfg.ScyllaStore, cfg.ServiceID, cfg.JWKSURL)
	}

	httpClient := &http.Client{
		Timeout:   cfg.FetchTimeout,
		Transport: transport,
	}

	c := &Cache{cfg: cfg}
	c.jwkCache = jwk.NewCache(ctx,
		jwk.WithErrSink(httprc.ErrSinkFunc(func(err error) {
			fmt.Printf("jwks: background refresh error: %v\n", err)
		})),
	)

	whitelist := httprc.NewMapWhitelist().Add(cfg.JWKSURL)
	registerOpts := []jwk.RegisterOption{
		jwk.WithHTTPClient(httpClient),
		jwk.WithFetchWhitelist(whitelist),
		jwk.WithRefreshInterval(cfg.RefreshInterval),
		jwk.WithMinRefreshInterval(cfg.MinRefreshInterval),
	}
	if err := c.jwkCache.Register(cfg.JWKSURL, registerOpts...); err != nil {
		return nil, fmt.Errorf("jwks: register url: %w", err)
	}

	if _, err, _ := c.sf.Do("init", func() (any, error) {
		_, err := c.jwkCache.Refresh(ctx, cfg.JWKSURL)
		return nil, err
	}); err != nil {
		return nil, fmt.Errorf("jwks: initial fetch failed: %w", err)
	}

	return c, nil
}

func (c *Cache) GetKeySet(ctx context.Context) (jwk.Set, error) {
	set, err, _ := c.sf.Do("get:"+c.cfg.JWKSURL, func() (any, error) {
		return c.jwkCache.Get(ctx, c.cfg.JWKSURL)
	})
	if err != nil {
		return nil, fmt.Errorf("jwks: get keyset: %w", err)
	}

	return set.(jwk.Set), nil
}

func (c *Cache) Refresh(ctx context.Context) (jwk.Set, error) {
	set, err := c.jwkCache.Refresh(ctx, c.cfg.JWKSURL)
	if err != nil {
		return nil, fmt.Errorf("jwks: refresh keyset: %w", err)
	}
	return set, nil
}

func (c *Cache) URL() string {
	return c.cfg.JWKSURL
}
