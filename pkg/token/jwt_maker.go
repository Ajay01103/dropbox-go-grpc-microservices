package token

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const minSecretKeySize = 32

// JWTMaker implements TokenMaker using HS256 JWT tokens.
type JWTMaker struct {
	secretKey      string
	secretKeyBytes []byte
	keyFunc        jwt.Keyfunc
}

// NewJWTMaker creates a JWTMaker. secretKey must be at least 32 characters.
func NewJWTMaker(secretKey string) (*JWTMaker, error) {
	if len(secretKey) < minSecretKeySize {
		return nil, fmt.Errorf("invalid key size: must be at least %d characters", minSecretKeySize)
	}
	m := &JWTMaker{
		secretKey:      secretKey,
		secretKeyBytes: []byte(secretKey),
	}
	m.keyFunc = func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return m.secretKeyBytes, nil
	}
	return m, nil
}

// ─── Refresh Token ────────────────────────────────────────────────────────────

// CreateRefreshToken mints a new refresh token.
func (m *JWTMaker) CreateRefreshToken(
	userID, email, name, sessionID string,
	gen int64,
	globalVer int,
	duration time.Duration,
) (string, *RefreshPayload, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid user id: %w", err)
	}
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid session id: %w", err)
	}

	payload, err := NewRefreshPayload(uid, email, name, sid, gen, globalVer, duration)
	if err != nil {
		return "", nil, err
	}

	claims := jwt.MapClaims{
		"sub":        payload.UserID.String(),
		"email":      payload.Email,
		"name":       payload.Name,
		"token_type": string(payload.TokenType),
		"sid":        payload.SessionID.String(),
		"gen":        payload.Gen,
		"gv":         payload.GlobalVer,
		"iat":        payload.IssuedAt.Unix(),
		"exp":        payload.ExpiredAt.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secretKeyBytes)
	if err != nil {
		return "", nil, err
	}
	return signed, payload, nil
}

// ─── Access Token ─────────────────────────────────────────────────────────────

// CreateAccessToken mints a new access token anchored to session id and generation.
func (m *JWTMaker) CreateAccessToken(
	userID, email, name, sessionID string,
	gen int64,
	globalVer int,
	duration time.Duration,
) (string, *AccessPayload, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid user id: %w", err)
	}
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("invalid session id: %w", err)
	}

	payload, err := NewAccessPayload(uid, email, name, sid, gen, globalVer, duration)
	if err != nil {
		return "", nil, err
	}

	claims := jwt.MapClaims{
		"sub":        payload.UserID.String(),
		"email":      payload.Email,
		"name":       payload.Name,
		"token_type": string(payload.TokenType),
		"sid":        payload.SessionID.String(),
		"gen":        payload.Gen,
		"gv":         payload.GlobalVer,
		"iat":        payload.IssuedAt.Unix(),
		"exp":        payload.ExpiredAt.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secretKeyBytes)
	if err != nil {
		return "", nil, err
	}
	return signed, payload, nil
}

// ─── Verification ─────────────────────────────────────────────────────────────

