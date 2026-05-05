package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"strings"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorylifecycle "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

func (r *Runtime) AvailableAgents() []tools.AgentInfo {
	cfg := r.Config()
	if cfg == nil {
		return nil
	}
	agents := cfg.Agents.List
	infos := make([]tools.AgentInfo, len(agents))
	for i, agentCfg := range agents {
		infos[i] = tools.AgentInfo{
			ID:   agentCfg.ID,
			Name: agentCfg.Name,
		}
	}
	return infos
}

func (r *Runtime) DoAgent(ctx context.Context, req tools.AgentCallRequest, callback tools.AgentResultCallback) (string, error) {
	deps := r.ExecutionDeps()
	launch, failed := r.prepareAgentCall(ctx, req)
	if failed != nil {
		r.emitAgentCallResult(ctx, callback, *failed)
		return "", nil
	}

	runtimelogging.Info("calling agent", "agent", req.Agent, "prompt_len", len(req.Task), "run_id", launch.runID)
	taskID, err := r.submitAgentTask(ctx, launch, req, func(callbackCtx context.Context, completion task.Completion) {
		result := agentCallResultFromCompletion(req, launch, completion)
		if deps.Pipeline != nil {
			deps.Pipeline.CaptureAgentCallComplete(launch.meta, req, result)
		}
		r.emitAgentCallResult(callbackCtx, callback, result)
	})
	if err != nil {
		failedResult := tools.AgentCallResult{
			Agent:     req.Agent,
			Status:    "failed",
			RunID:     launch.runID,
			SessionID: launch.sessionID,
			Error:     fmt.Sprintf("agent %q task submission failed: %v", req.Agent, err),
			Contract:  req.Contract,
		}
		r.emitAgentCallResult(ctx, callback, failedResult)
		return "", nil
	}

	return taskID, nil
}

func (r *Runtime) DoAgentParallel(ctx context.Context, tasks []tools.AgentCallRequest, maxParallel int, callback tools.ParallelAgentResultCallback) (string, error) {
	deps := r.ExecutionDeps()
	if deps.TaskRuntime == nil {
		return "", fmt.Errorf("task runtime not available")
	}
	limit := r.resolveAgentParallelLimit(maxParallel)

	batchID := tools.NewOpaqueID("agentcall_batch")
	if len(tasks) == 0 {
		r.emitParallelAgentCallResult(ctx, callback, tools.ParallelAgentCallResult{
			Mode:        "parallel",
			BatchID:     batchID,
			MaxParallel: limit,
			Results:     nil,
		})
		return batchID, nil
	}

	go func() {
		joined, err := deps.TaskRuntime.JoinAllSettled(ctx, task.JoinSpec{
			Count:       len(tasks),
			MaxParallel: limit,
		}, func(joinCtx context.Context, idx int, settle func(task.Record)) {
			if _, submitErr := r.DoAgent(joinCtx, tasks[idx], func(_ context.Context, result tools.AgentCallResult) {
				settle(r.agentCallRecord(tasks[idx], result))
			}); submitErr != nil {
				settle(failedAgentCallRecord(tasks[idx], submitErr.Error()))
			}
		})
		if err != nil {
			results := make([]tools.AgentCallResult, len(tasks))
			for i := range tasks {
				results[i] = tools.AgentCallResult{
					Agent:    tasks[i].Agent,
					Status:   "failed",
					Error:    err.Error(),
					Contract: tasks[i].Contract,
				}
			}
			r.emitParallelAgentCallResult(ctx, callback, tools.ParallelAgentCallResult{
				Mode:        "parallel",
				BatchID:     batchID,
				MaxParallel: limit,
				BatchCount:  (len(tasks) + limit - 1) / limit,
				Results:     results,
			})
			return
		}

		results := make([]tools.AgentCallResult, len(tasks))
		for i, item := range joined.Results {
			result := agentCallResultFromTaskRecord(item.Record.ID, tasks[i].Agent, tasks[i].Contract, item.Record)
			result.QueueIndex = i + 1
			result.Batch = (i / limit) + 1
			result.StartedOrder = item.StartedOrder
			result.FinishOrder = item.FinishOrder
			results[i] = result
		}

		r.emitParallelAgentCallResult(ctx, callback, tools.ParallelAgentCallResult{
			Mode:        "parallel",
			BatchID:     batchID,
			MaxParallel: limit,
			BatchCount:  (len(tasks) + limit - 1) / limit,
			Results:     results,
		})
	}()

	return batchID, nil
}

