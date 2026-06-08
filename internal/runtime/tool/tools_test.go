package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	RegisterCoreTools(reg, "", nil)

	names := reg.Names()
	assert.Len(t, names, 8)

	for _, name := range []string{"read_file", "write_file", "edit_file", "bash", "python", "web_fetch", "web_search", "browser"} {
		tool, ok := reg.Get(name)
		assert.True(t, ok, "tool %q should exist", name)
		assert.Equal(t, name, tool.Name())
		assert.NotEmpty(t, tool.Description())
		assert.NotEmpty(t, tool.Parameters())
	}
}

func TestToolDefs(t *testing.T) {
	reg := NewRegistry()
	RegisterCoreTools(reg, "", nil)
	defs := reg.ToolDefs()
	assert.Len(t, defs, 8)
}

func TestReadFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := &ReadFileTool{}
	input, _ := json.Marshal(readFileInput{Path: path})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "hello world", result.Output)
	assert.Empty(t, result.Error)
}

func TestReadFileToolMissing(t *testing.T) {
	tool := &ReadFileTool{}
	input, _ := json.Marshal(readFileInput{Path: "/nonexistent/file"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Error)
}

func TestReadFileToolResolvesRelativePathAgainstWorkspace(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("workspace note"), 0o644))

	tool := &ReadFileTool{WorkDir: dir}
	input, _ := json.Marshal(readFileInput{Path: "notes.txt"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "workspace note", result.Output)
	assert.Empty(t, result.Error)
}

func TestReadFileToolAllowsParentTraversalOutsideWorkspace(t *testing.T) {
	projectRoot := t.TempDir()
	workspace := filepath.Join(projectRoot, "agents", "reader")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "notes.txt"), []byte("project note"), 0o644))

	tool := &ReadFileTool{WorkDir: workspace}
	input, _ := json.Marshal(readFileInput{Path: "../../notes.txt"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "project note", result.Output)
	assert.Empty(t, result.Error)
}

func TestWriteFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "output.txt")

	tool := &WriteFileTool{}
	input, _ := json.Marshal(writeFileInput{Path: path, Content: "test content"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)
	assert.Contains(t, result.Output, "Successfully wrote")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "test content", string(data))
}

func TestWriteFileToolParametersAreStrictOpenAICompatible(t *testing.T) {
	// Strict OpenAI-compatible gateways (jiekouai/highwayapi etc.) reject any
	// function-call schema with oneOf/anyOf/allOf/enum/not at the top level —
	// observed in harness-coding session h5-online-coding-localhost-4321-r1
	// where write_file's prior anyOf produced 5/5 retries with HTTP 400. Keep
	// the schema flat so all upstream providers accept it; runtime validation
	// in normalizeWriteFileMode handles mode-specific required-field checks.
	var schema map[string]any
	require.NoError(t, json.Unmarshal((&WriteFileTool{}).Parameters(), &schema))
	assert.Equal(t, "object", schema["type"])
	for _, key := range []string{"oneOf", "anyOf", "allOf", "not", "enum"} {
		_, present := schema[key]
		assert.False(t, present, "write_file schema must not carry %q at the top level (rejected by strict OpenAI gateways)", key)
	}
}

