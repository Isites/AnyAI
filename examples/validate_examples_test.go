package examples

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/registry"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/startup"
)

const e2eGatewayVersion = "examples-e2e"

type exampleValidationCase struct {
	Name       string
	Turns      []string
	Isolation  string
	Timeout    time.Duration
	TurnChecks map[int]exampleTurnCheck
}

type exampleTurnCheck struct {
	ExpectParallelAgentCall bool
	ExpectNoDelegation      bool
	ExpectedAgents          []string
}

type runCreateResponse struct {
	Run struct {
		ID string `json:"id"`
	} `json:"run"`
	Error string `json:"error"`
}

type runGetResponse struct {
	Run   runtimeevents.RunRecord `json:"run"`
	Error string                  `json:"error"`
}

type runEventsResponse struct {
	Events []runtimeevents.EventRecord `json:"events"`
	Error  string                      `json:"error"`
}

type runTreeGetResponse struct {
	Tree  []runtimeevents.RunNode `json:"tree"`
	Error string                  `json:"error"`
}

type sessionGetResponse struct {
	Session struct {
		AgentID string               `json:"agent_id"`
		Key     string               `json:"key"`
		History []exampleSessionItem `json:"history"`
	} `json:"session"`
	Error string `json:"error"`
}

type exampleSessionItem struct {
	Type string `json:"type"`
	Role string `json:"role"`
	Text string `json:"text"`
}

func TestExampleProjectsE2E(t *testing.T) {
	if os.Getenv("ANYAI_EXAMPLE_E2E") == "" {
		t.Skip("set ANYAI_EXAMPLE_E2E=1 to run example E2E validation")
	}

	root := examplesRoot(t)
	only := strings.TrimSpace(os.Getenv("ANYAI_EXAMPLE_ONLY"))
	model := firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_MODEL"), "ollama/qwen3:1.7b")
	provider := firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_PROVIDER"), "ollama")
	baseURL := firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_BASE_URL"), "http://127.0.0.1:11434/v1")
	timeout := parseDurationOrDefault(os.Getenv("ANYAI_EXAMPLE_TIMEOUT"), 90*time.Second)

	exampleDirs, err := discoverExampleProjects(root)
	if err != nil {
		t.Fatalf("discover examples: %v", err)
	}
	if len(exampleDirs) == 0 {
		t.Fatalf("no example projects found under %s", root)
	}

	cases := validationCases()
	runs := 0
	for _, exampleDir := range exampleDirs {
		name := filepath.Base(exampleDir)
		if only != "" && name != only {
			continue
		}
		tc, ok := cases[name]
		if !ok {
			tc = exampleValidationCase{
				Name: name,
				Turns: []string{
					"不要调用工具。请用三句话介绍你当前这个 Agent 工程的主要职责。",
					"基于你上一条回答，压缩成两条要点，不要引入新信息。",
				},
			}
		}

		runs++
		t.Run(name, func(t *testing.T) {
			if err := validateExampleProject(exampleDir, tc, model, provider, baseURL, timeout); err != nil {
				if isLoopbackListenPermissionError(err) {
					t.Skipf("loopback listen unavailable in this environment: %v", err)
				}
				t.Fatalf("validate example: %v", err)
			}
		})
	}

	if runs == 0 {
		t.Fatalf("example project %q not found under %s", only, root)
	}
}

