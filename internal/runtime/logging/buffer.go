package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LogEntry is a single captured log record.
type LogEntry struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   string // pre-formatted key=value pairs
}

// Stream is the narrow runtime log surface consumed by gateway and hosted UIs.
type Stream interface {
	Snapshot() []LogEntry
	Subscribe() chan LogEntry
	Unsubscribe(chan LogEntry)
}

type logStore struct {
	mu      sync.RWMutex
	entries []LogEntry
	head    int
	count   int
	max     int
	subs    map[chan LogEntry]struct{}
}

// LogBuffer captures slog records into a ring buffer and streams new entries
// to subscribers such as the HTTP /logs surface.
type LogBuffer struct {
	store *logStore
	inner slog.Handler
}

// NewLogBuffer creates a log buffer with the given capacity that wraps an
// optional downstream slog handler.
func NewLogBuffer(capacity int, inner slog.Handler) *LogBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &LogBuffer{
		store: &logStore{
			entries: make([]LogEntry, capacity),
			max:     capacity,
			subs:    make(map[chan LogEntry]struct{}),
		},
		inner: inner,
	}
}

func (b *LogBuffer) Enabled(ctx context.Context, level slog.Level) bool {
	if b == nil {
		return false
	}
	if b.inner == nil {
		return true
	}
	return b.inner.Enabled(ctx, level)
}

func (b *LogBuffer) Handle(ctx context.Context, r slog.Record) error {
	if b == nil {
		return nil
	}
	attrs := ""
	r.Attrs(func(a slog.Attr) bool {
		if attrs != "" {
			attrs += " "
		}
		attrs += a.Key + "=" + a.Value.String()
		return true
	})

	entry := LogEntry{
		Time:    r.Time,
		Level:   r.Level,
		Message: r.Message,
		Attrs:   attrs,
	}

	s := b.store
	s.mu.Lock()
	s.entries[s.head] = entry
	s.head = (s.head + 1) % s.max
	if s.count < s.max {
		s.count++
	}
	for ch := range s.subs {
		select {
		case ch <- entry:
		default:
		}
	}
	s.mu.Unlock()

	if b.inner == nil {
		return nil
	}
	return b.inner.Handle(ctx, r)
}

func (b *LogBuffer) WithAttrs(attrs []slog.Attr) slog.Handler {
	if b == nil {
		return nil
	}
	var inner slog.Handler
	if b.inner != nil {
		inner = b.inner.WithAttrs(attrs)
	}
	return &LogBuffer{
		store: b.store,
		inner: inner,
	}
}

func (b *LogBuffer) WithGroup(name string) slog.Handler {
	if b == nil {
		return nil
	}
	var inner slog.Handler
	if b.inner != nil {
		inner = b.inner.WithGroup(name)
	}
	return &LogBuffer{
		store: b.store,
		inner: inner,
	}
}

// Snapshot returns current entries in chronological order.
func (b *LogBuffer) Snapshot() []LogEntry {
	s := b.store
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]LogEntry, s.count)
	start := (s.head - s.count + s.max) % s.max
	for i := 0; i < s.count; i++ {
		out[i] = s.entries[(start+i)%s.max]
	}
	return out
}

// Subscribe returns a buffered channel of new log entries.
func (b *LogBuffer) Subscribe() chan LogEntry {
	ch := make(chan LogEntry, 64)
	s := b.store
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (b *LogBuffer) Unsubscribe(ch chan LogEntry) {
	s := b.store
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
	close(ch)
}
