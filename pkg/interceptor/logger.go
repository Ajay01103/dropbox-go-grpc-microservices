package interceptor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"
)

// NewLoggingInterceptor creates a unary interceptor for structured logging
func NewLoggingInterceptor(logger *zap.Logger) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			start := time.Now()
			resp, err := next(ctx, req)

			duration := time.Since(start)
			procedure := req.Spec().Procedure
			ms := fmt.Sprintf("%dms", duration.Milliseconds())

			if err != nil {
				var connectErr *connect.Error
				if errors.As(err, &connectErr) {
					logger.Error("request failed",
						zap.String("procedure", procedure),
						zap.String("duration", ms),
						zap.String("code", connectErr.Code().String()),
						zap.String("error", connectErr.Message()),
					)
				} else {
					logger.Error("request failed",
						zap.String("procedure", procedure),
						zap.String("duration", ms),
						zap.String("error", err.Error()),
					)
				}
			} else {
				logger.Info("request succeeded",
					zap.String("procedure", procedure),
					zap.String("duration", ms),
				)
			}
			return resp, err
		}
	}
}
