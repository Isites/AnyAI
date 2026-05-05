package agent

import (
	"encoding/json"
	"strings"

	"github.com/Isites/anyai/internal/runtime/session"
)

const (
	queuedContextHeader = "[Earlier pending user turns for context]"
	currentMessageTag   = "[Current message - respond to this]"
)

type RequestFocus struct {
	CurrentRequest        string
	PendingContextSummary string
	SessionSummary        string
}

func deriveRequestFocus(history []session.SessionEntry, fallback string) RequestFocus {
	focus := RequestFocus{
		CurrentRequest: strings.TrimSpace(fallback),
	}
	if len(history) == 0 {
		return focus
	}

	for i := len(history) - 1; i >= 0; i-- {
		entry := history[i]
		switch entry.Type {
		case session.EntryTypeMeta:
			if focus.SessionSummary != "" {
				continue
			}
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			focus.SessionSummary = compactFocusText(md.Text, 320)
		case session.EntryTypeMessage:
			if entry.Role != "user" {
				continue
			}
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			current, pending, ok := parseStructuredFollowup(md.Text)
			if ok {
				if current != "" {
					focus.CurrentRequest = current
				}
				if pending != "" {
					focus.PendingContextSummary = pending
				}
				return focus
			}
			if text := strings.TrimSpace(md.Text); text != "" {
				focus.CurrentRequest = compactFocusText(text, 320)
			}
			if summary := summarizeTailUserTurns(history[:i], 3); summary != "" {
				focus.PendingContextSummary = summary
			}
			return focus
		}
	}
	return focus
}

func parseStructuredFollowup(text string) (current string, pending string, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" || !strings.Contains(text, currentMessageTag) {
		return "", "", false
	}

	if idx := strings.Index(text, currentMessageTag); idx >= 0 {
		current = strings.TrimSpace(text[idx+len(currentMessageTag):])
		head := strings.TrimSpace(text[:idx])
		if strings.HasPrefix(head, queuedContextHeader) {
			pending = strings.TrimSpace(strings.TrimPrefix(head, queuedContextHeader))
		}
	}
	current = compactFocusText(current, 320)
	pending = compactFocusText(pending, 320)
	return current, pending, current != ""
}

func summarizeTailUserTurns(history []session.SessionEntry, limit int) string {
	if len(history) == 0 || limit <= 0 {
		return ""
	}

	var items []string
	for i := len(history) - 1; i >= 0; i-- {
		entry := history[i]
		if entry.Type != session.EntryTypeMessage {
			if len(items) > 0 {
				break
			}
			continue
		}
		if entry.Role != "user" {
			if len(items) > 0 {
				break
			}
			continue
		}
		var md session.MessageData
		if err := json.Unmarshal(entry.Data, &md); err != nil {
			continue
		}
		text := compactFocusText(md.Text, 120)
		if text == "" {
			continue
		}
		items = append(items, text)
		if len(items) >= limit {
			break
		}
	}
	if len(items) == 0 {
		return ""
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return strings.Join(items, " | ")
}

func compactFocusText(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	cut := maxLen - 3
	if cut < 1 {
		return text[:maxLen]
	}
	return strings.TrimSpace(text[:cut]) + "..."
}
