package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	runtimesessionstate "github.com/Isites/anyai/internal/runtime/session/state"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	runtimetaskbuiltin "github.com/Isites/anyai/internal/runtime/task/builtin"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLLMProvider returns canned ChatEvent streams for testing.
type mockLLMProvider struct {
	events []llm.ChatEvent

	autoGoalFinalize bool
	autoGoalSeq      int
	autoGoalComplete bool
}

func (m *mockLLMProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if m.autoGoalComplete {
		if events, ok := autoGoalCompletionResponse(req, m.events, &m.autoGoalFinalize, &m.autoGoalSeq); ok {
			ch := make(chan llm.ChatEvent, len(events))
			for _, e := range events {
				ch <- e
			}
			close(ch)
			return ch, nil
		}
	}
	ch := make(chan llm.ChatEvent, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (m *mockLLMProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock", Name: "Mock", Provider: "mock"}}
}

func (m *mockLLMProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "mock compact summary"}, nil
}

type capturingLLMProvider struct {
	events []llm.ChatEvent

	compactSummary string
	compactResults []llm.CompactResponse
	compactErrors  []error

	mu               sync.Mutex
	requests         []llm.ChatRequest
	compactRequests  []llm.CompactRequest
	autoGoalFinalize bool
	autoGoalSeq      int
	autoGoalComplete bool
}

func (m *capturingLLMProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if m.autoGoalComplete {
		if events, ok := autoGoalCompletionResponse(req, m.events, &m.autoGoalFinalize, &m.autoGoalSeq); ok {
			ch := make(chan llm.ChatEvent, len(events))
			for _, e := range events {
				ch <- e
			}
			close(ch)
			return ch, nil
		}
	}

	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()

	ch := make(chan llm.ChatEvent, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (m *capturingLLMProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock", Name: "Mock", Provider: "mock"}}
}

func (m *capturingLLMProvider) Capabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{
		Provider: "mock",
		Compact:  llm.CompactCapabilityChatFallbackOnly,
	}
}

func (m *capturingLLMProvider) Compact(ctx context.Context, req llm.CompactRequest) (llm.CompactResponse, error) {
	m.mu.Lock()
	m.compactRequests = append(m.compactRequests, req)
	attempt := len(m.compactRequests) - 1
	m.mu.Unlock()

	if attempt >= 0 && attempt < len(m.compactErrors) && m.compactErrors[attempt] != nil {
		return llm.CompactResponse{}, m.compactErrors[attempt]
	}
	if attempt >= 0 && attempt < len(m.compactResults) {
		return m.compactResults[attempt], nil
	}

	summary := strings.TrimSpace(m.compactSummary)
	if summary == "" {
		summary = "provider compact summary"
	}
	return llm.CompactResponse{
		Summary:  summary,
		Strategy: llm.CompactStrategyChatFallback,
	}, nil
}

func (m *capturingLLMProvider) Requests() []llm.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.ChatRequest(nil), m.requests...)
}

func (m *capturingLLMProvider) CompactRequests() []llm.CompactRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.CompactRequest(nil), m.compactRequests...)
}

type scriptedLLMOutcome struct {
	err    error
	events []llm.ChatEvent
}

type scriptedLLMProvider struct {
	outcomes []scriptedLLMOutcome

	mu               sync.Mutex
	idx              int
	requests         []llm.ChatRequest
	autoGoalFinalize bool
	autoGoalSeq      int
	autoGoalComplete bool
}

func (m *scriptedLLMProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if m.autoGoalComplete {
		if events, ok := autoGoalCompletionResponse(req, nil, &m.autoGoalFinalize, &m.autoGoalSeq); ok {
			ch := make(chan llm.ChatEvent, len(events))
			for _, e := range events {
				ch <- e
			}
			close(ch)
			return ch, nil
		}
	}

	m.mu.Lock()
	idx := m.idx
	if idx >= len(m.outcomes) {
		idx = len(m.outcomes) - 1
	}
	outcome := m.outcomes[idx]
	if m.autoGoalComplete {
		if events, ok := autoGoalCompletionResponse(req, outcome.events, &m.autoGoalFinalize, &m.autoGoalSeq); ok {
			m.mu.Unlock()
			return staticEventStream(events), nil
		}
	}
	m.requests = append(m.requests, req)
	m.idx++
	m.mu.Unlock()

	if outcome.err != nil {
		return nil, outcome.err
	}

	ch := make(chan llm.ChatEvent, len(outcome.events))
	for _, e := range outcome.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (m *scriptedLLMProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock", Name: "Mock", Provider: "mock"}}
}

func (m *scriptedLLMProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "scripted compact summary"}, nil
}

func (m *scriptedLLMProvider) Requests() []llm.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.ChatRequest(nil), m.requests...)
}

func sessionEntryRuntimeControlText(t *testing.T, entry session.SessionEntry) (string, bool) {
	t.Helper()
	switch entry.Type {
	case session.EntryTypeRuntimeControl:
		var data session.RuntimeControlData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		return data.Text, strings.TrimSpace(data.Text) != ""
	case session.EntryTypeMessage:
		if entry.Role != "user" {
			return "", false
		}
		var data session.MessageData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		if !session.IsRuntimeControlText(data.Text) {
			return "", false
		}
		return data.Text, true
	default:
		return "", false
	}
}

func sessionHasRuntimeControlText(t *testing.T, history []session.SessionEntry, substrings ...string) bool {
	t.Helper()
	for _, entry := range history {
		text, ok := sessionEntryRuntimeControlText(t, entry)
		if !ok {
			continue
		}
		matched := true
		for _, expected := range substrings {
			if !strings.Contains(text, expected) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func requestMessagesText(messages []llm.Message) string {
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		parts = append(parts, msg.Role+": "+msg.Content)
	}
	return strings.Join(parts, "\n")
}

// --- assembleSystemPrompt tests ---

func TestAssembleSystemPromptUsesAgentInstructionsOnly(t *testing.T) {
	dir := t.TempDir()
	agentInstructions := "You are a test assistant."
	err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("legacy identity content"), 0o644)
	require.NoError(t, err)

	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			Instructions: agentInstructions,
			ID:           "test",
			Name:         "Test Agent",
		},
		Execution: ExecutionSurface{
			Model: "mock-model",
		},
		Tools: ToolSurface{
			Names: []string{"read_file", "bash"},
		},
	})
	assert.NotContains(t, result, defaultIdentityBase)
	assert.Contains(t, result, "## Runtime Contract")
	assert.Contains(t, result, "## Tool Call Style")
	assert.Contains(t, result, "## Runtime Facts")
	assert.Contains(t, result, "- Current model: mock-model")
	assert.Contains(t, result, runtimeCapabilityNote)
	assert.Contains(t, result, agentInstructions)
	assert.NotContains(t, result, "legacy identity content")
	assert.Contains(t, result, "## Environment Facts")
	assert.Less(t, strings.Index(result, agentInstructions), strings.Index(result, "## Runtime Identity"))
}

func TestAssembleSystemPromptDefault(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "default",
			Name: "Assistant",
		},
		Execution: ExecutionSurface{
			Model: "mock-model",
		},
		Tools: ToolSurface{
			Names: []string{"read_file", "bash", "callagent"},
		},
		Collaboration: CollaborationSurface{
			TopologySummary: "Scoped topology.",
		},
	})
	assert.Contains(t, result, defaultIdentityBase)
	assert.Contains(t, result, "Think carefully and independently")
	assert.Contains(t, result, "Do not pretend to know things you have not actually established")
	assert.Contains(t, result, "Do not cut corners")
	assert.Contains(t, result, "## Runtime Identity")
	assert.Contains(t, result, "- Agent name: Assistant")
	assert.Contains(t, result, "- Agent id: default")
	assert.Contains(t, result, "## Runtime Contract")
	assert.Contains(t, result, "## Runtime Capabilities")
	assert.Contains(t, result, "## Tool Call Style")
	assert.Contains(t, result, "Available tools in this run:")
	assert.Contains(t, result, "`read_file`")
	assert.Contains(t, result, "`bash`")
	assert.NotContains(t, result, "`web_fetch`")
	assert.Contains(t, result, "## Environment Facts")
	assert.Contains(t, result, "## Collaboration Contract")
	assert.Contains(t, result, "## Runtime Facts")
	assert.Contains(t, result, "- Current model: mock-model")
	assert.Contains(t, result, "Scoped topology.")
	assert.Less(t, strings.Index(result, defaultIdentityBase), strings.Index(result, "## Runtime Identity"))
}

func TestAssembleSystemPromptConfigOverride(t *testing.T) {
	dir := t.TempDir()
	// Agent instructions should come only from the runtime/system prompt input.
	err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("identity file content"), 0o644)
	require.NoError(t, err)

	configPrompt := "You are a custom agent from config."
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			Instructions: configPrompt,
			ID:           "custom",
			Name:         "Custom Agent",
		},
		Tools: ToolSurface{
			Names: []string{"read_file"},
		},
	})
	assert.NotContains(t, result, defaultIdentityBase)
	assert.Contains(t, result, "## Runtime Contract")
	assert.Contains(t, result, runtimeCapabilityNote)
	assert.Contains(t, result, configPrompt)
	assert.NotContains(t, result, "identity file content")
}

func TestAssembleSystemPromptSelfIdentity(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "supervisor",
			Name: "Supervisor",
		},
	})
	assert.Contains(t, result, "## Runtime Identity")
	assert.Contains(t, result, "- Agent name: Supervisor")
	assert.Contains(t, result, "- Agent id: supervisor")
}

func TestBuildDefaultIdentityToolSpecific(t *testing.T) {
	result := buildCapabilitySection([]string{"read_file", "web_search", "web_fetch"})
	assert.Contains(t, result, "Available tools in this run:")
	assert.Contains(t, result, "`read_file`")
	assert.Contains(t, result, "`web_search`")
	assert.Contains(t, result, "`web_fetch`")
	assert.NotContains(t, result, "`bash`")
	assert.NotContains(t, result, "`send_message`")
}

func TestBuildDefaultIdentityCallAgentHintsNaturalLanguageWorkflow(t *testing.T) {
	result := buildCapabilitySection([]string{"callagent"})
	assert.Contains(t, result, "request clearly needs a specialist")
	assert.Contains(t, result, "parallel")
	assert.Contains(t, result, "target_agent")
	assert.Contains(t, result, "first assistant action should be the callagent tool call")
	assert.Contains(t, result, "write the final integrated user-facing answer")
}

func TestBuildDefaultIdentityWriteFileHint(t *testing.T) {
	result := buildCapabilitySection([]string{"write_file"})
	assert.Contains(t, result, "Codex-style")
	assert.Contains(t, result, "expected_offset")
}

func TestBuildCapabilitySectionGuidesFailureRecoveryAndPathHandling(t *testing.T) {
	contract := buildContractSection()
	result := buildCapabilitySection([]string{"read_file", "bash", "write_file"})
	assert.Contains(t, contract, "When a tool call fails")
	assert.Contains(t, contract, "relative paths resolve from your agent workspace")
	assert.Contains(t, contract, "Python script")
	assert.Contains(t, result, "not directory listings")
	assert.Contains(t, result, "If an image is already attached")
	assert.Contains(t, result, "inspect that image directly")
	assert.Contains(t, result, "manageable chunks")
}

func TestBuildToolCallStyleSectionGuidesDirectExecutionAndRecovery(t *testing.T) {
	result := buildToolCallStyleSection()
	assert.Contains(t, result, "## Tool Call Style")
	assert.Contains(t, result, "call it directly")
	assert.Contains(t, result, "After a tool failure")
	assert.Contains(t, result, "switch approach instead of looping")
}

func TestAssembleSystemPromptSkipsTopologyWithoutCallAgent(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "worker",
			Name: "Worker",
		},
		Tools: ToolSurface{
			Names: []string{"read_file"},
		},
		Collaboration: CollaborationSurface{
			TopologySummary: "Configured agents:\n- Lead (id: lead)",
		},
	})
	assert.NotContains(t, result, "Configured agents:")
}

func TestAssembleSystemPromptSkipsEnvironmentWithoutFilesystemTools(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "memory",
			Name: "Memory Agent",
		},
		Tools: ToolSurface{
			Names: []string{"memory_search", "memory_get"},
		},
		Collaboration: CollaborationSurface{
			TopologySummary: "Configured agents:\n- Lead (id: lead)",
		},
	})
	assert.NotContains(t, result, "## Environment Facts")
}

