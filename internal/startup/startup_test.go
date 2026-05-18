package startup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	"github.com/Isites/anyai/internal/runtime/agent"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeexecution "github.com/Isites/anyai/internal/runtime/execution"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/testutil"
)

func TestApplyLaunchModeChannelsChatOnlyExposesCLI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Channels.Telegram.Token = "telegram-token"
	cfg.Channels.WhatsApp.DBPath = filepath.Join(t.TempDir(), "whatsapp.db")

	applyLaunchModeChannels(cfg, LaunchModeChat, t.TempDir())

	assert.Equal(t, []string{"cli"}, cfg.ActiveChannels)

	summary := agent.ConfigSummary(cfg)
	assert.Contains(t, summary, "Configured channels: cli")
	assert.NotContains(t, summary, "telegram")
	assert.NotContains(t, summary, "whatsapp")
}

func TestApplyLaunchModeChannelsStartExposesConfiguredMessagingOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Channels.CLI.Enabled = true
	cfg.Channels.Telegram.Token = "telegram-token"
	cfg.Channels.WhatsApp.DBPath = filepath.Join(t.TempDir(), "whatsapp.db")

	applyLaunchModeChannels(cfg, LaunchModeStart, t.TempDir())

	assert.Equal(t, []string{"telegram", "whatsapp"}, cfg.ActiveChannels)

	summary := agent.ConfigSummary(cfg)
	assert.Contains(t, summary, "Configured channels: telegram, whatsapp")
	assert.NotContains(t, summary, "http")
	assert.NotContains(t, summary, "cli")
}

