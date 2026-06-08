package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Isites/anyai/internal/runtime/llm"
)

const (
	defaultReadFileMaxBytes      = 64 * 1024
	defaultReadFileLineCount     = 200
	defaultReadFileSearchContext = 2
	defaultReadFileSearchMatches = 20
)

// ReadFileTool reads the contents of a file.
type ReadFileTool struct {
	WorkDir string // base directory for resolving relative paths
}

type readFileInput struct {
	Path         string `json:"path"`
	Offset       *int64 `json:"offset,omitempty"`
	Limit        *int64 `json:"limit,omitempty"`
	MaxBytes     *int64 `json:"max_bytes,omitempty"`
	LineStart    *int   `json:"line_start,omitempty"`
	LineCount    *int   `json:"line_count,omitempty"`
	Query        string `json:"query,omitempty"`
	ContextLines *int   `json:"context_lines,omitempty"`
	MaxMatches   *int   `json:"max_matches,omitempty"`
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Description() string {
	return "Read file contents. Prefer query, line_start/line_count, or offset/limit for large files so only relevant slices enter context. For image files (jpg, png, gif, webp, bmp), returns the image for visual inspection."
}

func (t *ReadFileTool) ToolMetadata() ToolMetadata {
	return readOnlyToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *ReadFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The absolute or relative path to the file to read"
			},
			"offset": {
				"type": "integer",
				"description": "Optional zero-based byte offset. Use with limit to page through large files."
			},
			"limit": {
				"type": "integer",
				"description": "Optional maximum bytes to return from offset. Defaults to max_bytes or a safe preview limit."
			},
			"max_bytes": {
				"type": "integer",
				"description": "Optional maximum bytes to return when reading from the start. Use next_offset from metadata to continue."
			},
			"line_start": {
				"type": "integer",
				"description": "Optional one-based starting line. Use with line_count to inspect a focused text range."
			},
			"line_count": {
				"type": "integer",
				"description": "Optional number of lines to return. Defaults to 200 when line_start is set."
			},
			"query": {
				"type": "string",
				"description": "Optional substring to search for. Returns matching lines with surrounding context instead of the whole file."
			},
			"context_lines": {
				"type": "integer",
				"description": "Optional number of surrounding lines for query matches. Defaults to 2."
			},
			"max_matches": {
				"type": "integer",
				"description": "Optional maximum number of query matches to return. Defaults to 20."
			}
		},
		"required": ["path"]
	}`)
}

// imageExtMap maps file extensions to MIME types for image files.
var imageExtMap = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// detectImageMIMEFromBytes inspects magic bytes to determine the actual image
// format, falling back to hint (typically derived from file extension).
func detectImageMIMEFromBytes(data []byte, hint string) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 4 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}
	if len(data) >= 4 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' {
		return "image/webp"
	}
	return hint
}

func (t *ReadFileTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in readFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	targetPath := resolvePathForBase(in.Path, t.WorkDir)

	data, err := os.ReadFile(targetPath)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}

	// Check if this is an image file
	ext := strings.ToLower(filepath.Ext(targetPath))
	if mimeType, ok := imageExtMap[ext]; ok && !readFileRequestsTextSlice(in) {
		mimeType = detectImageMIMEFromBytes(data, mimeType)
		return ToolResult{
			Output: fmt.Sprintf("Image file: %s (%d bytes)", filepath.Base(targetPath), len(data)),
			Images: []llm.ImageContent{
				{MimeType: mimeType, Data: data},
			},
		}, nil
	}

	return readTextFileSlice(data, in, targetPath), nil
}

func readFileRequestsTextSlice(in readFileInput) bool {
	return in.Offset != nil || in.Limit != nil || in.MaxBytes != nil ||
		in.LineStart != nil || in.LineCount != nil || strings.TrimSpace(in.Query) != ""
}

func readTextFileSlice(data []byte, in readFileInput, path string) ToolResult {
	if err := validateReadFileInput(in); err != nil {
		return ToolResult{Error: err.Error()}
	}
	switch {
	case strings.TrimSpace(in.Query) != "":
		return searchTextFile(data, in, path)
	case in.LineStart != nil || in.LineCount != nil:
		return readTextFileLines(data, in, path)
	default:
		return readTextFileBytes(data, in, path)
	}
}

func validateReadFileInput(in readFileInput) error {
	hasByteRange := in.Offset != nil || in.Limit != nil
	hasLineRange := in.LineStart != nil || in.LineCount != nil
	hasSearch := strings.TrimSpace(in.Query) != ""
	if hasSearch && (hasByteRange || hasLineRange || in.MaxBytes != nil) {
		return fmt.Errorf("query cannot be combined with offset/limit, max_bytes, line_start, or line_count")
	}
	if hasByteRange && hasLineRange {
		return fmt.Errorf("offset/limit cannot be combined with line_start/line_count")
	}
	if in.Offset != nil && *in.Offset < 0 {
		return fmt.Errorf("offset must be >= 0")
	}
	for name, value := range map[string]*int64{"limit": in.Limit, "max_bytes": in.MaxBytes} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("%s must be > 0", name)
		}
	}
	if in.LineStart != nil && *in.LineStart <= 0 {
		return fmt.Errorf("line_start must be >= 1")
	}
	for name, value := range map[string]*int{"line_count": in.LineCount, "context_lines": in.ContextLines, "max_matches": in.MaxMatches} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%s must be >= 0", name)
		}
	}
	return nil
}

func readTextFileBytes(data []byte, in readFileInput, path string) ToolResult {
	size := int64(len(data))
	start := int64(0)
	if in.Offset != nil {
		start = *in.Offset
	}
	if start > size {
		start = size
	}

	limit := int64(defaultReadFileMaxBytes)
	if in.MaxBytes != nil {
		limit = *in.MaxBytes
	}
	if in.Limit != nil {
		limit = *in.Limit
	}
	end := start + limit
	if end > size {
		end = size
	}

	output := safeUTF8Slice(data, start, end)
	truncated := end < size
	metadata := readFileMetadata(path, size)
	metadata["mode"] = "bytes"
	metadata["offset"] = start
	metadata["bytes_read"] = end - start
	metadata["truncated"] = truncated
	if truncated {
		metadata["next_offset"] = end
	}
	if truncated || start > 0 {
		output += fmt.Sprintf("\n[read_file: returned bytes %d-%d of %d; use offset=%d to continue]", start, end, size, end)
	}
	return ToolResult{Output: output, Metadata: metadata}
}

func readTextFileLines(data []byte, in readFileInput, path string) ToolResult {
	text := string(data)
	text = strings.TrimSuffix(text, "\n")
	lines := strings.SplitAfter(text, "\n")
	if text == "" {
		lines = nil
	} else if len(lines) > 0 {
		lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "\n")
	}

	startLine := 1
	if in.LineStart != nil {
		startLine = *in.LineStart
	}
	count := defaultReadFileLineCount
	if in.LineCount != nil {
		count = *in.LineCount
	}
	startIdx := startLine - 1
	if startIdx > len(lines) {
		startIdx = len(lines)
	}
	endIdx := startIdx + count
	if endIdx > len(lines) {
		endIdx = len(lines)
	}

	var b strings.Builder
	for i := startIdx; i < endIdx; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i+1, strings.TrimSuffix(lines[i], "\n"))
	}
	truncated := endIdx < len(lines)
	metadata := readFileMetadata(path, int64(len(data)))
	metadata["mode"] = "lines"
	metadata["line_start"] = startLine
	metadata["line_count"] = endIdx - startIdx
	metadata["total_lines"] = len(lines)
	metadata["truncated"] = truncated
	if truncated {
		metadata["next_line_start"] = endIdx + 1
		fmt.Fprintf(&b, "[read_file: returned lines %d-%d of %d; use line_start=%d to continue]\n", startIdx+1, endIdx, len(lines), endIdx+1)
	}
	return ToolResult{Output: strings.TrimSuffix(b.String(), "\n"), Metadata: metadata}
}

func searchTextFile(data []byte, in readFileInput, path string) ToolResult {
	query := strings.TrimSpace(in.Query)
	contextLines := defaultReadFileSearchContext
	if in.ContextLines != nil {
		contextLines = *in.ContextLines
	}
	maxMatches := defaultReadFileSearchMatches
	if in.MaxMatches != nil {
		maxMatches = *in.MaxMatches
	}
	text := string(data)
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	if text == "" {
		lines = nil
	}

	var b strings.Builder
	matches := 0
	lastWritten := -1
	truncated := false
	for i, line := range lines {
		if !strings.Contains(line, query) {
			continue
		}
		if matches >= maxMatches {
			truncated = true
			break
		}
		start := i - contextLines
		if start < 0 {
			start = 0
		}
		end := i + contextLines + 1
		if end > len(lines) {
			end = len(lines)
		}
		if start <= lastWritten {
			start = lastWritten + 1
		}
		if lastWritten >= 0 && start > lastWritten+1 {
			b.WriteString("...\n")
		}
		for j := start; j < end; j++ {
			fmt.Fprintf(&b, "%d: %s\n", j+1, lines[j])
			lastWritten = j
		}
		matches++
	}
	if truncated {
		fmt.Fprintf(&b, "[read_file: search stopped after %d matches; narrow query or increase max_matches]\n", maxMatches)
	}
	metadata := readFileMetadata(path, int64(len(data)))
	metadata["mode"] = "search"
	metadata["query"] = query
	metadata["matches"] = matches
	metadata["context_lines"] = contextLines
	metadata["truncated"] = truncated
	if b.Len() == 0 {
		b.WriteString("No matches found.")
	}
	return ToolResult{Output: strings.TrimSuffix(b.String(), "\n"), Metadata: metadata}
}

func readFileMetadata(path string, size int64) map[string]any {
	return map[string]any{
		"path":      path,
		"file_size": size,
	}
}

func safeUTF8Slice(data []byte, start, end int64) string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if start > int64(len(data)) {
		start = int64(len(data))
	}
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	for start < end && !utf8.RuneStart(data[start]) {
		start++
	}
	out := string(data[start:end])
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out
}