func validationCases() map[string]exampleValidationCase {
	return map[string]exampleValidationCase{
		"single-agent": {
			Name: "single-agent",
			Turns: []string{
				"不要调用工具。请用两句话介绍你自己，以及你会怎样和用户交流。",
				"基于你上一条回答，用一句中文短句重新总结你的风格。",
			},
			Isolation: "不要调用工具。请只回复：这是一个全新会话。",
			TurnChecks: map[int]exampleTurnCheck{
				1: {
					ExpectNoDelegation: true,
				},
			},
		},
		"parallel-workflow": {
			Name: "parallel-workflow",
			Turns: []string{
				"不要调用工具。请用两句话说明你会如何组织一次并行研究。",
				"基于你上一条回答，用一句话说明为什么用户只需要自然语言描述目标。",
			},
			Isolation: "请用一句话说明这是一次新的研究请求，不要引用之前的对话。",
			TurnChecks: map[int]exampleTurnCheck{
				1: {
					ExpectNoDelegation: true,
				},
			},
		},
		"runtime-lab": {
			Name: "runtime-lab",
			Turns: []string{
				"不要调用工具。请用两句话说明你这个示例主要演示什么。",
				"基于你上一条回答，用一句话说明为什么用户只需要自然语言。",
			},
			Isolation: "不要调用工具。请只回复：这是新的运行时会话。",
			TurnChecks: map[int]exampleTurnCheck{
				1: {
					ExpectNoDelegation: true,
				},
			},
		},
		"ecommerce-cs": {
			Name: "ecommerce-cs",
			Turns: []string{
				"不要调用工具。请用两句话说明你会如何拆解一个同时涉及订单和库存的客服请求。",
				"基于你上一条回答，用一句话说明为什么主 Agent 需要先判断该找哪位专员。",
			},
			Isolation: "不要调用工具。请只回复：这是新的客服会话。",
			TurnChecks: map[int]exampleTurnCheck{
				1: {
					ExpectNoDelegation: true,
				},
			},
		},
		"harness-analytics": {
			Name: "harness-analytics",
			Turns: []string{
				"不要调用工具。请用两句话说明你会如何开始一个新的分析任务。",
				"基于你上一条回答，用一句话说明为什么数据验证不能跳过。",
			},
			Isolation: "不要调用工具。请只回复：这是新的分析会话。",
			Timeout:   4 * time.Minute,
		},
		"harness-coding": {
			Name: "harness-coding",
			Turns: []string{
				"不要调用工具。请用两句话说明你是谁，以及用户如何开始一个编码任务。",
				"基于你上一条回答，用一句话说明为什么真正的编码任务必须经过验证。",
			},
			Isolation: "不要调用工具。请只回复：这是新的开发会话。",
			TurnChecks: map[int]exampleTurnCheck{
				1: {
					ExpectNoDelegation: true,
				},
				2: {
					ExpectNoDelegation: true,
				},
			},
		},
		"harness-google-review": {
			Name: "harness-google-review",
			Turns: []string{
				"不要调用工具。请用两句话说明你会如何组织一次站点审核。",
				"基于你上一条回答，用一句话说明为什么要做回归验证。",
			},
			Isolation: "不要调用工具。请只回复：这是新的审核会话。",
		},
	}
}

