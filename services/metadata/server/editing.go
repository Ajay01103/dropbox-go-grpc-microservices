package server

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	"github.com/Ajay01103/go-dropbox/metadata/internal/service"
)

// GetEditSession is the editor's entry point: authorize, decide whether the
// type is editable, and hand back a version-pinned ticket plus the parameters
// a save must use.
//
// A refusal is NOT an RPC error: can_edit=false with a reason is the answer for
// a 200 MB PDF or a .doc, and the SPA renders it. Only authentication, a
// missing file, or a storage failure surface as errors.
func (s *MetadataServer) GetEditSession(ctx context.Context, req *pb.GetEditSessionRequest) (*pb.GetEditSessionResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	session, err := s.svc.GetEditSession(ctx, userID, req.GetFileId())
	if err != nil {
		s.logger.Warn("GetEditSession failed",
			zap.String("fileID", req.GetFileId()),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, mapError(err)
	}

	return &pb.GetEditSessionResponse{
		CanEdit:           session.CanEdit,
		UnavailableReason: session.UnavailableReason,
		Version:           session.Version,
		ContentHash:       session.ContentHash,
		TicketUrl:         session.TicketURL,
		ExpiresInSeconds:  session.ExpiresInSeconds,
		BlockSize:         session.BlockSize,
		MaxEditBytes:      session.MaxEditBytes,
		SniffedType:       session.SniffedType,
		Filename:          session.Filename,
		SizeBytes:         session.SizeBytes,
		ContentType:       session.ContentType,
		BlockHashes:       session.BlockHashes,
	}, nil
}

// PrepareCommit is the advisory pre-flight for a save: which of the caller's
// blocks the file already holds, and whether the base version is still head.
// The commit itself is the authority; this only saves upload bytes.
func (s *MetadataServer) PrepareCommit(ctx context.Context, req *pb.PrepareCommitRequest) (*pb.PrepareCommitResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	result, err := s.svc.PrepareCommit(ctx, userID, req.GetFileId(), req.GetBaseVersion(), req.GetBlockHashes())
	if err != nil {
		return nil, mapError(err)
	}
	return &pb.PrepareCommitResponse{
		AlreadyReferencedHashes: result.AlreadyReferenced,
		HeadVersion:             result.HeadVersion,
		BaseIsHead:              result.BaseIsHead,
	}, nil
}

// CommitVersion records edited bytes as the next immutable version.
//
// Called by the upload service, which owns block writes and ref-counting: by
// the time this runs, every block of the new version is in storage and has
// been hash-verified there. The caller's access token is forwarded, so the
// ownership check below is the same one every other file read goes through.
func (s *MetadataServer) CommitVersion(ctx context.Context, req *pb.CommitVersionRequest) (*pb.CommitVersionResponse, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, newConnectError(connect.CodeUnauthenticated, err)
	}
	if req.GetFileId() == "" {
		return nil, newConnectError(connect.CodeInvalidArgument, errors.New("file_id is required"))
	}

	result, err := s.svc.CommitVersion(ctx, service.CommitVersionRequest{
		FileID:      req.GetFileId(),
		OwnerID:     userID,
		BaseVersion: req.GetBaseVersion(),
		CommitID:    req.GetCommitId(),
		SizeBytes:   req.GetSizeBytes(),
		BlockSize:   req.GetBlockSize(),
		BlockHashes: req.GetBlockHashes(),
		BlockSizes:  req.GetBlockSizes(),
		Editor:      req.GetEditor(),
		ContentType: req.GetContentType(),
		Filename:    req.GetFilename(),
	})
	if err != nil {
		s.logger.Warn("CommitVersion failed",
			zap.String("fileID", req.GetFileId()),
			zap.String("userID", userID),
			zap.Int32("baseVersion", req.GetBaseVersion()),
			zap.Error(err))
		return nil, commitError(err)
	}

	return &pb.CommitVersionResponse{
		Committed:       result.Committed,
		Duplicate:       result.Duplicate,
		Conflict:        result.Conflict,
		Version:         result.Version,
		ContentHash:     result.ContentHash,
		HeadVersion:     result.HeadVersion,
		HeadContentHash: result.HeadContentHash,
	}, nil
}

// commitError maps a commit failure onto a status code the client can act on.
// The two edit sentinels separate "your bytes are wrong" (fix the request)
// from "your bytes are not readable yet" (retry); everything else is a
// genuine server-side failure and must not masquerade as the client's fault.
func commitError(err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidEdit):
		return newConnectError(connect.CodeInvalidArgument, err)
	case errors.Is(err, service.ErrEditBlockUnavailable):
		return newConnectError(connect.CodeFailedPrecondition, err)
	case strings.Contains(err.Error(), "not found"):
		return newConnectError(connect.CodeNotFound, err)
	default:
		return newConnectError(connect.CodeInternal, err)
	}
}
