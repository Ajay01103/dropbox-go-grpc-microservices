package token

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenType distinguishes between access and refresh tokens in claims.
type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
	TokenIssuer                = "go-notion-auth"
	TokenAudience              = "go-notion-api"
)

// AccessPayload holds the JWT claims for an access token.
type AccessPayload struct {
	UserID    uuid.UUID `json:"sub"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	TokenType TokenType `json:"token_type"`
	SessionID uuid.UUID `json:"sid"`
	Gen       int64     `json:"gen"`
	GlobalVer int       `json:"gv,omitempty"`
	IssuedAt  time.Time `json:"iat"`
	ExpiredAt time.Time `json:"exp"`
	KeyID     string    `json:"kid,omitempty"`
}

// AccessTokenClaims is a typed JWT claim set for access tokens.
type AccessTokenClaims struct {
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	TokenType TokenType `json:"token_type"`
	SessionID string    `json:"sid"`
	Gen       int64     `json:"gen"`
	GlobalVer int       `json:"gv,omitempty"`

	jwt.RegisteredClaims
}

// Reset clears all fields before this claim object is reused from a pool.
func (c *AccessTokenClaims) Reset() {
	*c = AccessTokenClaims{}
}

// NewAccessPayload creates a new AccessPayload for the given user.
func NewAccessPayload(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, duration time.Duration) (*AccessPayload, error) {
	return NewAccessPayloadAt(userID, email, name, sessionID, gen, globalVer, time.Now().UTC(), duration)
}

// NewAccessPayloadAt creates a new AccessPayload using a provided timestamp.
func NewAccessPayloadAt(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, now time.Time, duration time.Duration) (*AccessPayload, error) {
	now = now.UTC()
	return &AccessPayload{
		UserID:    userID,
		Email:     email,
		Name:      name,
		TokenType: TokenTypeAccess,
		SessionID: sessionID,
		Gen:       gen,
		GlobalVer: globalVer,
		IssuedAt:  now,
		ExpiredAt: now.Add(duration),
	}, nil
}

func (p *AccessPayload) FillClaims(claims *AccessTokenClaims) {
	claims.Email = p.Email
	claims.Name = p.Name
	claims.TokenType = p.TokenType
	claims.SessionID = p.SessionID.String()
	claims.Gen = p.Gen
	claims.GlobalVer = p.GlobalVer
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Subject:   p.UserID.String(),
		Issuer:    TokenIssuer,
		Audience:  jwt.ClaimStrings{TokenAudience},
		IssuedAt:  jwt.NewNumericDate(p.IssuedAt),
		ExpiresAt: jwt.NewNumericDate(p.ExpiredAt),
	}
}

func accessPayloadFromTokenClaims(claims *AccessTokenClaims) (*AccessPayload, error) {
	if claims == nil || claims.TokenType != TokenTypeAccess {
		return nil, ErrInvalidToken
	}
	if claims.ExpiresAt == nil || claims.IssuedAt == nil {
		return nil, ErrInvalidToken
	}

	uid, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, ErrInvalidToken
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return nil, ErrInvalidToken
	}

	payload := &AccessPayload{
		UserID:    uid,
		Email:     claims.Email,
		Name:      claims.Name,
		TokenType: claims.TokenType,
		SessionID: sessionID,
		Gen:       claims.Gen,
		GlobalVer: claims.GlobalVer,
		IssuedAt:  claims.IssuedAt.Time,
		ExpiredAt: claims.ExpiresAt.Time,
	}

	if time.Now().After(payload.ExpiredAt) {
		return nil, ErrExpiredToken
	}

	return payload, nil
}

// Valid implements the jwt.Claims interface.
func (p *AccessPayload) Valid() error {
	if time.Now().After(p.ExpiredAt) {
		return ErrExpiredToken
	}
	return nil
}

// RefreshPayload holds the JWT claims for a refresh token.
type RefreshPayload struct {
	UserID    uuid.UUID `json:"sub"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	TokenType TokenType `json:"token_type"`
	SessionID uuid.UUID `json:"sid"`
	Gen       int64     `json:"gen"`
	GlobalVer int       `json:"gv,omitempty"`
	IssuedAt  time.Time `json:"iat"`
	ExpiredAt time.Time `json:"exp"`
	KeyID     string    `json:"kid,omitempty"`
}

// RefreshTokenClaims is a typed JWT claim set for refresh tokens.
type RefreshTokenClaims struct {
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	TokenType TokenType `json:"token_type"`
	SessionID string    `json:"sid"`
	Gen       int64     `json:"gen"`
	GlobalVer int       `json:"gv,omitempty"`

	jwt.RegisteredClaims
}

