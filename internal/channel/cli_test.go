package channel

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/gateway"
	runtimesession "github.com/Isites/anyai/internal/runtime/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func executeCLICommand(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch typed := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var msgs []tea.Msg
		for _, child := range typed {
			msgs = append(msgs, executeCLICommand(child)...)
		}
		return msgs
	default:
		return []tea.Msg{typed}
	}
}

func TestParseCLIReferencesNoRefs(t *testing.T) {
	blocks := ParseCLIReferences("hello world")
	assert.Nil(t, blocks)
}

func TestParseCLIReferencesFileRef(t *testing.T) {
	blocks := ParseCLIReferences("check @file:/tmp/test.txt please")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "file", blocks[0].Type)
	assert.Equal(t, "test.txt", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, "test.txt")
}

func TestParseCLIReferencesFileRefNaturalSyntax(t *testing.T) {
	blocks := ParseCLIReferences("check @file /tmp/test.txt please")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "file", blocks[0].Type)
	assert.Equal(t, "test.txt", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, "test.txt")
}

func TestParseCLIReferencesDirRef(t *testing.T) {
	blocks := ParseCLIReferences("scan @dir:/tmp/mydir")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "dir", blocks[0].Type)
	assert.Equal(t, "mydir", blocks[0].Name)
}

func TestParseCLIReferencesDirRefNaturalSyntax(t *testing.T) {
	blocks := ParseCLIReferences("scan @dir /tmp/mydir")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "dir", blocks[0].Type)
	assert.Equal(t, "mydir", blocks[0].Name)
}

func TestParseCLIReferencesURLRef(t *testing.T) {
	blocks := ParseCLIReferences("fetch @url:https://example.com/page")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "url", blocks[0].Type)
	assert.Equal(t, "https://example.com/page", blocks[0].URL)
}

func TestParseCLIReferencesURLRefNaturalSyntax(t *testing.T) {
	blocks := ParseCLIReferences("fetch @url https://example.com/page")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "url", blocks[0].Type)
	assert.Equal(t, "https://example.com/page", blocks[0].URL)
}

func TestParseCLIReferencesMultipleRefs(t *testing.T) {
	blocks := ParseCLIReferences("read @file:/tmp/a.txt and @dir:/tmp/subdir")
	assert.Len(t, blocks, 2)
	assert.Equal(t, "file", blocks[0].Type)
	assert.Equal(t, "dir", blocks[1].Type)
}

func TestParseCLIReferencesEmptyPath(t *testing.T) {
	// @file: with no path after it — should not produce a block
	blocks := ParseCLIReferences("check @file:")
	assert.Nil(t, blocks)
}

func TestCLIModelRendersPersistentRunTreeAndConversation(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_parent_123456",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.started",
		Timestamp: now,
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_parent_123456",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "agent.call.started",
		Timestamp: now.Add(500 * time.Millisecond),
		Payload: map[string]any{
			"target_agent": "researcher",
			"task":         "请调研并返回三条重点",
		},
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "run.started",
		Timestamp:     now.Add(time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "tool.called",
		Timestamp:     now.Add(1500 * time.Millisecond),
		Payload: map[string]any{
			"tool":  "web_search",
			"input": "最新行业动态",
		},
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "memory.recalled",
		Timestamp:     now.Add(1600 * time.Millisecond),
		Payload: map[string]any{
			"query": "最新行业动态",
			"entries": []map[string]any{
				{"id": "episodic/current-focus", "matched_terms": []string{"行业", "动态"}},
			},
		},
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "run.completed",
		Timestamp:     now.Add(2 * time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_parent_123456",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.completed",
		Timestamp: now.Add(3 * time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliAssistantMessageMsg{Text: "已经完成汇总。"})
	model = updated.(*cliModel)

	view := model.View()
	assert.Contains(t, view, "AnyAI")
	assert.Contains(t, view, "Session Window")
	assert.Contains(t, view, "Esc")
	assert.Contains(t, view, "2 done")
	assert.Contains(t, view, "state idle")
	assert.Contains(t, model.renderConversation(), "已经完成汇总")
	assert.NotContains(t, view, "Runtime Inspector")
	assert.NotContains(t, view, "RunTree")
	assert.NotContains(t, view, "Latest Call Stack")
}

func TestCLIModelShowsCompactionCapabilityAndStrategy(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_compact_1",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "session.compact.completed",
		Timestamp: now,
		Payload: map[string]any{
			"provider":           "anthropic",
			"model":              "claude-sonnet-4-6",
			"compact_capability": "native_compact_available",
			"compact_strategy":   "native_compact",
			"keep_entries":       6,
			"history_len_before": 48,
			"history_len_after":  7,
			"summary_source":     "provider_model",
			"summary":            "checkpoint summary",
		},
	}})
	model = updated.(*cliModel)

	traceView := model.renderTrace()
	assert.Contains(t, traceView, "session compaction completed")
	assert.Contains(t, traceView, "native_compact_available")
	assert.Contains(t, traceView, "native_compact")
	assert.Contains(t, traceView, "provider anthropic")
	assert.Contains(t, traceView, "checkpoint summary")
}

