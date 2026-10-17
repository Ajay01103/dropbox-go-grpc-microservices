package server

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"github.com/Ajay01103/go-dropbox/auth/gen/pb"
	"github.com/Ajay01103/go-dropbox/auth/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/auth/internal/service"
)

// AuthServer implements the pbconnect.AuthServiceHandler interface
type AuthServer struct {
	pbconnect.UnimplementedAuthServiceHandler
	svc *service.AuthService
}

// New creates a new Connect AuthServer instance.
func New(svc *service.AuthService) *AuthServer {
	return &AuthServer{svc: svc}
}

// Register creates a new user and returns tokens.
func (s *AuthServer) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.AuthResponse, error) {
	if req.GetEmail() == "" || req.GetName() == "" || req.GetPassword() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("missing required fields"))
	}

	res, err := s.svc.Register(ctx, req.GetEmail(), req.GetName(), req.GetPassword())
	if err != nil {
		switch err {
		case service.ErrEmailAlreadyExists:
			return nil, newConnectError(connect.CodeAlreadyExists, err)
		default:
			return nil, newConnectError(connect.CodeInternal, err)
		}
	}

	return &pb.AuthResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		UserId:       res.User.ID,
		Name:         res.User.Name,
		Email:        res.User.Email,
	}, nil
}

// Login authenticates a user and returns tokens.
func (s *AuthServer) Login(ctx context.Context, req *pb.LoginRequest) (*pb.AuthResponse, error) {
	if req.GetEmail() == "" || req.GetPassword() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("missing required fields"))
	}

	res, err := s.svc.Login(ctx, req.GetEmail(), req.GetPassword())
	if err != nil {
		if err == service.ErrInvalidCredentials {
			return nil, newConnectError(connect.CodeUnauthenticated, err)
		}
		return nil, newConnectError(connect.CodeInternal, err)
	}

	return &pb.AuthResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		UserId:       res.User.ID,
		Name:         res.User.Name,
		Email:        res.User.Email,
	}, nil
}

// RefreshToken rotates the token pair using a valid refresh token.
func (s *AuthServer) RefreshToken(ctx context.Context, req *pb.RefreshTokenRequest) (*pb.AuthResponse, error) {
	if req.GetRefreshToken() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("missing refresh token"))
	}

	res, err := s.svc.RefreshToken(ctx, req.GetRefreshToken())
	if err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken, service.ErrTokenRevoked, service.ErrTokenReuseDetected, service.ErrRefreshFamilyMissing:
			return nil, newConnectError(connect.CodeUnauthenticated, err)
		case service.ErrUserNotFound:
			return nil, newConnectError(connect.CodeNotFound, err)
		default:
			return nil, newConnectError(connect.CodeInternal, err)
		}
	}

	return &pb.AuthResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
	}, nil
}

// Logout revokes the session the presented refresh token belongs to: the
// session row is deleted, so both the refresh token and the access token minted
// alongside it stop validating.
func (s *AuthServer) Logout(ctx context.Context, req *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	if req.GetRefreshToken() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("missing refresh token"))
	}

	if err := s.svc.Logout(ctx, req.GetRefreshToken()); err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken:
			return nil, newConnectError(connect.CodeUnauthenticated, err)
		default:
			return nil, newConnectError(connect.CodeInternal, err)
		}
	}

	return &pb.LogoutResponse{
		Message: "successfully logged out",
	}, nil
}

// LogoutAllDevices invalidates all refresh token families for a user.
func (s *AuthServer) LogoutAllDevices(ctx context.Context, req *pb.LogoutAllDevicesRequest) (*pb.LogoutAllDevicesResponse, error) {
	if req.GetUserId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("missing user id"))
	}

	if err := s.svc.LogoutAllDevices(ctx, req.GetUserId()); err != nil {
		if err == service.ErrInvalidToken {
			return nil, newConnectError(connect.CodeInvalidArgument, err)
		}
		return nil, newConnectError(connect.CodeInternal, err)
	}

	return &pb.LogoutAllDevicesResponse{Message: "successfully logged out from all devices"}, nil
}

// ValidateToken checks if an access token is valid and returns user info.
func (s *AuthServer) ValidateToken(ctx context.Context, req *pb.ValidateTokenRequest) (*pb.ValidateTokenResponse, error) {
	if req.GetAccessToken() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("missing access token"))
	}

	res, err := s.svc.ValidateToken(ctx, req.GetAccessToken())
	if err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken, service.ErrTokenRevoked, service.ErrSessionNotFound:
			return nil, newConnectError(connect.CodeUnauthenticated, err)
		default:
			return nil, newConnectError(connect.CodeInternal, err)
		}
	}

	return &pb.ValidateTokenResponse{
		Valid:  true,
		UserId: res.UserID.String(),
		Email:  res.Email,
		Name:   res.Name,
	}, nil
}

// GetCurrentUser extracts bearer token and returns current user info.
func (s *AuthServer) GetCurrentUser(ctx context.Context, req *pb.GetCurrentUserRequest) (*pb.GetCurrentUserResponse, error) {
	// 1. Extract token from Connect headers
	var authHeader string
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if authHeader == "" {
		return nil, newConnectError(connect.CodeUnauthenticated, errors.New("authorization token is not provided"))
	}

	if len(authHeader) < 7 || authHeader[:7] != "Bearer " {
		return nil, newConnectError(connect.CodeUnauthenticated, errors.New("invalid authorization token format"))
	}
	tokenStr := authHeader[7:]

	// 2. Validate token
	res, err := s.svc.ValidateToken(ctx, tokenStr)
	if err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken, service.ErrTokenRevoked, service.ErrSessionNotFound:
			return nil, newConnectError(connect.CodeUnauthenticated, err)
		default:
			return nil, newConnectError(connect.CodeInternal, err)
		}
	}

	// 3. Fetch current user profile through the cache-backed service path
	user, err := s.svc.GetCurrentUserProfile(ctx, res.UserID)
	if err != nil {
		return nil, newConnectError(connect.CodeInternal, err)
	}
	if user == nil {
		return nil, newConnectError(connect.CodeNotFound, errors.New("user not found"))
	}

	// 4. Return user info
	return &pb.GetCurrentUserResponse{
		UserId: user.UserID,
		Email:  user.Email,
		Name:   user.Name,
	}, nil
}