func (r *Runtime) submitAgentTask(ctx context.Context, launch agentCallLaunch, req tools.AgentCallRequest, callback task.Callback) (string, error) {
	return r.DoTask(ctx, task.Spec{
		Kind:          task.KindAgent,
		AgentID:       req.Agent,
		RunID:         firstNonEmptyAgentCall(launch.meta.RunID, launch.runID),
		SessionID:     launch.meta.SessionID,
		IdleTimeoutMS: launch.idleTimeoutMS,
		Input:         marshalAgentTaskInput(req),
		TargetAgent:   req.Agent,
		Contract:      task.Contract(req.Contract.Normalized()),
		Metadata: map[string]any{
			"child_session_id":         launch.sessionID,
			"caller_agent":             launch.meta.AgentID,
			task.MetadataVisibilityKey: task.VisibilityInternal,
		},
		OnComplete: callback,
	})
}

func (r *Runtime) emitAgentCallResult(ctx context.Context, callback tools.AgentResultCallback, result tools.AgentCallResult) {
	if callback == nil {
		return
	}
	defer func() {
		if recover() != nil {
			return
		}
	}()
	callback(ctx, result)
}

func (r *Runtime) emitParallelAgentCallResult(ctx context.Context, callback tools.ParallelAgentResultCallback, result tools.ParallelAgentCallResult) {
	if callback == nil {
		return
	}
	defer func() {
		if recover() != nil {
			return
		}
	}()
	callback(ctx, result)
}

func agentCallResultFromCompletion(req tools.AgentCallRequest, launch agentCallLaunch, completion task.Completion) tools.AgentCallResult {
	if strings.TrimSpace(completion.Record.ID) != "" {
		result := agentCallResultFromTaskRecord(completion.Record.ID, req.Agent, req.Contract, completion.Record)
		if strings.TrimSpace(result.SessionID) == "" {
			result.SessionID = launch.sessionID
		}
		if strings.TrimSpace(result.RunID) == "" {
			result.RunID = launch.runID
		}
		return result
	}
	return tools.AgentCallResult{
		Agent:     req.Agent,
		TaskID:    completion.TaskID,
		Status:    "failed",
		RunID:     launch.runID,
		SessionID: launch.sessionID,
		Error:     "agent task completed without final record",
		Contract:  req.Contract,
	}
}

type agentCallLaunch struct {
	meta          tools.RuntimeContext
	runID         string
	sessionID     string
	idleTimeoutMS int
}

func (r *Runtime) prepareAgentCall(ctx context.Context, req tools.AgentCallRequest) (agentCallLaunch, *tools.AgentCallResult) {
	cfg := r.Config()
	deps := r.ExecutionDeps()
	if cfg == nil {
		result := tools.AgentCallResult{Agent: req.Agent, Status: "failed", Error: "runtime config not available"}
		return agentCallLaunch{}, &result
	}
	if strings.TrimSpace(req.Agent) == "" {
		result := tools.AgentCallResult{Status: "failed", Error: "target_agent is required"}
		return agentCallLaunch{}, &result
	}
	if strings.TrimSpace(req.Task) == "" {
		result := tools.AgentCallResult{Agent: req.Agent, Status: "failed", Error: "task is required"}
		return agentCallLaunch{}, &result
	}

	agentCfg, ok := cfg.GetAgent(req.Agent)
	if !ok {
		result := tools.AgentCallResult{
			Agent:  req.Agent,
			Status: "failed",
			Error:  fmt.Sprintf("agent %q not found", req.Agent),
		}
		return agentCallLaunch{}, &result
	}

	meta := tools.RuntimeContextFrom(ctx)
	callChain := append([]string(nil), meta.CallChain...)
	if meta.AgentID != "" && (len(callChain) == 0 || callChain[len(callChain)-1] != meta.AgentID) {
		callChain = append(callChain, meta.AgentID)
	}
	if meta.AgentID != "" && req.Agent == meta.AgentID {
		result := tools.AgentCallResult{
			Agent:  req.Agent,
			Status: "failed",
			Error:  fmt.Sprintf("agent call cycle detected: agent %q cannot call itself", req.Agent),
		}
		return agentCallLaunch{}, &result
	}
	for _, seen := range callChain {
		if seen == req.Agent {
			result := tools.AgentCallResult{
				Agent:  req.Agent,
				Status: "failed",
				Error:  fmt.Sprintf("agent call cycle detected for agent %q", req.Agent),
			}
			return agentCallLaunch{}, &result
		}
	}

	if limit := cfg.Runtime.AgentCall.DepthLimit; limit > 0 && meta.Depth >= limit {
		result := tools.AgentCallResult{
			Agent:  req.Agent,
			Status: "failed",
			Error:  fmt.Sprintf("agent call depth limit exceeded (limit=%d)", limit),
		}
		return agentCallLaunch{}, &result
	}

	providerName, _ := llm.ParseProviderModel(agentCfg.Model)
	if _, available := deps.Providers[providerName]; !available {
		result := tools.AgentCallResult{
			Agent:  req.Agent,
			Status: "failed",
			Error:  fmt.Sprintf("provider %q not available for agent %q", providerName, req.Agent),
		}
		return agentCallLaunch{}, &result
	}

	runID := meta.RunID
	if runID == "" {
		runID = tools.NewRunID()
	}
	idleTimeoutMS := req.IdleTimeoutMS
	if idleTimeoutMS <= 0 {
		idleTimeoutMS = cfg.Runtime.IdleTimeoutMS
	}
	sessionID := firstNonEmptyAgentCall(
		fmt.Sprintf("agentcall_%s_%s", req.Agent, runID),
	)

	return agentCallLaunch{
		meta:          meta,
		runID:         runID,
		sessionID:     sessionID,
		idleTimeoutMS: idleTimeoutMS,
	}, nil
}

