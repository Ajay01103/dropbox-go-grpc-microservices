package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/sharing/gen/pb"
	"github.com/Ajay01103/go-dropbox/sharing/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/sharing/internal/service"
)

// SharingServer implements the Connect handler for the sharing service.
type SharingServer struct {
	pbconnect.UnimplementedSharingServiceHandler
	svc    *service.ShareService
	logger *zap.Logger
}

func New(svc *service.ShareService, logger *zap.Logger) *SharingServer {
	return &SharingServer{svc: svc, logger: logger}
}

func (s *SharingServer) CreateShare(ctx context.Context, req *pb.CreateShareRequest) (*pb.CreateShareResponse, error) {
	userID, ok := ctx.Value("user_id").(string)
	if !ok || userID == "" {
		return nil, newConnectError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.GetFileId() == "" || req.GetSharedWith() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id and shared_with are required"))
	}

	expiresAt := time.Time{}
	if req.GetExpiresAt() != "" {
		parsed, err := time.Parse(time.RFC3339, req.GetExpiresAt())
		if err != nil {
			return nil, newConnectError(connect.CodeInvalidArgument, fmt.Errorf("expires_at must be RFC3339: %w", err))
		}
		expiresAt = parsed
	}

	shareID, err := s.svc.CreateShare(ctx, req.GetFileId(), req.GetSharedWith(), userID, strings.ToLower(req.GetPermission()), expiresAt)
	if err != nil {
		s.logger.Error("CreateShare failed", zap.String("fileID", req.GetFileId()), zap.String("userID", userID), zap.Error(err))
		return nil, newConnectError(connect.CodeInternal, err)
	}
	return &pb.CreateShareResponse{ShareId: shareID, Success: true}, nil
}

func (s *SharingServer) RevokeShare(ctx context.Context, req *pb.RevokeShareRequest) (*pb.RevokeShareResponse, error) {
	_, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, newConnectError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.GetFileId() == "" || req.GetSharedWith() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id and shared_with are required"))
	}
	if err := s.svc.RevokeShare(ctx, req.GetFileId(), req.GetSharedWith()); err != nil {
		return nil, newConnectError(connect.CodeInternal, err)
	}
	return &pb.RevokeShareResponse{Success: true}, nil
}

func (s *SharingServer) ListSharesForFile(ctx context.Context, req *pb.ListSharesForFileRequest) (*pb.ListSharesForFileResponse, error) {
	_, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, newConnectError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	shares, err := s.svc.ListSharesForFile(ctx, req.GetFileId())
	if err != nil {
		return nil, newConnectError(connect.CodeInternal, err)
	}
	resp := &pb.ListSharesForFileResponse{Shares: make([]*pb.Share, 0, len(shares))}
	for _, share := range shares {
		resp.Shares = append(resp.Shares, &pb.Share{
			ShareId:    share.ShareID.String(),
			FileId:     share.FileID.String(),
			SharedWith: share.SharedWith.String(),
			Permission: share.Permission,
			GrantedBy:  share.GrantedBy.String(),
			ExpiresAt:  share.ExpiresAt.Format(time.RFC3339),
		})
	}
	return resp, nil
}

func (s *SharingServer) CheckAccess(ctx context.Context, req *pb.CheckAccessRequest) (*pb.CheckAccessResponse, error) {
	allowed, permission, owner, err := s.svc.CheckAccess(ctx, req.GetFileId(), req.GetUserId(), req.GetLinkToken(), req.GetAction())
	if err != nil {
		return nil, newConnectError(connect.CodeInternal, err)
	}
	return &pb.CheckAccessResponse{Allowed: allowed, Permission: permission, Owner: owner}, nil
}

func (s *SharingServer) CreatePublicLink(ctx context.Context, req *pb.CreatePublicLinkRequest) (*pb.CreatePublicLinkResponse, error) {
	userID, ok := ctx.Value("user_id").(string)
	if !ok || userID == "" {
		return nil, newConnectError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}
	if req.GetExpiresInSeconds() <= 0 {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("expires_in_seconds must be positive"))
	}
	linkToken, permission, expiresAt, err := s.svc.CreatePublicLink(ctx, req.GetFileId(), userID, strings.ToLower(req.GetPermission()), req.GetExpiresInSeconds())
	if err != nil {
		return nil, newConnectError(connect.CodeInternal, err)
	}
	return &pb.CreatePublicLinkResponse{LinkToken: linkToken, Permission: permission, ExpiresAt: expiresAt.Format(time.RFC3339)}, nil
}

func (s *SharingServer) RevokePublicLink(ctx context.Context, req *pb.RevokePublicLinkRequest) (*pb.RevokePublicLinkResponse, error) {
	_, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, newConnectError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.GetLinkToken() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("link_token is required"))
	}
	if err := s.svc.RevokePublicLink(ctx, req.GetLinkToken()); err != nil {
		return nil, newConnectError(connect.CodeInternal, err)
	}
	return &pb.RevokePublicLinkResponse{Success: true}, nil
}
