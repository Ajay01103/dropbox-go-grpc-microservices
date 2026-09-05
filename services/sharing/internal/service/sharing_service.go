package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
	redis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/sharing/config"
	"github.com/Ajay01103/go-dropbox/sharing/internal/repository"
)

var (
	ErrInvalidPermission = errors.New("invalid permission")
	ErrMissingUserID     = errors.New("missing user id")
	ErrUserNotOwner      = errors.New("user is not the owner")
	ErrShareNotFound     = errors.New("share not found")
)

type ShareService struct {
	repo        *repository.ShareRepo
	redis       *redis.Client
	cfg         config.Config
	logger      *zap.Logger
	metadataURL string
}

func New(repo *repository.ShareRepo, redis *redis.Client, cfg config.Config, logger *zap.Logger) *ShareService {
	return &ShareService{repo: repo, redis: redis, cfg: cfg, logger: logger, metadataURL: cfg.MetadataURL}
}

func (s *ShareService) CreateShare(ctx context.Context, fileID, sharedWith, grantedBy, permission string, expiresAt time.Time) (string, error) {
	if fileID == "" || sharedWith == "" || grantedBy == "" {
		return "", errors.New("file_id, shared_with, and granted_by are required")
	}
	if !isValidPermission(permission) {
		return "", ErrInvalidPermission
	}

	fileUUID, err := parseUUID(fileID)
	if err != nil {
		return "", err
	}
	sharedUUID, err := parseUUID(sharedWith)
	if err != nil {
		return "", err
	}
	grantedUUID, err := parseUUID(grantedBy)
	if err != nil {
		return "", err
	}

	if fileUUID == (gocql.UUID{}) || sharedUUID == (gocql.UUID{}) || grantedUUID == (gocql.UUID{}) {
		return "", errors.New("invalid UUID values")
	}

	// Idempotency: if a share exists for the same user/file pair, update instead of creating duplicates.
	existing, err := s.repo.GetShareForUser(ctx, fileUUID, sharedUUID)
	if err != nil {
		return "", fmt.Errorf("lookup existing share: %w", err)
	}
	var shareID gocql.UUID
	if existing != nil {
		shareID = existing.ShareID
	} else {
		shareID = gocql.TimeUUID()
	}

	if err := s.repo.UpsertShare(ctx, fileUUID, shareID, sharedUUID, grantedUUID, permission, expiresAt); err != nil {
		return "", fmt.Errorf("upsert direct share: %w", err)
	}
	if err := s.repo.UpsertReverseShare(ctx, sharedUUID, fileUUID, permission, expiresAt); err != nil {
		return "", fmt.Errorf("upsert reverse share: %w", err)
	}

	if err := s.invalidateAccessCache(ctx, sharedUUID, fileUUID); err != nil {
		s.logger.Warn("failed to invalidate access cache for share update", zap.Error(err))
	}
	return shareID.String(), nil
}

func (s *ShareService) RevokeShare(ctx context.Context, fileID, sharedWith string) error {
	if fileID == "" || sharedWith == "" {
		return errors.New("file_id and shared_with are required")
	}
	fileUUID, err := parseUUID(fileID)
	if err != nil {
		return err
	}
	userUUID, err := parseUUID(sharedWith)
	if err != nil {
		return err
	}

	if err := s.repo.DeleteShare(ctx, fileUUID, userUUID); err != nil {
		return fmt.Errorf("delete share: %w", err)
	}
	if err := s.invalidateAccessCache(ctx, userUUID, fileUUID); err != nil {
		return fmt.Errorf("invalidate access cache: %w", err)
	}
	return nil
}

