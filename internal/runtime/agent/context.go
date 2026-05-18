package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/session"
)

const maxToolResultLen = 10000 // truncate tool results longer than this

type transcriptBuilder struct {
	history          []session.SessionEntry
	policy           transcriptPolicy
	runtimeDirective string
}

func newTranscriptBuilder(history []session.SessionEntry, policy transcriptPolicy) transcriptBuilder {
	return transcriptBuilder{
		history: append([]session.SessionEntry(nil), history...),
		policy:  policy,
	}
}

func (b transcriptBuilder) WithRuntimeDirective(text string) transcriptBuilder {
	b.runtimeDirective = strings.TrimSpace(text)
	return b
}

func (b transcriptBuilder) Build() preparedTranscript {
	prepared := prepareTranscript(assembleMessages(b.history), b.policy)
	if b.runtimeDirective != "" {
		prepared.Messages = append(prepared.Messages, llm.Message{
			Role:    llm.MessageRoleRuntime,
			Content: b.runtimeDirective,
		})
	}
	return prepared
}

// detectImageMIME returns the actual MIME type based on magic bytes.
// Falls back to the provided hint if the format is unrecognized.
func detectImageMIME(data []byte, hint string) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 4 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}
	if len(data) >= 4 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' {
		return "image/webp"
	}
	return hint
}

// assembleMessages converts session history into LLM messages.
// It ensures that every tool_use block in an assistant message has a
// corresponding tool_result in the next user message. Orphaned tool calls
// (e.g. from interrupted sessions) get synthetic error results injected.
func assembleMessages(history []session.SessionEntry) []llm.Message {
	history = session.RepairLeadingFragment(history)
	history = session.ModelVisibleEntries(history)
	var msgs []llm.Message
	knownToolCalls := make(map[string]struct{})

	for _, entry := range history {
		switch entry.Type {
		case session.EntryTypeMeta:
			// Meta entries (e.g. compaction summaries) are treated as system context
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			msgs = append(msgs, llm.Message{
				Role:    llm.MessageRoleUser,
				Content: "[Session Summary]\n" + md.Text,
			})

		case session.EntryTypeCompaction:
			var compact session.CompactionData
			if err := json.Unmarshal(entry.Data, &compact); err != nil {
				continue
			}
			text := strings.TrimSpace(compact.Text)
			if text == "" {
				continue
			}
			msgs = append(msgs, llm.Message{
				Role:    llm.MessageRoleUser,
				Content: compactionSummaryPrefix + text,
			})

		case session.EntryTypeMessage:
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			// Before appending a new message, check if the last assistant
			// message has orphaned tool calls that need synthetic results.
			msgs = injectMissingToolResults(msgs)
			msg := llm.Message{
				Role:    entry.Role,
				Content: md.Text,
			}
			// Convert session images to LLM image content
			if llm.IsUserRole(entry.Role) {
				for _, img := range md.Images {
					data, mimeType := sessionImageContent(img)
					if len(data) == 0 {
						continue
					}
					msg.Images = append(msg.Images, llm.ImageContent{
						MimeType: detectImageMIME(data, mimeType),
						Data:     data,
					})
				}
			}
			msgs = append(msgs, msg)

		case session.EntryTypeToolCall:
			var td session.ToolCallData
			if err := json.Unmarshal(entry.Data, &td); err != nil {
				continue
			}
			// Tool calls are part of the assistant turn — merge into the last assistant message
			// or create one if needed
			if len(msgs) == 0 || !llm.IsAssistantRole(msgs[len(msgs)-1].Role) {
				msgs = append(msgs, llm.Message{Role: llm.MessageRoleAssistant})
			}
			msgs[len(msgs)-1].ToolCalls = append(msgs[len(msgs)-1].ToolCalls, llm.ToolCall{
				ID:    td.ID,
				Name:  td.Tool,
				Input: td.Input,
			})
			if strings.TrimSpace(td.ID) != "" {
				knownToolCalls[td.ID] = struct{}{}
			}

		case session.EntryTypeToolResult:
			var tr session.ToolResultData
			if err := json.Unmarshal(entry.Data, &tr); err != nil {
				continue
			}
			content := formatToolResultContent(tr)
			if strings.TrimSpace(tr.ToolCallID) != "" {
				if _, ok := knownToolCalls[tr.ToolCallID]; !ok {
					msgs = append(msgs, llm.Message{
						Role:    llm.MessageRoleUser,
						Content: "[Recovered orphaned tool result]\n" + content,
						IsError: tr.IsError || tr.Error != "",
					})
					continue
				}
			}
			msg := llm.Message{
				Role:       llm.MessageRoleUser,
				Content:    content,
				ToolCallID: tr.ToolCallID,
				IsError:    tr.IsError,
			}
			// Convert session images to LLM image content
			for _, img := range tr.Images {
				data, mimeType := sessionImageContent(img)
				if len(data) == 0 {
					continue
				}
				msg.Images = append(msg.Images, llm.ImageContent{
					MimeType: detectImageMIME(data, mimeType),
					Data:     data,
				})
			}
			msgs = append(msgs, msg)
		}
	}

	// Final check: handle orphaned tool calls at the end of history.
	msgs = injectMissingToolResults(msgs)

	return msgs
}

func sessionImageContent(img session.ImageData) ([]byte, string) {
	mimeType := strings.TrimSpace(img.MimeType)
	if dataText := strings.TrimSpace(img.Data); dataText != "" {
		data, err := base64.StdEncoding.DecodeString(dataText)
		if err != nil {
			return nil, mimeType
		}
		return data, mimeType
	}
	path := strings.TrimSpace(img.Path)
	if path == "" {
		return nil, mimeType
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, mimeType
	}
	return data, mimeType
}

