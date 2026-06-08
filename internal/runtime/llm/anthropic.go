package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const (
	anthropicNativeCompactBetaHeader      = "compact-2026-01-12"
	anthropicNativeCompactMinInputTokens  = 50000
	defaultAnthropicCompactRequestMaxToks = 4096
)

// AnthropicProvider implements LLMProvider using the Anthropic Messages API.
type AnthropicProvider struct {
	client  anthropic.Client
	baseURL string
	headers map[string]string
}

// NewAnthropicProvider creates a new Anthropic LLM provider.
// If baseURL is non-empty, the client points to that endpoint.
func NewAnthropicProvider(apiKey, baseURL string, headers map[string]string) *AnthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	if httpClient := newHeaderHTTPClient(headers); httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	client := anthropic.NewClient(opts...)
	return &AnthropicProvider{
		client:  client,
		baseURL: baseURL,
		headers: cloneHeaderMap(headers),
	}
}

func (p *AnthropicProvider) Models() []ModelInfo {
	return []ModelInfo{
		{ID: "claude-sonnet-4-5-20250514", Name: "Claude Sonnet 4.5", Provider: "anthropic"},
		{ID: "claude-opus-4-0-20250514", Name: "Claude Opus 4", Provider: "anthropic"},
		{ID: "claude-haiku-3-5-20241022", Name: "Claude Haiku 3.5", Provider: "anthropic"},
	}
}

func (p *AnthropicProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider: "anthropic",
		Compact:  CompactCapabilityNativeAvailable,
	}
}

func (p *AnthropicProvider) Compact(ctx context.Context, req CompactRequest) (CompactResponse, error) {
	if !shouldUseAnthropicNativeCompaction(req) {
		return compactViaChatStream(ctx, p.ChatStream, req)
	}

	resp, err := p.compactNative(ctx, req)
	if err == nil {
		return resp, nil
	}

	runtimelogging.Warn("anthropic native compaction failed; falling back to chat compaction",
		"base_url", p.baseURL,
		"model", req.Model,
		"error", err,
	)
	return compactViaChatStream(ctx, p.ChatStream, req)
}

// DefaultModelOptions captures Anthropic-family knowledge about how to shape a
// request for a given model. The agent runtime layers these between its
// runtime baseline and any user-supplied overrides.
func (p *AnthropicProvider) DefaultModelOptions(model string) ModelOptions {
	opts := ModelOptions{}
	if supportsAnthropicNativeCompactionModel(model) {
		// claude-sonnet-4-6 / claude-opus-4-6 expose the beta ContextManagement
		// compaction API; opt them into the native path.
		opts.NativeCompaction = BoolPtr(true)
	}
	return opts
}

