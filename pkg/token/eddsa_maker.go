package token

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/gocql/gocql"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// EDDSAMaker implements TokenMaker using Ed25519 (EdDSA) JWT tokens.
type EDDSAMaker struct {
	mu               sync.RWMutex
	privateKey       ed25519.PrivateKey
	currentKeyID     string
	publicKeys       map[string]ed25519.PublicKey
	privateKeysByKid map[string]ed25519.PrivateKey
	expiresByKid     map[string]time.Time
	jwksCacheKey     string
	jwksCache        *ristretto.Cache
	keyStore         eddsaKeyStoreBackend
	keyTTL           time.Duration
}

// NewEDDSAMaker creates a maker with a newly generated Ed25519 keypair.
func NewEDDSAMaker() (*EDDSAMaker, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ed25519 generate key: %w", err)
	}
	return NewEDDSAMakerFromPrivateKey(privateKey)
}

// NewEDDSAMakerWithScylla loads keys from ScyllaDB and rotates only when needed.
func NewEDDSAMakerWithScylla(session interface{}, keyTTL time.Duration, jwksCacheKey string, jwksCache *ristretto.Cache) (*EDDSAMaker, error) {
	// Type assert to gocql.Session
	scyllaSession, ok := session.(*gocql.Session)
	if !ok {
		return nil, errors.New("invalid scylla session type")
	}

	if keyTTL < 90*24*time.Hour {
		keyTTL = 90 * 24 * time.Hour
	}

	maker := &EDDSAMaker{
		publicKeys:       make(map[string]ed25519.PublicKey),
		privateKeysByKid: make(map[string]ed25519.PrivateKey),
		expiresByKid:     make(map[string]time.Time),
		jwksCacheKey:     jwksCacheKey,
		jwksCache:        jwksCache,
		keyStore:         newEDDSAScyllaKeyStore(scyllaSession),
		keyTTL:           keyTTL,
	}

	ctx := context.Background()
	kids, err := maker.keyStore.loadAllKids(ctx)
	if err != nil {
		return nil, err
	}

	currentKid, err := maker.keyStore.loadCurrentKID(ctx)
	if err != nil {
		return nil, err
	}

	for _, kid := range kids {
		meta, err := maker.keyStore.loadKeyMeta(ctx, kid)
		if err != nil || meta == nil {
			continue
		}

		pubKey, err := maker.keyStore.loadPublicKey(ctx, kid)
		if err != nil || pubKey == nil {
			continue
		}

		maker.publicKeys[kid] = pubKey
		maker.expiresByKid[kid] = meta.ExpiresAt

		if meta.Status == KeyStatusActive {
			privKey, err := maker.keyStore.loadPrivateKey(ctx, kid)
			if err == nil && privKey != nil {
				maker.privateKeysByKid[kid] = privKey
			}
		}

		if kid == currentKid && meta.Status == KeyStatusActive {
			maker.currentKeyID = currentKid
			maker.privateKey = maker.privateKeysByKid[currentKid]
		}
	}

	if maker.currentKeyID != "" && maker.privateKey != nil {
		return maker, nil
	}

	if _, err := maker.RotateKey(); err != nil {
		return nil, err
	}
	return maker, nil
}

// NewEDDSAMakerFromPrivateKey creates a maker from an existing private key.
func NewEDDSAMakerFromPrivateKey(privateKey ed25519.PrivateKey) (*EDDSAMaker, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid ed25519 private key")
	}

	keyID := fmt.Sprintf("eddsa-key-%d", time.Now().Unix())
	expiresAt := time.Now().Add(10 * 365 * 24 * time.Hour).UTC()
	pub := privateKey.Public().(ed25519.PublicKey)

	m := &EDDSAMaker{
		privateKey:       privateKey,
		currentKeyID:     keyID,
		publicKeys:       make(map[string]ed25519.PublicKey),
		privateKeysByKid: make(map[string]ed25519.PrivateKey),
		expiresByKid:     make(map[string]time.Time),
		keyTTL:           10 * 365 * 24 * time.Hour,
	}

	m.publicKeys[keyID] = pub
	m.privateKeysByKid[keyID] = privateKey
	m.expiresByKid[keyID] = expiresAt

	return m, nil
}

