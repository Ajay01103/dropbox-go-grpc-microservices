package jwks

import (
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
)

type Config struct {
	JWKSURL            string
	Algorithm          jwa.SignatureAlgorithm
	RefreshInterval    time.Duration
	MinRefreshInterval time.Duration
	FetchTimeout       time.Duration
	ScyllaStore        JWKSStore
	ServiceID          string
}

type Option func(*Config)

func WithJWKSURL(url string) Option {
	return func(c *Config) {
		c.JWKSURL = url
	}
}

func WithAlgorithm(alg jwa.SignatureAlgorithm) Option {
	return func(c *Config) {
		c.Algorithm = alg
	}
}

func WithRefreshInterval(d time.Duration) Option {
	return func(c *Config) {
		c.RefreshInterval = d
	}
}

func WithMinRefreshInterval(d time.Duration) Option {
	return func(c *Config) {
		c.MinRefreshInterval = d
	}
}

func WithFetchTimeout(d time.Duration) Option {
	return func(c *Config) {
		c.FetchTimeout = d
	}
}

func WithScyllaDB(store JWKSStore, serviceID string) Option {
	return func(c *Config) {
		c.ScyllaStore = store
		c.ServiceID = serviceID
	}
}

func defaultConfig() *Config {
	return &Config{
		Algorithm:          jwa.RS256,
		RefreshInterval:    1 * time.Hour,
		MinRefreshInterval: 15 * time.Minute,
		FetchTimeout:       10 * time.Second,
	}
}