func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	systemParts := []anthropic.TextBlockParam(nil)
	if req.SystemPrompt != "" {
		systemParts = append(systemParts, anthropic.TextBlockParam{Text: req.SystemPrompt})
	}
	msgs, additionalSystemParts := buildAnthropicMessages(req.Messages)
	systemParts = append(systemParts, additionalSystemParts...)

	// Build tools
	tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
	for _, t := range req.Tools {
		var props any
		var required []string
		// Parse the JSON Schema to extract properties and required fields
		var schema struct {
			Properties any      `json:"properties"`
			Required   []string `json:"required"`
		}
		if err := json.Unmarshal(t.Parameters, &schema); err == nil {
			props = schema.Properties
			required = schema.Required
		}

		tools = append(tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: props,
					Required:   required,
				},
			},
		})
	}

	// Build params
	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 4096
	}

	model := req.Model
	if model == "" {
		model = "claude-sonnet-4-5-20250514"
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages:  msgs,
	}

	if len(tools) > 0 {
		params.Tools = tools
	}

	if len(systemParts) > 0 {
		params.System = systemParts
	}

	if req.Options.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Options.Temperature)
	}

	// Create stream
	stream := p.client.Messages.NewStreaming(ctx, params)

	events := make(chan ChatEvent, 100)

	go func() {
		defer close(events)
		defer stream.Close()

		tracker := newAnthropicToolCallTracker()

		for stream.Next() {
			event := stream.Current()
			events <- ChatEvent{Type: EventActivity}

			switch event.Type {
			case "content_block_start":
				cb := event.ContentBlock
				switch cb.Type {
				case "text":
					if cb.Text != "" {
						events <- ChatEvent{
							Type: EventTextDelta,
							Text: cb.Text,
						}
					}
				case "tool_use":
					tracker.start(event.Index, cb.ID, cb.Name, cb.Input)
					events <- ChatEvent{
						Type: EventToolCallStart,
						ToolCall: &ToolCall{
							ID:   cb.ID,
							Name: cb.Name,
						},
					}
				}

			case "content_block_delta":
				delta := event.Delta
				switch delta.Type {
				case "text_delta":
					events <- ChatEvent{
						Type: EventTextDelta,
						Text: delta.Text,
					}
				case "input_json_delta":
					tracker.append(event.Index, delta.PartialJSON)
				}

			case "content_block_stop":
				tc, ok, err := tracker.stopWithRepair(event.Index, func(toolName string, index int64, before, after string) {
					emitAnthropicRepairedToolCallDiagnostic(model, p.baseURL, toolName, index, before, after)
				})
				if err != nil {
					var malformedErr *anthropicMalformedToolCallJSONError
					if errors.As(err, &malformedErr) {
						emitAnthropicMalformedToolCallDiagnostic(model, p.baseURL, malformedErr)
					}
					events <- ChatEvent{Type: EventError, Error: err}
					return
				}
				if ok {
					events <- ChatEvent{Type: EventToolCallDone, ToolCall: tc}
				}

			case "message_delta":
				if event.Usage.OutputTokens > 0 {
					events <- ChatEvent{
						Type: EventDone,
						Usage: &Usage{
							OutputTokens: int(event.Usage.OutputTokens),
						},
					}
				}

			case "message_stop":
				if err := tracker.errIfPending(); err != nil {
					events <- ChatEvent{Type: EventError, Error: err}
					return
				}

			case "error":
				runtimelogging.Error("anthropic stream error", "event", event.Type)
				events <- ChatEvent{
					Type:  EventError,
					Error: fmt.Errorf("anthropic stream returned an error event"),
				}
				return
			}
		}

		if err := stream.Err(); err != nil {
			events <- ChatEvent{
				Type:  EventError,
				Error: err,
			}
			return
		}
		if err := tracker.errIfPending(); err != nil {
			events <- ChatEvent{Type: EventError, Error: err}
		}
	}()

	return events, nil
}

type anthropicPendingToolCall struct {
	id             string
	name           string
	inputJSON      string
	inputDeltas    int
	inputJSONBytes int
}

type anthropicMalformedToolCallJSONError struct {
	ToolName   string
	Index      int64
	InputBytes int
	DeltaCount int
	Prefix     string
	Suffix     string
}

func (e *anthropicMalformedToolCallJSONError) Error() string {
	return fmt.Sprintf("anthropic stream emitted malformed tool-call JSON for %s", e.ToolName)
}

func emitAnthropicMalformedToolCallDiagnostic(model, baseURL string, err *anthropicMalformedToolCallJSONError) {
	if err == nil {
		return
	}
	runtimelogging.Error("anthropic stream emitted malformed tool-call JSON",
		"model", model,
		"base_url", baseURL,
		"event_type", "content_block_stop",
		"tool", err.ToolName,
		"index", err.Index,
		"input_bytes", err.InputBytes,
		"delta_count", err.DeltaCount,
		"json_prefix", err.Prefix,
		"json_suffix", err.Suffix,
	)
}

func emitAnthropicRepairedToolCallDiagnostic(model, baseURL, toolName string, index int64, before, after string) {
	runtimelogging.Warn("anthropic stream repaired malformed tool-call JSON",
		"model", model,
		"base_url", baseURL,
		"event_type", "content_block_stop",
		"tool", toolName,
		"index", index,
		"input_bytes", len(before),
		"repaired_bytes", len(after),
		"json_prefix", safeLogSnippet(before, 160, false),
		"json_suffix", safeLogSnippet(before, 160, true),
	)
}

func safeLogSnippet(input string, limit int, suffix bool) string {
	if limit <= 0 {
		return ""
	}
	if len(input) <= limit {
		return input
	}
	if suffix {
		return "..." + input[len(input)-limit:]
	}
	return input[:limit] + "..."
}

type anthropicToolCallTracker struct {
	pending map[int64]*anthropicPendingToolCall
}

func newAnthropicToolCallTracker() *anthropicToolCallTracker {
	return &anthropicToolCallTracker{
		pending: make(map[int64]*anthropicPendingToolCall),
	}
}

func (t *anthropicToolCallTracker) start(index int64, id, name string, initialInput ...any) {
	if t == nil {
		return
	}
	if t.pending == nil {
		t.pending = make(map[int64]*anthropicPendingToolCall)
	}
	t.pending[index] = &anthropicPendingToolCall{
		id:        id,
		name:      name,
		inputJSON: anthropicInitialToolInputJSON(initialInput...),
	}
	t.pending[index].inputJSONBytes = len(t.pending[index].inputJSON)
}