func TestAssembleSystemPromptEnvironmentIncludesProjectWorkspaceAndRuntimeDirs(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "context-analyst",
			Name: "Context Analyst",
		},
		Tools: ToolSurface{
			Names: []string{"read_file", "bash", "write_file"},
		},
		Environment: EnvironmentSurface{
			ConfigPath:  "/repo/anyai.yaml",
			ProjectRoot: "/repo",
			Workspace:   "/repo/agents/context-analyst",
			DataDir:     "/repo/anyai",
			MemoryDir:   "/repo/anyai/memory",
		},
	})
	assert.Contains(t, result, "- Project root: /repo")
	assert.Contains(t, result, "- Agent workspace: /repo/agents/context-analyst")
	assert.Contains(t, result, "- Relative path base for file/bash/python tools: /repo/agents/context-analyst")
	assert.Contains(t, result, "- Runtime data directory: /repo/anyai")
	assert.Contains(t, result, "- Sessions directory: /repo/anyai/sessions")
	assert.Contains(t, result, "- Memory directory: /repo/anyai/memory")
}

func TestAssembleSystemPromptIncludesAgentCallContractDetails(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "coder",
			Name: "Coder",
		},
		Tools: ToolSurface{
			Names: []string{"read_file", "write_file", "callagent"},
		},
		Collaboration: CollaborationSurface{
			RunMode:         "agent_call",
			ParentAgentID:   "architect",
			TaskGoal:        "Implement the agreed patch",
			Workspace:       "/repo",
			InputArtifacts:  []string{"/repo/docs/spec.md"},
			ExpectedOutputs: []string{"/repo/out/patch.md"},
			ReturnMode:      "file_paths",
		},
	})
	assert.Contains(t, result, "## Collaboration Contract")
	assert.Contains(t, result, "- Execution mode: child agent call")
	assert.Contains(t, result, "- Parent agent: architect")
	assert.Contains(t, result, "- Task goal: Implement the agreed patch")
	assert.Contains(t, result, "- Task workspace hint: /repo")
	assert.Contains(t, result, "/repo/docs/spec.md")
	assert.Contains(t, result, "/repo/out/patch.md")
	assert.Contains(t, result, "- Return mode: file_paths")
	assert.Contains(t, result, "must use callagent instead of inventing their work yourself")
	assert.Contains(t, result, "prefer one parallel callagent invocation")
}

func TestAssembleSystemPromptIncludesGoalRulesAndState(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "worker",
			Name: "Worker",
		},
		Tools: ToolSurface{
			Names: []string{"todo", "goal_complete"},
		},
		Goal: GoalSurface{
			ID:             "goal_1",
			Description:    "Ship the runtime patch",
			State:          "in_progress",
			TurnCount:      2,
			ShouldContinue: true,
			ContinueReason: "goal still has open todo items",
			OpenTodos: []session.TodoItem{
				{ID: "todo_1", Content: "Close the runtime todo", Status: "open"},
			},
			CurrentPlan: &runtimeplan.Plan{
				ID:          "plan_1",
				Description: "Ship the runtime patch",
				State:       runtimeplan.PlanStateRunning,
				Steps: []runtimeplan.Step{
					{ID: "inspect", Description: "Inspect", State: runtimeplan.StepStateCompleted},
					{ID: "patch", Description: "Patch", State: runtimeplan.StepStatePending},
					{ID: "verify", Description: "Verify", State: runtimeplan.StepStatePending},
				},
			},
		},
	})
	assert.Contains(t, result, "## Goal Completion Rules")
	assert.Contains(t, result, "you may call `goal_complete`")
	assert.Contains(t, result, "Never use `goal_complete` to bypass")
	assert.Contains(t, result, "## Goal State")
	assert.Contains(t, result, "- Goal: Ship the runtime patch")
	assert.Contains(t, result, "- Goal id: goal_1")
	assert.Contains(t, result, "Close the runtime todo (todo_1) [open]")
	assert.Contains(t, result, "Latest recorded plan:")
}

func TestDerivePromptQueryIncludesRecentToolResults(t *testing.T) {
	history := []session.SessionEntry{
		session.UserMessageEntry("Need a release update."),
		session.ToolResultEntry("tc_1", "Beta launch is blocked on staging review.", "", nil),
	}

	query := derivePromptQuery(history, "Need a release update.")
	assert.Contains(t, query, "Current request: Need a release update.")
	assert.Contains(t, query, "Tool result: Beta launch is blocked on staging review.")
}

// --- assembleMessages tests ---

func TestAssembleMessagesUserAndAssistant(t *testing.T) {
	history := []session.SessionEntry{
		session.UserMessageEntry("hello"),
		session.AssistantMessageEntry("hi there"),
	}

	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "hello", msgs[0].Content)
	assert.Equal(t, "assistant", msgs[1].Role)
	assert.Equal(t, "hi there", msgs[1].Content)
}

func TestAssembleMessagesSkipsRuntimeControlEntries(t *testing.T) {
	history := []session.SessionEntry{
		session.UserMessageEntry("first user request"),
		session.RuntimeControlEntry(runtimeControlKindGoalCompletion, "[Runtime goal continuation]\nCall `goal_complete` now instead of ending silently."),
		session.UserMessageEntry("[Runtime goal continuation]\nlegacy control prompt"),
		session.UserMessageEntry("second user request"),
	}

	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Equal(t, "first user request", msgs[0].Content)
	assert.Equal(t, "second user request", msgs[1].Content)
}

func TestAssembleMessagesToolCallAndResult(t *testing.T) {
	tc := session.ToolCallEntry("tc_1", "bash", json.RawMessage(`{"command":"echo hi"}`))
	tr := session.ToolResultEntry("tc_1", "hi\n", "", nil)

	history := []session.SessionEntry{
		session.UserMessageEntry("run echo hi"),
		tc,
		tr,
	}

	msgs := assembleMessages(history)
	require.Len(t, msgs, 3)

	// User message
	assert.Equal(t, "user", msgs[0].Role)

	// Tool call should be an assistant message with tool calls
	assert.Equal(t, "assistant", msgs[1].Role)
	require.Len(t, msgs[1].ToolCalls, 1)
	assert.Equal(t, "tc_1", msgs[1].ToolCalls[0].ID)
	assert.Equal(t, "bash", msgs[1].ToolCalls[0].Name)

	// Tool result should be a user message with ToolCallID
	assert.Equal(t, "user", msgs[2].Role)
	assert.Equal(t, "tc_1", msgs[2].ToolCallID)
	assert.Equal(t, "hi\n", msgs[2].Content)
}

func TestAssembleMessagesMeta(t *testing.T) {
	summaryData, _ := json.Marshal(session.MessageData{Text: "previous conversation summary"})
	meta := session.SessionEntry{
		Type: session.EntryTypeMeta,
		Role: "system",
		Data: summaryData,
	}

	msgs := assembleMessages([]session.SessionEntry{meta})
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "[Session Summary]")
	assert.Contains(t, msgs[0].Content, "previous conversation summary")
}

func TestAssembleMessagesEmpty(t *testing.T) {
	msgs := assembleMessages(nil)
	assert.Nil(t, msgs)

	msgs = assembleMessages([]session.SessionEntry{})
	assert.Nil(t, msgs)
}

func TestAssembleMessagesOrphanedToolCall(t *testing.T) {
	// Simulate an interrupted session: tool_call without tool_result, followed by a new user message
	tc := session.ToolCallEntry("tc_orphan", "bash", json.RawMessage(`{"command":"pwd"}`))

	history := []session.SessionEntry{
		session.UserMessageEntry("run pwd"),
		tc,
		session.UserMessageEntry("hello again"),
	}

	msgs := assembleMessages(history)

	// Should have: user, assistant(tool_call), synthetic tool_result, user
	require.Len(t, msgs, 4)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "assistant", msgs[1].Role)
	require.Len(t, msgs[1].ToolCalls, 1)
	// Synthetic result injected
	assert.Equal(t, "user", msgs[2].Role)
	assert.Equal(t, "tc_orphan", msgs[2].ToolCallID)
	assert.True(t, msgs[2].IsError)
	assert.Contains(t, msgs[2].Content, "interrupted")
	// New user message
	assert.Equal(t, "user", msgs[3].Role)
	assert.Equal(t, "hello again", msgs[3].Content)
}

func TestAssembleMessagesOrphanedToolCallAtEnd(t *testing.T) {
	// Tool call at end of history with no result and no following message
	tc := session.ToolCallEntry("tc_end", "bash", json.RawMessage(`{"command":"ls"}`))

	history := []session.SessionEntry{
		session.UserMessageEntry("list files"),
		tc,
	}

	msgs := assembleMessages(history)

	// Should have: user, assistant(tool_call), synthetic tool_result
	require.Len(t, msgs, 3)
	assert.Equal(t, "tc_end", msgs[2].ToolCallID)
	assert.True(t, msgs[2].IsError)
}

func TestAssembleMessagesRepairsOrphanedToolResult(t *testing.T) {
	history := []session.SessionEntry{
		session.UserMessageEntry("hello"),
		session.ToolResultEntry("tc_missing", "", "tool failed", nil),
	}

	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "user", msgs[1].Role)
	assert.Empty(t, msgs[1].ToolCallID)
	assert.Contains(t, msgs[1].Content, "Recovered orphaned tool result")
	assert.Contains(t, msgs[1].Content, "tool failed")
}

// --- pruneToolResults tests ---

func TestPruneToolResults(t *testing.T) {
	longContent := strings.Repeat("a", 20000)
	msgs := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "user", Content: longContent, ToolCallID: "tc_1"},
	}

	pruneToolResults(msgs, 10000)

	// User message should be unchanged
	assert.Equal(t, "hello", msgs[0].Content)

	// Tool result should be truncated
	assert.Less(t, len(msgs[1].Content), 20000)
	assert.Contains(t, msgs[1].Content, "[output truncated")
}

func TestPruneToolResultsShort(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "short output", ToolCallID: "tc_1"},
	}

	pruneToolResults(msgs, 10000)

	assert.Equal(t, "short output", msgs[0].Content)
}

func TestPruneToolResultsNewlineBoundary(t *testing.T) {
	// Build content with newlines so truncation prefers a newline boundary
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(strings.Repeat("x", 80))
		b.WriteString("\n")
	}
	content := b.String() // ~16200 chars

	msgs := []llm.Message{
		{Role: "user", Content: content, ToolCallID: "tc_1"},
	}

	pruneToolResults(msgs, 10000)

	// Should be truncated and contain the truncation marker
	truncated := msgs[0].Content
	assert.Contains(t, truncated, "[output truncated")
	assert.Less(t, len(truncated), len(content))

	// The truncated content (before the suffix) should end at a newline boundary
	suffixIdx := strings.Index(truncated, "\n\n[output truncated")
	assert.Greater(t, suffixIdx, 0, "should contain truncation suffix")
}

// --- Runtime tests ---

