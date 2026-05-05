package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/runtime/contract"
)

// AgentCallContract is an alias for contract.Contract.
// Use contract.Contract for the canonical definition.
type AgentCallContract = contract.Contract

// AgentCallRequest is the structured input for one child-agent call.
type AgentCallRequest struct {
	Agent         string            `json:"target_agent"`
	Task          string            `json:"task"`
	IdleTimeoutMS int               `json:"idle_timeout_ms,omitempty"`
	Contract      AgentCallContract `json:"contract,omitempty"`
}

// AgentInfo is a summary of an available agent.
type AgentInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AgentCallResult is the structured output for one child-agent call.
type AgentCallResult struct {
	Agent        string            `json:"target_agent"`
	TaskID       string            `json:"task_id,omitempty"`
	Status       string            `json:"status"`
	Summary      string            `json:"summary,omitempty"`
	RunID        string            `json:"run_id,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	Error        string            `json:"error,omitempty"`
	QueueIndex   int               `json:"queue_index,omitempty"`
	Batch        int               `json:"batch,omitempty"`
	StartedOrder int               `json:"started_order,omitempty"`
	FinishOrder  int               `json:"finish_order,omitempty"`
	Contract     AgentCallContract `json:"contract,omitempty"`
}

type AgentResultCallback func(context.Context, AgentCallResult)

type ParallelAgentResultCallback func(context.Context, ParallelAgentCallResult)

// AgentCallRunner executes structured child-agent calls.
type AgentCallRunner interface {
	DoAgent(ctx context.Context, req AgentCallRequest, callback AgentResultCallback) (string, error)
	DoAgentParallel(ctx context.Context, tasks []AgentCallRequest, maxParallel int, callback ParallelAgentResultCallback) (string, error)
	AvailableAgents() []AgentInfo
}

// ParallelAgentCallResult is the structured output for a bounded parallel
// child-agent batch.
type ParallelAgentCallResult struct {
	Mode        string            `json:"mode"`
	BatchID     string            `json:"batch_id,omitempty"`
	MaxParallel int               `json:"max_parallel,omitempty"`
	BatchCount  int               `json:"batch_count,omitempty"`
	Results     []AgentCallResult `json:"results"`
}

// CallAgentTool is the AnyAI-standard inter-agent collaboration tool.
type CallAgentTool struct {
	Runner               AgentCallRunner
	MaxParallel          int
	DefaultIdleTimeoutMS int
}

type callAgentInput struct {
	Agent         string             `json:"target_agent"`
	Task          string             `json:"task"`
	IdleTimeoutMS int                `json:"idle_timeout_ms,omitempty"`
	Mode          string             `json:"mode,omitempty"`
	Tasks         []AgentCallRequest `json:"tasks,omitempty"`
	Contract      AgentCallContract  `json:"contract,omitempty"`
}

func (t *CallAgentTool) Name() string { return "callagent" }

func (t *CallAgentTool) ToolMetadata() ToolMetadata {
	meta := workflowToolMetadata(t.Name(), callAgentToolTimeoutMS)
	if t != nil && t.DefaultIdleTimeoutMS > 0 {
		meta.TimeoutHintMS = int64(t.DefaultIdleTimeoutMS)
	}
	return meta
}

func (t *CallAgentTool) TimeoutHintForInput(input json.RawMessage, fallback time.Duration) (time.Duration, bool) {
	idleTimeoutMS, ok := effectiveCallAgentIdleTimeoutMS(input)
	if !ok {
		return 0, false
	}
	if t != nil && t.DefaultIdleTimeoutMS > idleTimeoutMS {
		idleTimeoutMS = t.DefaultIdleTimeoutMS
	}
	if idleTimeoutMS <= 0 && fallback > 0 {
		idleTimeoutMS = int(fallback / time.Millisecond)
	}
	if idleTimeoutMS <= 0 {
		return 0, true
	}
	return time.Duration(idleTimeoutMS)*time.Millisecond + 5*time.Second, true
}

func (t *CallAgentTool) Description() string {
	toolName := t.Name()
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`Call another agent and get back a structured result. Use %s when the request needs a specialist or several specialist viewpoints. Every call must include target_agent. Calls without target_agent fail. For parallel work, every tasks[] item must include target_agent and task.

Preferred call shapes:
- single: {"target_agent":"agent-id","task":"what the child should do"}  (single calls must omit mode entirely; never send mode="sequential")
- parallel: {"mode":"parallel","tasks":[{"target_agent":"agent-a","task":"..."},{"target_agent":"agent-b","task":"..."}]}

Keep the child task concrete. If collaboration is required, your first action should be the %s call, not a direct prose answer. If a plan is naturally parallel, send one parallel %s call instead of several separate single calls. After child results return, continue and write the final integrated user-facing answer yourself.`, toolName, toolName, toolName))

	if t.Runner != nil {
		agents := t.Runner.AvailableAgents()
		if len(agents) > 0 {
			b.WriteString("\n\nAvailable agents:")
			for _, a := range agents {
				if a.Name != "" {
					fmt.Fprintf(&b, "\n- %s (%s)", a.ID, a.Name)
				} else {
					fmt.Fprintf(&b, "\n- %s", a.ID)
				}
			}
		}
	}

	return b.String()
}

func (t *CallAgentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"target_agent": {
				"type": "string",
				"description": "Canonical target agent ID for a single child-agent call. Required for single calls."
			},
			"task": {
				"type": "string",
				"description": "Concrete task for a single child-agent call. Required for single calls."
			},
			"contract": {
				"type": "object",
				"properties": {
					"task_goal": {"type": "string"},
					"workspace": {"type": "string"},
					"expected_outputs": {"type": "array", "items": {"type": "string"}},
					"input_artifacts": {"type": "array", "items": {"type": "string"}},
					"return_mode": {"type": "string"}
				}
			},
			"idle_timeout_ms": {
				"type": "integer",
				"description": "Optional per-task idle timeout in milliseconds. Activity events extend it."
			},
			"mode": {
				"type": "string",
				"enum": ["parallel"],
				"description": "Use parallel only with tasks[]."
			},
			"tasks": {
				"type": "array",
				"minItems": 2,
				"description": "Canonical parallel fan-out list. Every item must include target_agent and task.",
				"items": {
					"type": "object",
					"properties": {
						"target_agent": {"type": "string"},
						"task": {"type": "string"},
						"idle_timeout_ms": {"type": "integer"},
						"contract": {
							"type": "object",
							"properties": {
								"task_goal": {"type": "string"},
								"workspace": {"type": "string"},
								"expected_outputs": {"type": "array", "items": {"type": "string"}},
								"input_artifacts": {"type": "array", "items": {"type": "string"}},
								"return_mode": {"type": "string"}
							}
						}
					},
					"required": ["target_agent", "task"]
				}
			}
		}
	}`)
}

