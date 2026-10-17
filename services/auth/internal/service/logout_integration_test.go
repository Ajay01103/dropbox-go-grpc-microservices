//go:build integration

// End-to-end verification of logout against the real dev ScyllaDB:
//
//	go test -tags integration ./internal/service/...
//
// The bug this pins was only observable against real storage. Logout deleted the
// cache entry and left the `user_sessions` row alone, so the very next refresh
// took its cache-miss path back to that row, matched the generation counter, and
// minted a brand-new pair: a logged-out refresh token kept working for the rest
// of the row's TTL. Driving the real service against the real session store is
// what proves the row itself is now the revocation.
package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/auth/config"
	"github.com/Ajay01103/go-dropbox/auth/db"
	"github.com/Ajay01103/go-dropbox/auth/internal/repository"
	"github.com/Ajay01103/go-dropbox/auth/internal/scyllastore"
	"github.com/Ajay01103/go-dropbox/auth/internal/tokencache"
	"github.com/Ajay01103/go-dropbox/pkg/token"
)

func authIntegrationSession(t *testing.T) *gocql.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, err := db.Connect(ctx, db.Config{
		// 127.0.0.1, not "localhost": on Windows localhost resolves to ::1 first
		// and the container's published CQL port is IPv4-only.
		Hosts: []string{"127.0.0.1"}, Port: 9042,
		Username: "cassandra", Password: "cassandra",
		Consistency: gocql.LocalQuorum, Datacenter: "datacenter1", ReplicationFactor: 1,
	})
	if err != nil {
		t.Skipf("ScyllaDB unavailable: %v", err)
	}
	t.Cleanup(session.Close)

	if _, err := db.Migrate(ctx, session); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return session
}

// cleanupAuthTestUser removes every row the registered throwaway user can own, so
// running the suite leaves the dev keyspace as it found it.
func cleanupAuthTestUser(t *testing.T, session *gocql.Session, userID, email string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, stmt := range []struct {
		query string
		arg   string
	}{
		{query: `DELETE FROM users WHERE id = ?`, arg: userID},
		{query: `DELETE FROM users_by_email WHERE email = ?`, arg: email},
		{query: `DELETE FROM user_revocations WHERE user_id = ?`, arg: userID},
	} {
		if err := session.Query(stmt.query, stmt.arg).WithContext(ctx).Exec(); err != nil {
			t.Logf("cleanup %q failed: %v", stmt.query, err)
		}
	}

	iter := session.Query(`SELECT session_id FROM user_sessions WHERE user_id = ?`, userID).WithContext(ctx).Iter()
	var sessionID string
	for iter.Scan(&sessionID) {
		_ = session.Query(
			`DELETE FROM user_sessions WHERE user_id = ? AND session_id = ?`, userID, sessionID,
		).WithContext(ctx).Exec()
	}
	_ = iter.Close()
}

func TestIntegrationLogoutRevokesTheRefreshToken(t *testing.T) {
	session := authIntegrationSession(t)
	ctx := context.Background()

	cache, err := tokencache.NewRistrettoCache()
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	maker, err := token.NewEDDSAMaker()
	if err != nil {
		t.Fatalf("new eddsa maker: %v", err)
	}
	sessionStore := scyllastore.NewSessionStore(session)

	svc := New(
		repository.NewUserRepo(session),
		maker,
		sessionStore,
		scyllastore.NewRevocationStore(session),
		cache,
		config.Config{AccessTokenDuration: 15 * time.Minute, RefreshTokenDuration: 7 * 24 * time.Hour},
		zap.NewNop(),
	)

	email := fmt.Sprintf("logout-it-%s@example.com", uuid.NewString())
	const password = "correct horse battery staple"

	registered, err := svc.Register(ctx, email, "Logout Integration", password)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	userID := registered.User.ID
	t.Cleanup(func() { cleanupAuthTestUser(t, session, userID, email) })

	// A second device: its own login and therefore its own session, so the test
	// can also show that logging one out leaves the user's other one intact.
	other, err := svc.Login(ctx, email, password)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// The first device's session is live: one successful rotation, whose pair is
	// what it keeps using afterwards.
	rotatedFirst, err := svc.RefreshToken(ctx, registered.RefreshToken)
	if err != nil {
		t.Fatalf("refresh before logout: %v", err)
	}

	// ── The reported bug ───────────────────────────────────────────────────
	// The second device logs out with the token it was issued at login, then
	// tries to refresh with that same token — twice, as the original report did.
	// Both attempts used to succeed: the cache entry was gone, but ScyllaDB still
	// had the session row with a matching generation counter, so the miss path
	// simply minted a brand-new pair.
	if err := svc.Logout(ctx, other.RefreshToken); err != nil {
		t.Fatalf("logout: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		res, err := svc.RefreshToken(ctx, other.RefreshToken)
		if !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("refresh #%d after logout = (%v, %v), want ErrSessionNotFound", attempt, res, err)
		}
	}

	// The row really is gone, which is what makes every process agree.
	otherPayload, err := maker.VerifySessionRefreshToken(other.RefreshToken)
	if err != nil {
		t.Fatalf("verify refresh token: %v", err)
	}
	otherSessionID := otherPayload.SessionID.String()
	otherSession, err := sessionStore.GetSession(ctx, userID, otherSessionID)
	if err != nil {
		t.Fatalf("read session row: %v", err)
	}
	if otherSession != nil {
		t.Fatalf("logout left the session row behind: %+v", *otherSession)
	}

	// A process that still had that session cached (another replica, or a request
	// that raced the logout) must report a revoked session rather than a
	// retryable rotation race, and must drop the stale copy.
	cache.SetWithTTL(
		tokencache.SessionKey(otherSessionID),
		&CachedSession{Gen: otherPayload.Gen},
		tokencache.SessionStateCost,
		tokencache.SessionStateTTL,
	)
	cache.Wait()
	if _, err := svc.RefreshToken(ctx, other.RefreshToken); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("refresh from a stale cache after logout = %v, want ErrSessionNotFound", err)
	}
	if _, ok := cache.Get(tokencache.SessionKey(otherSessionID)); ok {
		t.Fatal("the revoked session stayed cached")
	}

	// The logged-out device's access token is dead too: validation can no longer
	// find the session it is anchored to.
	if _, err := svc.ValidateToken(ctx, other.AccessToken); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("validate the logged-out access token = %v, want ErrTokenRevoked", err)
	}

	// ── Session scope ──────────────────────────────────────────────────────
	// The user's other device keeps working: logout revoked one session, not the
	// account. (LogoutAllDevices is the endpoint that does that, via global_ver.)
	if _, err := svc.RefreshToken(ctx, rotatedFirst.RefreshToken); err != nil {
		t.Fatalf("a single-device logout revoked the user's other session: %v", err)
	}
}
