package jwks

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

type JWKSStore interface {
	Load(ctx context.Context, serviceID string) ([]byte, error)
	Save(ctx context.Context, serviceID string, jwksJSON []byte) error
}

type scyllaStore struct {
	session *gocql.Session
}

const (
	authJWKSKeyspace = "auth_ks"
	currentJWKSID    = "current"
)

func NewScyllaStore(session *gocql.Session) JWKSStore {
	return &scyllaStore{session: session}
}

func (s *scyllaStore) Load(ctx context.Context, serviceID string) ([]byte, error) {
	data, err := s.loadByID(ctx, serviceID)
	if err == nil {
		return data, nil
	}
	if err != gocql.ErrNotFound {
		return nil, fmt.Errorf("jwks scylla load: %w", err)
	}

	if serviceID == currentJWKSID {
		return nil, nil
	}

	data, err = s.loadByID(ctx, currentJWKSID)
	if err == gocql.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("jwks scylla load: %w", err)
	}

	return data, nil
}

func (s *scyllaStore) Save(ctx context.Context, serviceID string, jwksJSON []byte) error {
	now := time.Now().UTC()
	if serviceID == "" {
		serviceID = currentJWKSID
	}

	return s.session.Query(
		`INSERT INTO auth_ks.jwks_public_keys (id, jwks_json, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		serviceID,
		string(jwksJSON),
		now.UnixNano(),
		now,
		now,
	).WithContext(ctx).Exec()
}

func (s *scyllaStore) loadByID(ctx context.Context, id string) ([]byte, error) {
	var jwksJSON string
	err := s.session.Query(
		`SELECT jwks_json FROM auth_ks.jwks_public_keys WHERE id = ? LIMIT 1`,
		id,
	).WithContext(ctx).Scan(&jwksJSON)
	if err != nil {
		return nil, err
	}

	return []byte(jwksJSON), nil
}