func TestCLIModelStartsWithCollapsedInspector(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	assert.True(t, model.inspectorCollapsed)
	trace := model.renderTrace()
	assert.Contains(t, trace, "Latest Call Stack")
	assert.Contains(t, trace, "RunTree")
}

func TestCLIModelStartupCardUsesStatusLabel(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	conversation := model.renderConversation()
	assert.Contains(t, conversation, "STATUS")
	assert.NotContains(t, conversation, "SYSTEM")
}

func TestCLIModelSyncsRootSessionHistoryIntoConversation(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	rootSession, err := store.Load("lead", "cli_local")
	require.NoError(t, err)
	rootSession.Append(runtimesession.UserMessageEntry("拆一下任务"))

	childSession, err := store.Load("researcher", "agentcall_researcher_trace_1")
	require.NoError(t, err)
	childSession.Append(runtimesession.AssistantMessageEntry("子代理内部消息"))

	model := newCLIModel(projectRoot, "lead", func(string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.started",
		Timestamp: time.Now(),
	}})
	model = updated.(*cliModel)
	assert.Contains(t, model.renderConversation(), "拆一下任务")

	rootSession.Append(runtimesession.AssistantMessageEntry("阶段一：先分析测试现状。"))

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_1",
		ParentAgentID: "lead",
		Name:          "run.started",
		Timestamp:     time.Now().Add(time.Second),
	}})
	model = updated.(*cliModel)

	conversation := model.renderConversation()
	assert.Contains(t, conversation, "阶段一：先分析测试现状。")
	assert.NotContains(t, conversation, "子代理内部消息")
}

func TestCLIModelDoesNotDuplicateOutboundAssistantTextWhenSessionAlreadySynced(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	sess, err := store.Load("lead", "cli_local")
	require.NoError(t, err)
	sess.Append(runtimesession.UserMessageEntry("继续"))
	sess.Append(runtimesession.AssistantMessageEntry("最终答复"))

	model := newCLIModel(projectRoot, "lead", func(string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.completed",
		Timestamp: time.Now(),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliAssistantMessageMsg{Text: "最终答复"})
	model = updated.(*cliModel)

	assert.Equal(t, 1, strings.Count(model.renderConversation(), "最终答复"))
}

func TestCLIModelRendersSessionTranscriptCardsForStructuredEntries(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	sess, err := store.Load("lead", "cli_local")
	require.NoError(t, err)
	sess.Append(runtimesession.UserMessageEntry("开始执行"))
	sess.Append(runtimesession.ToolCallEntry("tool_1", "readfile", json.RawMessage(`{"path":"src/app.js"}`)))
	sess.Append(runtimesession.ToolResultEntry("tool_1", "file content", "", nil))
	sess.Append(runtimesession.AssistantMessageEntry("已完成阶段播报"))

	model := newCLIModel(projectRoot, "lead", func(string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.completed",
		Timestamp: time.Now(),
	}})
	model = updated.(*cliModel)

	conversation := model.renderConversation()
	assert.Contains(t, conversation, "USER")
	assert.Contains(t, conversation, "开始执行")
	assert.Contains(t, conversation, "TOOL CALL")
	assert.Contains(t, conversation, "lead -> readfile [success]")
	assert.Contains(t, conversation, "src/app.js")
	assert.NotContains(t, conversation, "TOOL RESULT")
	assert.NotContains(t, conversation, "file content")
	assert.NotContains(t, conversation, "PHASE")
	assert.NotContains(t, conversation, "PROGRESS")
	assert.Contains(t, conversation, "已完成阶段播报")
}

