package interceptor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect/v2"
	"go.uber.org/zap"
)

// NewLoggingInterceptor creates a unary interceptor for structured logging
func NewLoggingInterceptor(logger *zap.Logger) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			start := time.Now()
			err := next(ctx, spec, stream)

			duration := time.Since(start)
			procedure := spec.Procedure
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
			return err
		}
	}
}
