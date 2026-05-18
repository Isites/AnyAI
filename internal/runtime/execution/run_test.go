package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/registry"
	"github.com/Isites/anyai/internal/runtime/agent"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	runtimesessionops "github.com/Isites/anyai/internal/runtime/session/ops"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/testutil"
	"github.com/Isites/anyai/internal/runtime/tool"
	openai "github.com/sashabaranov/go-openai"
)

type capturingProvider struct {
	response string

	mu       sync.Mutex
	requests []llm.ChatRequest

	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *capturingProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	ch := make(chan llm.ChatEvent, 2)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *capturingProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *capturingProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "capturing compact summary"}, nil
}

func (p *capturingProvider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...)
}

type stagedProvider struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	handler  func(callIndex int, req llm.ChatRequest) ([]llm.ChatEvent, error)
}

func (p *stagedProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.mu.Lock()
	callIndex := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	events, err := p.handler(callIndex, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (p *stagedProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *stagedProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "staged compact summary"}, nil
}

func (p *stagedProvider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...)
}

type failingProvider struct {
	err error
}

func (p failingProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, p.err
}

func (p failingProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "failing-model", Name: "Failing Model", Provider: "test"}}
}

func (p failingProvider) Compact(context.Context, llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "failing compact summary"}, nil
}

type agentRuntimeConfigurerFunc func(*agent.Runtime)

func (f agentRuntimeConfigurerFunc) ConfigureAgentRuntime(rt any) error {
	agentRuntime, ok := rt.(*agent.Runtime)
	if !ok {
		return fmt.Errorf("unexpected runtime type %T", rt)
	}
	f(agentRuntime)
	return nil
}

type streamingProvider struct {
	segments []string
	delay    time.Duration

	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *streamingProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	ch := make(chan llm.ChatEvent, len(p.segments)+1)
	go func() {
		defer close(ch)
		for _, segment := range p.segments {
			if p.delay > 0 {
				select {
				case <-ctx.Done():
					ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
					return
				case <-time.After(p.delay):
				}
			}
			ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: segment}
		}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *streamingProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "stream-model", Name: "Stream Model", Provider: "test"}}
}

func (p *streamingProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "streaming compact summary"}, nil
}

type activityHeartbeatProvider struct {
	heartbeats int
	delay      time.Duration
	response   string

	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *activityHeartbeatProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	ch := make(chan llm.ChatEvent, p.heartbeats+2)
	go func() {
		defer close(ch)
		for i := 0; i < p.heartbeats; i++ {
			if p.delay > 0 {
				select {
				case <-ctx.Done():
					ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
					return
				case <-time.After(p.delay):
				}
			}
			ch <- llm.ChatEvent{Type: llm.EventActivity}
		}
		if strings.TrimSpace(p.response) != "" {
			ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *activityHeartbeatProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "activity-model", Name: "Activity Model", Provider: "test"}}
}

func (p *activityHeartbeatProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "activity compact summary"}, nil
}

type contextCapturingProvider struct {
	response string

	mu               sync.Mutex
	contexts         []tools.RuntimeContext
	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *contextCapturingProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}

	p.mu.Lock()
	p.contexts = append(p.contexts, tools.RuntimeContextFrom(ctx))
	p.mu.Unlock()

	ch := make(chan llm.ChatEvent, 2)
	ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
	ch <- llm.ChatEvent{Type: llm.EventDone}
	close(ch)
	return ch, nil
}

func (p *contextCapturingProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *contextCapturingProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "context compact summary"}, nil
}

func (p *contextCapturingProvider) Contexts() []tools.RuntimeContext {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]tools.RuntimeContext(nil), p.contexts...)
}

type imageToolExecutor struct{}

func (imageToolExecutor) Execute(_ context.Context, name string, _ json.RawMessage) (tools.ToolResult, error) {
	if name != "image_tool" {
		return tools.ToolResult{}, fmt.Errorf("unknown tool: %q", name)
	}
	return tools.ToolResult{
		Output: "generated image",
		Images: []llm.ImageContent{{
			Name:     "tool.png",
			MimeType: "image/png",
			Data:     []byte{0x89, 'P', 'N', 'G'},
		}},
	}, nil
}

