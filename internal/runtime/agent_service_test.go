package runtime

import (
	"context"
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/testutil"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type agentServiceMockProvider struct {
	response string

	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *agentServiceMockProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
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

func (p *agentServiceMockProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{Provider: "test", ID: "model"}}
}

func (p *agentServiceMockProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "agent service compact summary"}, nil
}

func TestAgentServiceRunSyncExecutesAgentRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "assistant", Model: "test/model", Workspace: t.TempDir()},
	}

	service := NewAgentService(func() runtimeport.ExecutionDeps {
		return runtimeport.ExecutionDeps{
			Config: cfg,
			Providers: map[string]llm.LLMProvider{
				"test": &agentServiceMockProvider{response: "hello from agent service"},
			},
		}
	})

	output, err := service.RunSync(context.Background(), "assistant", "daemon", "say hi", tools.ExtraToolDeps{})
	require.NoError(t, err)
	assert.Equal(t, "hello from agent service", output)
}
