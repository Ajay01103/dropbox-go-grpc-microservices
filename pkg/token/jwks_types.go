package token

// JWKSKey represents a key in JWKS format (RFC 7517 / RFC 8037).
type JWKSKey struct {
	KTY string `json:"kty"`
	Use string `json:"use,omitempty"`
	KID string `json:"kid"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
	CRV string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Alg string `json:"alg,omitempty"`
}

// JWKSResponse is the response structure for the JWKS endpoint.
type JWKSResponse struct {
	Keys []JWKSKey `json:"keys"`
}
