package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"net/url"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// OpenAIProvider implements LLMProvider using the OpenAI Chat Completions API.
type OpenAIProvider struct {
	client  *openai.Client
	baseURL string
	headers map[string]string
}

// NewOpenAIProvider creates a new OpenAI LLM provider.
// If baseURL is non-empty, the client points to that endpoint (e.g. LiteLLM).
func NewOpenAIProvider(apiKey, baseURL string, headers map[string]string) *OpenAIProvider {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = normalizeOpenAIBaseURL(baseURL)
	}
	cfg.HTTPClient = newOpenAIHTTPClient(headers)
	client := openai.NewClientWithConfig(cfg)
	return &OpenAIProvider{
		client:  client,
		baseURL: cfg.BaseURL,
		headers: cloneHeaderMap(headers),
	}
}

func normalizeOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	baseURL = strings.TrimRight(baseURL, "/")

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	if strings.TrimSpace(parsed.Path) == "" {
		parsed.Path = "/v1"
		return strings.TrimRight(parsed.String(), "/")
	}
	return baseURL
}

func (p *OpenAIProvider) Models() []ModelInfo {
	return []ModelInfo{
		{ID: "gpt-4o", Name: "GPT-4o", Provider: "openai"},
		{ID: "gpt-4o-mini", Name: "GPT-4o Mini", Provider: "openai"},
		{ID: "gpt-4-turbo", Name: "GPT-4 Turbo", Provider: "openai"},
	}
}

func (p *OpenAIProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider: "openai",
		Compact:  CompactCapabilityChatFallbackOnly,
	}
}

func (p *OpenAIProvider) Compact(ctx context.Context, req CompactRequest) (CompactResponse, error) {
	return compactViaChatStream(ctx, p.ChatStream, req)
}

func (p *OpenAIProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	// Build messages
	msgs := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+1)

	if req.SystemPrompt != "" {
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: req.SystemPrompt,
		})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			if m.ToolCallID != "" {
				if len(m.Images) > 0 {
					// Tool result with images: use multi-content parts
					var parts []openai.ChatMessagePart
					for _, img := range m.Images {
						encoded := base64.StdEncoding.EncodeToString(img.Data)
						dataURI := fmt.Sprintf("data:%s;base64,%s", img.MimeType, encoded)
						parts = append(parts, openai.ChatMessagePart{
							Type: openai.ChatMessagePartTypeImageURL,
							ImageURL: &openai.ChatMessageImageURL{
								URL:    dataURI,
								Detail: openai.ImageURLDetailAuto,
							},
						})
					}
					if m.Content != "" {
						parts = append(parts, openai.ChatMessagePart{
							Type: openai.ChatMessagePartTypeText,
							Text: m.Content,
						})
					}
					msgs = append(msgs, openai.ChatCompletionMessage{
						Role:         openai.ChatMessageRoleTool,
						MultiContent: parts,
						ToolCallID:   m.ToolCallID,
					})
				} else {
					msgs = append(msgs, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    m.Content,
						ToolCallID: m.ToolCallID,
					})
				}
			} else if len(m.Images) > 0 {
				var parts []openai.ChatMessagePart
				for _, img := range m.Images {
					encoded := base64.StdEncoding.EncodeToString(img.Data)
					dataURI := fmt.Sprintf("data:%s;base64,%s", img.MimeType, encoded)
					parts = append(parts, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeImageURL,
						ImageURL: &openai.ChatMessageImageURL{
							URL:    dataURI,
							Detail: openai.ImageURLDetailAuto,
						},
					})
				}
				if m.Content != "" {
					parts = append(parts, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeText,
						Text: m.Content,
					})
				}
				msgs = append(msgs, openai.ChatCompletionMessage{
					Role:         openai.ChatMessageRoleUser,
					MultiContent: parts,
				})
			} else {
				msgs = append(msgs, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleUser,
					Content: m.Content,
				})
			}
		case "assistant":
			msg := openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: m.Content,
			}
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
					ID:   tc.ID,
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				})
			}
			msgs = append(msgs, msg)
		}
	}

	// Build tools
	var tools []openai.Tool
	for _, t := range req.Tools {
		var params any
		if err := json.Unmarshal(t.Parameters, &params); err != nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}

		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	model := req.Model
	if model == "" {
		model = "gpt-4o"
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	openaiReq := openai.ChatCompletionRequest{
		Model:    model,
		Messages: msgs,
		Stream:   true,
	}
	if usesMaxCompletionTokens(model) {
		openaiReq.MaxCompletionTokens = maxTokens
	} else {
		openaiReq.MaxTokens = maxTokens
	}

	if len(tools) > 0 {
		openaiReq.Tools = tools
	}
	if shouldDisableThinkingForModel(model) {
		openaiReq.ChatTemplateKwargs = map[string]any{
			"enable_thinking": false,
		}
	}

	if req.Temperature > 0 {
		openaiReq.Temperature = float32(req.Temperature)
	}

	logOpenAICompatibleRequestSummary(p.baseURL, openaiReq, req)

	if shouldUseNonStreamingCompletion(model) {
		return p.chatNonStreaming(ctx, openaiReq)
	}

	stream, err := p.client.CreateChatCompletionStream(ctx, openaiReq)
	if err != nil {
		runtimelogging.Warn("openai-compatible stream request failed",
			"base_url", p.baseURL,
			"model", model,
			"messages", len(msgs),
			"tools", len(tools),
			"error", err,
		)
		return nil, err
	}

	events := make(chan ChatEvent, 100)

	go func() {
		emitOpenAICompatibleStream(events, stream)
	}()

	return events, nil
}

