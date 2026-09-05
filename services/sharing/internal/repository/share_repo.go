package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

const (
	PermissionViewer = "viewer"
	PermissionEditor = "editor"
	PermissionOwner  = "owner"
)

type Share struct {
	ShareID    gocql.UUID
	FileID     gocql.UUID
	SharedWith gocql.UUID
	Permission string
	GrantedBy  gocql.UUID
	ExpiresAt  time.Time
}

type PublicLink struct {
	LinkToken string
	FileID    gocql.UUID
	Permission string
	ExpiresAt time.Time
	CreatedBy gocql.UUID
}

type ShareRepo struct {
	session *gocql.Session
}

func NewShareRepo(session *gocql.Session) *ShareRepo {
	return &ShareRepo{session: session}
}

func (r *ShareRepo) UpsertShare(ctx context.Context, fileID, shareID, sharedWith, grantedBy gocql.UUID, permission string, expiresAt time.Time) error {
	query := `INSERT INTO shares_by_file (file_id, share_id, shared_with, permission, granted_by, expires_at) VALUES (?, ?, ?, ?, ?, ?)`
	if err := r.session.Query(query, fileID, shareID, sharedWith, permission, grantedBy, expiresAt).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert shares_by_file: %w", err)
	}
	return nil
}

func (r *ShareRepo) UpsertReverseShare(ctx context.Context, userID, fileID gocql.UUID, permission string, expiresAt time.Time) error {
	query := `INSERT INTO shares_by_user (user_id, file_id, permission, expires_at) VALUES (?, ?, ?, ?)`
	if err := r.session.Query(query, userID, fileID, permission, expiresAt).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert shares_by_user: %w", err)
	}
	return nil
}

func (r *ShareRepo) DeleteShare(ctx context.Context, fileID, sharedWith gocql.UUID) error {
	var shareID gocql.UUID
	iter := r.session.Query(`SELECT share_id FROM shares_by_file WHERE file_id = ? AND shared_with = ? LIMIT 1`, fileID, sharedWith).Iter()
	for iter.Scan(&shareID) {
		break
	}
	if err := iter.Close(); err != nil {
		return fmt.Errorf("lookup share_id: %w", err)
	}
	if shareID == (gocql.UUID{}) {
		return nil
	}

	if err := r.session.Query(`DELETE FROM shares_by_file WHERE file_id = ? AND share_id = ?`, fileID, shareID).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete direct share: %w", err)
	}
	if err := r.session.Query(`DELETE FROM shares_by_user WHERE user_id = ? AND file_id = ?`, sharedWith, fileID).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete reverse share: %w", err)
	}
	return nil
}

func (r *ShareRepo) GetShareForUser(ctx context.Context, fileID, userID gocql.UUID) (*Share, error) {
	var shareID gocql.UUID
	var permission string
	var grantedBy gocql.UUID
	var expiresAt time.Time
	var sharedWith gocql.UUID

	query := `SELECT share_id, shared_with, permission, granted_by, expires_at FROM shares_by_file WHERE file_id = ?`
	iter := r.session.Query(query, fileID).WithContext(ctx).Iter()
	for iter.Scan(&shareID, &sharedWith, &permission, &grantedBy, &expiresAt) {
		if sharedWith == userID {
			return &Share{
				ShareID:    shareID,
				FileID:     fileID,
				SharedWith: sharedWith,
				Permission: permission,
				GrantedBy:  grantedBy,
				ExpiresAt:  expiresAt,
			}, nil
		}
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("scan direct shares: %w", err)
	}
	return nil, nil
}

func (r *ShareRepo) ListSharesForFile(ctx context.Context, fileID gocql.UUID) ([]Share, error) {
	var shares []Share
	iter := r.session.Query(`SELECT share_id, shared_with, permission, granted_by, expires_at FROM shares_by_file WHERE file_id = ?`, fileID).WithContext(ctx).Iter()
	var shareID, sharedWith, grantedBy gocql.UUID
	var permission string
	var expiresAt time.Time
	for iter.Scan(&shareID, &sharedWith, &permission, &grantedBy, &expiresAt) {
		shares = append(shares, Share{
			ShareID:    shareID,
			FileID:     fileID,
			SharedWith: sharedWith,
			Permission: permission,
			GrantedBy:  grantedBy,
			ExpiresAt:  expiresAt,
		})
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("scan shares_by_file: %w", err)
	}
	return shares, nil
}

func (r *ShareRepo) UpsertPublicLink(ctx context.Context, linkToken string, fileID, createdBy gocql.UUID, permission string, expiresAt time.Time) error {
	if err := r.session.Query(`INSERT INTO public_links (link_token, file_id, permission, expires_at, created_by) VALUES (?, ?, ?, ?, ?)`,
		linkToken, fileID, permission, expiresAt, createdBy,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert public_links: %w", err)
	}
	return nil
}

func (r *ShareRepo) GetPublicLinkByToken(ctx context.Context, token string) (*PublicLink, error) {
	var fileID gocql.UUID
	var permission string
	var expiresAt time.Time
	var createdBy gocql.UUID
	if err := r.session.Query(`SELECT file_id, permission, expires_at, created_by FROM public_links WHERE link_token = ? LIMIT 1`, token).WithContext(ctx).Consistency(gocql.LocalQuorum).Scan(&fileID, &permission, &expiresAt, &createdBy); err != nil {
		if err == gocql.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("load public link: %w", err)
	}
	return &PublicLink{LinkToken: token, FileID: fileID, Permission: permission, ExpiresAt: expiresAt, CreatedBy: createdBy}, nil
}

func (r *ShareRepo) RevokePublicLink(ctx context.Context, token string) error {
	if err := r.session.Query(`DELETE FROM public_links WHERE link_token = ?`, token).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete public link: %w", err)
	}
	return nil
}

func (r *ShareRepo) UpsertOwnerCache(ctx context.Context, fileID, ownerID gocql.UUID) error {
	if err := r.session.Query(`INSERT INTO owner_cache (file_id, owner_id) VALUES (?, ?)`, fileID, ownerID).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert owner cache: %w", err)
	}
	return nil
}

func (r *ShareRepo) GetOwnerID(ctx context.Context, fileID gocql.UUID) (gocql.UUID, bool, error) {
	var ownerID gocql.UUID
	if err := r.session.Query(`SELECT owner_id FROM owner_cache WHERE file_id = ? LIMIT 1`, fileID).WithContext(ctx).Scan(&ownerID); err != nil {
		if err == gocql.ErrNotFound {
			return gocql.UUID{}, false, nil
		}
		return gocql.UUID{}, false, fmt.Errorf("load owner cache: %w", err)
	}
	return ownerID, true, nil
}