func validateExampleProject(projectDir string, testCase exampleValidationCase, model, provider, baseURL string, timeout time.Duration) error {
	project, err := registry.LoadProject(projectDir)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	entryAgent, err := project.SelectEntry("")
	if err != nil {
		return fmt.Errorf("select entry agent: %w", err)
	}

	applyValidationProvider(project.Config, model, provider, baseURL)
	host, port, err := reserveLocalGatewayAddr()
	if err != nil {
		return fmt.Errorf("reserve gateway address: %w", err)
	}
	project.Config.Gateway.Host = host
	project.Config.Gateway.Port = port

	result, err := startup.StartGatewayWithConfig(project.Config, e2eGatewayVersion, startup.Options{
		ConnectTimeout: 2 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}

	serverErr := make(chan error, 1)
	go func() {
		err := result.Serve()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	defer func() {
		result.Cleanup()
		<-serverErr
		time.Sleep(200 * time.Millisecond)
	}()

	baseEndpoint := fmt.Sprintf("http://%s:%d", project.Config.Gateway.Host, project.Config.Gateway.Port)
	if err := waitForHealth(baseEndpoint, 10*time.Second, serverErr); err != nil {
		return fmt.Errorf("wait for health: %w", err)
	}

	sessionID := "validate_" + testCase.Name + "_main"
	isolatedSessionID := "validate_" + testCase.Name + "_isolated"
	if err := resetValidationSession(project, sessionID); err != nil {
		return fmt.Errorf("reset primary validation session: %w", err)
	}
	if err := resetValidationSession(project, isolatedSessionID); err != nil {
		return fmt.Errorf("reset isolated validation session: %w", err)
	}
	turnTimeout := timeout
	if testCase.Timeout > 0 {
		turnTimeout = testCase.Timeout
	}
	for i, turn := range testCase.Turns {
		run, err := createRun(baseEndpoint, sessionID, turn)
		if err != nil {
			return fmt.Errorf("turn %d create run: %w", i+1, err)
		}
		finalRun, err := waitForRun(baseEndpoint, run.ID, turnTimeout)
		if err != nil {
			return fmt.Errorf("turn %d wait run: %w", i+1, err)
		}
		if finalRun.Status != runtimeevents.RunStatusCompleted {
			return fmt.Errorf("turn %d status=%s error=%s", i+1, finalRun.Status, strings.TrimSpace(finalRun.Error))
		}
		if strings.TrimSpace(finalRun.Output) == "" {
			return fmt.Errorf("turn %d produced empty output", i+1)
		}
		if check, ok := testCase.TurnChecks[i+1]; ok {
			if err := validateTurnBehavior(baseEndpoint, finalRun, check); err != nil {
				return fmt.Errorf("turn %d behavior check: %w", i+1, err)
			}
		}
	}

	primarySession, err := fetchSession(baseEndpoint, entryAgent.ID, sessionID)
	if err != nil {
		return fmt.Errorf("fetch primary session: %w", err)
	}
	if err := validateSessionConversation(primarySession.Session.History, testCase.Turns); err != nil {
		return fmt.Errorf("primary session continuation: %w", err)
	}

	isolationPrompt := firstNonEmpty(testCase.Isolation, "不要调用工具。请只回复：这是一个全新会话。")
	run, err := createRun(baseEndpoint, isolatedSessionID, isolationPrompt)
	if err != nil {
		return fmt.Errorf("isolated create run: %w", err)
	}
	finalRun, err := waitForRun(baseEndpoint, run.ID, turnTimeout)
	if err != nil {
		return fmt.Errorf("isolated wait run: %w", err)
	}
	if finalRun.Status != runtimeevents.RunStatusCompleted {
		return fmt.Errorf("isolated status=%s error=%s", finalRun.Status, strings.TrimSpace(finalRun.Error))
	}
	if strings.TrimSpace(finalRun.Output) == "" {
		return fmt.Errorf("isolated run produced empty output")
	}

	primaryAfterIsolation, err := fetchSession(baseEndpoint, entryAgent.ID, sessionID)
	if err != nil {
		return fmt.Errorf("fetch primary session after isolation: %w", err)
	}
	isolatedSession, err := fetchSession(baseEndpoint, entryAgent.ID, isolatedSessionID)
	if err != nil {
		return fmt.Errorf("fetch isolated session: %w", err)
	}
	if err := validateSessionIsolation(
		primarySession.Session.History,
		primaryAfterIsolation.Session.History,
		isolatedSession.Session.History,
		testCase.Turns,
		isolationPrompt,
	); err != nil {
		return fmt.Errorf("session isolation: %w", err)
	}

	return nil
}

func resetValidationSession(project *registry.Project, sessionID string) error {
	if project == nil || project.Config == nil {
		return fmt.Errorf("project config is nil")
	}
	entryAgent, err := project.SelectEntry("")
	if err != nil {
		return err
	}
	store := session.NewStore(filepath.Join(project.Config.RuntimeDataDir(), "sessions"))
	if err := store.Delete(entryAgent.ID, sessionID); err != nil {
		return err
	}
	return nil
}

func discoverExampleProjects(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read examples root: %w", err)
	}

	var projects []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectDir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(projectDir, "anyai.yaml")); err == nil {
			projects = append(projects, projectDir)
		}
	}
	sort.Strings(projects)
	return projects, nil
}

func examplesRoot(t *testing.T) string {
	t.Helper()

	if root := strings.TrimSpace(os.Getenv("ANYAI_EXAMPLES_ROOT")); root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			t.Fatalf("resolve ANYAI_EXAMPLES_ROOT: %v", err)
		}
		return abs
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

func applyValidationProvider(cfg *config.Config, model, provider, baseURL string) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.ProviderConfig{}
	}
	cfg.Providers[provider] = config.ProviderConfig{
		Kind:    "openai-compatible",
		BaseURL: baseURL,
	}
	for i := range cfg.Agents.List {
		cfg.Agents.List[i].Model = model
		for j := range cfg.Agents.List[i].Fallbacks {
			cfg.Agents.List[i].Fallbacks[j] = model
		}
	}
}

func reserveLocalGatewayAddr() (string, int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", 0, err
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return "", 0, fmt.Errorf("unexpected listener addr type %T", listener.Addr())
	}
	return "127.0.0.1", addr.Port, nil
}

func isLoopbackListenPermissionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied")
}

func waitForHealth(baseURL string, timeout time.Duration, serverErr <-chan error) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-serverErr:
			if err != nil {
				return fmt.Errorf("gateway exited before healthy: %w", err)
			}
			return fmt.Errorf("gateway stopped before healthy")
		default:
		}

		resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("gateway did not become healthy within %s", timeout)
}

func createRun(baseURL, sessionID, input string) (runtimeevents.RunRecord, error) {
	payload := map[string]any{
		"inputs":     []map[string]any{{"type": "text", "text": input}},
		"session_id": sessionID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return runtimeevents.RunRecord{}, err
	}

	resp, err := http.Post(strings.TrimRight(baseURL, "/")+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		return runtimeevents.RunRecord{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return runtimeevents.RunRecord{}, err
	}

	var parsed runCreateResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return runtimeevents.RunRecord{}, fmt.Errorf("decode create run response: %w", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		return runtimeevents.RunRecord{}, fmt.Errorf("http %d: %s", resp.StatusCode, firstNonEmpty(parsed.Error, string(data)))
	}
	return runtimeevents.RunRecord{ID: parsed.Run.ID}, nil
}

func waitForRun(baseURL, runID string, timeout time.Duration) (runtimeevents.RunRecord, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	url := strings.TrimRight(baseURL, "/") + "/api/runs/" + runID

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return runtimeevents.RunRecord{}, readErr
		}

		var parsed runGetResponse
		if err := json.Unmarshal(data, &parsed); err != nil {
			return runtimeevents.RunRecord{}, fmt.Errorf("decode run response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return runtimeevents.RunRecord{}, fmt.Errorf("http %d: %s", resp.StatusCode, firstNonEmpty(parsed.Error, string(data)))
		}

		switch parsed.Run.Status {
		case runtimeevents.RunStatusCompleted, runtimeevents.RunStatusFailed, runtimeevents.RunStatusAborted:
			return parsed.Run, nil
		}
		time.Sleep(250 * time.Millisecond)
	}

	return runtimeevents.RunRecord{}, fmt.Errorf("timed out waiting for run %s", runID)
}

func validateTurnBehavior(baseURL string, run runtimeevents.RunRecord, check exampleTurnCheck) error {
	if !check.ExpectParallelAgentCall && !check.ExpectNoDelegation {
		return nil
	}

	events, err := fetchRunEvents(baseURL, run.ID)
	if err != nil {
		return err
	}
	trace, err := fetchRunTree(baseURL, run.ID)
	if err != nil {
		return err
	}
	if check.ExpectParallelAgentCall {
		if err := validateParallelAgentCallEvent(events, check.ExpectedAgents); err != nil {
			return err
		}
		if err := validateChildRuns(trace, run.ID, check.ExpectedAgents); err != nil {
			return err
		}
	}
	if check.ExpectNoDelegation {
		if err := validateNoDelegation(events, trace, run.ID); err != nil {
			return err
		}
	}
	return nil
}

func fetchRunEvents(baseURL, runID string) ([]runtimeevents.EventRecord, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := strings.TrimRight(baseURL, "/") + "/api/runs/" + runID + "/events"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed runEventsResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("decode run events response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, firstNonEmpty(parsed.Error, string(data)))
	}
	return parsed.Events, nil
}