func (imageToolExecutor) ToolDefs() []llm.ToolDef {
	return []llm.ToolDef{{
		Name:        "image_tool",
		Description: "Return a generated test image.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}}
}

func (imageToolExecutor) Names() []string {
	return []string{"image_tool"}
}

func (imageToolExecutor) Get(name string) (tools.Tool, bool) {
	return nil, name == "image_tool"
}

type noopAgentCallRunner struct {
	agents []tools.AgentInfo
}

func (r noopAgentCallRunner) DoAgent(ctx context.Context, _ tools.AgentCallRequest, callback tools.AgentResultCallback) (string, error) {
	callback(ctx, tools.AgentCallResult{Status: "failed", Error: "agent call not implemented in test"})
	return "task_1", nil
}

func (r noopAgentCallRunner) DoAgentParallel(ctx context.Context, _ []tools.AgentCallRequest, _ int, callback tools.ParallelAgentResultCallback) (string, error) {
	callback(ctx, tools.ParallelAgentCallResult{Mode: "parallel"})
	return "batch_1", nil
}

func (r noopAgentCallRunner) AvailableAgents() []tools.AgentInfo {
	return append([]tools.AgentInfo(nil), r.agents...)
}

func TestStartManagedRunInjectsScopedSkillsAndLayeredMemory(t *testing.T) {
	root := t.TempDir()
	leadWorkspace := filepath.Join(root, "agents", "lead")
	hiddenWorkspace := filepath.Join(root, "agents", "hidden")
	sharedSkillsDir := filepath.Join(root, "common", "skills")

	require.NoError(t, os.MkdirAll(filepath.Join(leadWorkspace, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(hiddenWorkspace, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(sharedSkillsDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(sharedSkillsDir, "launch-checklist.md"), []byte(`---
name: launch-checklist
description: Create launch checklist guidance for Apollo release work
tags:
  - launch
  - checklist
---
Shared launch guidance: always produce a concise release checklist.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(leadWorkspace, "skills", "rollout-playbook.md"), []byte(`---
name: rollout-playbook
description: Apollo rollout plan and staging checklist for deployment work
tags:
  - rollout
  - apollo
---
Lead rollout rule: verify staging before rollout.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hiddenWorkspace, "skills", "secret-playbook.md"), []byte(`---
name: secret-playbook
description: Apollo rollout plan and checklist that should stay hidden
tags:
  - rollout
  - checklist
---
Hidden reviewer-only rule.
`), 0o644))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.SharedSkillsDir = sharedSkillsDir
	cfg.Memory.Inject.MaxItems = 2
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:                  "lead",
			Name:                "Lead",
			Model:               "test/mock-model",
			Workspace:           leadWorkspace,
			PrivateSkillsDir:    filepath.Join(leadWorkspace, "skills"),
			InheritSharedSkills: true,
		},
		{
			ID:                  "hidden",
			Name:                "Hidden",
			Model:               "test/mock-model",
			Workspace:           hiddenWorkspace,
			PrivateSkillsDir:    filepath.Join(hiddenWorkspace, "skills"),
			InheritSharedSkills: true,
		},
	}

	memMgr := memory.NewManager(filepath.Join(root, "memory"))
	require.NoError(t, memMgr.Load())
	require.NoError(t, memMgr.SaveToLayer(memory.LayerEpisodic, "current-sprint", "# Current Sprint\n\nApollo launch is blocked on staging verification today."))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "launch-rule", "# Launch Rule\n\nApollo rollout requires a blue green deployment plan."))

	provider := &capturingProvider{response: "done"}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
		Memory:       memMgr,
	}, runtimeport.RunRequest{
		AgentID:   "lead",
		SessionID: "shared",
		Envelope:  input.NewEnvelopeFromText("shared", "Need the Apollo launch checklist and rollout plan."),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	requests := provider.Requests()
	require.Len(t, requests, 1)
	systemPrompt := requests[0].SystemPrompt

	assert.Contains(t, systemPrompt, "## Relevant Skills")
	assert.Contains(t, systemPrompt, "### launch-checklist")
	assert.Contains(t, systemPrompt, "- Summary: Create launch checklist guidance for Apollo release work")
	assert.Contains(t, systemPrompt, "### rollout-playbook")
	assert.Contains(t, systemPrompt, "- Summary: Apollo rollout plan and staging checklist for deployment work")
	assert.Contains(t, systemPrompt, "call `skill_get`")
	assert.Contains(t, systemPrompt, "If exactly one skill is clearly relevant")
	assert.NotContains(t, systemPrompt, "Shared launch guidance: always produce a concise release checklist.")
	assert.NotContains(t, systemPrompt, "Lead rollout rule: verify staging before rollout.")
	assert.NotContains(t, systemPrompt, "Hidden reviewer-only rule.")
	assert.Contains(t, toolDefNames(requests[0].Tools), "skill_get")
	assert.Contains(t, systemPrompt, "## Tool Call Style")
	assert.Contains(t, systemPrompt, "## Runtime Facts")
	assert.Contains(t, systemPrompt, "- Current model: mock-model")

	assert.Contains(t, systemPrompt, "## Relevant Memory")
	assert.Contains(t, systemPrompt, "### [Episodic] Current Sprint")
	assert.Contains(t, systemPrompt, "### [Long-term] Launch Rule")
	assert.Contains(t, systemPrompt, "- ID: episodic/current-sprint")
	assert.Contains(t, systemPrompt, "- ID: long-term/launch-rule")
	assert.Contains(t, systemPrompt, "- Summary: Apollo launch is blocked on staging verification today.")
	assert.Contains(t, systemPrompt, "- Summary: Apollo rollout requires a blue green deployment plan.")
	assert.Less(t,
		strings.Index(systemPrompt, "### [Episodic] Current Sprint"),
		strings.Index(systemPrompt, "### [Long-term] Launch Rule"),
	)
}

func toolDefNames(defs []llm.ToolDef) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

func TestStartManagedRunPersistsToolImagesToAssets(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Agents.List = []config.AgentConfig{{
		ID:        "assistant",
		Name:      "Assistant",
		Model:     "test/mock-model",
		Workspace: workspace,
		Tools:     config.ToolPolicy{Allow: []string{"image_tool"}},
	}}

	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	provider := &stagedProvider{
		handler: func(callIndex int, req llm.ChatRequest) ([]llm.ChatEvent, error) {
			switch callIndex {
			case 0:
				return toolEventsForExecution(jsonToolCallForExecution("tc_image", "image_tool", map[string]any{})), nil
			default:
				var toolImages []llm.ImageContent
				for _, msg := range req.Messages {
					if msg.ToolCallID == "tc_image" {
						toolImages = append(toolImages, msg.Images...)
					}
				}
				require.Len(t, toolImages, 1)
				assert.Equal(t, "image/png", toolImages[0].MimeType)
				assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, toolImages[0].Data)
				return []llm.ChatEvent{
					{Type: llm.EventTextDelta, Text: "saw persisted image"},
					{Type: llm.EventDone},
				}, nil
			}
		},
	}

	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: store,
		Recorder:     recorder,
		RuntimeConfigurer: agentRuntimeConfigurerFunc(func(rt *agent.Runtime) {
			rt.Tools = imageToolExecutor{}
		}),
	}, runtimeport.RunRequest{
		RunID:     "run_tool_image",
		AgentID:   "assistant",
		SessionID: "session-tool-image",
		Envelope:  input.NewEnvelopeFromText("session-tool-image", "create an image"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	imagePath := filepath.Join(root, "anyai", "assets", "run_tool_image", "tool_results", "tc_image", "tool.png")
	assert.FileExists(t, imagePath)

	sess, err := store.Load("assistant", "session-tool-image")
	require.NoError(t, err)
	var persistedImage session.ImageData
	for _, entry := range sess.Entries() {
		if entry.Type != session.EntryTypeToolResult {
			continue
		}
		var data session.ToolResultData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		if data.ToolCallID == "tc_image" {
			require.Len(t, data.Images, 1)
			persistedImage = data.Images[0]
		}
	}
	assert.Equal(t, imagePath, persistedImage.Path)
	assert.Empty(t, persistedImage.Data)

	var eventImage map[string]any
	for _, event := range recorder.ListRunEvents("run_tool_image") {
		if event.Name != runtimeevents.EventToolCompleted {
			continue
		}
		images, _ := event.Payload["images"].([]map[string]any)
		if len(images) == 0 {
			rawImages, _ := event.Payload["images"].([]any)
			if len(rawImages) > 0 {
				eventImage, _ = rawImages[0].(map[string]any)
			}
		} else {
			eventImage = images[0]
		}
	}
	require.NotNil(t, eventImage)
	assert.Equal(t, imagePath, eventImage["path"])
	_, hasData := eventImage["data"]
	assert.False(t, hasData)
}

func TestStartManagedRunPersistsRunFailureMeta(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/failing-model",
			Workspace: workspace,
		},
	}

	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	_ = runtimesessionops.NewService(
		func() *config.Config { return cfg },
		func() *session.Store { return store },
		func() *runtimeevents.Recorder { return recorder },
	)
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": failingProvider{err: errors.New("provider unavailable")}},
		Config:       cfg,
		SessionStore: store,
		Recorder:     recorder,
		RuntimeConfigurer: agentRuntimeConfigurerFunc(func(rt *agent.Runtime) {
			rt.LLMMaxAttempts = 1
		}),
	}, runtimeport.RunRequest{
		RunID:     "run_failed",
		AgentID:   "assistant",
		SessionID: "session-failed",
		Envelope:  input.NewEnvelopeFromText("session-failed", "please fail"),
	})
	require.NoError(t, err)

	var sawFailed bool
	for event := range run.Events {
		if event.Name == runtimeevents.EventRunFailed {
			sawFailed = true
		}
	}
	require.True(t, sawFailed)

	sess, err := store.Load("assistant", "session-failed")
	require.NoError(t, err)
	history := sess.History()
	require.NotEmpty(t, history)
	last := history[len(history)-1]
	require.Equal(t, session.EntryTypeMeta, last.Type)

	var meta session.MessageData
	require.NoError(t, json.Unmarshal(last.Data, &meta))
	assert.Contains(t, meta.Text, "Run failed: llm error: provider unavailable")

	events := recorder.ListRunEvents("run_failed")
	var sawMeta bool
	for _, event := range events {
		if event.Name == runtimeevents.EventSessionMetaStored {
			sawMeta = true
			assert.Contains(t, event.Payload["text"], "Run failed: llm error: provider unavailable")
		}
	}
	assert.True(t, sawMeta)
}

