package examples

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeexecution "github.com/Isites/anyai/internal/runtime/execution"
	inputpkg "github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
)

func handleRuntimeLabScenario(scenario workflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	if agentID != "runtime-lab" {
		return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
	}

	if err := requireContains(req.SystemPrompt, "用户只需要描述目标", "后台子 Agent"); err != nil {
		return nil, err
	}

	switch ctx.Prompt {
	case scenario.Turns[0].Prompt:
		return textEvents("这个示例主要演示统一输入、会话计划、记忆检索和后台任务管理。"), nil
	case scenario.Turns[1].Prompt:
		if len(ctx.PriorUserTurns) == 0 {
			return nil, fmt.Errorf("runtime-lab follow-up turn did not receive prior history")
		}
		return textEvents("因为主 Agent 会自己判断何时使用这些运行时能力，所以用户只需要自然语言。"), nil
	case scenario.IsolatedPrompt:
		if len(ctx.PriorUserTurns) != 0 {
			return nil, fmt.Errorf("runtime-lab isolated session leaked prior history")
		}
		return textEvents("这是新的运行时实验会话。"), nil
	default:
		return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
	}
}

func (h *exampleHarness) runEnvelope(t *testing.T, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	t.Helper()
	return h.runEnvelopeForAgent(t, h.entry.ID, sessionID, env)
}

func (h *exampleHarness) runEnvelopeForAgent(t *testing.T, agentID, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if env.SessionID == "" {
		env.SessionID = sessionID
	}

	run, err := runtimeexecution.StartManagedRun(ctx, runtimeport.ExecutionDeps{
		Providers:    h.providers,
		Config:       h.cfg,
		SessionStore: h.store,
		AgentRunner:  h.runtime,
		Recorder:     h.recorder,
		Memory:       h.memoryMgr,
		Pipeline:     h.pipeline,
		TaskStore:    h.taskStore,
	}, runtimeport.RunRequest{
		AgentID:   agentID,
		SessionID: sessionID,
		Envelope:  env,
	})
	if err != nil {
		t.Fatalf("start run for %s: %v", agentID, err)
	}

	for event := range run.Events {
		if message := runtimeevents.FailureMessage(event); message != "" {
			t.Fatalf("run %s returned error: %s", run.RunID, message)
		}
	}

	finalRun, ok := h.recorder.GetRun(run.RunID)
	if !ok {
		t.Fatalf("run %s not recorded", run.RunID)
	}
	if finalRun.Status != runtimeevents.RunStatusCompleted {
		t.Fatalf("run %s status=%s error=%s", run.RunID, finalRun.Status, strings.TrimSpace(finalRun.Error))
	}

	trace := h.waitForRunTreeSnapshot(t, run.RunID, run.RunID)

	return exampleRunOutcome{
		Run:     finalRun,
		Events:  h.recorder.ListRunEvents(run.RunID),
		RunTree: trace,
	}
}

func (h *exampleHarness) rawSessionEntries(t *testing.T, agentID, sessionID string) []session.SessionEntry {
	t.Helper()

	sess, err := h.store.Load(agentID, sessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", sessionID, err)
	}
	return sess.Entries()
}