func TestRuntimeRun(t *testing.T) {
	mock := &mockLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "Hello "},
			{Type: llm.EventTextDelta, Text: "world!"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()

	rt := &Runtime{
		LLM:       mock,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	var textParts []string
	var gotDone bool
	for e := range events {
		switch e.Type {
		case EventTextDelta:
			textParts = append(textParts, e.Text)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.Equal(t, []string{"Hello ", "world!"}, textParts)
	assert.True(t, gotDone)
}

func TestRuntimeRunSettlesQuietlyForEmptyCompletion(t *testing.T) {
	mock := &mockLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()

	rt := &Runtime{
		LLM:       mock,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	var gotDone bool
	var textParts []string
	var gotFallback bool
	for e := range events {
		switch e.Type {
		case EventTextDelta:
			textParts = append(textParts, e.Text)
		case EventFallbackReply:
			gotFallback = true
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	require.True(t, gotDone)
	require.False(t, gotFallback)
	assert.Empty(t, textParts)

	history := sess.History()
	require.Len(t, history, 1)
	assert.Equal(t, session.EntryTypeMessage, history[0].Type)
	assert.Equal(t, "user", history[0].Role)
}

func TestRuntimeRunFailsAfterRepeatedEmptyGoalCompletionTurns(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_empty_finalize",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	todoID := stateStore.AddTodo(context.Background(), "Close the runtime todo")
	require.NotEmpty(t, todoID)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_todo", Name: "todo"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_todo",
					Name:  "todo",
					Input: json.RawMessage(`{"action":"complete","item":"` + todoID + `"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.TodoTool{Store: stateStore})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "finish the task", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty responses after runtime requested explicit goal_complete")
	assert.Empty(t, text)
	assert.Equal(t, 4, callCount)

	todos := stateStore.ListTodos(context.Background())
	require.Len(t, todos, 1)
	assert.Equal(t, "completed", todos[0].Status)

	var acceptedCompletion bool
	for _, entry := range sess.History() {
		switch entry.Type {
		case session.EntryTypeToolResult:
			var data session.ToolResultData
			require.NoError(t, json.Unmarshal(entry.Data, &data))
			if strings.Contains(data.Output, "marked as completed") {
				acceptedCompletion = true
			}
		}
	}
	assert.True(t, sessionHasRuntimeControlText(t, sess.History(),
		"[Runtime goal continuation]",
		"Call `goal_complete` now instead of ending silently.",
	))
	assert.False(t, acceptedCompletion)
}

func TestRuntimeRunCompletesWithExplicitGoalCompleteAfterPrompt(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_explicit_finalize",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	todoID := stateStore.AddTodo(context.Background(), "Close the runtime todo")
	require.NotEmpty(t, todoID)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_todo", Name: "todo"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_todo",
					Name:  "todo",
					Input: json.RawMessage(`{"action":"complete","item":"` + todoID + `"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_goal_done", Name: "goal_complete"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_goal_done",
					Name:  "goal_complete",
					Input: json.RawMessage(`{}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "Goal is complete."},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.TodoTool{Store: stateStore})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "finish the task", nil)
	require.NoError(t, err)
	assert.Equal(t, "Goal is complete.", text)
	assert.Equal(t, 4, callCount)

	var acceptedCompletion bool
	for _, entry := range sess.History() {
		switch entry.Type {
		case session.EntryTypeToolResult:
			var data session.ToolResultData
			require.NoError(t, json.Unmarshal(entry.Data, &data))
			if data.ToolCallID == "tc_goal_done" &&
				strings.Contains(data.Output, "marked as completed") {
				acceptedCompletion = true
			}
		}
	}
	assert.True(t, sessionHasRuntimeControlText(t, sess.History(),
		"[Runtime goal continuation]",
		"Call `goal_complete` now instead of ending silently.",
	))
	assert.True(t, sessionHasRuntimeControlText(t, sess.History(), "[Runtime visible reply required]"))
	assert.True(t, acceptedCompletion)
}

func TestRuntimeRunInjectsCurrentRuntimeControlOnlyOnce(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_inject_control",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	todoID := stateStore.AddTodo(context.Background(), "Close the runtime todo")
	require.NotEmpty(t, todoID)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_todo", Name: "todo"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_todo",
					Name:  "todo",
					Input: json.RawMessage(`{"action":"complete","item":"` + todoID + `"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "finished"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.TodoTool{Store: stateStore})
	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "finish the task", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires explicit goal_complete")
	assert.Equal(t, "finished", text)

	requests := provider.Requests()
	require.Len(t, requests, 3)
	assert.NotContains(t, requestMessagesText(requests[1].Messages), "[Runtime goal continuation]")
	require.NotEmpty(t, requests[2].Messages)
	assert.Equal(t, llm.MessageRoleRuntime, requests[2].Messages[len(requests[2].Messages)-1].Role)
	assert.Contains(t, requests[2].Messages[len(requests[2].Messages)-1].Content, "[Runtime goal continuation]")
}

func TestRuntimeRunDoesNotReplayLegacyRuntimeControlFromPreviousRun(t *testing.T) {
	sess := session.NewSession("test-agent", "shared-session")
	sess.Append(session.ApplyEntryRefs(
		session.UserMessageEntry("previous request"),
		session.EntryRefs{RunID: "run_previous"},
	))
	sess.Append(session.ApplyEntryRefs(
		session.UserMessageEntry("[Runtime goal continuation]\nCall `goal_complete` now instead of ending silently."),
		session.EntryRefs{RunID: "run_previous"},
	))

	provider := &capturingLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventDone},
		},
	}
	reg := tools.NewRegistry()
	reg.Register(&capturingTool{name: "goal_complete", result: tools.ToolResult{Output: "should not be called"}})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  3,
	}
	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:     "run_current",
		SessionID: "shared-session",
	})
	text, err := rt.RunSync(ctx, "new request", nil)
	require.NoError(t, err)
	assert.Empty(t, text)

	requests := provider.Requests()
	require.Len(t, requests, 1)
	joined := requestMessagesText(requests[0].Messages)
	assert.NotContains(t, joined, "Call `goal_complete` now instead of ending silently.")
	assert.Contains(t, joined, "new request")

	focus := deriveRequestFocus(sess.History(), "fallback")
	assert.Equal(t, "new request", focus.CurrentRequest)
}

func TestRuntimeRunFailsWhenModelRepliesWithoutCompletionToolAfterGoalPrompt(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_text_finalize",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	todoID := stateStore.AddTodo(context.Background(), "Close the runtime todo")
	require.NotEmpty(t, todoID)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_todo", Name: "todo"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_todo",
					Name:  "todo",
					Input: json.RawMessage(`{"action":"complete","item":"` + todoID + `"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "finished"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.TodoTool{Store: stateStore})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "finish the task", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires explicit goal_complete or await_user_input")
	assert.Equal(t, "finished", text)
	assert.Equal(t, 3, callCount)
}

func TestRuntimeRunStopsWithAwaitUserInputAfterGoalPrompt(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_await_prompt",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	todoID := stateStore.AddTodo(context.Background(), "Close the runtime todo")
	require.NotEmpty(t, todoID)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_todo", Name: "todo"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_todo",
					Name:  "todo",
					Input: json.RawMessage(`{"action":"complete","item":"` + todoID + `"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_await", Name: "await_user_input"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_await",
					Name:  "await_user_input",
					Input: json.RawMessage(`{"message":"Please choose option A or B."}`),
				}},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.TodoTool{Store: stateStore})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}
	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{RunID: "run_await_prompt"})
	text, err := rt.RunSync(ctx, "finish the task", nil)
	require.NoError(t, err)
	assert.Equal(t, "Please choose option A or B.", text)
	assert.Equal(t, 3, callCount)

	goal, ok := rt.GoalRuntime.GetGoal(task.GoalID("run_await_prompt"))
	require.True(t, ok)
	require.NotNil(t, goal)
	assert.Equal(t, task.GoalStateAwaitingInput, goal.State)
	assert.Equal(t, "Please choose option A or B.", goal.AwaitingInputMessage)

	var sawAwaitResult bool
	for _, entry := range sess.History() {
		switch entry.Type {
		case session.EntryTypeToolResult:
			var data session.ToolResultData
			require.NoError(t, json.Unmarshal(entry.Data, &data))
			if data.ToolCallID == "tc_await" &&
				strings.Contains(data.Output, "awaiting user input") {
				sawAwaitResult = true
			}
		}
	}
	assert.True(t, sessionHasRuntimeControlText(t, sess.History(),
		"[Runtime goal continuation]",
		"Call `goal_complete` now instead of ending silently.",
	))
	assert.True(t, sawAwaitResult)
}

func TestRuntimeRunRetriesLLMRequestFailure(t *testing.T) {
	provider := &scriptedLLMProvider{
		outcomes: []scriptedLLMOutcome{
			{err: errors.New("dial tcp timeout")},
			{events: []llm.ChatEvent{
				{Type: llm.EventTextDelta, Text: "Recovered"},
				{Type: llm.EventDone},
			}},
		},
	}

	rt := &Runtime{
		LLM:               provider,
		Tools:             tools.NewRegistry(),
		Session:           session.NewSession("test-agent", "test-key"),
		Model:             "mock-model",
		Workspace:         t.TempDir(),
		MaxTurns:          5,
		LLMMaxAttempts:    3,
		LLMRetryBackoffMS: 1,
	}

	events, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	var gotRetry bool
	var gotDone bool
	var parts []string
	for e := range events {
		switch e.Type {
		case EventLLMRetry:
			gotRetry = true
			require.NotNil(t, e.LLMRetry)
			assert.Equal(t, 2, e.LLMRetry.Attempt)
			assert.Equal(t, 3, e.LLMRetry.MaxAttempts)
			assert.Equal(t, "request", e.LLMRetry.Stage)
		case EventTextDelta:
			parts = append(parts, e.Text)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.True(t, gotRetry)
	assert.True(t, gotDone)
	assert.Equal(t, []string{"Recovered"}, parts)
	assert.Len(t, provider.Requests(), 2)
}

func TestRuntimeRunRetriesEarlyStreamFailure(t *testing.T) {
	provider := &scriptedLLMProvider{
		outcomes: []scriptedLLMOutcome{
			{events: []llm.ChatEvent{
				{Type: llm.EventActivity},
				{Type: llm.EventError, Error: errors.New("temporary stream error")},
			}},
			{events: []llm.ChatEvent{
				{Type: llm.EventTextDelta, Text: "done"},
				{Type: llm.EventDone},
			}},
		},
	}

	rt := &Runtime{
		LLM:               provider,
		Tools:             tools.NewRegistry(),
		Session:           session.NewSession("test-agent", "test-key"),
		Model:             "mock-model",
		Workspace:         t.TempDir(),
		MaxTurns:          5,
		LLMMaxAttempts:    3,
		LLMRetryBackoffMS: 1,
	}

	events, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	var gotRetry bool
	var gotDone bool
	for e := range events {
		switch e.Type {
		case EventLLMRetry:
			gotRetry = true
			require.NotNil(t, e.LLMRetry)
			assert.Equal(t, "stream", e.LLMRetry.Stage)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.True(t, gotRetry)
	assert.True(t, gotDone)
	assert.Len(t, provider.Requests(), 2)
}

func TestRuntimeRunWithToolCalls(t *testing.T) {
	callCount := 0

	// Use a stateful mock that returns different responses
	statefulMock := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			// First response: tool call
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"/tmp/test.txt"}`),
				}},
				{Type: llm.EventDone},
			},
			// Second response: text
			{
				{Type: llm.EventTextDelta, Text: "File contents: hello"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")

	// Create a registry with a mock tool
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "read_file", output: "hello"})

	rt := &Runtime{
		LLM:       statefulMock,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "read test.txt", nil)
	require.NoError(t, err)

	var gotToolResult bool
	var gotDone bool
	for e := range events {
		switch e.Type {
		case EventToolResult:
			gotToolResult = true
			assert.Equal(t, "read_file", e.ToolCall.Name)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.True(t, gotToolResult, "should have received tool result")
	assert.True(t, gotDone, "should have received done event")
}

func TestRuntimeRunUsesBrowserToolToAnalyzeLocalhost3000(t *testing.T) {
	resp, err := http.Get("http://localhost:3000/")
	if err != nil {
		t.Skipf("localhost:3000 is not running for browser integration test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Skipf("localhost:3000 returned %s", resp.Status)
	}

	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_browser", Name: "browser"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_browser",
					Name:  "browser",
					Input: json.RawMessage(`{"action":"get_text","url":"http://localhost:3000/","selector":"body","timeout":30}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "已通过 browser 打开 localhost:3000，并根据页面 HTML 分析出页面包含 game-container、canvas 或脚本入口等内容。"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "browser-localhost")
	reg := tools.NewRegistry()
	reg.Register(&tools.BrowserTool{})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "打开 http://localhost:3000/ 并分析页面内容", nil)
	require.NoError(t, err)
	var final strings.Builder
	var browserOutput string
	for event := range events {
		switch event.Type {
		case EventToolResult:
			if event.ToolCall != nil && event.ToolCall.Name == "browser" && event.Result != nil {
				browserOutput = event.Result.Output
			}
		case EventTextDelta:
			final.WriteString(event.Text)
		case EventError:
			t.Fatalf("unexpected browser run error: %v", event.Error)
		}
	}

	require.NotEmpty(t, strings.TrimSpace(browserOutput))
	assert.True(t,
		strings.Contains(browserOutput, "<") ||
			strings.Contains(browserOutput, "canvas") ||
			strings.Contains(browserOutput, "script") ||
			strings.Contains(browserOutput, "body"),
		"expected browser output to include page markup/content, got %q", browserOutput,
	)
	assert.Contains(t, final.String(), "browser")
	assert.Contains(t, final.String(), "localhost:3000")

	requests := provider.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, browserOutput, toolResultContentForTest(requests[1].Messages, "tc_browser"))
}

func TestRuntimeRunKeepsBrowserPageAcrossModelTurns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head><title>Runtime Browser State</title></head>
  <body>
    <h1 id="headline">Persistent Browser Page</h1>
    <script>
      window.anyaiState = {value: "kept-across-turns"};
    </script>
  </body>
</html>`))
	}))
	defer server.Close()

	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_nav", Name: "browser"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_nav",
					Name:  "browser",
					Input: json.RawMessage(fmt.Sprintf(`{"action":"navigate","url":%q}`, server.URL)),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_eval", Name: "browser"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_eval",
					Name:  "browser",
					Input: json.RawMessage(`{"action":"evaluate","script":"({title: document.title, headline: document.querySelector('#headline').textContent, state: window.anyaiState.value})"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "browser page stayed available across turns"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "browser-continuous")
	reg := tools.NewRegistry()
	reg.Register(&tools.BrowserTool{})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "open the page and inspect it twice", nil)
	require.NoError(t, err)
	var navOutput string
	var evalOutput string
	var final strings.Builder
	for event := range events {
		switch event.Type {
		case EventToolResult:
			if event.ToolCall == nil || event.Result == nil {
				continue
			}
			switch event.ToolCall.ID {
			case "tc_nav":
				navOutput = event.Result.Output
			case "tc_eval":
				evalOutput = event.Result.Output
			}
		case EventTextDelta:
			final.WriteString(event.Text)
		case EventError:
			t.Fatalf("unexpected browser run error: %v", event.Error)
		}
	}

	assert.Contains(t, navOutput, "Runtime Browser State")
	assert.Contains(t, evalOutput, "Runtime Browser State")
	assert.Contains(t, evalOutput, "Persistent Browser Page")
	assert.Contains(t, evalOutput, "kept-across-turns")
	assert.Contains(t, final.String(), "browser page stayed available")

	requests := provider.Requests()
	require.Len(t, requests, 3)
	assert.Contains(t, toolResultContentForTest(requests[2].Messages, "tc_eval"), "kept-across-turns")
}

