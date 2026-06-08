package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SessionInfo describes a session without loading its full contents.
type SessionInfo struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"createdAt"`
	LastActivity time.Time `json:"lastActivity"`
	EntryCount   int       `json:"entryCount"`
}

type ChangeKind string

const (
	ChangeCreate  ChangeKind = "create"
	ChangeAppend  ChangeKind = "append"
	ChangeRewrite ChangeKind = "rewrite"
	ChangeDelete  ChangeKind = "delete"
)

type Change struct {
	AgentID   string
	SessionID string
	Kind      ChangeKind
	Entry     SessionEntry
	Snapshot  []SessionEntry
}

type EntryPage struct {
	Entries    []SessionEntry
	HasMore    bool
	NextBefore string
	Total      int
}

type entryOffset struct {
	ID        string
	Offset    int64
	Timestamp int64
}

type entryOffsetIndex struct {
	path    string
	size    int64
	modTime time.Time
	entries []entryOffset
	byID    map[string]int
}

const entryOffsetIndexPrefixLimit = 64 * 1024

// Store handles JSONL file I/O for sessions.
type Store struct {
	baseDir string
	mu      sync.Mutex
	subMu   sync.Mutex
	indexMu sync.Mutex

	subscribers map[string]map[chan Change]struct{}
	appender    func(Change)
	entryIndex  map[string]*entryOffsetIndex
}

// NewStore creates a new session store.
func NewStore(baseDir string) *Store {
	return &Store{
		baseDir:     baseDir,
		subscribers: make(map[string]map[chan Change]struct{}),
		entryIndex:  make(map[string]*entryOffsetIndex),
	}
}

// BaseDir returns the root directory where agent session files are stored.
func (s *Store) BaseDir() string {
	if s == nil {
		return ""
	}
	return s.baseDir
}

func (s *Store) SetChangeAppender(appender func(Change)) {
	if s == nil {
		return
	}
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.appender = appender
}

// sessionDir returns the directory for a given agent's sessions.
func (s *Store) sessionDir(agentID string) string {
	return filepath.Join(s.baseDir, agentID)
}

// sessionPath returns the file path for a session.
func (s *Store) sessionPath(agentID, sessionID string) string {
	return filepath.Join(s.sessionDir(agentID), sessionID+".jsonl")
}

// Load reads a session from its JSONL file.
func (s *Store) Load(agentID, sessionID string) (*Session, error) {
	path := s.sessionPath(agentID, sessionID)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			sess := NewSession(agentID, sessionID)
			sess.SetStore(s)
			return sess, nil
		}
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	sess := NewSession(agentID, sessionID)
	sess.SetStore(s)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry SessionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			runtimelogging.Warn("skipping malformed session entry", "error", err)
			continue
		}
		entry = CompactDurableEntry(entry)

		// Add to session without re-persisting. Older clients could persist
		// duplicate entry IDs, which would otherwise corrupt the parent map and
		// create a cycle in History().
		sess.addLoadedEntry(entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	return sess, nil
}

func (s *Store) EntryPage(agentID, sessionID string, limit int, before string) (EntryPage, error) {
	if limit <= 0 {
		limit = 80
	}
	if limit > 500 {
		limit = 500
	}
	before = strings.TrimSpace(before)

	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.entryOffsetIndexLocked(agentID, sessionID)
	if err != nil {
		return EntryPage{}, err
	}

	total := len(index.entries)
	end := total
	if before != "" {
		if idx, ok := index.byID[before]; ok {
			end = idx
		}
	}
	if end < 0 {
		end = 0
	}
	if end > total {
		end = total
	}
	start := end - limit
	if start < 0 {
		start = 0
	}

	selected := index.entries[start:end]
	window, err := s.readEntriesAtOffsetsLocked(index.path, selected)
	if err != nil {
		return EntryPage{}, err
	}
	nextBefore := ""
	if len(selected) > 0 {
		nextBefore = strings.TrimSpace(selected[0].ID)
	}
	return EntryPage{
		Entries:    append([]SessionEntry(nil), window...),
		HasMore:    start > 0,
		NextBefore: nextBefore,
		Total:      total,
	}, nil
}

func (s *Store) entryOffsetIndexLocked(agentID, sessionID string) (*entryOffsetIndex, error) {
	path := s.sessionPath(agentID, sessionID)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &entryOffsetIndex{path: path, byID: make(map[string]int)}, nil
		}
		return nil, fmt.Errorf("stat session file: %w", err)
	}
	key := entryOffsetIndexKey(path)

	s.indexMu.Lock()
	if index := s.entryIndex[key]; index != nil && index.size == info.Size() && index.modTime.Equal(info.ModTime()) {
		s.indexMu.Unlock()
		return index, nil
	}
	s.indexMu.Unlock()

	index, err := buildEntryOffsetIndex(path, info)
	if err != nil {
		return nil, err
	}

	s.indexMu.Lock()
	s.entryIndex[key] = index
	s.indexMu.Unlock()
	return index, nil
}

func buildEntryOffsetIndex(path string, info os.FileInfo) (*entryOffsetIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &entryOffsetIndex{path: path, byID: make(map[string]int)}, nil
		}
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	index := &entryOffsetIndex{
		path:    path,
		size:    info.Size(),
		modTime: info.ModTime(),
		byID:    make(map[string]int),
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	var offset int64
	for {
		lineStart := offset
		prefix, result := readSessionLinePrefix(reader)
		offset += int64(result.bytesRead)
		if len(prefix) > 0 {
			id := extractJSONPrefixString(prefix, "id")
			if id != "" {
				index.byID[id] = len(index.entries)
				index.entries = append(index.entries, entryOffset{
					ID:        id,
					Offset:    lineStart,
					Timestamp: extractJSONPrefixInt64(prefix, "timestamp"),
				})
			}
		}
		if result.err == io.EOF {
			break
		}
		if result.err != nil {
			return nil, fmt.Errorf("read session file: %w", result.err)
		}
	}
	return index, nil
}

type sessionLinePrefixRead struct {
	bytesRead int
	err       error
}

func readSessionLinePrefix(reader *bufio.Reader) ([]byte, sessionLinePrefixRead) {
	var prefix []byte
	total := 0
	for {
		chunk, err := reader.ReadSlice('\n')
		total += len(chunk)
		if len(chunk) > 0 && len(prefix) < entryOffsetIndexPrefixLimit {
			remaining := entryOffsetIndexPrefixLimit - len(prefix)
			if remaining > len(chunk) {
				remaining = len(chunk)
			}
			prefix = append(prefix, chunk[:remaining]...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && total == 0 {
			return nil, sessionLinePrefixRead{err: io.EOF}
		}
		return bytes.TrimSpace(prefix), sessionLinePrefixRead{bytesRead: total, err: err}
	}
}

func extractJSONPrefixString(data []byte, field string) string {
	value := extractJSONPrefixRawValue(data, field)
	if len(value) == 0 || value[0] != '"' {
		return ""
	}
	end := 1
	escaped := false
	for end < len(value) {
		ch := value[end]
		if escaped {
			escaped = false
			end++
			continue
		}
		if ch == '\\' {
			escaped = true
			end++
			continue
		}
		if ch == '"' {
			var out string
			if err := json.Unmarshal(value[:end+1], &out); err != nil {
				return ""
			}
			return strings.TrimSpace(out)
		}
		end++
	}
	return ""
}

func extractJSONPrefixInt64(data []byte, field string) int64 {
	value := extractJSONPrefixRawValue(data, field)
	if len(value) == 0 {
		return 0
	}
	end := 0
	for end < len(value) {
		ch := value[end]
		if (ch >= '0' && ch <= '9') || (end == 0 && ch == '-') {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.ParseInt(string(value[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func extractJSONPrefixRawValue(data []byte, field string) []byte {
	needle, err := json.Marshal(field)
	if err != nil {
		return nil
	}
	idx := bytes.Index(data, needle)
	if idx < 0 {
		return nil
	}
	rest := data[idx+len(needle):]
	rest = bytes.TrimLeft(rest, " \t\r\n")
	if len(rest) == 0 || rest[0] != ':' {
		return nil
	}
	rest = bytes.TrimLeft(rest[1:], " \t\r\n")
	return rest
}

func entryOffsetIndexKey(path string) string {
	return filepath.Clean(path)
}

func (s *Store) invalidateEntryOffsetIndexPath(path string) {
	if s == nil {
		return
	}
	key := entryOffsetIndexKey(path)
	s.indexMu.Lock()
	delete(s.entryIndex, key)
	s.indexMu.Unlock()
}

func (s *Store) updateEntryOffsetIndexOnAppendLocked(path string, offset int64, entry SessionEntry, size int64, modTime time.Time) {
	if s == nil || strings.TrimSpace(entry.ID) == "" {
		return
	}
	key := entryOffsetIndexKey(path)
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	index := s.entryIndex[key]
	if index == nil {
		return
	}
	if index.size != offset {
		delete(s.entryIndex, key)
		return
	}
	entryID := strings.TrimSpace(entry.ID)
	index.byID[entryID] = len(index.entries)
	index.entries = append(index.entries, entryOffset{
		ID:        entryID,
		Offset:    offset,
		Timestamp: entry.Timestamp,
	})
	index.size = size
	index.modTime = modTime
}

func (s *Store) readEntriesAtOffsetsLocked(path string, offsets []entryOffset) ([]SessionEntry, error) {
	if len(offsets) == 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	entries := make([]SessionEntry, 0, len(offsets))
	for _, item := range offsets {
		if _, err := f.Seek(item.Offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek session entry: %w", err)
		}
		reader := bufio.NewReaderSize(f, 64*1024)
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read session entry: %w", err)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry SessionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			runtimelogging.Warn("skipping malformed session entry", "error", err)
			continue
		}
		entries = append(entries, CompactDurableEntry(entry))
	}
	return entries, nil
}

func (s *Store) ForEachEntry(agentID, sessionID string, fn func(SessionEntry) bool) error {
	if fn == nil {
		return nil
	}
	path := s.sessionPath(agentID, sessionID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry SessionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			runtimelogging.Warn("skipping malformed session entry", "error", err)
			continue
		}
		entry = CompactDurableEntry(entry)
		if !fn(entry) {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session file: %w", err)
	}
	return nil
}

// AppendEntry writes a single entry to the session's JSONL file.
func (s *Store) AppendEntry(sess *Session, entry SessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry = CompactDurableEntry(entry)

	dir := s.sessionDir(sess.AgentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		runtimelogging.Error("failed to create session dir", "error", err)
		return
	}

	path := s.sessionPath(sess.AgentID, sess.ID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		runtimelogging.Error("failed to open session file", "error", err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		runtimelogging.Error("failed to marshal session entry", "error", err)
		return
	}

	offset := int64(0)
	if info, err := f.Stat(); err == nil {
		offset = info.Size()
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		runtimelogging.Error("failed to write session entry", "error", err)
		return
	}
	if info, err := f.Stat(); err == nil {
		s.updateEntryOffsetIndexOnAppendLocked(path, offset, entry, info.Size(), info.ModTime())
	} else {
		s.invalidateEntryOffsetIndexPath(path)
	}
	s.publishChange(Change{
		AgentID:   sess.AgentID,
		SessionID: sess.ID,
		Kind:      ChangeAppend,
		Entry:     entry,
	})
}

// Create creates an empty session file on disk so it shows up in List.
func (s *Store) Create(agentID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(agentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	path := s.sessionPath(agentID, sessionID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.invalidateEntryOffsetIndexPath(path)
	s.publishChange(Change{
		AgentID:   agentID,
		SessionID: sessionID,
		Kind:      ChangeCreate,
	})
	return nil
}

// List returns metadata for all sessions belonging to the given agent.
func (s *Store) List(agentID string) ([]SessionInfo, error) {
	dir := s.sessionDir(agentID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session dir: %w", err)
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")

		info := SessionInfo{ID: sessionID}

		s.mu.Lock()
		index, err := s.entryOffsetIndexLocked(agentID, sessionID)
		s.mu.Unlock()
		if err != nil {
			continue
		}

		info.EntryCount = len(index.entries)
		var firstTS, lastTS int64
		if len(index.entries) > 0 {
			firstTS = index.entries[0].Timestamp
			lastTS = index.entries[len(index.entries)-1].Timestamp
		}

		if firstTS > 0 {
			info.CreatedAt = time.Unix(firstTS, 0)
		} else {
			// Fall back to file modification time
			if fi, err := entry.Info(); err == nil {
				info.CreatedAt = fi.ModTime()
			}
		}
		if lastTS > 0 {
			info.LastActivity = time.Unix(lastTS, 0)
		} else {
			info.LastActivity = info.CreatedAt
		}

		sessions = append(sessions, info)
	}

	return sessions, nil
}

// Exists checks whether a session file exists for the given agent and session ID.
func (s *Store) Exists(agentID, sessionID string) bool {
	path := s.sessionPath(agentID, sessionID)
	_, err := os.Stat(path)
	return err == nil
}

// Rename renames a session file from oldSessionID to newSessionID.
func (s *Store) Rename(agentID, oldSessionID, newSessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldPath := s.sessionPath(agentID, oldSessionID)
	newPath := s.sessionPath(agentID, newSessionID)

	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		return fmt.Errorf("session %q does not exist", oldSessionID)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("session %q already exists", newSessionID)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	s.invalidateEntryOffsetIndexPath(oldPath)
	s.invalidateEntryOffsetIndexPath(newPath)
	return nil
}

// Delete removes a session's JSONL file.
func (s *Store) Delete(agentID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.sessionPath(agentID, sessionID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove session file: %w", err)
	}
	s.invalidateEntryOffsetIndexPath(path)
	s.publishChange(Change{
		AgentID:   agentID,
		SessionID: sessionID,
		Kind:      ChangeDelete,
	})
	return nil
}

// Rewrite replaces the entire session JSONL file with the current entries.
// Used after compaction to replace the old file.
func (s *Store) Rewrite(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sess.AgentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		runtimelogging.Error("failed to create session dir", "error", err)
		return
	}

	path := s.sessionPath(sess.AgentID, sess.ID)

	f, err := os.Create(path)
	if err != nil {
		runtimelogging.Error("failed to create session file for rewrite", "error", err)
		return
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, entry := range sess.Entries() {
		entry = CompactDurableEntry(entry)
		data, err := json.Marshal(entry)
		if err != nil {
			runtimelogging.Error("failed to marshal session entry", "error", err)
			continue
		}
		w.Write(data)
		w.WriteByte('\n')
	}

	if err := w.Flush(); err != nil {
		runtimelogging.Error("failed to flush session file", "error", err)
		return
	}
	s.invalidateEntryOffsetIndexPath(path)

	s.publishChange(Change{
		AgentID:   sess.AgentID,
		SessionID: sess.ID,
		Kind:      ChangeRewrite,
		Snapshot:  append([]SessionEntry(nil), sess.History()...),
	})
}

func (s *Store) Subscribe(agentID, sessionID string) (<-chan Change, func()) {
	ch := make(chan Change, 64)
	key := sessionSubscriptionKey(agentID, sessionID)

	s.subMu.Lock()
	if s.subscribers[key] == nil {
		s.subscribers[key] = make(map[chan Change]struct{})
	}
	s.subscribers[key][ch] = struct{}{}
	s.subMu.Unlock()

	cancel := func() {
		s.subMu.Lock()
		if subs := s.subscribers[key]; subs != nil {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(s.subscribers, key)
			}
		}
		s.subMu.Unlock()
		close(ch)
	}

	return ch, cancel
}

func (s *Store) publishChange(change Change) {
	if s == nil {
		return
	}

	key := sessionSubscriptionKey(change.AgentID, change.SessionID)
	s.subMu.Lock()
	appender := s.appender
	subs := make([]chan Change, 0, len(s.subscribers[key]))
	for ch := range s.subscribers[key] {
		subs = append(subs, ch)
	}
	s.subMu.Unlock()

	if appender != nil {
		appender(change)
	}

	for _, ch := range subs {
		func(ch chan Change) {
			defer func() {
				_ = recover()
			}()
			select {
			case ch <- change:
			default:
			}
		}(ch)
	}
}

func sessionSubscriptionKey(agentID, sessionID string) string {
	return strings.TrimSpace(agentID) + ":" + strings.TrimSpace(sessionID)
}