func TestRuntimeLabInputPlanTodoFlowWithMockProvider(t *testing.T) {
	root := examplesRoot(t)
	projectDir := filepath.Join(root, "runtime-lab")
	prompt := "请先梳理当前输入，再帮我建立一个两步计划和一个待办。"
	stage := 0
	continuationCompleted := false

	provider := &scriptedProvider{
		handler: func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
			if agentID != "runtime-lab" {
				return nil, fmt.Errorf("unexpected agent %q", agentID)
			}

			ctx := currentTurnContext(req.Messages)
			if strings.Contains(ctx.Prompt, "[Runtime goal continuation]") {
				if continuationCompleted {
					return textEvents("已核对输入，并完成两步计划与待办。"), nil
				}
				todoID := firstTodoID(ctx.Prompt)
				if todoID == "" {
					return nil, fmt.Errorf("continuation prompt did not include todo id: %q", ctx.Prompt)
				}
				continuationCompleted = true
				return toolEvents(
					jsonToolCall("tc_plan_done", "update_plan", map[string]any{
						"description": "核对输入并输出摘要",
						"state":       "completed",
						"steps": []map[string]any{
							{"id": "step_input", "description": "核对输入", "state": "completed"},
							{"id": "step_summary", "description": "输出摘要", "state": "completed"},
						},
					}),
					jsonToolCall("tc_todo_done", "todo", map[string]any{"action": "complete", "item": todoID}),
				), nil
			}
			if !strings.Contains(ctx.Prompt, prompt) {
				return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
			}

			stage++
			switch stage {
			case 1:
				return toolEvents(jsonToolCall("tc_manifest", "input_manifest", map[string]any{})), nil
			case 2:
				return toolEvents(
					jsonToolCall("tc_plan", "update_plan", map[string]any{
						"description": "核对输入并输出摘要",
						"state":       "running",
						"steps": []map[string]any{
							{"id": "step_input", "description": "核对输入", "state": "completed"},
							{"id": "step_summary", "description": "输出摘要", "state": "pending"},
						},
					}),
					jsonToolCall("tc_todo", "todo", map[string]any{"action": "add", "item": "整理输入摘要"}),
				), nil
			default:
				return textEvents("已核对输入，并建立了两步计划与待办。"), nil
			}
		},
	}

	harness := newExampleHarness(t, projectDir, provider, nil)
	outcome := harness.runEnvelope(t, "runtime-input-plan", inputpkg.InputEnvelope{
		SessionID: "runtime-input-plan",
		Blocks: []inputpkg.InputBlock{
			{Type: "text", Text: prompt},
			{Type: "file", Name: "brief.txt", Path: filepath.Join(projectDir, "fixtures", "brief.txt")},
			{Type: "dir", Name: "reference", Path: filepath.Join(projectDir, "fixtures", "reference")},
			{Type: "url", URL: "https://example.com/runtime-lab"},
			{Type: "image", Name: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
			{Type: "pdf", Name: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.4 runtime lab")},
		},
	})

	if err := validateNoDelegation(outcome.Events, outcome.RunTree, outcome.Run.ID); err != nil {
		t.Fatalf("expected no delegation: %v", err)
	}
	assert.Contains(t, calledToolNames(outcome.Events), "input_manifest")
	assert.Contains(t, calledToolNames(outcome.Events), "update_plan")
	assert.Contains(t, calledToolNames(outcome.Events), "todo")

	var hasPlan, hasTodo bool
	for _, entry := range harness.rawSessionEntries(t, harness.entry.ID, "runtime-input-plan") {
		switch entry.Type {
		case session.EntryTypePlan:
			var plan session.PlanData
			require.NoError(t, json.Unmarshal(entry.Data, &plan))
			if plan.Structured != nil && strings.Contains(plan.Structured.Description, "核对输入") {
				hasPlan = true
			}
		case session.EntryTypeTodo:
			var todo session.TodoData
			require.NoError(t, json.Unmarshal(entry.Data, &todo))
			if todo.Content == "整理输入摘要" && todo.Status == "completed" {
				hasTodo = true
			}
		}
	}
	assert.True(t, hasPlan, "expected plan entry in session history")
	assert.True(t, hasTodo, "expected todo entry in session history")
}

func firstTodoID(text string) string {
	matches := regexp.MustCompile(`todo_[a-z0-9]+`).FindStringSubmatch(text)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func TestRuntimeLabMemoryToolsWithMockProvider(t *testing.T) {
	root := examplesRoot(t)
	prompt := "请从记忆里找出 runtime-lab 的长期规则和当前演示重点。"
	stage := 0

	provider := &scriptedProvider{
		handler: func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
			if agentID != "runtime-lab" {
				return nil, fmt.Errorf("unexpected agent %q", agentID)
			}

			ctx := currentTurnContext(req.Messages)
			if !strings.Contains(ctx.Prompt, prompt) {
				return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
			}

			stage++
			switch stage {
			case 1:
				return toolEvents(jsonToolCall("tc_mem_search", "memory_search", map[string]any{
					"query":     "runtime-lab 运行时 规则 演示重点",
					"max_items": 3,
				})), nil
			case 2:
				return toolEvents(
					jsonToolCall("tc_mem_get_long", "memory_get", map[string]any{"id": "long-term/runtime-principles"}),
					jsonToolCall("tc_mem_get_epi", "memory_get", map[string]any{"id": "episodic/current-focus"}),
				), nil
			default:
				return textEvents("长期规则强调自然语言驱动，当前演示重点是输入、计划、记忆和后台任务。"), nil
			}
		},
	}

	harness := newExampleHarness(t, filepath.Join(root, "runtime-lab"), provider, nil)
	outcome := harness.run(t, "runtime-memory-tools", prompt)

	if err := validateNoDelegation(outcome.Events, outcome.RunTree, outcome.Run.ID); err != nil {
		t.Fatalf("expected no delegation: %v", err)
	}
	assert.Contains(t, calledToolNames(outcome.Events), "memory_search")
	assert.Contains(t, calledToolNames(outcome.Events), "memory_get")
	require.NotNil(t, harness.memoryMgr)
	assert.GreaterOrEqual(t, len(harness.memoryMgr.Entries()), 2)
}

func toolResultContent(messages []llm.Message, toolCallID string) string {
	for _, msg := range messages {
		if msg.Role == "user" && msg.ToolCallID == toolCallID {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}

func jsonToolCall(id, name string, payload map[string]any) llm.ToolCall {
	input, _ := json.Marshal(payload)
	return llm.ToolCall{
		ID:    id,
		Name:  name,
		Input: input,
	}
}

func toolEvents(calls ...llm.ToolCall) []llm.ChatEvent {
	events := make([]llm.ChatEvent, 0, len(calls)*2+1)
	for _, call := range calls {
		call := call
		events = append(events, llm.ChatEvent{Type: llm.EventToolCallStart, ToolCall: &call})
		events = append(events, llm.ChatEvent{Type: llm.EventToolCallDone, ToolCall: &call})
	}
	events = append(events, llm.ChatEvent{Type: llm.EventDone})
	return events
}

func attachmentIDsByType(manifestJSON string) (map[string]string, error) {
	var manifest []map[string]any
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return nil, fmt.Errorf("decode input manifest: %w", err)
	}

	ids := map[string]string{}
	for _, item := range manifest {
		typ, _ := item["type"].(string)
		id, _ := item["id"].(string)
		if typ != "" && id != "" {
			ids[typ] = id
		}
	}
	for _, typ := range []string{"file", "image", "pdf"} {
		if ids[typ] == "" {
			return nil, fmt.Errorf("manifest missing attachment id for %s", typ)
		}
	}
	return ids, nil
}

func taskIDFromAgentCallResult(resultJSON string) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &payload); err != nil {
		return "", fmt.Errorf("decode callagent result: %w", err)
	}
	taskID, _ := payload["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return "", fmt.Errorf("callagent result missing task_id")
	}
	return taskID, nil
}

func calledToolNames(events []runtimeevents.EventRecord) []string {
	var names []string
	for _, event := range events {
		if !isToolStartEvent(event.Name) {
			continue
		}
		toolName, _ := event.Payload["tool"].(string)
		if toolName != "" {
			names = append(names, toolName)
		}
	}
	return names
}