func TestManagedRunOutputBufferUsesOnlyPostToolAssistantText(t *testing.T) {
	tests := []struct {
		name   string
		events []runtimeevents.AgentEvent
		want   string
	}{
		{
			name: "direct answer keeps streamed chunks",
			events: []runtimeevents.AgentEvent{
				{Type: runtimeevents.AgentEventTextDelta, Text: "hello "},
				{Type: runtimeevents.AgentEventTextDelta, Text: "world"},
				{Type: runtimeevents.AgentEventDone},
			},
			want: "hello world",
		},
		{
			name: "tool after progress clears stale progress",
			events: []runtimeevents.AgentEvent{
				{Type: runtimeevents.AgentEventTextDelta, Text: "I will inspect the project."},
				{Type: runtimeevents.AgentEventToolCallRequested, ToolCall: &llm.ToolCall{Name: "read_file"}},
				{Type: runtimeevents.AgentEventToolResult, ToolCall: &llm.ToolCall{Name: "read_file"}},
				{Type: runtimeevents.AgentEventDone},
			},
			want: "",
		},
		{
			name: "final answer after tool is preserved",
			events: []runtimeevents.AgentEvent{
				{Type: runtimeevents.AgentEventTextDelta, Text: "I will inspect the project."},
				{Type: runtimeevents.AgentEventToolCallRequested, ToolCall: &llm.ToolCall{Name: "read_file"}},
				{Type: runtimeevents.AgentEventToolResult, ToolCall: &llm.ToolCall{Name: "read_file"}},
				{Type: runtimeevents.AgentEventTextDelta, Text: "Done: tests passed."},
				{Type: runtimeevents.AgentEventDone},
			},
			want: "Done: tests passed.",
		},
		{
			name: "later tool clears earlier final-looking text",
			events: []runtimeevents.AgentEvent{
				{Type: runtimeevents.AgentEventToolResult, ToolCall: &llm.ToolCall{Name: "read_file"}},
				{Type: runtimeevents.AgentEventTextDelta, Text: "Almost done."},
				{Type: runtimeevents.AgentEventToolResult, ToolCall: &llm.ToolCall{Name: "bash"}},
				{Type: runtimeevents.AgentEventDone},
			},
			want: "",
		},
		{
			name: "completion tool does not clear final reply",
			events: []runtimeevents.AgentEvent{
				{Type: runtimeevents.AgentEventTextDelta, Text: "Done: delivered the project."},
				{Type: runtimeevents.AgentEventToolCallRequested, ToolCall: &llm.ToolCall{Name: "goal_complete"}},
				{Type: runtimeevents.AgentEventToolResult, ToolCall: &llm.ToolCall{Name: "goal_complete"}},
				{Type: runtimeevents.AgentEventDone},
			},
			want: "Done: delivered the project.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buffer managedRunOutputBuffer
			for _, event := range tt.events {
				buffer.Observe(event)
			}
			assert.Equal(t, tt.want, buffer.Final())
		})
	}
}

