package examples

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httpchannel "github.com/Isites/anyai/internal/startup/http"
	"github.com/Isites/anyai/internal/gateway"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	inputpkg "github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/tool"
)

type channelWorkflowTurn struct {
	Prompt        string
	BuildEnvelope func(projectDir string) inputpkg.InputEnvelope
	Steps         []agentCallStepExpectation
	ChildCounts   map[string]int
}

type channelWorkflowScenario struct {
	Project        string
	Turns          []channelWorkflowTurn
	IsolatedPrompt string
}

type exampleChannelHarness struct {
	t          *testing.T
	projectDir string
	base       *exampleHarness
	server     *httpchannel.Service
	baseURL    string
	serverErr  chan error
}

func TestExampleProjectsMultiChannelFiveTurnScenarios(t *testing.T) {
	root := examplesRoot(t)
	for _, scenario := range channelWorkflowScenarios() {
		scenario := scenario
		t.Run(scenario.Project, func(t *testing.T) {
			provider := &scriptedProvider{
				handler: channelWorkflowScenarioHandler(root, scenario),
			}
			harness := newExampleChannelHarness(t, filepath.Join(root, scenario.Project), provider)

			t.Run("direct", func(t *testing.T) {
				runDirectChannelScenario(t, harness, scenario)
			})

			t.Run("http", func(t *testing.T) {
				runHTTPChannelScenario(t, harness, scenario)
			})
		})
	}
}

func newExampleChannelHarness(t *testing.T, projectDir string, provider llm.LLMProvider) *exampleChannelHarness {
	t.Helper()
	return &exampleChannelHarness{
		t:          t,
		projectDir: projectDir,
		base:       newExampleHarness(t, projectDir, provider, nil),
	}
}

func (h *exampleChannelHarness) ensureServer(t *testing.T) {
	t.Helper()
	if h.server != nil {
		return
	}

	host, port, err := reserveLocalGatewayAddr()
	if err != nil {
		if isLoopbackListenPermissionError(err) {
			t.Skipf("loopback listen unavailable in this environment: %v", err)
		}
		t.Fatalf("reserve local gateway addr: %v", err)
	}

	h.base.cfg.Gateway.Host = host
	h.base.cfg.Gateway.Port = port
	gatewayService := gateway.New(h.base.runtime)
	gatewayService.SetVersion("test")
	h.server = httpchannel.NewService(httpchannel.ServiceOptions{
		Config:  h.base.cfg,
		Gateway: gatewayService,
	})
	h.baseURL = fmt.Sprintf("http://%s:%d", host, port)
	server := h.server
	serverErr := make(chan error, 1)
	h.serverErr = serverErr

	go func(server *httpchannel.Service, serverErr chan error) {
		err := server.Serve()
		if err != nil && err != http.ErrServerClosed {
			serverErr <- err
			return
		}
		serverErr <- nil
	}(server, serverErr)

	if err := waitForHealth(h.baseURL, 10*time.Second, serverErr); err != nil {
		t.Fatalf("wait for health: %v", err)
	}

	t.Cleanup(func() {
		h.server = nil
		h.serverErr = nil
		h.baseURL = ""
		if server == nil || serverErr == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil && err != http.ErrServerClosed {
			t.Fatalf("shutdown gateway: %v", err)
		}
		if err := <-serverErr; err != nil {
			t.Fatalf("gateway exited with error: %v", err)
		}
	})
}

func (h *exampleChannelHarness) runDirect(t *testing.T, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	t.Helper()
	return h.base.runEnvelopeForAgent(t, h.base.entry.ID, sessionID, env)
}

func (h *exampleChannelHarness) runHTTP(t *testing.T, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	t.Helper()
	h.ensureServer(t)

	run, err := createHTTPRunWithEnvelope(h.baseURL, h.base.entry.ID, sessionID, env)
	if err != nil {
		t.Fatalf("create http run: %v", err)
	}

	finalRun, err := waitForRun(h.baseURL, run.ID, 10*time.Second)
	if err != nil {
		t.Fatalf("wait http run: %v", err)
	}
	if finalRun.Status != runtimeevents.RunStatusCompleted {
		t.Fatalf("http run %s status=%s error=%s", finalRun.ID, finalRun.Status, strings.TrimSpace(finalRun.Error))
	}
	if strings.TrimSpace(finalRun.Output) == "" {
		t.Fatalf("http run %s produced empty output", finalRun.ID)
	}

	events, err := fetchRunEvents(h.baseURL, finalRun.ID)
	if err != nil {
		t.Fatalf("fetch http run events: %v", err)
	}
	trace, err := fetchRunTree(h.baseURL, finalRun.ID)
	if err != nil {
		t.Fatalf("fetch http trace: %v", err)
	}

	return exampleRunOutcome{
		Run:     finalRun,
		Events:  events,
		RunTree: trace,
	}
}

