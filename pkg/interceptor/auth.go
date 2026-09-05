package interceptor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/Ajay01103/go-dropbox/pkg/jwks"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// contextKeyUserID is the context key for the authenticated user ID
type contextKey string

const (
	ContextKeyUserID      contextKey = "user_id"
	ContextKeyAccessToken contextKey = "access_token"
)

type TokenVerifier interface {
	Verify(context.Context, string) (*jwks.Claims, error)
}

type TokenRevocationChecker interface {
	ValidateAccessToken(context.Context, string) error
}

type TokenRevocationCheckerFunc func(context.Context, string) error

func (f TokenRevocationCheckerFunc) ValidateAccessToken(ctx context.Context, token string) error {
	return f(ctx, token)
}

type authInterceptor struct {
	validator         TokenVerifier
	revocationChecker TokenRevocationChecker
}

var _ connect.Interceptor = (*authInterceptor)(nil)

// NewAuthInterceptor creates an interceptor for JWT authentication.
func NewAuthInterceptor(validator TokenVerifier) *authInterceptor {
	return NewAuthInterceptorWithRevocation(validator, nil)
}

func NewAuthInterceptorWithRevocation(validator TokenVerifier, revocationChecker TokenRevocationChecker) *authInterceptor {
	return &authInterceptor{validator: validator, revocationChecker: revocationChecker}
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		newCtx, err := a.authenticate(ctx, req.Header().Get("Authorization"))
		if err != nil {
			return nil, err
		}
		return next(newCtx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		newCtx, err := a.authenticate(ctx, conn.RequestHeader().Get("Authorization"))
		if err != nil {
			return err
		}
		return next(newCtx, conn)
	}
}

func (a *authInterceptor) authenticate(ctx context.Context, authHeader string) (context.Context, error) {
	if authHeader == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authorization token is not provided"))
	}

	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid authorization token format"))
	}

	tokenStr := authHeader[len(bearerPrefix):]
	if tokenStr == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authorization token is empty"))
	}

	claims, err := a.validator.Verify(ctx, tokenStr)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("token validation failed: %w", err))
	}
	if claims == nil || claims.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("token subject is missing"))
	}
	if claims.TokenType != "access" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("token is not an access token"))
	}
	if claims.SessionID == "" || claims.Generation <= 0 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("token session claims are missing"))
	}
	if a.revocationChecker != nil {
		if err := a.revocationChecker.ValidateAccessToken(ctx, tokenStr); err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("token revocation check failed: %w", err))
		}
	}

	newCtx := context.WithValue(ctx, ContextKeyUserID, claims.Subject)
	return context.WithValue(newCtx, ContextKeyAccessToken, tokenStr), nil
}

// UserIDFromContext extracts the authenticated user ID from the request context.
// Returns an error if the user ID is not present or invalid.
func UserIDFromContext(ctx context.Context) (uuid.UUID, error) {
	val := ctx.Value(ContextKeyUserID)
	if val == nil {
		return uuid.UUID{}, errors.New("user id not found in context")
	}

	userIDStr, ok := val.(string)
	if !ok {
		return uuid.UUID{}, errors.New("user id is not a string")
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("invalid user id format: %w", err)
	}

	return userID, nil
}

func AccessTokenFromContext(ctx context.Context) (string, error) {
	val := ctx.Value(ContextKeyAccessToken)
	token, ok := val.(string)
	if !ok || token == "" {
		return "", errors.New("access token not found in context")
	}
	return token, nil
}

// AuthInterceptor is a compatibility wrapper for callers that expect a net/http middleware-style helper.
func AuthInterceptor(next http.Handler, logger *zap.Logger) http.Handler {
	if logger != nil {
		logger.Debug("auth interceptor passthrough enabled")
	}
	return next
}
