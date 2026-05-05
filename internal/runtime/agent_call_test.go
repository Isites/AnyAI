package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	runtimetaskagent "github.com/Isites/anyai/internal/runtime/task/agentexec"
	runtimetaskbuiltin "github.com/Isites/anyai/internal/runtime/task/builtin"
	"github.com/Isites/anyai/internal/runtime/testutil"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeDoAgentUsesUnifiedTaskRuntime(t *testing.T) {
	rt, taskStore := newAgentCallTestRuntime(t, &mockProvider{response: "runtime callagent result"})

	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:   "run_parent",
		AgentID: "lead",
	})

	done := make(chan tools.AgentCallResult, 1)
	taskID, err := rt.DoAgent(ctx, tools.AgentCallRequest{
		Agent: "assistant",
		Task:  "execute through unified runtime",
	}, func(_ context.Context, result tools.AgentCallResult) {
		done <- result
	})
	require.NoError(t, err)
	require.NotEmpty(t, taskID)

	result := waitAgentCallResult(t, done)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, "completed", result.Status)
	assert.Equal(t, "runtime callagent result", result.Summary)

	record, ok := taskStore.Get(result.TaskID)
	require.True(t, ok)
	assert.Equal(t, task.KindAgent, record.Kind)
	assert.Equal(t, task.StatusCompleted, record.Status)
	assert.Equal(t, "assistant", record.TargetAgent)
}

func TestRuntimeDoAgentReturnsBeforeDelayedChildSettles(t *testing.T) {
	rt, _ := newAgentCallTestRuntime(t, &delayedProvider{response: "delayed result", delay: 150 * time.Millisecond})

	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:   "run_parent",
		AgentID: "lead",
	})

	done := make(chan tools.AgentCallResult, 1)
	startedAt := time.Now()
	taskID, err := rt.DoAgent(ctx, tools.AgentCallRequest{
		Agent: "assistant",
		Task:  "execute asynchronously",
	}, func(_ context.Context, result tools.AgentCallResult) {
		done <- result
	})
	require.NoError(t, err)
	require.NotEmpty(t, taskID)
	assert.Less(t, time.Since(startedAt), 100*time.Millisecond)

	result := waitAgentCallResult(t, done)
	assert.Equal(t, "completed", result.Status)
	assert.Equal(t, "delayed result", result.Summary)
}

func TestRuntimeDoAgentCancelsWithParentContext(t *testing.T) {
	rt, taskStore := newAgentCallTestRuntime(t, &delayedProvider{response: "background result", delay: 250 * time.Millisecond})

	parentCtx, cancel := context.WithCancel(tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:   "run_parent",
		AgentID: "lead",
	}))

	done := make(chan tools.AgentCallResult, 1)
	taskID, err := rt.DoAgent(parentCtx, tools.AgentCallRequest{
		Agent: "assistant",
		Task:  "execute inline child task",
	}, func(_ context.Context, result tools.AgentCallResult) {
		done <- result
	})
	require.NoError(t, err)
	require.NotEmpty(t, taskID)

	cancel()
	result := waitAgentCallResult(t, done)
	assert.Equal(t, "cancelled", result.Status)

	record, ok := taskStore.Get(taskID)
	require.True(t, ok)
	assert.Equal(t, task.StatusCancelled, record.Status)
}

func TestRuntimeDoAgentParallelInvokesBatchCallback(t *testing.T) {
	rt, _ := newAgentCallTestRuntime(t, &mockProvider{response: "runtime callagent result"})

	parentCtx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:   "run_parent",
		AgentID: "lead",
	})

	done := make(chan tools.ParallelAgentCallResult, 1)
	batchID, err := rt.DoAgentParallel(parentCtx, []tools.AgentCallRequest{
		{Agent: "assistant", Task: "execute task A"},
		{Agent: "assistant", Task: "execute task B"},
		{Agent: "assistant", Task: "execute task C"},
	}, 2, func(_ context.Context, result tools.ParallelAgentCallResult) {
		done <- result
	})
	require.NoError(t, err)
	require.NotEmpty(t, batchID)

	result := waitParallelAgentCallResult(t, done)
	require.Len(t, result.Results, 3)
	assert.Equal(t, "parallel", result.Mode)
	assert.Equal(t, batchID, result.BatchID)
	for _, item := range result.Results {
		assert.Equal(t, "completed", item.Status)
		assert.Equal(t, "runtime callagent result", item.Summary)
		assert.NotEmpty(t, item.TaskID)
	}
}

