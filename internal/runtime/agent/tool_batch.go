package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/tool"
)

type agentEventWriter struct {
	mu     sync.Mutex
	events chan<- AgentEvent
}

func newAgentEventWriter(events chan<- AgentEvent) *agentEventWriter {
	return &agentEventWriter{events: events}
}

func (w *agentEventWriter) Emit(event AgentEvent) {
	if w == nil || w.events == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events <- event
}

func toolMetadataForCall(executor tools.Executor, name string) tools.ToolMetadata {
	if executor != nil {
		if tool, ok := executor.Get(name); ok {
			return tools.DescribeToolMetadata(tool)
		}
	}
	return tools.DefaultToolMetadata(name)
}

func fatalToolExecutionResult(call llm.ToolCall, err error) tools.ToolResult {
	message := "tool execution failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = strings.TrimSpace(err.Error())
	}
	errorClass, autoRetryable, modelRecoverable := classifyToolFailure(call.Name, message)
	if errorClass == "" {
		errorClass = "execution_failed"
	}
	decision := "failed"
	if errorClass == "timeout" || errorClass == "interrupted" {
		decision = "cancelled"
	}
	return tools.SanitizeToolResult(tools.ToolResult{
		Error: message,
		Metadata: map[string]any{
			"tool_name":            call.Name,
			"input_summary":        summarizeToolInput(call.Input),
			"error_message":        message,
			"error_class":          errorClass,
			"auto_retryable":       autoRetryable,
			"model_recoverable":    modelRecoverable,
			"suggested_next_moves": suggestedNextMoves(call.Name, errorClass),
			"decision":             decision,
			"fatal_execution_err":  !(modelRecoverable && errorClass == "validation_error"),
		},
	})
}

func buildToolFanoutInfo(summary tools.ToolBatchSummary) *ToolFanoutInfo {
	info := &ToolFanoutInfo{
		TotalCount:     summary.TotalCount,
		StartedCount:   summary.StartedCount,
		CompletedCount: summary.CompletedCount,
		FailedCount:    summary.FailedCount,
		Status:         summary.Status,
	}
	if len(summary.Calls) == 0 {
		return info
	}
	info.Calls = make([]ToolFanoutCallInfo, 0, len(summary.Calls))
	for _, call := range summary.Calls {
		if call.Call.ID == "" && call.Call.Name == "" {
			continue
		}
		status := "completed"
		switch {
		case call.CompletedAt.IsZero():
			status = "pending"
		case call.Error != nil:
			status = "failed"
		case strings.TrimSpace(call.Result.Error) != "":
			status = "failed"
		}
		info.Calls = append(info.Calls, ToolFanoutCallInfo{
			ID:             call.Call.ID,
			ToolName:       call.Call.Name,
			Status:         status,
			StartedOrder:   call.StartedOrder,
			CompletedOrder: call.CompletedOrder,
			DurationMS:     durationMillis(call.StartedAt, call.CompletedAt),
		})
	}
	return info
}

func firstFatalBatchExecutionError(summary tools.ToolBatchSummary) error {
	for _, outcome := range summary.Calls {
		if outcome.Error == nil {
			continue
		}
		if !metadataBoolValue(outcome.Result.Metadata, "fatal_execution_err") {
			continue
		}
		decision := metadataStringValue(outcome.Result.Metadata, "decision")
		if decision == "cancelled" {
			continue
		}
		return outcome.Error
	}
	return nil
}

func (r *Runtime) appendToolBatchResults(outcomes []tools.ToolCallOutcome) {
	if r == nil || r.Session == nil {
		return
	}
	for _, outcome := range outcomes {
		if outcome.Call.ID == "" || outcome.StartedAt.IsZero() {
			continue
		}
		if outcome.Error != nil && strings.TrimSpace(outcome.Result.Error) == "" && len(outcome.Result.Metadata) == 0 {
			continue
		}
		var imgData []session.ImageData
		for _, img := range outcome.Result.Images {
			imgData = append(imgData, session.ImageData{
				MimeType: img.MimeType,
				Data:     base64.StdEncoding.EncodeToString(img.Data),
			})
		}
		r.Session.Append(session.ToolResultEntryWithMetadata(
			outcome.Call.ID,
			outcome.Result.Output,
			outcome.Result.Error,
			outcome.Result.Metadata,
			imgData,
		))
	}
}

func logToolOutcome(call llm.ToolCall, result tools.ToolResult) {
	if result.Error != "" {
		runtimelogging.Warn("tool error", "tool", call.Name, "id", call.ID, "error", result.Error)
	}
}

