package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgraph-io/ristretto"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/auth/config"
	"github.com/Ajay01103/go-dropbox/auth/internal/scyllastore"
	"github.com/Ajay01103/go-dropbox/auth/internal/tokencache"
	"github.com/Ajay01103/go-dropbox/pkg/token"
	"github.com/google/uuid"
)

func TestGetCurrentUserProfile_UsesCacheHit(t *testing.T) {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e4,
		MaxCost:     1 << 20,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}

	userID := uuid.New()
	entry := tokencache.CurrentUserEntry{
		UserID: userID.String(),
		Email:  "cached@example.com",
		Name:   "Cached User",
	}
	cache.SetWithTTL(tokencache.CurrentUserKey(userID.String()), entry, tokencache.CurrentUserCost, tokencache.CurrentUserTTL)
	cache.Wait()

	svc := &AuthService{
		cache:  cache,
		logger: zap.NewNop(),
	}

	got, err := svc.GetCurrentUserProfile(context.Background(), userID)
	if err != nil {
		t.Fatalf("get current user profile: %v", err)
	}

	if got == nil {
		t.Fatal("expected cached current user profile")
	}
	if got.UserID != entry.UserID || got.Email != entry.Email || got.Name != entry.Name {
		t.Fatalf("unexpected cached profile: %#v", got)
	}
}

type fakeRevocationStore struct {
	bumpCalls int
	newGv     int
}

func (f *fakeRevocationStore) GetUserRevocation(context.Context, string) (*scyllastore.UserRevocationRecord, error) {
	return nil, nil
}

func (f *fakeRevocationStore) BumpUserGlobalVer(_ context.Context, _ string) (int, error) {
	f.bumpCalls++
	f.newGv++
	return f.newGv, nil
}

// ─── Logout session revocation ────────────────────────────────────────────────
//
// The bug these pin: Logout only deleted the cache entry. The next refresh took
// its ScyllaDB miss path straight back to the still-present session row, matched
// the generation counter, and minted a brand-new pair — so a logged-out refresh
// token stayed usable. Logout has to delete the session row, because that row is
// the anchor both tokens of the session are validated against.

// fakeSessionStore is the session anchor as the service uses it: one row per
// (user, session) carrying the generation counter.
type fakeSessionStore struct {
	sessions map[string]scyllastore.SessionRecord
	deletes  int
}

func sessionRowKey(userID, sessionID string) string { return userID + "|" + sessionID }

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{sessions: make(map[string]scyllastore.SessionRecord)}
}

func (f *fakeSessionStore) GetSession(_ context.Context, userID, sessionID string) (*scyllastore.SessionRecord, error) {
	rec, ok := f.sessions[sessionRowKey(userID, sessionID)]
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (f *fakeSessionStore) StoreSession(_ context.Context, rec scyllastore.SessionRecord, _ time.Duration) error {
	f.sessions[sessionRowKey(rec.UserID, rec.SessionID)] = rec
	return nil
}

func (f *fakeSessionStore) AtomicBumpGen(_ context.Context, userID, sessionID string, expectGen, newGen int64) (bool, error) {
	rec, ok := f.sessions[sessionRowKey(userID, sessionID)]
	if !ok || rec.Gen != expectGen {
		return false, nil
	}
	rec.Gen = newGen
	f.sessions[sessionRowKey(userID, sessionID)] = rec
	return true, nil
}

func (f *fakeSessionStore) DeleteSession(_ context.Context, userID, sessionID string) error {
	f.deletes++
	delete(f.sessions, sessionRowKey(userID, sessionID))
	return nil
}

func newLogoutTestService(t *testing.T, store sessionStore) (*AuthService, *ristretto.Cache, token.TokenMaker) {
	t.Helper()
	// The service signs with EdDSA (an in-memory keypair is enough here: the
	// tests exercise the session anchor, not key custody).
	maker, err := token.NewEDDSAMaker()
	if err != nil {
		t.Fatalf("new eddsa maker: %v", err)
	}
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e4,
		MaxCost:     1 << 20,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	svc := &AuthService{
		tokenMaker:      maker,
		sessionStore:    store,
		revocationStore: &fakeRevocationStore{},
		cache:           cache,
		cfg: config.Config{
			AccessTokenDuration:  15 * time.Minute,
			RefreshTokenDuration: 7 * 24 * time.Hour,
		},
		logger: zap.NewNop(),
	}
	return svc, cache, maker
}

// seedSession writes the row a login would have written and mints the token pair
// anchored to it.
func seedSession(
	t *testing.T,
	maker token.TokenMaker,
	store *fakeSessionStore,
	userID, sessionID string,
	gen int64,
) (accessToken, refreshToken string) {
	t.Helper()
	now := time.Now().UTC()
	store.sessions[sessionRowKey(userID, sessionID)] = scyllastore.SessionRecord{
		UserID: userID, SessionID: sessionID, Gen: gen,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	refreshToken, _, err := maker.CreateSessionRefreshToken(
		userID, "user@example.com", "User", sessionID, gen, 0, 7*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("create session refresh token: %v", err)
	}
	accessToken, _, err = maker.CreateSessionAccessToken(
		userID, "user@example.com", "User", sessionID, gen, 0, 15*time.Minute,
	)
	if err != nil {
		t.Fatalf("create session access token: %v", err)
	}
	return accessToken, refreshToken
}

func TestLogoutRevokesBothTokensOfTheSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeSessionStore()
	svc, cache, maker := newLogoutTestService(t, store)
	userID, sessionID := uuid.NewString(), uuid.NewString()
	accessToken, refreshToken := seedSession(t, maker, store, userID, sessionID, 1)
	// A fresh login caches the session state, exactly as mintSessionTokenPair does.
	cache.SetWithTTL(tokencache.SessionKey(sessionID), &CachedSession{Gen: 1}, tokencache.SessionStateCost, tokencache.SessionStateTTL)
	cache.Wait()

	if err := svc.Logout(ctx, refreshToken); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if store.deletes != 1 {
		t.Fatalf("session row deletes = %d, want 1", store.deletes)
	}
	if _, ok := cache.Get(tokencache.SessionKey(sessionID)); ok {
		t.Fatal("logout left the cached session state in place")
	}

	// The regression: this used to succeed — and keep succeeding — because the
	// row was still there for the cache-miss path to find and rotate.
	for attempt := 1; attempt <= 2; attempt++ {
		res, err := svc.RefreshToken(ctx, refreshToken)
		if !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("refresh #%d after logout = (%v, %v), want ErrSessionNotFound", attempt, res, err)
		}
	}

	// The access token minted with the same session is dead too: validation can
	// no longer find the session it is anchored to.
	if _, err := svc.ValidateToken(ctx, accessToken); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("validate access token after logout = %v, want ErrTokenRevoked", err)
	}
}

