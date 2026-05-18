package gateway

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/runtime/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Isites/anyai/internal/config"
	gatewayrouter "github.com/Isites/anyai/internal/gateway/router"
	airuntime "github.com/Isites/anyai/internal/runtime"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeexecution "github.com/Isites/anyai/internal/runtime/execution"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/testutil"
)

// mockChannel is a test double for the Channel interface.
type mockChannel struct {
	name    string
	inbound chan InboundMessage
	sent    []OutboundMessage
	mu      sync.Mutex
	status  ChannelStatus
	onSend  func(OutboundMessage) // optional callback
	events  []RunEvent
}

func newMockChannel(name string) *mockChannel {
	return &mockChannel{
		name:    name,
		inbound: make(chan InboundMessage, 10),
		status:  StatusDisconnected,
	}
}

func (m *mockChannel) Name() string { return m.name }

func (m *mockChannel) Connect(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = StatusConnected
	return nil
}

func (m *mockChannel) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = StatusDisconnected
	close(m.inbound)
	return nil
}

func (m *mockChannel) Send(_ context.Context, msg OutboundMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	if m.onSend != nil {
		m.onSend(msg)
	}
	return nil
}

func (m *mockChannel) Receive() <-chan InboundMessage {
	return m.inbound
}

func (m *mockChannel) HandleRunEvent(_ context.Context, event RunEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockChannel) Status() ChannelStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func newChannelManagerRuntime(
	providers map[string]llm.LLMProvider,
	sessionStore *session.Store,
	cfg *config.Config,
) *airuntime.Runtime {
	deps := runtimeport.NewDependencySet(providers, sessionStore, cfg)
	deps.SetSessionCoordinator(runtimeexecution.NewCoordinator())
	rt := airuntime.WrapDependencies(deps)
	rt.SetAgentRunner(rt)
	return rt
}

func newChannelManagerGateway(
	providers map[string]llm.LLMProvider,
	sessionStore *session.Store,
	cfg *config.Config,
	r RouteResolver,
) *Service {
	service := New(newChannelManagerRuntime(providers, sessionStore, cfg))
	service.SetRouteResolver(r)
	return service
}

func (m *mockChannel) Sent() []OutboundMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]OutboundMessage{}, m.sent...)
}

func (m *mockChannel) Events() []RunEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RunEvent{}, m.events...)
}

// mockProvider is a minimal LLM provider that returns a fixed response.
type mockProvider struct {
	response         string
	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *mockProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	ch := make(chan llm.ChatEvent, 10)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *mockProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "test-model", Name: "Test Model", Provider: "test"}}
}

func (p *mockProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "gateway compact summary"}, nil
}

type scriptedProvider struct {
	mu      sync.Mutex
	streams [][]llm.ChatEvent
	calls   int

	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *scriptedProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.mu.Lock()
	index := p.calls
	p.calls++
	var stream []llm.ChatEvent
	if index < len(p.streams) {
		stream = append([]llm.ChatEvent(nil), p.streams[index]...)
	}
	if events, ok := testutil.AutoGoalCompletionResponse(req, stream, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		stream = events
	}
	p.mu.Unlock()

	ch := make(chan llm.ChatEvent, len(stream))
	go func() {
		defer close(ch)
		for _, event := range stream {
			select {
			case <-ctx.Done():
				return
			case ch <- event:
			}
		}
	}()
	return ch, nil
}

func (p *scriptedProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "scripted", Name: "Scripted", Provider: "scripted"}}
}

func (p *scriptedProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "gateway scripted compact summary"}, nil
}

type blockingGroupProvider struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	releaseFirst chan struct{}

	autoGoalFinalize bool
	autoGoalSeq      int
}

func newBlockingGroupProvider() *blockingGroupProvider {
	return &blockingGroupProvider{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (p *blockingGroupProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		p.mu.Unlock()
		return testutil.StaticEventStream(events), nil
	}
	p.mu.Unlock()

	ch := make(chan llm.ChatEvent, 2)
	go func() {
		defer close(ch)
		if call == 1 {
			close(p.firstStarted)
			select {
			case <-ctx.Done():
				return
			case <-p.releaseFirst:
			}
			ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: "group one done"}
			ch <- llm.ChatEvent{Type: llm.EventDone}
			return
		}
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: "group two done"}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *blockingGroupProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "group-model", Name: "Group Model", Provider: "blocking"}}
}

func (p *blockingGroupProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "gateway blocking compact summary"}, nil
}