func TestStartGatewayWithConfigChatUsesUnifiedStartupAndHeadlessCLI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Bindings = nil
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
			Entry:     true,
		},
	}
	cfg.Providers = map[string]config.ProviderConfig{
		"test": {
			Kind:    "openai-compatible",
			BaseURL: "http://127.0.0.1:11434/v1",
		},
	}
	cfg.Providers = map[string]config.ProviderConfig{
		"test": {
			Kind:    "openai-compatible",
			BaseURL: "http://127.0.0.1:11434/v1",
		},
	}

	result, err := StartGatewayWithConfig(cfg, "test", Options{
		FallbackAgentID: "assistant",
		LaunchMode:      LaunchModeChat,
		NonInteractive:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	t.Cleanup(result.Cleanup)

	assert.True(t, result.CanServe())
	require.NotNil(t, result.ChannelManager())
	require.NotNil(t, result.TaskStore())
	assert.Equal(t, []string{"cli"}, result.ActiveChannels())
	assert.Equal(t, []string{"cli"}, result.Config().ActiveChannels)
}

func TestStartGatewayWithConfigHeadlessCLIInjectsIntoUnifiedChatFlow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Bindings = nil
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
			Entry:     true,
		},
	}
	cfg.Providers = map[string]config.ProviderConfig{
		"test": {
			Kind:    "openai-compatible",
			BaseURL: "http://127.0.0.1:11434/v1",
		},
	}

	result, err := StartGatewayWithConfig(cfg, "test", Options{
		FallbackAgentID: "assistant",
		LaunchMode:      LaunchModeChat,
		NonInteractive:  true,
		ProviderOverrides: map[string]llm.LLMProvider{
			"test": &startupMockProvider{response: "cli reply"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.CLIChannel())
	t.Cleanup(result.Cleanup)

	require.NoError(t, result.CLIChannel().InjectMessage(gateway.InboundMessage{
		SenderID:   "local-main",
		SenderName: "User",
		Text:       "你好",
		ChatType:   gateway.ChatTypeDirect,
	}))

	store := session.NewStore(filepath.Join(cfg.RuntimeDataDir(), "sessions"))
	rt := result.Gateway().RawRuntime()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sess, loadErr := store.Load("assistant", "cli_local-main")
		if loadErr == nil {
			history := sess.History()
			if len(history) >= 2 {
				var userMsg, assistantMsg session.MessageData
				require.NoError(t, json.Unmarshal(history[0].Data, &userMsg))
				require.NoError(t, json.Unmarshal(history[1].Data, &assistantMsg))
				assert.Equal(t, "user", history[0].Role)
				assert.Equal(t, "assistant", history[1].Role)
				assert.Equal(t, "你好", userMsg.Text)
				assert.Equal(t, "cli reply", assistantMsg.Text)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	var runIDs []string
	var sessions []session.SessionInfo
	var logs []map[string]any
	if rt != nil {
		for _, run := range rt.ListRuns() {
			runIDs = append(runIDs, run.ID+":"+run.SessionID+":"+string(run.Status))
		}
		sessions, _ = rt.ListSessions("assistant")
	}
	logs = result.Gateway().LogEntriesPayload(20)
	t.Fatalf("timed out waiting for headless cli session history; runs=%v sessions=%v logs=%v", runIDs, sessions, logs)
}

func TestStartGatewayWithConfigStartKeepsGatewayServerAndNoCLIChannel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
			Entry:     true,
		},
	}
	cfg.Providers = map[string]config.ProviderConfig{
		"test": {
			Kind:    "openai-compatible",
			BaseURL: "http://127.0.0.1:11434/v1",
		},
	}

	result, err := StartGatewayWithConfig(cfg, "test", Options{
		FallbackAgentID: "assistant",
		LaunchMode:      LaunchModeStart,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	t.Cleanup(result.Cleanup)

	assert.True(t, result.CanServe())
	require.NotNil(t, result.ChannelManager())
	assert.Empty(t, result.ActiveChannels())
	assert.Empty(t, result.Config().ActiveChannels)
}

func TestResultWaitReturnsAfterCleanup(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
			Entry:     true,
		},
	}
	cfg.Providers = map[string]config.ProviderConfig{
		"test": {
			Kind:    "openai-compatible",
			BaseURL: "http://127.0.0.1:11434/v1",
		},
	}

	result, err := StartGatewayWithConfig(cfg, "test", Options{
		FallbackAgentID: "assistant",
		LaunchMode:      LaunchModeChat,
		NonInteractive:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	t.Cleanup(result.Cleanup)

	done := make(chan error, 1)
	go func() {
		done <- result.Wait(context.Background())
	}()

	result.Cleanup()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("result.Wait did not return after cleanup")
	}
}

func TestBuildCoreRuntimeDoesNotMigrateLegacyProjectMemoryDir(t *testing.T) {
	root := t.TempDir()
	legacyFile := filepath.Join(root, "memory", "long-term", "policy.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyFile), 0o755))
	require.NoError(t, os.WriteFile(legacyFile, []byte("# Policy\n\n- Always confirm refunds with logistics first.\n"), 0o644))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
			Entry:     true,
		},
	}
	cfg.Memory.Enabled = true
	cfg.Memory.Dir = ""

	core, err := BuildCoreRuntimeWithConfig(cfg, Options{LaunchMode: LaunchModeStart})
	require.NoError(t, err)
	require.NotNil(t, core.Memory)
	require.NotNil(t, core.Runtime)
	assert.NotEmpty(t, core.Runtime.EventStorageDir())

	targetFile := filepath.Join(root, "anyai", "memory", "long-term", "policy.md")
	_, statErr := os.Stat(targetFile)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	assert.Empty(t, core.Memory.Entries())
}

func TestBuildCoreRuntimeUsesRuntimeAsAgentRunner(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
			Entry:     true,
		},
	}

	core, err := BuildCoreRuntimeWithConfig(cfg, Options{LaunchMode: LaunchModeStart})
	require.NoError(t, err)
	require.NotNil(t, core.Runtime)
	require.NotNil(t, core.AgentRunner)
	assert.Same(t, core.Runtime, core.AgentRunner)
}