func TestStartManagedRunFiltersSessionScopedLifecycleMemory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agents", "lead")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Memory.Inject.MaxItems = 4
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "lead",
			Name:      "Lead",
			Model:     "test/mock-model",
			Workspace: workspace,
		},
	}

	memMgr := memory.NewManager(filepath.Join(root, "memory"))
	require.NoError(t, memMgr.Load())
	require.NoError(t, memMgr.SaveToLayer(memory.LayerEpisodic, "shared-note", "# Shared Note\n\nUse concise bullet points as the style guide for every codename brief."))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerEpisodic, "session-a", executionManagedDoc("Session A", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "episodic",
		"Scope":      "session",
		"Agent":      "lead",
		"Session":    "sess-a",
	}, "Outcome", "Session A keeps the Polaris codename.")))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "session-b", executionManagedDoc("Session B", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "long-term",
		"Scope":      "session",
		"Agent":      "lead",
		"Session":    "sess-b",
	}, "Decision", "Session B requires the Orion codename.")))

	provider := &capturingProvider{response: "done"}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
		Memory:       memMgr,
	}, runtimeport.RunRequest{
		AgentID:   "lead",
		SessionID: "sess-a",
		Envelope:  input.NewEnvelopeFromText("sess-a", "What should I remember about the codename and style guide?"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	requests := provider.Requests()
	require.Len(t, requests, 1)
	systemPrompt := requests[0].SystemPrompt

	assert.Contains(t, systemPrompt, "Shared Note")
	assert.Contains(t, systemPrompt, "Session A")
	assert.Contains(t, systemPrompt, "Polaris codename")
	assert.NotContains(t, systemPrompt, "Session B")
	assert.NotContains(t, systemPrompt, "Orion codename")
}

func TestStartManagedRunReloadsMemoryFromDiskWithoutRestart(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Memory.Inject.MaxItems = 2
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: workspace,
		},
	}

	memMgr := memory.NewManager(filepath.Join(root, "memory"))
	memMgr.SetRefreshInterval(0)
	require.NoError(t, memMgr.Load())

	provider := &capturingProvider{response: "done"}
	deps := runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
		Memory:       memMgr,
	}

	run, err := StartManagedRun(context.Background(), deps, runtimeport.RunRequest{
		AgentID:   "assistant",
		SessionID: "shared",
		Envelope:  input.NewEnvelopeFromText("shared", "What should I remember about Apollo staging?"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	firstPrompt := provider.Requests()[0].SystemPrompt
	assert.NotContains(t, firstPrompt, "Fresh Disk Memory")

	externalPath := filepath.Join(root, "memory", "episodic", "fresh-disk-memory.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(externalPath), 0o755))
	require.NoError(t, os.WriteFile(externalPath, []byte("# Fresh Disk Memory\n\nApollo staging window opens at 10am."), 0o644))

	run, err = StartManagedRun(context.Background(), deps, runtimeport.RunRequest{
		AgentID:   "assistant",
		SessionID: "shared",
		Envelope:  input.NewEnvelopeFromText("shared", "What should I remember about Apollo staging?"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	requests := provider.Requests()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1].SystemPrompt, "Fresh Disk Memory")
	assert.Contains(t, requests[1].SystemPrompt, "Apollo staging window opens at 10am.")
}

func TestStartManagedRunExposesRuntimeScopedTools(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Memory.Inject.MaxItems = 2
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: workspace,
			Tools: config.ToolPolicy{
				Allow: []string{
					"memory_search",
					"input_manifest",
					"attachment_get",
					"update_plan",
					"todo",
				},
			},
		},
	}

	memMgr := memory.NewManager(filepath.Join(root, "memory"))
	require.NoError(t, memMgr.Load())
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "rule", "# Rule\n\nAlways verify staging."))
	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.md"), []byte("# Spec\n\nCheck staging."), 0o644))

	taskStore := task.NewStore()
	provider := &capturingProvider{response: "done"}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
		Memory:       memMgr,
		TaskStore:    taskStore,
	}, runtimeport.RunRequest{
		AgentID:   "assistant",
		SessionID: "session-tools",
		Envelope: input.InputEnvelope{
			Blocks: []input.InputBlock{
				{Type: "text", Text: "please inspect the attached spec"},
				{Type: "file", Name: "spec.md", MimeType: "text/markdown", Path: filepath.Join(root, "spec.md")},
			},
		},
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	requests := provider.Requests()
	require.Len(t, requests, 1)

	var toolNames []string
	for _, tool := range requests[0].Tools {
		toolNames = append(toolNames, tool.Name)
	}
	assert.Contains(t, toolNames, "memory_search")
	assert.Contains(t, toolNames, "input_manifest")
	assert.Contains(t, toolNames, "attachment_get")
	assert.Contains(t, toolNames, "update_plan")
	assert.Contains(t, toolNames, "todo")

	require.NotEmpty(t, requests[0].Messages)
	assert.Contains(t, requests[0].Messages[0].Content, "please inspect the attached spec")
	assert.Contains(t, requests[0].Messages[0].Content, "[File: spec.md, MIME: text/markdown]")
}