func (t *anthropicToolCallTracker) append(index int64, partialJSON string) {
	if t == nil || partialJSON == "" {
		return
	}
	pending := t.pending[index]
	if pending == nil {
		return
	}
	pending.inputJSON += partialJSON
	pending.inputDeltas++
	pending.inputJSONBytes += len(partialJSON)
}

func (t *anthropicToolCallTracker) stop(index int64) (*ToolCall, bool, error) {
	return t.stopWithRepair(index, nil)
}

func (t *anthropicToolCallTracker) stopWithRepair(index int64, onRepair func(toolName string, index int64, before, after string)) (*ToolCall, bool, error) {
	if t == nil || len(t.pending) == 0 {
		return nil, false, nil
	}
	pending := t.pending[index]
	if pending == nil {
		return nil, false, nil
	}
	delete(t.pending, index)
	input := strings.TrimSpace(pending.inputJSON)
	if input == "" {
		input = "{}"
	}
	if !json.Valid([]byte(input)) {
		if repaired, ok := repairAnthropicToolCallJSON(input); ok {
			if onRepair != nil {
				onRepair(pending.name, index, input, repaired)
			}
			input = repaired
		}
	}
	if !json.Valid([]byte(input)) {
		return nil, false, &anthropicMalformedToolCallJSONError{
			ToolName:   pending.name,
			Index:      index,
			InputBytes: len(input),
			DeltaCount: pending.inputDeltas,
			Prefix:     safeLogSnippet(input, 160, false),
			Suffix:     safeLogSnippet(input, 160, true),
		}
	}
	return &ToolCall{
		ID:    pending.id,
		Name:  pending.name,
		Input: json.RawMessage(input),
	}, true, nil
}

func repairAnthropicToolCallJSON(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" || json.Valid([]byte(input)) {
		return input, json.Valid([]byte(input))
	}
	repaired, ok := closeIncompleteJSONObject(input)
	if !ok || repaired == input || !json.Valid([]byte(repaired)) {
		return "", false
	}
	return repaired, true
}

func closeIncompleteJSONObject(input string) (string, bool) {
	stack := make([]rune, 0, 4)
	inString := false
	escaped := false
	lastNonSpace := rune(0)

	for _, r := range input {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
				lastNonSpace = r
			}
			continue
		}

		if !isJSONSpace(r) {
			lastNonSpace = r
		}
		switch r {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != r {
				return "", false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString || escaped || len(stack) == 0 {
		return "", false
	}
	switch lastNonSpace {
	case 0, ',', ':', '{', '[':
		return "", false
	}
	var b strings.Builder
	b.WriteString(input)
	for i := len(stack) - 1; i >= 0; i-- {
		b.WriteRune(stack[i])
	}
	return b.String(), true
}

func isJSONSpace(r rune) bool {
	switch r {
	case ' ', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}

func (t *anthropicToolCallTracker) errIfPending() error {
	if t == nil || len(t.pending) == 0 {
		return nil
	}
	names := make([]string, 0, len(t.pending))
	for _, pending := range t.pending {
		if pending == nil {
			continue
		}
		name := strings.TrimSpace(pending.name)
		if name == "" {
			name = "unknown"
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return fmt.Errorf("anthropic stream ended before %d tool call(s) completed", len(t.pending))
	}
	return fmt.Errorf("anthropic stream ended before tool call input completed for %s", strings.Join(names, ", "))
}

func anthropicInitialToolInputJSON(values ...any) string {
	if len(values) == 0 || values[0] == nil {
		return ""
	}
	data, err := json.Marshal(values[0])
	if err != nil {
		return ""
	}
	input := strings.TrimSpace(string(data))
	if input == "" || input == "null" || input == "{}" || !strings.HasPrefix(input, "{") {
		return ""
	}
	return input
}

func buildAnthropicMessages(messages []Message) ([]anthropic.MessageParam, []anthropic.TextBlockParam) {
	msgs := make([]anthropic.MessageParam, 0, len(messages))
	systemParts := []anthropic.TextBlockParam(nil)
	var pendingToolResults []anthropic.ContentBlockParamUnion

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		msgs = append(msgs, anthropic.NewUserMessage(pendingToolResults...))
		pendingToolResults = nil
	}

	for _, m := range messages {
		switch m.Role {
		case MessageRoleRuntime, MessageRoleDeveloper, MessageRoleSystem:
			flushToolResults()
			if strings.TrimSpace(m.Content) != "" {
				systemParts = append(systemParts, anthropic.TextBlockParam{Text: m.Content})
			}
		case MessageRoleUser:
			if m.ToolCallID != "" {
				pendingToolResults = append(pendingToolResults, buildAnthropicToolResultBlock(m))
				continue
			}
			flushToolResults()
			msgs = append(msgs, anthropic.NewUserMessage(buildAnthropicUserBlocks(m)...))
		case MessageRoleAssistant:
			flushToolResults()
			var blocks []anthropic.ContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, anthropicToolInputObject(tc.Input), tc.Name))
			}
			if len(blocks) > 0 {
				msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
			}
		}
	}
	flushToolResults()
	return msgs, systemParts
}

