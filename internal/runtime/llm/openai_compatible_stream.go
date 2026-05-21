package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"io"
	"sort"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

type streamedToolCall struct {
	id       string
	name     string
	argsJSON string
	started  bool
}

const (
	openAICompatibleTruncationStageFirstPacket       = "first_packet"
	openAICompatibleTruncationStageMidStream         = "mid_stream"
	openAICompatibleTruncationStageToolCallArguments = "tool_call_arguments"
)

type openAICompatibleStreamProgress struct {
	decodedChunks     int
	sawText           bool
	textBytes         int
	toolArgumentBytes int
}

type openAICompatibleStreamDiagnostic struct {
	Stage             string
	Recovered         bool
	RecoveryMode      string
	DecodedChunks     int
	TextBytes         int
	ToolArgumentBytes int
	ActiveToolCalls   int
	ToolNames         []string
	Error             string
}

var emitOpenAICompatibleStreamDiagnostic = func(diag openAICompatibleStreamDiagnostic) {
	runtimelogging.Warn("openai-compatible stream diagnostic",
		"stage", diag.Stage,
		"recovered", diag.Recovered,
		"recovery_mode", diag.RecoveryMode,
		"decoded_chunks", diag.DecodedChunks,
		"text_bytes", diag.TextBytes,
		"tool_argument_bytes", diag.ToolArgumentBytes,
		"active_tool_calls", diag.ActiveToolCalls,
		"tool_names", diag.ToolNames,
		"error", diag.Error,
	)
}

func emitOpenAICompatibleStream(events chan ChatEvent, stream *openai.ChatCompletionStream) {
	defer close(events)
	defer stream.Close()

	toolCalls := make(map[int]*streamedToolCall)
	progress := openAICompatibleStreamProgress{}

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			completed, recovered, recoveryErr, diag := recoverOpenAICompatibleStream(err, toolCalls, progress)
			if diag != nil {
				emitOpenAICompatibleStreamDiagnostic(*diag)
			}
			if recovered {
				emitRecoveredToolCalls(events, completed)
				events <- ChatEvent{Type: EventDone}
				return
			}
			events <- ChatEvent{Type: EventError, Error: recoveryErr}
			return
		}

		progress.observeChunk(resp)
		events <- ChatEvent{Type: EventActivity}

		for _, choice := range resp.Choices {
			delta := choice.Delta

			if delta.Content != "" {
				progress.observeText(delta.Content)
				events <- ChatEvent{
					Type: EventTextDelta,
					Text: delta.Content,
				}
			}

			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				pending, exists := toolCalls[idx]
				if !exists {
					pending = &streamedToolCall{}
					toolCalls[idx] = pending
				}

				if tc.ID != "" {
					pending.id = tc.ID
				}
				if tc.Function.Name != "" {
					pending.name = tc.Function.Name
					progress.observeToolName(pending.name)
					if !pending.started {
						pending.started = true
						events <- ChatEvent{
							Type: EventToolCallStart,
							ToolCall: &ToolCall{
								ID:   pending.id,
								Name: pending.name,
							},
						}
					}
				}
				if tc.Function.Arguments != "" {
					pending.argsJSON += tc.Function.Arguments
					progress.observeToolArguments(pending.name, tc.Function.Arguments)
				}
			}

			if choice.FinishReason == openai.FinishReasonToolCalls || choice.FinishReason == openai.FinishReasonStop {
				completed, err := finalizeOpenAICompatibleToolCalls(toolCalls)
				if err != nil {
					events <- ChatEvent{Type: EventError, Error: err}
					return
				}
				emitRecoveredToolCalls(events, completed)
				toolCalls = make(map[int]*streamedToolCall)
			}
		}
	}

	if len(toolCalls) > 0 {
		completed, err := finalizeOpenAICompatibleToolCalls(toolCalls)
		if err != nil {
			events <- ChatEvent{Type: EventError, Error: fmt.Errorf("provider stream ended before tool-call arguments completed%s: %w", describeOpenAICompatibleTools(toolCalls), err)}
			return
		}
		if len(completed) > 0 {
			emitRecoveredToolCalls(events, completed)
		}
	}
	events <- ChatEvent{Type: EventDone}
}

func (p *openAICompatibleStreamProgress) observeChunk(resp openai.ChatCompletionStreamResponse) {
	p.decodedChunks++
	_ = resp
}

func (p *openAICompatibleStreamProgress) observeText(text string) {
	if text == "" {
		return
	}
	p.sawText = true
	p.textBytes += len(text)
}

func (p *openAICompatibleStreamProgress) observeToolName(name string) {
	_ = name
}

func (p *openAICompatibleStreamProgress) observeToolArguments(name, args string) {
	if args == "" {
		return
	}
	p.toolArgumentBytes += len(args)
	_, _ = name, args
}

