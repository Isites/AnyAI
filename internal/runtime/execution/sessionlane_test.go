package execution

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingQueueProvider struct {
	mu           sync.Mutex
	requests     []llm.ChatRequest
	firstStarted chan struct{}
	releaseFirst chan struct{}

	autoGoalFinalize bool
	autoGoalSeq      int
}

func newBlockingQueueProvider() *blockingQueueProvider {
	return &blockingQueueProvider{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (p *blockingQueueProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	idx := len(p.requests)
	p.mu.Unlock()

	ch := make(chan llm.ChatEvent, 2)
	go func() {
		defer close(ch)
		if idx == 1 {
			close(p.firstStarted)
			select {
			case <-ctx.Done():
				ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
				return
			case <-p.releaseFirst:
			}
		}
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: "done"}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *blockingQueueProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock", Provider: "test"}}
}

func (p *blockingQueueProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "blocking queue compact summary"}, nil
}

func (p *blockingQueueProvider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...)
}

func TestSessionRunCoordinatorCollectQueuesWithoutPollutingTranscript(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Name:      "Assistant",
			Model:     "test/mock-model",
			Workspace: t.TempDir(),
		},
	}
	cfg.Runtime.Sessions.QueueMode = "collect"
	cfg.Runtime.Sessions.QueueDebounceMS = 1
	cfg.Runtime.Sessions.QueueMaxPending = 20

	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	provider := newBlockingQueueProvider()
	deps := runtimeport.ExecutionDeps{
		Providers:          map[string]llm.LLMProvider{"test": provider},
		Config:             cfg,
		SessionStore:       store,
		SessionCoordinator: NewCoordinator(),
		Recorder:           recorder,
	}

	run1, err := StartIngressRun(context.Background(), deps, runtimeport.IngressRequest{
		RequestedID: "assistant",
		SessionID:   "shared",
		Channel:     "http",
		Envelope:    input.NewEnvelopeFromText("shared", "first request"),
	})
	require.NoError(t, err)
	<-provider.firstStarted

	run2, err := StartIngressRun(context.Background(), deps, runtimeport.IngressRequest{
		RequestedID: "assistant",
		SessionID:   "shared",
		Channel:     "http",
		Envelope:    input.NewEnvelopeFromText("shared", "second request"),
	})
	require.NoError(t, err)

	run3, err := StartIngressRun(context.Background(), deps, runtimeport.IngressRequest{
		RequestedID: "assistant",
		SessionID:   "shared",
		Channel:     "http",
		Envelope:    input.NewEnvelopeFromText("shared", "who are you"),
	})
	require.NoError(t, err)

	sess, err := store.Load("assistant", "shared")
	require.NoError(t, err)
	history := session.SerializeHistory(sess)
	require.Len(t, history, 1)
	assert.Equal(t, "first request", history[0]["text"])

	close(provider.releaseFirst)
	drainManagedRun(t, run1)
	drainManagedRun(t, run2)
	drainManagedRun(t, run3)

	requests := provider.Requests()
	require.Len(t, requests, 2)
	lastUser := ""
	for i := len(requests[1].Messages) - 1; i >= 0; i-- {
		if requests[1].Messages[i].Role == "user" {
			lastUser = requests[1].Messages[i].Content
			break
		}
	}
	assert.Contains(t, lastUser, "[Earlier pending user turns for context]")
	assert.Contains(t, lastUser, "second request")
	assert.Contains(t, lastUser, "[Current message - respond to this]")
	assert.Contains(t, lastUser, "who are you")

	record2, ok := recorder.GetRun(run2.RunID)
	require.True(t, ok)
	assert.Equal(t, runtimeevents.RunStatusCompleted, record2.Status)
	assert.Contains(t, record2.Output, "merged into followup run")

	finalSession, err := store.Load("assistant", "shared")
	require.NoError(t, err)
	texts := make([]string, 0, len(finalSession.History()))
	for _, entry := range finalSession.History() {
		if entry.Type != session.EntryTypeMessage {
			continue
		}
		var msg session.MessageData
		require.NoError(t, json.Unmarshal(entry.Data, &msg))
		if strings.HasPrefix(strings.TrimSpace(msg.Text), "[Runtime goal continuation]") {
			continue
		}
		texts = append(texts, msg.Text)
	}
	assert.Equal(t, 4, len(texts))
	assert.Equal(t, "first request", texts[0])
	assert.Equal(t, "done", texts[1])
	assert.True(t, strings.Contains(texts[2], "who are you"))
	assert.Equal(t, "done", texts[3])
}