func TestRuntimeRunKeepsBrowserPageAcrossToolCallsInSameTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head><title>Browser Batch State</title></head>
  <body>
    <h1 id="headline">Sequential Browser Batch</h1>
    <script>
      window.anyaiBatchState = "same-turn-state";
    </script>
  </body>
</html>`))
	}))
	defer server.Close()

	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_nav", Name: "browser"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_nav",
					Name:  "browser",
					Input: json.RawMessage(fmt.Sprintf(`{"action":"navigate","url":%q}`, server.URL)),
				}},
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_eval", Name: "browser"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_eval",
					Name:  "browser",
					Input: json.RawMessage(`{"action":"evaluate","script":"({title: document.title, headline: document.querySelector('#headline').textContent, state: window.anyaiBatchState})"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "browser batch stayed ordered"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "browser-same-turn")
	reg := tools.NewRegistry()
	reg.Register(&tools.BrowserTool{})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  4,
	}

	events, err := rt.Run(context.Background(), "open and inspect in one turn", nil)
	require.NoError(t, err)
	var evalOutput string
	var final strings.Builder
	var fanout *ToolFanoutInfo
	for event := range events {
		switch event.Type {
		case EventToolResult:
			if event.ToolCall != nil && event.ToolCall.ID == "tc_eval" && event.Result != nil {
				evalOutput = event.Result.Output
			}
		case EventToolFanoutCompleted:
			fanout = event.ToolFanout
		case EventTextDelta:
			final.WriteString(event.Text)
		case EventError:
			t.Fatalf("unexpected browser run error: %v", event.Error)
		}
	}

	assert.Contains(t, evalOutput, "Browser Batch State")
	assert.Contains(t, evalOutput, "Sequential Browser Batch")
	assert.Contains(t, evalOutput, "same-turn-state")
	assert.Contains(t, final.String(), "browser batch stayed ordered")
	require.NotNil(t, fanout)
	require.Len(t, fanout.Calls, 2)
	assert.Equal(t, "tc_nav", fanout.Calls[0].ID)
	assert.Equal(t, "tc_eval", fanout.Calls[1].ID)
	assert.Less(t, fanout.Calls[0].StartedOrder, fanout.Calls[1].StartedOrder)
	assert.Less(t, fanout.Calls[0].CompletedOrder, fanout.Calls[1].CompletedOrder)

	requests := provider.Requests()
	require.Len(t, requests, 2)
	assert.Contains(t, toolResultContentForTest(requests[1].Messages, "tc_eval"), "same-turn-state")
}

func TestRuntimeRunExecutesParallelSafeToolsWithFanoutAndStableSessionOrder(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}},
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_2", Name: "web_search"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_2",
					Name:  "web_search",
					Input: json.RawMessage(`{"query":"anyai"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "done"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	time.AfterFunc(20*time.Millisecond, closeRelease)
	var started int
	var mu sync.Mutex
	timedRead := &timedTool{
		name:   "read_file",
		output: "file",
		onStart: func() {
			mu.Lock()
			defer mu.Unlock()
			started++
			if started == 2 {
				closeRelease()
			}
		},
		waitFor: release,
		delay:   70 * time.Millisecond,
	}
	timedSearch := &timedTool{
		name:   "web_search",
		output: "search",
		onStart: func() {
			mu.Lock()
			defer mu.Unlock()
			started++
			if started == 2 {
				closeRelease()
			}
		},
		waitFor: release,
		delay:   70 * time.Millisecond,
	}
	reg.Register(timedRead)
	reg.Register(timedSearch)

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  4,
	}

	events, err := rt.Run(context.Background(), "do both", nil)
	require.NoError(t, err)

	var requestedCount int
	var startedCount int
	var resultCount int
	var fanout *ToolFanoutInfo
	for event := range events {
		switch event.Type {
		case EventToolCallRequested:
			requestedCount++
		case EventToolCallStart:
			startedCount++
		case EventToolResult:
			resultCount++
		case EventToolFanoutCompleted:
			fanout = event.ToolFanout
		case EventError:
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	assert.Equal(t, 2, requestedCount)
	assert.Equal(t, 2, startedCount)
	assert.Equal(t, 2, resultCount)
	require.NotNil(t, fanout)
	assert.Equal(t, "completed", fanout.Status)
	assert.Len(t, fanout.Calls, 2)
	history := sess.History()
	var toolResults []session.ToolResultData
	for _, entry := range history {
		if entry.Type != session.EntryTypeToolResult {
			continue
		}
		var item session.ToolResultData
		require.NoError(t, json.Unmarshal(entry.Data, &item))
		if item.ToolCallID == "tc_auto_goal_complete_1" {
			continue
		}
		toolResults = append(toolResults, item)
	}
	require.Len(t, toolResults, 2)
	assert.Equal(t, "tc_1", toolResults[0].ToolCallID)
	assert.Equal(t, "tc_2", toolResults[1].ToolCallID)
}

func TestRuntimeRunRoutesToolCallsThroughTaskRuntime(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "handled"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "read_file", output: "hello"})

	recorder := runtimeevents.NewRecorder()
	taskStore := task.NewStore(task.WithEventAppender(recorder.AppendEvent))
	registry := task.NewExecutorRegistry()
	require.NoError(t, registry.Register(task.KindTool, testToolTaskExecutor{executor: reg}))

	taskRuntime := task.NewRuntime(taskStore, registry)
	taskRuntime.SetEventAppender(recorder.AppendEvent)

	rt := &Runtime{
		LLM:         provider,
		Tools:       reg,
		Session:     sess,
		Model:       "mock-model",
		Workspace:   t.TempDir(),
		MaxTurns:    4,
		TaskRuntime: taskRuntime,
	}

	events, err := rt.Run(context.Background(), "read file", nil)
	require.NoError(t, err)
	for event := range events {
		if event.Type == EventError {
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	tasks := taskStore.ListAll()
	require.Len(t, tasks, 1)
	assert.Equal(t, task.KindTool, tasks[0].Kind)
	assert.Equal(t, task.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "read_file", tasks[0].ToolName)
	assert.Equal(t, "hello", tasks[0].Summary)
}

func TestRuntimeRunExecutesOriginalWriteFileInputWhilePersistingSummary(t *testing.T) {
	dir := t.TempDir()
	longContent := strings.Repeat("0123456789", 80)
	input, err := json.Marshal(map[string]any{
		"path":    "large.txt",
		"content": longContent,
	})
	require.NoError(t, err)

	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_write", Name: "write_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_write",
					Name:  "write_file",
					Input: input,
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "done"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&tools.WriteFileTool{WorkDir: dir})

	recorder := runtimeevents.NewRecorder()
	taskStore := task.NewStore(task.WithEventAppender(recorder.AppendEvent))
	registry := task.NewExecutorRegistry()
	require.NoError(t, registry.Register(task.KindTool, runtimetaskbuiltin.NewToolExecutor(reg)))

	taskRuntime := task.NewRuntime(taskStore, registry)
	taskRuntime.SetEventAppender(recorder.AppendEvent)

	rt := &Runtime{
		LLM:         provider,
		Tools:       reg,
		Session:     sess,
		Model:       "mock-model",
		Workspace:   dir,
		MaxTurns:    4,
		TaskRuntime: taskRuntime,
	}

	events, err := rt.Run(context.Background(), "write file", nil)
	require.NoError(t, err)
	for event := range events {
		if event.Type == EventError {
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, "large.txt"))
	require.NoError(t, err)
	assert.Equal(t, longContent, string(data))

	var storedInput string
	for _, entry := range sess.History() {
		if entry.Type != session.EntryTypeToolCall {
			continue
		}
		var data session.ToolCallData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		if data.ID == "tc_write" {
			storedInput = string(data.Input)
		}
	}
	require.NotEmpty(t, storedInput)
	assert.Contains(t, storedInput, "content_bytes")
	assert.Contains(t, storedInput, "content omitted")
	assert.NotContains(t, storedInput, longContent)

	tasks := taskStore.ListAll()
	require.Len(t, tasks, 1)
	assert.Contains(t, tasks[0].Input, "content_bytes")
	assert.NotContains(t, tasks[0].Input, longContent)
}

func TestRuntimeRunRoutesBashThroughProcessTask(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "bash"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"if [ ! -f marker ]; then touch marker; echo 'dial tcp 127.0.0.1:9: connection refused' >&2; exit 1; fi; printf hello","timeout":5}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "handled"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	workDir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(&tools.BashTool{
		WorkDir:    workDir,
		ExecPolicy: &tools.ExecPolicy{Level: "full"},
	})

	recorder := runtimeevents.NewRecorder()
	taskStore := task.NewStore(task.WithEventAppender(recorder.AppendEvent))
	registry := task.NewExecutorRegistry()
	require.NoError(t, registry.Register(task.KindProcess, runtimetaskbuiltin.NewProcessExecutor()))

	taskRuntime := task.NewRuntime(taskStore, registry)
	taskRuntime.SetEventAppender(recorder.AppendEvent)

	rt := &Runtime{
		LLM:         provider,
		Tools:       reg,
		Session:     sess,
		Model:       "mock-model",
		Workspace:   workDir,
		MaxTurns:    4,
		TaskRuntime: taskRuntime,
		ToolRecovery: ToolRecoveryConfig{
			MaxAttempts:    2,
			RetryBackoffMS: 1,
		},
	}

	events, err := rt.Run(context.Background(), "run bash", nil)
	require.NoError(t, err)
	var sawRetry bool
	for event := range events {
		switch event.Type {
		case EventError:
			t.Fatalf("unexpected error: %v", event.Error)
		case EventToolRetry:
			sawRetry = true
		}
	}
	assert.True(t, sawRetry)

	tasks := taskStore.ListAll()
	require.Len(t, tasks, 1)
	assert.Equal(t, task.KindProcess, tasks[0].Kind)
	assert.Equal(t, task.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "bash", tasks[0].ProcessName)
	assert.Equal(t, "hello", tasks[0].Summary)

	result := mustToolResultDataForCall(t, sess.History(), "tc_1")
	assert.Equal(t, "completed_after_retry", result.Metadata["decision"])
	assert.Equal(t, float64(2), result.Metadata["attempt"])
	assert.Equal(t, float64(2), result.Metadata["max_attempts"])
	assert.Equal(t, float64(1), result.Metadata["retry_count"])
}

func TestRuntimeRunRoutesUpdatePlanThroughToolTaskRuntime(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "update_plan"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "update_plan",
					Input: json.RawMessage(`{"plan":{"description":"Ship the fix","state":"running","steps":[{"id":"inspect","description":"Inspect","state":"completed"},{"id":"implement","description":"Implement","state":"pending"}]}}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "handled"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	planStore := &mockPlanStore{}
	reg.Register(&tools.UpdatePlanTool{Store: planStore})

	recorder := runtimeevents.NewRecorder()
	taskStore := task.NewStore(task.WithEventAppender(recorder.AppendEvent))
	registry := task.NewExecutorRegistry()
	require.NoError(t, registry.Register(task.KindTool, testToolTaskExecutor{executor: reg}))

	taskRuntime := task.NewRuntime(taskStore, registry)
	taskRuntime.SetEventAppender(recorder.AppendEvent)

	rt := &Runtime{
		LLM:         provider,
		Tools:       reg,
		Session:     sess,
		Model:       "mock-model",
		Workspace:   t.TempDir(),
		MaxTurns:    4,
		TaskRuntime: taskRuntime,
	}

	events, err := rt.Run(context.Background(), "run workflow", nil)
	require.NoError(t, err)
	for event := range events {
		if event.Type == EventError {
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	tasks := taskStore.ListAll()
	require.Len(t, tasks, 1)
	assert.Equal(t, task.KindTool, tasks[0].Kind)
	assert.Equal(t, task.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "update_plan", tasks[0].ToolName)
	assert.Equal(t, "Structured plan updated.", tasks[0].Summary)
	assert.Contains(t, planStore.Plan(), "Ship the fix")
	assert.Contains(t, planStore.Plan(), "[pending] Implement")
}

func TestSubmitCallAgentToolTaskBridgesParentActivity(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		runner := &recordingAgentCallRunner{}
		reg := tools.NewRegistry()
		tools.RegisterCallAgent(reg, runner, 4, 300000)

		rt := &Runtime{Tools: reg}
		var emitted []EventType

		handled, err := rt.submitCallAgentToolTask(context.Background(), tools.ToolCallSpec{
			Call: llm.ToolCall{
				ID:    "call_single",
				Name:  "callagent",
				Input: json.RawMessage(`{"target_agent":"assistant","task":"ship it"}`),
			},
		}, func(event AgentEvent) {
			emitted = append(emitted, event.Type)
		}, func(result tools.ToolResult, record task.Record, resultErr error) {})
		require.True(t, handled)
		require.NoError(t, err)
		assert.Equal(t, 1, runner.singleCalls)
		assert.Contains(t, emitted, EventActivity)
	})

	t.Run("parallel", func(t *testing.T) {
		runner := &recordingAgentCallRunner{}
		reg := tools.NewRegistry()
		tools.RegisterCallAgent(reg, runner, 4, 300000)

		rt := &Runtime{Tools: reg}
		var emitted []EventType

		handled, err := rt.submitCallAgentToolTask(context.Background(), tools.ToolCallSpec{
			Call: llm.ToolCall{
				ID:   "call_parallel",
				Name: "callagent",
				Input: json.RawMessage(`{
					"mode":"parallel",
					"tasks":[
						{"target_agent":"assistant","task":"ship A"},
						{"target_agent":"assistant","task":"ship B"}
					]
				}`),
			},
		}, func(event AgentEvent) {
			emitted = append(emitted, event.Type)
		}, func(result tools.ToolResult, record task.Record, resultErr error) {})
		require.True(t, handled)
		require.NoError(t, err)
		assert.Equal(t, 1, runner.parallelCalls)
		assert.Contains(t, emitted, EventActivity)
	})
}