func durationMillis(startedAt, completedAt time.Time) int64 {
	if startedAt.IsZero() || completedAt.IsZero() || completedAt.Before(startedAt) {
		return 0
	}
	return completedAt.Sub(startedAt).Milliseconds()
}

func metadataBoolValue(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	value, ok := metadata[key]
	if !ok {
		return false
	}
	flag, _ := value.(bool)
	return flag
}

func metadataStringValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func withParentActivityBridge(ctx context.Context, emit func(AgentEvent)) context.Context {
	if emit == nil {
		return ctx
	}
	return runtimeactivity.WithHook(ctx, func(_ map[string]any) {
		emit(AgentEvent{Type: EventActivity})
	})
}

func (r *Runtime) submitToolTask(
	ctx context.Context,
	call llm.ToolCall,
	emit func(AgentEvent),
	loopState *toolLoopState,
	turn int,
	callback task.Callback,
) (string, error) {
	if r == nil {
		return "", fmt.Errorf("agent runtime is nil")
	}
	r.ensureTaskRuntime()

	meta := tools.RuntimeContextFrom(ctx)
	taskCtx := tools.WithExecutorContext(withParentActivityBridge(ctx, emit), r.Tools)
	taskCtx = tools.WithTaskToolExecution(taskCtx, func(execCtx context.Context, taskCall llm.ToolCall) (tools.ToolResult, error) {
		result, err := r.executeToolWithRecovery(execCtx, taskCall, emit, loopState, turn)
		if err != nil {
			return fatalToolExecutionResult(taskCall, err), err
		}
		return result, nil
	})

	spec := r.taskSpecForCall(meta, call, turn)
	spec.OnComplete = callback
	return r.doTask(taskCtx, spec)
}

func (r *Runtime) startTaskToolBatch(
	ctx context.Context,
	prepared []tools.ToolCallSpec,
	emit func(AgentEvent),
	loopState *toolLoopState,
	turn int,
	callback func(tools.ToolBatchSummary, error),
) error {
	if callback == nil {
		return fmt.Errorf("tool batch callback is nil")
	}
	if r == nil {
		return fmt.Errorf("agent runtime is nil")
	}
	go func() {
		summary, err := r.runTaskToolBatch(ctx, prepared, emit, loopState, turn)
		callback(summary, err)
	}()
	return nil
}