func TestCLIModelSurfacesRuntimePhasesAndSubagentLifecycle(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.started",
		Timestamp: now,
	}})
	model = updated.(*cliModel)
	assert.Contains(t, model.renderTrace(), "thinking")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "text.delta",
		Timestamp: now.Add(500 * time.Millisecond),
		Payload: map[string]any{
			"text": "正在生成答复",
		},
	}})
	model = updated.(*cliModel)
	trace := model.renderTrace()
	assert.Contains(t, trace, "generating")
	assert.Contains(t, trace, "streaming")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "agent.call.started",
		Timestamp: now.Add(time.Second),
		Payload: map[string]any{
			"id":           "deleg_call_1",
			"target_agent": "coder",
			"task":         "实现修复",
		},
	}})
	model = updated.(*cliModel)
	trace = model.renderTrace()
	assert.Contains(t, trace, "subagent active")
	assert.Contains(t, trace, "subagents 1 active / 0 done / 0 failed")
	assert.Contains(t, trace, "waiting on subagent coder")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child",
		AgentID:       "coder",
		SessionID:     "agentcall_coder_trace_status",
		ParentAgentID: "lead",
		Name:          "run.started",
		Timestamp:     now.Add(1500 * time.Millisecond),
	}})
	model = updated.(*cliModel)
	assert.Contains(t, model.renderTrace(), "coder")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child",
		AgentID:       "coder",
		SessionID:     "agentcall_coder_trace_status",
		ParentAgentID: "lead",
		Name:          "run.completed",
		Timestamp:     now.Add(2 * time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "agent.call.completed",
		Timestamp: now.Add(2500 * time.Millisecond),
		Payload: map[string]any{
			"id":           "deleg_call_1",
			"target_agent": "coder",
			"status":       "completed",
			"summary":      "修复已完成",
		},
	}})
	model = updated.(*cliModel)
	trace = model.renderTrace()
	assert.Contains(t, trace, "integrating")
	assert.Contains(t, trace, "subagents 0 active / 1 done / 0 failed")
	assert.Contains(t, trace, "coder finished")
}

func TestCLIModelHeaderAndStatusUseSameRuntimeSummary(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "tech-lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"id":           "call_1",
				"target_agent": "plan-reviewer",
				"task":         "review plan",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(2 * time.Second),
			Payload: map[string]any{
				"id":           "call_2",
				"target_agent": "test-engineer",
				"task":         "verify tests",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	summary := model.runtimeSummary()
	assert.Equal(t, "subagent active", summary.State)
	assert.Equal(t, 1, summary.RunsLive)
	assert.Equal(t, 2, summary.SubagentsActive)

	status := model.statusEntry.text
	assert.Contains(t, status, "state subagent active")
	assert.Contains(t, status, "runs 1 live / 0 done / 0 failed")
	assert.Contains(t, status, "subagents 2 active / 0 done / 0 failed")
	assert.Contains(t, status, "events")

	header := model.renderHeader(140)
	assert.Contains(t, header, "state")
	assert.Contains(t, header, "subagent active")
	assert.Contains(t, header, "runs")
	assert.Contains(t, header, "1 live / 0 done / 0 failed")
	assert.Contains(t, header, "subagents")
	assert.Contains(t, header, "2 active / 0 done / 0 failed")
}

func TestCLIModelSuppressesTransientRunningActivities(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.activity",
			Timestamp: now.Add(200 * time.Millisecond),
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "text.delta",
			Timestamp: now.Add(400 * time.Millisecond),
			Payload: map[string]any{
				"text": "正在生成答复",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.running",
			Timestamp: now.Add(600 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_1",
				"target_agent": "coder",
				"status":       "running",
			},
		},
	}

	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	require.Empty(t, model.activities)
	node := model.traceNodes["run_root"]
	require.NotNil(t, node)
	assert.Equal(t, "text.delta", node.lastEventName)
	assert.Equal(t, "正在生成答复", node.lastEventSummary)
}

func TestWrapCLITextBreaksLongTokens(t *testing.T) {
	wrapped := wrapCLIText(strings.Repeat("a", 35), 10)
	lines := strings.Split(wrapped, "\n")
	assert.Greater(t, len(lines), 1)
	for _, line := range lines {
		assert.LessOrEqual(t, len([]rune(line)), 10)
	}
}

func TestCLIModelCanCollapseInspector(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	initialTraceHeight := model.traceHeight
	assert.True(t, model.inspectorCollapsed)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	assert.True(t, model.inspectorCollapsed)
	assert.Equal(t, initialTraceHeight, model.traceHeight)
	assert.Contains(t, model.View(), "Session Window")
	assert.NotContains(t, model.View(), "Runtime Inspector")
}

