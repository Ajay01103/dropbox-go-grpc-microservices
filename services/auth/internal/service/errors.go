package service

import "errors"

var (
	ErrEmailAlreadyExists   = errors.New("email already exists")
	ErrInvalidCredentials   = errors.New("invalid email or password")
	ErrTokenExpired         = errors.New("token has expired")
	ErrInvalidToken         = errors.New("token is invalid")
	ErrTokenRevoked         = errors.New("token has been revoked")
	ErrTokenReuseDetected   = errors.New("refresh token reuse detected")
	ErrRefreshFamilyMissing = errors.New("refresh token family missing")
	ErrUserNotFound         = errors.New("user not found")
	ErrReplayDetected       = errors.New("token replay detected: gen mismatch")
	ErrSessionNotFound      = errors.New("session not found")
	ErrConcurrentRotation   = errors.New("concurrent rotation detected: please retry")
)