func TestChildAgentCallAutoCompletesAfterGroundedAnswer(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "data"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "data", "metrics.csv"),
		[]byte("dimension,score,notes\nrisk_coordination,64,shared context still needs reinforcement\n"),
		0o644,
	))

	provider := &readThenSummarizeProvider{}
	rt, taskStore := newAgentCallTestRuntime(t, provider)
	cfg := rt.Config()
	for i := range cfg.Agents.List {
		if cfg.Agents.List[i].ID != "assistant" {
			continue
		}
		cfg.Agents.List[i].Workspace = workspace
		cfg.Agents.List[i].MaxTurns = 3
		cfg.Agents.List[i].Tools.Allow = []string{"read_file"}
	}

	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		RunID:   "run_parent",
		AgentID: "lead",
	})

	done := make(chan tools.AgentCallResult, 1)
	taskID, err := rt.DoAgent(ctx, tools.AgentCallRequest{
		Agent: "assistant",
		Task:  "请读取 data/metrics.csv，并只基于实际内容给我一句风险判断。",
	}, func(_ context.Context, result tools.AgentCallResult) {
		done <- result
	})
	require.NoError(t, err)
	require.NotEmpty(t, taskID)

	result := waitAgentCallResult(t, done)
	assert.Equal(t, "completed", result.Status)
	assert.Contains(t, result.Summary, "risk_coordination")
	assert.Equal(t, 2, provider.calls)

	record, ok := taskStore.Get(taskID)
	require.True(t, ok)
	assert.Equal(t, task.StatusCompleted, record.Status)
}

type delayedProvider struct {
	response string
	delay    time.Duration

	autoGoalFinalize bool
	autoGoalSeq      int
}

type readThenSummarizeProvider struct {
	calls int
}

func (p *readThenSummarizeProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.calls++
	if promptContainsRuntimeGoalContinuation(req.Messages) {
		return testutil.StaticEventStream([]llm.ChatEvent{
			{Type: llm.EventError, Error: assert.AnError},
		}), nil
	}
	if requestHasToolResult(req.Messages) {
		return testutil.StaticEventStream([]llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "关键发现：risk_coordination=64，说明共享上下文协同仍然偏弱。"},
			{Type: llm.EventDone},
		}), nil
	}
	return testutil.StaticEventStream([]llm.ChatEvent{
		{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_read_metrics", Name: "read_file"}},
		{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
			ID:    "tc_read_metrics",
			Name:  "read_file",
			Input: json.RawMessage(`{"path":"data/metrics.csv"}`),
		}},
		{Type: llm.EventDone},
	}), nil
}

func (p *readThenSummarizeProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *readThenSummarizeProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "compact summary"}, nil
}

func requestHasToolResult(messages []llm.Message) bool {
	for _, message := range messages {
		if strings.TrimSpace(message.ToolCallID) != "" {
			return true
		}
	}
	return false
}

func promptContainsRuntimeGoalContinuation(messages []llm.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Role) != "user" {
			continue
		}
		return strings.Contains(messages[i].Content, "[Runtime goal continuation]")
	}
	return false
}

func (p *delayedProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
			return
		case <-time.After(p.delay):
		}
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *delayedProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *delayedProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "delayed compact summary"}, nil
}

func newAgentCallTestRuntime(t *testing.T, provider llm.LLMProvider) (*Runtime, *task.Store) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Runtime.IdleTimeoutMS = 1000
	cfg.ProjectRoot = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "lead",
			Name:      "Lead",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
		},
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
		},
	}

	sessionStore := session.NewStore(t.TempDir())
	rt := New(map[string]llm.LLMProvider{"test": provider}, sessionStore, cfg)

	taskStore := task.NewStore()
	registry := task.NewExecutorRegistry()
	agentExecutor := runtimetaskagent.New()
	agentExecutor.SetRunner(rt)
	toolExecutor := runtimetaskbuiltin.NewToolExecutor(nil)
	require.NoError(t, registry.Register(task.KindAgent, agentExecutor))
	require.NoError(t, registry.Register(task.KindTool, toolExecutor))
	require.NoError(t, registry.Register(task.KindProcess, runtimetaskbuiltin.NewProcessExecutor()))

	taskRuntime := task.NewRuntime(taskStore, registry)
	rt.SetTaskRuntime(taskRuntime)

	return rt, taskStore
}

func waitAgentCallResult(t *testing.T, done <-chan tools.AgentCallResult) tools.AgentCallResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for agent callback")
		return tools.AgentCallResult{}
	}
}

func waitParallelAgentCallResult(t *testing.T, done <-chan tools.ParallelAgentCallResult) tools.ParallelAgentCallResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for parallel agent callback")
		return tools.ParallelAgentCallResult{}
	}
}
