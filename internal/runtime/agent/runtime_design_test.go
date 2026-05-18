package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareTranscriptMovesSummaryContextAndMergesConsecutiveUsers(t *testing.T) {
	history := []session.SessionEntry{
		session.MetaEntry("older summary"),
		session.UserMessageEntry("please continue the old task"),
		session.UserMessageEntry("who are you"),
	}

	prepared := prepareTranscript(assembleMessages(history), defaultTranscriptPolicy())
	require.Len(t, prepared.Messages, 1)
	assert.Equal(t, "older summary", prepared.SummaryContext)
	assert.Equal(t, "user", prepared.Messages[0].Role)
	assert.Contains(t, prepared.Messages[0].Content, queuedContextHeader)
	assert.Contains(t, prepared.Messages[0].Content, "please continue the old task")
	assert.Contains(t, prepared.Messages[0].Content, currentMessageTag)
	assert.Contains(t, prepared.Messages[0].Content, "who are you")
}

func TestDeriveRequestFocusParsesStructuredFollowup(t *testing.T) {
	history := []session.SessionEntry{
		session.UserMessageEntry("[Earlier pending user turns for context]\n- finish review\n\n[Current message - respond to this]\nwho are you"),
	}

	focus := deriveRequestFocus(history, "fallback")
	assert.Equal(t, "who are you", focus.CurrentRequest)
	assert.Contains(t, focus.PendingContextSummary, "finish review")
}

func TestToolPreflightRepairsToolNameAndStringifiedJSON(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFileTool{})
	rt := &Runtime{
		Tools:         reg,
		ToolPreflight: ToolPreflightConfig{Enabled: true},
	}

	call, meta := rt.maybeRepairToolCall(llm.ToolCall{
		Name:  " Read-File ",
		Input: json.RawMessage(`"{\"file\":\"./notes.md\"}"`),
	})

	assert.Equal(t, "read_file", call.Name)
	assert.JSONEq(t, `{"path":"./notes.md"}`, string(call.Input))
	require.NotNil(t, meta)
	assert.Equal(t, true, meta["repair_applied"])
	assert.Equal(t, " Read-File ", meta["original_tool_name"])
}

func TestClassifyToolFailureTreatsContextDeadlineExceededAsRetryableTimeout(t *testing.T) {
	errorClass, autoRetryable, modelRecoverable := classifyToolFailure("callagent", "context deadline exceeded")
	assert.Equal(t, "timeout", errorClass)
	assert.True(t, autoRetryable)
	assert.True(t, modelRecoverable)
}

func TestClassifyToolFailureTreatsChromeConnectionRefusedAsNetworkError(t *testing.T) {
	errorClass, autoRetryable, modelRecoverable := classifyToolFailure("browser", "navigate failed: page load error net::ERR_CONNECTION_REFUSED")
	assert.Equal(t, "network_error", errorClass)
	assert.True(t, autoRetryable)
	assert.True(t, modelRecoverable)
}

func TestClassifyToolFailureTreatsBrowserLocalhostSSRFAsRetryable(t *testing.T) {
	errorClass, autoRetryable, modelRecoverable := classifyToolFailure("browser", "navigate failed: access to internal address localhost (::1) is blocked")
	assert.Equal(t, "network_error", errorClass)
	assert.True(t, autoRetryable)
	assert.True(t, modelRecoverable)
}

func TestWriteFileMalformedJSONRecoverySuggestsChunkedWriteOrPatch(t *testing.T) {
	errorMessage := "invalid input: unexpected end of JSON input"
	errorClass, autoRetryable, modelRecoverable := classifyToolFailure("write_file", errorMessage)
	assert.Equal(t, "validation_error", errorClass)
	assert.False(t, autoRetryable)
	assert.True(t, modelRecoverable)

	moves := tools.RecoveryHints("write_file", errorClass, errorMessage)
	require.NotEmpty(t, moves)
	joined := strings.Join(moves, "\n")
	assert.Contains(t, joined, "not valid JSON")
	assert.Contains(t, joined, "mode=overwrite")
	assert.Contains(t, joined, "mode=append")
	assert.Contains(t, joined, "expected_offset")
	assert.Contains(t, joined, "mode=patch")
}

