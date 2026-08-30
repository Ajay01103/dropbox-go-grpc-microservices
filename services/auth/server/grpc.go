package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/Ajay01103/go-notion/auth/gen/pb"
	"github.com/Ajay01103/go-notion/auth/gen/pb/pbconnect"
	"github.com/Ajay01103/go-notion/auth/internal/service"
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
func (s *AuthServer) Register(ctx context.Context, req *connect.Request[pb.RegisterRequest]) (*connect.Response[pb.AuthResponse], error) {
	if req.Msg.GetEmail() == "" || req.Msg.GetName() == "" || req.Msg.GetPassword() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing required fields"))
	}

	res, err := s.svc.Register(ctx, req.Msg.GetEmail(), req.Msg.GetName(), req.Msg.GetPassword())
	if err != nil {
		switch err {
		case service.ErrEmailAlreadyExists:
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return connect.NewResponse(&pb.AuthResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		UserId:       res.User.ID,
		Name:         res.User.Name,
		Email:        res.User.Email,
	}), nil
}

// Login authenticates a user and returns tokens.
func (s *AuthServer) Login(ctx context.Context, req *connect.Request[pb.LoginRequest]) (*connect.Response[pb.AuthResponse], error) {
	if req.Msg.GetEmail() == "" || req.Msg.GetPassword() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing required fields"))
	}

	res, err := s.svc.Login(ctx, req.Msg.GetEmail(), req.Msg.GetPassword())
	if err != nil {
		if err == service.ErrInvalidCredentials {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.AuthResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		UserId:       res.User.ID,
		Name:         res.User.Name,
		Email:        res.User.Email,
	}), nil
}

// RefreshToken rotates the token pair using a valid refresh token.
func (s *AuthServer) RefreshToken(ctx context.Context, req *connect.Request[pb.RefreshTokenRequest]) (*connect.Response[pb.AuthResponse], error) {
	if req.Msg.GetRefreshToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing refresh token"))
	}

	res, err := s.svc.RefreshToken(ctx, req.Msg.GetRefreshToken())
	if err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken, service.ErrTokenRevoked, service.ErrTokenReuseDetected, service.ErrRefreshFamilyMissing:
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		case service.ErrUserNotFound:
			return nil, connect.NewError(connect.CodeNotFound, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return connect.NewResponse(&pb.AuthResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
	}), nil
}

// Logout invalidates a refresh token in Redis.
func (s *AuthServer) Logout(ctx context.Context, req *connect.Request[pb.LogoutRequest]) (*connect.Response[pb.LogoutResponse], error) {
	if req.Msg.GetRefreshToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing refresh token"))
	}

	if err := s.svc.Logout(ctx, req.Msg.GetRefreshToken()); err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken:
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return connect.NewResponse(&pb.LogoutResponse{
		Message: "successfully logged out",
	}), nil
}

// LogoutAllDevices invalidates all refresh token families for a user.
func (s *AuthServer) LogoutAllDevices(ctx context.Context, req *connect.Request[pb.LogoutAllDevicesRequest]) (*connect.Response[pb.LogoutAllDevicesResponse], error) {
	if req.Msg.GetUserId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing user id"))
	}

	if err := s.svc.LogoutAllDevices(ctx, req.Msg.GetUserId()); err != nil {
		if err == service.ErrInvalidToken {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.LogoutAllDevicesResponse{Message: "successfully logged out from all devices"}), nil
}

// ValidateToken checks if an access token is valid and returns user info.
func (s *AuthServer) ValidateToken(ctx context.Context, req *connect.Request[pb.ValidateTokenRequest]) (*connect.Response[pb.ValidateTokenResponse], error) {
	if req.Msg.GetAccessToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing access token"))
	}

	res, err := s.svc.ValidateToken(ctx, req.Msg.GetAccessToken())
	if err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken, service.ErrTokenRevoked, service.ErrSessionNotFound:
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return connect.NewResponse(&pb.ValidateTokenResponse{
		Valid:  true,
		UserId: res.UserID.String(),
		Email:  res.Email,
		Name:   res.Name,
	}), nil
}

// GetCurrentUser extracts bearer token and returns current user info.
func (s *AuthServer) GetCurrentUser(ctx context.Context, req *connect.Request[pb.GetCurrentUserRequest]) (*connect.Response[pb.GetCurrentUserResponse], error) {
	// 1. Extract token from Connect headers
	authHeader := req.Header().Get("Authorization")
	if authHeader == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authorization token is not provided"))
	}

	if len(authHeader) < 7 || authHeader[:7] != "Bearer " {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid authorization token format"))
	}
	tokenStr := authHeader[7:]

	// 2. Validate token
	res, err := s.svc.ValidateToken(ctx, tokenStr)
	if err != nil {
		switch err {
		case service.ErrTokenExpired, service.ErrInvalidToken, service.ErrTokenRevoked, service.ErrSessionNotFound:
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	// 3. Fetch current user profile through the cache-backed service path
	user, err := s.svc.GetCurrentUserProfile(ctx, res.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if user == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	}

	// 4. Return user info
	return connect.NewResponse(&pb.GetCurrentUserResponse{
		UserId: user.UserID,
		Email:  user.Email,
		Name:   user.Name,
	}), nil
}