func TestWriteFileToolResolvesRelativePathAgainstWorkspace(t *testing.T) {
	dir := t.TempDir()

	tool := &WriteFileTool{WorkDir: dir}
	input, _ := json.Marshal(writeFileInput{Path: "subdir/output.txt", Content: "workspace content"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(filepath.Join(dir, "subdir", "output.txt"))
	require.NoError(t, err)
	assert.Equal(t, "workspace content", string(data))
}

func TestWriteFileToolAllowsParentTraversalOutsideWorkspace(t *testing.T) {
	projectRoot := t.TempDir()
	workspace := filepath.Join(projectRoot, "agents", "writer")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	tool := &WriteFileTool{WorkDir: workspace}
	input, _ := json.Marshal(writeFileInput{Path: "../../output.txt", Content: "project content"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(filepath.Join(projectRoot, "output.txt"))
	require.NoError(t, err)
	assert.Equal(t, "project content", string(data))
}

func TestWriteFileToolAppendsWithExpectedOffset(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteFileTool{WorkDir: dir}

	input, _ := json.Marshal(writeFileInput{Path: "large.txt", Content: "hello", Mode: "overwrite"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	offset := int64(5)
	input, _ = json.Marshal(writeFileInput{Path: "large.txt", Content: " world", Mode: "append", ExpectedOffset: &offset})
	result, err = tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(filepath.Join(dir, "large.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
}

func TestWriteFileToolRejectsAppendWhenExpectedOffsetMismatches(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "large.txt"), []byte("hello"), 0o644))
	tool := &WriteFileTool{WorkDir: dir}

	offset := int64(4)
	input, _ := json.Marshal(writeFileInput{Path: "large.txt", Content: " world", Mode: "append", ExpectedOffset: &offset})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "expected file size")

	data, err := os.ReadFile(filepath.Join(dir, "large.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestWriteFileToolAppliesCodexStylePatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(target, []byte("alpha\nbeta\ngamma\n"), 0o644))
	tool := &WriteFileTool{WorkDir: dir}

	patch := `*** Begin Patch
*** Update File: notes.txt
@@
 alpha
-beta
+bravo
 gamma
*** End Patch`
	input, _ := json.Marshal(writeFileInput{Patch: patch})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "alpha\nbravo\ngamma\n", string(data))
}

func TestWriteFileToolPatchCanAddAndDeleteFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.txt"), []byte("remove me\n"), 0o644))
	tool := &WriteFileTool{WorkDir: dir}

	patch := `*** Begin Patch
*** Add File: new.txt
+created
+file
*** Delete File: old.txt
*** End Patch`
	input, _ := json.Marshal(writeFileInput{Patch: patch})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, "created\nfile\n", string(data))
	_, err = os.Stat(filepath.Join(dir, "old.txt"))
	assert.True(t, os.IsNotExist(err))
}

func TestEditFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := &EditFileTool{}
	input, _ := json.Marshal(editFileInput{
		Path:      path,
		OldString: "world",
		NewString: "Go",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello Go", string(data))
}

func TestEditFileToolNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := &EditFileTool{}
	input, _ := json.Marshal(editFileInput{
		Path:      path,
		OldString: "missing",
		NewString: "replacement",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "not found")
}

func TestEditFileToolResolvesRelativePathAgainstWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0o644))

	tool := &EditFileTool{WorkDir: dir}
	input, _ := json.Marshal(editFileInput{
		Path:      "edit.txt",
		OldString: "world",
		NewString: "workspace",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello workspace", string(data))
}

func TestEditFileToolAllowsParentTraversalOutsideWorkspace(t *testing.T) {
	projectRoot := t.TempDir()
	workspace := filepath.Join(projectRoot, "agents", "editor")
	target := filepath.Join(projectRoot, "edit.txt")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(target, []byte("hello project"), 0o644))

	tool := &EditFileTool{WorkDir: workspace}
	input, _ := json.Marshal(editFileInput{
		Path:      "../../edit.txt",
		OldString: "project",
		NewString: "scope",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "hello scope", string(data))
}

func TestBashTool(t *testing.T) {
	tool := &BashTool{}
	input, _ := json.Marshal(BashProcessInput{Command: "echo hello"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "hello\n", result.Output)
	assert.Empty(t, result.Error)
}

func TestBashToolDescriptionMentionsStructuredAutomation(t *testing.T) {
	tool := &BashTool{}
	desc := tool.Description()
	assert.Contains(t, desc, "directory listing")
	assert.Contains(t, desc, "short script")
	assert.Contains(t, desc, "python3")
}

func TestPythonToolExecutesInlineScript(t *testing.T) {
	if _, err := resolvePythonBinary("", nil); err != nil {
		t.Skip("python interpreter not available")
	}
	tool := &PythonTool{}
	input, _ := json.Marshal(PythonProcessInput{Script: "print('hello from python')"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "hello from python", strings.TrimSpace(result.Output))
	assert.Empty(t, result.Error)
}

func TestPythonToolRespectsExecPolicy(t *testing.T) {
	tool := &PythonTool{
		ExecPolicy: &ExecPolicy{
			Level:     "allowlist",
			Allowlist: []string{"ls"},
		},
	}
	input, _ := json.Marshal(PythonProcessInput{Script: "print('blocked')"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Output)
	assert.Contains(t, result.Error, "allowlist")
}

func TestBashToolError(t *testing.T) {
	tool := &BashTool{}
	input, _ := json.Marshal(BashProcessInput{Command: "exit 1"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Error)
}

func TestBashToolAllowsWorkspaceRelativePaths(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "hello.txt"), []byte("hello workspace"), 0o644))

	tool := &BashTool{WorkDir: dir}
	input, _ := json.Marshal(BashProcessInput{Command: "cat nested/hello.txt"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "hello workspace", strings.TrimSpace(result.Output))
	assert.Empty(t, result.Error)
}

func TestBashToolAllowsParentTraversalOutsideWorkspace(t *testing.T) {
	projectRoot := t.TempDir()
	workspace := filepath.Join(projectRoot, "agents", "runner")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "hello.txt"), []byte("hello project"), 0o644))

	tool := &BashTool{WorkDir: workspace}
	input, _ := json.Marshal(BashProcessInput{Command: "cd ../.. && cat hello.txt"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "hello project", strings.TrimSpace(result.Output))
	assert.Empty(t, result.Error)
}

func TestBashToolAllowsAbsolutePathsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agents", "runner")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	target := filepath.Join(root, "shared.txt")
	require.NoError(t, os.WriteFile(target, []byte("shared"), 0o644))

	tool := &BashTool{WorkDir: workspace}
	input, _ := json.Marshal(BashProcessInput{Command: "cat " + target})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "shared", strings.TrimSpace(result.Output))
	assert.Empty(t, result.Error)
}

func TestBuildAgentRegistryResolvesRelativePathsFromWorkspace(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("shared"), 0o644))

	cfg := config.DefaultConfig()
	agentCfg := &config.AgentConfig{
		ID:        "coder",
		Name:      "Coder",
		Workspace: workspace,
	}

	reg := BuildAgentRegistry(cfg, agentCfg, nil, nil, nil)
	input, _ := json.Marshal(readFileInput{Path: "shared.txt"})

	result, err := reg.Execute(context.Background(), "read_file", input)
	require.NoError(t, err)
	assert.Equal(t, "shared", result.Output)
	assert.Empty(t, result.Error)
}

