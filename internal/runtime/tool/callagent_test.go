package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAgentCallRunner struct {
	agents []AgentInfo
}

func (m *mockAgentCallRunner) DoAgent(ctx context.Context, req AgentCallRequest, callback AgentResultCallback) (string, error) {
	callback(ctx, AgentCallResult{
		Agent:  req.Agent,
		TaskID: "task_1",
		Status: "completed",
	})
	return "task_1", nil
}

func (m *mockAgentCallRunner) AvailableAgents() []AgentInfo {
	return m.agents
}

func (m *mockAgentCallRunner) DoAgentParallel(ctx context.Context, tasks []AgentCallRequest, maxParallel int, callback ParallelAgentResultCallback) (string, error) {
	results := make([]AgentCallResult, len(tasks))
	for i, task := range tasks {
		results[i] = AgentCallResult{
			Agent:  task.Agent,
			TaskID: "task_" + string(rune('1'+i)),
			Status: "completed",
		}
	}
	callback(ctx, ParallelAgentCallResult{
		Mode:        "parallel",
		BatchID:     "batch_1",
		MaxParallel: maxParallel,
		BatchCount:  1,
		Results:     results,
	})
	return "batch_1", nil
}

func TestCallAgentToolRejectsDirectExecution(t *testing.T) {
	tool := &CallAgentTool{}
	input, _ := json.Marshal(map[string]any{
		"target_agent": "coder",
		"task":         "implement it",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "callagent must be scheduled by the runtime workflow dispatcher", result.Error)
}

func TestCallAgentToolRejectsDirectParallelExecution(t *testing.T) {
	tool := &CallAgentTool{}
	input := []byte(`{
		"mode": "parallel",
		"tasks": [
			{"target_agent": "researcher", "task": "collect constraints"},
			{"target_agent": "coder", "task": "ship patch"}
		]
	}`)

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "callagent must be scheduled by the runtime workflow dispatcher", result.Error)
}

func TestCallAgentToolDirectExecutionStillValidatesInput(t *testing.T) {
	tool := &CallAgentTool{}
	input := []byte(`{
		"mode": "parallel",
		"tasks": [
			{"target_agent": "researcher"}
		]
	}`)

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "task is required", result.Error)
}

func TestNormalizeCallAgentPayloadAcceptsSingleAliases(t *testing.T) {
	payload, err := NormalizeCallAgentPayload([]byte(`{
		"targetAgent": "coder",
		"prompt": "implement it"
	}`))
	require.NoError(t, err)
	assert.Equal(t, "coder", payload["target_agent"])
	assert.Equal(t, "implement it", payload["task"])
}

func TestNormalizeCallAgentPayloadUsesCanonicalParallelShape(t *testing.T) {
	payload, err := NormalizeCallAgentPayload([]byte(`{
		"mode": "parallel",
		"tasks": [
			{"target_agent": "order-query", "task": "check order"},
			{"target_agent": "inventory-query", "task": "check stock"}
		]
	}`))
	require.NoError(t, err)
	assert.Equal(t, "parallel", payload["mode"])
	tasks, ok := payload["tasks"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tasks, 2)
	assert.Equal(t, "order-query", tasks[0]["target_agent"])
	assert.Equal(t, "check order", tasks[0]["task"])
	assert.Equal(t, "inventory-query", tasks[1]["target_agent"])
	assert.Equal(t, "check stock", tasks[1]["task"])
}

func TestNormalizeCallAgentPayloadAcceptsExpandedParallelTaskAliases(t *testing.T) {
	payload, err := NormalizeCallAgentPayload([]byte(`{
		"mode": "parallel",
		"delegations": [
			{"agentId": "web-researcher", "prompt": "collect web findings"},
			{"agent_name": "doc-analyzer", "instruction": "analyze docs"}
		]
	}`))
	require.NoError(t, err)
	tasks, ok := payload["tasks"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tasks, 2)
	assert.Equal(t, "web-researcher", tasks[0]["target_agent"])
	assert.Equal(t, "collect web findings", tasks[0]["task"])
	assert.Equal(t, "doc-analyzer", tasks[1]["target_agent"])
	assert.Equal(t, "analyze docs", tasks[1]["task"])
}

