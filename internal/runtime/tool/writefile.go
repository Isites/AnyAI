package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteFileTool creates, appends to, or patches files.
type WriteFileTool struct {
	WorkDir string // base directory for resolving relative paths
}

type writeFileInput struct {
	Path           string `json:"path,omitempty"`
	Content        string `json:"content,omitempty"`
	Mode           string `json:"mode,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Patch          string `json:"patch,omitempty"`
	OldString      string `json:"old_string,omitempty"`
	NewString      string `json:"new_string,omitempty"`
	ExpectedOffset *int64 `json:"expected_offset,omitempty"`
}

func (t *WriteFileTool) Name() string { return "write_file" }

func (t *WriteFileTool) Description() string {
	return "Create, overwrite, append to, or patch files. Use patch mode for existing-file edits and append mode with expected_offset for large files written in chunks."
}

func (t *WriteFileTool) ToolMetadata() ToolMetadata {
	return workspaceWriteToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *WriteFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The absolute or relative path to the file. Required for overwrite, append, and replace modes. Optional for patch mode when the patch contains file paths."
			},
			"content": {
				"type": "string",
				"description": "Content to write or append. For large files, send manageable chunks with mode=overwrite for the first chunk and mode=append for later chunks."
			},
			"mode": {
				"type": "string",
				"enum": ["overwrite", "append", "patch", "replace"],
				"description": "Write mode. Defaults to patch when patch is provided, replace when old_string/new_string are provided, otherwise overwrite."
			},
			"patch": {
				"type": "string",
				"description": "Codex-style patch text bounded by *** Begin Patch and *** End Patch. Supports Add File, Delete File, and Update File hunks."
			},
			"old_string": {
				"type": "string",
				"description": "Exact text to replace in replace mode. Must occur exactly once."
			},
			"new_string": {
				"type": "string",
				"description": "Replacement text for replace mode."
			},
			"expected_offset": {
				"type": "integer",
				"description": "Optional byte size the target file must have before writing. Use with chunked large-file writes to catch missing or partial chunks."
			}
		},
		"anyOf": [
			{"required": ["path", "content"]},
			{"required": ["patch"]},
			{"required": ["path", "old_string", "new_string"]}
		]
	}`)
}

func (t *WriteFileTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in writeFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	mode := normalizeWriteFileMode(in)
	switch mode {
	case "overwrite", "append":
		return t.writeContent(in, mode)
	case "replace":
		return t.replaceContent(in)
	case "patch":
		return t.applyPatch(in)
	default:
		return ToolResult{Error: fmt.Sprintf("unsupported write_file mode %q", mode)}, nil
	}
}

func normalizeWriteFileMode(in writeFileInput) string {
	mode := strings.ToLower(strings.TrimSpace(firstNonEmptyWriteFileValue(in.Mode, in.Operation)))
	switch mode {
	case "":
		switch {
		case strings.TrimSpace(in.Patch) != "":
			return "patch"
		case in.OldString != "" || in.NewString != "":
			return "replace"
		default:
			return "overwrite"
		}
	case "write", "create", "truncate":
		return "overwrite"
	case "apply_patch", "update":
		return "patch"
	default:
		return mode
	}
}

func (t *WriteFileTool) writeContent(in writeFileInput, mode string) (ToolResult, error) {
	if strings.TrimSpace(in.Path) == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	targetPath := resolvePathForBase(in.Path, t.WorkDir)
	if err := ensureExpectedOffset(targetPath, in.ExpectedOffset); err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}

	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if mode == "append" {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(targetPath, flags, 0o644)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	written, err := f.WriteString(in.Content)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}
	size := fileSize(targetPath)
	return ToolResult{Output: fmt.Sprintf("Successfully %s %d bytes to %s (file size %d bytes)", writeModeVerb(mode), written, targetPath, size)}, nil
}

func (t *WriteFileTool) replaceContent(in writeFileInput) (ToolResult, error) {
	if strings.TrimSpace(in.Path) == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	if in.OldString == "" {
		return ToolResult{Error: "old_string is required for replace mode"}, nil
	}
	targetPath := resolvePathForBase(in.Path, t.WorkDir)
	data, err := os.ReadFile(targetPath)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}
	content := string(data)
	count := strings.Count(content, in.OldString)
	if count == 0 {
		return ToolResult{Error: "old_string not found in file"}, nil
	}
	if count > 1 {
		return ToolResult{Error: fmt.Sprintf("old_string found %d times in file, must be unique", count)}, nil
	}
	newContent := strings.Replace(content, in.OldString, in.NewString, 1)
	if err := os.WriteFile(targetPath, []byte(newContent), filePerm(targetPath)); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}
	return ToolResult{Output: fmt.Sprintf("Successfully replaced text in %s", targetPath)}, nil
}

