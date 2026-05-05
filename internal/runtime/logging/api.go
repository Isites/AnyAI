package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

func Debug(msg string, args ...any) {
	Logger().Debug(msg, args...)
}

func Info(msg string, args ...any) {
	Logger().Info(msg, args...)
}

func Warn(msg string, args ...any) {
	Logger().Warn(msg, args...)
}

func Error(msg string, args ...any) {
	Logger().Error(msg, args...)
}

func DebugContext(ctx context.Context, msg string, args ...any) {
	Logger().DebugContext(ctx, msg, args...)
}

func InfoContext(ctx context.Context, msg string, args ...any) {
	Logger().InfoContext(ctx, msg, args...)
}

func WarnContext(ctx context.Context, msg string, args ...any) {
	Logger().WarnContext(ctx, msg, args...)
}

func ErrorContext(ctx context.Context, msg string, args ...any) {
	Logger().ErrorContext(ctx, msg, args...)
}

func ParseLevel(value string) (slog.Level, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", value)
	}
}