func TestRefreshAgainstARemotelyRevokedSessionIsNotARetryableRace(t *testing.T) {
	ctx := context.Background()
	store := newFakeSessionStore()
	svc, cache, maker := newLogoutTestService(t, store)
	userID, sessionID := uuid.NewString(), uuid.NewString()
	_, refreshToken := seedSession(t, maker, store, userID, sessionID, 1)

	// This process still has the session cached while the row is gone (another
	// process served the logout, or the same user logged out elsewhere), so the
	// refresh takes its cache-hit path and the generation CAS cannot apply.
	delete(store.sessions, sessionRowKey(userID, sessionID))
	cache.SetWithTTL(tokencache.SessionKey(sessionID), &CachedSession{Gen: 1}, tokencache.SessionStateCost, tokencache.SessionStateTTL)
	cache.Wait()

	res, err := svc.RefreshToken(ctx, refreshToken)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("refresh against a revoked session = (%v, %v), want ErrSessionNotFound", res, err)
	}
	if _, ok := cache.Get(tokencache.SessionKey(sessionID)); ok {
		t.Fatal("the revoked session stayed cached after a refresh attempt")
	}
}

func TestLogoutIsIdempotentAndRejectsUnverifiableTokens(t *testing.T) {
	ctx := context.Background()
	store := newFakeSessionStore()
	svc, _, maker := newLogoutTestService(t, store)
	userID, sessionID := uuid.NewString(), uuid.NewString()
	_, refreshToken := seedSession(t, maker, store, userID, sessionID, 1)

	if err := svc.Logout(ctx, refreshToken); err != nil {
		t.Fatalf("first logout: %v", err)
	}
	// A client that retries the logout, or that logs out after another device
	// already revoked the session, must still get a success.
	if err := svc.Logout(ctx, refreshToken); err != nil {
		t.Fatalf("repeat logout: %v", err)
	}
	if store.deletes != 2 {
		t.Fatalf("session row deletes = %d, want 2 (delete is idempotent)", store.deletes)
	}

	if err := svc.Logout(ctx, "not-a-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("logout with an unverifiable token = %v, want ErrInvalidToken", err)
	}
}

func TestHandleTheftDetected_BumpsGlobalVersionAfterThreshold(t *testing.T) {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e4,
		MaxCost:     1 << 20,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}

	svc := &AuthService{
		revocationStore: &fakeRevocationStore{},
		cache:           cache,
		logger:          zap.NewNop(),
	}

	userID := uuid.NewString()
	for i := 0; i < tokencache.TheftThreshold; i++ {
		svc.handleTheftDetected(userID, uuid.NewString())
	}

	store := svc.revocationStore.(*fakeRevocationStore)
	if store.bumpCalls != 1 {
		t.Fatalf("expected one global version bump, got %d", store.bumpCalls)
	}
	if _, ok := cache.Get(tokencache.TheftCounterKey(userID)); ok {
		t.Fatal("expected theft counter to be cleared after escalation")
	}
}
