package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

type runController struct {
	rt        *Runtime
	ctx       context.Context
	userMsg   string
	events    chan AgentEvent
	maxTurns  int
	toolNames []string
	loopState *toolLoopState

	hadToolActivity bool
	currentTurn     int
	goalID          task.GoalID

	goalCompletionPrompted bool
	lastTurnVisibleReply   bool

	finishOnce sync.Once
}

func newRunController(rt *Runtime, ctx context.Context, userMsg string, events chan AgentEvent) *runController {
	maxTurns := rt.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultRuntimeMaxTurns
	}
	return &runController{
		rt:        rt,
		ctx:       ctx,
		userMsg:   userMsg,
		events:    events,
		maxTurns:  maxTurns,
		toolNames: normalizeToolNames(rt.Tools.Names()),
		loopState: newToolLoopState(),
	}
}

func (c *runController) start(images []llm.ImageContent) {
	defer func() {
		if recovered := recover(); recovered != nil {
			c.finish(AgentEvent{
				Type:  EventError,
				Error: fmt.Errorf("agent run panic: %v", recovered),
			})
		}
	}()

	c.emit(AgentEvent{Type: EventRunStarted})
	c.rt.guardSessionToolResults("tool execution was interrupted before the current run started")
	c.appendUserMessage(images)
	c.initializeGoal()
	c.runTurn(0)
}

func (c *runController) appendUserMessage(images []llm.ImageContent) {
	if c == nil || c.rt == nil || c.rt.Session == nil {
		return
	}
	if len(images) == 0 {
		c.rt.Session.Append(session.UserMessageEntry(c.userMsg))
		return
	}

	imgData := make([]session.ImageData, 0, len(images))
	for _, img := range images {
		imgData = append(imgData, session.ImageData{
			MimeType: img.MimeType,
			Data:     base64.StdEncoding.EncodeToString(img.Data),
		})
	}
	c.rt.Session.Append(session.UserMessageWithImagesEntry(c.userMsg, imgData))
}

func (c *runController) buildChatRequest(
	baseCtx context.Context,
	turn int,
	toolNames []string,
	toolDefs []llm.ToolDef,
) (context.Context, llm.ChatRequest, error) {
	if c == nil || c.rt == nil || c.rt.Session == nil {
		return baseCtx, llm.ChatRequest{}, fmt.Errorf("agent session is not available")
	}

	turnCtx := baseCtx
	turnMeta := tools.RuntimeContextFrom(turnCtx)
	turnCtx = tools.WithRuntimeContext(turnCtx, turnMeta)
	history := c.rt.Session.History()
	if !historyEndsWithRuntimeControlPrompt(history) {
		if err := c.rt.maybeCompactSession(turnCtx); err != nil {
			return turnCtx, llm.ChatRequest{}, err
		}
	}

	normalizedToolNames := normalizeToolNames(toolNames)
	history = c.rt.Session.History()
	focus := deriveRequestFocus(history, c.userMsg)
	promptQuery := derivePromptQueryWithFocus(history, focus, c.userMsg)
	turnMeta.CurrentRequest = strings.TrimSpace(firstNonEmpty(focus.CurrentRequest, c.userMsg))
	turnCtx = tools.WithRuntimeContext(turnCtx, turnMeta)
	dynamicPromptContext := PromptContext{
		Tools: ToolSurface{
			Names: normalizedToolNames,
		},
		Retrieval: RetrievalSurface{
			CurrentQuery:          promptQuery,
			CurrentRequest:        focus.CurrentRequest,
			PendingContextSummary: focus.PendingContextSummary,
			SessionSummary:        focus.SessionSummary,
		},
	}
	if c.rt.GoalRuntime != nil && c.goalID != "" {
		if status, err := c.rt.GoalRuntime.Inspect(c.goalID); err == nil {
			dynamicPromptContext.Goal = goalSurfaceFromStatus(status)
		}
	}

	if c.rt.Skills != nil {
		dynamicPromptContext.Retrieval.MatchedSkills = c.rt.Skills.MatchSkills(promptQuery, 3)
	}
	if c.rt.Memory != nil {
		maxItems := c.rt.MemoryMaxItems
		if maxItems <= 0 {
			maxItems = 3
		}
		memScope := memory.SearchScope{AgentID: c.rt.AgentID}
		if c.rt.Session != nil {
			memScope.SessionID = c.rt.Session.ID
		}
		memMatches := c.rt.Memory.SearchExplainedScoped(promptQuery, maxItems, memScope, memory.LayerEpisodic, memory.LayerLongTerm)
		memEntries := make([]memory.Entry, 0, len(memMatches))
		for _, match := range memMatches {
			memEntries = append(memEntries, match.Entry)
		}
		if len(memMatches) > 0 {
			c.emit(AgentEvent{Type: EventMemoryRecall, Query: promptQuery, MemoryMatches: memMatches})
		}
		dynamicPromptContext.Retrieval.MemoryEntries = memEntries
	}

	systemPrompt, err := c.rt.composeSystemPrompt(turnCtx, turn, c.userMsg, history, normalizedToolNames, dynamicPromptContext)
	if err != nil {
		return turnCtx, llm.ChatRequest{}, err
	}

	history = c.rt.Session.History()
	prepared := prepareTranscript(assembleMessages(history), c.rt.transcriptPolicy())
	msgs := prepared.Messages
	if prepared.SummaryContext != "" && dynamicPromptContext.Retrieval.SessionSummary == "" {
		dynamicPromptContext.Retrieval.SessionSummary = prepared.SummaryContext
		systemPrompt, err = c.rt.composeSystemPrompt(turnCtx, turn, c.userMsg, history, normalizedToolNames, dynamicPromptContext)
		if err != nil {
			return turnCtx, llm.ChatRequest{}, err
		}
	}

	pruneToolResults(msgs, maxToolResultLen)

	req := llm.ChatRequest{
		Model:        c.rt.Model,
		Messages:     msgs,
		Tools:        toolDefs,
		MaxTokens:    8192,
		Temperature:  0.01,
		SystemPrompt: systemPrompt,
	}
	reqState, err := c.rt.applyBeforeModelResolveHooks(turnCtx, ModelResolveState{
		Turn:        turn,
		UserMessage: c.userMsg,
		History:     history,
		Request:     req,
	})
	if err != nil {
		return turnCtx, llm.ChatRequest{}, err
	}
	return turnCtx, reqState.Request, nil
}

