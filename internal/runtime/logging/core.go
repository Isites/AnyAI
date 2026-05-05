package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultRuntimeLogFilename   = "runtime.log"
	defaultRuntimeLogMaxBytes   = 10 << 20
	defaultRuntimeLogMaxBackups = 20
)

// MultiHandler fans a slog record out to multiple downstream handlers.
type MultiHandler struct {
	handlers []slog.Handler
}

func NewMultiHandler(handlers ...slog.Handler) slog.Handler {
	filtered := make([]slog.Handler, 0, len(handlers))
	for _, handler := range handlers {
		if handler != nil {
			filtered = append(filtered, handler)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return &MultiHandler{handlers: filtered}
	}
}

func (h *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h == nil {
		return false
	}
	for _, handler := range h.handlers {
		if handler != nil && handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	if h == nil {
		return nil
	}
	var firstErr error
	for _, handler := range h.handlers {
		if handler == nil || !handler.Enabled(ctx, r.Level) {
			continue
		}
		if err := handler.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if h == nil {
		return nil
	}
	derived := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		if handler != nil {
			derived = append(derived, handler.WithAttrs(attrs))
		}
	}
	return NewMultiHandler(derived...)
}

func (h *MultiHandler) WithGroup(name string) slog.Handler {
	if h == nil {
		return nil
	}
	derived := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		if handler != nil {
			derived = append(derived, handler.WithGroup(name))
		}
	}
	return NewMultiHandler(derived...)
}

type RotatingFileOptions struct {
	Filename   string
	MaxBytes   int64
	MaxBackups int
}

type RotatingFileWriter struct {
	mu         sync.Mutex
	dir        string
	filename   string
	activePath string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

func NewRotatingFileWriter(dir string, opts RotatingFileOptions) (*RotatingFileWriter, error) {
	filename := strings.TrimSpace(opts.Filename)
	if filename == "" {
		filename = defaultRuntimeLogFilename
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultRuntimeLogMaxBytes
	}
	maxBackups := opts.MaxBackups
	if maxBackups <= 0 {
		maxBackups = defaultRuntimeLogMaxBackups
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create logs dir: %w", err)
	}
	writer := &RotatingFileWriter{
		dir:        dir,
		filename:   filename,
		activePath: filepath.Join(dir, filename),
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.rotateCurrentLocked(); err != nil {
		return nil, err
	}
	if err := writer.openLocked(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *RotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.openLocked(); err != nil {
			return 0, err
		}
	}
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateCurrentLocked(); err != nil {
			return 0, err
		}
		if err := w.openLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.size = 0
	return err
}

func (w *RotatingFileWriter) ActivePath() string {
	if w == nil {
		return ""
	}
	return w.activePath
}

func (w *RotatingFileWriter) rotateCurrentLocked() error {
	info, err := os.Stat(w.activePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat active log: %w", err)
	}
	if info.Size() == 0 {
		return nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("close active log: %w", err)
		}
		w.file = nil
		w.size = 0
	}
	target, err := w.nextArchivePathLocked()
	if err != nil {
		return err
	}
	if err := os.Rename(w.activePath, target); err != nil {
		return fmt.Errorf("rotate active log: %w", err)
	}
	return w.pruneArchivesLocked()
}

func (w *RotatingFileWriter) openLocked() error {
	file, err := os.OpenFile(w.activePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open active log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("stat active log file: %w", err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *RotatingFileWriter) nextArchivePathLocked() (string, error) {
	stem := strings.TrimSuffix(w.filename, filepath.Ext(w.filename))
	ext := filepath.Ext(w.filename)
	timestamp := time.Now().Format("20060102-150405.000")
	for seq := 0; seq < 1000; seq++ {
		suffix := ""
		if seq > 0 {
			suffix = fmt.Sprintf("-%02d", seq)
		}
		candidate := filepath.Join(w.dir, fmt.Sprintf("%s-%s%s%s", stem, timestamp, suffix, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("stat archive candidate: %w", err)
		}
	}
	return "", fmt.Errorf("allocate archive path for %s", w.filename)
}

func (w *RotatingFileWriter) pruneArchivesLocked() error {
	if w.maxBackups <= 0 {
		return nil
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("read logs dir: %w", err)
	}
	stem := strings.TrimSuffix(w.filename, filepath.Ext(w.filename))
	ext := filepath.Ext(w.filename)
	prefix := stem + "-"
	type archived struct {
		name    string
		modTime time.Time
	}
	archives := make([]archived, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == w.filename || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat archived log %s: %w", name, err)
		}
		archives = append(archives, archived{name: name, modTime: info.ModTime()})
	}
	if len(archives) <= w.maxBackups {
		return nil
	}
	sort.Slice(archives, func(i, j int) bool {
		if archives[i].modTime.Equal(archives[j].modTime) {
			return archives[i].name < archives[j].name
		}
		return archives[i].modTime.Before(archives[j].modTime)
	})
	for _, item := range archives[:len(archives)-w.maxBackups] {
		if err := os.Remove(filepath.Join(w.dir, item.name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove archived log %s: %w", item.name, err)
		}
	}
	return nil
}

func NewRotatingFileHandler(dir string, opts RotatingFileOptions) (*RotatingFileWriter, slog.Handler, error) {
	writer, err := NewRotatingFileWriter(dir, opts)
	if err != nil {
		return nil, nil, err
	}
	handler := slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug})
	return writer, handler, nil
}

var _ io.WriteCloser = (*RotatingFileWriter)(nil)