func (r *Runtime) runTaskToolBatch(
	ctx context.Context,
	prepared []tools.ToolCallSpec,
	emit func(AgentEvent),
	loopState *toolLoopState,
	turn int,
) (tools.ToolBatchSummary, error) {
	if r == nil {
		return tools.ToolBatchSummary{}, fmt.Errorf("agent runtime is nil")
	}
	r.ensureTaskRuntime()
	if len(prepared) == 0 {
		return tools.ToolBatchSummary{Status: "completed"}, nil
	}

	outcomes := make([]tools.ToolCallOutcome, len(prepared))
	batchStartedAt := time.Now().UTC()
	segments := segmentPreparedToolCalls(prepared)
	startedBase := 0
	completedBase := 0

	for _, segment := range segments {
		if len(segment) == 0 {
			continue
		}
		if ctx.Err() != nil {
			return summarizeTaskToolBatch(outcomes, len(prepared), batchStartedAt, time.Now().UTC(), ctx.Err()), ctx.Err()
		}

		joined, err := r.TaskRuntime.JoinAllSettled(ctx, task.JoinSpec{
			Count:       len(segment),
			MaxParallel: segmentMaxParallel(segment),
		}, func(joinCtx context.Context, index int, settle func(task.Record)) {
			spec := segment[index]
			startedAt := time.Now().UTC()
			if !isInlineGoalTool(spec.Call.Name) {
				emit(AgentEvent{
					Type:         EventToolCallStart,
					ToolCall:     &spec.Call,
					ToolMetadata: &spec.Metadata,
				})
			}

			if handled, handleErr := r.submitInlineGoalToolTask(joinCtx, spec, func(result tools.ToolResult, record task.Record, resultErr error) {
				completedAt := time.Now().UTC()
				outcomes[spec.Index] = tools.ToolCallOutcome{
					Index:       spec.Index,
					Call:        spec.Call,
					Metadata:    spec.Metadata,
					Result:      result,
					Error:       resultErr,
					StartedAt:   startedAt,
					CompletedAt: completedAt,
				}
				logToolOutcome(spec.Call, result)
				settle(record)
			}); handled {
				if handleErr != nil {
					failed := fatalToolExecutionResult(spec.Call, handleErr)
					outcomes[spec.Index] = tools.ToolCallOutcome{
						Index:       spec.Index,
						Call:        spec.Call,
						Metadata:    spec.Metadata,
						Result:      failed,
						Error:       handleErr,
						StartedAt:   startedAt,
						CompletedAt: time.Now().UTC(),
					}
					logToolOutcome(spec.Call, failed)
					settle(synthesizeFailedToolTaskRecord(spec, failed, handleErr))
				}
				return
			}

			if handled, handleErr := r.submitCallAgentToolTask(joinCtx, spec, emit, func(result tools.ToolResult, record task.Record, resultErr error) {
				completedAt := time.Now().UTC()
				outcomes[spec.Index] = tools.ToolCallOutcome{
					Index:       spec.Index,
					Call:        spec.Call,
					Metadata:    spec.Metadata,
					Result:      result,
					Error:       resultErr,
					StartedAt:   startedAt,
					CompletedAt: completedAt,
				}
				logToolOutcome(spec.Call, result)
				emit(AgentEvent{
					Type:         EventToolResult,
					ToolCall:     &spec.Call,
					ToolMetadata: &spec.Metadata,
					Result:       &result,
				})
				settle(record)
			}); handled {
				if handleErr != nil {
					failed := fatalToolExecutionResult(spec.Call, handleErr)
					outcomes[spec.Index] = tools.ToolCallOutcome{
						Index:       spec.Index,
						Call:        spec.Call,
						Metadata:    spec.Metadata,
						Result:      failed,
						Error:       handleErr,
						StartedAt:   startedAt,
						CompletedAt: time.Now().UTC(),
					}
					logToolOutcome(spec.Call, failed)
					emit(AgentEvent{
						Type:         EventToolResult,
						ToolCall:     &spec.Call,
						ToolMetadata: &spec.Metadata,
						Result:       &failed,
					})
					settle(synthesizeFailedToolTaskRecord(spec, failed, handleErr))
				}
				return
			}

			_, submitErr := r.submitToolTask(joinCtx, spec.Call, emit, loopState, turn, func(_ context.Context, completion task.Completion) {
				result, record, waitErr := toolTaskResultFromCompletion(joinCtx, completion)
				completedAt := time.Now().UTC()
				outcomes[spec.Index] = tools.ToolCallOutcome{
					Index:       spec.Index,
					Call:        spec.Call,
					Metadata:    spec.Metadata,
					Result:      result,
					Error:       waitErr,
					StartedAt:   startedAt,
					CompletedAt: completedAt,
				}
				logToolOutcome(spec.Call, result)
				emit(AgentEvent{
					Type:         EventToolResult,
					ToolCall:     &spec.Call,
					ToolMetadata: &spec.Metadata,
					Result:       &result,
				})
				settle(record)
			})
			if submitErr != nil {
				failed := fatalToolExecutionResult(spec.Call, submitErr)
				outcomes[spec.Index] = tools.ToolCallOutcome{
					Index:       spec.Index,
					Call:        spec.Call,
					Metadata:    spec.Metadata,
					Result:      failed,
					Error:       submitErr,
					StartedAt:   startedAt,
					CompletedAt: time.Now().UTC(),
				}
				logToolOutcome(spec.Call, failed)
				emit(AgentEvent{
					Type:         EventToolResult,
					ToolCall:     &spec.Call,
					ToolMetadata: &spec.Metadata,
					Result:       &failed,
				})
				settle(synthesizeFailedToolTaskRecord(spec, failed, submitErr))
				return
			}
		})
		if err != nil {
			return tools.ToolBatchSummary{}, err
		}

		for idx, item := range joined.Results {
			spec := segment[idx]
			outcome := outcomes[spec.Index]
			outcome.StartedOrder = startedBase + item.StartedOrder
			outcome.CompletedOrder = completedBase + item.FinishOrder
			if outcome.StartedAt.IsZero() {
				outcome.StartedAt = item.StartedAt
			}
			if outcome.CompletedAt.IsZero() {
				outcome.CompletedAt = item.CompletedAt
			}
			outcomes[spec.Index] = outcome
		}
		startedBase += len(segment)
		completedBase += len(segment)
	}

	return summarizeTaskToolBatch(outcomes, len(prepared), batchStartedAt, time.Now().UTC(), nil), nil
}