func TestChannelManagerRouting(t *testing.T) {
	tmpDir := t.TempDir()
	sessionStore := session.NewStore(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "agent1",
			Name:      "Agent One",
			Model:     "test/test-model",
			Workspace: t.TempDir(),
			Sandbox:   "none",
		},
		{
			ID:        "agent2",
			Name:      "Agent Two",
			Model:     "test/test-model",
			Workspace: t.TempDir(),
			Sandbox:   "none",
		},
	}

	bindings := []config.Binding{
		{AgentID: "agent1", Match: config.BindingMatch{Channel: "mock1"}},
		{AgentID: "agent2", Match: config.BindingMatch{Channel: "mock2"}},
	}

	r := gatewayrouter.NewRouter(bindings, "agent1")

	providers := map[string]llm.LLMProvider{
		"test": &mockProvider{response: "Hello from agent!"},
	}

	cm := NewChannelManager(newChannelManagerGateway(providers, sessionStore, cfg, r), "respond")

	// Create and register two mock channels
	mock1 := newMockChannel("mock1")
	mock2 := newMockChannel("mock2")
	cm.Register(mock1)
	cm.Register(mock2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.Start(ctx)
	require.NoError(t, err)

	// Send a message on mock1
	done := make(chan struct{})
	mock1.mu.Lock()
	mock1.onSend = func(msg OutboundMessage) {
		close(done)
	}
	mock1.mu.Unlock()

	mock1.inbound <- InboundMessage{
		Channel:    "mock1",
		AccountID:  "chat1",
		SenderID:   "user1",
		SenderName: "Test User",
		Text:       "hello",
		Timestamp:  time.Now(),
	}

	// Wait for response
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for response")
	}

	sent := mock1.Sent()
	require.Len(t, sent, 1)
	assert.Equal(t, "chat1", sent[0].ChatID) // Should reply to the chat ID
	assert.Equal(t, "Hello from agent!", sent[0].Text)
	assert.Equal(t, "agent1", sent[0].AgentID)
	assert.Equal(t, "mock1_chat1", sent[0].SessionID)
	assert.NotEmpty(t, sent[0].RunID)

	cm.Stop()
}

