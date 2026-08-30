package server

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-notion/upload/gen/pb"
	"github.com/Ajay01103/go-notion/upload/gen/pb/pbconnect"
	"github.com/Ajay01103/go-notion/upload/internal/service"
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

// InitUpload creates a new resumable upload session
func (s *UploadServer) InitUpload(ctx context.Context, req *connect.Request[pb.InitUploadRequest]) (*connect.Response[pb.InitUploadResponse], error) {
	// Extract user ID from context (set by auth interceptor)
	userID, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
	}

	// Validate request
	if req.Msg.GetFilename() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("filename is required"))
	}
	if req.Msg.GetTotalSizeBytes() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("total_size_bytes must be positive"))
	}

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

// UploadChunks handles bidirectional streaming of chunks
// Client sends chunks, server acks each chunk as it's persisted
func (s *UploadServer) UploadChunks(ctx context.Context, stream *connect.BidiStream[pb.UploadChunkRequest, pb.UploadChunkAck]) error {
	// Extract user ID from context
	userID, ok := ctx.Value("user_id").(string)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
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

	// Send ack for first chunk
	if err := stream.Send(&pb.UploadChunkAck{
		UploadId:         uploadID,
		OffsetPersisted: chunkResult.OffsetPersisted,
		IsFinal:          chunkResult.IsFinal,
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

		// Send ack
		if err := stream.Send(&pb.UploadChunkAck{
			UploadId:         uploadID,
			OffsetPersisted: chunkResult.OffsetPersisted,
			IsFinal:          chunkResult.IsFinal,
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
	userID, ok := ctx.Value("user_id").(string)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid user_id in context"))
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
		UploadId:             uploadID,
		LastPersistedOffset: result.LastPersistedOffset,
		TotalSizeBytes:      result.TotalSizeBytes,
		Status:              result.Status,
	}), nil
}