func (r *Runtime) submitCallAgentToolTask(
	ctx context.Context,
	spec tools.ToolCallSpec,
	emit func(AgentEvent),
	callback func(tools.ToolResult, task.Record, error),
) (bool, error) {
	callAgentTool, ok := lookupCallAgentTool(r.Tools, spec.Call.Name)
	if callback == nil || !ok || callAgentTool == nil {
		return false, nil
	}

	if callAgentTool.Runner == nil {
		return true, fmt.Errorf("agent call runner not available")
	}

	in, err := parseCanonicalCallAgentInput(ctx, spec.Call.Input)
	if err != nil {
		return true, err
	}

	callCtx := withParentActivityBridge(ctx, emit)

	if in.Mode == "parallel" {
		_, err := callAgentTool.Runner.DoAgentParallel(callCtx, in.Tasks, callAgentTool.MaxParallel, func(_ context.Context, result tools.ParallelAgentCallResult) {
			payload, _ := json.Marshal(result)
			toolResult := tools.ToolResult{Output: string(payload)}
			callback(toolResult, workflowToolRecord(spec, toolResult, nil), nil)
		})
		return true, err
	}

	req := tools.AgentCallRequest{
		Agent:         in.Agent,
		Task:          in.Task,
		IdleTimeoutMS: in.IdleTimeoutMS,
		Contract:      in.Contract.Normalized(),
	}
	_, err = callAgentTool.Runner.DoAgent(callCtx, req, func(_ context.Context, result tools.AgentCallResult) {
		payload, _ := json.Marshal(result)
		toolResult := tools.ToolResult{Output: string(payload)}
		callback(toolResult, workflowToolRecord(spec, toolResult, nil), nil)
	})
	return true, err
}

func (r *Runtime) submitInlineGoalToolTask(
	ctx context.Context,
	spec tools.ToolCallSpec,
	callback func(tools.ToolResult, task.Record, error),
) (bool, error) {
	if callback == nil || !isInlineGoalTool(spec.Call.Name) {
		return false, nil
	}
	result, err := r.executeToolAttempt(ctx, spec.Call)
	if err != nil {
		callback(fatalToolExecutionResult(spec.Call, err), task.Record{}, err)
		return true, nil
	}
	callback(result, workflowToolRecord(spec, result, nil), nil)
	return true, nil
}

func isInlineGoalTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "goal_complete", "await_user_input":
		return true
	default:
		return false
	}
}

func toolTaskResultFromCompletion(ctx context.Context, completion task.Completion) (tools.ToolResult, task.Record, error) {
	record := completion.Record
	if record.ID == "" {
		record.ID = completion.TaskID
	}
	result := completion.Result
	toolResult := tools.SanitizeToolResult(tools.ToolResult{
		Output:   result.Summary,
		Error:    result.Error,
		Metadata: cloneToolMetadata(result.Metadata),
		Images:   append([]llm.ImageContent(nil), result.Images...),
	})

	switch {
	case result.Status == task.StatusCancelled:
		if ctx.Err() != nil {
			return toolResult, record, ctx.Err()
		}
		if strings.TrimSpace(result.Error) != "" {
			return toolResult, record, fmt.Errorf("%s", result.Error)
		}
		return toolResult, record, context.Canceled
	case result.Status == task.StatusFailed && metadataBoolValue(toolResult.Metadata, "fatal_execution_err"):
		if strings.TrimSpace(result.Error) != "" {
			return toolResult, record, fmt.Errorf("%s", result.Error)
		}
		return toolResult, record, fmt.Errorf("tool task %q failed fatally", completion.TaskID)
	default:
		return toolResult, record, nil
	}
}

type canonicalCallAgentInput struct {
	Agent         string                   `json:"target_agent"`
	Task          string                   `json:"task"`
	IdleTimeoutMS int                      `json:"idle_timeout_ms,omitempty"`
	Mode          string                   `json:"mode,omitempty"`
	Tasks         []tools.AgentCallRequest `json:"tasks,omitempty"`
	Contract      tools.AgentCallContract  `json:"contract,omitempty"`
}