func buildAnthropicUserBlocks(m Message) []anthropic.ContentBlockParamUnion {
	if len(m.Images) == 0 {
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)}
	}
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Images)+1)
	for _, img := range m.Images {
		encoded := base64.StdEncoding.EncodeToString(img.Data)
		blocks = append(blocks, anthropic.NewImageBlockBase64(img.MimeType, encoded))
	}
	if m.Content != "" {
		blocks = append(blocks, anthropic.NewTextBlock(m.Content))
	}
	return blocks
}

func buildAnthropicToolResultBlock(m Message) anthropic.ContentBlockParamUnion {
	if len(m.Images) == 0 {
		return anthropic.NewToolResultBlock(m.ToolCallID, m.Content, m.IsError)
	}
	var content []anthropic.ToolResultBlockParamContentUnion
	for _, img := range m.Images {
		encoded := base64.StdEncoding.EncodeToString(img.Data)
		content = append(content, anthropic.ToolResultBlockParamContentUnion{
			OfImage: &anthropic.ImageBlockParam{
				Source: anthropic.ImageBlockParamSourceUnion{
					OfBase64: &anthropic.Base64ImageSourceParam{
						Data:      encoded,
						MediaType: anthropic.Base64ImageSourceMediaType(img.MimeType),
					},
				},
			},
		})
	}
	if m.Content != "" {
		content = append(content, anthropic.ToolResultBlockParamContentUnion{
			OfText: &anthropic.TextBlockParam{Text: m.Content},
		})
	}
	toolBlock := anthropic.ToolResultBlockParam{
		ToolUseID: m.ToolCallID,
		IsError:   anthropic.Bool(m.IsError),
		Content:   content,
	}
	return anthropic.ContentBlockParamUnion{OfToolResult: &toolBlock}
}

func anthropicToolInputObject(raw json.RawMessage) map[string]any {
	var input any
	if err := json.Unmarshal(raw, &input); err != nil {
		return map[string]any{}
	}
	if obj, ok := input.(map[string]any); ok {
		return obj
	}
	return map[string]any{"value": input}
}

func (p *AnthropicProvider) compactNative(ctx context.Context, req CompactRequest) (CompactResponse, error) {
	msgs, systemParts := buildAnthropicBetaMessages(req.Messages)
	if len(msgs) == 0 {
		return CompactResponse{}, fmt.Errorf("anthropic native compaction requires non-empty history")
	}

	model := req.Model
	if model == "" {
		model = "claude-sonnet-4-5-20250514"
	}

	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = defaultAnthropicCompactRequestMaxToks
	}

	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages:  msgs,
		ContextManagement: anthropic.BetaContextManagementConfigParam{
			Edits: []anthropic.BetaContextManagementConfigEditUnionParam{{
				OfCompact20260112: &anthropic.BetaCompact20260112EditParam{
					Instructions:         anthropic.String(buildAnthropicCompactInstructions(req)),
					PauseAfterCompaction: anthropic.Bool(true),
					Trigger:              anthropic.BetaInputTokensTriggerParam{Value: anthropicNativeCompactMinInputTokens},
				},
			}},
		},
		Betas: []anthropic.AnthropicBeta{anthropicNativeCompactBetaHeader},
	}

	if req.SystemPrompt != "" {
		systemParts = append([]anthropic.BetaTextBlockParam{{Text: req.SystemPrompt}}, systemParts...)
	}
	if len(systemParts) > 0 {
		params.System = systemParts
	}
	if req.Options.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Options.Temperature)
	}

	message, err := p.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return CompactResponse{}, err
	}

	summary := extractAnthropicCompactionSummary(message)
	if summary == "" {
		return CompactResponse{}, fmt.Errorf("anthropic native compaction returned no compaction block")
	}
	return CompactResponse{
		Summary:  summary,
		Strategy: CompactStrategyNative,
	}, nil
}

