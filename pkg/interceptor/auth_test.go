package interceptor

import (
	"context"
	"errors"
	"testing"

	"github.com/Ajay01103/go-dropbox/pkg/jwks"
)

type stubVerifier struct {
	claims *jwks.Claims
}

func (s stubVerifier) Verify(context.Context, string) (*jwks.Claims, error) {
	return s.claims, nil
}

func TestAuthInterceptorRejectsNonAccessToken(t *testing.T) {
	a := NewAuthInterceptor(stubVerifier{claims: &jwks.Claims{
		Subject: "11111111-1111-1111-1111-111111111111", TokenType: "refresh",
		SessionID: "22222222-2222-2222-2222-222222222222", Generation: 1,
	}})
	if _, err := a.authenticate(context.Background(), "Bearer token"); err == nil {
		t.Fatal("expected refresh token to be rejected")
	}
}

func TestAuthInterceptorRejectsMissingTokenType(t *testing.T) {
	a := NewAuthInterceptor(stubVerifier{claims: &jwks.Claims{
		Subject:   "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222", Generation: 1,
	}})
	if _, err := a.authenticate(context.Background(), "Bearer token"); err == nil {
		t.Fatal("expected token without type to be rejected")
	}
}

func TestAuthInterceptorRejectsRevokedAccessToken(t *testing.T) {
	a := NewAuthInterceptorWithRevocation(
		stubVerifier{claims: &jwks.Claims{
			Subject: "11111111-1111-1111-1111-111111111111", TokenType: "access",
			SessionID: "22222222-2222-2222-2222-222222222222", Generation: 1,
		}},
		TokenRevocationCheckerFunc(func(context.Context, string) error {
			return errors.New("session revoked")
		}),
	)
	if _, err := a.authenticate(context.Background(), "Bearer token"); err == nil {
		t.Fatal("expected revoked token to be rejected")
	}
}

func TestAuthInterceptorInjectsUserAndAccessToken(t *testing.T) {
	a := NewAuthInterceptor(stubVerifier{claims: &jwks.Claims{
		Subject: "11111111-1111-1111-1111-111111111111", TokenType: "access",
		SessionID: "22222222-2222-2222-2222-222222222222", Generation: 1,
	}})
	ctx, err := a.authenticate(context.Background(), "Bearer access-token")
	if err != nil {
		t.Fatalf("expected authentication to succeed: %v", err)
	}
	userID, err := UserIDFromContext(ctx)
	if err != nil || userID.String() != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("unexpected user ID: %v", err)
	}
	token, err := AccessTokenFromContext(ctx)
	if err != nil || token != "access-token" {
		t.Fatalf("unexpected access token: %v", err)
	}
}