func TestRuntimeRunSurfacesRecoverableCallAgentValidationErrorToModel(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "call_bad", Name: "callagent"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:   "call_bad",
					Name: "callagent",
					Input: json.RawMessage(`{
						"mode":"parallel",
						"tasks":"[{\"target_agent\":\"reviewer\",\"task\":\"review it\"}]"
					}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "call_good", Name: "callagent"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "call_good",
					Name:  "callagent",
					Input: json.RawMessage(`{"target_agent":"reviewer","task":"review it"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "review complete"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	runner := &recordingAgentCallRunner{}
	reg := tools.NewRegistry()
	tools.RegisterCallAgent(reg, runner, 4, 300000)

	sess := session.NewSession("tech-lead", "cli_local")
	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  4,
	}

	var sawDone bool
	events, err := rt.Run(context.Background(), "continue workflow", nil)
	require.NoError(t, err)
	for event := range events {
		switch event.Type {
		case EventError:
			t.Fatalf("unexpected error: %v", event.Error)
		case EventDone:
			sawDone = true
		}
	}

	require.True(t, sawDone)
	assert.Equal(t, 3, callCount)
	assert.Equal(t, 1, runner.singleCalls)
	assert.Equal(t, 0, runner.parallelCalls)

	badResult := mustToolResultDataForCall(t, sess.History(), "call_bad")
	assert.Equal(t, "tasks is required when mode=parallel", badResult.Error)
	assert.Equal(t, "validation_error", badResult.Metadata["error_class"])
	assert.Equal(t, true, badResult.Metadata["model_recoverable"])
	assert.Equal(t, false, badResult.Metadata["fatal_execution_err"])

	goodResult := mustToolResultDataForCall(t, sess.History(), "call_good")
	assert.Empty(t, goodResult.Error)
	assert.Contains(t, goodResult.Output, `"target_agent":"reviewer"`)
}

func cloneTestMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

type testToolTaskExecutor struct {
	executor tools.Executor
}

func (e testToolTaskExecutor) Kind() task.Kind {
	return task.KindTool
}

func (e testToolTaskExecutor) Execute(ctx context.Context, record task.Record) (task.Result, error) {
	call := llm.ToolCall{
		ID:   record.ID,
		Name: record.ToolName,
	}
	if record.Input != "" {
		call.Input = json.RawMessage(record.Input)
	}
	if exec := tools.TaskToolExecutionFromContext(ctx); exec != nil {
		result, err := exec(ctx, call)
		return task.Result{
			Status:   task.StatusCompleted,
			Summary:  result.Output,
			Error:    result.Error,
			Metadata: cloneTestMap(result.Metadata),
			Images:   append([]llm.ImageContent(nil), result.Images...),
		}, err
	}
	result, err := e.executor.Execute(ctx, call.Name, call.Input)
	return task.Result{
		Status:   task.StatusCompleted,
		Summary:  result.Output,
		Error:    result.Error,
		Metadata: cloneTestMap(result.Metadata),
		Images:   append([]llm.ImageContent(nil), result.Images...),
	}, err
}

type testProcessTaskExecutor struct{}

func (e testProcessTaskExecutor) Kind() task.Kind {
	return task.KindProcess
}

func (e testProcessTaskExecutor) Execute(ctx context.Context, record task.Record) (task.Result, error) {
	switch strings.TrimSpace(record.ProcessName) {
	case "bash":
		call := llm.ToolCall{
			ID:    metadataStringValue(record.Metadata, "tool_call_id"),
			Name:  "bash",
			Input: json.RawMessage(record.Input),
		}
		if call.ID == "" {
			call.ID = record.ID
		}
		if exec := tools.TaskToolExecutionFromContext(ctx); exec != nil {
			result, err := exec(ctx, call)
			return task.Result{
				Status:   task.StatusCompleted,
				Summary:  result.Output,
				Error:    result.Error,
				Metadata: cloneTestMap(result.Metadata),
			}, err
		}
		in, err := tools.ParseBashProcessInput(json.RawMessage(record.Input))
		if err != nil {
			return task.Result{Status: task.StatusFailed, Error: err.Error()}, nil
		}
		result, err := tools.RunBashProcess(ctx, record.Contract.Workspace, nil, in)
		return task.Result{
			Status:   task.StatusCompleted,
			Summary:  result.Output,
			Error:    result.Error,
			Metadata: cloneTestMap(result.Metadata),
		}, err
	default:
		return task.Result{Status: task.StatusFailed, Error: "unsupported process"}, nil
	}
}

type recordingAgentCallRunner struct {
	singleCalls   int
	parallelCalls int
}

func (r *recordingAgentCallRunner) DoAgent(ctx context.Context, req tools.AgentCallRequest, callback tools.AgentResultCallback) (string, error) {
	r.singleCalls++
	runtimeactivity.Emit(ctx, map[string]any{"target_agent": req.Agent})
	if callback != nil {
		callback(ctx, tools.AgentCallResult{
			Agent:   req.Agent,
			Status:  "completed",
			Summary: "done",
		})
	}
	return "task_single", nil
}

func (r *recordingAgentCallRunner) DoAgentParallel(ctx context.Context, tasks []tools.AgentCallRequest, maxParallel int, callback tools.ParallelAgentResultCallback) (string, error) {
	r.parallelCalls++
	runtimeactivity.Emit(ctx, map[string]any{"parallel_count": len(tasks)})
	if callback != nil {
		results := make([]tools.AgentCallResult, 0, len(tasks))
		for _, task := range tasks {
			results = append(results, tools.AgentCallResult{
				Agent:   task.Agent,
				Status:  "completed",
				Summary: "done",
			})
		}
		callback(ctx, tools.ParallelAgentCallResult{
			Mode:    "parallel",
			BatchID: "batch_test",
			Results: results,
		})
	}
	return "batch_test", nil
}

func (r *recordingAgentCallRunner) AvailableAgents() []tools.AgentInfo {
	return []tools.AgentInfo{{ID: "assistant", Name: "Assistant"}}
}

func TestRuntimeRunRetriesTransientToolFailure(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "web_fetch"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "web_fetch",
					Input: json.RawMessage(`{"url":"https://example.com"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "fetched"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&sequenceTool{
		name: "web_fetch",
		results: []tools.ToolResult{
			{Error: "dial tcp 127.0.0.1:443: connection refused"},
			{Output: "ok"},
		},
	})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
		ToolRecovery: ToolRecoveryConfig{
			MaxAttempts:    2,
			RetryBackoffMS: 1,
			LoopDetection: ToolLoopDetectionConfig{
				Enabled:          true,
				HistorySize:      8,
				WarningThreshold: 4,
				BlockThreshold:   6,
			},
		},
	}

	events, err := rt.Run(context.Background(), "fetch once", nil)
	require.NoError(t, err)

	var gotToolRetry bool
	var gotDone bool
	for e := range events {
		switch e.Type {
		case EventToolRetry:
			gotToolRetry = true
			require.NotNil(t, e.ToolRetry)
			assert.Equal(t, "web_fetch", e.ToolRetry.ToolName)
			assert.Equal(t, 2, e.ToolRetry.Attempt)
			assert.Equal(t, "network_error", e.ToolRetry.ErrorClass)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.True(t, gotToolRetry)
	assert.True(t, gotDone)

	history := sess.History()
	require.GreaterOrEqual(t, len(history), 3)
	tr := mustLastToolResultData(t, history)
	assert.Equal(t, "ok", tr.Output)
	assert.Equal(t, float64(1), tr.Metadata["retry_count"])
	assert.Equal(t, "completed_after_retry", tr.Metadata["decision"])
}

func TestRuntimeRunRetriesTransientToolFailureWithDefaultRecovery(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "web_search"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "web_search",
					Input: json.RawMessage(`{"query":"anyai"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "searched"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	search := &sequenceTool{
		name: "web_search",
		results: []tools.ToolResult{
			{Error: "search failed: context deadline exceeded"},
			{Output: "ok"},
		},
	}
	reg := tools.NewRegistry()
	reg.Register(search)

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "search once", nil)
	require.NoError(t, err)

	var gotToolRetry bool
	for e := range events {
		switch e.Type {
		case EventToolRetry:
			gotToolRetry = true
			require.NotNil(t, e.ToolRetry)
			assert.Equal(t, "web_search", e.ToolRetry.ToolName)
			assert.Equal(t, 2, e.ToolRetry.Attempt)
			assert.Equal(t, "timeout", e.ToolRetry.ErrorClass)
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.True(t, gotToolRetry)
	assert.Equal(t, 2, search.Calls())
	tr := mustLastToolResultData(t, sess.History())
	assert.Equal(t, "ok", tr.Output)
	assert.Equal(t, float64(2), tr.Metadata["max_attempts"])
	assert.Equal(t, float64(1), tr.Metadata["retry_count"])
	assert.Equal(t, "completed_after_retry", tr.Metadata["decision"])
}

func TestRuntimeRunShapesNonRetryableToolFailureForNextTurn(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"."}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "handled"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&sequenceTool{
		name: "read_file",
		results: []tools.ToolResult{
			{Error: "failed to read file: read .: is a directory"},
		},
	})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
		ToolRecovery: ToolRecoveryConfig{
			MaxAttempts:    3,
			RetryBackoffMS: 1,
			LoopDetection: ToolLoopDetectionConfig{
				Enabled:          true,
				HistorySize:      8,
				WarningThreshold: 4,
				BlockThreshold:   6,
			},
		},
	}

	events, err := rt.Run(context.Background(), "read dir", nil)
	require.NoError(t, err)

	var gotToolRetry bool
	for e := range events {
		switch e.Type {
		case EventToolRetry:
			gotToolRetry = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}
	assert.False(t, gotToolRetry)

	requests := provider.Requests()
	require.Len(t, requests, 2)
	var foundRecovery bool
	for _, msg := range requests[1].Messages {
		if msg.ToolCallID == "tc_1" {
			foundRecovery = true
			assert.Contains(t, msg.Content, "status: failed")
			assert.Contains(t, msg.Content, "error type: path_is_directory")
			assert.Contains(t, msg.Content, "next step: inspect this failure and choose a corrected follow-up call yourself.")
			assert.Contains(t, msg.Content, "suggested next steps:")
		}
	}
	assert.True(t, foundRecovery)

	tr := mustLastToolResultData(t, sess.History())
	assert.Equal(t, "path_is_directory", tr.Metadata["error_class"])
	assert.Equal(t, false, tr.Metadata["auto_retryable"])
	assert.Equal(t, true, tr.Metadata["model_recoverable"])
}

func TestFormatToolResultContentOmitsRecoveryBlockForRegularSuccessMetadata(t *testing.T) {
	content := formatToolResultContent(session.ToolResultData{
		Output: "done",
		Metadata: map[string]any{
			"http_status": 200,
			"duration_ms": 18,
		},
	})

	assert.Equal(t, "done", content)
	assert.NotContains(t, content, "status:")
}

func TestRuntimeRunWarnsAndBlocksRepeatedNoProgressToolCalls(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "task_status"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "task_status",
					Input: json.RawMessage(`{"task_id":"t_1"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_2", Name: "task_status"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_2",
					Name:  "task_status",
					Input: json.RawMessage(`{"task_id":"t_1"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_3", Name: "task_status"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_3",
					Name:  "task_status",
					Input: json.RawMessage(`{"task_id":"t_1"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "stopped"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	repeatingTool := &sequenceTool{
		name: "task_status",
		results: []tools.ToolResult{
			{Output: `{"status":"running"}`},
			{Output: `{"status":"running"}`},
		},
	}
	reg.Register(repeatingTool)

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  6,
		ToolRecovery: ToolRecoveryConfig{
			MaxAttempts:    1,
			RetryBackoffMS: 1,
			LoopDetection: ToolLoopDetectionConfig{
				Enabled:          true,
				HistorySize:      8,
				WarningThreshold: 2,
				BlockThreshold:   3,
			},
		},
	}

	events, err := rt.Run(context.Background(), "poll task", nil)
	require.NoError(t, err)

	var warningCount int
	var blocked bool
	for e := range events {
		switch e.Type {
		case EventToolWarning:
			warningCount++
			require.NotNil(t, e.ToolWarning)
			if e.ToolWarning.Blocked {
				blocked = true
			}
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.GreaterOrEqual(t, warningCount, 2)
	assert.True(t, blocked)
	assert.Equal(t, 2, repeatingTool.Calls())

	tr := mustLastToolResultData(t, sess.History())
	warning, ok := tr.Metadata["warning"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, warning["blocked"])
	assert.Equal(t, "loop_detected", tr.Metadata["error_class"])
}

func TestRuntimeRunAppliesPromptAndAgentEndHooks(t *testing.T) {
	provider := &capturingLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	var endCalled bool

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  3,
		Hooks: RuntimeHooks{
			BeforePromptBuild: []BeforePromptBuildHook{
				func(_ context.Context, state PromptBuildState) (PromptBuildState, error) {
					state.StaticPrompt.Agent.Instructions = strings.TrimSpace(state.StaticPrompt.Agent.Instructions + "\nHOOK_PROMPT")
					return state, nil
				},
			},
			BeforeModelResolve: []BeforeModelResolveHook{
				func(_ context.Context, state ModelResolveState) (ModelResolveState, error) {
					state.Request.SystemPrompt += "\nMODEL_RESOLVE_MARK"
					return state, nil
				},
			},
			AgentEnd: []AgentEndHook{
				func(_ context.Context, state AgentEndState) error {
					endCalled = state.FinalEvent.Type == EventDone
					return nil
				},
			},
		},
	}

	text, err := rt.RunSync(context.Background(), "hi", nil)
	require.NoError(t, err)
	assert.Equal(t, "done", text)

	requests := provider.Requests()
	require.Len(t, requests, 1)
	assert.Contains(t, requests[0].SystemPrompt, "HOOK_PROMPT")
	assert.Contains(t, requests[0].SystemPrompt, "MODEL_RESOLVE_MARK")
	assert.True(t, endCalled)
}

func TestRuntimeRunAppliesToolHooksAndShapesResult(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "handled"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	tool := &capturingTool{
		name:   "read_file",
		result: tools.ToolResult{Output: "hello"},
	}
	reg.Register(tool)

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  4,
		Hooks: RuntimeHooks{
			BeforeToolCall: []BeforeToolCallHook{
				func(_ context.Context, state BeforeToolCallResult) (BeforeToolCallResult, error) {
					state.State.Call.Input = json.RawMessage(`{"path":"/repo/README.md"}`)
					return state, nil
				},
			},
			AfterToolCall: []AfterToolCallHook{
				func(_ context.Context, state AfterToolCallState) (AfterToolCallState, error) {
					if state.Result.Metadata == nil {
						state.Result.Metadata = map[string]any{}
					}
					state.Result.Metadata["after_hook"] = true
					return state, nil
				},
			},
			ToolResultShape: []ToolResultShapeHook{
				func(_ context.Context, state ToolResultShapeState) (ToolResultShapeState, error) {
					if state.Result.Metadata == nil {
						state.Result.Metadata = map[string]any{}
					}
					state.Result.Metadata["shape_hook"] = true
					return state, nil
				},
			},
		},
	}

	events, err := rt.Run(context.Background(), "read file", nil)
	require.NoError(t, err)
	for e := range events {
		if e.Type == EventError {
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.JSONEq(t, `{"path":"/repo/README.md"}`, tool.LastInput())
	tr := mustLastToolResultData(t, sess.History())
	assert.Equal(t, true, tr.Metadata["after_hook"])
	assert.Equal(t, true, tr.Metadata["shape_hook"])
}

func TestRuntimeRunAppendsSyntheticGuardResultWhenToolPhaseFailsBeforeResult(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "bash"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"pwd"}`),
				}},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "bash", output: "/repo"})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  2,
		Hooks: RuntimeHooks{
			BeforeToolCall: []BeforeToolCallHook{
				func(_ context.Context, state BeforeToolCallResult) (BeforeToolCallResult, error) {
					return state, errors.New("before_tool_call failed")
				},
			},
		},
	}

	events, err := rt.Run(context.Background(), "run pwd", nil)
	require.NoError(t, err)

	var gotErr bool
	for e := range events {
		if e.Type == EventError {
			gotErr = true
			assert.Contains(t, e.Error.Error(), "before_tool_call failed")
		}
	}
	assert.True(t, gotErr)

	tr := mustLastToolResultData(t, sess.History())
	assert.Equal(t, true, tr.Metadata["fatal_execution_err"])
	assert.Equal(t, "failed", tr.Metadata["decision"])
	assert.Equal(t, "unknown", tr.Metadata["error_class"])
}

