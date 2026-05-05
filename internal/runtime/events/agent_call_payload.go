package runtimeevents

import (
	"encoding/json"
	"strings"

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
