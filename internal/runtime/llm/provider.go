package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EventType identifies the kind of streaming event from the LLM.
type EventType int

const (
	EventActivity EventType = iota
	EventTextDelta
	EventToolCallStart
	EventToolCallDelta
	EventToolCallDone
	EventDone
	EventError
)

// ImageContent holds image data for multimodal messages.
type ImageContent struct {
	ID       string // stable image/attachment identifier when persisted
	Name     string // original or generated filename when available
	MimeType string // "image/jpeg", "image/png", etc.
	Path     string // persisted local asset path when available
	Size     int    // byte size when known
	Data     []byte // raw image bytes
}

const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"
	MessageRoleSystem    = "system"
	// MessageRoleDeveloper carries runtime-authored instructions that should
	// be treated as control context rather than a user turn when a provider
	// supports that distinction.
	MessageRoleDeveloper = "developer"
	// MessageRoleRuntime is an internal role for one-shot runtime directives.
	// Provider adapters map it to the closest supported control role.
	MessageRoleRuntime = "runtime"
)

// Message represents a conversation message.
type Message struct {
	Role       string         `json:"role"` // user, assistant, system, developer, runtime
	Content    string         `json:"content,omitempty"`
	Images     []ImageContent `json:"-"` // image attachments (not serialized)
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"` // for tool results
	IsError    bool           `json:"is_error,omitempty"`     // for tool results
}

func IsUserRole(role string) bool {
	return strings.TrimSpace(role) == MessageRoleUser
}

func IsAssistantRole(role string) bool {
	return strings.TrimSpace(role) == MessageRoleAssistant
}

func IsControlRole(role string) bool {
	switch strings.TrimSpace(role) {
	case MessageRoleSystem, MessageRoleDeveloper, MessageRoleRuntime:
		return true
	default:
		return false
	}
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolDef defines a tool for the LLM.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// ChatRequest is the input to a streaming chat call.
type ChatRequest struct {
	Model        string
	Messages     []Message
	Tools        []ToolDef
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
}

// Usage tracks token usage.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ChatEvent is a single streaming event from the LLM.
type ChatEvent struct {
	Type     EventType
	Text     string
	ToolCall *ToolCall
	Usage    *Usage
	Error    error
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID       string
	Name     string
	Provider string
}

// LLMProvider is the interface for all LLM backends.
type LLMProvider interface {
	ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
	Compact(ctx context.Context, req CompactRequest) (CompactResponse, error)
	Models() []ModelInfo
}

var (
	_ LLMProvider = (*AnthropicProvider)(nil)
	_ LLMProvider = (*OpenAIProvider)(nil)
	_ LLMProvider = (*GeminiProvider)(nil)
	_ LLMProvider = (*QwenProvider)(nil)

	_ CapabilityDescriber = (*AnthropicProvider)(nil)
	_ CapabilityDescriber = (*OpenAIProvider)(nil)
	_ CapabilityDescriber = (*GeminiProvider)(nil)
	_ CapabilityDescriber = (*QwenProvider)(nil)
)

// ParseProviderModel splits "provider/model" into (provider, model).
// If no slash is present, returns ("", name) and the caller should use a default.
func ParseProviderModel(name string) (provider, model string) {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", name
}

// ProviderOptions holds connection details for creating an LLM provider.
type ProviderOptions struct {
	APIKey  string
	BaseURL string
	Kind    string            // override: "openai-compatible"/"anthropic-compatible"/"anthropic-messages" selects the protocol client explicitly
	Headers map[string]string // additional HTTP headers for provider verification/routing
}

const (
	ProviderKindAnthropic           = "anthropic"
	ProviderKindAnthropicCompatible = "anthropic-compatible"
	ProviderKindAnthropicMessages   = "anthropic-messages"
	ProviderKindOpenAI              = "openai"
	ProviderKindOpenAICompatible    = "openai-compatible"
	ProviderKindGemini              = "gemini"
	ProviderKindQwen                = "qwen"
)

func normalizeProviderKind(providerName string, opts ProviderOptions) string {
	kind := strings.TrimSpace(opts.Kind)
	if kind != "" {
		return kind
	}
	if strings.TrimSpace(opts.BaseURL) != "" {
		// A custom endpoint is typically an OpenAI-compatible proxy such as
		// Ollama, LiteLLM, vLLM, or other local/open-source gateways.
		return ProviderKindOpenAICompatible
	}
	return strings.TrimSpace(providerName)
}

// CanInitializeProvider reports whether the configured provider has enough
// connection information to be constructed. For local/self-hosted gateways such
// as Ollama, a compatible custom endpoint is sufficient even without an API key.
func CanInitializeProvider(providerName string, opts ProviderOptions) bool {
	if strings.TrimSpace(opts.APIKey) != "" {
		return true
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return false
	}

	switch normalizeProviderKind(providerName, opts) {
	case ProviderKindOpenAI, ProviderKindOpenAICompatible, ProviderKindAnthropic, ProviderKindAnthropicCompatible, ProviderKindAnthropicMessages:
		return true
	default:
		return false
	}
}

// NewProvider creates an LLMProvider for the given provider name.
func NewProvider(providerName string, opts ProviderOptions) (LLMProvider, error) {
	kind := normalizeProviderKind(providerName, opts)

	switch kind {
	case ProviderKindAnthropic, ProviderKindAnthropicCompatible, ProviderKindAnthropicMessages:
		return NewAnthropicProvider(opts.APIKey, opts.BaseURL, opts.Headers), nil
	case ProviderKindOpenAI, ProviderKindOpenAICompatible:
		return NewOpenAIProvider(opts.APIKey, opts.BaseURL, opts.Headers), nil
	case ProviderKindGemini:
		return NewGeminiProvider(context.Background(), opts.APIKey, opts.BaseURL, opts.Headers)
	case ProviderKindQwen:
		return NewQwenProvider(opts.APIKey, opts.BaseURL, opts.Headers), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider kind: %q", kind)
	}
}
