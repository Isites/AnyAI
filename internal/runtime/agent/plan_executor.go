package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/contract"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

const (
	maxAutoPlanExecutionsPerIdleCheck = 8
	maxRuntimePlanMetaTextLen         = 4000
)

type runtimePlanStore struct {
	session *session.Session
}

func (s runtimePlanStore) UpdateStructuredPlan(plan runtimeplan.Plan) error {
	if s.session == nil {
		return nil
	}
	s.session.Append(session.StructuredPlanEntry(runtimeplan.Normalize(plan)))
	return nil
}

func (s runtimePlanStore) GetStructuredPlan() (runtimeplan.Plan, bool) {
	return session.LatestStructuredPlan(s.session)
}

type runtimePlanDispatcher struct {
	controller *runController
	turn       int
}

func (d runtimePlanDispatcher) ExecuteTool(ctx context.Context, name, input string) (string, error) {
	if d.controller == nil || d.controller.rt == nil {
		return "", fmt.Errorf("agent runtime is not available")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("structured plan tool target is required")
	}

	raw := json.RawMessage(`{}`)
	if trimmed := strings.TrimSpace(input); trimmed != "" {
		raw = tools.SanitizeRawJSON(json.RawMessage(trimmed))
	}
	call := llm.ToolCall{
		ID:    contract.NewOpaqueID("planstep_tool"),
		Name:  name,
		Input: raw,
	}
	meta := toolMetadataForCall(d.controller.rt.Tools, call.Name)
	d.controller.hadToolActivity = true
	if d.controller.rt.Session != nil {
		d.controller.rt.Session.Append(session.ToolCallEntry(call.ID, call.Name, raw))
	}
	d.controller.emit(AgentEvent{
		Type:         EventToolCallStart,
		ToolCall:     &call,
		ToolMetadata: &meta,
	})

	result, err := d.controller.rt.executeToolWithRecovery(ctx, call, d.controller.emit, d.controller.loopState, d.turn)
	if err != nil {
		result = fatalToolExecutionResult(call, err)
	}
	appendRuntimePlanToolResult(d.controller.rt.Session, call.ID, result)
	d.controller.emit(AgentEvent{
		Type:         EventToolResult,
		ToolCall:     &call,
		ToolMetadata: &meta,
		Result:       &result,
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Error) != "" {
		return "", fmt.Errorf("%s", strings.TrimSpace(result.Error))
	}
	return strings.TrimSpace(result.Output), nil
}

func (d runtimePlanDispatcher) ExecuteAgent(ctx context.Context, target, input string) (string, error) {
	if d.controller == nil || d.controller.rt == nil {
		return "", fmt.Errorf("agent runtime is not available")
	}
	callAgentTool, ok := lookupCallAgentTool(d.controller.rt.Tools, "callagent")
	if !ok || callAgentTool == nil || callAgentTool.Runner == nil {
		return "", fmt.Errorf("callagent runner is not available")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("structured plan agent target is required")
	}
	taskText := strings.TrimSpace(input)
	if taskText == "" {
		return "", fmt.Errorf("structured plan agent task is required")
	}

	resultCh := make(chan tools.AgentCallResult, 1)
	_, err := callAgentTool.Runner.DoAgent(withParentActivityBridge(ctx, d.controller.emit), tools.AgentCallRequest{
		Agent: target,
		Task:  taskText,
	}, func(_ context.Context, result tools.AgentCallResult) {
		resultCh <- result
	})
	if err != nil {
		return "", err
	}

	select {
	case result := <-resultCh:
		payload, _ := json.Marshal(result)
		if !strings.EqualFold(strings.TrimSpace(result.Status), "completed") {
			if strings.TrimSpace(result.Error) != "" {
				return "", fmt.Errorf("%s", strings.TrimSpace(result.Error))
			}
			return "", fmt.Errorf("agent step %q finished with status %s", target, strings.TrimSpace(result.Status))
		}
		return string(payload), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (d runtimePlanDispatcher) AwaitUserInput(_ context.Context, message string) error {
	if d.controller == nil || d.controller.rt == nil || d.controller.rt.GoalRuntime == nil || d.controller.goalID == "" {
		return fmt.Errorf("goal runtime is not available for structured user-input step")
	}
	return d.controller.rt.GoalRuntime.AwaitUserInput(d.controller.goalID, strings.TrimSpace(message))
}

func (d runtimePlanDispatcher) CompleteCheckpoint(_ context.Context, checkpointID, evidence string) error {
	if d.controller == nil || d.controller.rt == nil || d.controller.rt.GoalRuntime == nil || d.controller.goalID == "" {
		return fmt.Errorf("goal runtime is not available for structured checkpoint step")
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return fmt.Errorf("checkpoint id is required")
	}
	goal, ok := d.controller.rt.GoalRuntime.GetGoal(d.controller.goalID)
	if !ok || goal == nil {
		return fmt.Errorf("goal %q not found", d.controller.goalID)
	}
	if !goalHasCheckpoint(goal, checkpointID) {
		checkpoints := append([]task.Checkpoint(nil), goal.Checkpoints...)
		checkpoints = append(checkpoints, task.Checkpoint{
			ID:          checkpointID,
			Description: checkpointID,
		})
		if err := d.controller.rt.GoalRuntime.SetCheckpoints(d.controller.goalID, checkpoints); err != nil {
			return err
		}
	}
	return d.controller.rt.GoalRuntime.MarkCheckpointComplete(d.controller.goalID, checkpointID, strings.TrimSpace(evidence))
}

func goalHasCheckpoint(goal *task.Goal, checkpointID string) bool {
	if goal == nil {
		return false
	}
	for _, checkpoint := range goal.Checkpoints {
		if strings.TrimSpace(checkpoint.ID) == checkpointID {
			return true
		}
	}
	return false
}

func appendRuntimePlanToolResult(sess *session.Session, toolCallID string, result tools.ToolResult) {
	if sess == nil {
		return
	}
	entry := session.ToolResultEntryWithMetadata(
		toolCallID,
		result.Output,
		result.Error,
		result.Metadata,
		session.ImagePayloads(result.Images),
	)
	if len(result.Images) > 0 {
		sess.AppendWithPersistedEntry(entry, session.ToolResultEntryWithMetadata(
			toolCallID,
			result.Output,
			result.Error,
			result.Metadata,
			session.ImageRefs(result.Images),
		))
	} else {
		sess.Append(entry)
	}
}

func planActionAutoExecutable(step *runtimeplan.Step) bool {
	if step == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(step.Action.Type)) {
	case "tool", "tool_call", "agent", "agent_call", "callagent", "user_input", "await_user_input", "checkpoint":
		return true
	default:
		return false
	}
}

func (c *runController) maybeAutoExecuteReadyPlanStep(result task.ShouldContinueResult) (*runtimeplan.ExecuteResult, bool, error) {
	if c == nil || c.rt == nil || c.rt.Session == nil || result.ReadyPlanStep == nil {
		return nil, false, nil
	}
	if !planActionAutoExecutable(result.ReadyPlanStep) {
		return nil, false, nil
	}

	execMeta := tools.RuntimeContextFrom(c.ctx)
	execCtx := tools.WithRuntimeContext(c.ctx, execMeta)
	executor := runtimeplan.NewExecutor(runtimePlanStore{session: c.rt.Session}, runtimePlanDispatcher{
		controller: c,
		turn:       c.currentTurn,
	})
	execResult, err := executor.Execute(execCtx, "")
	if err != nil || execResult == nil {
		return execResult, err != nil, err
	}
	c.recordRuntimePlanExecution(*execResult)
	return execResult, true, nil
}

func (c *runController) recordRuntimePlanExecution(result runtimeplan.ExecuteResult) {
	if c == nil || c.rt == nil || c.rt.Session == nil {
		return
	}
	if strings.EqualFold(string(result.Status), string(runtimeplan.ExecuteStatusPaused)) {
		message := strings.TrimSpace(result.Output)
		if message == "" && result.Step != nil {
			message = strings.TrimSpace(result.Step.Description)
		}
		if message == "" {
			message = "I need more information from you before I can continue."
		}
		c.rt.Session.Append(session.AssistantMessageEntry(message))
		c.emit(AgentEvent{Type: EventTextDelta, Text: message})
		return
	}

	text := buildRuntimePlanExecutionMeta(result)
	if text == "" {
		return
	}
	c.rt.Session.Append(session.MetaEntry(text))
}

func buildRuntimePlanExecutionMeta(result runtimeplan.ExecuteResult) string {
	label := "unknown step"
	if result.Step != nil {
		label = firstNonEmpty(
			strings.TrimSpace(result.Step.Description),
			strings.TrimSpace(result.Step.ID),
		)
	}

	lines := []string{
		"[Runtime plan execution]",
		"Step: " + label,
		"Status: " + strings.TrimSpace(string(result.Status)),
	}
	if result.Step != nil {
		actionType := strings.TrimSpace(result.Step.Action.Type)
		actionTarget := strings.TrimSpace(result.Step.Action.Target)
		if actionType != "" || actionTarget != "" {
			lines = append(lines, "Action: "+strings.TrimSpace(actionType+" "+actionTarget))
		}
	}
	if output := truncateRuntimePlanText(firstNonEmpty(strings.TrimSpace(result.Output), strings.TrimSpace(result.Error))); output != "" {
		if result.Error != "" {
			lines = append(lines, "Detail: "+output)
		} else {
			lines = append(lines, "Output: "+output)
		}
	}
	return strings.Join(lines, "\n")
}

func truncateRuntimePlanText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxRuntimePlanMetaTextLen {
		return text
	}
	return strings.TrimSpace(text[:maxRuntimePlanMetaTextLen]) + "...(truncated)"
}