func TestRuntimeRunSupportsCustomLoopDetectHook(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "bash"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"pwd"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "changed plan"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	tool := &capturingTool{name: "bash", result: tools.ToolResult{Output: "/repo"}}
	reg.Register(tool)

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  3,
		ToolRecovery: ToolRecoveryConfig{
			MaxAttempts:    1,
			RetryBackoffMS: 1,
			LoopDetection: ToolLoopDetectionConfig{
				Enabled:          true,
				HistorySize:      8,
				WarningThreshold: 4,
				BlockThreshold:   6,
			},
		},
		Hooks: RuntimeHooks{
			LoopDetect: []LoopDetectHook{
				func(_ context.Context, state LoopDetectState) (*ToolWarningInfo, error) {
					return &ToolWarningInfo{
						ToolName: state.Call.Name,
						Detector: "custom_guard",
						Count:    1,
						Message:  "custom loop block",
						Blocked:  true,
					}, nil
				},
			},
		},
	}

	events, err := rt.Run(context.Background(), "run pwd", nil)
	require.NoError(t, err)

	var gotWarning bool
	for e := range events {
		switch e.Type {
		case EventToolWarning:
			gotWarning = true
			assert.Equal(t, "custom_guard", e.ToolWarning.Detector)
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}
	assert.Equal(t, 0, tool.Calls())

	tr := mustToolResultDataForCall(t, sess.History(), "tc_1")
	warning, ok := tr.Metadata["warning"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "custom_guard", warning["detector"])
	assert.True(t, gotWarning || warning["detector"] == "custom_guard")
}

func TestRuntimeRunAppliesCompactionHooks(t *testing.T) {
	mock := &mockLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	for i := 0; i < 6; i++ {
		sess.Append(session.UserMessageEntry("question " + string(rune('0'+i))))
		sess.Append(session.AssistantMessageEntry("answer " + string(rune('0'+i))))
	}

	var beforeCalled bool
	var afterCalled bool
	rt := &Runtime{
		LLM:                       mock,
		Tools:                     tools.NewRegistry(),
		Session:                   sess,
		Model:                     "mock-model",
		Workspace:                 t.TempDir(),
		MaxTurns:                  2,
		SessionCompactThreshold:   8,
		SessionCompactKeepEntries: 4,
		Hooks: RuntimeHooks{
			BeforeCompaction: []BeforeCompactionHook{
				func(_ context.Context, state CompactionState) (CompactionState, error) {
					beforeCalled = true
					state.Summary = "HOOK SUMMARY"
					return state, nil
				},
			},
			AfterCompaction: []AfterCompactionHook{
				func(_ context.Context, state CompactionState) error {
					afterCalled = strings.TrimSpace(state.Summary) == "HOOK SUMMARY"
					return nil
				},
			},
		},
	}

	text, err := rt.RunSync(context.Background(), "compact now", nil)
	require.NoError(t, err)
	assert.Equal(t, "done", text)
	assert.True(t, beforeCalled)
	assert.True(t, afterCalled)

	history := sess.History()
	require.NotEmpty(t, history)
	assert.Equal(t, session.EntryTypeCompaction, history[0].Type)
	var compact session.CompactionData
	require.NoError(t, json.Unmarshal(history[0].Data, &compact))
	assert.Contains(t, compact.Text, "HOOK SUMMARY")
}

func TestRuntimeRunSync(t *testing.T) {
	mock := &mockLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "Hello "},
			{Type: llm.EventTextDelta, Text: "world!"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()

	rt := &Runtime{
		LLM:       mock,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "hi", nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello world!", text)
}

func TestRuntimeRunContinuesWhenGoalStillHasOpenTodos(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_goal",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	todoID := stateStore.AddTodo(context.Background(), "Close the runtime todo")
	require.NotEmpty(t, todoID)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventTextDelta, Text: "done for now"},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_todo", Name: "todo"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_todo",
					Name:  "todo",
					Input: json.RawMessage(`{"action":"complete","item":"` + todoID + `"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "actually finished"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.TodoTool{Store: stateStore})

	var endCalls int
	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
		Hooks: RuntimeHooks{
			AgentEnd: []AgentEndHook{
				func(_ context.Context, state AgentEndState) error {
					endCalls++
					assert.Equal(t, EventDone, state.FinalEvent.Type)
					return nil
				},
			},
		},
	}

	events, err := rt.Run(context.Background(), "finish the task", nil)
	require.NoError(t, err)

	var text strings.Builder
	for event := range events {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Text)
		case EventError:
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	assert.Equal(t, 3, callCount)
	assert.Equal(t, 1, endCalls)
	assert.Equal(t, "done for nowactually finished", text.String())

	todos := stateStore.ListTodos(context.Background())
	require.Len(t, todos, 1)
	assert.Equal(t, "completed", todos[0].Status)

	history := sess.History()
	assert.True(t, sessionHasRuntimeControlText(t, history, "[Runtime goal continuation]"))
	assert.True(t, sessionHasRuntimeControlText(t, history, "[Runtime goal continuation]", todoID))
}

func TestRuntimeRunAutoCompletesAfterVisibleReplyFollowingToolWork(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_read", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_read",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "finished"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := newStaticExecutor(&capturingTool{
		name:   "read_file",
		result: tools.ToolResult{Output: "README contents"},
	})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "finish after reading", nil)
	require.NoError(t, err)

	var text strings.Builder
	for event := range events {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Text)
		case EventError:
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	assert.Equal(t, "finished", text.String())
	assert.Equal(t, 2, callCount)

	history := sess.History()
	var acceptedCompletion bool
	for _, entry := range history {
		switch entry.Type {
		case session.EntryTypeToolResult:
			var data session.ToolResultData
			require.NoError(t, json.Unmarshal(entry.Data, &data))
			if data.ToolCallID == "tc_auto_goal_complete_1" &&
				strings.Contains(data.Output, "marked as completed") {
				acceptedCompletion = true
			}
		}
	}
	assert.False(t, sessionHasRuntimeControlText(t, history,
		"[Runtime goal continuation]",
		"Call `goal_complete` now instead of ending silently.",
	))
	assert.False(t, acceptedCompletion)
}

func TestRuntimeRunRequestsVisibleReplyAfterToolOnlyWork(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_read", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_read",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "I read README.md and found the project summary."},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := newStaticExecutor(&capturingTool{
		name:   "read_file",
		result: tools.ToolResult{Output: "README contents"},
	})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "inspect and summarize", nil)
	require.NoError(t, err)
	assert.Equal(t, "I read README.md and found the project summary.", text)
	assert.Equal(t, 3, callCount)

	requests := provider.Requests()
	require.Len(t, requests, 3)
	require.NotEmpty(t, requests[2].Messages)
	last := requests[2].Messages[len(requests[2].Messages)-1]
	assert.Equal(t, llm.MessageRoleRuntime, last.Role)
	assert.Contains(t, last.Content, "[Runtime visible reply required]")
}

func TestRuntimeRunFailsAfterRepeatedEmptyVisibleReplyRequest(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_read", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_read",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := newStaticExecutor(&capturingTool{
		name:   "read_file",
		result: tools.ToolResult{Output: "README contents"},
	})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "inspect and summarize", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "visible reply")
	assert.Empty(t, text)
	assert.Equal(t, 3, callCount)
}

func TestRuntimeGoalCompleteToolRejectsUntilObjectiveWorkIsCleared(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_goal_complete",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	todoID := stateStore.AddTodo(context.Background(), "Close the runtime todo")
	require.NotEmpty(t, todoID)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_goal_1", Name: "goal_complete"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_goal_1",
					Name:  "goal_complete",
					Input: json.RawMessage(`{}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_todo", Name: "todo"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_todo",
					Name:  "todo",
					Input: json.RawMessage(`{"action":"complete","item":"` + todoID + `"}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_goal_2", Name: "goal_complete"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_goal_2",
					Name:  "goal_complete",
					Input: json.RawMessage(`{}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "finished"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.TodoTool{Store: stateStore})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  6,
	}

	text, err := rt.RunSync(context.Background(), "finish the task", nil)
	require.NoError(t, err)
	assert.Equal(t, "finished", text)
	assert.Equal(t, 4, callCount)

	requests := provider.Requests()
	require.NotEmpty(t, requests)
	assert.Contains(t, requests[0].SystemPrompt, "## Goal Completion Rules")
	assert.Contains(t, requests[0].SystemPrompt, "## Goal State")
	assert.Contains(t, requests[0].SystemPrompt, "Close the runtime todo")

	var sawGoalTool bool
	for _, def := range requests[0].Tools {
		if def.Name == "goal_complete" {
			sawGoalTool = true
			break
		}
	}
	assert.True(t, sawGoalTool)

	var rejectedCompletion bool
	var acceptedCompletion bool
	for _, entry := range sess.History() {
		if entry.Type != session.EntryTypeToolResult {
			continue
		}
		var data session.ToolResultData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		switch data.ToolCallID {
		case "tc_goal_1":
			rejectedCompletion = strings.Contains(data.Error, "unfinished work")
		case "tc_goal_2":
			acceptedCompletion = strings.Contains(data.Output, "marked as completed")
		}
	}
	assert.True(t, rejectedCompletion)
	assert.True(t, acceptedCompletion)
	assert.True(t, sessionHasRuntimeControlText(t, sess.History(), "[Runtime visible reply required]"))
}