func TestProjectAssetsDirUsesProjectRoot(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root

	assert.Equal(t, filepath.Join(root, "anyai", "assets"), input.ProjectAssetsDir(cfg))
}

func TestStartIngressRunCoversImageAndFileFullPath(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	fixturesDir := filepath.Join(root, "fixtures")
	require.NoError(t, os.MkdirAll(filepath.Join(fixturesDir, "reference"), 0o755))
	sourceFile := filepath.Join(fixturesDir, "brief.txt")
	require.NoError(t, os.WriteFile(sourceFile, []byte("brief content from source file"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixturesDir, "reference", "note.md"), []byte("# reference note"), 0o644))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: workspace,
			Tools: config.ToolPolicy{
				Allow: []string{"input_manifest", "attachment_get"},
			},
		},
	}

	var idsByType map[string]string
	provider := &stagedProvider{
		handler: func(callIndex int, req llm.ChatRequest) ([]llm.ChatEvent, error) {
			switch callIndex {
			case 0:
				require.NotEmpty(t, req.Messages)
				require.Len(t, req.Messages[0].Images, 1)
				assert.Equal(t, "image/png", req.Messages[0].Images[0].MimeType)
				assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, req.Messages[0].Images[0].Data)
				return toolEventsForExecution(jsonToolCallForExecution("tc_manifest", "input_manifest", map[string]any{})), nil
			case 1:
				manifest := toolResultContentForExecution(req.Messages, "tc_manifest")
				require.NotEmpty(t, manifest)
				var err error
				idsByType, err = attachmentIDsByTypeForExecution(manifest)
				require.NoError(t, err)
				return toolEventsForExecution(
					jsonToolCallForExecution("tc_file", "attachment_get", map[string]any{"attachment_id": idsByType["file"]}),
					jsonToolCallForExecution("tc_dir", "attachment_get", map[string]any{"attachment_id": idsByType["dir"]}),
					jsonToolCallForExecution("tc_image", "attachment_get", map[string]any{"attachment_id": idsByType["image"], "include_base64": true}),
					jsonToolCallForExecution("tc_pdf", "attachment_get", map[string]any{"attachment_id": idsByType["pdf"]}),
				), nil
			default:
				fileResult := toolResultContentForExecution(req.Messages, "tc_file")
				dirResult := toolResultContentForExecution(req.Messages, "tc_dir")
				imageResult := toolResultContentForExecution(req.Messages, "tc_image")
				pdfResult := toolResultContentForExecution(req.Messages, "tc_pdf")
				assert.Contains(t, fileResult, "brief content from source file")
				assert.Contains(t, dirResult, `"kind":"directory"`)
				assert.Contains(t, dirResult, `"name":"note.md"`)
				assert.Contains(t, imageResult, `"encoding":"base64"`)
				assert.Contains(t, pdfResult, `"mime_type":"application/pdf"`)
				assert.Contains(t, pdfResult, `"content_available":false`)
				assert.Contains(t, pdfResult, `"name":"spec.pdf"`)
				return []llm.ChatEvent{
					{Type: llm.EventTextDelta, Text: "图片和文件全流程验证完成"},
					{Type: llm.EventDone},
				}, nil
			}
		},
	}
	recorder := runtimeevents.NewRecorder()
	run, err := StartIngressRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
		Recorder:     recorder,
	}, runtimeport.IngressRequest{
		RunID:       "run_full_path",
		RequestedID: "assistant",
		SessionID:   "session-full-path",
		Channel:     "http",
		Envelope: input.InputEnvelope{
			SessionID: "session-full-path",
			Blocks: []input.InputBlock{
				{Type: "text", Text: "请检查输入中的图片和文件。"},
				{Type: "file", Name: "brief.txt", Path: sourceFile},
				{Type: "dir", Name: "reference", Path: filepath.Join(fixturesDir, "reference")},
				{Type: "image", Name: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
				{Type: "pdf", Name: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.4 full path test")},
			},
		},
	})
	require.NoError(t, err)
	drainManagedRun(t, run)
	require.NotNil(t, idsByType)

	assetsDir := filepath.Join(root, "anyai", "assets", "run_full_path")
	for _, typ := range []string{"file", "image", "pdf"} {
		assert.DirExists(t, filepath.Join(assetsDir, idsByType[typ]))
	}
	for _, typ := range []string{"file", "image", "pdf"} {
		assert.NotContains(t, provider.Requests()[0].Messages[0].Content, filepath.Join("assets", "run_full_path", idsByType[typ]))
	}
	assert.FileExists(t, filepath.Join(assetsDir, idsByType["file"], "brief.txt"))
	assert.FileExists(t, filepath.Join(assetsDir, idsByType["image"], "diagram.png"))
	assert.FileExists(t, filepath.Join(assetsDir, idsByType["pdf"], "spec.pdf"))

	events := recorder.ListRunEvents("run_full_path")
	require.NotEmpty(t, events)
	var sawInputNormalized, sawCompactSessionImage bool
	var storedAttachmentTypes []string
	for _, event := range events {
		switch event.Name {
		case runtimeevents.EventInputNormalized:
			sawInputNormalized = true
			assert.Equal(t, 5, event.Payload["block_count"])
			assert.Equal(t, 4, event.Payload["attachment_count"])
		case runtimeevents.EventAttachmentStored:
			if typ, _ := event.Payload["type"].(string); typ != "" {
				storedAttachmentTypes = append(storedAttachmentTypes, typ)
			}
		case runtimeevents.EventSessionInputStored:
			images, _ := event.Payload["images"].([]map[string]any)
			if len(images) == 0 {
				if raw, ok := event.Payload["images"].([]any); ok && len(raw) > 0 {
					item, _ := raw[0].(map[string]any)
					_, hasData := item["data"]
					sawCompactSessionImage = !hasData
				}
			} else {
				_, hasData := images[0]["data"]
				sawCompactSessionImage = !hasData
			}
		}
	}
	assert.True(t, sawInputNormalized)
	assert.ElementsMatch(t, []string{"file", "dir", "image", "pdf"}, storedAttachmentTypes)
	assert.True(t, sawCompactSessionImage, "session input event should store image refs, not base64 data")
}

