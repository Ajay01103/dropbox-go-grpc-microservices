package scyllastore

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

type SessionRecord struct {
	UserID    string
	SessionID string
	Gen       int64
	DeviceFP  string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type UserRevocationRecord struct {
	UserID    string
	GlobalVer int
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SessionStore struct {
	session *gocql.Session
}

func NewSessionStore(session *gocql.Session) *SessionStore {
	return &SessionStore{session: session}
}

func NewRevocationStore(session *gocql.Session) *SessionStore {
	return &SessionStore{session: session}
}

func (s *SessionStore) GetSession(ctx context.Context, userID, sessionID string) (*SessionRecord, error) {
	var (
		storedUserID string
		storedSessID string
		gen         int64
		deviceFP    string
		expiresAt   time.Time
		createdAt   time.Time
		updatedAt   time.Time
	)

	err := s.session.Query(
		`SELECT user_id, session_id, gen, device_fp, expires_at, created_at, updated_at
		FROM user_sessions WHERE user_id = ? AND session_id = ? LIMIT 1`,
		userID, sessionID,
	).WithContext(ctx).Scan(&storedUserID, &storedSessID, &gen, &deviceFP, &expiresAt, &createdAt, &updatedAt)
	if err == gocql.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	return &SessionRecord{
		UserID:    storedUserID,
		SessionID: storedSessID,
		Gen:       gen,
		DeviceFP:  deviceFP,
		ExpiresAt: expiresAt,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func (s *SessionStore) StoreSession(ctx context.Context, rec SessionRecord, ttl time.Duration) error {
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	if err := s.session.Query(
		`INSERT INTO user_sessions (user_id, session_id, gen, device_fp, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) USING TTL ?`,
		rec.UserID, rec.SessionID, rec.Gen, rec.DeviceFP, rec.ExpiresAt, rec.CreatedAt, rec.UpdatedAt, int(ttl.Seconds()),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("store session: %w", err)
	}

	return nil
}

func (s *SessionStore) AtomicBumpGen(ctx context.Context, userID, sessionID string, expectGen, newGen int64) (bool, error) {
	var currentGen int64
	// USING TTL 604800 (7 days) resets TTL on every rotation, ensuring row lives exactly 7D from last use
	applied, err := s.session.Query(
		`UPDATE user_sessions USING TTL 604800 SET gen = ?, updated_at = ? WHERE user_id = ? AND session_id = ? IF gen = ?`,
		newGen, time.Now().UTC(), userID, sessionID, expectGen,
	).WithContext(ctx).ScanCAS(&currentGen)
	if err != nil {
		return false, fmt.Errorf("bump session gen: %w", err)
	}
	return applied, nil
}

func (s *SessionStore) GetUserRevocation(ctx context.Context, userID string) (*UserRevocationRecord, error) {
	var (
		storedUserID string
		globalVer    int
		createdAt    time.Time
		updatedAt    time.Time
	)
	err := s.session.Query(
		`SELECT user_id, global_ver, created_at, updated_at FROM user_revocations WHERE user_id = ? LIMIT 1`,
		userID,
	).WithContext(ctx).Scan(&storedUserID, &globalVer, &createdAt, &updatedAt)
	if err == gocql.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user revocation: %w", err)
	}

	return &UserRevocationRecord{
		UserID:    storedUserID,
		GlobalVer: globalVer,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func (s *SessionStore) StoreUserRevocation(ctx context.Context, rec UserRevocationRecord) error {
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	// USING TTL 604800 (7 days) matches session lifetime; orphaned rows auto-expire
	if err := s.session.Query(
		`INSERT INTO user_revocations (user_id, global_ver, created_at, updated_at) VALUES (?, ?, ?, ?) USING TTL 604800`,
		rec.UserID, rec.GlobalVer, rec.CreatedAt, rec.UpdatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("store user revocation: %w", err)
	}
	return nil
}

func (s *SessionStore) BumpUserGlobalVer(ctx context.Context, userID string) (int, error) {
	rec, err := s.GetUserRevocation(ctx, userID)
	if err != nil {
		return 0, err
	}
	if rec == nil {
		rec = &UserRevocationRecord{UserID: userID, GlobalVer: 0}
	}
	newVer := rec.GlobalVer + 1
	if err := s.StoreUserRevocation(ctx, UserRevocationRecord{UserID: userID, GlobalVer: newVer}); err != nil {
		return 0, err
	}
	return newVer, nil
}

// DeleteSession removes a session from the database (used on theft detection)
func (s *SessionStore) DeleteSession(ctx context.Context, userID, sessionID string) error {
	if err := s.session.Query(
		`DELETE FROM user_sessions WHERE user_id = ? AND session_id = ?`,
		userID, sessionID,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
