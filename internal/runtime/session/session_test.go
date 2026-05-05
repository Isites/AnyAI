package session

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionAppendAndHistory(t *testing.T) {
	sess := NewSession("default", "test")

	sess.Append(UserMessageEntry("hello"))
	sess.Append(AssistantMessageEntry("hi there"))
	sess.Append(UserMessageEntry("how are you?"))

	history := sess.History()
	assert.Len(t, history, 3)

	assert.Equal(t, EntryTypeMessage, history[0].Type)
	assert.Equal(t, "user", history[0].Role)
	assert.Equal(t, "assistant", history[1].Role)
	assert.Equal(t, "user", history[2].Role)
}

func TestSessionDAGTraversal(t *testing.T) {
	sess := NewSession("default", "test")

	sess.Append(UserMessageEntry("first"))
	sess.Append(AssistantMessageEntry("second"))

	history := sess.History()
	assert.Len(t, history, 2)

	// Parent chain should be connected
	assert.Empty(t, history[0].ParentID)
	assert.Equal(t, history[0].ID, history[1].ParentID)
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create and populate a session
	sess, err := store.Load("agent1", "test_peer")
	require.NoError(t, err)

	sess.Append(UserMessageEntry("hello"))
	sess.Append(AssistantMessageEntry("world"))

	// Reload from disk
	sess2, err := store.Load("agent1", "test_peer")
	require.NoError(t, err)

	history := sess2.History()
	assert.Len(t, history, 2)

	// Check file exists
	path := filepath.Join(dir, "agent1", "test_peer.jsonl")
	assert.FileExists(t, path)
}

func TestToolCallEntries(t *testing.T) {
	sess := NewSession("default", "test")

	sess.Append(UserMessageEntry("run ls"))
	sess.Append(ToolCallEntry("tc_1", "bash", []byte(`{"command":"ls"}`)))
	sess.Append(ToolResultEntry("tc_1", "file1\nfile2", "", nil))
	sess.Append(AssistantMessageEntry("Here are the files."))

	history := sess.History()
	assert.Len(t, history, 4)
	assert.Equal(t, EntryTypeToolCall, history[1].Type)
	assert.Equal(t, EntryTypeToolResult, history[2].Type)
}

func TestToolResultEntryWithMetadataPersistsAndSerializes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess, err := store.Load("agent1", "tool-metadata")
	require.NoError(t, err)

	sess.Append(ToolResultEntryWithMetadata("tc_1", "", "failed to read file: read .: is a directory", map[string]any{
		"error_class":          "path_is_directory",
		"auto_retryable":       false,
		"model_recoverable":    true,
		"suggested_next_moves": []string{"Use bash to inspect the directory first."},
	}, nil))

	reloaded, err := store.Load("agent1", "tool-metadata")
	require.NoError(t, err)

	history := reloaded.History()
	require.Len(t, history, 1)

	var data ToolResultData
	require.NoError(t, json.Unmarshal(history[0].Data, &data))
	assert.Equal(t, "path_is_directory", data.Metadata["error_class"])
	assert.Equal(t, false, data.Metadata["auto_retryable"])
	assert.Equal(t, true, data.Metadata["model_recoverable"])

	view := SerializeHistory(reloaded)
	require.Len(t, view, 1)
	metadata, ok := view[0]["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "path_is_directory", metadata["error_class"])
	assert.Equal(t, false, metadata["auto_retryable"])
	assert.Equal(t, true, metadata["model_recoverable"])
}

func TestPendingToolCallsReturnsOnlyUnpairedCalls(t *testing.T) {
	history := []SessionEntry{
		UserMessageEntry("inspect repo"),
		ToolCallEntry("tc_1", "bash", []byte(`{"command":"pwd"}`)),
		ToolResultEntry("tc_1", "/repo", "", nil),
		ToolCallEntry("tc_2", "read_file", []byte(`{"path":"README.md"}`)),
		ToolCallEntry("tc_3", "bash", []byte(`{"command":"ls"}`)),
		ToolResultEntry("tc_3", "file1\nfile2", "", nil),
	}

	pending := PendingToolCalls(history)
	require.Len(t, pending, 1)
	assert.Equal(t, "tc_2", pending[0].ID)
	assert.Equal(t, "read_file", pending[0].Tool)
}

func TestSessionBranch(t *testing.T) {
	sess := NewSession("default", "test")

	sess.Append(UserMessageEntry("first"))
	firstID := sess.LeafID()
	sess.Append(AssistantMessageEntry("response 1"))
	sess.Append(UserMessageEntry("second"))

	// Branch back to first entry
	err := sess.Branch(firstID)
	require.NoError(t, err)

	assert.Equal(t, firstID, sess.LeafID())

	// Append on the branch
	sess.Append(AssistantMessageEntry("alternate response"))

	// History should follow the branch
	history := sess.History()
	assert.Len(t, history, 2) // first + alternate response
	assert.Equal(t, "user", history[0].Role)
	assert.Equal(t, "assistant", history[1].Role)
}

