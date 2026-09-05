package interceptor

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/Ajay01103/go-dropbox/pkg/jwks"
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

	next := interceptor.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
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

	next := interceptor.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
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

	next := authInterceptor.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
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

func TestAuthInterceptorInjectsUserAndAccessToken(t *testing.T) {
	authInterceptor := NewAuthInterceptor(stubVerifier{claims: &jwks.Claims{
		Subject:    "11111111-1111-1111-1111-111111111111",
		TokenType:  "access",
		SessionID:  "22222222-2222-2222-2222-222222222222",
		Generation: 1,
	}})

	next := authInterceptor.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		userID, err := UserIDFromContext(ctx)
		if err != nil {
			return nil, err
		}
		if userID.String() != "11111111-1111-1111-1111-111111111111" {
			t.Fatalf("unexpected user ID: %s", userID)
		}
		token, err := AccessTokenFromContext(ctx)
		if err != nil {
			return nil, err
		}
		if token != "access-token" {
			t.Fatalf("unexpected access token: %s", token)
		}
		return nil, nil
	})

	req := connect.NewRequest(&struct{}{})
	req.Header().Set("Authorization", "Bearer access-token")
	if _, err := next(context.Background(), req); err != nil {
		t.Fatalf("expected authenticated request, got %v", err)
	}
}

type testStreamingConn struct {
	header http.Header
}

func (c *testStreamingConn) Spec() connect.Spec { return connect.Spec{} }
func (c *testStreamingConn) Peer() connect.Peer { return connect.Peer{} }
func (c *testStreamingConn) Receive(any) error {
	return errors.New("receive should not be called by interceptor")
}
func (c *testStreamingConn) RequestHeader() http.Header   { return c.header }
func (c *testStreamingConn) Send(any) error               { return nil }
func (c *testStreamingConn) ResponseHeader() http.Header  { return http.Header{} }
func (c *testStreamingConn) ResponseTrailer() http.Header { return http.Header{} }

func TestAuthInterceptorAuthenticatesStreamingBeforeHandler(t *testing.T) {
	authInterceptor := NewAuthInterceptor(stubVerifier{claims: &jwks.Claims{
		Subject:    "11111111-1111-1111-1111-111111111111",
		TokenType:  "access",
		SessionID:  "22222222-2222-2222-2222-222222222222",
		Generation: 1,
	}})

	called := false
	handler := authInterceptor.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		called = true
		if _, err := UserIDFromContext(ctx); err != nil {
			return err
		}
		return nil
	})

	conn := &testStreamingConn{header: http.Header{"Authorization": []string{"Bearer stream-token"}}}
	if err := handler(context.Background(), conn); err != nil {
		t.Fatalf("expected authenticated stream, got %v", err)
	}
	if !called {
		t.Fatal("expected streaming handler to be called")
	}
}