func createHTTPRunWithEnvelope(baseURL, agentID, sessionID string, env inputpkg.InputEnvelope) (runtimeevents.RunRecord, error) {
	payload := map[string]any{
		"agent_id":   agentID,
		"inputs":     env.Blocks,
		"session_id": sessionID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return runtimeevents.RunRecord{}, err
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/chat?stream=1", bytes.NewReader(body))
	if err != nil {
		return runtimeevents.RunRecord{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return runtimeevents.RunRecord{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return runtimeevents.RunRecord{}, err
		}
		var parsed runCreateResponse
		if err := json.Unmarshal(data, &parsed); err != nil {
			return runtimeevents.RunRecord{}, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		return runtimeevents.RunRecord{}, fmt.Errorf("http %d: %s", resp.StatusCode, firstNonEmpty(parsed.Error, string(data)))
	}

	return parseAcceptedRunFromSSE(resp.Body)
}

func parseAcceptedRunFromSSE(r io.Reader) (runtimeevents.RunRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentEvent string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:") && currentEvent == "run.accepted":
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var accepted struct {
				Run runtimeevents.RunRecord `json:"run"`
			}
			if err := json.Unmarshal([]byte(payload), &accepted); err != nil {
				return runtimeevents.RunRecord{}, fmt.Errorf("decode accepted run from sse: %w", err)
			}
			if strings.TrimSpace(accepted.Run.ID) == "" {
				return runtimeevents.RunRecord{}, fmt.Errorf("accepted run payload missing run id")
			}
			return accepted.Run, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return runtimeevents.RunRecord{}, err
	}
	return runtimeevents.RunRecord{}, fmt.Errorf("run.accepted event not found in sse stream")
}

func waitForRecordedRun(t *testing.T, recorder *runtimeevents.Recorder, runID string) runtimeevents.RunRecord {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, ok := recorder.GetRun(runID)
		if ok && (run.Status == runtimeevents.RunStatusCompleted || run.Status == runtimeevents.RunStatusFailed || run.Status == runtimeevents.RunStatusAborted) {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("recorded run %s did not complete in time", runID)
	return runtimeevents.RunRecord{}
}

func runDirectChannelScenario(t *testing.T, harness *exampleChannelHarness, scenario channelWorkflowScenario) {
	t.Helper()

	sessionID := scenario.Project + "-direct-main"
	isolatedSessionID := scenario.Project + "-direct-isolated"
	expectedTurns := runChannelTurns(t, scenario, func(turn channelWorkflowTurn) exampleRunOutcome {
		env := buildChannelTurnEnvelope(turn, harness.projectDir)
		return harness.runDirect(t, sessionID, env)
	}, harness.projectDir)

	primaryBeforeIsolation := harness.base.sessionHistory(t, sessionID)
	if err := validateSessionConversation(primaryBeforeIsolation, expectedTurns); err != nil {
		t.Fatalf("direct primary session continuation: %v", err)
	}

	isolatedOutcome := harness.runDirect(t, isolatedSessionID, inputpkg.NewEnvelopeFromText(isolatedSessionID, scenario.IsolatedPrompt))
	if err := validateNoDelegation(isolatedOutcome.Events, isolatedOutcome.RunTree, isolatedOutcome.Run.ID); err != nil {
		t.Fatalf("direct isolated session should not callagent: %v", err)
	}

	primaryAfterIsolation := harness.base.sessionHistory(t, sessionID)
	isolatedHistory := harness.base.sessionHistory(t, isolatedSessionID)
	if err := validateSessionIsolation(primaryBeforeIsolation, primaryAfterIsolation, isolatedHistory, expectedTurns, scenario.IsolatedPrompt); err != nil {
		t.Fatalf("direct session isolation: %v", err)
	}
}

func runHTTPChannelScenario(t *testing.T, harness *exampleChannelHarness, scenario channelWorkflowScenario) {
	t.Helper()

	sessionID := scenario.Project + "-http-main"
	isolatedSessionID := scenario.Project + "-http-isolated"
	expectedTurns := runChannelTurns(t, scenario, func(turn channelWorkflowTurn) exampleRunOutcome {
		env := buildChannelTurnEnvelope(turn, harness.projectDir)
		return harness.runHTTP(t, sessionID, env)
	}, harness.projectDir)

	primaryBeforeIsolation, err := fetchSession(harness.baseURL, harness.base.entry.ID, sessionID)
	if err != nil {
		t.Fatalf("fetch http primary session: %v", err)
	}
	if err := validateSessionConversation(primaryBeforeIsolation.Session.History, expectedTurns); err != nil {
		t.Fatalf("http primary session continuation: %v", err)
	}

	isolatedEnv := inputpkg.NewEnvelopeFromText(isolatedSessionID, scenario.IsolatedPrompt)
	isolatedOutcome := harness.runHTTP(t, isolatedSessionID, isolatedEnv)
	if err := validateNoDelegation(isolatedOutcome.Events, isolatedOutcome.RunTree, isolatedOutcome.Run.ID); err != nil {
		t.Fatalf("http isolated session should not callagent: %v", err)
	}

	primaryAfterIsolation, err := fetchSession(harness.baseURL, harness.base.entry.ID, sessionID)
	if err != nil {
		t.Fatalf("fetch http primary session after isolation: %v", err)
	}
	isolatedHistory, err := fetchSession(harness.baseURL, harness.base.entry.ID, isolatedSessionID)
	if err != nil {
		t.Fatalf("fetch http isolated session: %v", err)
	}
	if err := validateSessionIsolation(primaryBeforeIsolation.Session.History, primaryAfterIsolation.Session.History, isolatedHistory.Session.History, expectedTurns, scenario.IsolatedPrompt); err != nil {
		t.Fatalf("http session isolation: %v", err)
	}
}

func runChannelTurns(
	t *testing.T,
	scenario channelWorkflowScenario,
	runner func(turn channelWorkflowTurn) exampleRunOutcome,
	projectDir string,
) []string {
	t.Helper()

	var expectedTurns []string
	for idx, turn := range scenario.Turns {
		outcome := runner(turn)
		expectedTurns = append(expectedTurns, resolvedEnvelopeText(buildChannelTurnEnvelope(turn, projectDir)))

		if len(turn.Steps) == 0 {
			if err := validateNoDelegation(outcome.Events, outcome.RunTree, outcome.Run.ID); err != nil {
				t.Fatalf("turn %d expected no delegation: %v", idx+1, err)
			}
			continue
		}
		if err := validateAgentCallSteps(outcome.Events, turn.Steps); err != nil {
			t.Fatalf("turn %d callagent plan: %v", idx+1, err)
		}
		if err := validateChildRunCounts(outcome.RunTree, outcome.Run.ID, turn.ChildCounts); err != nil {
			t.Fatalf("turn %d child runs: %v", idx+1, err)
		}
	}
	return expectedTurns
}

func buildChannelTurnEnvelope(turn channelWorkflowTurn, projectDir string) inputpkg.InputEnvelope {
	if turn.BuildEnvelope != nil {
		return turn.BuildEnvelope(projectDir)
	}
	return inputpkg.NewEnvelopeFromText("", turn.Prompt)
}

func resolvedEnvelopeText(env inputpkg.InputEnvelope) string {
	return strings.TrimSpace(strings.Join(inputpkg.ResolveEnvelope(env), "\n"))
}

func channelWorkflowScenarios() []channelWorkflowScenario {
	return []channelWorkflowScenario{
		{
			Project: "single-agent",
			Turns: []channelWorkflowTurn{
				{Prompt: "请用两句话介绍你自己，并说明你会如何和用户交流。"},
				{Prompt: "基于你上一条回答，用一句话重新总结。"},
				{Prompt: "如果用户希望你继续保持简洁直接，你接下来会怎么做？"},
				{Prompt: "再用一句话说明为什么用户只需要自然语言描述任务。"},
				{Prompt: "把前面四轮整理成两个短语。"},
			},
			IsolatedPrompt: "这是新的对话吗？请只用一句话回答。",
		},
		{
			Project: "parallel-workflow",
			Turns: []channelWorkflowTurn{
				{
					Prompt: "请同时从网络角度、本地文档、结构化数据三个角度给我一个研究结论。如果当前环境不便联网，就让网络角度直接基于 workspace 里的离线研究材料完成，不要反复联网重试。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"web-researcher", "doc-analyzer", "data-processor"}},
					},
					ChildCounts: map[string]int{
						"web-researcher": 1,
						"doc-analyzer":   1,
						"data-processor": 1,
					},
				},
				{Prompt: "基于你刚才的研究，用一句话说明你是怎么组织协作的。"},
				{Prompt: "请把上一轮研究结论压缩成三个关键词。"},
				{
					Prompt: "现在请再次并行拆分网络、文档、数据三个角度，但这次只给风险清单。必须重新发起一轮新的三路并行委派，不能直接复用上一轮结果。如果当前环境不便联网，请继续使用离线研究材料，不要重复联网重试。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"web-researcher", "doc-analyzer", "data-processor"}},
					},
					ChildCounts: map[string]int{
						"web-researcher": 1,
						"doc-analyzer":   1,
						"data-processor": 1,
					},
				},
				{Prompt: "基于这五轮过程，用一句话说明为什么用户只要自然语言目标即可。"},
			},
			IsolatedPrompt: "这是新的研究请求，请只用一句话说明你会重新开始。",
		},
		{
			Project: "runtime-lab",
			Turns: []channelWorkflowTurn{
				{Prompt: "请用两句话说明这个示例主要演示哪些运行时能力。"},
				{Prompt: "基于你上一条回答，用一句话说明为什么用户只需要自然语言。"},
				{
					Prompt: "我现在会同时给你文本、文件、目录、URL、图片和 PDF，请说明你会如何理解这些输入。",
					BuildEnvelope: func(projectDir string) inputpkg.InputEnvelope {
						return inputpkg.InputEnvelope{
							Blocks: []inputpkg.InputBlock{
								{Type: "text", Text: "我现在会同时给你文本、文件、目录、URL、图片和 PDF，请说明你会如何理解这些输入。"},
								{Type: "file", Name: "brief.txt", Path: filepath.Join(projectDir, "fixtures", "brief.txt"), MimeType: "text/plain"},
								{Type: "dir", Name: "reference", Path: filepath.Join(projectDir, "fixtures", "reference")},
								{Type: "url", URL: "https://example.com/runtime-lab"},
								{Type: "image", Name: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
								{Type: "pdf", Name: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.4 runtime lab")},
							},
						}
					},
				},
				{Prompt: "什么时候你会考虑把任务交给后台子 Agent？"},
				{Prompt: "把整个运行时能力压缩成两句结论。"},
			},
			IsolatedPrompt: "这是新的运行时实验会话吗？请只用一句话回答。",
		},
		{
			Project: "ecommerce-cs",
			Turns: []channelWorkflowTurn{
				{
					Prompt: "订单号 ORD123456，我要并行确认两件事：1. 这个订单是否已发货；2. SKU PHONE-APPLE-IPHONE15 当前库存。这里只做精确订单和精确库存查询，不需要商品推荐。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"order-query", "inventory-query"}},
					},
					ChildCounts: map[string]int{
						"order-query":     1,
						"inventory-query": 1,
					},
				},
				{
					Prompt: "继续跟进顺丰单号 SF1234567890 的物流状态。这一轮只做物流跟进，不需要推荐商品，也不需要查询库存。",
					Steps: []agentCallStepExpectation{
						{Mode: "single", Agents: []string{"logistics-specialist"}},
					},
					ChildCounts: map[string]int{
						"logistics-specialist": 1,
					},
				},
				{Prompt: "基于前两轮处理流程，用一句话说明主 Agent 为什么要先分流。"},
				{
					Prompt: "现在我只关心 SKU PHONE-APPLE-IPHONE15 的精确库存，请继续查询，不需要推荐、参数介绍或其他商品比较。",
					Steps: []agentCallStepExpectation{
						{Mode: "single", Agents: []string{"inventory-query"}},
					},
					ChildCounts: map[string]int{
						"inventory-query": 1,
					},
				},
				{Prompt: "把本次客服处理过程压缩成两条要点。"},
			},
			IsolatedPrompt: "这是新的客服会话，请只说你会重新查询。",
		},
		{
			Project: "harness-analytics",
			Turns: []channelWorkflowTurn{
				{
					Prompt: "请基于 CAC、ARPU 和 Meta 渠道做一次完整分析，并给出优化建议。",
					Steps: []agentCallStepExpectation{
						{Mode: "single", Agents: []string{"data-validator"}},
						{Mode: "parallel", Agents: []string{"analyst-growth", "analyst-product", "analyst-monetization"}},
						{Mode: "single", Agents: []string{"forecaster"}},
						{Mode: "parallel", Agents: []string{"ua-optimizer", "monetization-optimizer"}},
						{Mode: "single", Agents: []string{"reporter"}},
					},
					ChildCounts: map[string]int{
						"data-validator":         1,
						"analyst-growth":         1,
						"analyst-product":        1,
						"analyst-monetization":   1,
						"forecaster":             1,
						"ua-optimizer":           1,
						"monetization-optimizer": 1,
						"reporter":               1,
					},
				},
				{Prompt: "基于你刚才的分析，用一句话概括当前最大风险。"},
				{Prompt: "如果现在只允许先做一件事，你会先做什么？"},
				{
					Prompt: "请再组织一次轻量的双专家优化会话，分别从投放和变现给出一句建议。必须重新并行调用这两个优化专家，不能只复用前面的结论直接回答。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"ua-optimizer", "monetization-optimizer"}},
					},
					ChildCounts: map[string]int{
						"ua-optimizer":           1,
						"monetization-optimizer": 1,
					},
				},
				{Prompt: "把整个分析闭环压缩成两句结论。"},
			},
			IsolatedPrompt: "这是新的分析会话，请一句话说明你会重新开始。",
		},
		{
			Project: "harness-coding",
			Turns: []channelWorkflowTurn{
				{
					Prompt: "请按项目上下文和编码规范，组织一次新增健康检查接口的完整开发流程。",
					Steps: []agentCallStepExpectation{
						{Mode: "single", Agents: []string{"context-analyst"}},
						{Mode: "single", Agents: []string{"architect"}},
						{Mode: "single", Agents: []string{"plan-reviewer"}},
						{Mode: "parallel", Agents: []string{"coder", "test-engineer"}},
						{Mode: "parallel", Agents: []string{"reviewer", "reviewer-security"}},
						{Mode: "parallel", Agents: []string{"global-reviewer", "alignment-reviewer"}},
						{Mode: "single", Agents: []string{"test-engineer"}},
					},
					ChildCounts: map[string]int{
						"context-analyst":    1,
						"architect":          1,
						"plan-reviewer":      1,
						"coder":              1,
						"test-engineer":      2,
						"reviewer":           1,
						"reviewer-security":  1,
						"global-reviewer":    1,
						"alignment-reviewer": 1,
					},
				},
				{Prompt: "基于你刚才的流程，用一句话说明为什么不能跳过测试。"},
				{Prompt: "如果现在继续推进，你下一步会做什么？"},
				{
					Prompt: "请再做一次精简的代码与安全双评审。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"reviewer", "reviewer-security"}},
					},
					ChildCounts: map[string]int{
						"reviewer":          1,
						"reviewer-security": 1,
					},
				},
				{Prompt: "把整个开发闭环压缩成两句结论。"},
			},
			IsolatedPrompt: "这是新的开发会话，请一句话说明你会重新开始。",
		},
		{
			Project: "harness-google-review",
			Turns: []channelWorkflowTurn{
				{
					Prompt: "请基于 E-E-A-T 和常见拒审原因，做一次完整 Google 审核闭环。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"quality-auditor", "seo-auditor", "ux-auditor", "policy-auditor"}},
						{Mode: "single", Agents: []string{"requirement-generator"}},
						{Mode: "single", Agents: []string{"qa-verifier"}},
						{Mode: "parallel", Agents: []string{"quality-auditor", "seo-auditor", "ux-auditor", "policy-auditor"}},
					},
					ChildCounts: map[string]int{
						"quality-auditor":       2,
						"seo-auditor":           2,
						"ux-auditor":            2,
						"policy-auditor":        2,
						"requirement-generator": 1,
						"qa-verifier":           1,
					},
				},
				{Prompt: "基于你刚才的审核，用一句话概括当前最大风险。"},
				{Prompt: "如果现在只修一个问题，你会优先修什么？"},
				{
					Prompt: "请再做一次轻量回归，只让 quality-auditor 和 policy-auditor 各给一句判断。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"quality-auditor", "policy-auditor"}},
					},
					ChildCounts: map[string]int{
						"quality-auditor": 1,
						"policy-auditor":  1,
					},
				},
				{Prompt: "把整个审核闭环压缩成两句结论。"},
			},
			IsolatedPrompt: "这是新的审核会话，请一句话说明你会重新开始。",
		},
	}
}