// Reset clears all fields before this claim object is reused from a pool.
func (c *RefreshTokenClaims) Reset() {
	*c = RefreshTokenClaims{}
}

// NewRefreshPayload creates a new RefreshPayload for the given user.
func NewRefreshPayload(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, duration time.Duration) (*RefreshPayload, error) {
	return NewRefreshPayloadAt(userID, email, name, sessionID, gen, globalVer, time.Now().UTC(), duration)
}

// NewRefreshPayloadAt creates a new RefreshPayload using a provided timestamp.
func NewRefreshPayloadAt(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, now time.Time, duration time.Duration) (*RefreshPayload, error) {
	now = now.UTC()
	return &RefreshPayload{
		UserID:    userID,
		Email:     email,
		Name:      name,
		TokenType: TokenTypeRefresh,
		SessionID: sessionID,
		Gen:       gen,
		GlobalVer: globalVer,
		IssuedAt:  now,
		ExpiredAt: now.Add(duration),
	}, nil
}

func (p *RefreshPayload) FillClaims(claims *RefreshTokenClaims) {
	claims.Email = p.Email
	claims.Name = p.Name
	claims.TokenType = p.TokenType
	claims.SessionID = p.SessionID.String()
	claims.Gen = p.Gen
	claims.GlobalVer = p.GlobalVer
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Subject:   p.UserID.String(),
		Issuer:    TokenIssuer,
		Audience:  jwt.ClaimStrings{TokenAudience},
		IssuedAt:  jwt.NewNumericDate(p.IssuedAt),
		ExpiresAt: jwt.NewNumericDate(p.ExpiredAt),
	}
}

func refreshPayloadFromTokenClaims(claims *RefreshTokenClaims) (*RefreshPayload, error) {
	if claims == nil || claims.TokenType != TokenTypeRefresh {
		return nil, ErrInvalidToken
	}
	if claims.ExpiresAt == nil || claims.IssuedAt == nil {
		return nil, ErrInvalidToken
	}

	uid, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, ErrInvalidToken
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return nil, ErrInvalidToken
	}

	payload := &RefreshPayload{
		UserID:    uid,
		Email:     claims.Email,
		Name:      claims.Name,
		TokenType: claims.TokenType,
		SessionID: sessionID,
		Gen:       claims.Gen,
		GlobalVer: claims.GlobalVer,
		IssuedAt:  claims.IssuedAt.Time,
		ExpiredAt: claims.ExpiresAt.Time,
	}

	if time.Now().After(payload.ExpiredAt) {
		return nil, ErrExpiredToken
	}

	return payload, nil
}

// Valid implements the jwt.Claims interface.
func (p *RefreshPayload) Valid() error {
	if time.Now().After(p.ExpiredAt) {
		return ErrExpiredToken
	}
	return nil
}

// SessionAccessPayload and SessionRefreshPayload are compatibility aliases.
type SessionAccessPayload = AccessPayload
type SessionAccessClaims = AccessTokenClaims
type SessionRefreshPayload = RefreshPayload
type SessionRefreshClaims = RefreshTokenClaims

// NewSessionAccessPayload creates a session-mode access payload.
func NewSessionAccessPayload(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, duration time.Duration) (*SessionAccessPayload, error) {
	return NewSessionAccessPayloadAt(userID, email, name, sessionID, gen, globalVer, time.Now().UTC(), duration)
}

// NewSessionAccessPayloadAt creates a session-mode access payload at a provided time.
func NewSessionAccessPayloadAt(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, now time.Time, duration time.Duration) (*SessionAccessPayload, error) {
	return NewAccessPayloadAt(userID, email, name, sessionID, gen, globalVer, now, duration)
}

// NewSessionRefreshPayload creates a session-mode refresh payload.
func NewSessionRefreshPayload(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, duration time.Duration) (*SessionRefreshPayload, error) {
	return NewSessionRefreshPayloadAt(userID, email, name, sessionID, gen, globalVer, time.Now().UTC(), duration)
}

// NewSessionRefreshPayloadAt creates a session-mode refresh payload at a provided time.
func NewSessionRefreshPayloadAt(userID uuid.UUID, email, name string, sessionID uuid.UUID, gen int64, globalVer int, now time.Time, duration time.Duration) (*SessionRefreshPayload, error) {
	return NewRefreshPayloadAt(userID, email, name, sessionID, gen, globalVer, now, duration)
}

func sessionAccessPayloadFromClaims(claims *SessionAccessClaims) (*SessionAccessPayload, error) {
	return accessPayloadFromTokenClaims((*AccessTokenClaims)(claims))
}

func sessionRefreshPayloadFromClaims(claims *SessionRefreshClaims) (*SessionRefreshPayload, error) {
	return refreshPayloadFromTokenClaims((*RefreshTokenClaims)(claims))
}