func TestStartManagedRunPropagatesTaskIDIntoRuntimeContext(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: workspace,
		},
	}

	provider := &contextCapturingProvider{response: "done"}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
	}, runtimeport.RunRequest{
		RunID:     "run_task",
		AgentID:   "assistant",
		SessionID: "session-task",
		TaskID:    "task_current",
		Envelope:  input.NewEnvelopeFromText("session-task", "finish this child task"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	contexts := provider.Contexts()
	require.NotEmpty(t, contexts)
	assert.Equal(t, "task_current", contexts[0].TaskID)
	assert.Equal(t, "session-task", contexts[0].SessionID)
	assert.Equal(t, "run_task", contexts[0].RunID)
}

func TestStartManagedRunPersistsProvidedInputMessageID(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: workspace,
		},
	}

	store := session.NewStore(t.TempDir())
	provider := &capturingProvider{response: "done"}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: store,
	}, runtimeport.RunRequest{
		RunID:     "run_message_id",
		AgentID:   "assistant",
		SessionID: "session-message-id",
		MessageID: "e_client_supplied",
		Envelope:  input.NewEnvelopeFromText("session-message-id", "hello with id"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	sess, err := store.Load("assistant", "session-message-id")
	require.NoError(t, err)
	history := sess.History()
	require.NotEmpty(t, history)
	assert.Equal(t, "e_client_supplied", history[0].ID)
}

func TestStartManagedRunExtendsTimeoutWhileStreamingProgressContinues(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Runtime.IdleTimeoutMS = 60
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/stream-model",
			Workspace: workspace,
		},
	}

	provider := &streamingProvider{
		segments: []string{"part-1", "part-2", "part-3"},
		delay:    25 * time.Millisecond,
	}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
	}, runtimeport.RunRequest{
		AgentID:   "assistant",
		SessionID: "session-stream",
		Envelope:  input.NewEnvelopeFromText("session-stream", "keep streaming"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)
}

