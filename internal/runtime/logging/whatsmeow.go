package logging

import (
	"fmt"
	"log/slog"
	"strings"

	waLog "go.mau.fi/whatsmeow/util/log"
)

type whatsMeowSlogAdapter struct {
	logger   *slog.Logger
	minLevel slog.Level
}

func NewWhatsMeowLogger(module string) waLog.Logger {
	level := slog.LevelWarn
	if runtime := Current(); runtime != nil {
		level = runtime.whatsMeowThreshold()
	}
	base := Component("whatsmeow")
	if name := strings.TrimSpace(module); name != "" {
		base = base.With("module", name)
	}
	return &whatsMeowSlogAdapter{
		logger:   base,
		minLevel: level,
	}
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
	if l == nil {
		return NewWhatsMeowLogger(module)
	}
	logger := l.logger
	if name := strings.TrimSpace(module); name != "" {
		logger = logger.With("module", name)
	}
	return &whatsMeowSlogAdapter{
		logger:   logger,
		minLevel: l.minLevel,
	}
}

func (l *whatsMeowSlogAdapter) logf(level slog.Level, format string, args ...interface{}) {
	if l == nil || l.logger == nil || level < l.minLevel {
		return
	}
	l.logger.Log(nil, level, fmt.Sprintf(format, args...))
}