func recoverOpenAICompatibleStream(err error, toolCalls map[int]*streamedToolCall, progress openAICompatibleStreamProgress) ([]ToolCall, bool, error, *openAICompatibleStreamDiagnostic) {
	if !isTruncatedJSONStreamError(err) {
		return nil, false, err, nil
	}

	if len(toolCalls) > 0 {
		completed, finalizeErr := finalizeOpenAICompatibleToolCalls(toolCalls)
		if finalizeErr == nil && len(completed) > 0 {
			diag := buildOpenAICompatibleStreamDiagnostic(err, toolCalls, progress, true, "tool_calls")
			return completed, true, nil, &diag
		}
		if classifyOpenAICompatibleTruncation(progress, toolCalls) == openAICompatibleTruncationStageToolCallArguments {
			diag := buildOpenAICompatibleStreamDiagnostic(err, toolCalls, progress, false, "none")
			return nil, false, fmt.Errorf("provider stream ended during tool-call arguments%s: %w", describeOpenAICompatibleTools(toolCalls), err), &diag
		}
	}

	if progress.sawText {
		diag := buildOpenAICompatibleStreamDiagnostic(err, toolCalls, progress, true, "text")
		return nil, true, nil, &diag
	}

	diag := buildOpenAICompatibleStreamDiagnostic(err, toolCalls, progress, false, "none")
	if diag.Stage == openAICompatibleTruncationStageFirstPacket {
		return nil, false, fmt.Errorf("provider stream returned truncated JSON before the first chunk could be decoded: %w", err), &diag
	}
	return nil, false, fmt.Errorf("provider stream returned truncated JSON mid-response before completion: %w", err), &diag
}

func finalizeOpenAICompatibleToolCalls(toolCalls map[int]*streamedToolCall) ([]ToolCall, error) {
	if len(toolCalls) == 0 {
		return nil, nil
	}

	indexes := make([]int, 0, len(toolCalls))
	for idx := range toolCalls {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)

	completed := make([]ToolCall, 0, len(indexes))
	for _, idx := range indexes {
		pending := toolCalls[idx]
		if pending == nil {
			continue
		}
		if strings.TrimSpace(pending.name) == "" {
			return nil, fmt.Errorf("provider emitted incomplete tool call at index %d without a function name", idx)
		}
		input := strings.TrimSpace(pending.argsJSON)
		if input == "" {
			input = "{}"
		}
		if !json.Valid([]byte(input)) {
			return nil, fmt.Errorf("provider emitted malformed tool-call JSON for %s", pending.name)
		}
		completed = append(completed, ToolCall{
			ID:    pending.id,
			Name:  pending.name,
			Input: json.RawMessage(input),
		})
	}

	return completed, nil
}

func buildOpenAICompatibleStreamDiagnostic(err error, toolCalls map[int]*streamedToolCall, progress openAICompatibleStreamProgress, recovered bool, recoveryMode string) openAICompatibleStreamDiagnostic {
	return openAICompatibleStreamDiagnostic{
		Stage:             classifyOpenAICompatibleTruncation(progress, toolCalls),
		Recovered:         recovered,
		RecoveryMode:      recoveryMode,
		DecodedChunks:     progress.decodedChunks,
		TextBytes:         progress.textBytes,
		ToolArgumentBytes: progress.toolArgumentBytes,
		ActiveToolCalls:   countOpenAICompatibleToolCalls(toolCalls),
		ToolNames:         openAICompatibleToolNames(toolCalls),
		Error:             err.Error(),
	}
}

func classifyOpenAICompatibleTruncation(progress openAICompatibleStreamProgress, toolCalls map[int]*streamedToolCall) string {
	if progress.decodedChunks == 0 {
		return openAICompatibleTruncationStageFirstPacket
	}
	if progress.toolArgumentBytes > 0 || hasOpenAICompatibleToolArguments(toolCalls) {
		return openAICompatibleTruncationStageToolCallArguments
	}
	return openAICompatibleTruncationStageMidStream
}

func hasOpenAICompatibleToolArguments(toolCalls map[int]*streamedToolCall) bool {
	for _, pending := range toolCalls {
		if pending == nil {
			continue
		}
		if strings.TrimSpace(pending.argsJSON) != "" {
			return true
		}
	}
	return false
}

func countOpenAICompatibleToolCalls(toolCalls map[int]*streamedToolCall) int {
	count := 0
	for _, pending := range toolCalls {
		if pending != nil {
			count++
		}
	}
	return count
}

func openAICompatibleToolNames(toolCalls map[int]*streamedToolCall) []string {
	seen := make(map[string]struct{})
	names := make([]string, 0, len(toolCalls))
	for _, pending := range toolCalls {
		if pending == nil {
			continue
		}
		name := strings.TrimSpace(pending.name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func describeOpenAICompatibleTools(toolCalls map[int]*streamedToolCall) string {
	names := openAICompatibleToolNames(toolCalls)
	switch len(names) {
	case 0:
		if count := countOpenAICompatibleToolCalls(toolCalls); count > 0 {
			return fmt.Sprintf(" for %d pending tool call(s)", count)
		}
		return ""
	case 1:
		return " for " + names[0]
	default:
		return " for tools " + strings.Join(names, ", ")
	}
}

func emitRecoveredToolCalls(events chan ChatEvent, toolCalls []ToolCall) {
	for i := range toolCalls {
		tc := toolCalls[i]
		events <- ChatEvent{
			Type: EventToolCallDone,
			ToolCall: &ToolCall{
				ID:    tc.ID,
				Name:  tc.Name,
				Input: tc.Input,
			},
		}
	}
}

func isTruncatedJSONStreamError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "unexpected end of json input") ||
		strings.Contains(msg, "unexpected eof")
}
