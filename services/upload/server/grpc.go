package server

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	"github.com/Ajay01103/go-dropbox/upload/gen/pb"
	"github.com/Ajay01103/go-dropbox/upload/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/upload/internal/service"
)

// UploadServer implements the pbconnect.UploadServiceHandler interface
type UploadServer struct {
	pbconnect.UnimplementedUploadServiceHandler
	svc    *service.UploadService
	logger *zap.Logger
}

// New creates a new Connect UploadServer instance
func New(svc *service.UploadService, logger *zap.Logger) *UploadServer {
	return &UploadServer{svc: svc, logger: logger}
}

func authenticatedUserID(ctx context.Context) (string, error) {
	userID, err := interceptor.UserIDFromContext(ctx)
	if err != nil {
		return "", err
	}
	return userID.String(), nil
}

// InitUpload creates a new resumable upload session
func (s *UploadServer) InitUpload(ctx context.Context, req *connect.Request[pb.InitUploadRequest]) (*connect.Response[pb.InitUploadResponse], error) {
	// Extract user ID from context (set by auth interceptor)
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	// Validate request
	if req.Msg.GetFilename() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("filename is required"))
	}
	if req.Msg.GetTotalSizeBytes() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("total_size_bytes must be positive"))
	}
	s.logger.Info("upload initialization requested",
		zap.String("userID", userID),
		zap.String("filename", req.Msg.GetFilename()),
		zap.Int64("totalSize", req.Msg.GetTotalSizeBytes()),
		zap.String("contentType", req.Msg.GetContentType()))

	// Call service
	result, err := s.svc.InitUpload(
		ctx,
		userID,
		req.Msg.GetFilename(),
		req.Msg.GetContentType(),
		req.Msg.GetTotalSizeBytes(),
		req.Msg.GetSha256OfFullFile(),
	)
	if err != nil {
		s.logger.Error("InitUpload failed",
			zap.String("userID", userID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.InitUploadResponse{
		UploadId:               result.UploadID,
		ChunkSizeBytes:         result.ChunkSizeBytes,
		AlreadyReceivedOffsets: result.AlreadyReceivedOffsets,
		AlreadyComplete:        result.AlreadyComplete,
		ObjectId:               result.ObjectID,
	}), nil
}

// UploadChunk persists one chunk and is the browser-compatible counterpart to
// the bidi UploadChunks RPC, which browsers cannot invoke with a streaming request body.
func (s *UploadServer) UploadChunk(ctx context.Context, req *connect.Request[pb.UploadChunkRequest]) (*connect.Response[pb.UploadChunkAck], error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	if req.Msg.GetUploadId() == "" || req.Msg.GetOffset() < 0 || len(req.Msg.GetData()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_id, non-negative offset, and data are required"))
	}

	s.logger.Info("upload chunk received",
		zap.String("uploadID", req.Msg.GetUploadId()),
		zap.String("userID", userID),
		zap.Int64("offset", req.Msg.GetOffset()),
		zap.Int("bytes", len(req.Msg.GetData())))

	result, err := s.svc.ReceiveChunk(
		ctx,
		req.Msg.GetUploadId(),
		req.Msg.GetOffset(),
		req.Msg.GetData(),
		req.Msg.GetSha256OfChunk(),
	)
	if err != nil {
		s.logger.Error("upload chunk failed",
			zap.String("uploadID", req.Msg.GetUploadId()),
			zap.Int64("offset", req.Msg.GetOffset()),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.logger.Info("upload chunk persisted",
		zap.String("uploadID", req.Msg.GetUploadId()),
		zap.Int64("offsetPersisted", result.OffsetPersisted),
		zap.Bool("isFinal", result.IsFinal))

	return connect.NewResponse(&pb.UploadChunkAck{
		UploadId:        req.Msg.GetUploadId(),
		OffsetPersisted: result.OffsetPersisted,
		IsFinal:         result.IsFinal,
		Deduplicated:    result.Deduplicated,
	}), nil
}

// UploadChunks handles bidirectional streaming of chunks
// Client sends chunks, server acks each chunk as it's persisted
func (s *UploadServer) UploadChunks(ctx context.Context, stream *connect.BidiStream[pb.UploadChunkRequest, pb.UploadChunkAck]) error {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}

	// Read the first chunk to get upload_id
	firstReq, err := stream.Receive()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("stream ended prematurely"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("receive first chunk: %w", err))
	}

	uploadID := firstReq.GetUploadId()
	if uploadID == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("upload_id is required in first message"))
	}

	s.logger.Info("UploadChunks stream started",
		zap.String("uploadID", uploadID),
		zap.String("userID", userID))

	// Process the first chunk
	chunkResult, err := s.svc.ReceiveChunk(
		ctx,
		uploadID,
		firstReq.GetOffset(),
		firstReq.GetData(),
		firstReq.GetSha256OfChunk(),
	)
	if err != nil {
		s.logger.Error("failed to receive first chunk",
			zap.String("uploadID", uploadID),
			zap.Int64("offset", firstReq.GetOffset()),
			zap.Error(err))
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.logger.Debug("upload chunk persisted",
		zap.String("uploadID", uploadID),
		zap.Int64("offset", firstReq.GetOffset()),
		zap.Int("bytes", len(firstReq.GetData())),
		zap.Int64("offsetPersisted", chunkResult.OffsetPersisted))

	// Send ack for first chunk
	if err := stream.Send(&pb.UploadChunkAck{
		UploadId:        uploadID,
		OffsetPersisted: chunkResult.OffsetPersisted,
		IsFinal:         chunkResult.IsFinal,
	}); err != nil {
		s.logger.Error("failed to send ack for first chunk",
			zap.String("uploadID", uploadID),
			zap.Error(err))
		return connect.NewError(connect.CodeInternal, err)
	}

	if chunkResult.IsFinal {
		s.logger.Info("upload completed (single chunk)",
			zap.String("uploadID", uploadID))
		return nil
	}

	// Process remaining chunks
	for {
		req, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Stream closed by client, upload complete
				s.logger.Info("upload stream closed by client",
					zap.String("uploadID", uploadID))
				return nil
			}
			s.logger.Error("error receiving chunk",
				zap.String("uploadID", uploadID),
				zap.Error(err))
			return connect.NewError(connect.CodeInternal, fmt.Errorf("receive chunk: %w", err))
		}

		// Verify upload_id consistency
		if req.GetUploadId() != uploadID {
			s.logger.Error("upload_id mismatch",
				zap.String("expected", uploadID),
				zap.String("got", req.GetUploadId()))
			return connect.NewError(connect.CodeInvalidArgument, errors.New("upload_id mismatch in stream"))
		}

		// Process the chunk
		chunkResult, err := s.svc.ReceiveChunk(
			ctx,
			uploadID,
			req.GetOffset(),
			req.GetData(),
			req.GetSha256OfChunk(),
		)
		if err != nil {
			s.logger.Error("failed to receive chunk",
				zap.String("uploadID", uploadID),
				zap.Int64("offset", req.GetOffset()),
				zap.Error(err))
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.logger.Debug("upload chunk persisted",
			zap.String("uploadID", uploadID),
			zap.Int64("offset", req.GetOffset()),
			zap.Int("bytes", len(req.GetData())),
			zap.Int64("offsetPersisted", chunkResult.OffsetPersisted))

		// Send ack
		if err := stream.Send(&pb.UploadChunkAck{
			UploadId:        uploadID,
			OffsetPersisted: chunkResult.OffsetPersisted,
			IsFinal:         chunkResult.IsFinal,
		}); err != nil {
			s.logger.Error("failed to send ack",
				zap.String("uploadID", uploadID),
				zap.Int64("offset", req.GetOffset()),
				zap.Error(err))
			return connect.NewError(connect.CodeInternal, err)
		}

		if chunkResult.IsFinal {
			s.logger.Info("upload completed",
				zap.String("uploadID", uploadID))
			return nil
		}
	}
}

// GetUploadStatus returns the current status and last persisted offset
// Used for resuming uploads after connection interruption
func (s *UploadServer) GetUploadStatus(ctx context.Context, req *connect.Request[pb.GetUploadStatusRequest]) (*connect.Response[pb.GetUploadStatusResponse], error) {
	// Extract user ID from context
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	uploadID := req.Msg.GetUploadId()
	if uploadID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_id is required"))
	}

	// Call service
	result, err := s.svc.GetUploadStatus(ctx, uploadID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, connect.NewError(connect.CodeCanceled, err)
		}
		s.logger.Error("GetUploadStatus failed",
			zap.String("uploadID", uploadID),
			zap.String("userID", userID),
			zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.GetUploadStatusResponse{
		UploadId:            uploadID,
		LastPersistedOffset: result.LastPersistedOffset,
		TotalSizeBytes:      result.TotalSizeBytes,
		Status:              result.Status,
		ReceivedChunkIndices: result.ReceivedChunkIndices,
	}), nil
}
