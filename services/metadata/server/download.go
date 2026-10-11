package server

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/gen/pb"
)

// GetDownloadTicket mints the single-file short-lived ticket the plain-HTTP
// content server verifies statelessly. Ownership is checked HERE — the only
// place — so the byte path stays a cheap signature check.
func (s *MetadataServer) GetDownloadTicket(ctx context.Context, req *pb.GetDownloadTicketRequest) (*pb.GetDownloadTicketResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}
	disposition := req.GetDisposition()
	if disposition != "inline" && disposition != "attachment" {
		disposition = "attachment"
	}

	result, err := s.svc.GetDownloadTicket(ctx, userID, req.GetFileId(), disposition)
	if err != nil {
		s.logger.Warn("GetDownloadTicket failed",
			zap.String("fileID", req.GetFileId()),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, mapError(err)
	}

	return &pb.GetDownloadTicketResponse{
		Ticket:           result.Ticket,
		Url:              result.URL,
		ExpiresInSeconds: result.ExpiresInSeconds,
		Filename:         result.Filename,
		ContentType:      result.ContentType,
		SizeBytes:        result.SizeBytes,
	}, nil
}

// GetFileManifest returns the dense block manifest. Same ownership gate as
// GetFile; consumers are the content server's reconstruction path and tests.
func (s *MetadataServer) GetFileManifest(ctx context.Context, req *pb.GetFileManifestRequest) (*pb.FileManifest, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	manifest, err := s.svc.GetFileManifest(ctx, userID, req.GetFileId())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, newConnectError(connect.CodeNotFound, err)
		}
		s.logger.Warn("GetFileManifest failed",
			zap.String("fileID", req.GetFileId()),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, newConnectError(connect.CodeInternal, err)
	}

	blocks := make([]*pb.BlockRef, len(manifest.Blocks))
	for i, block := range manifest.Blocks {
		blocks[i] = &pb.BlockRef{Hash: block.Hash, SizeBytes: block.SizeBytes}
	}
	return &pb.FileManifest{
		FileId:      manifest.FileID,
		TotalSize:   manifest.TotalSize,
		ContentType: manifest.ContentType,
		ContentHash: manifest.ContentHash,
		Blocks:      blocks,
	}, nil
}