func TestToolRecoveryBlockIncludesWriteFileChunkAndPatchGuidance(t *testing.T) {
	content := formatToolResultContent(session.ToolResultData{
		Error: "invalid input: unexpected end of JSON input",
		Metadata: map[string]any{
			"tool_name":            "write_file",
			"error_class":          "validation_error",
			"error_message":        "invalid input: unexpected end of JSON input",
			"model_recoverable":    true,
			"auto_retryable":       false,
			"suggested_next_moves": tools.RecoveryHints("write_file", "validation_error", "invalid input: unexpected end of JSON input"),
		},
	})

	assert.Contains(t, content, "status: failed")
	assert.Contains(t, content, "error type: validation_error")
	assert.Contains(t, content, "suggested next steps:")
	assert.Contains(t, content, "mode=append")
	assert.Contains(t, content, "expected_offset")
	assert.Contains(t, content, "mode=patch")
}

func TestParseCanonicalCallAgentInputFallsBackToCurrentRequest(t *testing.T) {
	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		CurrentRequest: "implement the reviewed patch",
	})

	in, err := parseCanonicalCallAgentInput(ctx, json.RawMessage(`{"target_agent":"coder"}`))
	require.NoError(t, err)
	assert.Equal(t, "coder", in.Agent)
	assert.Equal(t, "implement the reviewed patch", in.Task)
}

func TestParseCanonicalCallAgentInputParallelFallsBackToCurrentRequest(t *testing.T) {
	ctx := tools.WithRuntimeContext(context.Background(), tools.RuntimeContext{
		CurrentRequest: "请分别从各自视角整理风险",
	})

	in, err := parseCanonicalCallAgentInput(ctx, json.RawMessage(`{
		"mode": "parallel",
		"tasks": [
			{"target_agent": "web-researcher"},
			{"target_agent": "doc-analyzer"}
		]
	}`))
	require.NoError(t, err)
	require.Len(t, in.Tasks, 2)
	assert.Equal(t, "web-researcher", in.Tasks[0].Agent)
	assert.Equal(t, "请分别从各自视角整理风险", in.Tasks[0].Task)
	assert.Equal(t, "doc-analyzer", in.Tasks[1].Agent)
	assert.Equal(t, "请分别从各自视角整理风险", in.Tasks[1].Task)
}

func TestRuntimeSurfacesFallbackReplyForIncompleteTurn(t *testing.T) {
	rt := &Runtime{
		LLM: &scriptedLLMProvider{
			outcomes: []scriptedLLMOutcome{
				{
					events: []llm.ChatEvent{
						{Type: llm.EventTextDelta, Text: "partial"},
						{Type: llm.EventError, Error: errors.New("stream broke")},
					},
				},
			},
		},
		Tools:          tools.NewRegistry(),
		Session:        session.NewSession("assistant", "sess"),
		IncompleteTurn: IncompleteTurnConfig{Enabled: true},
	}

	events, err := rt.Run(context.Background(), "hello", nil)
	require.NoError(t, err)

	var gotIncomplete bool
	var gotFallback bool
	for event := range events {
		switch event.Type {
		case EventRunIncomplete:
			gotIncomplete = true
		case EventFallbackReply:
			gotFallback = true
		}
	}

	assert.True(t, gotIncomplete)
	assert.True(t, gotFallback)

	history := rt.Session.History()
	require.Len(t, history, 2)
	var msg session.MessageData
	require.NoError(t, json.Unmarshal(history[len(history)-1].Data, &msg))
	assert.Equal(t, incompleteTurnMessage, msg.Text)
}
