package gateway

import (
	"fmt"
	"log/slog"
	"strings"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func Debug(msg string, args ...any) {
	slog.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	slog.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	slog.Error(msg, args...)
}

type whatsMeowSlogAdapter struct {
	logger *slog.Logger
}

func NewWhatsMeowLogger(module string) waLog.Logger {
	logger := slog.Default().With("component", "whatsmeow")
	if module = strings.TrimSpace(module); module != "" {
		logger = logger.With("module", module)
	}
	return &whatsMeowSlogAdapter{logger: logger}
}

func (l *whatsMeowSlogAdapter) Warnf(msg string, args ...interface{}) {
	l.logf(slog.LevelWarn, msg, args...)
}

func (l *whatsMeowSlogAdapter) Errorf(msg string, args ...interface{}) {
	l.logf(slog.LevelError, msg, args...)
}

func (l *whatsMeowSlogAdapter) Infof(msg string, args ...interface{}) {
	l.logf(slog.LevelInfo, msg, args...)
}

func (l *whatsMeowSlogAdapter) Debugf(msg string, args ...interface{}) {
	l.logf(slog.LevelDebug, msg, args...)
}

func (l *whatsMeowSlogAdapter) Sub(module string) waLog.Logger {
	logger := slog.Default().With("component", "whatsmeow")
	if l != nil && l.logger != nil {
		logger = l.logger
	}
	if module = strings.TrimSpace(module); module != "" {
		logger = logger.With("module", module)
	}
	return &whatsMeowSlogAdapter{logger: logger}
}

func (l *whatsMeowSlogAdapter) logf(level slog.Level, format string, args ...interface{}) {
	if l == nil || l.logger == nil {
		return
	}
	l.logger.Log(nil, level, fmt.Sprintf(format, args...))
}