func TestNormalizeCallAgentPayloadAcceptsAgentsPlusSharedPrompt(t *testing.T) {
	payload, err := NormalizeCallAgentPayload([]byte(`{
		"mode": "parallel",
		"agents": ["web-researcher", "doc-analyzer", "data-processor"],
		"prompt": "请分别从各自视角总结当前风险"
	}`))
	require.NoError(t, err)
	tasks, ok := payload["tasks"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tasks, 3)
	assert.Equal(t, "web-researcher", tasks[0]["target_agent"])
	assert.Equal(t, "请分别从各自视角总结当前风险", tasks[0]["task"])
	assert.Equal(t, "doc-analyzer", tasks[1]["target_agent"])
	assert.Equal(t, "data-processor", tasks[2]["target_agent"])
}

func TestNormalizeCallAgentPayloadCanonicalizesParallelStringAliases(t *testing.T) {
	payload, err := NormalizeCallAgentPayload([]byte(`{
		"mode": "parallel",
		"agent": ["order-query", "inventory-query"],
		"task": ["check order", "check stock"]
	}`))
	require.NoError(t, err)
	assert.Equal(t, "parallel", payload["mode"])
	tasks, ok := payload["tasks"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tasks, 2)
	assert.Equal(t, "order-query", tasks[0]["target_agent"])
	assert.Equal(t, "check order", tasks[0]["task"])
	assert.Equal(t, "inventory-query", tasks[1]["target_agent"])
	assert.Equal(t, "check stock", tasks[1]["task"])
}

func TestNormalizeCallAgentPayloadRejectsBackgroundModes(t *testing.T) {
	_, err := NormalizeCallAgentPayload([]byte(`{"mode":"background","target_agent":"worker","task":"run"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown mode "background"`)
}

func TestNormalizeCallAgentPayloadIncludesStructuredContract(t *testing.T) {
	payload, err := NormalizeCallAgentPayload([]byte(`{
		"target_agent": "coder",
		"task": "implement patch",
		"task_goal": "ship the reviewed patch",
		"workspace": "/repo",
		"expected_outputs": ["/repo/out/patch.md"],
		"input_artifacts": ["/repo/docs/spec.md"],
		"return_mode": "file_paths"
	}`))
	require.NoError(t, err)
	contract, ok := payload["contract"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ship the reviewed patch", contract["task_goal"])
	assert.Equal(t, "/repo", contract["workspace"])
	assert.Equal(t, "file_paths", contract["return_mode"])
}

func TestRegisterCallAgent(t *testing.T) {
	reg := NewRegistry()
	runner := &mockAgentCallRunner{}
	RegisterCallAgent(reg, runner, 4, 180000)

	tool, ok := reg.Get("callagent")
	assert.True(t, ok)
	assert.Equal(t, "callagent", tool.Name())
}

func TestCallAgentToolTimeoutHintForInputUsesConfiguredDefault(t *testing.T) {
	tool := &CallAgentTool{DefaultIdleTimeoutMS: 180000}
	timeout, ok := tool.TimeoutHintForInput(json.RawMessage(`{"target_agent":"coder","task":"ship patch"}`), 0)
	require.True(t, ok)
	assert.Equal(t, 185*time.Second, timeout)
}

func TestCallAgentToolTimeoutHintForInputUsesLongestRequestedTimeout(t *testing.T) {
	tool := &CallAgentTool{DefaultIdleTimeoutMS: 180000}
	timeout, ok := tool.TimeoutHintForInput(json.RawMessage(`{
		"mode":"parallel",
		"tasks":[
			{"target_agent":"coder","task":"ship patch","idle_timeout_ms":240000},
			{"target_agent":"reviewer","task":"review patch","idle_timeout_ms":120000}
		]
	}`), 0)
	require.True(t, ok)
	assert.Equal(t, 245*time.Second, timeout)
}

func TestCallAgentToolDescriptionListsAgents(t *testing.T) {
	runner := &mockAgentCallRunner{
		agents: []AgentInfo{
			{ID: "supervisor", Name: "Supervisor Agent"},
			{ID: "assistant"},
		},
	}
	tool := &CallAgentTool{Runner: runner}

	desc := tool.Description()
	assert.Contains(t, desc, "Call another agent and get back a structured result")
	assert.Contains(t, desc, "Available agents:")
	assert.Contains(t, desc, "supervisor (Supervisor Agent)")
	assert.Contains(t, desc, "- assistant")
	assert.Contains(t, desc, "Every call must include target_agent")
	assert.Contains(t, desc, "Use callagent when the request needs a specialist")
	assert.Contains(t, desc, `"mode":"parallel","tasks"`)
	assert.Contains(t, desc, "write the final integrated user-facing answer")
	assert.NotContains(t, desc, "background")
}
