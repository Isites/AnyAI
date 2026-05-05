package testutil

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/llm"
)

// AutoGoalCompletionResponse synthesizes the runtime-control goal completion
// turns expected by tests that use static mock providers.
func AutoGoalCompletionResponse(req llm.ChatRequest, events []llm.ChatEvent, pending *bool, seq *int) ([]llm.ChatEvent, bool) {
	if !goalCompletionRequired(req) || !goalToolAvailable(req) {
		return nil, false
	}
	if responseContainsGoalTool(events) || responseContainsAnyToolCall(events) {
		return nil, false
	}
	id := "tc_auto_goal_complete"
	if seq != nil {
		*seq = *seq + 1
		id = fmt.Sprintf("tc_auto_goal_complete_%d", *seq)
	}
	return []llm.ChatEvent{
		{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: id, Name: "goal_complete"}},
		{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{ID: id, Name: "goal_complete", Input: json.RawMessage(`{}`)}},
		{Type: llm.EventDone},
	}, true
}

// StaticEventStream returns a closed stream for a fixed event slice.
func StaticEventStream(events []llm.ChatEvent) <-chan llm.ChatEvent {
	ch := make(chan llm.ChatEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch
}

func goalCompletionRequired(req llm.ChatRequest) bool {
	text := strings.TrimSpace(req.SystemPrompt)
	if strings.Contains(text, "Runtime requirement: do not stop silently. Call `goal_complete`") {
		return true
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "user" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		return strings.Contains(content, "[Runtime goal continuation]") &&
			strings.Contains(content, "Call `goal_complete` now instead of ending silently.")
	}
	return false
}

func goalToolAvailable(req llm.ChatRequest) bool {
	for _, tool := range req.Tools {
		if strings.TrimSpace(tool.Name) == "goal_complete" {
			return true
		}
	}
	return false
}

func responseContainsGoalTool(events []llm.ChatEvent) bool {
	for _, event := range events {
		if event.ToolCall == nil {
			continue
		}
		if strings.TrimSpace(event.ToolCall.Name) == "goal_complete" {
			return true
		}
	}
	return false
}

func responseContainsAnyToolCall(events []llm.ChatEvent) bool {
	for _, event := range events {
		if event.Type == llm.EventToolCallStart || event.Type == llm.EventToolCallDone {
			return true
		}
	}
	return false
}
