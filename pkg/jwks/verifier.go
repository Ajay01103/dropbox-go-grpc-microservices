package jwks

import (
	"context"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwt"
)

type Claims struct {
	Subject       string
	Issuer        string
	Audience      []string
	Expiry        time.Time
	IssuedAt      time.Time
	TokenType     string
	SessionID     string
	Generation    int64
	GlobalVersion int
	Extra         map[string]any
}

type Verifier struct {
	cache    *Cache
	issuer   string
	audience []string
}

func NewVerifier(cache *Cache, issuer string, audience ...string) *Verifier {
	return &Verifier{cache: cache, issuer: issuer, audience: audience}
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	keySet, err := v.cache.GetKeySet(ctx)
	if err != nil {
		return nil, fmt.Errorf("jwks: keyset unavailable: %w", err)
	}

	options := []jwt.ParseOption{
		jwt.WithKeySet(keySet),
		jwt.WithValidate(true),
		jwt.WithContext(ctx),
	}
	if v.issuer != "" {
		options = append(options, jwt.WithIssuer(v.issuer))
	}
	for _, audience := range v.audience {
		options = append(options, jwt.WithAudience(audience))
	}

	token, err := jwt.ParseString(rawToken, options...)
	if err != nil {
		// Try refreshing cache once and parsing again in case key rotated/service restarted
		if refreshSet, refreshErr := v.cache.Refresh(ctx); refreshErr == nil {
			options[0] = jwt.WithKeySet(refreshSet)
			token, err = jwt.ParseString(rawToken, options...)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("jwks: invalid token: %w", err)
	}

	extra := make(map[string]any)
	for key, value := range token.PrivateClaims() {
		extra[key] = value
	}

	return &Claims{
		Subject:       token.Subject(),
		Issuer:        token.Issuer(),
		Audience:      token.Audience(),
		Expiry:        token.Expiration(),
		IssuedAt:      token.IssuedAt(),
		TokenType:     stringClaim(token.PrivateClaims(), "token_type"),
		SessionID:     stringClaim(token.PrivateClaims(), "sid"),
		Generation:    int64Claim(token.PrivateClaims(), "gen"),
		GlobalVersion: intClaim(token.PrivateClaims(), "gv"),
		Extra:         extra,
	}, nil
}

func stringClaim(claims map[string]any, name string) string {
	value, _ := claims[name].(string)
	return value
}

func int64Claim(claims map[string]any, name string) int64 {
	if value, ok := claims[name].(int64); ok {
		return value
	}
	floatValue, _ := claims[name].(float64)
	return int64(floatValue)
}

func intClaim(claims map[string]any, name string) int {
	return int(int64Claim(claims, name))
}
