package token

import (
	"errors"
	"time"
)

// Sentinel errors for token validation
var (
	ErrExpiredToken = errors.New("token has expired")
	ErrInvalidToken = errors.New("token is invalid")
)

// TokenMaker is the interface for creating and verifying JWT tokens.
type TokenMaker interface {
	// CreateRefreshToken mints a refresh token anchored to a session row and generation.
	CreateRefreshToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *RefreshPayload, error)

	// CreateAccessToken mints an access token anchored to the same session row and generation.
	CreateAccessToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *AccessPayload, error)

	// VerifyAccessToken parses and validates an access token string.
	VerifyAccessToken(token string) (*AccessPayload, error)

	// VerifyRefreshToken parses and validates a refresh token string.
	VerifyRefreshToken(token string) (*RefreshPayload, error)

	// CreateSessionRefreshToken mints a session-mode refresh token.
	CreateSessionRefreshToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *SessionRefreshPayload, error)

	// CreateSessionAccessToken mints a session-mode access token.
	CreateSessionAccessToken(userID, email, name, sessionID string, gen int64, globalVer int, duration time.Duration) (string, *SessionAccessPayload, error)

	// VerifySessionRefreshToken parses and validates a session-mode refresh token.
	VerifySessionRefreshToken(token string) (*SessionRefreshPayload, error)

	// VerifySessionAccessToken parses and validates a session-mode access token.
	VerifySessionAccessToken(token string) (*SessionAccessPayload, error)

	// GetCurrentKeyID returns the current active signing key ID.
	GetCurrentKeyID() string
}