func TestRuntimeUpdaterAppliesRuntimeSpecResourcesAndGatewayView(t *testing.T) {
	root := t.TempDir()
	sharedDir := filepath.Join(root, "common", "skills")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "first.md"), []byte("---\nname: first\n---\nFirst skill."), 0o644))

	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.ProjectConfigDir = root
	cfg.SharedSkillsDir = sharedDir
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:                  "assistant",
			Name:                "Assistant",
			Model:               "test/mock-model",
			Workspace:           t.TempDir(),
			Entry:               true,
			InheritSharedSkills: true,
		},
	}

	provider := &startupMockProvider{response: "ok"}
	core, err := BuildCoreRuntimeWithConfig(cfg, Options{
		LaunchMode: LaunchModeChat,
		ProviderOverrides: map[string]llm.LLMProvider{
			"test": provider,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, core.Runtime.Resources())
	require.Len(t, core.Runtime.Resources().SharedSkills(), 1)

	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "second.md"), []byte("---\nname: second\n---\nSecond skill."), 0o644))
	next := *cfg
	next.Bindings = []config.Binding{{AgentID: "assistant", Match: config.BindingMatch{Channel: "http"}}}
	updater := newRuntimeUpdater(core, Options{
		LaunchMode: LaunchModeStart,
		ProviderOverrides: map[string]llm.LLMProvider{
			"test": provider,
		},
	}, nil)

	require.NoError(t, updater.ApplyConfig(context.Background(), &next, ""))
	require.NotNil(t, core.Runtime.Resources())
	require.Len(t, core.Runtime.Resources().SharedSkills(), 2)
	assert.Equal(t, []string{}, core.Config.ActiveChannels)
	assert.Equal(t, "assistant", core.Gateway.ResolveIngressAgent(gateway.IngressRequest{Channel: "http"}))
}

type startupMockProvider struct {
	response         string
	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *startupMockProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	ch := make(chan llm.ChatEvent, 3)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *startupMockProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *startupMockProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "startup compact summary"}, nil
}

type startupCapturingProvider struct {
	response         string
	autoGoalFinalize bool
	autoGoalSeq      int

	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (p *startupCapturingProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}

	ch := make(chan llm.ChatEvent, 2)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *startupCapturingProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *startupCapturingProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "startup capturing compact summary"}, nil
}

func (p *startupCapturingProvider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...)
}

