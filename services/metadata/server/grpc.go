package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	"github.com/Ajay01103/go-dropbox/metadata/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
)

// MetadataServer implements the pbconnect.MetadataServiceHandler interface
type MetadataServer struct {
	pbconnect.UnimplementedMetadataServiceHandler
	svc    *service.MetadataService
	logger *zap.Logger
}

// New creates a new Connect MetadataServer instance
func New(svc *service.MetadataService, logger *zap.Logger) *MetadataServer {
	return &MetadataServer{svc: svc, logger: logger}
}

func authenticatedUserID(ctx context.Context) (string, error) {
	userID, err := interceptor.UserIDFromContext(ctx)
	if err != nil {
		return "", err
	}
	return userID.String(), nil
}

// CreateFile creates a new file metadata record
func (s *MetadataServer) CreateFile(ctx context.Context, req *connect.Request[pb.CreateFileRequest]) (*connect.Response[pb.CreateFileResponse], error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	if req.Msg.GetFilename() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("filename is required"))
	}

	result, err := s.svc.CreateFile(ctx, service.CreateFileRequest{
		FolderID:      req.Msg.GetFolderId(),
		Filename:      req.Msg.GetFilename(),
		SizeBytes:     req.Msg.GetSizeBytes(),
		ContentType:   req.Msg.GetContentType(),
		ContentHash:   req.Msg.GetContentHash(),
		OwnerID:       userID,
		BlockHashList: req.Msg.GetBlockHashList(),
	})
	if err != nil {
		s.logger.Error("CreateFile failed",
			zap.String("userID", userID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.CreateFileResponse{
		FileId:      result.FileID,
		FolderId:    result.FolderID,
		Filename:    result.Filename,
		SizeBytes:   result.SizeBytes,
		ContentHash: result.ContentHash,
		CreatedAt:   result.CreatedAt,
	}), nil
}

// GetFile retrieves file metadata
func (s *MetadataServer) SetThumbnail(ctx context.Context, req *connect.Request[pb.SetThumbnailRequest]) (*connect.Response[pb.SetThumbnailResponse], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	if req.Msg.GetFileId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	if err := s.svc.SetThumbnail(ctx, req.Msg.GetFileId(), userID, req.Msg.GetThumbnailKey(), req.Msg.GetThumbnailStatus()); err != nil {
		s.logger.Error("SetThumbnail failed",
			zap.String("fileID", req.Msg.GetFileId()),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.SetThumbnailResponse{Success: true}), nil
}

func (s *MetadataServer) GetThumbnailURL(ctx context.Context, req *connect.Request[pb.GetThumbnailURLRequest]) (*connect.Response[pb.GetThumbnailURLResponse], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if req.Msg.GetFileId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}
	url, status, err := s.svc.GetThumbnailURL(ctx, userID, req.Msg.GetFileId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&pb.GetThumbnailURLResponse{Url: url, ThumbnailStatus: status}), nil
}

func (s *MetadataServer) GetThumbnailStatus(ctx context.Context, req *connect.Request[pb.GetThumbnailStatusRequest]) (*connect.Response[pb.GetThumbnailStatusResponse], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	fileID := req.Msg.GetFileId()
	if fileID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	thumbnailKey, thumbnailStatus, err := s.svc.GetThumbnailStatus(ctx, fileID, userID)
	if err != nil {
		s.logger.Warn("GetThumbnailStatus failed",
			zap.String("fileID", fileID),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	return connect.NewResponse(&pb.GetThumbnailStatusResponse{
		ThumbnailKey:    thumbnailKey,
		ThumbnailStatus: thumbnailStatus,
	}), nil
}

func (s *MetadataServer) GetFile(ctx context.Context, req *connect.Request[pb.GetFileRequest]) (*connect.Response[pb.File], error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	fileID := req.Msg.FileId
	if fileID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	result, err := s.svc.GetFileOwned(ctx, userID, fileID)
	if err != nil {
		s.logger.Warn("GetFile failed",
			zap.String("fileID", fileID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	return connect.NewResponse(fileMessage(result)), nil
}

// ListFolder returns all files in a folder
func (s *MetadataServer) ListFolder(ctx context.Context, req *connect.Request[pb.ListFolderRequest]) (*connect.Response[pb.ListFolderResponse], error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	folderID := req.Msg.GetFolderId()
	if folderID == "" {
		// If no folder_id provided, use user's root folder
		var err error
		folderID, err = s.svc.GetOrCreateRootFolder(ctx, userID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	pageSize := int(req.Msg.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}

	files, nextPageToken, err := s.svc.ListFilesOwned(ctx, userID, folderID, pageSize, req.Msg.GetPageToken(), false)
	if err != nil {
		s.logger.Error("ListFolder failed",
			zap.String("folderID", folderID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	publicFiles := make([]*pb.File, len(files))
	for i, file := range files {
		publicFiles[i] = fileMessage(file)
	}

	return connect.NewResponse(&pb.ListFolderResponse{
		Files:         publicFiles,
		NextPageToken: nextPageToken,
	}), nil
}

// DeleteFile removes a file record
func (s *MetadataServer) DeleteFile(ctx context.Context, req *connect.Request[pb.DeleteFileRequest]) (*connect.Response[pb.DeleteFileResponse], error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	fileID := req.Msg.FileId
	if fileID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	// Use user_id as folder_id
	err = s.svc.DeleteFile(ctx, fileID, userID)
	if err != nil {
		s.logger.Error("DeleteFile failed",
			zap.String("fileID", fileID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.DeleteFileResponse{
		Success: true,
	}), nil
}
