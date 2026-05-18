package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type compactFallbackProvider struct {
	events    []ChatEvent
	lastReq   ChatRequest
	seenCalls int
}

func (p *compactFallbackProvider) ChatStream(_ context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	p.lastReq = req
	p.seenCalls++
	events := p.events
	ch := make(chan ChatEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (p *compactFallbackProvider) Models() []ModelInfo {
	return []ModelInfo{{ID: "mock", Name: "Mock", Provider: "mock"}}
}

func (p *compactFallbackProvider) Compact(ctx context.Context, req CompactRequest) (CompactResponse, error) {
	return compactViaChatStream(ctx, p.ChatStream, req)
}

type compactNativeProvider struct {
	response CompactResponse
	err      error
	lastReq  CompactRequest
	calls    int
}

func (p *compactNativeProvider) ChatStream(_ context.Context, _ ChatRequest) (<-chan ChatEvent, error) {
	ch := make(chan ChatEvent)
	close(ch)
	return ch, nil
}

func (p *compactNativeProvider) Models() []ModelInfo {
	return []ModelInfo{{ID: "mock", Name: "Mock", Provider: "mock"}}
}

func (p *compactNativeProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider: "mock",
		Compact:  CompactCapabilityNativeAvailable,
	}
}

func (p *compactNativeProvider) Compact(_ context.Context, req CompactRequest) (CompactResponse, error) {
	p.lastReq = req
	p.calls++
	if p.err != nil {
		return CompactResponse{}, p.err
	}
	return p.response, nil
}

func TestCompactWithProviderFallsBackToChatStream(t *testing.T) {
	provider := &compactFallbackProvider{
		events: []ChatEvent{
			{Type: EventTextDelta, Text: "summary"},
			{Type: EventDone},
		},
	}

	resp, err := CompactWithProvider(context.Background(), provider, CompactRequest{
		Model:        "mock-model",
		Messages:     []Message{{Role: "user", Content: "older history"}},
		MaxTokens:    128,
		Temperature:  0.01,
		SystemPrompt: "compact system",
		UserPrompt:   "compact user",
	})
	require.NoError(t, err)
	assert.Equal(t, "summary", resp.Summary)
	assert.Equal(t, CompactStrategyChatFallback, resp.Strategy)
	assert.Equal(t, 1, provider.seenCalls)
	require.Len(t, provider.lastReq.Messages, 2)
	assert.Equal(t, "compact user", provider.lastReq.Messages[1].Content)
	assert.Equal(t, "compact system", provider.lastReq.SystemPrompt)

	caps := DescribeProviderCapabilities(provider)
	assert.Equal(t, "mock", caps.Provider)
	assert.Equal(t, CompactCapabilityUnknown, caps.Compact)
}

func TestCompactWithProviderUsesProviderCompact(t *testing.T) {
	provider := &compactNativeProvider{
		response: CompactResponse{
			Summary:  "native summary",
			Strategy: CompactStrategyNative,
		},
	}

	resp, err := CompactWithProvider(context.Background(), provider, CompactRequest{
		Model:      "mock-model",
		UserPrompt: "compact user",
	})
	require.NoError(t, err)
	assert.Equal(t, "native summary", resp.Summary)
	assert.Equal(t, CompactStrategyNative, resp.Strategy)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, "compact user", provider.lastReq.UserPrompt)

	caps := DescribeProviderCapabilities(provider)
	assert.Equal(t, "mock", caps.Provider)
	assert.Equal(t, CompactCapabilityNativeAvailable, caps.Compact)
}

func TestCompactWithProviderPropagatesProviderError(t *testing.T) {
	provider := &compactNativeProvider{
		err: errors.New("native compact failed"),
	}

	_, err := CompactWithProvider(context.Background(), provider, CompactRequest{
		UserPrompt: "compact user",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "native compact failed")
	assert.Equal(t, 1, provider.calls)
}