func channelWorkflowScenarioHandler(root string, scenario channelWorkflowScenario) func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	return func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
		switch scenario.Project {
		case "single-agent":
			return handleSingleAgentChannelScenario(root, scenario, agentID, req)
		case "parallel-workflow":
			return handleParallelWorkflowChannelScenario(root, scenario, agentID, req)
		case "runtime-lab":
			return handleRuntimeLabChannelScenario(root, scenario, agentID, req)
		case "ecommerce-cs":
			return handleEcommerceChannelScenario(root, scenario, agentID, req)
		case "harness-analytics":
			return handleAnalyticsChannelScenario(root, scenario, agentID, req)
		case "harness-coding":
			return handleCodingChannelScenario(root, scenario, agentID, req)
		case "harness-google-review":
			return handleGoogleReviewChannelScenario(root, scenario, agentID, req)
		default:
			return nil, fmt.Errorf("unsupported scenario %q", scenario.Project)
		}
	}
}

func handleSingleAgentChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	if agentID != "single-agent" {
		return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
	}

	switch ctx.Prompt {
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], filepath.Join(root, scenario.Project))):
		return textEvents("我是一个简洁友好的中文助手，会直接用自然语言和用户交流。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], filepath.Join(root, scenario.Project))):
		if len(ctx.PriorUserTurns) < 1 {
			return nil, fmt.Errorf("single-agent turn 2 did not receive prior history")
		}
		return textEvents("我是一个简洁直接的中文助手。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], filepath.Join(root, scenario.Project))):
		if len(ctx.PriorUserTurns) < 2 {
			return nil, fmt.Errorf("single-agent turn 3 did not receive prior history")
		}
		return textEvents("我会继续先抓主线，再用更短的中文回答。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], filepath.Join(root, scenario.Project))):
		if len(ctx.PriorUserTurns) < 3 {
			return nil, fmt.Errorf("single-agent turn 4 did not receive prior history")
		}
		return textEvents("因为用户只要说出目标，我就能自己组织表达方式和节奏。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], filepath.Join(root, scenario.Project))):
		if len(ctx.PriorUserTurns) < 4 {
			return nil, fmt.Errorf("single-agent turn 5 did not receive prior history")
		}
		return textEvents("中文直答；简洁协作。"), nil
	case scenario.IsolatedPrompt:
		if len(ctx.PriorUserTurns) != 0 {
			return nil, fmt.Errorf("single-agent isolated session leaked prior history")
		}
		return textEvents("这是新的对话。"), nil
	default:
		return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
	}
}

func handleParallelWorkflowChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	projectDir := filepath.Join(root, scenario.Project)
	switch agentID {
	case "parallel-researcher":
		switch ctx.Prompt {
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], projectDir)):
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_parallel_research", []tools.AgentCallRequest{
					{Agent: "web-researcher", Task: "只回复 web"},
					{Agent: "doc-analyzer", Task: "只回复 doc"},
					{Agent: "data-processor", Task: "只回复 data"},
				}), nil
			}
			return textEvents("web, doc, data"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
			if len(ctx.PriorUserTurns) < 1 {
				return nil, fmt.Errorf("parallel turn 2 did not receive prior history")
			}
			return textEvents("我先并行拆分三个角度，再统一汇总。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], projectDir)):
			if len(ctx.PriorUserTurns) < 2 {
				return nil, fmt.Errorf("parallel turn 3 did not receive prior history")
			}
			return textEvents("并行、汇总、结论。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], projectDir)):
			if len(ctx.PriorUserTurns) < 3 {
				return nil, fmt.Errorf("parallel turn 4 did not receive prior history")
			}
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_parallel_risk", []tools.AgentCallRequest{
					{Agent: "web-researcher", Task: "只回复 web risk"},
					{Agent: "doc-analyzer", Task: "只回复 doc risk"},
					{Agent: "data-processor", Task: "只回复 data risk"},
				}), nil
			}
			return textEvents("风险：web 噪音高，doc 证据旧，data 口径偏差。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], projectDir)):
			if len(ctx.PriorUserTurns) < 4 {
				return nil, fmt.Errorf("parallel turn 5 did not receive prior history")
			}
			return textEvents("因为主 Agent 会自己决定何时并行拆解、何时统一汇总。"), nil
		case scenario.IsolatedPrompt:
			if len(ctx.PriorUserTurns) != 0 {
				return nil, fmt.Errorf("parallel isolated session leaked prior history")
			}
			return textEvents("这是新的研究请求，我会重新开始。"), nil
		}
	case "web-researcher":
		return textEvents("web"), nil
	case "doc-analyzer":
		return textEvents("doc"), nil
	case "data-processor":
		return textEvents("data"), nil
	}
	return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
}

func handleRuntimeLabChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	projectDir := filepath.Join(root, scenario.Project)
	if agentID != "runtime-lab" {
		return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
	}

	if err := requireContains(req.SystemPrompt, "用户只需要描述目标", "后台子 Agent"); err != nil {
		return nil, err
	}

	switch ctx.Prompt {
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], projectDir)):
		return textEvents("这个示例主要演示统一输入、会话状态、记忆和后台子 Agent 等运行时能力。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
		if len(ctx.PriorUserTurns) < 1 {
			return nil, fmt.Errorf("runtime-lab turn 2 did not receive prior history")
		}
		return textEvents("因为主 Agent 会自己判断何时使用这些运行时能力，所以用户只需要自然语言。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], projectDir)):
		if len(ctx.PriorUserTurns) < 2 {
			return nil, fmt.Errorf("runtime-lab turn 3 did not receive prior history")
		}
		if err := requireContains(ctx.Prompt,
			"[File: brief.txt, MIME: text/plain]",
			"[Directory: reference]",
			"[URL: https://example.com/runtime-lab]",
			"[Image: diagram.png]",
			"[PDF: spec.pdf]",
		); err != nil {
			return nil, err
		}
		return textEvents("我会先把这些输入统一整理成结构化上下文，需要细节时再按需读取附件。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], projectDir)):
		if len(ctx.PriorUserTurns) < 3 {
			return nil, fmt.Errorf("runtime-lab turn 4 did not receive prior history")
		}
		return textEvents("当任务很长、可并行且不阻塞当前主线时，我会考虑交给后台子 Agent。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], projectDir)):
		if len(ctx.PriorUserTurns) < 4 {
			return nil, fmt.Errorf("runtime-lab turn 5 did not receive prior history")
		}
		return textEvents("统一输入；自然语言驱动。"), nil
	case scenario.IsolatedPrompt:
		if len(ctx.PriorUserTurns) != 0 {
			return nil, fmt.Errorf("runtime-lab isolated session leaked prior history")
		}
		return textEvents("这是新的运行时实验会话。"), nil
	default:
		return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
	}
}

func handleEcommerceChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	projectDir := filepath.Join(root, scenario.Project)
	switch agentID {
	case "main-cs":
		if err := requireNotContains(req.SystemPrompt, "快递公司编码", "顺丰速运"); err != nil {
			return nil, err
		}
		switch ctx.Prompt {
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], projectDir)):
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_cs_parallel", []tools.AgentCallRequest{
					{Agent: "order-query", Task: "查询 ORD123456 的发货状态，只返回订单与发货信息。"},
					{Agent: "inventory-query", Task: "查询 PHONE-APPLE-IPHONE15 的库存，只返回库存结果。"},
				}), nil
			}
			return textEvents("订单已发货，目标 SKU 仍有库存。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
			if len(ctx.PriorUserTurns) < 1 {
				return nil, fmt.Errorf("ecommerce turn 2 did not receive prior history")
			}
			if ctx.ToolCallsThisTurn == 0 {
				return singleAgentCallEvents("tc_cs_logistics", tools.AgentCallRequest{
					Agent: "logistics-specialist",
					Task:  "请根据 SF1234567890 解释顺丰物流状态，重点说明 SF 和 PENDING 的含义。",
				}), nil
			}
			return textEvents("顺丰单号 SF1234567890 当前为待发货状态。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], projectDir)):
			if len(ctx.PriorUserTurns) < 2 {
				return nil, fmt.Errorf("ecommerce turn 3 did not receive prior history")
			}
			return textEvents("因为主 Agent 要先判断问题类型，再把任务交给最合适的专员。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], projectDir)):
			if len(ctx.PriorUserTurns) < 3 {
				return nil, fmt.Errorf("ecommerce turn 4 did not receive prior history")
			}
			if ctx.ToolCallsThisTurn == 0 {
				return singleAgentCallEvents("tc_cs_inventory", tools.AgentCallRequest{
					Agent: "inventory-query",
					Task:  "请继续查询 PHONE-APPLE-IPHONE15 的库存，只返回库存状态。",
				}), nil
			}
			return textEvents("PHONE-APPLE-IPHONE15 当前仍有现货。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], projectDir)):
			if len(ctx.PriorUserTurns) < 4 {
				return nil, fmt.Errorf("ecommerce turn 5 did not receive prior history")
			}
			return textEvents("先分流；再汇总。"), nil
		case scenario.IsolatedPrompt:
			if len(ctx.PriorUserTurns) != 0 {
				return nil, fmt.Errorf("ecommerce isolated session leaked prior history")
			}
			return textEvents("这是新的客服会话，我会重新查询。"), nil
		}
	case "order-query":
		return textEvents("ORD123456 当前已发货，单号 SF1234567890。"), nil
	case "inventory-query":
		return textEvents("PHONE-APPLE-IPHONE15 当前有现货。"), nil
	case "logistics-specialist":
		if err := requireContains(req.SystemPrompt,
			"### logistics-reference",
			"物流配送参考信息，包括快递公司编码和物流状态说明",
			"call `skill_get`",
		); err != nil {
			return nil, err
		}
		if err := requireNotContains(req.SystemPrompt, "## 快递公司编码", "| PENDING | 待发货 |"); err != nil {
			return nil, err
		}
		if err := requireTool(req.Tools, "skill_get"); err != nil {
			return nil, err
		}
		return textEvents("快递公司：顺丰速运；状态：待发货。"), nil
	}
	return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
}

func handleAnalyticsChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	projectDir := filepath.Join(root, scenario.Project)
	switch agentID {
	case "data-analyst":
		switch ctx.Prompt {
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], projectDir)):
			switch ctx.ToolCallsThisTurn {
			case 0:
				return singleAgentCallEvents("tc_analytics_validate", tools.AgentCallRequest{
					Agent: "data-validator",
					Task:  "请先验证 CAC、ARPU 和 Meta 渠道数据口径是否可用。",
				}), nil
			case 1:
				return parallelAgentCallEvents("tc_analytics_analyze", []tools.AgentCallRequest{
					{Agent: "analyst-growth", Task: "请基于 CAC、ARPU 和 Meta 渠道从增长视角分析。"},
					{Agent: "analyst-product", Task: "请基于 CAC、ARPU 和 Meta 渠道从产品体验视角分析。"},
					{Agent: "analyst-monetization", Task: "请基于 CAC、ARPU 和 Meta 渠道从变现视角分析。"},
				}), nil
			case 2:
				return singleAgentCallEvents("tc_analytics_forecast", tools.AgentCallRequest{
					Agent: "forecaster",
					Task:  "请基于前面的分析做趋势预测。",
				}), nil
			case 3:
				return parallelAgentCallEvents("tc_analytics_optimize", []tools.AgentCallRequest{
					{Agent: "ua-optimizer", Task: "请给出投放优化建议。"},
					{Agent: "monetization-optimizer", Task: "请给出变现优化建议。"},
				}), nil
			case 4:
				return singleAgentCallEvents("tc_analytics_report", tools.AgentCallRequest{
					Agent: "reporter",
					Task:  "请整合前面所有结论生成最终报告。",
				}), nil
			default:
				return textEvents("结论：Meta 渠道的 CAC 偏高，ARPU 支撑不足，需同步优化投放与变现。"), nil
			}
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
			if len(ctx.PriorUserTurns) < 1 {
				return nil, fmt.Errorf("analytics turn 2 did not receive prior history")
			}
			return textEvents("最大风险是买量成本高于当前变现回收速度。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], projectDir)):
			if len(ctx.PriorUserTurns) < 2 {
				return nil, fmt.Errorf("analytics turn 3 did not receive prior history")
			}
			return textEvents("我会先控制 Meta CAC，因为这是当前回收链路的最大压力点。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], projectDir)):
			if len(ctx.PriorUserTurns) < 3 {
				return nil, fmt.Errorf("analytics turn 4 did not receive prior history")
			}
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_analytics_light_optimize", []tools.AgentCallRequest{
					{Agent: "ua-optimizer", Task: "请只用一句话给出投放优化建议。"},
					{Agent: "monetization-optimizer", Task: "请只用一句话给出变现优化建议。"},
				}), nil
			}
			return textEvents("先控 CAC，再提 ARPU。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], projectDir)):
			if len(ctx.PriorUserTurns) < 4 {
				return nil, fmt.Errorf("analytics turn 5 did not receive prior history")
			}
			return textEvents("先控买量，再提变现；分析与验证必须闭环。"), nil
		case scenario.IsolatedPrompt:
			if len(ctx.PriorUserTurns) != 0 {
				return nil, fmt.Errorf("analytics isolated session leaked prior history")
			}
			return textEvents("这是新的分析会话，我会重新开始。"), nil
		}
	case "data-validator":
		return textEvents("数据口径可用。"), nil
	case "analyst-growth":
		if err := requireContains(req.SystemPrompt,
			"### metrics-dictionary",
			"### channel-knowledge",
			"H5买量变现核心指标定义和计算口径",
			"H5买量主流渠道特征和优化要点",
			"call `skill_get`",
		); err != nil {
			return nil, err
		}
		if err := requireNotContains(req.SystemPrompt, "获客成本 | CAC", "Meta (Facebook/Instagram)"); err != nil {
			return nil, err
		}
		if err := requireTool(req.Tools, "skill_get"); err != nil {
			return nil, err
		}
		return textEvents("增长视角：Meta CAC 偏高。"), nil
	case "analyst-product":
		return textEvents("产品视角：关键转化页还有流失。"), nil
	case "analyst-monetization":
		return textEvents("变现视角：ARPU 偏低。"), nil
	case "forecaster":
		return textEvents("趋势预测：若不调整，ROI 会继续承压。"), nil
	case "ua-optimizer":
		return textEvents("投放建议：收紧 Meta 定向并重做素材。"), nil
	case "monetization-optimizer":
		return textEvents("变现建议：提高广告填充率并优化付费路径。"), nil
	case "reporter":
		return textEvents("最终报告：问题成立，建议先控 CAC 再提 ARPU。"), nil
	}
	return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
}

func handleCodingChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	projectDir := filepath.Join(root, scenario.Project)
	switch agentID {
	case "tech-lead":
		switch ctx.Prompt {
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], projectDir)):
			switch ctx.ToolCallsThisTurn {
			case 0:
				return singleAgentCallEvents("tc_coding_context", tools.AgentCallRequest{
					Agent: "context-analyst",
					Task:  "请分析健康检查接口需求和项目现状。",
				}), nil
			case 1:
				return singleAgentCallEvents("tc_coding_architecture", tools.AgentCallRequest{
					Agent: "architect",
					Task:  "请基于上下文给出健康检查接口的完整实施方案。",
				}), nil
			case 2:
				return singleAgentCallEvents("tc_coding_plan_review", tools.AgentCallRequest{
					Agent: "plan-reviewer",
					Task:  "请审核健康检查接口方案并给出通过或封驳结论。",
				}), nil
			case 3:
				return parallelAgentCallEvents("tc_coding_build", []tools.AgentCallRequest{
					{Agent: "coder", Task: "请按审批通过的方案实现健康检查接口。"},
					{Agent: "test-engineer", Task: "请按审批通过的方案编写并运行健康检查接口测试。"},
				}), nil
			case 4:
				return parallelAgentCallEvents("tc_coding_review", []tools.AgentCallRequest{
					{Agent: "reviewer", Task: "请审查健康检查接口实现。"},
					{Agent: "reviewer-security", Task: "请从安全角度审查健康检查接口实现。"},
				}), nil
			case 5:
				return parallelAgentCallEvents("tc_coding_global_alignment", []tools.AgentCallRequest{
					{Agent: "global-reviewer", Task: "请检查健康检查接口变更的全局影响。"},
					{Agent: "alignment-reviewer", Task: "请检查实现与审批通过方案是否对齐。"},
				}), nil
			case 6:
				return singleAgentCallEvents("tc_coding_acceptance", tools.AgentCallRequest{
					Agent: "test-engineer",
					Task:  "请执行最终验收测试并给出结论。",
				}), nil
			default:
				return textEvents("流程完成：方案、实现、测试、审查和验收都已闭环。"), nil
			}
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
			if len(ctx.PriorUserTurns) < 1 {
				return nil, fmt.Errorf("coding turn 2 did not receive prior history")
			}
			return textEvents("不能跳过测试，因为实现是否可交付必须靠验证闭环确认。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], projectDir)):
			if len(ctx.PriorUserTurns) < 2 {
				return nil, fmt.Errorf("coding turn 3 did not receive prior history")
			}
			return textEvents("我会先处理评审意见，再确认实现和测试是否需要补强。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], projectDir)):
			if len(ctx.PriorUserTurns) < 3 {
				return nil, fmt.Errorf("coding turn 4 did not receive prior history")
			}
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_coding_light_review", []tools.AgentCallRequest{
					{Agent: "reviewer", Task: "请做一次精简代码评审。"},
					{Agent: "reviewer-security", Task: "请做一次精简安全评审。"},
				}), nil
			}
			return textEvents("精简双评审完成，当前没有新的高风险问题。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], projectDir)):
			if len(ctx.PriorUserTurns) < 4 {
				return nil, fmt.Errorf("coding turn 5 did not receive prior history")
			}
			return textEvents("方案、实现、评审、测试都要闭环；交付前必须验证。"), nil
		case scenario.IsolatedPrompt:
			if len(ctx.PriorUserTurns) != 0 {
				return nil, fmt.Errorf("coding isolated session leaked prior history")
			}
			return textEvents("这是新的开发会话，我会重新开始。"), nil
		}
	case "context-analyst":
		return textEvents("上下文分析完成：需要一个可回归验证的健康检查接口。"), nil
	case "architect":
		return textEvents("架构方案完成：新增健康检查路由、handler 与服务层接口。"), nil
	case "plan-reviewer":
		return textEvents("方案审批通过。"), nil
	case "coder":
		return textEvents("实现完成：新增健康检查处理器。"), nil
	case "test-engineer":
		return textEvents("测试完成：新增健康检查接口测试并给出验收结论。"), nil
	case "reviewer":
		return textEvents("逻辑审查通过。"), nil
	case "reviewer-security":
		return textEvents("安全审查通过。"), nil
	case "global-reviewer":
		return textEvents("全局影响审查通过。"), nil
	case "alignment-reviewer":
		return textEvents("对齐审查通过。"), nil
	}
	return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
}

func handleGoogleReviewChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	projectDir := filepath.Join(root, scenario.Project)
	switch agentID {
	case "review-lead":
		switch ctx.Prompt {
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], projectDir)):
			switch ctx.ToolCallsThisTurn {
			case 0:
				return parallelAgentCallEvents("tc_review_audit_round1", []tools.AgentCallRequest{
					{Agent: "quality-auditor", Task: "首次审计：请基于 E-E-A-T 和常见拒审原因审查站点。"},
					{Agent: "seo-auditor", Task: "首次审计：请基于 E-E-A-T 和常见拒审原因审查站点。"},
					{Agent: "ux-auditor", Task: "首次审计：请基于 E-E-A-T 和常见拒审原因审查站点。"},
					{Agent: "policy-auditor", Task: "首次审计：请基于 E-E-A-T 和常见拒审原因审查站点。"},
				}), nil
			case 1:
				return singleAgentCallEvents("tc_review_requirements", tools.AgentCallRequest{
					Agent: "requirement-generator",
					Task:  "请把四位审计师的问题整理成修复需求。",
				}), nil
			case 2:
				return singleAgentCallEvents("tc_review_qa", tools.AgentCallRequest{
					Agent: "qa-verifier",
					Task:  "请验证修复是否到位。",
				}), nil
			case 3:
				return parallelAgentCallEvents("tc_review_audit_round2", []tools.AgentCallRequest{
					{Agent: "quality-auditor", Task: "回归后重新审计：请再次基于 E-E-A-T 和常见拒审原因审查站点。"},
					{Agent: "seo-auditor", Task: "回归后重新审计：请再次基于 E-E-A-T 和常见拒审原因审查站点。"},
					{Agent: "ux-auditor", Task: "回归后重新审计：请再次基于 E-E-A-T 和常见拒审原因审查站点。"},
					{Agent: "policy-auditor", Task: "回归后重新审计：请再次基于 E-E-A-T 和常见拒审原因审查站点。"},
				}), nil
			default:
				return textEvents("审核闭环完成：主要阻塞项已清理，剩余风险可控。"), nil
			}
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
			if len(ctx.PriorUserTurns) < 1 {
				return nil, fmt.Errorf("google review turn 2 did not receive prior history")
			}
			return textEvents("当前最大风险仍然是站点信任信号不足。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], projectDir)):
			if len(ctx.PriorUserTurns) < 2 {
				return nil, fmt.Errorf("google review turn 3 did not receive prior history")
			}
			return textEvents("我会优先补齐最影响信任和拒审风险的信任页与政策页。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], projectDir)):
			if len(ctx.PriorUserTurns) < 3 {
				return nil, fmt.Errorf("google review turn 4 did not receive prior history")
			}
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_review_light_regression", []tools.AgentCallRequest{
					{Agent: "quality-auditor", Task: "请做一次轻量质量回归，只给一句判断。"},
					{Agent: "policy-auditor", Task: "请做一次轻量政策回归，只给一句判断。"},
				}), nil
			}
			return textEvents("轻量回归显示主要问题已缓解，但还需继续增强信任信号。"), nil
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], projectDir)):
			if len(ctx.PriorUserTurns) < 4 {
				return nil, fmt.Errorf("google review turn 5 did not receive prior history")
			}
			return textEvents("先审计，再修复，再回归；始终围绕信任和合规闭环。"), nil
		case scenario.IsolatedPrompt:
			if len(ctx.PriorUserTurns) != 0 {
				return nil, fmt.Errorf("google review isolated session leaked prior history")
			}
			return textEvents("这是新的审核会话，我会重新开始。"), nil
		}
	case "quality-auditor":
		return textEvents("内容质量审计：存在薄内容和信任信号不足。"), nil
	case "seo-auditor":
		return textEvents("SEO 审计：结构化数据需要补齐。"), nil
	case "ux-auditor":
		return textEvents("UX 审计：移动端信任信号不足。"), nil
	case "policy-auditor":
		return textEvents("政策审计：暂无硬性违规，但需补充政策页。"), nil
	case "requirement-generator":
		return textEvents("修复需求：补信任页、补结构化数据、补内容深度。"), nil
	case "qa-verifier":
		return textEvents("QA 验证：修复项基本通过。"), nil
	}
	return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
}