func TestSessionBranchInvalidID(t *testing.T) {
	sess := NewSession("default", "test")
	sess.Append(UserMessageEntry("hello"))

	err := sess.Branch("nonexistent")
	assert.Error(t, err)
}

func TestSessionCompact(t *testing.T) {
	sess := NewSession("default", "test")

	// Add 10 exchanges
	for i := 0; i < 10; i++ {
		sess.Append(UserMessageEntry("question " + string(rune('0'+i))))
		sess.Append(AssistantMessageEntry("answer " + string(rune('0'+i))))
	}

	history := sess.History()
	assert.Len(t, history, 20)

	// Compact, keeping last 4 entries
	sess.Compact("Summary of conversation: discussed topics 0-7", 4)

	history = sess.History()
	// Should have: 1 summary + 4 kept entries = 5
	assert.Len(t, history, 5)

	// First entry should be the compaction summary entry
	assert.Equal(t, EntryTypeCompaction, history[0].Type)
	assert.Equal(t, "user", history[0].Role)
	var compact CompactionData
	require.NoError(t, json.Unmarshal(history[0].Data, &compact))
	assert.Contains(t, compact.Text, "discussed topics 0-7")
	assert.Equal(t, "legacy_entry_count", compact.Trigger)
	assert.True(t, compact.LegacyHeuristic)
}

func TestSessionCompactNoOp(t *testing.T) {
	sess := NewSession("default", "test")
	sess.Append(UserMessageEntry("hello"))
	sess.Append(AssistantMessageEntry("world"))

	// Compacting with keepEntries >= history length should be a no-op
	sess.Compact("summary", 10)

	history := sess.History()
	assert.Len(t, history, 2)
}

func TestSessionCompactWithStore(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess, err := store.Load("agent1", "compact_test")
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		sess.Append(UserMessageEntry("msg " + string(rune('0'+i))))
		sess.Append(AssistantMessageEntry("reply " + string(rune('0'+i))))
	}

	sess.Compact("Summary of conversation", 4)

	// Reload and verify
	sess2, err := store.Load("agent1", "compact_test")
	require.NoError(t, err)

	history := sess2.History()
	assert.Len(t, history, 5) // 1 summary + 4 kept
	assert.Equal(t, EntryTypeCompaction, history[0].Type)
}

func TestSessionCompactPreservesLatestPlanAndTodoState(t *testing.T) {
	sess := NewSession("default", "test")

	for i := 0; i < 6; i++ {
		sess.Append(UserMessageEntry("question " + string(rune('0'+i))))
		sess.Append(AssistantMessageEntry("answer " + string(rune('0'+i))))
	}
	sess.Append(PlanEntry("1. Keep summaries\n2. Preserve todos"))
	sess.Append(TodoEntry(TodoData{
		ID:        "todo_1",
		Content:   "Write regression test",
		Status:    "open",
		CreatedAt: 1,
	}))
	sess.Append(TodoEntry(TodoData{
		ID:          "todo_1",
		Content:     "Write regression test",
		Status:      "completed",
		CreatedAt:   1,
		CompletedAt: 2,
	}))

	sess.Compact("Summary of conversation", 4)

	history := sess.History()
	require.NotEmpty(t, history)
	assert.Equal(t, EntryTypeCompaction, history[0].Type)

	var planCount int
	var latestTodo TodoData
	for _, entry := range sess.Entries() {
		switch entry.Type {
		case EntryTypePlan:
			planCount++
		case EntryTypeTodo:
			require.NoError(t, json.Unmarshal(entry.Data, &latestTodo))
		}
	}

	assert.Equal(t, 1, planCount)
	assert.Equal(t, "todo_1", latestTodo.ID)
	assert.Equal(t, "completed", latestTodo.Status)
}

func TestBuildRollingSummary(t *testing.T) {
	history := []SessionEntry{
		MetaEntry("Earlier summary"),
		UserMessageEntry("Need a rollout plan for Apollo."),
		AssistantMessageEntry("We should validate staging first."),
		ToolCallEntry("tc_1", "memory_search", []byte(`{"query":"apollo"}`)),
		PlanEntry("1. Inspect memory\n2. Draft plan"),
		TodoEntry(TodoData{ID: "todo_1", Content: "Write regression test", Status: "open", CreatedAt: 1}),
	}

	summary := BuildRollingSummary(history)
	assert.Contains(t, summary, "Earlier summary")
	assert.Contains(t, summary, "Need a rollout plan for Apollo.")
	assert.Contains(t, summary, "validate staging first")
	assert.Contains(t, summary, "memory_search")
	assert.Contains(t, summary, "Write regression test")
}

func TestEstimateTokens(t *testing.T) {
	sess := NewSession("default", "test")
	sess.Append(UserMessageEntry("Hello, how are you doing today?"))
	sess.Append(AssistantMessageEntry("I'm doing well, thank you for asking!"))

	tokens := sess.EstimateTokens()
	assert.Greater(t, tokens, 0)
}