func parseCanonicalCallAgentInput(ctx context.Context, raw json.RawMessage) (canonicalCallAgentInput, error) {
	payload, err := tools.NormalizeCallAgentPayload(tools.SanitizeRawJSON(raw))
	if err != nil {
		return canonicalCallAgentInput{}, err
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return canonicalCallAgentInput{}, err
	}
	var in canonicalCallAgentInput
	if err := json.Unmarshal(blob, &in); err != nil {
		return canonicalCallAgentInput{}, err
	}

	defaultTask := strings.TrimSpace(tools.RuntimeContextFrom(ctx).CurrentRequest)
	if strings.EqualFold(strings.TrimSpace(in.Mode), "parallel") {
		for i := range in.Tasks {
			if strings.TrimSpace(in.Tasks[i].Task) == "" {
				in.Tasks[i].Task = defaultTask
			}
			if strings.TrimSpace(in.Tasks[i].Agent) == "" {
				return canonicalCallAgentInput{}, fmt.Errorf("target_agent is required")
			}
			if strings.TrimSpace(in.Tasks[i].Task) == "" {
				return canonicalCallAgentInput{}, fmt.Errorf("task is required")
			}
		}
		return in, nil
	}

	if strings.TrimSpace(in.Task) == "" {
		in.Task = defaultTask
	}
	if strings.TrimSpace(in.Agent) == "" {
		return canonicalCallAgentInput{}, fmt.Errorf("target_agent is required")
	}
	if strings.TrimSpace(in.Task) == "" {
		return canonicalCallAgentInput{}, fmt.Errorf("task is required")
	}
	return in, nil
}

func summarizeTaskToolBatch(
	outcomes []tools.ToolCallOutcome,
	total int,
	startedAt, completedAt time.Time,
	fatalErr error,
) tools.ToolBatchSummary {
	summary := tools.ToolBatchSummary{
		Calls:       append([]tools.ToolCallOutcome(nil), outcomes...),
		TotalCount:  total,
		Status:      "completed",
		StartedAt:   startedAt,
		CompletedAt: completedAt,
	}
	for _, outcome := range outcomes {
		if outcome.StartedAt.IsZero() {
			continue
		}
		summary.StartedCount++
		if !outcome.CompletedAt.IsZero() {
			summary.CompletedCount++
		}
		if outcome.Error != nil || strings.TrimSpace(outcome.Result.Error) != "" {
			summary.FailedCount++
		}
	}
	switch {
	case fatalErr != nil:
		summary.Status = "cancelled"
	case summary.FailedCount > 0:
		summary.Status = "completed_with_failures"
	}
	return summary
}

func synthesizeFailedToolTaskRecord(spec tools.ToolCallSpec, result tools.ToolResult, err error) task.Record {
	now := time.Now().UTC()
	message := result.Error
	if strings.TrimSpace(message) == "" && err != nil {
		message = err.Error()
	}
	return task.Record{
		ID:          spec.Call.ID,
		Kind:        task.KindTool,
		Status:      task.StatusFailed,
		Input:       string(tools.SanitizeRawJSON(spec.Call.Input)),
		ToolName:    spec.Call.Name,
		Summary:     strings.TrimSpace(result.Output),
		Error:       strings.TrimSpace(message),
		Metadata:    cloneToolMetadata(result.Metadata),
		CreatedAt:   now,
		StartedAt:   now,
		UpdatedAt:   now,
		CompletedAt: now,
	}
}

func workflowToolRecord(spec tools.ToolCallSpec, result tools.ToolResult, execErr error) task.Record {
	now := time.Now().UTC()
	status := task.StatusCompleted
	errText := strings.TrimSpace(result.Error)
	if execErr != nil {
		status = task.StatusFailed
		if errText == "" {
			errText = execErr.Error()
		}
	} else if errText != "" {
		status = task.StatusFailed
	}
	return task.Record{
		ID:             firstNonEmpty(spec.Call.ID, tools.NewOpaqueID("workflow_tool")),
		Kind:           task.KindTool,
		Status:         status,
		Input:          string(tools.SanitizeRawJSON(spec.Call.Input)),
		ToolName:       spec.Call.Name,
		Summary:        strings.TrimSpace(result.Output),
		Error:          errText,
		Metadata:       cloneToolMetadata(result.Metadata),
		CreatedAt:      now,
		StartedAt:      now,
		LastActivityAt: now,
		UpdatedAt:      now,
		CompletedAt:    now,
	}
}

func cloneToolMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func segmentPreparedToolCalls(prepared []tools.ToolCallSpec) [][]tools.ToolCallSpec {
	if len(prepared) == 0 {
		return nil
	}
	var segments [][]tools.ToolCallSpec
	for i := 0; i < len(prepared); {
		if !prepared[i].Metadata.AllowsParallelFanout() {
			segments = append(segments, []tools.ToolCallSpec{prepared[i]})
			i++
			continue
		}
		j := i + 1
		for j < len(prepared) && prepared[j].Metadata.AllowsParallelFanout() {
			j++
		}
		segments = append(segments, append([]tools.ToolCallSpec(nil), prepared[i:j]...))
		i = j
	}
	return segments
}