func (s *ShareService) ListSharesForFile(ctx context.Context, fileID string) ([]repository.Share, error) {
	fileUUID, err := parseUUID(fileID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListSharesForFile(ctx, fileUUID)
}

func (s *ShareService) CheckAccess(ctx context.Context, fileID, userID, linkToken, action string) (bool, string, bool, error) {
	if fileID == "" {
		return false, "", false, errors.New("file_id is required")
	}

	fileUUID, err := parseUUID(fileID)
	if err != nil {
		return false, "", false, err
	}

	if userID != "" {
		userUUID, err := parseUUID(userID)
		if err != nil {
			return false, "", false, err
		}
		if cached, ok := s.getCachedAccess(userUUID, fileUUID); ok {
			if cached == "deny" {
				return false, "", false, nil
			}
			if actionAllowed(cached, action) {
				return true, cached, false, nil
			}
			return false, cached, false, nil
		}
		if ownerID, ok, err := s.repo.GetOwnerID(ctx, fileUUID); err == nil && ok && ownerID == userUUID {
			if actionAllowed("owner", action) {
				s.setCachedAccess(userUUID, fileUUID, "owner")
				return true, "owner", true, nil
			}
			return false, "owner", true, nil
		}

		share, err := s.repo.GetShareForUser(ctx, fileUUID, userUUID)
		if err != nil {
			return false, "", false, err
		}
		if share != nil && !share.ExpiresAt.IsZero() && share.ExpiresAt.Before(time.Now()) {
			return false, share.Permission, false, nil
		}
		if share != nil {
			if actionAllowed(share.Permission, action) {
				s.setCachedAccess(userUUID, fileUUID, share.Permission)
				return true, share.Permission, false, nil
			}
			return false, share.Permission, false, nil
		}
		if linkToken != "" {
			link, err := s.repo.GetPublicLinkByToken(ctx, linkToken)
			if err != nil {
				return false, "", false, err
			}
			if link != nil && link.FileID == fileUUID && !link.ExpiresAt.IsZero() && link.ExpiresAt.After(time.Now()) {
				if actionAllowed(link.Permission, action) {
					s.setCachedAccess(userUUID, fileUUID, link.Permission)
					return true, link.Permission, false, nil
				}
				return false, link.Permission, false, nil
			}
		}
		s.setCachedAccess(userUUID, fileUUID, "deny")
		return false, "", false, nil
	}

	if linkToken == "" {
		s.setCachedAccess(gocql.UUID{}, fileUUID, "deny")
		return false, "", false, nil
	}

	link, err := s.repo.GetPublicLinkByToken(ctx, linkToken)
	if err != nil {
		return false, "", false, err
	}
	if link == nil || link.FileID != fileUUID || link.ExpiresAt.Before(time.Now()) {
		s.setCachedAccess(gocql.UUID{}, fileUUID, "deny")
		return false, "", false, nil
	}
	if actionAllowed(link.Permission, action) {
		return true, link.Permission, false, nil
	}
	return false, link.Permission, false, nil
}

func (s *ShareService) CreatePublicLink(ctx context.Context, fileID, createdBy, permission string, expiresInSeconds int64) (string, string, time.Time, error) {
	if fileID == "" || createdBy == "" {
		return "", "", time.Time{}, errors.New("file_id and created_by are required")
	}
	if !isValidPermission(permission) {
		return "", "", time.Time{}, ErrInvalidPermission
	}

	fileUUID, err := parseUUID(fileID)
	if err != nil {
		return "", "", time.Time{}, err
	}
	createdUUID, err := parseUUID(createdBy)
	if err != nil {
		return "", "", time.Time{}, err
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	expiresAt := time.Now().Add(time.Duration(expiresInSeconds) * time.Second)
	if err := s.repo.UpsertPublicLink(ctx, token, fileUUID, createdUUID, permission, expiresAt); err != nil {
		return "", "", time.Time{}, fmt.Errorf("store public link: %w", err)
	}
	return token, permission, expiresAt, nil
}

func (s *ShareService) RevokePublicLink(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("link_token is required")
	}
	if err := s.repo.RevokePublicLink(ctx, token); err != nil {
		return fmt.Errorf("revoke public link: %w", err)
	}
	return nil
}

func isValidPermission(permission string) bool {
	switch strings.ToLower(permission) {
	case repository.PermissionViewer, repository.PermissionEditor, repository.PermissionOwner:
		return true
	default:
		return false
	}
}

func actionAllowed(permission, action string) bool {
	permission = strings.ToLower(permission)
	action = strings.ToLower(action)
	if action == "read" || action == "view" || action == "download" {
		return permission == repository.PermissionViewer || permission == repository.PermissionEditor || permission == repository.PermissionOwner
	}
	if action == "write" || action == "edit" || action == "upload" {
		return permission == repository.PermissionEditor || permission == repository.PermissionOwner
	}
	if action == "owner" {
		return permission == repository.PermissionOwner
	}
	return false
}

func parseUUID(v string) (gocql.UUID, error) {
	if v == "" {
		return gocql.UUID{}, errors.New("uuid is empty")
	}
	u, err := gocql.ParseUUID(v)
	if err != nil {
		return gocql.UUID{}, fmt.Errorf("parse uuid %q: %w", v, err)
	}
	return u, nil
}

func (s *ShareService) getCachedAccess(userID gocql.UUID, fileID gocql.UUID) (string, bool) {
	if s.redis == nil {
		return "", false
	}
	key := fmt.Sprintf("access:%s:%s", userID.String(), fileID.String())
	value, err := s.redis.Get(context.Background(), key).Result()
	if err == redis.Nil {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return value, true
}

func (s *ShareService) setCachedAccess(userID gocql.UUID, fileID gocql.UUID, permission string) {
	if s.redis == nil {
		return
	}
	key := fmt.Sprintf("access:%s:%s", userID.String(), fileID.String())
	_ = s.redis.Set(context.Background(), key, permission, time.Duration(s.cfg.AccessCacheTTLSeconds)*time.Second).Err()
}

func (s *ShareService) invalidateAccessCache(ctx context.Context, userID gocql.UUID, fileID gocql.UUID) error {
	if s.redis == nil {
		return nil
	}
	key := fmt.Sprintf("access:%s:%s", userID.String(), fileID.String())
	if err := s.redis.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("delete access cache key: %w", err)
	}
	return nil
}
