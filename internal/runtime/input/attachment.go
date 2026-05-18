package input

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// BaseDir returns the root directory used by this store.
func (s *AttachmentStore) BaseDir() string {
	if s == nil {
		return ""
	}
	return s.baseDir
}

// Save persists an attachment and returns its metadata.
func (s *AttachmentStore) Save(name, mimeType string, data []byte) (*Attachment, error) {
	return s.SaveWithID(generateAttachmentID(), name, mimeType, data)
}

// SaveWithID persists an attachment using a caller-supplied stable ID.
func (s *AttachmentStore) SaveWithID(id, name, mimeType string, data []byte) (*Attachment, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = generateAttachmentID()
	}
	return s.saveWithRelativePath(id, filepath.Join(id, sanitizeAttachmentName(name)), mimeType, data, "")
}

// SaveAt persists an attachment at a caller-supplied relative path under the
// store base directory. The returned ID remains the lookup/stable reference.
func (s *AttachmentStore) SaveAt(id, relativePath, mimeType string, data []byte) (*Attachment, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = generateAttachmentID()
	}
	return s.saveWithRelativePath(id, relativePath, mimeType, data, "")
}

func (s *AttachmentStore) saveWithRelativePath(id, relativePath, mimeType string, data []byte, source string) (*Attachment, error) {
	if s == nil {
		return nil, fmt.Errorf("attachment store is nil")
	}
	name, relPath := sanitizeAttachmentPath(relativePath)
	filePath := filepath.Join(s.baseDir, relPath)
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create attachment dir: %w", err)
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write attachment: %w", err)
	}
	att := &Attachment{
		ID:        id,
		Name:      name,
		MimeType:  mimeType,
		Size:      int64(len(data)),
		Path:      filePath,
		Source:    strings.TrimSpace(source),
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.entries[id] = att
	s.mu.Unlock()
	return att, nil
}

// SaveFileWithID copies a filesystem file into the attachment store using a
// caller-supplied stable ID.
func (s *AttachmentStore) SaveFileWithID(id, name, mimeType, sourcePath string) (*Attachment, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = generateAttachmentID()
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return nil, fmt.Errorf("source path is required")
	}
	src, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open attachment source: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat attachment source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment source is not a regular file")
	}

	name, relPath := sanitizeAttachmentPath(filepath.Join(id, sanitizeAttachmentName(name)))
	filePath := filepath.Join(s.baseDir, relPath)
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create attachment dir: %w", err)
	}
	dst, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return nil, fmt.Errorf("copy attachment: %w", err)
	}
	if err := dst.Close(); err != nil {
		return nil, fmt.Errorf("close attachment: %w", err)
	}

	att := &Attachment{
		ID:        id,
		Name:      name,
		MimeType:  mimeType,
		Size:      info.Size(),
		Path:      filePath,
		Source:    sourcePath,
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

func sanitizeAttachmentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "attachment"
	}
	name = filepath.Base(name)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return "attachment"
	}
	return name
}

func sanitizeAttachmentPath(path string) (string, string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "attachment", "attachment"
	}
	parts := strings.FieldsFunc(filepath.Clean(path), func(r rune) bool {
		return r == '/' || r == '\\'
	})
	sanitized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = sanitizeAttachmentName(part)
		if part == "" || part == "." || part == ".." {
			continue
		}
		sanitized = append(sanitized, part)
	}
	if len(sanitized) == 0 {
		return "attachment", "attachment"
	}
	return sanitized[len(sanitized)-1], filepath.Join(sanitized...)
}