func (c *runController) runTurn(turn int) {
	if c == nil || c.rt == nil {
		return
	}
	c.currentTurn = turn
	if c.rt.GoalRuntime != nil && c.goalID != "" {
		c.rt.GoalRuntime.RecordTurn(c.goalID, turn+1)
	}
	if turn >= c.maxTurns {
		c.finish(AgentEvent{
			Type:  EventError,
			Error: fmt.Errorf("agent exceeded maximum turns (%d)", c.maxTurns),
		})
		return
	}
	if c.ctx.Err() != nil {
		c.finish(AgentEvent{Type: EventAborted})
		return
	}

	turnCtx, req, err := c.buildChatRequest(c.ctx, turn, c.toolNames, c.rt.Tools.ToolDefs())
	if err != nil {
		c.finish(AgentEvent{Type: EventError, Error: err})
		return
	}

	var textContent strings.Builder
	var toolCalls []llm.ToolCall
	progress := turnProgress{}
	if err := c.rt.collectLLMResponse(turnCtx, c.events, req, &textContent, &toolCalls, &progress); err != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, textContent.String(), progress)
		c.finish(AgentEvent{Type: EventError, Error: err})
		return
	}

	if textContent.Len() > 0 {
		c.rt.Session.Append(session.AssistantMessageEntry(textContent.String()))
		progress.SavedAssistant = true
	}
	c.lastTurnVisibleReply = strings.TrimSpace(textContent.String()) != ""
	if len(toolCalls) == 0 {
		c.finish(AgentEvent{Type: EventDone})
		return
	}
	c.goalCompletionPrompted = false
	if c.rt.GoalRuntime != nil && c.goalID != "" {
		c.rt.GoalRuntime.RecordToolCalls(c.goalID, len(toolCalls))
	}

	for _, tc := range toolCalls {
		progress.SawToolCall = true
		meta := toolMetadataForCall(c.rt.Tools, tc.Name)
		if !meta.IsReadOnly() {
			progress.SideEffectRisk = true
		}
		c.rt.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tools.SanitizeRawJSON(tc.Input)))
	}

	toolWriter := newAgentEventWriter(c.events)
	preparedToolCalls := tools.PrepareToolCalls(c.rt.Tools, toolCalls)
	responseText := textContent.String()
	progressSnapshot := progress
	if err := c.rt.startTaskToolBatch(turnCtx, preparedToolCalls, toolWriter.Emit, c.loopState, turn, func(batchSummary tools.ToolBatchSummary, batchErr error) {
		c.handleToolBatchCompletion(turn, turnCtx, len(toolCalls), responseText, progressSnapshot, toolWriter, batchSummary, batchErr)
	}); err != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progressSnapshot)
		c.finish(AgentEvent{Type: EventError, Error: err})
	}
}