func (r *Runtime) resolveAgentParallelLimit(maxParallel int) int {
	limit := maxParallel
	if limit <= 0 {
		if cfg := r.Config(); cfg != nil {
			limit = cfg.Runtime.AgentCall.MaxParallel
		}
	}
	if limit <= 0 {
		limit = 4
	}
	return limit
}

func marshalAgentTaskInput(req tools.AgentCallRequest) string {
	payload, err := json.Marshal(struct {
		Agent         string `json:"target_agent,omitempty"`
		Task          string `json:"task,omitempty"`
		IdleTimeoutMS int    `json:"idle_timeout_ms,omitempty"`
	}{
		Agent:         strings.TrimSpace(req.Agent),
		Task:          strings.TrimSpace(req.Task),
		IdleTimeoutMS: req.IdleTimeoutMS,
	})
	if err != nil {
		return strings.TrimSpace(req.Task)
	}
	return string(payload)
}

func agentCallResultFromTaskRecord(taskID, agentID string, contract tools.AgentCallContract, record task.Record) tools.AgentCallResult {
	status := string(record.Status)
	if status == "" {
		status = "failed"
	}
	return tools.AgentCallResult{
		Agent:     agentID,
		TaskID:    taskID,
		Status:    status,
		Summary:   record.Summary,
		RunID:     record.RunID,
		SessionID: record.SessionID,
		Error:     record.Error,
		Contract:  contract,
	}
}

func (r *Runtime) agentCallRecord(req tools.AgentCallRequest, result tools.AgentCallResult) task.Record {
	if taskID := strings.TrimSpace(result.TaskID); taskID != "" {
		if record, ok := r.GetTask(taskID); ok {
			return task.Record(record)
		}
	}
	return syntheticAgentCallRecord(req, result.TaskID, result.Status, result.Summary, result.Error, result.RunID, result.SessionID)
}

func failedAgentCallRecord(req tools.AgentCallRequest, errMsg string) task.Record {
	return syntheticAgentCallRecord(req, "", "failed", "", errMsg, "", "")
}

func syntheticAgentCallRecord(
	req tools.AgentCallRequest,
	taskID string,
	status string,
	summary string,
	errMsg string,
	runID string,
	sessionID string,
) task.Record {
	now := time.Now().UTC()
	return task.Record{
		ID:          strings.TrimSpace(taskID),
		Kind:        task.KindAgent,
		Status:      taskStatusFromAgentCallStatus(status, errMsg),
		Input:       strings.TrimSpace(req.Task),
		TargetAgent: strings.TrimSpace(req.Agent),
		Summary:     strings.TrimSpace(summary),
		Error:       strings.TrimSpace(errMsg),
		RunID:       strings.TrimSpace(runID),
		SessionID:   strings.TrimSpace(sessionID),
		Contract:    task.Contract(req.Contract.Normalized()),
		CreatedAt:   now,
		StartedAt:   now,
		UpdatedAt:   now,
		CompletedAt: now,
	}
}

func taskStatusFromAgentCallStatus(status, errMsg string) task.Status {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case string(task.StatusCompleted):
		return task.StatusCompleted
	case string(task.StatusCancelled):
		return task.StatusCancelled
	case string(task.StatusFailed):
		return task.StatusFailed
	default:
		if strings.TrimSpace(errMsg) != "" {
			return task.StatusFailed
		}
		return task.StatusCompleted
	}
}

func firstNonEmptyAgentCall(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func compactAgentCallText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "...(truncated)"
}

var _ tools.AgentCallRunner = (*Runtime)(nil)
var _ interface {
	SetSender(tools.MessageSender)
	SetSkills(*skill.Loader)
	SetResources(*runtimeresources.Catalog)
	SetMemory(*memory.Manager)
	SetRecorder(*runtimeevents.Recorder)
	SetMemoryPipeline(*runtimememorylifecycle.Pipeline)
	SetTaskStore(*task.Store)
	SetTaskRuntime(*task.Runtime)
} = (*Runtime)(nil)
