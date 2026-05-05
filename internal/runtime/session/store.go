package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"os"
	"path/filepath"
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

// Store handles JSONL file I/O for sessions.
type Store struct {
	baseDir string
	mu      sync.Mutex
	subMu   sync.Mutex

	subscribers map[string]map[chan Change]struct{}
	appender    func(Change)
}

// NewStore creates a new session store.
func NewStore(baseDir string) *Store {
	return &Store{
		baseDir:     baseDir,
		subscribers: make(map[string]map[chan Change]struct{}),
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

		// Add to session without re-persisting
		sess.entries = append(sess.entries, entry)
		sess.entryMap[entry.ID] = &sess.entries[len(sess.entries)-1]
		sess.leafID = entry.ID
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	return sess, nil
}

// AppendEntry writes a single entry to the session's JSONL file.
func (s *Store) AppendEntry(sess *Session, entry SessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		runtimelogging.Error("failed to write session entry", "error", err)
		return
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
		path := filepath.Join(dir, entry.Name())

		info := SessionInfo{ID: sessionID}

		// Count lines and extract timestamps from first/last entries
		f, err := os.Open(path)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
		var firstTS, lastTS int64
		lineCount := 0
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			lineCount++
			var partial struct {
				Timestamp int64 `json:"timestamp"`
			}
			if json.Unmarshal(line, &partial) == nil && partial.Timestamp > 0 {
				if firstTS == 0 {
					firstTS = partial.Timestamp
				}
				lastTS = partial.Timestamp
			}
		}
		f.Close()

		info.EntryCount = lineCount
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

	return os.Rename(oldPath, newPath)
}

// Delete removes a session's JSONL file.
func (s *Store) Delete(agentID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.sessionPath(agentID, sessionID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove session file: %w", err)
	}
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