func (t *CallAgentTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	in, err := parseCallAgentInput(input)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	if err := validateCallAgentInput(in); err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return ToolResult{Error: "callagent must be scheduled by the runtime workflow dispatcher"}, nil
}

// NormalizeCallAgentPayload canonicalizes permissive callagent tool inputs
// into a stable payload shape suitable for observability.
func NormalizeCallAgentPayload(raw json.RawMessage) (map[string]any, error) {
	in, err := parseCallAgentInput(raw)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if in.IdleTimeoutMS > 0 {
		payload["idle_timeout_ms"] = in.IdleTimeoutMS
	}
	if contract := in.Contract.ToMap(); contract != nil {
		payload["contract"] = contract
	}
	if in.Mode == "parallel" {
		payload["mode"] = "parallel"
		tasks := make([]map[string]any, 0, len(in.Tasks))
		for _, task := range in.Tasks {
			taskPayload := map[string]any{
				"target_agent": task.Agent,
				"task":         task.Task,
			}
			if task.IdleTimeoutMS > 0 {
				taskPayload["idle_timeout_ms"] = task.IdleTimeoutMS
			}
			if contract := task.Contract.ToMap(); contract != nil {
				taskPayload["contract"] = contract
			}
			tasks = append(tasks, taskPayload)
		}
		payload["tasks"] = tasks
		return payload, nil
	}
	payload["target_agent"] = in.Agent
	payload["task"] = in.Task
	return payload, nil
}

