package runtimeevents

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/contract"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/tool"
)

// AgentCallStartedPayload normalizes callagent tool-call inputs into a
// structured payload shared by recorder and transport-specific event streams.
func AgentCallStartedPayload(toolCall *llm.ToolCall) (map[string]any, bool) {
	if toolCall == nil || !isAgentCallToolName(toolCall.Name) {
		return nil, false
	}
	payload, err := tools.NormalizeCallAgentPayload(tools.SanitizeRawJSON(toolCall.Input))
	if err != nil {
		return nil, false
	}
	return payload, true
}

// AgentCallFinishedPayload normalizes callagent tool results into a structured
// payload shared by recorder and transport-specific event streams.
func AgentCallFinishedPayload(toolCall *llm.ToolCall, result *tools.ToolResult) (string, map[string]any, bool) {
	if toolCall == nil || result == nil || !isAgentCallToolName(toolCall.Name) {
		return "", nil, false
	}
	if result.Error != "" {
		return EventAgentCallFailed, map[string]any{
			"status": "failed",
			"error":  result.Error,
		}, true
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		return EventAgentCallFailed, map[string]any{
			"status":  "failed",
			"error":   "invalid callagent output",
			"summary": result.Output,
		}, true
	}
	status := strings.ToLower(strings.TrimSpace(stringValue(payload, "status")))
	if status == "running" {
		return EventAgentCallSubmitted, payload, true
	}
	if status == "failed" || strings.TrimSpace(stringValue(payload, "error")) != "" {
		return EventAgentCallFailed, payload, true
	}
	return EventAgentCallCompleted, payload, true
}

// AgentCallFinishedPayloadForTranscript mirrors AgentCallFinishedPayload, but
// first caps and redacts the callagent tool result. Use it for event logs and
// durable projections so a large child-agent summary cannot be parsed and held
// again while building observability payloads.
func AgentCallFinishedPayloadForTranscript(toolCall *llm.ToolCall, result tools.ToolResult) (string, map[string]any, bool) {
	if toolCall == nil || !isAgentCallToolName(toolCall.Name) {
		return "", nil, false
	}
	durable := tools.SanitizeToolResultForTranscript(result)
	eventName, payload, ok := AgentCallFinishedPayload(toolCall, &durable)
	if !ok {
		return "", nil, false
	}
	return eventName, contract.SanitizeDurableMetadata(payload), true
}

func CompactAgentCallResultForTranscript(result tools.AgentCallResult) tools.AgentCallResult {
	result.Summary = compactAgentCallText(result.Summary)
	result.Error = compactAgentCallText(result.Error)
	return result
}

func CompactParallelAgentCallResultForTranscript(result tools.ParallelAgentCallResult) tools.ParallelAgentCallResult {
	for i := range result.Results {
		result.Results[i] = CompactAgentCallResultForTranscript(result.Results[i])
	}
	return result
}

func compactAgentCallText(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	compact, _ := contract.SanitizeDurableText(value)
	return compact
}

func MarshalCompactAgentCallResultForTranscript(result tools.AgentCallResult) (string, error) {
	payload, err := json.Marshal(CompactAgentCallResultForTranscript(result))
	if err != nil {
		return "", fmt.Errorf("marshal agent call result: %w", err)
	}
	return string(payload), nil
}

func MarshalCompactParallelAgentCallResultForTranscript(result tools.ParallelAgentCallResult) (string, error) {
	payload, err := json.Marshal(CompactParallelAgentCallResultForTranscript(result))
	if err != nil {
		return "", fmt.Errorf("marshal parallel agent call result: %w", err)
	}
	return string(payload), nil
}

func stringValue(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func isAgentCallToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "callagent":
		return true
	default:
		return false
	}
}