func (t *WriteFileTool) applyPatch(in writeFileInput) (ToolResult, error) {
	ops, err := parseCodexPatch(in.Patch)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	if len(ops) == 0 {
		return ToolResult{Error: "patch contains no file changes"}, nil
	}
	summary, err := applyCodexPatch(ops, t.WorkDir)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return ToolResult{Output: summary}, nil
}

func writeModeVerb(mode string) string {
	if mode == "append" {
		return "appended"
	}
	return "wrote"
}

func ensureExpectedOffset(path string, expected *int64) error {
	if expected == nil {
		return nil
	}
	size := int64(0)
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to stat file: %v", err)
		}
	} else if info.IsDir() {
		return fmt.Errorf("path is a directory: %s", path)
	} else {
		size = info.Size()
	}
	if size != *expected {
		return fmt.Errorf("expected file size %d bytes before write, got %d bytes", *expected, size)
	}
	return nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

func filePerm(path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0o644
	}
	return info.Mode().Perm()
}

type codexPatchOpKind string

const (
	codexPatchAdd    codexPatchOpKind = "add"
	codexPatchDelete codexPatchOpKind = "delete"
	codexPatchUpdate codexPatchOpKind = "update"
)

type codexPatchOp struct {
	kind    codexPatchOpKind
	path    string
	moveTo  string
	content []string
	changes []codexPatchChange
}

type codexPatchChange struct {
	oldLines []string
	newLines []string
}

func parseCodexPatch(raw string) ([]codexPatchOp, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	start := firstNonEmptyLine(lines)
	end := lastNonEmptyLine(lines)
	if start < 0 || strings.TrimSpace(lines[start]) != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}
	if end < 0 || strings.TrimSpace(lines[end]) != "*** End Patch" {
		return nil, fmt.Errorf("patch must end with *** End Patch")
	}

	var ops []codexPatchOp
	for i := start + 1; i < end; {
		line := lines[i]
		switch {
		case strings.TrimSpace(line) == "":
			i++
		case strings.HasPrefix(line, "*** Add File: "):
			op, next, err := parseCodexAdd(lines, i, end)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			if path == "" {
				return nil, fmt.Errorf("delete file patch is missing a path")
			}
			ops = append(ops, codexPatchOp{kind: codexPatchDelete, path: path})
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			op, next, err := parseCodexUpdate(lines, i, end)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		default:
			return nil, fmt.Errorf("unsupported patch line %q", line)
		}
	}
	return ops, nil
}

func parseCodexAdd(lines []string, i, end int) (codexPatchOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Add File: "))
	if path == "" {
		return codexPatchOp{}, i, fmt.Errorf("add file patch is missing a path")
	}
	op := codexPatchOp{kind: codexPatchAdd, path: path}
	i++
	for i < end && !isCodexPatchHeader(lines[i]) {
		line := lines[i]
		if !strings.HasPrefix(line, "+") {
			return codexPatchOp{}, i, fmt.Errorf("add file line must start with +: %q", line)
		}
		op.content = append(op.content, strings.TrimPrefix(line, "+"))
		i++
	}
	return op, i, nil
}

func parseCodexUpdate(lines []string, i, end int) (codexPatchOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Update File: "))
	if path == "" {
		return codexPatchOp{}, i, fmt.Errorf("update file patch is missing a path")
	}
	op := codexPatchOp{kind: codexPatchUpdate, path: path}
	i++
	var current codexPatchChange
	changed := false
	flush := func() {
		if !changed {
			current = codexPatchChange{}
			return
		}
		op.changes = append(op.changes, current)
		current = codexPatchChange{}
		changed = false
	}

	for i < end && !isCodexPatchHeader(lines[i]) {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Move to: "):
			if len(op.changes) > 0 || changed {
				return codexPatchOp{}, i, fmt.Errorf("move target must appear before update hunks")
			}
			op.moveTo = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to: "))
			if op.moveTo == "" {
				return codexPatchOp{}, i, fmt.Errorf("move target is empty")
			}
		case strings.HasPrefix(line, "@@"):
			flush()
		case line == "*** End of File":
			flush()
		case strings.HasPrefix(line, " "):
			text := strings.TrimPrefix(line, " ")
			current.oldLines = append(current.oldLines, text)
			current.newLines = append(current.newLines, text)
		case strings.HasPrefix(line, "-"):
			current.oldLines = append(current.oldLines, strings.TrimPrefix(line, "-"))
			changed = true
		case strings.HasPrefix(line, "+"):
			current.newLines = append(current.newLines, strings.TrimPrefix(line, "+"))
			changed = true
		default:
			return codexPatchOp{}, i, fmt.Errorf("update hunk line must start with space, +, -, or @@: %q", line)
		}
		i++
	}
	flush()
	if op.moveTo == "" && len(op.changes) == 0 {
		return codexPatchOp{}, i, fmt.Errorf("update patch for %s contains no changes", path)
	}
	return op, i, nil
}

