package server

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/upload/gen/pb"
	"github.com/Ajay01103/go-dropbox/upload/internal/service"
)

// WriteEditBlock stores one block of a pending save.
//
// Unary, one block per call: a save then sends its missing blocks from several
// workers at once and stays inside the per-message size cap that bootstraps
// the whole upload service (see mount.go).
func (s *UploadServer) WriteEditBlock(ctx context.Context, req *pb.WriteEditBlockRequest) (*pb.WriteEditBlockResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" || req.GetSha256() == "" || len(req.GetData()) == 0 {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id, sha256 and data are required"))
	}

	result, err := s.svc.WriteEditBlock(ctx, userID, req.GetFileId(), req.GetIndex(), req.GetSha256(), req.GetData())
	if err != nil {
		s.logger.Warn("WriteEditBlock failed",
			zap.String("fileID", req.GetFileId()),
			zap.String("userID", userID),
			zap.Int32("index", req.GetIndex()),
			zap.Error(err))
		return nil, newConnectError(connect.CodeInvalidArgument, err)
	}

	return &pb.WriteEditBlockResponse{
		Written:   result.Written,
		Reused:    result.Reused,
		SizeBytes: result.SizeBytes,
	}, nil
}

// CommitEdit commits edited bytes as the next version of a file. The metadata
// service arbitrates the version number; this service adds the block
// references and publishes the versioned event once that arbitration wins.
func (s *UploadServer) CommitEdit(ctx context.Context, req *pb.CommitEditRequest) (*pb.CommitEditResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" || len(req.GetBlockHashes()) == 0 {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id and block_hashes are required"))
	}

	result, err := s.svc.CommitEdit(ctx, userID, service.CommitEditRequest{
		FileID:      req.GetFileId(),
		BaseVersion: req.GetBaseVersion(),
		CommitID:    req.GetCommitId(),
		SizeBytes:   req.GetSizeBytes(),
		BlockSize:   req.GetBlockSize(),
		BlockHashes: req.GetBlockHashes(),
		BlockSizes:  req.GetBlockSizes(),
		Editor:      req.GetEditor(),
		Filename:    req.GetFilename(),
		ContentType: req.GetContentType(),
	})
	if err != nil {
		code := connect.CodeInternal
		switch {
		case strings.Contains(err.Error(), "required"),
			strings.Contains(err.Error(), "invalid"),
			strings.Contains(err.Error(), "does not cover"),
			strings.Contains(err.Error(), "adds up to"):
			code = connect.CodeInvalidArgument
		case strings.Contains(err.Error(), "not configured"):
			code = connect.CodeUnavailable
		}
		s.logger.Warn("CommitEdit failed",
			zap.String("fileID", req.GetFileId()),
			zap.String("userID", userID),
			zap.Int32("baseVersion", req.GetBaseVersion()),
			zap.Error(err))
		return nil, newConnectError(code, err)
	}

	return &pb.CommitEditResponse{
		Committed:       result.Committed,
		Conflict:        result.Conflict,
		Version:         result.Version,
		ContentHash:     result.ContentHash,
		HeadVersion:     result.HeadVersion,
		HeadContentHash: result.HeadContentHash,
		BlocksReused:    result.BlocksReused,
	}, nil
}
