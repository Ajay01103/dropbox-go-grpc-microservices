package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-notion/metadata/gen/pb"
	"github.com/Ajay01103/go-notion/metadata/gen/pb/pbconnect"
	"github.com/Ajay01103/go-notion/metadata/internal/service"
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

// CreateFile creates a new file metadata record
func (s *MetadataServer) CreateFile(ctx context.Context, req *connect.Request[pb.CreateFileRequest]) (*connect.Response[pb.CreateFileResponse], error) {
	// Extract user ID from context
	userID, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}

	if req.Msg.GetFilename() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("filename is required"))
	}

	result, err := s.svc.CreateFile(ctx, service.CreateFileRequest{
		FolderID:    req.Msg.GetFolderId(),
		Filename:    req.Msg.GetFilename(),
		SizeBytes:   req.Msg.GetSizeBytes(),
		ContentType: req.Msg.GetContentType(),
		ContentHash: req.Msg.GetContentHash(),
		OwnerID:     userID,
		StorageKey:  req.Msg.GetStorageKey(),
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
func (s *MetadataServer) GetFile(ctx context.Context, req *connect.Request[pb.GetFileRequest]) (*connect.Response[pb.File], error) {
	// Extract user ID from context
	userID, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}

	fileID := req.Msg.FileId
	if fileID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	// For now, use user_id as folder_id (root folder)
	// In production, you'd look up the actual folder
	result, err := s.svc.GetFile(ctx, fileID, userID)
	if err != nil {
		s.logger.Warn("GetFile failed",
			zap.String("fileID", fileID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	return connect.NewResponse(&pb.File{
		FileId:         result.FileID,
		FolderId:       result.FolderID,
		Filename:       result.Filename,
		SizeBytes:      result.SizeBytes,
		ContentType:    result.ContentType,
		ContentHash:    result.ContentHash,
		Version:        result.Version,
		CreatedAt:      result.CreatedAt,
		OwnerId:        result.OwnerID,
		ParentFolderId: result.FolderID,
	}), nil
}

// ListFolder returns all files in a folder
func (s *MetadataServer) ListFolder(ctx context.Context, req *connect.Request[pb.ListFolderRequest]) (*connect.Response[pb.ListFolderResponse], error) {
	// Extract user ID from context
	userID, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
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

	result, err := s.svc.ListFolder(ctx, folderID, pageSize)
	if err != nil {
		s.logger.Error("ListFolder failed",
			zap.String("folderID", folderID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	files := make([]*pb.File, len(result.Files))
	for i, f := range result.Files {
		files[i] = &pb.File{
			FileId:         f.FileID,
			FolderId:       folderID,
			Filename:       f.Filename,
			SizeBytes:      f.SizeBytes,
			ContentType:    f.ContentType,
			CreatedAt:      f.CreatedAt,
			OwnerId:        userID,
			ParentFolderId: folderID,
		}
	}

	return connect.NewResponse(&pb.ListFolderResponse{
		Files: files,
	}), nil
}

// DeleteFile removes a file record
func (s *MetadataServer) DeleteFile(ctx context.Context, req *connect.Request[pb.DeleteFileRequest]) (*connect.Response[pb.DeleteFileResponse], error) {
	// Extract user ID from context
	userID, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}

	fileID := req.Msg.FileId
	if fileID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	// Use user_id as folder_id
	err := s.svc.DeleteFile(ctx, fileID, userID)
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
