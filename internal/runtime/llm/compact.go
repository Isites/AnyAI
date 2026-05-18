package llm

import (
	"context"
	"fmt"
	"strings"
)

type CompactStrategy string

const (
	CompactStrategyUnknown      CompactStrategy = "unknown"
	CompactStrategyNative       CompactStrategy = "native_compact"
	CompactStrategyChatFallback CompactStrategy = "chat_fallback_compact"
	CompactStrategyHookSupplied CompactStrategy = "hook_supplied"
)

type CompactCapability string

const (
	CompactCapabilityUnknown          CompactCapability = "unknown"
	CompactCapabilityNativeAvailable  CompactCapability = "native_compact_available"
	CompactCapabilityChatFallbackOnly CompactCapability = "chat_fallback_only"
)

type ProviderCapabilities struct {
	Provider string
	Compact  CompactCapability
}

type CapabilityDescriber interface {
	Capabilities() ProviderCapabilities
}

// CompactRequest describes a provider-level context compaction request.
// Providers may implement this natively or fall back to a model-authored
// summary generated through their chat interface.
type CompactRequest struct {
	Model        string
	Messages     []Message
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
	UserPrompt   string
}

// CompactResponse contains the provider-authored compaction summary.
type CompactResponse struct {
	Summary  string
	Strategy CompactStrategy
}

// CompactWithProvider executes context compaction through the provider. Each
// provider decides whether to use a native compaction API or fall back to a
// chat-authored summary built from the same model surface.
func CompactWithProvider(ctx context.Context, provider LLMProvider, req CompactRequest) (CompactResponse, error) {
	if provider == nil {
		return CompactResponse{}, fmt.Errorf("llm provider is nil")
	}
	return provider.Compact(ctx, req)
}

func DescribeProviderCapabilities(provider LLMProvider) ProviderCapabilities {
	if provider == nil {
		return ProviderCapabilities{Compact: CompactCapabilityUnknown}
	}
	if describer, ok := provider.(CapabilityDescriber); ok {
		return normalizeProviderCapabilities(describer.Capabilities(), provider)
	}
	return normalizeProviderCapabilities(ProviderCapabilities{}, provider)
}

func compactViaChatStream(
	ctx context.Context,
	streamFn func(context.Context, ChatRequest) (<-chan ChatEvent, error),
	req CompactRequest,
) (CompactResponse, error) {
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		return CompactResponse{}, fmt.Errorf("compact request user prompt is empty")
	}

	msgs := append([]Message(nil), req.Messages...)
	msgs = append(msgs, Message{
		Role:    MessageRoleUser,
		Content: userPrompt,
	})

	stream, err := streamFn(ctx, ChatRequest{
		Model:        req.Model,
		Messages:     msgs,
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
		SystemPrompt: req.SystemPrompt,
	})
	if err != nil {
		return CompactResponse{}, err
	}

	var summary strings.Builder
	for event := range stream {
		switch event.Type {
		case EventTextDelta:
			summary.WriteString(event.Text)
		case EventError:
			return CompactResponse{}, event.Error
		}
	}

	result := strings.TrimSpace(summary.String())
	if result == "" {
		return CompactResponse{}, fmt.Errorf("compaction returned empty summary")
	}
	return CompactResponse{
		Summary:  result,
		Strategy: CompactStrategyChatFallback,
	}, nil
}

func approximateCompactRequestTokens(req CompactRequest) int {
	// Only count the transcript payload that will actually be compacted.
	// Provider prompts/instructions guide summarization, but they are not part
	// of the history being replaced and should not influence compaction scope.
	totalChars := 0
	for _, msg := range req.Messages {
		totalChars += len(msg.Role)
		totalChars += len(msg.Content)
		totalChars += len(msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			totalChars += len(tc.ID)
			totalChars += len(tc.Name)
			totalChars += len(tc.Input)
		}
	}
	approx := totalChars / 4
	if approx <= 0 {
		return 1
	}
	return approx
}

func normalizeProviderCapabilities(caps ProviderCapabilities, provider LLMProvider) ProviderCapabilities {
	caps.Provider = strings.TrimSpace(caps.Provider)
	if caps.Provider == "" {
		caps.Provider = inferProviderName(provider)
	}
	if caps.Compact == "" {
		caps.Compact = CompactCapabilityUnknown
	}
	return caps
}

func inferProviderName(provider LLMProvider) string {
	if provider == nil {
		return ""
	}
	for _, model := range provider.Models() {
		if name := strings.TrimSpace(model.Provider); name != "" {
			return name
		}
	}
	typeName := strings.TrimPrefix(fmt.Sprintf("%T", provider), "*")
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typeName = typeName[idx+1:]
	}
	typeName = strings.TrimSpace(strings.TrimSuffix(typeName, "Provider"))
	return strings.ToLower(typeName)
}
