package examples

import (
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

	"github.com/Isites/anyai/internal/gateway"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	inputpkg "github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/tool"
	httpchannel "github.com/Isites/anyai/internal/startup/http"
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
						{Mode: "single", Agents: []string{"coder"}},
						{Mode: "single", Agents: []string{"ui-test-engineer"}},
						{Mode: "single", Agents: []string{"test-engineer"}},
						{Mode: "parallel", Agents: []string{"reviewer", "reviewer-security"}},
						{Mode: "single", Agents: []string{"global-reviewer"}},
						{Mode: "single", Agents: []string{"alignment-reviewer"}},
					},
					ChildCounts: map[string]int{
						"context-analyst":    1,
						"architect":          1,
						"plan-reviewer":      1,
						"coder":              1,
						"ui-test-engineer":   1,
						"test-engineer":      1,
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
					Prompt: "请审核 https://example.com，按标准 AdSense 串行产物流程推进。",
					Steps: []agentCallStepExpectation{
						{Mode: "single", Agents: []string{"intake-triager"}},
						{Mode: "single", Agents: []string{"site-crawler"}},
						{Mode: "parallel", Agents: []string{"content-analyzer", "duplication-auditor", "seo-analyzer"}},
						{Mode: "parallel", Agents: []string{"ux-analyzer", "policy-analyzer", "ad-inventory-analyzer"}},
						{Mode: "single", Agents: []string{"rejection-mapper"}},
						{Mode: "single", Agents: []string{"report-generator"}},
					},
					ChildCounts: map[string]int{
						"intake-triager":        1,
						"site-crawler":          1,
						"content-analyzer":      1,
						"duplication-auditor":   1,
						"seo-analyzer":          1,
						"ux-analyzer":           1,
						"policy-analyzer":       1,
						"ad-inventory-analyzer": 1,
						"rejection-mapper":      1,
						"report-generator":      1,
					},
				},
				{Prompt: "基于你刚才的审核，用一句话概括当前最大风险。"},
				{Prompt: "如果现在只修一个问题，你会优先修什么？"},
				{
					Prompt: "请基于已有站点画像再做一次轻量回归，只让 content-analyzer 和 policy-analyzer 各给一句判断。",
					Steps: []agentCallStepExpectation{
						{Mode: "parallel", Agents: []string{"content-analyzer", "policy-analyzer"}},
					},
					ChildCounts: map[string]int{
						"content-analyzer": 1,
						"policy-analyzer":  1,
					},
				},
				{Prompt: "把整个审核闭环压缩成两句结论。"},
			},
			IsolatedPrompt: "这是新的审核会话，请一句话说明你会重新开始。",
		},
		{
			Project: "h5-online",
			Turns: []channelWorkflowTurn{
				{Prompt: "不要调用工具。请用两句话说明 h5-online 这个示例如何组织上线流程。"},
				{Prompt: "基于你上一条回答，用一句话说明为什么用户只需要自然语言描述上线目标。"},
				{Prompt: "如果审核发现问题，下一步应该发生什么？"},
				{Prompt: "如果复审没有发现问题，工作流如何退出？"},
				{Prompt: "把整个上线闭环压缩成两句结论。"},
			},
			IsolatedPrompt: "这是新的 H5 上线会话，请一句话说明你会重新开始。",
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
		case "h5-online":
			return handleH5OnlineChannelScenario(root, scenario, agentID, req)
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
				return singleAgentCallEvents("tc_coding_build", tools.AgentCallRequest{
					Agent: "coder",
					Task:  "请按审批通过的方案实现健康检查接口。",
				}), nil
			case 4:
				return singleAgentCallEvents("tc_coding_ui_test", tools.AgentCallRequest{
					Agent: "ui-test-engineer",
					Task:  "本轮涉及接口响应但没有浏览器界面变更；请确认是否需要 UI 测试并写入 UI 测试报告。",
				}), nil
			case 5:
				return singleAgentCallEvents("tc_coding_test", tools.AgentCallRequest{
					Agent: "test-engineer",
					Task:  "UI 测试门禁已处理；请按审批通过的方案编写并运行健康检查接口测试。",
				}), nil
			case 6:
				return parallelAgentCallEvents("tc_coding_review", []tools.AgentCallRequest{
					{Agent: "reviewer", Task: "请审查健康检查接口实现。"},
					{Agent: "reviewer-security", Task: "请从安全角度审查健康检查接口实现。"},
				}), nil
			case 7:
				return singleAgentCallEvents("tc_coding_global", tools.AgentCallRequest{
					Agent: "global-reviewer",
					Task:  "请检查健康检查接口变更的全局影响。",
				}), nil
			case 8:
				return singleAgentCallEvents("tc_coding_alignment", tools.AgentCallRequest{
					Agent: "alignment-reviewer",
					Task:  "请检查实现、UI 测试证据、普通测试证据与审批通过方案是否对齐。",
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
	case "ui-test-engineer":
		return textEvents("UI 测试门禁完成：本轮无需浏览器界面测试。"), nil
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
				return singleAgentCallEvents("tc_review_intake", googleReviewTask("intake-triager", "Phase 0: normalize URL and write the site brief.", nil, []string{"artifacts-1/01-site-brief.md"})), nil
			case 1:
				return singleAgentCallEvents("tc_review_crawl", googleReviewTask("site-crawler", "Phase 1: crawl the site using the intake brief.", []string{"artifacts-1/01-site-brief.md"}, []string{"artifacts-1/02-site-profile.json"})), nil
			case 2:
				return parallelAgentCallEvents("tc_review_analysis_batch1", []tools.AgentCallRequest{
					googleReviewTask("content-analyzer", "Phase 2A: analyze content value from the site profile.", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/03-content-analysis.md"}),
					googleReviewTask("duplication-auditor", "Phase 2A: audit duplication from the site profile.", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/04-duplication-analysis.md"}),
					googleReviewTask("seo-analyzer", "Phase 2A: analyze technical SEO from the site profile.", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/05-seo-analysis.md"}),
				}), nil
			case 3:
				return parallelAgentCallEvents("tc_review_analysis_batch2", []tools.AgentCallRequest{
					googleReviewTask("ux-analyzer", "Phase 3: analyze mobile UX from the site profile.", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/06-ux-analysis.md"}),
					googleReviewTask("policy-analyzer", "Phase 3: analyze trust and policy pages from the site profile.", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/07-policy-analysis.md"}),
					googleReviewTask("ad-inventory-analyzer", "Phase 3: analyze AdSense inventory from the site profile.", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/08-ad-inventory-analysis.md"}),
				}), nil
			case 4:
				return singleAgentCallEvents("tc_review_mapping", googleReviewTask("rejection-mapper", "Phase 4: map analysis results to rejection types.", []string{
					"artifacts-1/01-site-brief.md",
					"artifacts-1/03-content-analysis.md",
					"artifacts-1/04-duplication-analysis.md",
					"artifacts-1/05-seo-analysis.md",
					"artifacts-1/06-ux-analysis.md",
					"artifacts-1/07-policy-analysis.md",
					"artifacts-1/08-ad-inventory-analysis.md",
				}, []string{"artifacts-1/09-rejection-mapping.md"})), nil
			case 5:
				return singleAgentCallEvents("tc_review_report", googleReviewTask("report-generator", "Phase 5: generate the final review report.", []string{
					"artifacts-1/01-site-brief.md",
					"artifacts-1/02-site-profile.json",
					"artifacts-1/03-content-analysis.md",
					"artifacts-1/04-duplication-analysis.md",
					"artifacts-1/05-seo-analysis.md",
					"artifacts-1/06-ux-analysis.md",
					"artifacts-1/07-policy-analysis.md",
					"artifacts-1/08-ad-inventory-analysis.md",
					"artifacts-1/09-rejection-mapping.md",
				}, []string{"artifacts-1/10-final-report.md"})), nil
			default:
				return textEvents("审核完成：最终报告已生成，关键风险和 submit_ready 结论已写入正式产物。"), nil
			}
		case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
			if len(ctx.PriorUserTurns) < 1 {
				return nil, fmt.Errorf("google review turn 2 did not receive prior history")
			}
			return textEvents("当前最大风险来自内容价值和信任页证据不足。"), nil
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
					googleReviewTask("content-analyzer", "请基于已有站点画像做一次轻量内容回归，只给一句判断。", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/03-content-analysis.md"}),
					googleReviewTask("policy-analyzer", "请基于已有站点画像做一次轻量政策回归，只给一句判断。", []string{"artifacts-1/02-site-profile.json"}, []string{"artifacts-1/07-policy-analysis.md"}),
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
	case "intake-triager":
		return googleReviewArtifactEvents(req, "artifacts-1/01-site-brief.md", googleReviewArtifactContent("01-site-brief.md"), "已写入 artifacts-1/01-site-brief.md。"), nil
	case "site-crawler":
		return googleReviewArtifactEvents(req, "artifacts-1/02-site-profile.json", googleReviewArtifactContent("02-site-profile.json"), "已写入 artifacts-1/02-site-profile.json。"), nil
	case "content-analyzer":
		return googleReviewArtifactEvents(req, "artifacts-1/03-content-analysis.md", googleReviewArtifactContent("03-content-analysis.md"), "已写入 artifacts-1/03-content-analysis.md。"), nil
	case "duplication-auditor":
		return googleReviewArtifactEvents(req, "artifacts-1/04-duplication-analysis.md", googleReviewArtifactContent("04-duplication-analysis.md"), "已写入 artifacts-1/04-duplication-analysis.md。"), nil
	case "seo-analyzer":
		return googleReviewArtifactEvents(req, "artifacts-1/05-seo-analysis.md", googleReviewArtifactContent("05-seo-analysis.md"), "已写入 artifacts-1/05-seo-analysis.md。"), nil
	case "ux-analyzer":
		return googleReviewArtifactEvents(req, "artifacts-1/06-ux-analysis.md", googleReviewArtifactContent("06-ux-analysis.md"), "已写入 artifacts-1/06-ux-analysis.md。"), nil
	case "policy-analyzer":
		return googleReviewArtifactEvents(req, "artifacts-1/07-policy-analysis.md", googleReviewArtifactContent("07-policy-analysis.md"), "已写入 artifacts-1/07-policy-analysis.md。"), nil
	case "ad-inventory-analyzer":
		return googleReviewArtifactEvents(req, "artifacts-1/08-ad-inventory-analysis.md", googleReviewArtifactContent("08-ad-inventory-analysis.md"), "已写入 artifacts-1/08-ad-inventory-analysis.md。"), nil
	case "rejection-mapper":
		return googleReviewArtifactEvents(req, "artifacts-1/09-rejection-mapping.md", googleReviewArtifactContent("09-rejection-mapping.md"), "已写入 artifacts-1/09-rejection-mapping.md。"), nil
	case "report-generator":
		return googleReviewArtifactEvents(req, "artifacts-1/10-final-report.md", googleReviewArtifactContent("10-final-report.md"), "已写入 artifacts-1/10-final-report.md。"), nil
	}
	return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
}

func handleH5OnlineChannelScenario(root string, scenario channelWorkflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	projectDir := filepath.Join(root, scenario.Project)
	if agentID != "h5-online" {
		return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
	}

	switch ctx.Prompt {
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[0], projectDir)):
		return textEvents("h5-online 是单 Agent 上线主控，通过 HTTP 调用审核和编码两个独立项目。它会循环审核、修复、复审，直到审核没有问题。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[1], projectDir)):
		if len(ctx.PriorUserTurns) < 1 {
			return nil, fmt.Errorf("h5-online turn 2 did not receive prior history")
		}
		return textEvents("用户只要描述上线目标，主控会自己调度审核与修复闭环。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[2], projectDir)):
		if len(ctx.PriorUserTurns) < 2 {
			return nil, fmt.Errorf("h5-online turn 3 did not receive prior history")
		}
		return textEvents("审核发现问题后，必须把问题清单交给 coding 服务修复。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[3], projectDir)):
		if len(ctx.PriorUserTurns) < 3 {
			return nil, fmt.Errorf("h5-online turn 4 did not receive prior history")
		}
		return textEvents("复审没有发现问题时，工作流才能退出并给出上线结论。"), nil
	case resolvedEnvelopeText(buildChannelTurnEnvelope(scenario.Turns[4], projectDir)):
		if len(ctx.PriorUserTurns) < 4 {
			return nil, fmt.Errorf("h5-online turn 5 did not receive prior history")
		}
		return textEvents("先审核，再修复，再复审；唯一退出条件是审核无问题。"), nil
	case scenario.IsolatedPrompt:
		if len(ctx.PriorUserTurns) != 0 {
			return nil, fmt.Errorf("h5-online isolated session leaked prior history")
		}
		return textEvents("这是新的 H5 上线会话，我会重新开始。"), nil
	default:
		return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
	}
}
