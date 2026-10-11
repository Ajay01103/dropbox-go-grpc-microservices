package server

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	"github.com/Ajay01103/go-dropbox/metadata/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
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
func (s *MetadataServer) CreateFile(ctx context.Context, req *pb.CreateFileRequest) (*pb.CreateFileResponse, error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}

	if req.GetFilename() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("filename is required"))
	}

	// The dense manifest is written from block_refs (hash + exact size); when
	// the caller sends only the sparse hash list we still create the file and
	// the download path degrades to per-block size lookups.
	blockSizes := make([]int64, 0, len(req.GetBlockRefs()))
	for _, ref := range req.GetBlockRefs() {
		blockSizes = append(blockSizes, ref.GetSizeBytes())
	}

	result, err := s.svc.CreateFile(ctx, service.CreateFileRequest{
		FolderID:      req.GetFolderId(),
		Filename:      req.GetFilename(),
		SizeBytes:     req.GetSizeBytes(),
		ContentType:   req.GetContentType(),
		ContentHash:   req.GetContentHash(),
		OwnerID:       userID,
		BlockHashList: req.GetBlockHashList(),
		BlockSizes:    blockSizes,
		ContentFamily: eventsv2.ContentFamily(req.GetContentFamily()),
	})
	if err != nil {
		s.logger.Error("CreateFile failed",
			zap.String("userID", userID),
			zap.Error(err))
		return nil, newConnectError(connect.CodeInternal, err)
	}

	return &pb.CreateFileResponse{
		FileId:      result.FileID,
		FolderId:    result.FolderID,
		Filename:    result.Filename,
		SizeBytes:   result.SizeBytes,
		ContentHash: result.ContentHash,
		CreatedAt:   result.CreatedAt,
	}, nil
}

// GetFile retrieves file metadata
func (s *MetadataServer) SetThumbnail(ctx context.Context, req *pb.SetThumbnailRequest) (*pb.SetThumbnailResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}

	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	if err := s.svc.SetThumbnail(ctx, req.GetFileId(), userID, req.GetThumbnailKey(), req.GetThumbnailStatus()); err != nil {
		s.logger.Error("SetThumbnail failed",
			zap.String("fileID", req.GetFileId()),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, newConnectError(connect.CodeInternal, err)
	}

	return &pb.SetThumbnailResponse{Success: true}, nil
}

func (s *MetadataServer) GetThumbnailURL(ctx context.Context, req *pb.GetThumbnailURLRequest) (*pb.GetThumbnailURLResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}
	url, status, err := s.svc.GetThumbnailURL(ctx, userID, req.GetFileId())
	if err != nil {
		return nil, newConnectError(connect.CodeNotFound, err)
	}
	return &pb.GetThumbnailURLResponse{Url: url, ThumbnailStatus: status}, nil
}

func (s *MetadataServer) GetThumbnailStatus(ctx context.Context, req *pb.GetThumbnailStatusRequest) (*pb.GetThumbnailStatusResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}

	fileID := req.GetFileId()
	if fileID == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	thumbnailKey, thumbnailStatus, err := s.svc.GetThumbnailStatus(ctx, fileID, userID)
	if err != nil {
		s.logger.Warn("GetThumbnailStatus failed",
			zap.String("fileID", fileID),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, newConnectError(connect.CodeNotFound, err)
	}

	return &pb.GetThumbnailStatusResponse{
		ThumbnailKey:    thumbnailKey,
		ThumbnailStatus: thumbnailStatus,
	}, nil
}

func (s *MetadataServer) GetFile(ctx context.Context, req *pb.GetFileRequest) (*pb.File, error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}

	fileID := req.FileId
	if fileID == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	result, err := s.svc.GetFileOwned(ctx, userID, fileID)
	if err != nil {
		s.logger.Warn("GetFile failed",
			zap.String("fileID", fileID),
			zap.Error(err))
		return nil, newConnectError(connect.CodeNotFound, err)
	}

	return fileMessage(result), nil
}

// ListFolder returns all files in a folder
func (s *MetadataServer) ListFolder(ctx context.Context, req *pb.ListFolderRequest) (*pb.ListFolderResponse, error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}

	folderID := req.GetFolderId()
	if folderID == "" {
		// If no folder_id provided, use user's root folder
		var err error
		folderID, err = s.svc.GetOrCreateRootFolder(ctx, userID)
		if err != nil {
			return nil, newConnectError(connect.CodeInternal, err)
		}
	}

	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}

	files, nextPageToken, err := s.svc.ListFilesOwned(ctx, userID, folderID, pageSize, req.GetPageToken(), false)
	if err != nil {
		s.logger.Error("ListFolder failed",
			zap.String("folderID", folderID),
			zap.Error(err))
		return nil, newConnectError(connect.CodeInternal, err)
	}

	publicFiles := make([]*pb.File, len(files))
	for i, file := range files {
		publicFiles[i] = fileMessage(file)
	}

	return &pb.ListFolderResponse{
		Files:         publicFiles,
		NextPageToken: nextPageToken,
	}, nil
}

// DeleteFile removes a file record
func (s *MetadataServer) DeleteFile(ctx context.Context, req *pb.DeleteFileRequest) (*pb.DeleteFileResponse, error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}

	fileID := req.FileId
	if fileID == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	// Use user_id as folder_id
	err = s.svc.DeleteFile(ctx, fileID, userID)
	if err != nil {
		s.logger.Error("DeleteFile failed",
			zap.String("fileID", fileID),
			zap.Error(err))
		return nil, newConnectError(connect.CodeInternal, err)
	}

	return &pb.DeleteFileResponse{
		Success: true,
	}, nil
}