// VerifyAccessToken parses and validates an access token.
func (m *JWTMaker) VerifyAccessToken(tokenStr string) (*AccessPayload, error) {
	claims, err := m.parseClaims(tokenStr)
	if err != nil {
		return nil, err
	}

	tokenType, _ := claims["token_type"].(string)
	if tokenType != string(TokenTypeAccess) {
		return nil, ErrInvalidToken
	}

	payload, err := accessPayloadFromClaims(claims)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// VerifyRefreshToken parses and validates a refresh token.
func (m *JWTMaker) VerifyRefreshToken(tokenStr string) (*RefreshPayload, error) {
	claims, err := m.parseClaims(tokenStr)
	if err != nil {
		return nil, err
	}

	tokenType, _ := claims["token_type"].(string)
	if tokenType != string(TokenTypeRefresh) {
		return nil, ErrInvalidToken
	}

	payload, err := refreshPayloadFromClaims(claims)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// ─── Session Token ───────────────────────────────────────────────────────────

func (m *JWTMaker) CreateSessionRefreshToken(
	userID, email, name, sessionID string,
	gen int64,
	globalVer int,
	duration time.Duration,
) (string, *SessionRefreshPayload, error) {
	return m.CreateRefreshToken(userID, email, name, sessionID, gen, globalVer, duration)
}

func (m *JWTMaker) CreateSessionAccessToken(
	userID, email, name, sessionID string,
	gen int64,
	globalVer int,
	duration time.Duration,
) (string, *SessionAccessPayload, error) {
	return m.CreateAccessToken(userID, email, name, sessionID, gen, globalVer, duration)
}

func (m *JWTMaker) VerifySessionRefreshToken(tokenStr string) (*SessionRefreshPayload, error) {
	claims := &SessionRefreshClaims{}
	jwtToken, err := jwt.ParseWithClaims(tokenStr, claims, m.keyFunc)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !jwtToken.Valid || claims.Subject == "" {
		return nil, ErrInvalidToken
	}
	if claims.TokenType != TokenTypeRefresh {
		return nil, ErrInvalidToken
	}
	return sessionRefreshPayloadFromClaims(claims)
}

func (m *JWTMaker) VerifySessionAccessToken(tokenStr string) (*SessionAccessPayload, error) {
	claims := &SessionAccessClaims{}
	jwtToken, err := jwt.ParseWithClaims(tokenStr, claims, m.keyFunc)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !jwtToken.Valid || claims.Subject == "" {
		return nil, ErrInvalidToken
	}
	if claims.TokenType != TokenTypeAccess {
		return nil, ErrInvalidToken
	}
	return sessionAccessPayloadFromClaims(claims)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (m *JWTMaker) parseClaims(tokenStr string) (jwt.MapClaims, error) {
	jwtToken, err := jwt.ParseWithClaims(tokenStr, make(jwt.MapClaims, 10), m.keyFunc)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	claims, ok := jwtToken.Claims.(jwt.MapClaims)
	if !ok || !jwtToken.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func uuidFromClaims(claims jwt.MapClaims, key string) (uuid.UUID, error) {
	raw, ok := claims[key].(string)
	if !ok {
		return uuid.Nil, ErrInvalidToken
	}
	v, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, ErrInvalidToken
	}
	return v, nil
}

func timeFromClaims(claims jwt.MapClaims, key string) (time.Time, error) {
	raw, ok := claims[key].(float64)
	if !ok {
		return time.Time{}, ErrInvalidToken
	}
	return time.Unix(int64(raw), 0), nil
}

func optionalStringClaim(claims jwt.MapClaims, key string) string {
	if v, ok := claims[key].(string); ok {
		return v
	}
	return ""
}

func accessPayloadFromClaims(claims jwt.MapClaims) (*AccessPayload, error) {
	sub, err := uuidFromClaims(claims, "sub")
	if err != nil {
		return nil, err
	}
	sid, err := uuidFromClaims(claims, "sid")
	if err != nil {
		return nil, err
	}
	genRaw, ok := claims["gen"].(float64)
	if !ok {
		return nil, ErrInvalidToken
	}
	gv := 0
	if gvRaw, ok := claims["gv"].(float64); ok {
		gv = int(gvRaw)
	}
	exp, err := timeFromClaims(claims, "exp")
	if err != nil {
		return nil, err
	}
	iat, err := timeFromClaims(claims, "iat")
	if err != nil {
		return nil, err
	}

	return &AccessPayload{
		UserID:    sub,
		Email:     optionalStringClaim(claims, "email"),
		Name:      optionalStringClaim(claims, "name"),
		TokenType: TokenTypeAccess,
		SessionID: sid,
		Gen:       int64(genRaw),
		GlobalVer: gv,
		IssuedAt:  iat,
		ExpiredAt: exp,
	}, nil
}

func refreshPayloadFromClaims(claims jwt.MapClaims) (*RefreshPayload, error) {
	sub, err := uuidFromClaims(claims, "sub")
	if err != nil {
		return nil, err
	}
	sid, err := uuidFromClaims(claims, "sid")
	if err != nil {
		return nil, err
	}
	genRaw, ok := claims["gen"].(float64)
	if !ok {
		return nil, ErrInvalidToken
	}
	gv := 0
	if gvRaw, ok := claims["gv"].(float64); ok {
		gv = int(gvRaw)
	}
	exp, err := timeFromClaims(claims, "exp")
	if err != nil {
		return nil, err
	}
	iat, err := timeFromClaims(claims, "iat")
	if err != nil {
		return nil, err
	}

	return &RefreshPayload{
		UserID:    sub,
		Email:     optionalStringClaim(claims, "email"),
		Name:      optionalStringClaim(claims, "name"),
		TokenType: TokenTypeRefresh,
		SessionID: sid,
		Gen:       int64(genRaw),
		GlobalVer: gv,
		IssuedAt:  iat,
		ExpiredAt: exp,
	}, nil
}