func parseCallAgentInput(raw json.RawMessage) (callAgentInput, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return callAgentInput{}, fmt.Errorf("invalid input: %v", err)
	}
	return callAgentInputFromMap(payload)
}

func callAgentInputFromMap(payload map[string]any) (callAgentInput, error) {
	var in callAgentInput
	in.IdleTimeoutMS = intFromAny(payload["idle_timeout_ms"])
	in.Contract = parseCallAgentContractFromMap(payload)
	if nestedContract, ok := payload["contract"].(map[string]any); ok {
		in.Contract = in.Contract.MergeMissing(parseCallAgentContractFromMap(nestedContract))
	}

	explicitMode := strings.TrimSpace(stringFromAny(payload["mode"]))
	switch explicitMode {
	case "", "parallel":
	default:
		return callAgentInput{}, fmt.Errorf("unknown mode %q", explicitMode)
	}
	tasks := parseCallAgentRequests(firstNonEmptySlice(
		sliceFromAny(payload["tasks"]),
		sliceFromAny(payload["delegations"]),
		sliceFromAny(payload["calls"]),
		sliceFromAny(payload["children"]),
		sliceFromAny(payload["agents"]),
	))
	agents := stringSliceFromAny(payload["agents"])
	if len(agents) == 0 {
		agents = stringSliceFromAny(payload["agent"])
	}
	if len(agents) == 0 {
		agents = stringSliceFromAny(payload["target_agent"])
	}
	taskStrings := stringSliceFromAny(payload["tasks"])
	if len(taskStrings) == 0 {
		taskStrings = stringSliceFromAny(payload["task"])
	}
	if len(taskStrings) == 0 {
		taskStrings = stringSliceFromAny(payload["prompt"])
	}
	commonTask := taskTextFromMap(payload)
	if len(tasks) == 0 && len(agents) > 0 {
		switch {
		case len(taskStrings) == len(agents) && len(taskStrings) > 0:
			tasks = zipCallAgentRequests(agents, taskStrings, in.IdleTimeoutMS, in.Contract)
		case commonTask != "":
			tasks = zipCallAgentRequests(agents, repeatString(commonTask, len(agents)), in.IdleTimeoutMS, in.Contract)
		}
	}

	if len(tasks) > 0 {
		for i := range tasks {
			tasks[i].Contract = tasks[i].Contract.MergeMissing(in.Contract)
		}
		switch {
		case explicitMode == "parallel" || len(tasks) > 1:
			in.Mode = "parallel"
			in.Tasks = tasks
		default:
			in.Agent = tasks[0].Agent
			in.Task = tasks[0].Task
			if in.IdleTimeoutMS <= 0 {
				in.IdleTimeoutMS = tasks[0].IdleTimeoutMS
			}
			in.Contract = tasks[0].Contract.Normalized()
		}
	}

	if in.Agent == "" {
		in.Agent = agentIDFromMap(payload)
	}
	if in.Task == "" {
		in.Task = taskTextFromMap(payload)
	}
	in.Contract = in.Contract.Normalized()

	switch explicitMode {
	case "parallel":
		if len(in.Tasks) == 0 {
			return callAgentInput{}, fmt.Errorf("tasks is required when mode=parallel")
		}
	}

	return in, nil
}

func parseCallAgentRequests(items []any) []AgentCallRequest {
	var tasks []AgentCallRequest
	for _, item := range items {
		taskMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		agentID := agentIDFromMap(taskMap)
		task := taskTextFromMap(taskMap)
		if agentID == "" {
			continue
		}
		contract := parseCallAgentContractFromMap(taskMap)
		if nestedContract, ok := taskMap["contract"].(map[string]any); ok {
			contract = contract.MergeMissing(parseCallAgentContractFromMap(nestedContract))
		}
		tasks = append(tasks, AgentCallRequest{
			Agent:         agentID,
			Task:          task,
			IdleTimeoutMS: intFromAny(taskMap["idle_timeout_ms"]),
			Contract:      contract.Normalized(),
		})
	}
	return tasks
}

