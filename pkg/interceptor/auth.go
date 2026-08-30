package interceptor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/Ajay01103/go-notion/pkg/jwks"
	"github.com/google/uuid"
)

// contextKeyUserID is the context key for the authenticated user ID
type contextKey string

const ContextKeyUserID contextKey = "user_id"

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

// NewAuthInterceptor creates a unary interceptor for JWT authentication.
// It validates bearer tokens via the provided verifier and injects the user ID into context.
// Unauthenticated requests are rejected before the handler is invoked.
func NewAuthInterceptor(validator TokenVerifier) connect.UnaryInterceptorFunc {
	return NewAuthInterceptorWithRevocation(validator, nil)
}

func NewAuthInterceptorWithRevocation(validator TokenVerifier, revocationChecker TokenRevocationChecker) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			// Extract bearer token from Authorization header
			authHeader := req.Header().Get("Authorization")
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

			// Validate the access token via the shared jwks verifier.
			claims, err := validator.Verify(ctx, tokenStr)
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
			if revocationChecker != nil {
				if err := revocationChecker.ValidateAccessToken(ctx, tokenStr); err != nil {
					return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("token revocation check failed: %w", err))
				}
			}

			// Inject the authenticated user ID into the request context
			newCtx := context.WithValue(ctx, ContextKeyUserID, claims.Subject)

			// Call the next handler with the enriched context
			return next(newCtx, req)
		}
	}
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