func fetchRunTree(baseURL, runID string) (runtimeevents.RunTreeRecord, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := strings.TrimRight(baseURL, "/") + "/api/runs/" + runID + "/tree"
	resp, err := client.Get(url)
	if err != nil {
		return runtimeevents.RunTreeRecord{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return runtimeevents.RunTreeRecord{}, err
	}

	var parsed runTreeGetResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return runtimeevents.RunTreeRecord{}, fmt.Errorf("decode run tree response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return runtimeevents.RunTreeRecord{}, fmt.Errorf("http %d: %s", resp.StatusCode, firstNonEmpty(parsed.Error, string(data)))
	}
	treeEvents, err := fetchRunTreeEvents(baseURL, runID)
	if err != nil {
		return runtimeevents.RunTreeRecord{}, err
	}
	return runtimeevents.RunTreeRecord{
		Runs:   flattenRunTreeRuns(parsed.Tree),
		Events: treeEvents,
	}, nil
}

func fetchRunTreeEvents(baseURL, runID string) ([]runtimeevents.EventRecord, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := strings.TrimRight(baseURL, "/") + "/api/runs/" + runID + "/tree/events"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed runEventsResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("decode run tree events response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, firstNonEmpty(parsed.Error, string(data)))
	}
	return parsed.Events, nil
}

func flattenRunTreeRuns(tree []runtimeevents.RunNode) []runtimeevents.RunRecord {
	var runs []runtimeevents.RunRecord
	var walk func([]runtimeevents.RunNode)
	walk = func(nodes []runtimeevents.RunNode) {
		for _, node := range nodes {
			runs = append(runs, node.Run)
			walk(node.Children)
		}
	}
	walk(tree)
	return runs
}

func validateParallelAgentCallEvent(events []runtimeevents.EventRecord, expectedAgents []string) error {
	want := make(map[string]bool, len(expectedAgents))
	for _, agentID := range expectedAgents {
		want[agentID] = false
	}

	for _, event := range events {
		if !isToolStartEvent(event.Name) && event.Name != "agent.call.started" {
			continue
		}
		if isToolStartEvent(event.Name) {
			if toolName, _ := event.Payload["tool"].(string); toolName != "callagent" {
				continue
			}
		}

		if event.Name == "agent.call.started" {
			if mode, _ := event.Payload["mode"].(string); mode == "parallel" {
				tasks, ok := event.Payload["tasks"].([]any)
				if !ok {
					if typedTasks, typedOK := event.Payload["tasks"].([]map[string]any); typedOK {
						for _, task := range typedTasks {
							agentID, _ := task["target_agent"].(string)
							if _, exists := want[agentID]; exists {
								want[agentID] = true
							}
						}
						for agentID, seen := range want {
							if !seen {
								return fmt.Errorf("parallel callagent call missing expected agent %q", agentID)
							}
						}
						return nil
					}
					return fmt.Errorf("parallel callagent event did not include tasks")
				}
				for _, task := range tasks {
					taskMap, ok := task.(map[string]any)
					if !ok {
						continue
					}
					agentID, _ := taskMap["target_agent"].(string)
					if _, exists := want[agentID]; exists {
						want[agentID] = true
					}
				}
				for agentID, seen := range want {
					if !seen {
						return fmt.Errorf("parallel callagent call missing expected agent %q", agentID)
					}
				}
				return nil
			}
			continue
		}

		input, ok := event.Payload["input"].(map[string]any)
		if !ok {
			continue
		}
		mode, _ := input["mode"].(string)
		if mode != "parallel" {
			continue
		}

		tasks, ok := input["tasks"].([]any)
		if !ok {
			return fmt.Errorf("parallel callagent call did not include tasks")
		}
		for _, task := range tasks {
			taskMap, ok := task.(map[string]any)
			if !ok {
				continue
			}
			agentID, _ := taskMap["target_agent"].(string)
			if _, exists := want[agentID]; exists {
				want[agentID] = true
			}
		}

		for agentID, seen := range want {
			if !seen {
				return fmt.Errorf("parallel callagent call missing expected agent %q", agentID)
			}
		}
		return nil
	}

	return fmt.Errorf("no parallel callagent tool call found in run events")
}

func validateChildRuns(trace runtimeevents.RunTreeRecord, parentRunID string, expectedAgents []string) error {
	want := make(map[string]bool, len(expectedAgents))
	for _, agentID := range expectedAgents {
		want[agentID] = false
	}

	for agentID := range agentCallCounts(trace.Events) {
		if _, exists := want[agentID]; exists {
			want[agentID] = true
		}
	}

	for agentID, seen := range want {
		if !seen {
			return fmt.Errorf("trace missing callagentd child run for agent %q", agentID)
		}
	}
	return nil
}

func validateNoDelegation(events []runtimeevents.EventRecord, trace runtimeevents.RunTreeRecord, parentRunID string) error {
	for _, event := range events {
		if !isToolStartEvent(event.Name) {
			continue
		}
		if toolName, _ := event.Payload["tool"].(string); toolName == "callagent" {
			return fmt.Errorf("unexpected callagent tool call found")
		}
	}

	for _, run := range trace.Runs {
		if run.ID != parentRunID {
			return fmt.Errorf("unexpected child run for agent %q", run.AgentID)
		}
	}

	return nil
}

func fetchSession(baseURL, agentID, sessionID string) (sessionGetResponse, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := strings.TrimRight(baseURL, "/") + "/api/sessions/" + agentID + "/" + sessionID
	resp, err := client.Get(url)
	if err != nil {
		return sessionGetResponse{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return sessionGetResponse{}, err
	}

	var parsed sessionGetResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return sessionGetResponse{}, fmt.Errorf("decode session response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return sessionGetResponse{}, fmt.Errorf("http %d: %s", resp.StatusCode, firstNonEmpty(parsed.Error, string(data)))
	}
	return parsed, nil
}

func validateSessionConversation(history []exampleSessionItem, expectedUserTurns []string) error {
	userMessages := sessionMessagesForRole(history, "user")
	if !reflect.DeepEqual(userMessages, expectedUserTurns) {
		return fmt.Errorf("user history mismatch: got %v want %v", userMessages, expectedUserTurns)
	}

	assistantMessages := sessionMessagesForRole(history, "assistant")
	if len(assistantMessages) == 0 {
		return fmt.Errorf("session does not contain assistant messages")
	}

	return nil
}

func validateSessionIsolation(
	primaryBefore []exampleSessionItem,
	primaryAfter []exampleSessionItem,
	isolated []exampleSessionItem,
	expectedPrimaryTurns []string,
	isolatedTurn string,
) error {
	if err := validateSessionConversation(primaryBefore, expectedPrimaryTurns); err != nil {
		return fmt.Errorf("primary session before isolation invalid: %w", err)
	}
	if err := validateSessionConversation(primaryAfter, expectedPrimaryTurns); err != nil {
		return fmt.Errorf("primary session after isolation invalid: %w", err)
	}

	beforeUsers := sessionMessagesForRole(primaryBefore, "user")
	afterUsers := sessionMessagesForRole(primaryAfter, "user")
	if !reflect.DeepEqual(beforeUsers, afterUsers) {
		return fmt.Errorf("primary session changed after isolated run: before=%v after=%v", beforeUsers, afterUsers)
	}

	if err := validateSessionConversation(isolated, []string{isolatedTurn}); err != nil {
		return fmt.Errorf("isolated session invalid: %w", err)
	}

	return nil
}

func sessionMessagesForRole(history []exampleSessionItem, role string) []string {
	var messages []string
	for _, item := range history {
		if item.Type != "message" || item.Role != role {
			continue
		}
		messages = append(messages, strings.TrimSpace(item.Text))
	}
	return messages
}

func TestValidateParallelAgentCallEvent(t *testing.T) {
	events := []runtimeevents.EventRecord{
		{
			Name: "tool.called",
			Payload: map[string]any{
				"tool": "callagent",
				"input": map[string]any{
					"mode": "parallel",
					"tasks": []any{
						map[string]any{"target_agent": "web-researcher", "task": "research"},
						map[string]any{"target_agent": "doc-analyzer", "task": "analyze docs"},
						map[string]any{"target_agent": "data-processor", "task": "process data"},
					},
				},
			},
		},
	}

	if err := validateParallelAgentCallEvent(events, []string{
		"web-researcher",
		"doc-analyzer",
		"data-processor",
	}); err != nil {
		t.Fatalf("validate parallel callagent event: %v", err)
	}
}

func TestValidateChildRuns(t *testing.T) {
	trace := runtimeevents.RunTreeRecord{
		Events: []runtimeevents.EventRecord{
			{
				Name: runtimeevents.EventAgentCallStarted,
				Payload: map[string]any{
					"mode": "parallel",
					"tasks": []map[string]any{
						{"target_agent": "web-researcher"},
						{"target_agent": "doc-analyzer"},
						{"target_agent": "data-processor"},
					},
				},
			},
		},
		Runs: []runtimeevents.RunRecord{
			{ID: "run_root", AgentID: "parallel-researcher"},
			{ID: "run_web", AgentID: "web-researcher"},
			{ID: "run_doc", AgentID: "doc-analyzer"},
			{ID: "run_data", AgentID: "data-processor"},
		},
	}

	if err := validateChildRuns(trace, "run_root", []string{
		"web-researcher",
		"doc-analyzer",
		"data-processor",
	}); err != nil {
		t.Fatalf("validate child runs: %v", err)
	}
}

func parseDurationOrDefault(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