func (m *EDDSAMaker) CreateRefreshToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *RefreshPayload, error) {
	if err := m.ensureCurrentSigningKey(); err != nil {
		return "", nil, err
	}

	uid, err := uuid.Parse(userID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid user id: %w", err)
	}
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid session id: %w", err)
	}

	now := time.Now().UTC()
	payload, err := NewRefreshPayloadAt(uid, email, name, sid, gen, globalVer, now, duration)
	if err != nil {
		return "", nil, err
	}

	m.mu.RLock()
	currentKid := m.currentKeyID
	privateKey := m.privateKey
	m.mu.RUnlock()

	payload.KeyID = currentKid

	claims := refreshClaimsPool.Get().(*RefreshTokenClaims)
	claims.Reset()
	payload.FillClaims(claims)

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = currentKid

	signed, err := token.SignedString(privateKey)
	claims.Reset()
	refreshClaimsPool.Put(claims)
	if err != nil {
		return "", nil, fmt.Errorf("sign token: %w", err)
	}

	return signed, payload, nil
}

func (m *EDDSAMaker) CreateAccessToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *AccessPayload, error) {
	if err := m.ensureCurrentSigningKey(); err != nil {
		return "", nil, err
	}

	uid, err := uuid.Parse(userID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid user id: %w", err)
	}
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid session id: %w", err)
	}

	now := time.Now().UTC()
	payload, err := NewAccessPayloadAt(uid, email, name, sid, gen, globalVer, now, duration)
	if err != nil {
		return "", nil, err
	}

	m.mu.RLock()
	currentKid := m.currentKeyID
	privateKey := m.privateKey
	m.mu.RUnlock()

	payload.KeyID = currentKid

	claims := accessClaimsPool.Get().(*AccessTokenClaims)
	claims.Reset()
	payload.FillClaims(claims)

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = currentKid

	signed, err := token.SignedString(privateKey)
	claims.Reset()
	accessClaimsPool.Put(claims)
	if err != nil {
		return "", nil, fmt.Errorf("sign token: %w", err)
	}

	return signed, payload, nil
}

func (m *EDDSAMaker) VerifyAccessToken(tokenStr string) (*AccessPayload, error) {
	claims := &AccessTokenClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims,
		func(token *jwt.Token) (interface{}, error) {
			if token.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}

			kid, ok := token.Header["kid"].(string)
			if !ok || kid == "" {
				return nil, errors.New("missing kid in token header")
			}

			pubKey, exists := m.getValidPublicKey(kid)
			if !exists {
				return nil, fmt.Errorf("unknown key id: %s", kid)
			}
			return pubKey, nil
		},
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !token.Valid {
		return nil, ErrInvalidToken
	}

	payload, err := accessPayloadFromTokenClaims(claims)
	if err != nil {
		return nil, err
	}
	payload.KeyID, _ = token.Header["kid"].(string)
	return payload, nil
}

func (m *EDDSAMaker) VerifyRefreshToken(tokenStr string) (*RefreshPayload, error) {
	claims := &RefreshTokenClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims,
		func(token *jwt.Token) (interface{}, error) {
			if token.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}

			kid, ok := token.Header["kid"].(string)
			if !ok || kid == "" {
				return nil, errors.New("missing kid in token header")
			}

			pubKey, exists := m.getValidPublicKey(kid)
			if !exists {
				return nil, fmt.Errorf("unknown key id: %s", kid)
			}
			return pubKey, nil
		},
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !token.Valid {
		return nil, ErrInvalidToken
	}

	payload, err := refreshPayloadFromTokenClaims(claims)
	if err != nil {
		return nil, err
	}
	payload.KeyID, _ = token.Header["kid"].(string)
	return payload, nil
}

func (m *EDDSAMaker) CreateSessionRefreshToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *SessionRefreshPayload, error) {
	return m.CreateRefreshToken(userID, email, name, sessionID, gen, globalVer, duration)
}

func (m *EDDSAMaker) CreateSessionAccessToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *SessionAccessPayload, error) {
	return m.CreateAccessToken(userID, email, name, sessionID, gen, globalVer, duration)
}

func (m *EDDSAMaker) VerifySessionRefreshToken(tokenStr string) (*SessionRefreshPayload, error) {
	claims := &SessionRefreshClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims,
		func(token *jwt.Token) (interface{}, error) {
			if token.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			kid, ok := token.Header["kid"].(string)
			if !ok || kid == "" {
				return nil, errors.New("missing kid in token header")
			}
			pubKey, exists := m.getValidPublicKey(kid)
			if !exists {
				return nil, fmt.Errorf("unknown key id: %s", kid)
			}
			return pubKey, nil
		},
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !token.Valid || claims.TokenType != TokenTypeRefresh {
		return nil, ErrInvalidToken
	}
	payload, err := sessionRefreshPayloadFromClaims(claims)
	if err != nil {
		return nil, err
	}
	payload.KeyID, _ = token.Header["kid"].(string)
	return payload, nil
}