func TestCLIModelCanExpandLatestCallStack(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"target_agent": "context-analyst",
				"task":         "读取需求文档并产出分析",
			},
		},
		{
			RunID:         "run_child",
			AgentID:       "context-analyst",
			SessionID:     "agentcall_context",
			ParentAgentID: "tech-lead",
			Name:          "run.started",
			Timestamp:     now.Add(2 * time.Second),
		},
		{
			RunID:         "run_child",
			AgentID:       "context-analyst",
			SessionID:     "agentcall_context",
			ParentAgentID: "tech-lead",
			Name:          "tool.called",
			Timestamp:     now.Add(3 * time.Second),
			Payload: map[string]any{
				"tool":  "read_file",
				"input": "{\"path\":\"workflow-artifacts/user-request.md\"}",
			},
		},
		{
			RunID:         "run_child",
			AgentID:       "context-analyst",
			SessionID:     "agentcall_context",
			ParentAgentID: "tech-lead",
			Name:          "tool.called",
			Timestamp:     now.Add(4 * time.Second),
			Payload: map[string]any{
				"tool":  "bash",
				"input": "{\"command\":\"ls\"}",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	collapsed := model.renderTrace()
	assert.Contains(t, collapsed, "Latest Call Stack")
	assert.Contains(t, collapsed, "tech-lead -> context-analyst")
	assert.Contains(t, collapsed, "agent call -> context-analyst")
	assert.Contains(t, collapsed, "read_file")
	assert.Contains(t, collapsed, "bash")

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF3})
	model = updated.(*cliModel)

	expanded := model.renderTrace()
	assert.Contains(t, expanded, "tech-lead")
	assert.Contains(t, expanded, "context-analyst")
	assert.Contains(t, expanded, "agent call -> context-analyst")
	assert.Contains(t, expanded, "read_file")
	assert.Contains(t, expanded, "bash")
	assert.Contains(t, expanded, "F2 切换 split / full")
}

func TestCLIModelLatestCallStackShowsNodeErrors(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "tool.called",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"id":    "tool_read",
				"tool":  "read_file",
				"input": "{\"path\":\"missing.md\"}",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "tool.finished",
			Timestamp: now.Add(2 * time.Second),
			Payload: map[string]any{
				"id":    "tool_read",
				"tool":  "read_file",
				"error": "open missing.md: no such file or directory",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.failed",
			Timestamp: now.Add(3 * time.Second),
			Payload: map[string]any{
				"message": "provider stream returned truncated JSON before a complete response: unexpected end of JSON input",
			},
		},
	}

	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	stack := model.renderLatestCallStack(120)
	assert.Contains(t, stack, "read_file")
	assert.Contains(t, stack, "error: open missing.md: no such file or directory")
	assert.Contains(t, stack, "error: provider stream returned truncated JSON before a complete response")
}

func TestCLIModelTreatsRunAbortedAsTerminal(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.aborted",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"message": "run aborted",
				"reason":  "aborted",
			},
		},
	}

	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	node := model.traceNodes["run_root"]
	require.NotNil(t, node)
	assert.Equal(t, "aborted", node.status)
	assert.Equal(t, 0, model.runningRunCount())
	running, completed, failed := model.traceStatusCounts()
	assert.Equal(t, 0, running)
	assert.Equal(t, 0, completed)
	assert.Equal(t, 1, failed)
	assert.Equal(t, "idle", model.runtimeStatusLabel())
	assert.Contains(t, model.renderLatestCallStack(120), "error: run aborted")
}

func TestCLIModelToggleInspectorPreservesTraceContent(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"target_agent": "architect",
				"task":         "输出方案",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	before := model.renderTrace()

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)
	after := model.renderTrace()

	assert.Equal(t, before, after)
}

func TestCLIModelExpandedInspectorKeepsRunTreeVisible(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 18})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"target_agent": "coder",
				"task":         "实现修复",
			},
		},
		{
			RunID:         "run_child",
			AgentID:       "coder",
			SessionID:     "agentcall_child",
			ParentAgentID: "lead",
			Name:          "run.started",
			Timestamp:     now.Add(2 * time.Second),
		},
	}
	for i, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
		for j := 0; j < 8; j++ {
			updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
				RunID:     "run_root",
				AgentID:   "lead",
				SessionID: "cli_local",
				Name:      "tool.finished",
				Timestamp: now.Add(time.Duration(i*10+j+3) * time.Second),
				Payload: map[string]any{
					"tool":   "bash",
					"output": "ok",
				},
			}})
			model = updated.(*cliModel)
		}
	}

	view := model.View()
	assert.Contains(t, view, "Session Window")
	assert.NotContains(t, view, "Current Focus")
	assert.NotContains(t, view, "Selected RunTree Node")
	assert.NotContains(t, view, "Global Activity")
}

