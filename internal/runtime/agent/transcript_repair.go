package agent

import (
	"strings"

	"github.com/Isites/anyai/internal/runtime/llm"
)

const (
	sessionSummaryPrefix        = "[Session Summary]\n"
	recoveredOrphanResultPrefix = "[Recovered orphaned tool result]\n"
)

type preparedTranscript struct {
	Messages       []llm.Message
	SummaryContext string
	DroppedOrphans []string
}

func prepareTranscript(raw []llm.Message, policy transcriptPolicy) preparedTranscript {
	out := preparedTranscript{
		Messages: append([]llm.Message(nil), raw...),
	}
	if !policy.enabled {
		return out
	}

	filtered := make([]llm.Message, 0, len(out.Messages))
	var summaries []string
	for _, msg := range out.Messages {
		switch {
		case policy.treatMetaAsSummaryContext &&
			msg.Role == "user" &&
			msg.ToolCallID == "" &&
			strings.HasPrefix(msg.Content, sessionSummaryPrefix):
			summaries = append(summaries, compactFocusText(strings.TrimPrefix(msg.Content, sessionSummaryPrefix), 400))
		case policy.dropOrphanToolResults &&
			msg.Role == "user" &&
			msg.ToolCallID == "" &&
			strings.HasPrefix(msg.Content, recoveredOrphanResultPrefix):
			out.DroppedOrphans = append(out.DroppedOrphans, compactFocusText(strings.TrimPrefix(msg.Content, recoveredOrphanResultPrefix), 200))
		default:
			filtered = append(filtered, msg)
		}
	}
	out.Messages = filtered
	out.SummaryContext = joinCompactLines(summaries, 2)

	if policy.mergeConsecutiveUserTurns {
		out.Messages = mergeConsecutiveUserTurns(out.Messages)
	}
	out.Messages = mergeConsecutiveAssistantTurns(out.Messages)
	return out
}

func mergeConsecutiveUserTurns(msgs []llm.Message) []llm.Message {
	if len(msgs) < 2 {
		return msgs
	}

	out := make([]llm.Message, 0, len(msgs))
	for i := 0; i < len(msgs); {
		if !isPlainUserMessage(msgs[i]) {
			out = append(out, msgs[i])
			i++
			continue
		}
		j := i + 1
		for j < len(msgs) && isPlainUserMessage(msgs[j]) {
			j++
		}
		group := msgs[i:j]
		if len(group) == 1 {
			out = append(out, group[0])
			i = j
			continue
		}

		current := group[len(group)-1]
		earlier := group[:len(group)-1]
		parts := make([]string, 0, len(earlier))
		for _, item := range earlier {
			text := compactFocusText(item.Content, 160)
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			current.Content = queuedContextHeader + "\n" +
				strings.Join(parts, "\n") +
				"\n\n" +
				currentMessageTag + "\n" +
				strings.TrimSpace(current.Content)
		}
		for _, item := range earlier {
			if len(item.Images) > 0 {
				current.Images = append(current.Images, item.Images...)
			}
		}
		out = append(out, current)
		i = j
	}
	return out
}

func mergeConsecutiveAssistantTurns(msgs []llm.Message) []llm.Message {
	if len(msgs) < 2 {
		return msgs
	}
	out := make([]llm.Message, 0, len(msgs))
	for _, msg := range msgs {
		if len(out) == 0 || !isMergeableAssistant(out[len(out)-1]) || !isMergeableAssistant(msg) {
			out = append(out, msg)
			continue
		}
		last := &out[len(out)-1]
		switch {
		case strings.TrimSpace(last.Content) == "":
			last.Content = msg.Content
		case strings.TrimSpace(msg.Content) == "":
		default:
			last.Content = strings.TrimSpace(last.Content) + "\n\n" + strings.TrimSpace(msg.Content)
		}
		if len(msg.Images) > 0 {
			last.Images = append(last.Images, msg.Images...)
		}
	}
	return out
}

func isPlainUserMessage(msg llm.Message) bool {
	return msg.Role == "user" &&
		msg.ToolCallID == "" &&
		len(msg.ToolCalls) == 0 &&
		!msg.IsError &&
		!strings.HasPrefix(msg.Content, compactionSummaryTag)
}

func isMergeableAssistant(msg llm.Message) bool {
	return msg.Role == "assistant" && msg.ToolCallID == "" && len(msg.ToolCalls) == 0
}

func joinCompactLines(items []string, limit int) string {
	if len(items) == 0 {
		return ""
	}
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	return strings.Join(items, " | ")
}
