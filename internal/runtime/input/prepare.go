package input

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// PrepareOptions controls runtime-side input normalization.
type PrepareOptions struct {
	RunID   string
	BaseDir string
}

// PrepareEnvelope normalizes an input envelope for runtime execution. It keeps
// transport DTOs mechanical while making runtime own stable IDs, MIME/size
// metadata, and attachment storage.
func PrepareEnvelope(env InputEnvelope, opts PrepareOptions) (InputEnvelope, []*Attachment, error) {
	blocks := append([]InputBlock(nil), env.Blocks...)
	store := NewAttachmentStore(attachmentBaseDir(opts))
	attachments := make([]*Attachment, 0, len(blocks))
	storeBaseDir := store.BaseDir()

	for i := range blocks {
		block := &blocks[i]
		block.Type = strings.TrimSpace(strings.ToLower(block.Type))
		if block.Type == "file" || block.Type == "image" || block.Type == "pdf" || block.Type == "dir" {
			block.Path = NormalizeLocalPath(block.Path)
		}
		if strings.TrimSpace(block.ID) == "" {
			block.ID = fmt.Sprintf("input_%d", i+1)
		}
		if strings.TrimSpace(block.Name) == "" {
			block.Name = defaultBlockName(*block)
		}
		if strings.TrimSpace(block.MimeType) == "" {
			block.MimeType = ResolveMIME(*block)
		}
		if block.Meta == nil {
			block.Meta = map[string]any{}
		}

		switch block.Type {
		case "file", "image", "pdf":
			if isStoredAttachment(*block) {
				block.Data = nil
				if strings.TrimSpace(block.AttachmentID) != "" {
					block.Meta["attachment_id"] = block.AttachmentID
				}
				if size := fileSize(block.Path); size >= 0 {
					block.Meta["size"] = size
				}
				continue
			}
			if isInsideAttachmentStore(block.Path, storeBaseDir) {
				block.AttachmentID = firstNonEmpty(strings.TrimSpace(block.AttachmentID), strings.TrimSpace(block.ID))
				if strings.TrimSpace(block.AttachmentID) != "" {
					block.Meta["attachment_id"] = block.AttachmentID
				}
				if size := fileSize(block.Path); size >= 0 {
					block.Meta["size"] = size
				}
				block.Data = nil
				continue
			}
			att, err := persistAttachment(store, *block)
			if err != nil {
				return InputEnvelope{}, nil, err
			}
			block.Name = att.Name
			block.MimeType = att.MimeType
			block.Path = att.Path
			block.Data = nil
			block.AttachmentID = att.ID
			block.Meta["attachment_id"] = att.ID
			block.Meta["size"] = att.Size
			if strings.TrimSpace(att.Source) != "" {
				block.Meta["source_path"] = att.Source
			}
			attachments = append(attachments, att)
		case "dir":
			if size := dirEntryCount(block.Path); size >= 0 {
				block.Meta["entry_count"] = size
			}
		}
	}

	env.Blocks = blocks
	return env, attachments, nil
}

// ResolveMIME returns the runtime's best MIME guess for a block.
func ResolveMIME(block InputBlock) string {
	if strings.TrimSpace(block.MimeType) != "" {
		return strings.TrimSpace(block.MimeType)
	}
	for _, value := range []string{block.Path, block.Name} {
		if value == "" {
			continue
		}
		if guess := mime.TypeByExtension(strings.ToLower(filepath.Ext(value))); guess != "" {
			return guess
		}
	}
	if len(block.Data) > 0 {
		return http.DetectContentType(block.Data)
	}
	switch strings.TrimSpace(strings.ToLower(block.Type)) {
	case "image":
		return "image/png"
	case "pdf":
		return "application/pdf"
	case "text":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// ImageMIMEFromBytes returns the actual image MIME type from magic bytes,
// falling back to hint when the format is not recognized.
func ImageMIMEFromBytes(data []byte, hint string) string {
	hint = strings.TrimSpace(hint)
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 4 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}
	if len(data) >= 12 &&
		data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}
	if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
		return "image/bmp"
	}
	if hint != "" {
		return hint
	}
	return http.DetectContentType(data)
}

// ExtensionForMIME returns a compact extension for common MIME types.
func ExtensionForMIME(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
			return exts[0]
		}
		return ""
	}
}

func attachmentBaseDir(opts PrepareOptions) string {
	baseDir := strings.TrimSpace(opts.BaseDir)
	if baseDir == "" {
		baseDir = filepath.Join(".", "anyai", "assets")
	}
	runID := strings.TrimSpace(opts.RunID)
	if runID != "" {
		baseDir = filepath.Join(baseDir, runID)
	}
	return baseDir
}

func persistAttachment(store *AttachmentStore, block InputBlock) (*Attachment, error) {
	id := firstNonEmpty(strings.TrimSpace(block.AttachmentID), strings.TrimSpace(block.ID))
	name := defaultBlockName(block)
	mimeType := ResolveMIME(block)
	if len(block.Data) > 0 {
		return store.SaveWithID(id, name, mimeType, block.Data)
	}
	if strings.TrimSpace(block.Path) == "" {
		return &Attachment{
			ID:       id,
			Name:     name,
			MimeType: mimeType,
			Path:     strings.TrimSpace(block.Path),
		}, nil
	}
	return store.SaveFileWithID(id, name, mimeType, block.Path)
}

func defaultBlockName(block InputBlock) string {
	switch {
	case strings.TrimSpace(block.Name) != "":
		return strings.TrimSpace(block.Name)
	case strings.TrimSpace(block.Path) != "":
		return filepath.Base(strings.TrimSpace(block.Path))
	case strings.TrimSpace(block.URL) != "":
		return strings.TrimSpace(block.URL)
	case strings.TrimSpace(block.Type) != "":
		return strings.TrimSpace(block.Type)
	default:
		return "input"
	}
}

func dirEntryCount(path string) int {
	path = strings.TrimSpace(path)
	if path == "" {
		return -1
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return -1
	}
	return len(entries)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isStoredAttachment(block InputBlock) bool {
	if len(block.Data) > 0 || strings.TrimSpace(block.Path) == "" {
		return false
	}
	id := strings.TrimSpace(block.AttachmentID)
	if id == "" && block.Meta != nil {
		id, _ = block.Meta["attachment_id"].(string)
	}
	if strings.TrimSpace(id) == "" {
		return false
	}
	if _, err := os.Stat(block.Path); err != nil {
		return false
	}
	return true
}

func fileSize(path string) int64 {
	path = strings.TrimSpace(path)
	if path == "" {
		return -1
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return -1
	}
	return info.Size()
}

func isInsideAttachmentStore(path, baseDir string) bool {
	path = NormalizeLocalPath(path)
	baseDir = NormalizeLocalPath(baseDir)
	if path == "" || baseDir == "" {
		return false
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