func TestCLIModelArrowKeysMoveSelectedTraceNode(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:         "run_child",
			AgentID:       "researcher",
			SessionID:     "agentcall_child",
			ParentAgentID: "lead",
			Name:          "run.started",
			Timestamp:     now.Add(time.Second),
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	assert.Equal(t, "run_root", model.selectedTraceRunID)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(*cliModel)
	assert.Equal(t, "run_root", model.selectedTraceRunID)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(*cliModel)
	assert.Equal(t, "run_root", model.selectedTraceRunID)
}

func TestCLIModelPreservesManualChatScrollOnRefresh(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	model = updated.(*cliModel)

	for i := 0; i < 24; i++ {
		updated, _ = model.Update(cliAssistantMessageMsg{Text: fmt.Sprintf("message %02d", i)})
		model = updated.(*cliModel)
	}

	model.chatView.LineUp(6)
	offsetBefore := model.chatView.YOffset
	assert.Greater(t, offsetBefore, 0)
	assert.False(t, model.chatView.AtBottom())

	updated, _ = model.Update(cliAssistantMessageMsg{Text: "new message while reviewing history"})
	model = updated.(*cliModel)

	assert.Equal(t, offsetBefore, model.chatView.YOffset)
	assert.False(t, model.chatView.AtBottom())
}

func TestCLIModelSurfacesBackgroundTaskLifecycle(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(200 * time.Millisecond),
			Payload: map[string]any{
				"id":           "deleg_call_1",
				"target_agent": "coder",
				"task":         "实现修复",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "agent.call.completed",
			Timestamp: now.Add(400 * time.Millisecond),
			Payload: map[string]any{
				"id":           "deleg_call_1",
				"target_agent": "coder",
				"status":       "running",
				"task_id":      "task_1",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.started",
			Timestamp: now.Add(600 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_1",
				"target_agent": "coder",
				"status":       "running",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.completed",
			Timestamp: now.Add(800 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_1",
				"target_agent": "coder",
				"status":       "completed",
				"summary":      "修复已完成",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	trace := model.renderTrace()
	assert.Contains(t, trace, "state ")
	assert.Contains(t, trace, "task completed")
	assert.Contains(t, trace, "subagents:")
	assert.Contains(t, trace, "task done")
	assert.Contains(t, trace, "coder task completed")
}

func TestCLIModelSupportsShortcutQuit(t *testing.T) {
	model := newCLIModel("", "", func(string) {})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(*cliModel)
	assert.NotNil(t, model)
	if assert.NotNil(t, cmd) {
		assert.IsType(t, tea.QuitMsg{}, cmd())
	}
}

func TestCLIModelEnterSendsInput(t *testing.T) {
	var sent []string
	model := newCLIModel("", "", func(text string) {
		sent = append(sent, text)
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	assert.Equal(t, 1, model.input.Height())
	model.input.SetValue("第一行")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, []string{"第一行"}, sent)
	assert.Equal(t, "", model.input.Value())
	assert.Equal(t, 1, model.input.Height())
	assert.Equal(t, "status", model.messages[len(model.messages)-1].kind)
	assert.Equal(t, "第一行", model.messages[len(model.messages)-2].text)
}

func TestCLIModelCtrlJAddsNewline(t *testing.T) {
	var sent []string
	model := newCLIModel("", "", func(text string) {
		sent = append(sent, text)
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("第一行")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, "第一行\n", model.input.Value())
	assert.Equal(t, 2, model.input.Height())
	assert.Empty(t, sent)
}

func TestCLIModelViewKeepsInputPanelVisibleAfterInputGrows(t *testing.T) {
	model := newCLIModel("", "", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("你")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	view := model.View()
	assert.Contains(t, view, "Compose")
	assert.Contains(t, view, "Session Window")
}

func TestCLIModelFirstEnterKeepsPreviousLineVisible(t *testing.T) {
	model := newCLIModel("", "", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("你")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	panel := model.renderInputPanel(model.panelWidth)
	assert.Contains(t, panel, "你")
}

func TestCLIModelEnterSendsMultilineInput(t *testing.T) {
	var sent []string
	model := newCLIModel("", "", func(text string) {
		sent = append(sent, text)
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("第一行\n第二行")
	model.syncInputHeight()
	assert.Equal(t, 2, model.input.Height())

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, []string{"第一行\n第二行"}, sent)
	assert.Equal(t, "", model.input.Value())
	assert.Equal(t, 1, model.input.Height())
	assert.Equal(t, "status", model.messages[len(model.messages)-1].kind)
	assert.Equal(t, "第一行\n第二行", model.messages[len(model.messages)-2].text)
}