func (m *EDDSAMaker) VerifySessionAccessToken(tokenStr string) (*SessionAccessPayload, error) {
	claims := &SessionAccessClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims,
		func(token *jwt.Token) (interface{}, error) {
			if token.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			kid, ok := token.Header["kid"].(string)
			if !ok || kid == "" {
				return nil, errors.New("missing kid in token header")
			}
			pubKey, exists := m.getValidPublicKey(kid)
			if !exists {
				return nil, fmt.Errorf("unknown key id: %s", kid)
			}
			return pubKey, nil
		},
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !token.Valid || claims.TokenType != TokenTypeAccess {
		return nil, ErrInvalidToken
	}
	payload, err := sessionAccessPayloadFromClaims(claims)
	if err != nil {
		return nil, err
	}
	payload.KeyID, _ = token.Header["kid"].(string)
	return payload, nil
}

func (m *EDDSAMaker) RotateKey() (string, error) {
	_, newPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("ed25519 generate key: %w", err)
	}
	newPublicKey := newPrivateKey.Public().(ed25519.PublicKey)
	newKeyID := fmt.Sprintf("eddsa-key-%d", time.Now().Unix())
	expiresAt := time.Now().UTC().Add(m.keyTTL)
	if m.keyTTL <= 0 {
		expiresAt = time.Now().UTC().Add(10 * 365 * 24 * time.Hour)
	}

	m.mu.Lock()
	pastKeyID := m.currentKeyID
	m.privateKey = newPrivateKey
	m.currentKeyID = newKeyID
	m.publicKeys[newKeyID] = newPublicKey
	m.privateKeysByKid[newKeyID] = newPrivateKey
	m.expiresByKid[newKeyID] = expiresAt
	m.mu.Unlock()

	if m.keyStore != nil {
		ctx := context.Background()
		if pastKeyID != "" {
			_ = m.keyStore.retireKey(ctx, pastKeyID, rotatedKeyRetention)
		}
		if err := m.keyStore.storeKey(ctx, newKeyID, newPrivateKey, m.keyTTL); err != nil {
			return "", err
		}
	}

	if m.jwksCache != nil && m.jwksCacheKey != "" {
		m.jwksCache.Del(m.jwksCacheKey)
	}

	return newKeyID, nil
}

func (m *EDDSAMaker) GetCurrentKeyID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentKeyID
}

func (m *EDDSAMaker) ExportPublicKeys() ([]byte, error) {
	response := JWKSResponse{Keys: []JWKSKey{}}
	now := time.Now().UTC()

	m.mu.RLock()
	for kid, pubKey := range m.publicKeys {
		expiresAt, ok := m.expiresByKid[kid]
		if ok && now.After(expiresAt) {
			continue
		}

		x := base64.RawURLEncoding.EncodeToString(pubKey)
		response.Keys = append(response.Keys, JWKSKey{
			KTY: "OKP",
			Use: "sig",
			KID: kid,
			CRV: "Ed25519",
			X:   x,
			Alg: jwt.SigningMethodEdDSA.Alg(),
		})
	}
	m.mu.RUnlock()

	return json.Marshal(response)
}

func (m *EDDSAMaker) ensureCurrentSigningKey() error {
	m.mu.RLock()
	currentKid := m.currentKeyID
	privateKey := m.privateKey
	expiresAt, hasExpiry := m.expiresByKid[currentKid]
	m.mu.RUnlock()

	if currentKid != "" && privateKey != nil {
		if !hasExpiry || time.Now().UTC().Add(60*24*time.Hour).Before(expiresAt) {
			return nil
		}
	}

	if _, err := m.RotateKey(); err != nil {
		return fmt.Errorf("rotate expired signing key: %w", err)
	}
	return nil
}

func (m *EDDSAMaker) getValidPublicKey(kid string) (ed25519.PublicKey, bool) {
	now := time.Now().UTC()
	m.mu.RLock()
	pubKey, exists := m.publicKeys[kid]
	expiresAt, hasExpiry := m.expiresByKid[kid]
	m.mu.RUnlock()
	if !exists {
		return nil, false
	}
	if hasExpiry && now.After(expiresAt) {
		return nil, false
	}
	return pubKey, true
}