func TestReadFileDefaultsLargeTextToPagedPreview(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Repeat("x", defaultReadFileMaxBytes+128)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(content), 0o644))

	tool := &ReadFileTool{WorkDir: workspace}
	input, _ := json.Marshal(readFileInput{Path: "large.txt"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)
	assert.Contains(t, result.Output, "use offset=")
	assert.Equal(t, "bytes", result.Metadata["mode"])
	assert.Equal(t, true, result.Metadata["truncated"])
	assert.Equal(t, int64(defaultReadFileMaxBytes), result.Metadata["next_offset"])
}

func TestReadFileReadsByteRange(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("0123456789"), 0o644))

	tool := &ReadFileTool{WorkDir: workspace}
	offset := int64(2)
	limit := int64(4)
	input, _ := json.Marshal(readFileInput{Path: "data.txt", Offset: &offset, Limit: &limit})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "2345\n[read_file: returned bytes 2-6 of 10; use offset=6 to continue]", result.Output)
	assert.Equal(t, int64(6), result.Metadata["next_offset"])
}

func TestReadFileReadsLineRange(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644))

	tool := &ReadFileTool{WorkDir: workspace}
	start := 2
	count := 2
	input, _ := json.Marshal(readFileInput{Path: "data.txt", LineStart: &start, LineCount: &count})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "2: beta\n3: gamma\n[read_file: returned lines 2-3 of 4; use line_start=4 to continue]", result.Output)
	assert.Equal(t, "lines", result.Metadata["mode"])
	assert.Equal(t, 4, result.Metadata["next_line_start"])
}

func TestReadFileSearchesWithContext(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("one\ntwo\nneedle\nfour\nfive\n"), 0o644))

	tool := &ReadFileTool{WorkDir: workspace}
	contextLines := 1
	input, _ := json.Marshal(readFileInput{Path: "data.txt", Query: "needle", ContextLines: &contextLines})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "2: two\n3: needle\n4: four", result.Output)
	assert.Equal(t, "search", result.Metadata["mode"])
	assert.Equal(t, 1, result.Metadata["matches"])
}