func TestStartManagedRunExtendsTimeoutWhileOnlyActivityEventsContinue(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Runtime.IdleTimeoutMS = 40
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/activity-model",
			Workspace: workspace,
		},
	}

	provider := &activityHeartbeatProvider{
		heartbeats: 8,
		delay:      10 * time.Millisecond,
		response:   "finished after heartbeats",
	}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
	}, runtimeport.RunRequest{
		AgentID:   "assistant",
		SessionID: "session-activity",
		Envelope:  input.NewEnvelopeFromText("session-activity", "keep thinking"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)
}

func TestStartManagedRunPublishesRunActivityEvents(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agent")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.Runtime.IdleTimeoutMS = 40
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/activity-model",
			Workspace: workspace,
		},
	}

	provider := &activityHeartbeatProvider{
		heartbeats: 2,
		delay:      10 * time.Millisecond,
		response:   "finished after heartbeats",
	}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    map[string]llm.LLMProvider{"test": provider},
		Config:       cfg,
		SessionStore: session.NewStore(t.TempDir()),
	}, runtimeport.RunRequest{
		AgentID:   "assistant",
		SessionID: "session-activity",
		Envelope:  input.NewEnvelopeFromText("session-activity", "keep thinking"),
	})
	require.NoError(t, err)

	var activityCount int
	for event := range run.Events {
		if message := runtimeevents.FailureMessage(event); message != "" {
			t.Fatalf("managed run failed: %s", message)
		}
		if event.Name == runtimeevents.EventRunActivity {
			activityCount++
		}
	}

	assert.GreaterOrEqual(t, activityCount, 2)
}

func TestHarnessCodingTechLeadDirectQuestionBuildsExpectedPromptAndTools(t *testing.T) {
	project, req := captureHarnessCodingTechLeadDirectQuestion(t)
	agentCfg, ok := project.Config.GetAgent("tech-lead")
	require.True(t, ok)
	_, expectedModel := llm.ParseProviderModel(agentCfg.Model)
	assert.Equal(t, expectedModel, req.Model)
	assert.Contains(t, req.SystemPrompt, "让你介绍当前项目/Agent 能力、解释界面状态")
	assert.Contains(t, req.SystemPrompt, "不进入多阶段流程，不调用 callagent")
	assert.Contains(t, req.SystemPrompt, "任何一次运行都必须给用户一个可见的文本结果")

	var toolNames []string
	for _, tool := range req.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	assert.Contains(t, toolNames, "read_file")
	assert.Contains(t, toolNames, "edit_file")
	assert.Contains(t, toolNames, "bash")
	assert.Contains(t, toolNames, "callagent")
	assert.NotContains(t, toolNames, "task_status")
	assert.NotContains(t, toolNames, "task_wait")
	assert.NotContains(t, toolNames, "task_cancel")

	require.Len(t, req.Messages, 1)
	assert.Equal(t, "user", req.Messages[0].Role)
	assert.Equal(t, "请简要介绍一下你能在这个 harness-coding 工作流里做什么", req.Messages[0].Content)
}

func TestHarnessCodingTechLeadDirectQuestionLiveOpenAICompatible(t *testing.T) {
	if os.Getenv("ANYAI_LIVE_HARNESS_CODING") != "1" {
		t.Skip("set ANYAI_LIVE_HARNESS_CODING=1 to run the live openai-compatible harness-coding probe")
	}

	project, req := captureHarnessCodingTechLeadDirectQuestion(t)

	providerCfg, ok := project.Config.Providers["openai"]
	require.True(t, ok, "openai provider must exist in harness-coding config")
	require.NotEmpty(t, providerCfg.BaseURL)

	provider := llm.NewOpenAIProvider(providerCfg.APIKey, providerCfg.BaseURL, providerCfg.Headers)
	stream, err := provider.ChatStream(context.Background(), req)
	require.NoError(t, err)

	var text strings.Builder
	toolStarts := 0
	toolDone := 0
	for event := range stream {
		switch event.Type {
		case llm.EventTextDelta:
			text.WriteString(event.Text)
		case llm.EventToolCallStart:
			toolStarts++
		case llm.EventToolCallDone:
			toolDone++
		case llm.EventError:
			require.NoError(t, event.Error)
		}
	}

	t.Logf("live openai-compatible text=%q tool_starts=%d tool_done=%d", text.String(), toolStarts, toolDone)
	if strings.TrimSpace(text.String()) == "" && toolStarts == 0 && toolDone == 0 {
		rawBody, rawErr := probeRawOpenAICompatibleResponse(providerCfg, req)
		if rawErr != nil {
			t.Logf("raw openai-compatible probe error: %v", rawErr)
		} else {
			t.Logf("raw openai-compatible sse=%s", rawBody)
		}
		t.Fatalf("live openai-compatible call returned neither text nor tool calls")
	}
}