func isCodexPatchHeader(line string) bool {
	return strings.HasPrefix(line, "*** Add File: ") ||
		strings.HasPrefix(line, "*** Delete File: ") ||
		strings.HasPrefix(line, "*** Update File: ")
}

func applyCodexPatch(ops []codexPatchOp, workDir string) (string, error) {
	changed := make([]string, 0, len(ops))
	for _, op := range ops {
		path := resolvePathForBase(op.path, workDir)
		switch op.kind {
		case codexPatchAdd:
			if _, err := os.Stat(path); err == nil {
				return "", fmt.Errorf("cannot add file that already exists: %s", path)
			} else if err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to stat file: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", fmt.Errorf("failed to create directory: %v", err)
			}
			if err := os.WriteFile(path, []byte(linesToFileText(op.content)), 0o644); err != nil {
				return "", fmt.Errorf("failed to write file: %v", err)
			}
			changed = append(changed, path)
		case codexPatchDelete:
			if err := os.Remove(path); err != nil {
				return "", fmt.Errorf("failed to delete file: %v", err)
			}
			changed = append(changed, path)
		case codexPatchUpdate:
			updatedPath, err := applyCodexUpdate(op, path, workDir)
			if err != nil {
				return "", err
			}
			changed = append(changed, updatedPath)
		}
	}
	return fmt.Sprintf("Successfully applied patch to %d file(s): %s", len(changed), strings.Join(changed, ", ")), nil
}

func applyCodexUpdate(op codexPatchOp, path, workDir string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %v", err)
	}
	content := string(data)
	for _, change := range op.changes {
		var ok bool
		content, ok, err = replaceUniquePatchChunk(content, change)
		if err != nil {
			return "", fmt.Errorf("failed to apply patch to %s: %v", path, err)
		}
		if !ok {
			return "", fmt.Errorf("failed to apply patch to %s: old hunk not found", path)
		}
	}

	targetPath := path
	if op.moveTo != "" {
		targetPath = resolvePathForBase(op.moveTo, workDir)
		if _, err := os.Stat(targetPath); err == nil {
			return "", fmt.Errorf("move target already exists: %s", targetPath)
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to stat move target: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte(content), filePerm(path)); err != nil {
		return "", fmt.Errorf("failed to write file: %v", err)
	}
	if targetPath != path {
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("failed to remove moved file: %v", err)
		}
	}
	return targetPath, nil
}

func replaceUniquePatchChunk(content string, change codexPatchChange) (string, bool, error) {
	if len(change.oldLines) == 0 {
		return "", false, fmt.Errorf("update hunk must include context or deleted lines")
	}
	oldText := linesToFileText(change.oldLines)
	newText := linesToFileText(change.newLines)
	if oldText == newText {
		return content, true, nil
	}
	if strings.Count(content, oldText) == 1 {
		return strings.Replace(content, oldText, newText, 1), true, nil
	}
	if count := strings.Count(content, oldText); count > 1 {
		return "", false, fmt.Errorf("old hunk found %d times, must be unique", count)
	}

	oldNoFinalNewline := strings.TrimSuffix(oldText, "\n")
	newNoFinalNewline := strings.TrimSuffix(newText, "\n")
	if oldNoFinalNewline != oldText && strings.Count(content, oldNoFinalNewline) == 1 {
		return strings.Replace(content, oldNoFinalNewline, newNoFinalNewline, 1), true, nil
	}
	if count := strings.Count(content, oldNoFinalNewline); oldNoFinalNewline != oldText && count > 1 {
		return "", false, fmt.Errorf("old hunk found %d times, must be unique", count)
	}
	return content, false, nil
}

func linesToFileText(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func firstNonEmptyLine(lines []string) int {
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			return i
		}
	}
	return -1
}

func lastNonEmptyLine(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return -1
}

func summarizePatchPaths(patch string) []string {
	patch = strings.ReplaceAll(patch, "\r\n", "\n")
	patch = strings.ReplaceAll(patch, "\r", "\n")
	lines := strings.Split(patch, "\n")
	var paths []string
	seen := map[string]struct{}{}
	for _, line := range lines {
		for _, prefix := range []string{"*** Add File: ", "*** Delete File: ", "*** Update File: ", "*** Move to: "} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			path := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	return paths
}

func firstNonEmptyWriteFileValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
