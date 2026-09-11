package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
)

func mapError(err error) error {
	if err == nil {
		return nil
	}
	code := connect.CodeInternal
	switch {
	case errors.Is(err, service.ErrInvalidName):
		code = connect.CodeInvalidArgument
	case errors.Is(err, service.ErrFolderConflict):
		code = connect.CodeAlreadyExists
	case errors.Is(err, service.ErrFolderCycle):
		code = connect.CodeFailedPrecondition
	case strings.Contains(err.Error(), "not found"):
		code = connect.CodeNotFound
	case strings.Contains(err.Error(), "deleted"):
		code = connect.CodeFailedPrecondition
	case strings.Contains(err.Error(), "owner"):
		code = connect.CodePermissionDenied
	}
	return connect.NewError(code, err)
}

func purgeJobMessage(job repository.PurgeJob) *pb.PurgeJob {
	result := &pb.PurgeJob{
		JobId: job.JobID, FileId: job.FileID, FileVersion: job.FileVersion,
		State: job.State, HasReconciliationErrors: job.HasReconciliationErrors,
		Attempts: int32(job.Attempts), LastError: job.LastError,
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if !job.CompletedAt.IsZero() {
		result.CompletedAt = job.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func fileMessage(file repository.File) *pb.File {
	result := &pb.File{FileId: file.FileID, FolderId: file.FolderID, Filename: file.Filename, SizeBytes: file.SizeBytes, ContentType: file.ContentType, ContentHash: file.ContentHash, Version: file.Version, CreatedAt: file.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), OwnerId: file.OwnerID, ThumbnailKey: file.ThumbnailKey, ThumbnailStatus: file.ThumbnailStatus, IsDeleted: file.IsDeleted, Current: file.Current}
	if !file.DeletedAt.IsZero() {
		result.DeletedAt = file.DeletedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return result
}

func folderMessage(folder repository.Folder) *pb.Folder {
	result := &pb.Folder{FolderId: folder.FolderID, OwnerId: folder.OwnerID, ParentId: folder.ParentID, Name: folder.FolderName, Path: folder.PathCache, CreatedAt: folder.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), UpdatedAt: folder.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), IsDeleted: folder.IsDeleted}
	if !folder.DeletedAt.IsZero() {
		result.DeletedAt = folder.DeletedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return result
}

func (s *MetadataServer) ListFiles(ctx context.Context, req *connect.Request[pb.ListFilesRequest]) (*connect.Response[pb.ListFilesResponse], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if req.Msg.GetFolderId() == "" {
		root, rootErr := s.svc.EnsureRootFolder(ctx, userID)
		if rootErr != nil {
			return nil, mapError(rootErr)
		}
		req.Msg.FolderId = root.FolderID
	}
	if err := servicePageSize(req.Msg.GetPageSize()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	files, next, err := s.svc.ListFilesOwned(ctx, userID, req.Msg.GetFolderId(), int(req.Msg.GetPageSize()), req.Msg.GetPageToken(), false)
	if err != nil {
		return nil, mapError(err)
	}
	response := &pb.ListFilesResponse{NextPageToken: next, Files: make([]*pb.File, 0, len(files))}
	for _, file := range files {
		response.Files = append(response.Files, fileMessage(file))
	}
	return connect.NewResponse(response), nil
}

func (s *MetadataServer) RenameFile(ctx context.Context, req *connect.Request[pb.RenameFileRequest]) (*connect.Response[pb.File], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	file, err := s.svc.RenameFileOwned(ctx, userID, req.Msg.GetFileId(), req.Msg.GetNewName())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(fileMessage(file)), nil
}

func (s *MetadataServer) MoveFile(ctx context.Context, req *connect.Request[pb.MoveFileRequest]) (*connect.Response[pb.File], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	file, err := s.svc.MoveFileOwned(ctx, userID, req.Msg.GetFileId(), req.Msg.GetNewFolderId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(fileMessage(file)), nil
}

func (s *MetadataServer) RestoreFile(ctx context.Context, req *connect.Request[pb.RestoreFileRequest]) (*connect.Response[pb.File], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	file, err := s.svc.RestoreFileOwned(ctx, userID, req.Msg.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(fileMessage(file)), nil
}

func (s *MetadataServer) PermanentlyDeleteFile(ctx context.Context, req *connect.Request[pb.PermanentlyDeleteFileRequest]) (*connect.Response[pb.PurgeJob], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if req.Msg.GetFileId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}
	job, err := s.svc.RequestPermanentDelete(ctx, userID, req.Msg.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(purgeJobMessage(job)), nil
}

func (s *MetadataServer) GetPurgeJobStatus(ctx context.Context, req *connect.Request[pb.GetPurgeJobStatusRequest]) (*connect.Response[pb.PurgeJob], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if req.Msg.GetJobId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job_id is required"))
	}
	job, err := s.svc.GetPurgeJobStatus(ctx, userID, req.Msg.GetJobId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(purgeJobMessage(job)), nil
}

func (s *MetadataServer) ListTrash(context.Context, *connect.Request[pb.ListTrashRequest]) (*connect.Response[pb.ListTrashResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("trash listing is not enabled in this phase"))
}

func servicePageSize(size int32) error {
	if size < 0 || size > 1000 {
		return errors.New("page_size must be between 0 and 1000")
	}
	return nil
}

func (s *MetadataServer) CreateFolder(ctx context.Context, req *connect.Request[pb.CreateFolderRequest]) (*connect.Response[pb.Folder], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	folder, err := s.svc.CreateChildFolder(ctx, userID, req.Msg.GetParentId(), req.Msg.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(folderMessage(folder)), nil
}

func (s *MetadataServer) GetFolder(ctx context.Context, req *connect.Request[pb.GetFolderRequest]) (*connect.Response[pb.Folder], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	folder, err := s.svc.GetFolderOwned(ctx, userID, req.Msg.GetFolderId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(folderMessage(folder)), nil
}

func (s *MetadataServer) ListFolderContents(ctx context.Context, req *connect.Request[pb.ListFolderContentsRequest]) (*connect.Response[pb.ListFolderContentsResponse], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	folderID := req.Msg.GetFolderId()
	if folderID == "" {
		root, rootErr := s.svc.EnsureRootFolder(ctx, userID)
		if rootErr != nil {
			return nil, mapError(rootErr)
		}
		folderID = root.FolderID
	}
	if err := servicePageSize(req.Msg.GetPageSize()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	folders, files, next, err := s.svc.ListFolderContentsOwned(ctx, userID, folderID, int(req.Msg.GetPageSize()), req.Msg.GetPageToken())
	if err != nil {
		return nil, mapError(err)
	}
	response := &pb.ListFolderContentsResponse{NextPageToken: next, Folders: make([]*pb.Folder, 0, len(folders)), Files: make([]*pb.File, 0, len(files))}
	for _, folder := range folders {
		response.Folders = append(response.Folders, folderMessage(folder))
	}
	for _, file := range files {
		response.Files = append(response.Files, fileMessage(file))
	}
	return connect.NewResponse(response), nil
}

func (s *MetadataServer) RenameFolder(ctx context.Context, req *connect.Request[pb.RenameFolderRequest]) (*connect.Response[pb.Folder], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	folder, err := s.svc.RenameFolderOwned(ctx, userID, req.Msg.GetFolderId(), req.Msg.GetNewName())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(folderMessage(folder)), nil
}

func (s *MetadataServer) MoveFolder(ctx context.Context, req *connect.Request[pb.MoveFolderRequest]) (*connect.Response[pb.Folder], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	folder, err := s.svc.MoveFolderOwned(ctx, userID, req.Msg.GetFolderId(), req.Msg.GetNewParentId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(folderMessage(folder)), nil
}

func (s *MetadataServer) DeleteFolder(ctx context.Context, req *connect.Request[pb.DeleteFolderRequest]) (*connect.Response[pb.DeleteFolderResponse], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if _, err := s.svc.DeleteFolderOwned(ctx, userID, req.Msg.GetFolderId()); err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&pb.DeleteFolderResponse{Success: true}), nil
}

func (s *MetadataServer) RestoreFolder(ctx context.Context, req *connect.Request[pb.RestoreFolderRequest]) (*connect.Response[pb.Folder], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	folder, err := s.svc.RestoreFolderOwned(ctx, userID, req.Msg.GetFolderId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(folderMessage(folder)), nil
}

func (s *MetadataServer) GetBreadcrumbs(ctx context.Context, req *connect.Request[pb.GetBreadcrumbsRequest]) (*connect.Response[pb.GetBreadcrumbsResponse], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	folders, err := s.svc.Breadcrumbs(ctx, userID, req.Msg.GetFolderId())
	if err != nil {
		return nil, mapError(err)
	}
	response := &pb.GetBreadcrumbsResponse{Folders: make([]*pb.Folder, 0, len(folders))}
	for _, folder := range folders {
		response.Folders = append(response.Folders, folderMessage(folder))
	}
	return connect.NewResponse(response), nil
}
