package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
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

func (s *SharingServer) CreateShare(ctx context.Context, req *connect.Request[pb.CreateShareRequest]) (*connect.Response[pb.CreateShareResponse], error) {
	userID, ok := ctx.Value("user_id").(string)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.Msg.GetFileId() == "" || req.Msg.GetSharedWith() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id and shared_with are required"))
	}

	expiresAt := time.Time{}
	if req.Msg.GetExpiresAt() != "" {
		parsed, err := time.Parse(time.RFC3339, req.Msg.GetExpiresAt())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("expires_at must be RFC3339: %w", err))
		}
		expiresAt = parsed
	}

	shareID, err := s.svc.CreateShare(ctx, req.Msg.GetFileId(), req.Msg.GetSharedWith(), userID, strings.ToLower(req.Msg.GetPermission()), expiresAt)
	if err != nil {
		s.logger.Error("CreateShare failed", zap.String("fileID", req.Msg.GetFileId()), zap.String("userID", userID), zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.CreateShareResponse{ShareId: shareID, Success: true}), nil
}

func (s *SharingServer) RevokeShare(ctx context.Context, req *connect.Request[pb.RevokeShareRequest]) (*connect.Response[pb.RevokeShareResponse], error) {
	_, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.Msg.GetFileId() == "" || req.Msg.GetSharedWith() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id and shared_with are required"))
	}
	if err := s.svc.RevokeShare(ctx, req.Msg.GetFileId(), req.Msg.GetSharedWith()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.RevokeShareResponse{Success: true}), nil
}

func (s *SharingServer) ListSharesForFile(ctx context.Context, req *connect.Request[pb.ListSharesForFileRequest]) (*connect.Response[pb.ListSharesForFileResponse], error) {
	_, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	shares, err := s.svc.ListSharesForFile(ctx, req.Msg.GetFileId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
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
	return connect.NewResponse(resp), nil
}

func (s *SharingServer) CheckAccess(ctx context.Context, req *connect.Request[pb.CheckAccessRequest]) (*connect.Response[pb.CheckAccessResponse], error) {
	allowed, permission, owner, err := s.svc.CheckAccess(ctx, req.Msg.GetFileId(), req.Msg.GetUserId(), req.Msg.GetLinkToken(), req.Msg.GetAction())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.CheckAccessResponse{Allowed: allowed, Permission: permission, Owner: owner}), nil
}

func (s *SharingServer) CreatePublicLink(ctx context.Context, req *connect.Request[pb.CreatePublicLinkRequest]) (*connect.Response[pb.CreatePublicLinkResponse], error) {
	userID, ok := ctx.Value("user_id").(string)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.Msg.GetFileId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}
	if req.Msg.GetExpiresInSeconds() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("expires_in_seconds must be positive"))
	}
	linkToken, permission, expiresAt, err := s.svc.CreatePublicLink(ctx, req.Msg.GetFileId(), userID, strings.ToLower(req.Msg.GetPermission()), req.Msg.GetExpiresInSeconds())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.CreatePublicLinkResponse{LinkToken: linkToken, Permission: permission, ExpiresAt: expiresAt.Format(time.RFC3339)}), nil
}

func (s *SharingServer) RevokePublicLink(ctx context.Context, req *connect.Request[pb.RevokePublicLinkRequest]) (*connect.Response[pb.RevokePublicLinkResponse], error) {
	_, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}
	if req.Msg.GetLinkToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("link_token is required"))
	}
	if err := s.svc.RevokePublicLink(ctx, req.Msg.GetLinkToken()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.RevokePublicLinkResponse{Success: true}), nil
}