func (p *OpenAIProvider) chatNonStreaming(ctx context.Context, req openai.ChatCompletionRequest) (<-chan ChatEvent, error) {
	req.Stream = false
	resp, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		runtimelogging.Warn("openai-compatible completion request failed",
			"base_url", p.baseURL,
			"model", req.Model,
			"messages", len(req.Messages),
			"tools", len(req.Tools),
			"error", err,
		)
		return nil, err
	}

	events := make(chan ChatEvent, 16)
	go func() {
		defer close(events)
		events <- ChatEvent{Type: EventActivity}
		if len(resp.Choices) == 0 {
			events <- ChatEvent{Type: EventDone}
			return
		}

		choice := resp.Choices[0]
		if strings.TrimSpace(choice.Message.Content) != "" {
			events <- ChatEvent{
				Type: EventTextDelta,
				Text: choice.Message.Content,
			}
		}
		for _, tc := range choice.Message.ToolCalls {
			toolCall := ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Input: json.RawMessage(func() string {
					args := strings.TrimSpace(tc.Function.Arguments)
					if args == "" {
						return "{}"
					}
					return args
				}()),
			}
			events <- ChatEvent{Type: EventToolCallStart, ToolCall: &toolCall}
			events <- ChatEvent{Type: EventToolCallDone, ToolCall: &toolCall}
		}
		events <- ChatEvent{Type: EventDone}
	}()

	return events, nil
}

func usesMaxCompletionTokens(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	return strings.HasPrefix(model, "o1") ||
		strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4") ||
		strings.HasPrefix(model, "gpt-5")
}

func shouldDisableThinkingForModel(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	return strings.Contains(model, "qwen3")
}

func shouldUseNonStreamingCompletion(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	return strings.Contains(model, "qwen3")
}

func logOpenAICompatibleRequestSummary(baseURL string, openaiReq openai.ChatCompletionRequest, req ChatRequest) {
	payloadBytes := 0
	if body, err := json.Marshal(openaiReq); err == nil {
		payloadBytes = len(body)
	}

	messageTextBytes := 0
	imageCount := 0
	assistantToolCalls := 0
	for _, msg := range req.Messages {
		messageTextBytes += len(msg.Content)
		imageCount += len(msg.Images)
		assistantToolCalls += len(msg.ToolCalls)
	}

	runtimelogging.Debug("openai-compatible request summary",
		"base_url", baseURL,
		"model", openaiReq.Model,
		"messages", len(openaiReq.Messages),
		"tools", len(openaiReq.Tools),
		"system_prompt_bytes", len(req.SystemPrompt),
		"message_text_bytes", messageTextBytes,
		"image_count", imageCount,
		"assistant_tool_calls", assistantToolCalls,
		"max_tokens", req.MaxTokens,
		"payload_bytes", payloadBytes,
	)
}