func shouldUseAnthropicNativeCompaction(req CompactRequest) bool {
	if req.Options.NativeCompaction == nil || !*req.Options.NativeCompaction {
		return false
	}
	return approximateCompactRequestTokens(req) >= anthropicNativeCompactMinInputTokens
}

func supportsAnthropicNativeCompactionModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	return strings.Contains(model, "claude-sonnet-4-6") || strings.Contains(model, "claude-opus-4-6")
}

func buildAnthropicCompactInstructions(req CompactRequest) string {
	return strings.TrimSpace(req.UserPrompt)
}

func extractAnthropicCompactionSummary(message *anthropic.BetaMessage) string {
	if message == nil {
		return ""
	}
	for _, block := range message.Content {
		if block.Type != "compaction" {
			continue
		}
		return strings.TrimSpace(block.AsCompaction().Content)
	}
	return ""
}

func buildAnthropicBetaMessages(messages []Message) ([]anthropic.BetaMessageParam, []anthropic.BetaTextBlockParam) {
	msgs := make([]anthropic.BetaMessageParam, 0, len(messages))
	systemParts := []anthropic.BetaTextBlockParam(nil)
	var pendingToolResults []anthropic.BetaContentBlockParamUnion

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		msgs = append(msgs, anthropic.NewBetaUserMessage(pendingToolResults...))
		pendingToolResults = nil
	}

	for _, m := range messages {
		switch m.Role {
		case MessageRoleRuntime, MessageRoleDeveloper, MessageRoleSystem:
			flushToolResults()
			if strings.TrimSpace(m.Content) != "" {
				systemParts = append(systemParts, anthropic.BetaTextBlockParam{Text: m.Content})
			}
		case MessageRoleUser:
			if m.ToolCallID != "" {
				pendingToolResults = append(pendingToolResults, buildAnthropicBetaToolResultBlock(m))
				continue
			}
			flushToolResults()

			if len(m.Images) > 0 {
				var blocks []anthropic.BetaContentBlockParamUnion
				for _, img := range m.Images {
					blocks = append(blocks, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
						Data:      base64.StdEncoding.EncodeToString(img.Data),
						MediaType: anthropic.BetaBase64ImageSourceMediaType(img.MimeType),
					}))
				}
				if m.Content != "" {
					blocks = append(blocks, anthropic.NewBetaTextBlock(m.Content))
				}
				msgs = append(msgs, anthropic.NewBetaUserMessage(blocks...))
				continue
			}

			msgs = append(msgs, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(m.Content)))

		case MessageRoleAssistant:
			flushToolResults()
			var blocks []anthropic.BetaContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewBetaTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropic.NewBetaToolUseBlock(tc.ID, anthropicToolInputObject(tc.Input), tc.Name))
			}
			if len(blocks) > 0 {
				msgs = append(msgs, anthropic.BetaMessageParam{
					Role:    anthropic.BetaMessageParamRoleAssistant,
					Content: blocks,
				})
			}
		}
	}
	flushToolResults()
	return msgs, systemParts
}

func buildAnthropicBetaToolResultBlock(m Message) anthropic.BetaContentBlockParamUnion {
	if len(m.Images) == 0 {
		return anthropic.NewBetaToolResultBlock(m.ToolCallID, m.Content, m.IsError)
	}
	var content []anthropic.BetaToolResultBlockParamContentUnion
	for _, img := range m.Images {
		content = append(content, anthropic.BetaToolResultBlockParamContentUnion{
			OfImage: &anthropic.BetaImageBlockParam{
				Source: anthropic.BetaImageBlockParamSourceUnion{
					OfBase64: &anthropic.BetaBase64ImageSourceParam{
						Data:      base64.StdEncoding.EncodeToString(img.Data),
						MediaType: anthropic.BetaBase64ImageSourceMediaType(img.MimeType),
					},
				},
			},
		})
	}
	if m.Content != "" {
		content = append(content, anthropic.BetaToolResultBlockParamContentUnion{
			OfText: &anthropic.BetaTextBlockParam{Text: m.Content},
		})
	}
	toolBlock := anthropic.BetaToolResultBlockParam{
		ToolUseID: m.ToolCallID,
		IsError:   anthropic.Bool(m.IsError),
		Content:   content,
	}
	return anthropic.BetaContentBlockParamUnion{OfToolResult: &toolBlock}
}