func TestStartGatewayWithConfigProviderOverridesServeUnifiedChat(t *testing.T) {
	host, port, err := reserveLocalGatewayAddrForTest()
	if err != nil && isLoopbackListenPermissionErrorForTest(err) {
		t.Skipf("loopback listen unavailable in this environment: %v", err)
	}
	require.NoError(t, err)

	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = projectRoot
	cfg.Gateway.Host = host
	cfg.Gateway.Port = port
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
			Entry:     true,
		},
	}

	result, err := StartGatewayWithConfig(cfg, "test", Options{
		FallbackAgentID: "assistant",
		LaunchMode:      LaunchModeStart,
		ProviderOverrides: map[string]llm.LLMProvider{
			"test": &startupMockProvider{response: "override reply"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	serverErr := make(chan error, 1)
	go func() {
		err := result.Serve()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	defer func() {
		result.Cleanup()
		<-serverErr
	}()

	baseURL := "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	require.NoError(t, waitForHealthForTest(baseURL, 10*time.Second, serverErr))

	body := []byte(`{"agent_id":"assistant","session_id":"demo-session","text":"你好"}`)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/chat?stream=1", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	streamBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(streamBody), "event: run.accepted")
	assert.Contains(t, string(streamBody), "event: run.started")
	assert.Contains(t, string(streamBody), "event: text.delta")
	assert.Contains(t, string(streamBody), "override reply")

	sessionResp, err := http.Get(baseURL + "/api/sessions/assistant/demo-session")
	require.NoError(t, err)
	defer sessionResp.Body.Close()

	sessionBody, err := io.ReadAll(sessionResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sessionResp.StatusCode)

	var parsed struct {
		Session struct {
			Events []struct {
				Name    string         `json:"name"`
				Payload map[string]any `json:"payload"`
			} `json:"events"`
		} `json:"session"`
	}
	require.NoError(t, json.Unmarshal(sessionBody, &parsed))
	require.Len(t, parsed.Session.Events, 2)
	assert.Equal(t, runtimeevents.EventSessionInputStored, parsed.Session.Events[0].Name)
	assert.Equal(t, "你好", parsed.Session.Events[0].Payload["text"])
	assert.Equal(t, runtimeevents.EventSessionOutputStored, parsed.Session.Events[1].Name)
	assert.Equal(t, "override reply", parsed.Session.Events[1].Payload["text"])

	sessionPath := filepath.Join(projectRoot, "anyai", "sessions", "assistant", "demo-session.jsonl")
	_, statErr := os.Stat(sessionPath)
	require.NoError(t, statErr)
}

func TestBuildCoreRuntimeLaunchModesShareSystemPromptAssembly(t *testing.T) {
	capturePrompt := func(mode LaunchMode) string {
		root := t.TempDir()
		provider := &startupCapturingProvider{response: "ok"}

		cfg := config.DefaultConfig()
		cfg.ProjectRoot = root
		cfg.ProjectConfigDir = root
		cfg.Agents.List = []config.AgentConfig{
			{
				ID:           "assistant",
				Name:         "Assistant",
				Model:        "test/mock-model",
				Workspace:    t.TempDir(),
				Entry:        true,
				SystemPrompt: "Always mention the shared startup path.",
			},
		}
		cfg.Providers = map[string]config.ProviderConfig{
			"test": {
				Kind:    "openai-compatible",
				BaseURL: "http://127.0.0.1:11434/v1",
			},
		}

		core, err := BuildCoreRuntimeWithConfig(cfg, Options{
			LaunchMode: mode,
			ProviderOverrides: map[string]llm.LLMProvider{
				"test": provider,
			},
		})
		require.NoError(t, err)
		require.NotNil(t, core.Runtime)

		run, err := runtimeexecution.StartManagedRun(context.Background(), core.Runtime.ExecutionDeps(), runtimeport.RunRequest{
			AgentID:   "assistant",
			SessionID: "prompt-check",
			Envelope:  input.NewEnvelopeFromText("prompt-check", "hello"),
		})
		require.NoError(t, err)
		for range run.Events {
		}

		requests := provider.Requests()
		require.NotEmpty(t, requests)
		return requests[0].SystemPrompt
	}

	chatPrompt := capturePrompt(LaunchModeChat)
	startPrompt := capturePrompt(LaunchModeStart)

	for _, prompt := range []string{chatPrompt, startPrompt} {
		assert.Contains(t, prompt, "## Runtime Identity")
		assert.Contains(t, prompt, "- Agent name: Assistant")
		assert.Contains(t, prompt, "- Agent id: assistant")
		assert.Contains(t, prompt, "The AnyAI runtime exposes real working tools")
		assert.Contains(t, prompt, "Always mention the shared startup path.")
		assert.Contains(t, prompt, "## Environment")
	}

	assert.Contains(t, chatPrompt, "Configured channels: cli")
	assert.NotContains(t, startPrompt, "Configured channels: http")
}

func reserveLocalGatewayAddrForTest() (string, int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", 0, err
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return "", 0, errors.New("unexpected listener address type")
	}
	return "127.0.0.1", addr.Port, nil
}

func isLoopbackListenPermissionErrorForTest(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied")
}

func waitForHealthForTest(baseURL string, timeout time.Duration, serverErr <-chan error) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-serverErr:
			if err != nil {
				return err
			}
			return errors.New("gateway stopped before healthy")
		default:
		}

		resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("gateway did not become healthy in time")
}
