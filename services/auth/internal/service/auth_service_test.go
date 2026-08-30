package service

import (
	"context"
	"testing"

	"github.com/dgraph-io/ristretto"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-notion/auth/internal/scyllastore"
	"github.com/Ajay01103/go-notion/auth/internal/tokencache"
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
