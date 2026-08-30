package logger

import (
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func New() *zap.Logger {
	encoderCfg := zapcore.EncoderConfig{
		TimeKey:       "T",
		LevelKey:      "L",
		MessageKey:    "M",
		CallerKey:     "", // no caller
		FunctionKey:   "", // no function
		StacktraceKey: "", // no stacktrace
		LineEnding:    zapcore.DefaultLineEnding,
		EncodeTime:    zapcore.TimeEncoderOfLayout("15:04:05"),
		EncodeLevel:   zapcore.CapitalColorLevelEncoder,
		EncodeDuration: func(d time.Duration, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(fmt.Sprintf("%dms", d.Milliseconds()))
		},
		EncodeCaller: nil,
	}

	logger := zap.New(zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.Lock(os.Stdout),
		zap.NewAtomicLevelAt(zap.InfoLevel),
	))

	return logger
}
