package logging

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const defaultBufferCapacity = 2000

type Options struct {
	DataDir        string
	MirrorStderr   bool
	BufferCapacity int
	FileLevel      slog.Level // zero value means "use module default"
	StderrLevel    slog.Level // zero value means "use module default"
	WhatsMeowLevel slog.Level // zero value means "use module default"
	Rotation       RotatingFileOptions
}

type Runtime struct {
	logger         *slog.Logger
	buffer         *LogBuffer
	writer         *RotatingFileWriter
	previous       *slog.Logger
	whatsMeowLevel slog.Level

	mu     sync.Mutex
	closed bool
}

var activeRuntime struct {
	mu sync.RWMutex
	rt *Runtime
}

func DefaultOptions(dataDir string) Options {
	return Options{
		DataDir:        strings.TrimSpace(dataDir),
		BufferCapacity: defaultBufferCapacity,
		FileLevel:      slog.LevelDebug,
		StderrLevel:    slog.LevelInfo,
		WhatsMeowLevel: slog.LevelWarn,
	}
}

func Install(opts Options) (*Runtime, error) {
	opts = normalizeOptions(opts)
	if opts.DataDir == "" {
		return nil, fmt.Errorf("logging data dir is required")
	}

	logsDir := filepath.Join(opts.DataDir, "logs")
	fileWriter, err := NewRotatingFileWriter(logsDir, opts.Rotation)
	if err != nil {
		return nil, fmt.Errorf("create rotating file writer: %w", err)
	}

	handlers := make([]slog.Handler, 0, 2)
	handlers = append(handlers, slog.NewTextHandler(fileWriter, &slog.HandlerOptions{Level: opts.FileLevel}))
	if opts.MirrorStderr {
		handlers = append(handlers, slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: opts.StderrLevel}))
	}

	logBuf := NewLogBuffer(opts.BufferCapacity, NewMultiHandler(handlers...))
	runtime := &Runtime{
		logger:         slog.New(logBuf),
		buffer:         logBuf,
		writer:         fileWriter,
		whatsMeowLevel: opts.WhatsMeowLevel,
	}

	activeRuntime.mu.Lock()
	if current := activeRuntime.rt; current != nil {
		runtime.previous = current.previous
		current.closeWithoutRestore()
	} else {
		runtime.previous = slog.Default()
	}
	slog.SetDefault(runtime.logger)
	activeRuntime.rt = runtime
	activeRuntime.mu.Unlock()

	return runtime, nil
}

// SetupRuntime keeps the previous startup-facing API while calling to the
// unified logging runtime installer.
func SetupRuntime(dataDir string, mirrorStderr bool) (*LogBuffer, func(), error) {
	runtime, err := Install(Options{
		DataDir:      dataDir,
		MirrorStderr: mirrorStderr,
	})
	if err != nil {
		return nil, nil, err
	}
	return runtime.Buffer(), func() {
		_ = runtime.Close()
	}, nil
}

func Current() *Runtime {
	activeRuntime.mu.RLock()
	defer activeRuntime.mu.RUnlock()
	return activeRuntime.rt
}

func Logger() *slog.Logger {
	if runtime := Current(); runtime != nil {
		return runtime.Logger()
	}
	return slog.Default()
}

func Component(name string) *slog.Logger {
	name = strings.TrimSpace(name)
	if name == "" {
		return Logger()
	}
	return Logger().With("component", name)
}

func Buffer() *LogBuffer {
	if runtime := Current(); runtime != nil {
		return runtime.Buffer()
	}
	return nil
}

func (r *Runtime) Logger() *slog.Logger {
	if r == nil || r.logger == nil {
		return slog.Default()
	}
	return r.logger
}

func (r *Runtime) Component(name string) *slog.Logger {
	name = strings.TrimSpace(name)
	if name == "" {
		return r.Logger()
	}
	return r.Logger().With("component", name)
}

func (r *Runtime) Buffer() *LogBuffer {
	if r == nil {
		return nil
	}
	return r.buffer
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}

	activeRuntime.mu.Lock()
	defer activeRuntime.mu.Unlock()

	if activeRuntime.rt == r {
		slog.SetDefault(r.previous)
		activeRuntime.rt = nil
	}
	return r.closeLocked()
}

func (r *Runtime) closeWithoutRestore() error {
	if r == nil {
		return nil
	}
	return r.closeLocked()
}

func (r *Runtime) closeLocked() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.writer == nil {
		return nil
	}
	return r.writer.Close()
}

func (r *Runtime) whatsMeowThreshold() slog.Level {
	if r == nil {
		return slog.LevelWarn
	}
	return r.whatsMeowLevel
}

func normalizeOptions(opts Options) Options {
	normalized := DefaultOptions(opts.DataDir)
	normalized.MirrorStderr = opts.MirrorStderr
	if strings.TrimSpace(opts.DataDir) != "" {
		normalized.DataDir = strings.TrimSpace(opts.DataDir)
	}
	if opts.BufferCapacity > 0 {
		normalized.BufferCapacity = opts.BufferCapacity
	}
	if opts.FileLevel != 0 {
		normalized.FileLevel = opts.FileLevel
	}
	if opts.StderrLevel != 0 {
		normalized.StderrLevel = opts.StderrLevel
	}
	if opts.WhatsMeowLevel != 0 {
		normalized.WhatsMeowLevel = opts.WhatsMeowLevel
	}
	if opts.Rotation != (RotatingFileOptions{}) {
		normalized.Rotation = opts.Rotation
	}
	return normalized
}
