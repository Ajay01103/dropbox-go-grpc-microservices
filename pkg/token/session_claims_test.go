package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestJWTMaker_SessionTokenRoundTrip(t *testing.T) {
	maker, err := NewJWTMaker("abcdefghijklmnopqrstuvwxyz0123456789abcdef")
	if err != nil {
		t.Fatalf("new jwt maker: %v", err)
	}

	refreshToken, refreshPayload, err := maker.CreateSessionRefreshToken(
		"11111111-1111-1111-1111-111111111111",
		"user@example.com",
		"User One",
		"22222222-2222-2222-2222-222222222222",
		7,
		3,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("create session refresh token: %v", err)
	}

	parsedRefresh, err := maker.VerifySessionRefreshToken(refreshToken)
	if err != nil {
		t.Fatalf("verify session refresh token: %v", err)
	}
	if parsedRefresh.SessionID != refreshPayload.SessionID {
		t.Fatalf("expected session id %s, got %s", refreshPayload.SessionID, parsedRefresh.SessionID)
	}
	if parsedRefresh.Gen != 7 || parsedRefresh.GlobalVer != 3 {
		t.Fatalf("unexpected refresh claims: %+v", parsedRefresh)
	}

	accessToken, accessPayload, err := maker.CreateSessionAccessToken(
		"11111111-1111-1111-1111-111111111111",
		"user@example.com",
		"User One",
		"22222222-2222-2222-2222-222222222222",
		7,
		3,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("create session access token: %v", err)
	}

	parsedAccess, err := maker.VerifySessionAccessToken(accessToken)
	if err != nil {
		t.Fatalf("verify session access token: %v", err)
	}
	if parsedAccess.GlobalVer != accessPayload.GlobalVer {
		t.Fatalf("expected global ver %d, got %d", accessPayload.GlobalVer, parsedAccess.GlobalVer)
	}
	if parsedAccess.SessionID.String() != "22222222-2222-2222-2222-222222222222" || parsedAccess.Gen != 7 {
		t.Fatalf("unexpected session anchoring claims: %+v", parsedAccess)
	}
}

func TestEDDSAMaker_SessionTokenRoundTrip(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	maker, err := NewEDDSAMakerFromPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("new eddsa maker: %v", err)
	}

	refreshToken, _, err := maker.CreateSessionRefreshToken(
		"11111111-1111-1111-1111-111111111111",
		"user@example.com",
		"User One",
		"22222222-2222-2222-2222-222222222222",
		9,
		5,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("create session refresh token: %v", err)
	}
	if _, err := maker.VerifySessionRefreshToken(refreshToken); err != nil {
		t.Fatalf("verify session refresh token: %v", err)
	}

	accessToken, _, err := maker.CreateSessionAccessToken(
		"11111111-1111-1111-1111-111111111111",
		"user@example.com",
		"User One",
		"22222222-2222-2222-2222-222222222222",
		9,
		5,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("create session access token: %v", err)
	}
	if _, err := maker.VerifySessionAccessToken(accessToken); err != nil {
		t.Fatalf("verify session access token: %v", err)
	}
}
