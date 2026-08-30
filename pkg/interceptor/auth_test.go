package interceptor

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/Ajay01103/go-notion/pkg/jwks"
)

type stubVerifier struct {
	claims *jwks.Claims
}

func (s stubVerifier) Verify(context.Context, string) (*jwks.Claims, error) {
	return s.claims, nil
}

func TestAuthInterceptorRejectsNonAccessToken(t *testing.T) {
	interceptor := NewAuthInterceptor(stubVerifier{claims: &jwks.Claims{
		Subject:    "11111111-1111-1111-1111-111111111111",
		TokenType:  "refresh",
		SessionID:  "22222222-2222-2222-2222-222222222222",
		Generation: 1,
	}})

	next := interceptor(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("handler must not receive a refresh token")
		return nil, nil
	})

	req := connect.NewRequest(&struct{}{})
	req.Header().Set("Authorization", "Bearer refresh-token")
	_, err := next(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", connect.CodeOf(err))
	}
}

func TestAuthInterceptorRejectsMissingTokenType(t *testing.T) {
	interceptor := NewAuthInterceptor(stubVerifier{claims: &jwks.Claims{
		Subject:    "11111111-1111-1111-1111-111111111111",
		SessionID:  "22222222-2222-2222-2222-222222222222",
		Generation: 1,
	}})

	next := interceptor(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("handler must not receive a token without a type")
		return nil, nil
	})

	req := connect.NewRequest(&struct{}{})
	req.Header().Set("Authorization", "Bearer token-without-type")
	_, err := next(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", connect.CodeOf(err))
	}
}

func TestAuthInterceptorRejectsRevokedAccessToken(t *testing.T) {
	authInterceptor := NewAuthInterceptorWithRevocation(
		stubVerifier{claims: &jwks.Claims{
			Subject:    "11111111-1111-1111-1111-111111111111",
			TokenType:  "access",
			SessionID:  "22222222-2222-2222-2222-222222222222",
			Generation: 1,
		}},
		TokenRevocationCheckerFunc(func(context.Context, string) error {
			return errors.New("session revoked")
		}),
	)

	next := authInterceptor(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("handler must not receive a revoked token")
		return nil, nil
	})

	req := connect.NewRequest(&struct{}{})
	req.Header().Set("Authorization", "Bearer revoked-token")
	_, err := next(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", connect.CodeOf(err))
	}
}