func TestChannelManagerFallbackChatID(t *testing.T) {
	// When AccountID is empty, should fall back to SenderID
	tmpDir := t.TempDir()
	sessionStore := session.NewStore(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.List[0].Workspace = t.TempDir()

	r := gatewayrouter.NewRouter(nil, "default")

	providers := map[string]llm.LLMProvider{
		"anthropic": &mockProvider{response: "response text"},
	}

	cm := NewChannelManager(newChannelManagerGateway(providers, sessionStore, cfg, r), "respond")

	mock := newMockChannel("test")
	cm.Register(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.Start(ctx)
	require.NoError(t, err)

	done := make(chan struct{})
	mock.mu.Lock()
	mock.onSend = func(msg OutboundMessage) {
		close(done)
	}
	mock.mu.Unlock()

	mock.inbound <- InboundMessage{
		Channel:    "test",
		SenderID:   "sender123",
		SenderName: "Test",
		Text:       "hi",
		Timestamp:  time.Now(),
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for response")
	}

	sent := mock.Sent()
	require.Len(t, sent, 1)
	assert.Equal(t, "sender123", sent[0].ChatID) // Falls back to SenderID
	assert.Equal(t, "response text", sent[0].Text)

	cm.Stop()
}

func TestChannelManagerForwardRunEventIncludesToolRecoveryEvents(t *testing.T) {
	cfg := config.DefaultConfig()
	runtime := newChannelManagerRuntime(nil, nil, cfg)
	cm := NewChannelManager(New(runtime), "respond")
	mock := newMockChannel("mock")

	run := runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
	}
	call := &llm.ToolCall{
		ID:    "tc_1",
		Name:  "read_file",
		Input: json.RawMessage(`{"path":"."}`),
	}

	for _, record := range runtimeevents.EventRecordsForAgentEvent(run, agent.AgentEvent{
		Type:     agent.EventToolRetry,
		ToolCall: call,
		ToolRetry: &agent.ToolRetryInfo{
			ToolName:    "read_file",
			Attempt:     2,
			MaxAttempts: 3,
			WaitMS:      250,
			ErrorClass:  "path_is_directory",
			Error:       "failed to read file: read .: is a directory",
			Decision:    "retry",
		},
	}) {
		require.NoError(t, mock.HandleRunEvent(context.Background(), cm.dispatch.channelRunEventFromRecord(gatewayEvent(record))))
	}
	for _, record := range runtimeevents.EventRecordsForAgentEvent(run, agent.AgentEvent{
		Type:     agent.EventToolWarning,
		ToolCall: call,
		ToolWarning: &agent.ToolWarningInfo{
			ToolName: "read_file",
			Detector: "generic_repeat",
			Count:    3,
			Message:  "tool warning",
			Blocked:  true,
		},
	}) {
		require.NoError(t, mock.HandleRunEvent(context.Background(), cm.dispatch.channelRunEventFromRecord(gatewayEvent(record))))
	}

	events := mock.Events()
	require.Len(t, events, 2)
	assert.Equal(t, "tool.retrying", events[0].Name)
	assert.Equal(t, "tool.warning", events[1].Name)
	assert.Equal(t, "read_file", events[0].Payload["tool"])
	assert.Equal(t, true, events[1].Payload["blocked"])
}

func TestChannelManagerGroupSessionsAreIsolatedByAccount(t *testing.T) {
	tmpDir := t.TempDir()
	sessionStore := session.NewStore(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.List[0].Workspace = t.TempDir()

	r := gatewayrouter.NewRouter(nil, "default")

	providers := map[string]llm.LLMProvider{
		"anthropic": &mockProvider{response: "group response"},
	}

	cm := NewChannelManager(newChannelManagerGateway(providers, sessionStore, cfg, r), "respond")

	mock := newMockChannel("test")
	cm.Register(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.Start(ctx)
	require.NoError(t, err)

	var (
		sendCount int
		sendMu    sync.Mutex
		done      = make(chan struct{})
	)
	mock.mu.Lock()
	mock.onSend = func(msg OutboundMessage) {
		sendMu.Lock()
		defer sendMu.Unlock()
		sendCount++
		if sendCount == 2 {
			close(done)
		}
	}
	mock.mu.Unlock()

	mock.inbound <- InboundMessage{
		Channel:    "test",
		AccountID:  "group-one",
		ChatType:   ChatTypeGroup,
		SenderID:   "user123",
		SenderName: "Tester",
		Text:       "message from group one",
		Timestamp:  time.Now(),
	}
	mock.inbound <- InboundMessage{
		Channel:    "test",
		AccountID:  "group-two",
		ChatType:   ChatTypeGroup,
		SenderID:   "user123",
		SenderName: "Tester",
		Text:       "message from group two",
		Timestamp:  time.Now(),
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for group responses")
	}

	require.True(t, sessionStore.Exists("default", "test_group_group-one"))
	require.True(t, sessionStore.Exists("default", "test_group_group-two"))

	groupOne, err := sessionStore.Load("default", "test_group_group-one")
	require.NoError(t, err)
	groupTwo, err := sessionStore.Load("default", "test_group_group-two")
	require.NoError(t, err)

	historyOne := groupOne.History()
	historyTwo := groupTwo.History()
	require.GreaterOrEqual(t, len(historyOne), 2)
	require.GreaterOrEqual(t, len(historyTwo), 2)

	assert.Contains(t, string(historyOne[0].Data), "message from group one")
	assert.Contains(t, string(historyTwo[0].Data), "message from group two")
	assert.Contains(t, string(historyOne[1].Data), "group response")
	assert.Contains(t, string(historyTwo[1].Data), "group response")

	cm.Stop()
}

func TestChannelManagerReliesOnRuntimeSessionLaneInsteadOfSenderSerialization(t *testing.T) {
	tmpDir := t.TempDir()
	sessionStore := session.NewStore(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.List[0].Workspace = t.TempDir()

	r := gatewayrouter.NewRouter(nil, "default")
	provider := newBlockingGroupProvider()
	providers := map[string]llm.LLMProvider{
		"anthropic": provider,
	}

	cm := NewChannelManager(newChannelManagerGateway(providers, sessionStore, cfg, r), "respond")
	mock := newMockChannel("test")
	cm.Register(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.Start(ctx)
	require.NoError(t, err)

	mock.inbound <- InboundMessage{
		Channel:    "test",
		AccountID:  "group-one",
		ChatType:   ChatTypeGroup,
		SenderID:   "shared-user",
		SenderName: "Tester",
		Text:       "message from first group",
		Timestamp:  time.Now(),
	}

	select {
	case <-provider.firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first grouped run to start")
	}

	mock.inbound <- InboundMessage{
		Channel:    "test",
		AccountID:  "group-two",
		ChatType:   ChatTypeGroup,
		SenderID:   "shared-user",
		SenderName: "Tester",
		Text:       "message from second group",
		Timestamp:  time.Now(),
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		sent := mock.Sent()
		var sawFirstGroupDone bool
		for _, msg := range sent {
			if msg.ChatID == "group-one" && msg.Text == "group one done" {
				sawFirstGroupDone = true
			}
			if msg.ChatID == "group-two" && msg.Text == "group two done" {
				close(provider.releaseFirst)
				waitDeadline := time.Now().Add(5 * time.Second)
				for !sawFirstGroupDone && time.Now().Before(waitDeadline) {
					time.Sleep(20 * time.Millisecond)
					for _, next := range mock.Sent() {
						if next.ChatID == "group-one" && next.Text == "group one done" {
							sawFirstGroupDone = true
							break
						}
					}
				}
				cm.Stop()
				require.True(t, sawFirstGroupDone, "first group response did not finish after release")
				return
			}
		}
		if time.Now().After(deadline) {
			close(provider.releaseFirst)
			cm.Stop()
			t.Fatal("second group response was blocked by channel-side sender serialization")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestChannelManagerForwardsChildTraceEvents(t *testing.T) {
	tmpDir := t.TempDir()
	sessionStore := session.NewStore(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Runtime.AgentCall.MaxParallel = 2
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "lead",
			Name:      "Lead",
			Model:     "leadprov/lead-model",
			Workspace: t.TempDir(),
			Sandbox:   "none",
			Tools: config.ToolPolicy{
				Allow: []string{"callagent"},
			},
		},
		{
			ID:        "worker",
			Name:      "Worker",
			Model:     "workerprov/worker-model",
			Workspace: t.TempDir(),
			Sandbox:   "none",
		},
	}

	delegateInput := json.RawMessage(`{"target_agent":"worker","task":"Investigate the request and return a concise answer"}`)
	leadProvider := &scriptedProvider{
		streams: [][]llm.ChatEvent{
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tool_1", Name: "callagent", Input: delegateInput}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{ID: "tool_1", Name: "callagent", Input: delegateInput}},
			},
			{
				{Type: llm.EventTextDelta, Text: "Lead summary"},
			},
		},
	}
	workerProvider := &scriptedProvider{
		streams: [][]llm.ChatEvent{
			{
				{Type: llm.EventTextDelta, Text: "Worker summary"},
			},
		},
	}
	providers := map[string]llm.LLMProvider{
		"leadprov":   leadProvider,
		"workerprov": workerProvider,
	}

	recorder := runtimeevents.NewRecorder()
	r := gatewayrouter.NewRouter(nil, "lead")
	runtime := newChannelManagerRuntime(providers, sessionStore, cfg)
	taskStore := task.NewStore()
	runtime.SetRecorder(recorder)
	service := New(runtime)
	service.SetRouteResolver(r)
	cm := NewChannelManager(service, "respond")
	configureGatewayTaskRuntime(t, runtime, cfg, taskStore, cm)

	mock := newMockChannel("test")
	cm.Register(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.Start(ctx)
	require.NoError(t, err)

	done := make(chan struct{})
	mock.mu.Lock()
	mock.onSend = func(msg OutboundMessage) {
		if msg.Text == "Lead summary" {
			close(done)
		}
	}
	mock.mu.Unlock()

	mock.inbound <- InboundMessage{
		Channel:    "test",
		AccountID:  "chat1",
		SenderID:   "user1",
		SenderName: "Test User",
		Text:       "please call another agent",
		Timestamp:  time.Now(),
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for agent-call response")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		events := mock.Events()
		require.NotEmpty(t, events)

		var sawParentStart bool
		var sawAgentCall bool
		var sawChildTaskStart bool
		var sawChildTaskComplete bool
		for _, event := range events {
			if event.AgentID == "lead" && event.Name == "run.started" {
				sawParentStart = true
			}
			if event.AgentID == "lead" && event.Name == "agent.call.started" {
				sawAgentCall = true
			}
			if event.AgentID == "worker" && event.Name == "task.started" {
				sawChildTaskStart = true
				assert.NotEmpty(t, event.SessionID)
				assert.NotEmpty(t, event.RunID)
				assert.Equal(t, "worker", event.Payload["target_agent"])
			}
			if event.AgentID == "worker" && event.Name == "task.completed" {
				sawChildTaskComplete = true
				assert.Equal(t, "Worker summary", event.Payload["summary"])
				assert.NotEmpty(t, event.Payload["task_id"])
			}
		}

		if sawParentStart && sawAgentCall && sawChildTaskStart && sawChildTaskComplete {
			break
		}
		if time.Now().After(deadline) {
			assert.True(t, sawParentStart)
			assert.True(t, sawAgentCall)
			assert.True(t, sawChildTaskStart)
			assert.True(t, sawChildTaskComplete)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cm.Stop()
}
