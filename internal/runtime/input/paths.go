package input

import (
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// NormalizeLocalPath expands a user-facing local path into an absolute path.
// It is intentionally small: entrypoints decide when a string is a path.
func NormalizeLocalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			path = home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// IsImagePath reports whether a local path looks like a supported image file.
func IsImagePath(path string) bool {
	mimeType := strings.ToLower(strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))))
	return strings.HasPrefix(mimeType, "image/")
}

// BlockTypeForAttachmentName infers the runtime input block type from a file
// name and MIME type. Unknown binary/text files remain regular file blocks.
func BlockTypeForAttachmentName(name, mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case mimeType == "application/pdf":
		return "pdf"
	case strings.EqualFold(ext, ".pdf"):
		return "pdf"
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime.TypeByExtension(ext))), "image/"):
		return "image"
	default:
		return "file"
	}
}