func captureHarnessCodingTechLeadDirectQuestion(t *testing.T) (*registry.Project, llm.ChatRequest) {
	t.Helper()

	projectDir, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "harness-coding"))
	require.NoError(t, err)

	project, err := registry.LoadProject(projectDir)
	require.NoError(t, err)

	provider := &capturingProvider{response: "当然可以，我可以先直接回答问题，复杂任务再组织多 agent workflow。"}
	sessionStore := session.NewStore(t.TempDir())
	agentCfg, ok := project.Config.GetAgent("tech-lead")
	require.True(t, ok)
	providerName, _ := llm.ParseProviderModel(agentCfg.Model)
	providers := map[string]llm.LLMProvider{providerName: provider}
	agentRunner := noopAgentCallRunner{agents: []tools.AgentInfo{{ID: "coder", Name: "Coder"}, {ID: "reviewer", Name: "Reviewer"}}}
	run, err := StartManagedRun(context.Background(), runtimeport.ExecutionDeps{
		Providers:    providers,
		Config:       project.Config,
		SessionStore: sessionStore,
		AgentRunner:  agentRunner,
	}, runtimeport.RunRequest{
		AgentID:   "tech-lead",
		SessionID: "direct-qna",
		Envelope:  input.NewEnvelopeFromText("direct-qna", "请简要介绍一下你能在这个 harness-coding 工作流里做什么"),
	})
	require.NoError(t, err)
	drainManagedRun(t, run)

	requests := provider.Requests()
	require.Len(t, requests, 1)
	return project, requests[0]
}

func probeRawOpenAICompatibleResponse(providerCfg config.ProviderConfig, req llm.ChatRequest) (string, error) {
	openaiReq := openai.ChatCompletionRequest{
		Model:  req.Model,
		Stream: true,
	}
	if req.SystemPrompt != "" {
		openaiReq.Messages = append(openaiReq.Messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: req.SystemPrompt,
		})
	}
	for _, msg := range req.Messages {
		openaiReq.Messages = append(openaiReq.Messages, openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	for _, tool := range req.Tools {
		var params any
		if err := json.Unmarshal(tool.Parameters, &params); err != nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		openaiReq.Tools = append(openaiReq.Tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  params,
			},
		})
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Model)), "gpt-5") {
		openaiReq.MaxCompletionTokens = req.MaxTokens
	} else {
		openaiReq.MaxTokens = req.MaxTokens
	}

	body, err := json.Marshal(openaiReq)
	if err != nil {
		return "", err
	}

	baseURL := strings.TrimRight(providerCfg.BaseURL, "/")
	if parsed, err := http.NewRequest(http.MethodPost, baseURL, nil); err == nil {
		if parsed.URL.Path == "" || parsed.URL.Path == "/" {
			baseURL += "/v1"
		}
	}
	url := baseURL + "/chat/completions"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if providerCfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+providerCfg.APIKey)
	}
	for key, value := range providerCfg.Headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func drainManagedRun(t *testing.T, run *runtimeport.ManagedRun) {
	t.Helper()

	timeout := time.After(3 * time.Second)
	for {
		select {
		case event, ok := <-run.Events:
			if !ok {
				return
			}
			if message := runtimeevents.FailureMessage(event); message != "" {
				t.Fatalf("managed run failed: %s", message)
			}
		case <-timeout:
			t.Fatal("timeout waiting for managed run to finish")
		}
	}
}

func jsonToolCallForExecution(id, name string, payload map[string]any) llm.ToolCall {
	input, _ := json.Marshal(payload)
	return llm.ToolCall{
		ID:    id,
		Name:  name,
		Input: input,
	}
}

func toolEventsForExecution(calls ...llm.ToolCall) []llm.ChatEvent {
	events := make([]llm.ChatEvent, 0, len(calls)*2+1)
	for _, call := range calls {
		call := call
		events = append(events, llm.ChatEvent{Type: llm.EventToolCallStart, ToolCall: &call})
		events = append(events, llm.ChatEvent{Type: llm.EventToolCallDone, ToolCall: &call})
	}
	events = append(events, llm.ChatEvent{Type: llm.EventDone})
	return events
}

func toolResultContentForExecution(messages []llm.Message, toolCallID string) string {
	for _, msg := range messages {
		if msg.Role == llm.MessageRoleUser && msg.ToolCallID == toolCallID {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}

func attachmentIDsByTypeForExecution(manifestJSON string) (map[string]string, error) {
	var manifest []map[string]any
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, item := range manifest {
		typ, _ := item["type"].(string)
		id, _ := item["id"].(string)
		if typ != "" && id != "" {
			ids[typ] = id
		}
	}
	for _, typ := range []string{"file", "dir", "image", "pdf"} {
		if strings.TrimSpace(ids[typ]) == "" {
			return nil, fmt.Errorf("manifest missing id for %s", typ)
		}
	}
	return ids, nil
}

func executionManagedDoc(title string, meta map[string]string, section string, lines ...string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\nGenerated at: 2026-04-19T00:00:00Z\n\n")
	for key, value := range meta {
		b.WriteString("- ")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\n")
	}
	b.WriteString("\n## ")
	b.WriteString(section)
	b.WriteString("\n")
	for _, line := range lines {
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