func TestRuntimeRunHardStopSummarizesBeforeFailing(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "当前已确认存在未完成事项。下一步建议：启动新的 run，继续完成剩余 todo。"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	sess.Append(session.TodoEntry(session.TodoData{
		ID:        "todo_1",
		Content:   "Close the runtime todo",
		Status:    "open",
		CreatedAt: time.Now().Unix(),
		RunID:     "run_hardstop",
	}))

	rt := &Runtime{
		LLM:       provider,
		Tools:     tools.NewRegistry(),
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  1,
	}

	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:     "run_hardstop",
		SessionID: "test-key",
	})
	events, err := rt.Run(ctx, "finish the task", nil)
	require.NoError(t, err)

	var text strings.Builder
	var finalErr error
	for event := range events {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Text)
		case EventError:
			finalErr = event.Error
		}
	}

	require.Error(t, finalErr)
	assert.Contains(t, finalErr.Error(), "goal forced to stop")
	assert.Contains(t, finalErr.Error(), "reached max turns")
	assert.Contains(t, text.String(), "下一步建议")
	assert.Equal(t, 2, callCount)

	requests := provider.Requests()
	require.Len(t, requests, 2)
	assert.Len(t, requests[1].Tools, 0)
	require.NotEmpty(t, requests[1].Messages)
	assert.Contains(t, requests[1].Messages[len(requests[1].Messages)-1].Content, "[Runtime hard stop]")

	history := sess.History()
	assert.True(t, sessionHasRuntimeControlText(t, history, "[Runtime hard stop]"))
}

func TestRuntimeRunAutoExecutesStructuredPlanReadyStep(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_plan_auto",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	planInput, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"description": "Auto execute the ready step",
			"state":       "running",
			"steps": []map[string]any{
				{
					"id":          "step_readme",
					"description": "Read the README",
					"state":       "pending",
					"action": map[string]any{
						"type":   "tool",
						"target": "read_file",
						"input":  `{"path":"README.md"}`,
					},
				},
			},
		},
	})
	require.NoError(t, err)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_plan", Name: "update_plan"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_plan",
					Name:  "update_plan",
					Input: planInput,
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "plan recorded"},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_goal_done", Name: "goal_complete"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_goal_done",
					Name:  "goal_complete",
					Input: json.RawMessage(`{}`),
				}},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.UpdatePlanTool{Store: stateStore})
	reg.Register(&capturingTool{
		name:   "read_file",
		result: tools.ToolResult{Output: "README contents"},
	})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  6,
	}

	events, err := rt.Run(context.Background(), "follow the structured plan", nil)
	require.NoError(t, err)
	for event := range events {
		if event.Type == EventError {
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	assert.Equal(t, 2, callCount)
	currentPlan, ok := stateStore.GetStructuredPlan()
	require.True(t, ok)
	require.Len(t, currentPlan.Steps, 1)
	assert.Equal(t, runtimeplan.PlanStateCompleted, currentPlan.State)
	assert.Equal(t, runtimeplan.StepStateCompleted, currentPlan.Steps[0].State)
	assert.Equal(t, "README contents", currentPlan.Steps[0].Output)

	history := sess.History()
	var sawRuntimePlanMeta bool
	var sawReadToolCall bool
	for _, entry := range history {
		switch entry.Type {
		case session.EntryTypeMeta:
			var data session.MessageData
			require.NoError(t, json.Unmarshal(entry.Data, &data))
			if strings.Contains(data.Text, "[Runtime plan execution]") {
				sawRuntimePlanMeta = true
			}
		case session.EntryTypeToolCall:
			var data session.ToolCallData
			require.NoError(t, json.Unmarshal(entry.Data, &data))
			if data.Tool == "read_file" {
				sawReadToolCall = true
			}
		}
	}
	assert.True(t, sawRuntimePlanMeta)
	assert.True(t, sawReadToolCall)
}

func TestRuntimeRunAutoExecutesStructuredUserInputStep(t *testing.T) {
	callCount := 0
	sess := session.NewSession("test-agent", "test-key")
	stateStore := runtimesessionstate.NewStateStore(sess, nil, runtimeevents.RunRecord{
		ID:        "run_plan_user_input",
		AgentID:   "test-agent",
		SessionID: "test-key",
	})
	planInput, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"description": "Need one missing fact",
			"state":       "running",
			"steps": []map[string]any{
				{
					"id":          "step_question",
					"description": "Ask for the deployment target",
					"state":       "pending",
					"action": map[string]any{
						"type":  "user_input",
						"input": "Which deployment target should I use?",
					},
				},
			},
		},
	})
	require.NoError(t, err)

	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_plan", Name: "update_plan"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_plan",
					Name:  "update_plan",
					Input: planInput,
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "plan recorded"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	rt := &Runtime{
		LLM:       provider,
		Tools:     tools.NewRegistry(),
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}
	rt.Tools.(*tools.Registry).Register(&tools.UpdatePlanTool{Store: stateStore})

	var text strings.Builder
	events, err := rt.Run(context.Background(), "follow the structured plan", nil)
	require.NoError(t, err)
	for event := range events {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Text)
		case EventError:
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	assert.Equal(t, 2, callCount)
	assert.Contains(t, text.String(), "Which deployment target should I use?")
	currentPlan, ok := stateStore.GetStructuredPlan()
	require.True(t, ok)
	assert.Equal(t, runtimeplan.PlanStatePaused, currentPlan.State)
	require.Len(t, currentPlan.Steps, 1)
	assert.Equal(t, runtimeplan.StepStatePending, currentPlan.Steps[0].State)
}

func TestRuntimeDefaultMaxTurns(t *testing.T) {
	assert.Equal(t, 10000, defaultRuntimeMaxTurns)
}

func TestRuntimeRunCompactsLongSessionIntoRollingSummary(t *testing.T) {
	provider := &capturingLLMProvider{
		compactSummary: "Provider compact summary",
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	for i := 0; i < 8; i++ {
		sess.Append(session.UserMessageEntry("user turn " + string(rune('0'+i))))
		sess.Append(session.AssistantMessageEntry("assistant turn " + string(rune('0'+i))))
	}
	recorder := runtimeevents.NewRecorder()

	rt := &Runtime{
		LLM:                       provider,
		Tools:                     tools.NewRegistry(),
		Session:                   sess,
		Model:                     "mock-model",
		Workspace:                 t.TempDir(),
		MaxTurns:                  5,
		SessionCompactThreshold:   8,
		SessionCompactKeepEntries: 4,
		EventAppender:             recorder.AppendEvent,
	}

	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:     "run_compact",
		AgentID:   "test-agent",
		SessionID: sess.ID,
	})
	events, err := rt.Run(ctx, "latest question", nil)
	require.NoError(t, err)
	for range events {
	}

	compactRequests := provider.CompactRequests()
	require.Len(t, compactRequests, 1)
	assert.Equal(t, "mock-model", compactRequests[0].Model)
	assert.Contains(t, compactRequests[0].SystemPrompt, "checkpoint handoff summary")
	assert.Contains(t, compactRequests[0].UserPrompt, "Create a handoff summary")

	requests := provider.Requests()
	require.Len(t, requests, 1)
	joined := requests[0].SystemPrompt + "\n" + func() string {
		var contents []string
		for _, msg := range requests[0].Messages {
			contents = append(contents, msg.Content)
		}
		return strings.Join(contents, "\n")
	}()
	assert.NotContains(t, requests[0].SystemPrompt, "Session summary context:")
	assert.Contains(t, joined, compactionSummaryTag)
	assert.Contains(t, joined, "Provider compact summary")
	assert.NotContains(t, joined, "user turn 0")
	assert.Contains(t, joined, "latest question")

	history := sess.History()
	require.NotEmpty(t, history)
	assert.Equal(t, session.EntryTypeCompaction, history[0].Type)

	runEvents := recorder.ListRunEvents("run_compact")
	require.Len(t, runEvents, 2)
	assert.Equal(t, runtimeevents.EventSessionCompactRequested, runEvents[0].Name)
	assert.Equal(t, runtimeevents.EventSessionCompactCompleted, runEvents[1].Name)
	assert.Equal(t, "mock", runEvents[1].Payload["provider"])
	assert.Equal(t, string(llm.CompactCapabilityChatFallbackOnly), runEvents[1].Payload["compact_capability"])
	assert.Equal(t, string(llm.CompactStrategyChatFallback), runEvents[1].Payload["compact_strategy"])
	assert.Equal(t, "provider_model", runEvents[1].Payload["summary_source"])
	assert.Equal(t, "Provider compact summary", runEvents[1].Payload["summary"])
}