func segmentMaxParallel(segment []tools.ToolCallSpec) int {
	if len(segment) <= 1 {
		return 1
	}
	return len(segment)
}

func sessionIDFromRuntime(r *Runtime) string {
	if r == nil || r.Session == nil {
		return ""
	}
	return r.Session.ID
}

func (r *Runtime) taskSpecForCall(meta tools.RuntimeContext, call llm.ToolCall, turn int) task.Spec {
	idleTimeoutMS := r.resolveToolTaskTimeout(call)
	spec := task.Spec{
		Kind:          task.KindTool,
		AgentID:       r.AgentID,
		RunID:         meta.RunID,
		SessionID:     firstNonEmpty(meta.SessionID, sessionIDFromRuntime(r)),
		IdleTimeoutMS: idleTimeoutMS,
		Input:         string(tools.SanitizeRawJSON(call.Input)),
		ToolName:      call.Name,
		Metadata: map[string]any{
			"tool_call_id":             call.ID,
			"tool_name":                call.Name,
			"turn":                     turn + 1,
			task.MetadataVisibilityKey: task.VisibilityInternal,
		},
	}

	if processTool, ok := processBackedTool(r.Tools, call.Name); ok {
		spec.Kind = task.KindProcess
		spec.ProcessName = processTool.Name()
		spec.ToolName = ""
		spec.Contract.Workspace = processTool.WorkDir()
		if processTool.ExecPolicy() != nil {
			spec.Metadata["exec_policy_level"] = strings.TrimSpace(processTool.ExecPolicy().Level)
			if len(processTool.ExecPolicy().Allowlist) > 0 {
				spec.Metadata["exec_allowlist"] = append([]string(nil), processTool.ExecPolicy().Allowlist...)
			}
		}
	}

	return spec
}

func (r *Runtime) resolveToolTaskTimeout(call llm.ToolCall) int {
	meta := toolMetadataForCall(r.Tools, call.Name)
	timeout := meta.TimeoutHint()
	if tool, ok := r.Tools.Get(call.Name); ok {
		if provider, ok := tool.(tools.InputTimeoutHintProvider); ok {
			if resolved, ok := provider.TimeoutHintForInput(call.Input, timeout); ok {
				timeout = resolved
			}
		}
	}
	if timeout <= 0 {
		return 0
	}
	return int(timeout / time.Millisecond)
}

type processBackedToolInfo interface {
	Name() string
	WorkDir() string
	ExecPolicy() *tools.ExecPolicy
}

func processBackedTool(executor tools.Executor, name string) (processBackedToolInfo, bool) {
	if executor == nil {
		return nil, false
	}
	tool, ok := executor.Get(strings.TrimSpace(name))
	if !ok {
		return nil, false
	}
	switch typed := tool.(type) {
	case *tools.BashTool:
		return bashProcessToolInfo{tool: typed}, true
	case *tools.PythonTool:
		return pythonProcessToolInfo{tool: typed}, true
	default:
		return nil, false
	}
}

func lookupCallAgentTool(executor tools.Executor, name string) (*tools.CallAgentTool, bool) {
	if executor == nil || !strings.EqualFold(strings.TrimSpace(name), "callagent") {
		return nil, false
	}
	tool, ok := executor.Get(strings.TrimSpace(name))
	if !ok {
		return nil, false
	}
	callAgentTool, ok := tool.(*tools.CallAgentTool)
	return callAgentTool, ok
}

type bashProcessToolInfo struct {
	tool *tools.BashTool
}

func (i bashProcessToolInfo) Name() string {
	return "bash"
}

func (i bashProcessToolInfo) WorkDir() string {
	if i.tool == nil {
		return ""
	}
	return i.tool.WorkDir
}

func (i bashProcessToolInfo) ExecPolicy() *tools.ExecPolicy {
	if i.tool == nil {
		return nil
	}
	return i.tool.ExecPolicy
}

type pythonProcessToolInfo struct {
	tool *tools.PythonTool
}

func (i pythonProcessToolInfo) Name() string {
	return "python"
}

func (i pythonProcessToolInfo) WorkDir() string {
	if i.tool == nil {
		return ""
	}
	return i.tool.WorkDir
}

func (i pythonProcessToolInfo) ExecPolicy() *tools.ExecPolicy {
	if i.tool == nil {
		return nil
	}
	return i.tool.ExecPolicy
}
