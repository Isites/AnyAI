package input

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Attachment represents a stored attachment.
type Attachment struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MimeType  string    `json:"mime_type"`
	Size      int64     `json:"size"`
	Path      string    `json:"path"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AttachmentStore provides unified attachment storage.
type AttachmentStore struct {
	baseDir string
	mu      sync.RWMutex
	entries map[string]*Attachment
}

// NewAttachmentStore creates an AttachmentStore rooted at baseDir.
func NewAttachmentStore(baseDir string) *AttachmentStore {
	return &AttachmentStore{
		baseDir: baseDir,
		entries: make(map[string]*Attachment),
	}
}

// Save persists an attachment and returns its metadata.
func (s *AttachmentStore) Save(name, mimeType string, data []byte) (*Attachment, error) {
	id := generateAttachmentID()
	dir := filepath.Join(s.baseDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create attachment dir: %w", err)
	}
	filePath := filepath.Join(dir, name)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write attachment: %w", err)
	}
	att := &Attachment{
		ID:        id,
		Name:      name,
		MimeType:  mimeType,
		Size:      int64(len(data)),
		Path:      filePath,
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.entries[id] = att
	s.mu.Unlock()
	return att, nil
}

// Get retrieves an attachment by ID.
func (s *AttachmentStore) Get(id string) (*Attachment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	att, ok := s.entries[id]
	if !ok {
		return nil, false
	}
	return att, true
}

func generateAttachmentID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "att_" + hex.EncodeToString(b)
}