func (c *runController) handleToolBatchCompletion(
	turn int,
	turnCtx context.Context,
	_ int,
	responseText string,
	progress turnProgress,
	toolWriter *agentEventWriter,
	batchSummary tools.ToolBatchSummary,
	batchErr error,
) {
	onlyInlineGoalTools := batchContainsOnlyInlineGoalTools(batchSummary)
	if toolWriter != nil && !onlyInlineGoalTools {
		toolWriter.Emit(AgentEvent{
			Type:       EventToolFanoutCompleted,
			ToolFanout: buildToolFanoutInfo(batchSummary),
		})
	}

	fatalBatchErr := firstFatalBatchExecutionError(batchSummary)
	if batchSummary.StartedCount > 0 {
		progress.ExecutedTool = true
		c.hadToolActivity = true
	}
	c.rt.appendToolBatchResults(batchSummary.Calls)

	if batchErr != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progress)
		if errors.Is(batchErr, context.Canceled) || errors.Is(batchErr, context.DeadlineExceeded) || turnCtx.Err() != nil {
			c.finish(AgentEvent{Type: EventAborted})
			return
		}
		c.finish(AgentEvent{Type: EventError, Error: batchErr})
		return
	}
	if fatalBatchErr != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progress)
		c.finish(AgentEvent{Type: EventError, Error: fatalBatchErr})
		return
	}
	if turnCtx.Err() != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progress)
		c.finish(AgentEvent{Type: EventAborted})
		return
	}
	if onlyInlineGoalTools && c.goalAlreadyCompleted() {
		c.finish(AgentEvent{Type: EventDone})
		return
	}

	c.runTurn(turn + 1)
}

func (c *runController) emit(event AgentEvent) {
	if c == nil || c.events == nil {
		return
	}
	c.events <- event
}

func (c *runController) finish(ev AgentEvent) {
	if c == nil {
		return
	}
	if ev.Type == EventDone {
		continued, err := c.advanceGoalAfterDone()
		if err != nil {
			ev = AgentEvent{Type: EventError, Error: err}
		} else if continued {
			return
		}
	}
	c.finalize(ev)
}

func (c *runController) finalize(ev AgentEvent) {
	if c == nil {
		return
	}
	c.finishOnce.Do(func() {
		if ev.Type == EventError || ev.Type == EventAborted {
			c.rt.guardSessionToolResults(toolGuardReasonForEvent(ev))
		}
		if err := c.rt.applyAgentEndHooks(c.ctx, AgentEndState{
			FinalEvent: ev,
			Session:    c.rt.Session,
		}); err != nil {
			ev = AgentEvent{Type: EventError, Error: fmt.Errorf("agent end hook failed: %w", err)}
			c.rt.guardSessionToolResults(toolGuardReasonForEvent(ev))
		}
		c.emit(ev)
		close(c.events)
	})
}

func (c *runController) initializeGoal() {
	if c == nil || c.rt == nil || c.rt.GoalRuntime == nil || c.goalID != "" {
		return
	}
	meta := tools.RuntimeContextFrom(c.ctx)
	goal := c.rt.GoalRuntime.StartGoal(
		firstNonEmpty(strings.TrimSpace(c.rt.AgentCallContract.TaskGoal), strings.TrimSpace(c.userMsg)),
		firstNonEmpty(meta.SessionID, sessionIDFromRuntime(c.rt)),
		meta.RunID,
	)
	if goal != nil {
		c.goalID = goal.ID
	}
}

func (c *runController) goalAlreadyCompleted() bool {
	if c == nil || c.rt == nil || c.rt.GoalRuntime == nil || c.goalID == "" {
		return false
	}
	current, ok := c.rt.GoalRuntime.GetGoal(c.goalID)
	if !ok || current == nil {
		return false
	}
	return current.State == task.GoalStateCompleted
}

func batchContainsOnlyInlineGoalTools(summary tools.ToolBatchSummary) bool {
	if len(summary.Calls) == 0 {
		return false
	}
	for _, call := range summary.Calls {
		if !isInlineGoalTool(call.Call.Name) {
			return false
		}
	}
	return true
}

func historyEndsWithRuntimeControlPrompt(history []session.SessionEntry) bool {
	if len(history) == 0 {
		return false
	}
	last := history[len(history)-1]
	if last.Type != session.EntryTypeMessage || last.Role != "user" {
		return false
	}
	var data session.MessageData
	if err := json.Unmarshal(last.Data, &data); err != nil {
		return false
	}
	text := strings.TrimSpace(data.Text)
	return strings.HasPrefix(text, "[Runtime goal continuation]") || strings.HasPrefix(text, "[Runtime hard stop]")
}