func formatToolResultContent(tr session.ToolResultData) string {
	content := tr.Output
	if tr.Error != "" {
		content = tr.Error
	}
	if strings.TrimSpace(content) == "" {
		content = "(no output)"
	}

	recovery := formatToolRecoveryBlock(tr)
	if recovery == "" {
		return content
	}
	return content + "\n\n" + recovery
}

func formatToolRecoveryBlock(tr session.ToolResultData) string {
	if len(tr.Metadata) == 0 {
		return ""
	}

	toolName := metadataString(tr.Metadata, "tool_name")
	errorClass := metadataString(tr.Metadata, "error_class")
	syntheticReason := metadataString(tr.Metadata, "synthetic_reason")
	inputSummary := metadataString(tr.Metadata, "input_summary")
	errorMessage := metadataString(tr.Metadata, "error_message")
	autoRetryable, hasAutoRetryable := metadataBoolField(tr.Metadata, "auto_retryable")
	modelRecoverable, hasModelRecoverable := metadataBoolField(tr.Metadata, "model_recoverable")
	retryCount := metadataIntField(tr.Metadata, "retry_count")
	synthetic, _ := metadataBoolField(tr.Metadata, "synthetic")
	warning, _ := tr.Metadata["warning"].(map[string]any)
	suggestedNextMoves := metadataStringSlice(tr.Metadata, "suggested_next_moves")
	blocked, warningBlocked := metadataBoolField(warning, "blocked")

	hasRecovery := synthetic ||
		strings.TrimSpace(tr.Error) != "" ||
		errorMessage != "" ||
		len(warning) > 0 ||
		retryCount > 0 ||
		errorClass != "" ||
		hasAutoRetryable ||
		hasModelRecoverable ||
		syntheticReason != "" ||
		len(suggestedNextMoves) > 0
	if !hasRecovery {
		return ""
	}

	var lines []string

	switch {
	case synthetic:
		lines = append(lines, "status: failed")
		lines = append(lines, "note: this is a synthetic runtime result because the original tool call did not finish cleanly.")
	case warningBlocked && blocked:
		lines = append(lines, "status: blocked")
	case tr.Error != "" || errorMessage != "" || len(warning) > 0:
		lines = append(lines, "status: failed")
	case retryCount > 0:
		lines = append(lines, "status: completed after retry")
	}

	if strings.TrimSpace(tr.ToolCallID) == "" && toolName != "" {
		lines = append(lines, "tool: "+toolName)
	}
	if errorClass != "" {
		lines = append(lines, "error type: "+errorClass)
	}
	if retryCount > 0 {
		lines = append(lines, fmt.Sprintf("previous retries: %d", retryCount))
	}
	if hasAutoRetryable {
		lines = append(lines, "runtime retry: "+yesNo(autoRetryable))
	}
	if hasModelRecoverable {
		if modelRecoverable {
			lines = append(lines, "next step: inspect this failure and choose a corrected follow-up call yourself.")
		} else {
			lines = append(lines, "next step: do not repeat the same call pattern without new information.")
		}
	}
	if inputSummary != "" {
		lines = append(lines, "previous input: "+inputSummary)
	}
	if syntheticReason != "" {
		lines = append(lines, "synthetic reason: "+syntheticReason)
	}

	if len(warning) > 0 {
		if message := metadataString(warning, "message"); message != "" {
			lines = append(lines, "warning: "+message)
		}
	}

	if len(suggestedNextMoves) > 0 {
		lines = append(lines, "suggested next steps:")
		for _, move := range suggestedNextMoves {
			lines = append(lines, "- "+move)
		}
	}

	return strings.Join(lines, "\n")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func metadataBoolField(metadata map[string]any, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	value, ok := metadata[key].(bool)
	return value, ok
}

func metadataIntField(metadata map[string]any, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func metadataStringSlice(metadata map[string]any, key string) []string {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata[key]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		out := make([]string, 0, len(value))
		for _, item := range value {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			text, _ := item.(string)
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// injectMissingToolResults checks if the last assistant message has tool calls
// without corresponding tool results following it. If so, it injects synthetic
// error results so the message sequence is valid for the LLM API.
func injectMissingToolResults(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}
	last := msgs[len(msgs)-1]
	if !llm.IsAssistantRole(last.Role) || len(last.ToolCalls) == 0 {
		return msgs
	}

	// Collect tool call IDs that already have results after this assistant message.
	// Since this is called before appending a non-tool-result message, any results
	// would already be in msgs. We only need to check if results exist at all.
	// The assistant message is the last one, so there are no results yet.
	for _, tc := range last.ToolCalls {
		msgs = append(msgs, llm.Message{
			Role:       llm.MessageRoleUser,
			Content:    "(tool execution was interrupted)",
			ToolCallID: tc.ID,
			IsError:    true,
		})
	}
	return msgs
}

// pruneToolResults truncates oversized tool results in the message history
// to prevent context window overflow. Only affects tool result messages
// (identified by having a ToolCallID).
func pruneToolResults(msgs []llm.Message, maxLen int) {
	for i := range msgs {
		if msgs[i].ToolCallID != "" && len(msgs[i].Content) > maxLen {
			originalLen := len(msgs[i].Content)
			truncated := msgs[i].Content[:maxLen]
			// Try to cut at a newline boundary
			if idx := strings.LastIndex(truncated, "\n"); idx > maxLen/2 {
				truncated = truncated[:idx]
			}
			msgs[i].Content = fmt.Sprintf("%s\n\n[output truncated — %d chars total]", truncated, originalLen)
		}
	}
}