func zipCallAgentRequests(agents, tasks []string, idleTimeoutMS int, contract AgentCallContract) []AgentCallRequest {
	limit := len(agents)
	if len(tasks) < limit {
		limit = len(tasks)
	}
	out := make([]AgentCallRequest, 0, limit)
	for i := 0; i < limit; i++ {
		if strings.TrimSpace(agents[i]) == "" || strings.TrimSpace(tasks[i]) == "" {
			continue
		}
		out = append(out, AgentCallRequest{
			Agent:         strings.TrimSpace(agents[i]),
			Task:          strings.TrimSpace(tasks[i]),
			IdleTimeoutMS: idleTimeoutMS,
			Contract:      contract.Normalized(),
		})
	}
	return out
}

func parseCallAgentContractFromMap(payload map[string]any) AgentCallContract {
	contract := AgentCallContract{
		TaskGoal:        firstNonEmpty(stringFromAny(payload["task_goal"]), stringFromAny(payload["goal"])),
		Workspace:       stringFromAny(payload["workspace"]),
		ExpectedOutputs: stringSliceFromAny(payload["expected_outputs"]),
		InputArtifacts:  stringSliceFromAny(payload["input_artifacts"]),
		ReturnMode:      firstNonEmpty(stringFromAny(payload["return_mode"]), stringFromAny(payload["result_mode"])),
	}
	return contract.Normalized()
}

func agentIDFromMap(payload map[string]any) string {
	return firstNonEmpty(
		stringFromAny(payload["target_agent"]),
		stringFromAny(payload["agent"]),
		stringFromAny(payload["target"]),
		stringFromAny(payload["targetAgent"]),
		stringFromAny(payload["agent_id"]),
		stringFromAny(payload["agentId"]),
		stringFromAny(payload["agent_name"]),
		stringFromAny(payload["agentName"]),
		stringFromAny(payload["specialist"]),
		stringFromAny(payload["delegate_to"]),
		stringFromAny(payload["delegateTo"]),
		stringFromAny(payload["assignee"]),
	)
}

func taskTextFromMap(payload map[string]any) string {
	return firstNonEmpty(
		stringFromAny(payload["task"]),
		stringFromAny(payload["prompt"]),
		stringFromAny(payload["instruction"]),
		stringFromAny(payload["request"]),
		stringFromAny(payload["message"]),
		stringFromAny(payload["objective"]),
		stringFromAny(payload["subtask"]),
		stringFromAny(payload["work"]),
		stringFromAny(payload["job"]),
		stringFromAny(payload["task_goal"]),
		stringFromAny(payload["goal"]),
	)
}

func normalizeCallAgentPaths(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sliceFromAny(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	default:
		return nil
	}
}

func firstNonEmptySlice(values ...[]any) []any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func stringSliceFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s := strings.TrimSpace(stringFromAny(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func effectiveCallAgentIdleTimeoutMS(input json.RawMessage) (int, bool) {
	in, err := parseCallAgentInput(input)
	if err != nil {
		return 0, false
	}
	idleTimeoutMS := in.IdleTimeoutMS
	for _, task := range in.Tasks {
		if task.IdleTimeoutMS > idleTimeoutMS {
			idleTimeoutMS = task.IdleTimeoutMS
		}
	}
	return idleTimeoutMS, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func repeatString(value string, count int) []string {
	if count <= 0 {
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	out := make([]string, count)
	for i := range out {
		out[i] = value
	}
	return out
}

func validateCallAgentInput(in callAgentInput) error {
	if strings.EqualFold(strings.TrimSpace(in.Mode), "parallel") {
		if len(in.Tasks) == 0 {
			return fmt.Errorf("tasks is required when mode=parallel")
		}
		for _, item := range in.Tasks {
			if strings.TrimSpace(item.Agent) == "" {
				return fmt.Errorf("target_agent is required")
			}
			if strings.TrimSpace(item.Task) == "" {
				return fmt.Errorf("task is required")
			}
		}
		return nil
	}
	if strings.TrimSpace(in.Agent) == "" {
		return fmt.Errorf("target_agent is required")
	}
	if strings.TrimSpace(in.Task) == "" {
		return fmt.Errorf("task is required")
	}
	return nil
}