func TestRuntimeRunCompactionPreservesProtectedSystemPrefix(t *testing.T) {
	provider := &capturingLLMProvider{
		compactSummary: "Protected-prefix compact summary",
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	sess.Append(session.MetaEntry("Pinned system session context"))
	for i := 0; i < 8; i++ {
		sess.Append(session.UserMessageEntry("user turn " + string(rune('0'+i))))
		sess.Append(session.AssistantMessageEntry("assistant turn " + string(rune('0'+i))))
	}

	rt := &Runtime{
		LLM:                       provider,
		Tools:                     tools.NewRegistry(),
		Session:                   sess,
		Model:                     "mock-model",
		SystemPrompt:              "Runtime system prompt should stay outside compaction",
		Workspace:                 t.TempDir(),
		MaxTurns:                  5,
		SessionCompactThreshold:   8,
		SessionCompactKeepEntries: 4,
	}

	events, err := rt.Run(context.Background(), "latest question", nil)
	require.NoError(t, err)
	for range events {
	}

	compactRequests := provider.CompactRequests()
	require.Len(t, compactRequests, 1)
	assert.Equal(t, compactionSystemPrompt, compactRequests[0].SystemPrompt)
	assert.NotContains(t, compactRequests[0].SystemPrompt, rt.SystemPrompt)

	var compactedInput []string
	for _, msg := range compactRequests[0].Messages {
		compactedInput = append(compactedInput, msg.Content)
	}
	assert.NotContains(t, strings.Join(compactedInput, "\n"), "Pinned system session context")
	assert.Equal(t, "Runtime system prompt should stay outside compaction", rt.SystemPrompt)

	history := sess.History()
	require.GreaterOrEqual(t, len(history), 2)
	assert.Equal(t, session.EntryTypeMeta, history[0].Type)
	assert.Equal(t, "system", history[0].Role)
	var meta session.MessageData
	require.NoError(t, json.Unmarshal(history[0].Data, &meta))
	assert.Equal(t, "Pinned system session context", meta.Text)
	assert.Equal(t, session.EntryTypeCompaction, history[1].Type)
}

func TestRuntimeRunCompactionRetriesOnceAfterProviderFailure(t *testing.T) {
	provider := &capturingLLMProvider{
		compactErrors: []error{
			errors.New("compaction returned empty summary"),
		},
		compactResults: []llm.CompactResponse{
			{},
			{
				Summary:  "Recovered compact summary",
				Strategy: llm.CompactStrategyChatFallback,
			},
		},
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	for i := 0; i < 8; i++ {
		sess.Append(session.UserMessageEntry("user turn " + string(rune('0'+i))))
		sess.Append(session.AssistantMessageEntry("assistant turn " + string(rune('0'+i))))
	}
	recorder := runtimeevents.NewRecorder()

	rt := &Runtime{
		LLM:                       provider,
		Tools:                     tools.NewRegistry(),
		Session:                   sess,
		Model:                     "mock-model",
		Workspace:                 t.TempDir(),
		MaxTurns:                  5,
		LLMRetryBackoffMS:         1,
		SessionCompactThreshold:   8,
		SessionCompactKeepEntries: 4,
		EventAppender:             recorder.AppendEvent,
	}

	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:     "run_compact_retry",
		AgentID:   "test-agent",
		SessionID: sess.ID,
	})
	events, err := rt.Run(ctx, "latest question", nil)
	require.NoError(t, err)
	for event := range events {
		if event.Type == EventError {
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	compactRequests := provider.CompactRequests()
	require.Len(t, compactRequests, 2)

	history := sess.History()
	require.NotEmpty(t, history)
	assert.Equal(t, session.EntryTypeCompaction, history[0].Type)

	runEvents := recorder.ListRunEvents("run_compact_retry")
	require.Len(t, runEvents, 2)
	assert.Equal(t, runtimeevents.EventSessionCompactRequested, runEvents[0].Name)
	assert.Equal(t, runtimeevents.EventSessionCompactCompleted, runEvents[1].Name)
	assert.Equal(t, "Recovered compact summary", runEvents[1].Payload["summary"])
}

func TestRuntimeRunRefreshesMemoryRecallQueryFromLatestContext(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "inspect_status"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "inspect_status",
					Input: json.RawMessage(`{}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "done"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	memMgr := memory.NewManager(t.TempDir())
	require.NoError(t, memMgr.Load())
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "beta-launch", "# Beta Launch\n\nBeta launch is blocked on staging review."))

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "inspect_status", output: "Beta launch is blocked on staging review."})

	rt := &Runtime{
		LLM:            provider,
		Tools:          reg,
		Session:        sess,
		Model:          "mock-model",
		Workspace:      t.TempDir(),
		MaxTurns:       5,
		Memory:         memMgr,
		MemoryMaxItems: 2,
	}

	events, err := rt.Run(context.Background(), "Give me a status update.", nil)
	require.NoError(t, err)
	for range events {
	}

	requests := provider.Requests()
	require.Len(t, requests, 2)
	assert.NotContains(t, requests[0].SystemPrompt, "Beta Launch")
	assert.Contains(t, requests[1].SystemPrompt, "Beta Launch")
	assert.Contains(t, requests[1].SystemPrompt, "Beta launch is blocked on staging review.")
}

func TestRuntimeRunRefreshesSkillMatchingFromLatestContext(t *testing.T) {
	callCount := 0
	provider := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "inspect_status"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "inspect_status",
					Input: json.RawMessage(`{}`),
				}},
				{Type: llm.EventDone},
			},
			{
				{Type: llm.EventTextDelta, Text: "done"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "inspect_status", output: "Use the beta-rollout procedure before publishing."})
	reg.Register(&mockTool{name: "skill_get", output: "full skill"})

	loader := skill.NewLoaderFromSkills([]skill.Skill{
		{
			Name:        "beta-rollout",
			Description: "Guide for beta rollout readiness and staging checks",
			Body:        "Verify staging readiness, rollout gating, and release comms.",
			Tags:        []string{"beta", "rollout"},
		},
	})

	rt := &Runtime{
		LLM:       provider,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
		Skills:    loader,
	}

	events, err := rt.Run(context.Background(), "给我一个状态更新。", nil)
	require.NoError(t, err)
	for range events {
	}

	requests := provider.Requests()
	require.Len(t, requests, 2)
	assert.NotContains(t, requests[0].SystemPrompt, "beta-rollout")
	assert.Contains(t, requests[1].SystemPrompt, "## Relevant Skills")
	assert.Contains(t, requests[1].SystemPrompt, "### beta-rollout")
	assert.Contains(t, requests[1].SystemPrompt, "- Summary: Guide for beta rollout readiness and staging checks")
	assert.Contains(t, requests[1].SystemPrompt, "call `skill_get`")
	assert.Contains(t, requests[1].SystemPrompt, "If exactly one skill is clearly relevant")
	assert.Contains(t, requests[1].SystemPrompt, "Do not preload multiple skills just in case")
	assert.NotContains(t, requests[1].SystemPrompt, "Verify staging readiness, rollout gating, and release comms.")
}

// --- Helpers ---

// statefulMockLLMProvider returns different responses on successive calls.
type statefulMockLLMProvider struct {
	responses [][]llm.ChatEvent
	callCount *int

	mu               sync.Mutex
	requests         []llm.ChatRequest
	autoGoalFinalize bool
	autoGoalSeq      int
	autoGoalComplete bool
}

func (m *statefulMockLLMProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if m.autoGoalComplete {
		if events, ok := autoGoalCompletionResponse(req, nil, &m.autoGoalFinalize, &m.autoGoalSeq); ok {
			return staticEventStream(events), nil
		}
	}

	m.mu.Lock()
	idx := *m.callCount
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	events := m.responses[idx]
	if m.autoGoalComplete {
		if synthetic, ok := autoGoalCompletionResponse(req, events, &m.autoGoalFinalize, &m.autoGoalSeq); ok {
			m.mu.Unlock()
			return staticEventStream(synthetic), nil
		}
	}
	m.requests = append(m.requests, req)
	*m.callCount++
	m.mu.Unlock()
	ch := make(chan llm.ChatEvent, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (m *statefulMockLLMProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock", Name: "Mock", Provider: "mock"}}
}

func (m *statefulMockLLMProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "stateful compact summary"}, nil
}

func (m *statefulMockLLMProvider) Requests() []llm.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.ChatRequest(nil), m.requests...)
}

func autoGoalCompletionResponse(req llm.ChatRequest, events []llm.ChatEvent, pending *bool, seq *int) ([]llm.ChatEvent, bool) {
	if pending != nil && *pending {
		*pending = false
		return []llm.ChatEvent{{Type: llm.EventDone}}, true
	}
	if !goalCompletionRequired(req) || !goalToolAvailable(req) {
		return nil, false
	}
	if responseContainsGoalTool(events) || responseContainsAnyToolCall(events) {
		return nil, false
	}
	if pending != nil {
		*pending = true
	}
	id := "tc_auto_goal_complete"
	if seq != nil {
		*seq = *seq + 1
		id = fmt.Sprintf("tc_auto_goal_complete_%d", *seq)
	}
	return []llm.ChatEvent{
		{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: id, Name: "goal_complete"}},
		{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{ID: id, Name: "goal_complete", Input: json.RawMessage(`{}`)}},
		{Type: llm.EventDone},
	}, true
}

func goalCompletionRequired(req llm.ChatRequest) bool {
	text := strings.TrimSpace(req.SystemPrompt)
	if strings.Contains(text, "Runtime requirement: do not stop silently. Call `goal_complete`") {
		return true
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "user" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		return strings.Contains(content, "[Runtime goal continuation]") &&
			strings.Contains(content, "Call `goal_complete` now instead of ending silently.")
	}
	return false
}

func goalToolAvailable(req llm.ChatRequest) bool {
	for _, tool := range req.Tools {
		if strings.TrimSpace(tool.Name) == "goal_complete" {
			return true
		}
	}
	return false
}

func responseContainsGoalTool(events []llm.ChatEvent) bool {
	for _, event := range events {
		if event.ToolCall == nil {
			continue
		}
		if strings.TrimSpace(event.ToolCall.Name) == "goal_complete" {
			return true
		}
	}
	return false
}

func responseContainsAnyToolCall(events []llm.ChatEvent) bool {
	for _, event := range events {
		if event.Type == llm.EventToolCallStart || event.Type == llm.EventToolCallDone {
			return true
		}
	}
	return false
}

func staticEventStream(events []llm.ChatEvent) <-chan llm.ChatEvent {
	ch := make(chan llm.ChatEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch
}

func toolResultContentForTest(messages []llm.Message, toolCallID string) string {
	for _, msg := range messages {
		if msg.Role == llm.MessageRoleUser && msg.ToolCallID == toolCallID {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}

// mockTool is a simple tool that returns a canned output.
type mockTool struct {
	name   string
	output string
}

func (t *mockTool) Name() string        { return t.name }
func (t *mockTool) Description() string { return "mock tool" }
func (t *mockTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *mockTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Output: t.output}, nil
}

type mockPlanStore struct {
	mu         sync.Mutex
	structured runtimeplan.Plan
}

func (s *mockPlanStore) UpdateStructuredPlan(plan runtimeplan.Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.structured = runtimeplan.Normalize(plan)
	return nil
}

func (s *mockPlanStore) GetStructuredPlan() (runtimeplan.Plan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.structured, s.structured.ID != ""
}

func (s *mockPlanStore) Plan() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return runtimeplan.Render(s.structured)
}

type sequenceTool struct {
	name    string
	results []tools.ToolResult

	mu    sync.Mutex
	calls int
}

func (t *sequenceTool) Name() string        { return t.name }
func (t *sequenceTool) Description() string { return "sequence tool" }
func (t *sequenceTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *sequenceTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	idx := t.calls
	t.calls++
	if len(t.results) == 0 {
		return tools.ToolResult{}, nil
	}
	if idx >= len(t.results) {
		idx = len(t.results) - 1
	}
	return t.results[idx], nil
}

func (t *sequenceTool) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type capturingTool struct {
	name   string
	result tools.ToolResult

	mu        sync.Mutex
	calls     int
	lastInput string
}

func (t *capturingTool) Name() string        { return t.name }
func (t *capturingTool) Description() string { return "capturing tool" }
func (t *capturingTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *capturingTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.lastInput = string(input)
	return t.result, nil
}

func (t *capturingTool) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func (t *capturingTool) LastInput() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastInput
}

type staticExecutor struct {
	tools map[string]tools.Tool
}

func newStaticExecutor(items ...tools.Tool) *staticExecutor {
	exec := &staticExecutor{tools: make(map[string]tools.Tool, len(items))}
	for _, item := range items {
		if item == nil {
			continue
		}
		exec.tools[item.Name()] = item
	}
	return exec
}

func (e *staticExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	tool, ok := e.Get(name)
	if !ok {
		return tools.ToolResult{}, fmt.Errorf("unknown tool: %q", name)
	}
	return tool.Execute(ctx, input)
}

func (e *staticExecutor) ToolDefs() []llm.ToolDef {
	if e == nil {
		return nil
	}
	defs := make([]llm.ToolDef, 0, len(e.tools))
	for _, tool := range e.tools {
		defs = append(defs, llm.ToolDef{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
		})
	}
	return defs
}

func (e *staticExecutor) Names() []string {
	if e == nil {
		return nil
	}
	names := make([]string, 0, len(e.tools))
	for name := range e.tools {
		names = append(names, name)
	}
	return names
}

func (e *staticExecutor) Get(name string) (tools.Tool, bool) {
	if e == nil {
		return nil, false
	}
	tool, ok := e.tools[name]
	return tool, ok
}

type timedTool struct {
	name    string
	output  string
	waitFor <-chan struct{}
	onStart func()
	delay   time.Duration
}

func (t *timedTool) Name() string        { return t.name }
func (t *timedTool) Description() string { return "timed tool" }
func (t *timedTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *timedTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	if t.onStart != nil {
		t.onStart()
	}
	if t.waitFor != nil {
		select {
		case <-ctx.Done():
			return tools.ToolResult{}, ctx.Err()
		case <-t.waitFor:
		}
	}
	if t.delay > 0 {
		select {
		case <-ctx.Done():
			return tools.ToolResult{}, ctx.Err()
		case <-time.After(t.delay):
		}
	}
	return tools.ToolResult{Output: t.output}, nil
}

func mustLastToolResultData(t *testing.T, history []session.SessionEntry) session.ToolResultData {
	t.Helper()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Type != session.EntryTypeToolResult {
			continue
		}
		var data session.ToolResultData
		require.NoError(t, json.Unmarshal(history[i].Data, &data))
		switch toolNameForCall(history, data.ToolCallID) {
		case "goal_complete", "await_user_input":
			continue
		}
		return data
	}
	t.Fatalf("expected at least one tool result entry")
	return session.ToolResultData{}
}

func mustToolResultDataForCall(t *testing.T, history []session.SessionEntry, toolCallID string) session.ToolResultData {
	t.Helper()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Type != session.EntryTypeToolResult {
			continue
		}
		var data session.ToolResultData
		require.NoError(t, json.Unmarshal(history[i].Data, &data))
		if data.ToolCallID == toolCallID {
			return data
		}
	}
	t.Fatalf("expected tool result entry for %s", toolCallID)
	return session.ToolResultData{}
}

func toolNameForCall(history []session.SessionEntry, toolCallID string) string {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return ""
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Type != session.EntryTypeToolCall {
			continue
		}
		var data session.ToolCallData
		if err := json.Unmarshal(history[i].Data, &data); err != nil {
			continue
		}
		if strings.TrimSpace(data.ID) != toolCallID {
			continue
		}
		return strings.TrimSpace(data.Tool)
	}
	return ""
}
