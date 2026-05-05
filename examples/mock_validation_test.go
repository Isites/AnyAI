package examples

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/registry"
	airuntime "github.com/Isites/anyai/internal/runtime"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeexecution "github.com/Isites/anyai/internal/runtime/execution"
	inputpkg "github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorypipe "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/tool"
)

var legacySystemPromptAgentIDPattern = regexp.MustCompile(`\((?:id: )([^)]+)\)`)

type scriptedProvider struct {
	handler            func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error)
	handlerWithContext func(ctx context.Context, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error)
	mu                 sync.Mutex
	requests           []llm.ChatRequest
}

func (p *scriptedProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if p != nil {
		p.mu.Lock()
		p.requests = append(p.requests, cloneChatRequest(req))
		p.mu.Unlock()
	}
	agentID := extractAgentID(req.SystemPrompt)
	var (
		events []llm.ChatEvent
		err    error
	)
	if p.handlerWithContext != nil {
		events, err = p.handlerWithContext(ctx, agentID, req)
	} else {
		events, err = p.handler(agentID, req)
	}
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (p *scriptedProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "mock"}}
}

func (p *scriptedProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "example compact summary"}, nil
}

func (p *scriptedProvider) Requests() []llm.ChatRequest {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]llm.ChatRequest, len(p.requests))
	for i, req := range p.requests {
		out[i] = cloneChatRequest(req)
	}
	return out
}

type turnContext struct {
	Prompt             string
	PriorUserTurns     []string
	PriorAssistantText []string
	ToolCallsThisTurn  int
}

type agentCallStepExpectation struct {
	Mode   string
	Agents []string
}

type workflowTurnExpectation struct {
	Prompt            string
	Steps             []agentCallStepExpectation
	ChildCounts       map[string]int
	RequirePriorTurns bool
}

type workflowScenario struct {
	Project        string
	Turns          []workflowTurnExpectation
	IsolatedPrompt string
}

type exampleHarness struct {
	project   *registry.Project
	entry     *config.AgentConfig
	cfg       *config.Config
	store     *session.Store
	recorder  *runtimeevents.Recorder
	runtime   *airuntime.Runtime
	taskStore *task.Store
	taskRT    *task.Runtime
	memoryMgr *memory.Manager
	pipeline  *runtimememorypipe.Pipeline
	providers map[string]llm.LLMProvider
}

type exampleRunOutcome struct {
	Run     runtimeevents.RunRecord
	Events  []runtimeevents.EventRecord
	RunTree runtimeevents.RunTreeRecord
}

func TestExampleProjectsSupportDirectoryAndAgentFileModes(t *testing.T) {
	root := examplesRoot(t)
	projectDirs, err := discoverExampleProjects(root)
	if err != nil {
		t.Fatalf("discover examples: %v", err)
	}

	for _, projectDir := range projectDirs {
		projectDir := projectDir
		t.Run(filepath.Base(projectDir), func(t *testing.T) {
			projectFromDir, err := registry.LoadProject(projectDir)
			if err != nil {
				t.Fatalf("load directory mode: %v", err)
			}
			entryFromDir, err := projectFromDir.SelectEntry("")
			if err != nil {
				t.Fatalf("select entry from directory mode: %v", err)
			}

			projectFromFile, err := registry.LoadProject(filepath.Join(projectDir, "agent.md"))
			if err != nil {
				t.Fatalf("load file mode: %v", err)
			}
			entryFromFile, err := projectFromFile.SelectEntry("")
			if err != nil {
				t.Fatalf("select entry from file mode: %v", err)
			}

			if projectFromDir.Mode != registry.LoadModeDirectory {
				t.Fatalf("expected directory mode, got %s", projectFromDir.Mode)
			}
			if projectFromFile.Mode != registry.LoadModeFile {
				t.Fatalf("expected file mode, got %s", projectFromFile.Mode)
			}
			if entryFromDir.ID != entryFromFile.ID {
				t.Fatalf("entry mismatch: dir=%s file=%s", entryFromDir.ID, entryFromFile.ID)
			}
		})
	}
}

func TestAllExamplesHaveMockScenarioCoverage(t *testing.T) {
	root := examplesRoot(t)
	projectDirs, err := discoverExampleProjects(root)
	if err != nil {
		t.Fatalf("discover examples: %v", err)
	}

	var discovered []string
	for _, dir := range projectDirs {
		discovered = append(discovered, filepath.Base(dir))
	}

	var covered []string
	for _, scenario := range workflowScenarios() {
		covered = append(covered, scenario.Project)
	}

	sort.Strings(discovered)
	sort.Strings(covered)

	if !reflect.DeepEqual(discovered, covered) {
		t.Fatalf("mock scenario coverage mismatch: discovered=%v covered=%v", discovered, covered)
	}
}

func TestExampleWorkflowScenariosWithMockProvider(t *testing.T) {
	root := examplesRoot(t)
	for _, scenario := range workflowScenarios() {
		scenario := scenario
		t.Run(scenario.Project, func(t *testing.T) {
			provider := &scriptedProvider{
				handler: workflowScenarioHandler(scenario),
			}
			harness := newExampleHarness(t, filepath.Join(root, scenario.Project), provider, nil)

			sessionID := scenario.Project + "-session"
			var prompts []string
			var primaryBeforeIsolation []exampleSessionItem

			for idx, turn := range scenario.Turns {
				outcome := harness.run(t, sessionID, turn.Prompt)
				prompts = append(prompts, turn.Prompt)
				if len(turn.Steps) == 0 {
					if err := validateNoDelegation(outcome.Events, outcome.RunTree, outcome.Run.ID); err != nil {
						t.Fatalf("turn %d expected no delegation: %v", idx+1, err)
					}
				} else {
					if err := validateAgentCallSteps(outcome.Events, turn.Steps); err != nil {
						t.Fatalf("turn %d callagent plan: %v", idx+1, err)
					}
					if err := validateChildRunCounts(outcome.RunTree, outcome.Run.ID, turn.ChildCounts); err != nil {
						t.Fatalf("turn %d child runs: %v", idx+1, err)
					}
				}
				if idx == len(scenario.Turns)-1 {
					primaryBeforeIsolation = harness.sessionHistory(t, sessionID)
				}
			}

			if err := validateSessionConversation(primaryBeforeIsolation, prompts); err != nil {
				t.Fatalf("primary session continuation: %v", err)
			}

			isolatedSessionID := scenario.Project + "-isolated"
			isolatedOutcome := harness.run(t, isolatedSessionID, scenario.IsolatedPrompt)
			if err := validateNoDelegation(isolatedOutcome.Events, isolatedOutcome.RunTree, isolatedOutcome.Run.ID); err != nil {
				t.Fatalf("isolated session should not callagent: %v", err)
			}

			primaryAfterIsolation := harness.sessionHistory(t, sessionID)
			isolatedHistory := harness.sessionHistory(t, isolatedSessionID)
			if err := validateSessionIsolation(
				primaryBeforeIsolation,
				primaryAfterIsolation,
				isolatedHistory,
				prompts,
				scenario.IsolatedPrompt,
			); err != nil {
				t.Fatalf("session isolation: %v", err)
			}
		})
	}
}

func TestExampleSkillScopesWithMockProvider(t *testing.T) {
	root := examplesRoot(t)

	t.Run("shared skills are injected for shared-skill projects", func(t *testing.T) {
		provider := &scriptedProvider{
			handler: func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
				return textEvents("分析框架已准备好"), nil
			},
		}
		harness := newExampleHarness(t, filepath.Join(root, "harness-analytics"), provider, nil)
		outcome := harness.run(t, "skill-shared", "请基于 CAC、ARPU 和 Meta 渠道给我一个分析框架。")
		if err := validateNoDelegation(outcome.Events, outcome.RunTree, outcome.Run.ID); err != nil {
			t.Fatalf("expected no delegation: %v", err)
		}

		requests := provider.Requests()
		require.Len(t, requests, 1)
		assert.Equal(t, "data-analyst", extractAgentID(requests[0].SystemPrompt))
		assert.Contains(t, requests[0].SystemPrompt, "### metrics-dictionary")
		assert.Contains(t, requests[0].SystemPrompt, "### channel-knowledge")
		assert.Contains(t, requests[0].SystemPrompt, "H5买量变现核心指标定义和计算口径")
		assert.Contains(t, requests[0].SystemPrompt, "H5买量主流渠道特征和优化要点")
		assert.Contains(t, requests[0].SystemPrompt, "call `skill_get`")
		require.NoError(t, requireTool(requests[0].Tools, "skill_get"))
	})

	t.Run("private skills stay private to the target agent", func(t *testing.T) {
		provider := &scriptedProvider{
			handler: func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
				switch agentID {
				case "logistics-specialist":
					return textEvents("SF 表示顺丰，PENDING 表示待发货"), nil
				case "main-cs":
					return textEvents("我会先判断需要找哪位专员处理"), nil
				default:
					return nil, fmt.Errorf("unexpected agent %q", agentID)
				}
			},
		}

		logisticsHarness := newExampleHarness(t, filepath.Join(root, "ecommerce-cs"), provider, nil)
		logisticsOutcome := logisticsHarness.runForAgent(t, "logistics-specialist", "skill-private", "请解释 SF 快递 PENDING 状态意味着什么。")
		if err := validateNoDelegation(logisticsOutcome.Events, logisticsOutcome.RunTree, logisticsOutcome.Run.ID); err != nil {
			t.Fatalf("logistics specialist should answer directly: %v", err)
		}

		mainHarness := newExampleHarness(t, filepath.Join(root, "ecommerce-cs"), provider, nil)
		mainOutcome := mainHarness.runForAgent(t, "main-cs", "skill-main", "请解释 SF 快递 PENDING 状态意味着什么。")
		if err := validateNoDelegation(mainOutcome.Events, mainOutcome.RunTree, mainOutcome.Run.ID); err != nil {
			t.Fatalf("main-cs should answer directly in this focused validation: %v", err)
		}

		requests := provider.Requests()
		require.Len(t, requests, 2)

		logisticsReq := requests[0]
		mainReq := requests[1]
		assert.Equal(t, "logistics-specialist", extractAgentID(logisticsReq.SystemPrompt))
		assert.Equal(t, "main-cs", extractAgentID(mainReq.SystemPrompt))
		assert.Contains(t, logisticsReq.SystemPrompt, "### logistics-reference")
		assert.Contains(t, logisticsReq.SystemPrompt, "物流配送参考信息，包括快递公司编码和物流状态说明")
		assert.Contains(t, logisticsReq.SystemPrompt, "call `skill_get`")
		require.NoError(t, requireTool(logisticsReq.Tools, "skill_get"))
		assert.NotContains(t, mainReq.SystemPrompt, "快递公司编码")
		assert.NotContains(t, mainReq.SystemPrompt, "顺丰速运")
		assert.NotContains(t, mainReq.SystemPrompt, "待发货")
	})
}

func TestExampleStaticMemoryAndSessionBoundaries(t *testing.T) {
	root := examplesRoot(t)
	staticPrompt := "AnyAI 当前这组 single-agent 示例主要用来验证以下链路 是什么？ 小智长期保持以下回答风格 是什么？"
	followupPrompt := "基于你上一条回答，用一句话再说一次。"
	isolatedPrompt := "这是一个新会话吗？请只用一句话回答。"

	provider := &scriptedProvider{
		handler: func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
			if agentID != "single-agent" {
				return nil, fmt.Errorf("unexpected agent %q", agentID)
			}
			ctx := currentTurnContext(req.Messages)
			switch ctx.Prompt {
			case staticPrompt:
				if err := requireContains(req.SystemPrompt,
					"AnyAI 当前这组 single-agent 示例主要用来验证",
					"小智长期保持以下回答风格",
					"简洁直接",
				); err != nil {
					return nil, err
				}
				return textEvents("Aurora 重点是验证会话与记忆边界；我的长期风格是中文、简洁、直接。"), nil
			case followupPrompt:
				if len(ctx.PriorUserTurns) == 0 || ctx.PriorUserTurns[0] != staticPrompt {
					return nil, fmt.Errorf("follow-up turn did not receive prior session history")
				}
				return textEvents("重点是会话与记忆边界，风格是中文且简洁直接。"), nil
			case isolatedPrompt:
				if len(ctx.PriorUserTurns) != 0 {
					return nil, fmt.Errorf("isolated session leaked prior conversation history")
				}
				return textEvents("这是一个新会话。"), nil
			default:
				return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
			}
		},
	}

	harness := newExampleHarness(t, filepath.Join(root, "single-agent"), provider, nil)
	sessionID := "memory-static-main"
	harness.run(t, sessionID, staticPrompt)
	harness.run(t, sessionID, followupPrompt)

	primaryBeforeIsolation := harness.sessionHistory(t, sessionID)
	if err := validateSessionConversation(primaryBeforeIsolation, []string{staticPrompt, followupPrompt}); err != nil {
		t.Fatalf("primary session continuation: %v", err)
	}

	isolatedSessionID := "memory-static-isolated"
	harness.run(t, isolatedSessionID, isolatedPrompt)
	primaryAfterIsolation := harness.sessionHistory(t, sessionID)
	isolatedHistory := harness.sessionHistory(t, isolatedSessionID)
	if err := validateSessionIsolation(
		primaryBeforeIsolation,
		primaryAfterIsolation,
		isolatedHistory,
		[]string{staticPrompt, followupPrompt},
		isolatedPrompt,
	); err != nil {
		t.Fatalf("session isolation: %v", err)
	}
}

func TestExampleChatDoesNotBecomeMemoryWithoutExplicitSave(t *testing.T) {
	root := examplesRoot(t)
	capturePrompt := "请记住，已确认：Aurora 代号必须是 Polaris。"
	recallPrompt := "Aurora 代号必须是什么？"

	provider := &scriptedProvider{
		handler: func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
			if agentID != "single-agent" {
				return nil, fmt.Errorf("unexpected agent %q", agentID)
			}
			ctx := currentTurnContext(req.Messages)
			switch ctx.Prompt {
			case capturePrompt:
				return textEvents("已确认：Aurora 代号必须是 Polaris。"), nil
			case recallPrompt:
				if len(ctx.PriorUserTurns) != 0 {
					return nil, fmt.Errorf("cross-session recall should not reuse prior conversation history")
				}
				if err := requireNotContains(req.SystemPrompt, "Aurora 代号必须是 Polaris"); err != nil {
					return nil, err
				}
				return textEvents("当前这个新 session 里还没有确认过 Aurora 代号。"), nil
			default:
				return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
			}
		},
	}

	harness := newExampleHarness(t, filepath.Join(root, "single-agent"), provider, func(project *registry.Project) {
		project.Config.Memory.Enabled = true
		project.Config.Memory.Dir = t.TempDir()
		project.Config.Memory.AutoCapture = config.MemoryAutoCaptureConfig{
			Enabled: false,
		}
		project.Config.Memory.Inject = config.MemoryInjectConfig{MaxItems: 3}
	})

	harness.run(t, "memory-capture-main", capturePrompt)
	if harness.memoryMgr == nil {
		t.Fatal("memory manager was not initialized")
	}

	entries := harness.memoryMgr.Entries()
	if len(entries) != 0 {
		t.Fatalf("expected no implicit memory entries, got %d", len(entries))
	}

	harness.run(t, "memory-capture-isolated", recallPrompt)
	isolatedHistory := harness.sessionHistory(t, "memory-capture-isolated")
	if err := validateSessionConversation(isolatedHistory, []string{recallPrompt}); err != nil {
		t.Fatalf("isolated recall session: %v", err)
	}
}

func workflowScenarios() []workflowScenario {
	return []workflowScenario{
		{
			Project: "single-agent",
			Turns: []workflowTurnExpectation{
				{
					Prompt: "请用两句话介绍你自己，并说明你会如何和用户交流。",
				},
				{
					Prompt:            "基于你上一条回答，用一句话重新总结。",
					RequirePriorTurns: true,
				},
			},
			IsolatedPrompt: "这是新的对话吗？请只用一句话回答。",
		},
		{
			Project: "parallel-workflow",
			Turns: []workflowTurnExpectation{
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
				{
					Prompt:            "基于你刚才的研究，用一句话说明你是怎么组织协作的。",
					RequirePriorTurns: true,
				},
			},
			IsolatedPrompt: "这是新的研究请求，请只用一句话说明你会重新开始。",
		},
		{
			Project: "runtime-lab",
			Turns: []workflowTurnExpectation{
				{
					Prompt: "请用两句话说明这个示例主要演示哪些运行时能力。",
				},
				{
					Prompt:            "基于你上一条回答，用一句话说明为什么用户只需要自然语言。",
					RequirePriorTurns: true,
				},
			},
			IsolatedPrompt: "这是新的运行时实验会话吗？请只用一句话回答。",
		},
		{
			Project: "ecommerce-cs",
			Turns: []workflowTurnExpectation{
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
					Prompt:            "继续跟进顺丰单号 SF1234567890 的物流状态。这一轮只做物流跟进，不需要推荐商品，也不需要查询库存。",
					RequirePriorTurns: true,
					Steps: []agentCallStepExpectation{
						{Mode: "single", Agents: []string{"logistics-specialist"}},
					},
					ChildCounts: map[string]int{
						"logistics-specialist": 1,
					},
				},
			},
			IsolatedPrompt: "这是新的客服会话，请只说你会重新查询。",
		},
		{
			Project: "harness-analytics",
			Turns: []workflowTurnExpectation{
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
				{
					Prompt:            "基于你刚才的分析，用一句话概括当前最大风险。",
					RequirePriorTurns: true,
				},
			},
			IsolatedPrompt: "这是新的分析会话，请一句话说明你会重新开始。",
		},
		{
			Project: "harness-coding",
			Turns: []workflowTurnExpectation{
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
				{
					Prompt:            "基于你刚才的流程，用一句话说明为什么不能跳过测试。",
					RequirePriorTurns: true,
				},
			},
			IsolatedPrompt: "这是新的开发会话，请一句话说明你会重新开始。",
		},
		{
			Project: "harness-google-review",
			Turns: []workflowTurnExpectation{
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
				{
					Prompt:            "基于你刚才的审核，用一句话概括当前最大风险。",
					RequirePriorTurns: true,
				},
			},
			IsolatedPrompt: "这是新的审核会话，请一句话说明你会重新开始。",
		},
	}
}

func workflowScenarioHandler(scenario workflowScenario) func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	return func(agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
		switch scenario.Project {
		case "single-agent":
			return handleSingleAgentScenario(scenario, agentID, req)
		case "parallel-workflow":
			return handleParallelWorkflowScenario(scenario, agentID, req)
		case "runtime-lab":
			return handleRuntimeLabScenario(scenario, agentID, req)
		case "ecommerce-cs":
			return handleEcommerceScenario(scenario, agentID, req)
		case "harness-analytics":
			return handleAnalyticsScenario(scenario, agentID, req)
		case "harness-coding":
			return handleCodingScenario(scenario, agentID, req)
		case "harness-google-review":
			return handleGoogleReviewScenario(scenario, agentID, req)
		default:
			return nil, fmt.Errorf("unsupported scenario %q", scenario.Project)
		}
	}
}

func handleSingleAgentScenario(scenario workflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	if agentID != "single-agent" {
		return nil, fmt.Errorf("unexpected agent %q for scenario %q", agentID, scenario.Project)
	}

	switch ctx.Prompt {
	case scenario.Turns[0].Prompt:
		return textEvents("我是一个简洁友好的中文助手，会直接用自然语言和用户交流。"), nil
	case scenario.Turns[1].Prompt:
		if len(ctx.PriorUserTurns) == 0 {
			return nil, fmt.Errorf("single-agent follow-up turn did not receive prior history")
		}
		return textEvents("我是一个简洁直接的中文助手。"), nil
	case scenario.IsolatedPrompt:
		if len(ctx.PriorUserTurns) != 0 {
			return nil, fmt.Errorf("single-agent isolated session leaked prior history")
		}
		return textEvents("这是新的对话。"), nil
	default:
		return nil, fmt.Errorf("unexpected prompt %q", ctx.Prompt)
	}
}

func handleParallelWorkflowScenario(scenario workflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	switch agentID {
	case "parallel-researcher":
		switch ctx.Prompt {
		case scenario.Turns[0].Prompt:
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_parallel_research", []tools.AgentCallRequest{
					{Agent: "web-researcher", Task: "只回复 web"},
					{Agent: "doc-analyzer", Task: "只回复 doc"},
					{Agent: "data-processor", Task: "只回复 data"},
				}), nil
			}
			return textEvents("web, doc, data"), nil
		case scenario.Turns[1].Prompt:
			if len(ctx.PriorUserTurns) == 0 {
				return nil, fmt.Errorf("parallel follow-up turn did not receive prior history")
			}
			return textEvents("我先并行拆分三个角度，再统一汇总。"), nil
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

func handleEcommerceScenario(scenario workflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	switch agentID {
	case "main-cs":
		if err := requireNotContains(req.SystemPrompt, "快递公司编码", "顺丰速运"); err != nil {
			return nil, err
		}
		switch ctx.Prompt {
		case scenario.Turns[0].Prompt:
			if ctx.ToolCallsThisTurn == 0 {
				return parallelAgentCallEvents("tc_cs_parallel", []tools.AgentCallRequest{
					{Agent: "order-query", Task: "查询 ORD123456 的发货状态，只返回订单与发货信息。"},
					{Agent: "inventory-query", Task: "查询 PHONE-APPLE-IPHONE15 的库存，只返回库存结果。"},
				}), nil
			}
			return textEvents("订单已发货，目标 SKU 仍有库存。"), nil
		case scenario.Turns[1].Prompt:
			if len(ctx.PriorUserTurns) == 0 {
				return nil, fmt.Errorf("ecommerce follow-up turn did not receive prior history")
			}
			if ctx.ToolCallsThisTurn == 0 {
				return singleAgentCallEvents("tc_cs_logistics", tools.AgentCallRequest{
					Agent: "logistics-specialist",
					Task:  "请根据 SF1234567890 解释顺丰物流状态，重点说明 SF 和 PENDING 的含义。",
				}), nil
			}
			return textEvents("顺丰单号 SF1234567890 当前为待发货状态。"), nil
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

func handleAnalyticsScenario(scenario workflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	switch agentID {
	case "data-analyst":
		switch ctx.Prompt {
		case scenario.Turns[0].Prompt:
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
		case scenario.Turns[1].Prompt:
			if len(ctx.PriorUserTurns) == 0 {
				return nil, fmt.Errorf("analytics follow-up turn did not receive prior history")
			}
			return textEvents("最大风险是买量成本高于当前变现回收速度。"), nil
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

func handleCodingScenario(scenario workflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	switch agentID {
	case "tech-lead":
		switch ctx.Prompt {
		case scenario.Turns[0].Prompt:
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
		case scenario.Turns[1].Prompt:
			if len(ctx.PriorUserTurns) == 0 {
				return nil, fmt.Errorf("coding follow-up turn did not receive prior history")
			}
			return textEvents("不能跳过测试，因为实现是否可交付必须靠验证闭环确认。"), nil
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

func handleGoogleReviewScenario(scenario workflowScenario, agentID string, req llm.ChatRequest) ([]llm.ChatEvent, error) {
	ctx := currentTurnContext(req.Messages)
	switch agentID {
	case "review-lead":
		switch ctx.Prompt {
		case scenario.Turns[0].Prompt:
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
		case scenario.Turns[1].Prompt:
			if len(ctx.PriorUserTurns) == 0 {
				return nil, fmt.Errorf("google review follow-up turn did not receive prior history")
			}
			return textEvents("当前最大风险仍然是站点信任信号不足。"), nil
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

func newExampleHarness(t *testing.T, projectDir string, provider llm.LLMProvider, mutate func(project *registry.Project)) *exampleHarness {
	t.Helper()

	project, err := registry.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("load project %s: %v", projectDir, err)
	}
	if mutate != nil {
		mutate(project)
	}

	for i := range project.Config.Agents.List {
		project.Config.Agents.List[i].Model = "mock/mock-model"
		project.Config.Agents.List[i].Fallbacks = nil
	}

	entry, err := project.SelectEntry("")
	if err != nil {
		t.Fatalf("select entry for %s: %v", projectDir, err)
	}

	providers := map[string]llm.LLMProvider{"mock": provider}
	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	taskStore := task.NewStore()

	var memMgr *memory.Manager
	if project.Config.Memory.Enabled {
		memMgr = memory.NewManager(project.Config.Memory.Dir)
		memMgr.SetRefreshInterval(0)
		if err := memMgr.Load(); err != nil {
			t.Fatalf("load memory for %s: %v", projectDir, err)
		}
	}
	pipeline := runtimememorypipe.NewPipeline(memMgr, project.Config.Memory)
	rt := airuntime.New(providers, store, project.Config)
	rt.SetRecorder(recorder)
	rt.SetMemory(memMgr)
	rt.SetMemoryPipeline(pipeline)
	taskRT := configureExampleTaskRuntime(t, rt, project.Config, taskStore, nil)

	return &exampleHarness{
		project:   project,
		entry:     entry,
		cfg:       project.Config,
		store:     store,
		recorder:  recorder,
		runtime:   rt,
		taskStore: taskStore,
		taskRT:    taskRT,
		memoryMgr: memMgr,
		pipeline:  pipeline,
		providers: providers,
	}
}

func (h *exampleHarness) run(t *testing.T, sessionID, input string) exampleRunOutcome {
	t.Helper()
	return h.runForAgent(t, h.entry.ID, sessionID, input)
}

func (h *exampleHarness) runForAgent(t *testing.T, agentID, sessionID, input string) exampleRunOutcome {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	run, err := runtimeexecution.StartManagedRun(ctx, runtimeport.ExecutionDeps{
		Providers:    h.providers,
		Config:       h.cfg,
		SessionStore: h.store,
		AgentRunner:  h.runtime,
		Recorder:     h.recorder,
		Memory:       h.memoryMgr,
		Pipeline:     h.pipeline,
		TaskStore:    h.taskStore,
	}, runtimeport.RunRequest{
		AgentID:   agentID,
		SessionID: sessionID,
		Envelope:  inputpkg.NewEnvelopeFromText(sessionID, input),
	})
	if err != nil {
		t.Fatalf("start run for %s: %v", agentID, err)
	}

	for event := range run.Events {
		if message := runtimeevents.FailureMessage(event); message != "" {
			t.Fatalf("run %s returned error: %s", run.RunID, message)
		}
	}

	finalRun, ok := h.recorder.GetRun(run.RunID)
	if !ok {
		t.Fatalf("run %s not recorded", run.RunID)
	}
	if finalRun.Status != runtimeevents.RunStatusCompleted {
		t.Fatalf("run %s status=%s error=%s", run.RunID, finalRun.Status, strings.TrimSpace(finalRun.Error))
	}

	trace := h.waitForRunTreeSnapshot(t, run.RunID, run.RunID)

	return exampleRunOutcome{
		Run:     finalRun,
		Events:  h.recorder.ListRunEvents(run.RunID),
		RunTree: trace,
	}
}

func (h *exampleHarness) waitForRunTreeSnapshot(t *testing.T, parentRunID, rootRunID string) runtimeevents.RunTreeRecord {
	t.Helper()

	expectedChildren := 0
	if h.taskStore != nil {
		for _, tk := range h.taskStore.List() {
			if tk.ParentTaskID == parentRunID {
				expectedChildren++
			}
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	var lastTree runtimeevents.RunTreeRecord
	for {
		tree, ok := h.recorder.GetRunTree(rootRunID)
		if ok {
			lastTree = tree
			if expectedChildren == 0 || countTraceChildRuns(tree, parentRunID) >= expectedChildren {
				return tree
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(lastTree.Runs) == 0 {
		t.Fatalf("run tree %s not recorded", rootRunID)
	}
	return lastTree
}

func countTraceChildRuns(trace runtimeevents.RunTreeRecord, parentRunID string) int {
	count := 0
	for _, n := range agentCallCounts(trace.Events) {
		count += n
	}
	return count
}

func (h *exampleHarness) sessionHistory(t *testing.T, sessionID string) []exampleSessionItem {
	t.Helper()

	sess, err := h.store.Load(h.entry.ID, sessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", sessionID, err)
	}

	var history []exampleSessionItem
	for _, entry := range sess.History() {
		if entry.Type != session.EntryTypeMessage {
			continue
		}
		var msg session.MessageData
		if err := json.Unmarshal(entry.Data, &msg); err != nil {
			t.Fatalf("decode session message: %v", err)
		}
		history = append(history, exampleSessionItem{
			Type: "message",
			Role: entry.Role,
			Text: msg.Text,
		})
	}
	return history
}

func currentTurnContext(messages []llm.Message) turnContext {
	idx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].ToolCallID == "" {
			idx = i
			break
		}
	}
	if idx == -1 {
		return turnContext{}
	}

	ctx := turnContext{
		Prompt: strings.TrimSpace(messages[idx].Content),
	}
	for _, msg := range messages[:idx] {
		if msg.Role == "user" && msg.ToolCallID == "" {
			ctx.PriorUserTurns = append(ctx.PriorUserTurns, strings.TrimSpace(msg.Content))
		}
		if msg.Role == "assistant" && strings.TrimSpace(msg.Content) != "" {
			ctx.PriorAssistantText = append(ctx.PriorAssistantText, strings.TrimSpace(msg.Content))
		}
	}
	for _, msg := range messages[idx+1:] {
		if msg.Role == "assistant" {
			ctx.ToolCallsThisTurn += len(msg.ToolCalls)
		}
	}
	return ctx
}

func extractAgentID(systemPrompt string) string {
	for _, line := range strings.Split(systemPrompt, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- Agent id:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "- Agent id:"))
	}

	matches := legacySystemPromptAgentIDPattern.FindStringSubmatch(systemPrompt)
	if len(matches) == 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

func TestExtractAgentIDSupportsCurrentAndLegacyPromptFormats(t *testing.T) {
	t.Run("runtime identity block", func(t *testing.T) {
		prompt := strings.TrimSpace(`
You are the main assistant.

## Runtime Identity
- Agent name: Runtime Lab
- Agent id: runtime-lab

## Runtime Capabilities
- ...
`)
		if got := extractAgentID(prompt); got != "runtime-lab" {
			t.Fatalf("expected runtime-lab, got %q", got)
		}
	})

	t.Run("legacy inline format", func(t *testing.T) {
		prompt := `This run is for the "assistant" agent (id: assistant).`
		if got := extractAgentID(prompt); got != "assistant" {
			t.Fatalf("expected assistant, got %q", got)
		}
	})
}

func textEvents(text string) []llm.ChatEvent {
	return []llm.ChatEvent{
		{Type: llm.EventTextDelta, Text: text},
		{Type: llm.EventDone},
	}
}

func singleAgentCallEvents(callID string, req tools.AgentCallRequest) []llm.ChatEvent {
	return agentCallEvents(callID, map[string]any{
		"target_agent":    req.Agent,
		"task":            req.Task,
		"idle_timeout_ms": req.IdleTimeoutMS,
	})
}

func parallelAgentCallEvents(callID string, tasks []tools.AgentCallRequest) []llm.ChatEvent {
	payload := map[string]any{
		"mode": "parallel",
	}
	var taskPayload []map[string]any
	for _, task := range tasks {
		item := map[string]any{
			"target_agent": task.Agent,
			"task":         task.Task,
		}
		if task.IdleTimeoutMS > 0 {
			item["idle_timeout_ms"] = task.IdleTimeoutMS
		}
		taskPayload = append(taskPayload, item)
	}
	payload["tasks"] = taskPayload
	return agentCallEvents(callID, payload)
}

func agentCallEvents(callID string, payload map[string]any) []llm.ChatEvent {
	input, _ := json.Marshal(payload)
	call := llm.ToolCall{
		ID:    callID,
		Name:  "callagent",
		Input: input,
	}
	return []llm.ChatEvent{
		{Type: llm.EventToolCallStart, ToolCall: &call},
		{Type: llm.EventToolCallDone, ToolCall: &call},
		{Type: llm.EventDone},
	}
}

func requireContains(haystack string, needles ...string) error {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return fmt.Errorf("system prompt missing %q", needle)
		}
	}
	return nil
}

func requireNotContains(haystack string, needles ...string) error {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return fmt.Errorf("system prompt unexpectedly contained %q", needle)
		}
	}
	return nil
}

func requireTool(defs []llm.ToolDef, name string) error {
	for _, def := range defs {
		if def.Name == name {
			return nil
		}
	}
	return fmt.Errorf("tool %q not exposed to model", name)
}

func validateAgentCallSteps(events []runtimeevents.EventRecord, expected []agentCallStepExpectation) error {
	actual := collectAgentCallSteps(events)
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("agent call steps mismatch: got %+v want %+v", actual, expected)
	}
	return nil
}

func collectAgentCallSteps(events []runtimeevents.EventRecord) []agentCallStepExpectation {
	var steps []agentCallStepExpectation
	for _, event := range events {
		if !isToolStartEvent(event.Name) {
			continue
		}
		if toolName, _ := event.Payload["tool"].(string); toolName != "callagent" {
			continue
		}
		input, ok := decodePayloadMap(event.Payload["input"])
		if !ok {
			continue
		}
		mode, _ := input["mode"].(string)
		if mode == "parallel" {
			agents := collectParallelTaskAgents(input["tasks"])
			steps = append(steps, agentCallStepExpectation{Mode: "parallel", Agents: agents})
			continue
		}
		if agentID, _ := input["target_agent"].(string); strings.TrimSpace(agentID) != "" {
			steps = append(steps, agentCallStepExpectation{Mode: "single", Agents: []string{agentID}})
		}
	}
	return steps
}

func collectParallelTaskAgents(value any) []string {
	var agents []string
	switch tasks := value.(type) {
	case []any:
		for _, task := range tasks {
			taskMap, ok := task.(map[string]any)
			if !ok {
				continue
			}
			if agentID, _ := taskMap["target_agent"].(string); strings.TrimSpace(agentID) != "" {
				agents = append(agents, agentID)
			}
		}
	case []map[string]any:
		for _, taskMap := range tasks {
			if agentID, _ := taskMap["target_agent"].(string); strings.TrimSpace(agentID) != "" {
				agents = append(agents, agentID)
			}
		}
	}
	return agents
}

func cloneChatRequest(req llm.ChatRequest) llm.ChatRequest {
	cloned := req
	if len(req.Messages) > 0 {
		cloned.Messages = append([]llm.Message(nil), req.Messages...)
	}
	if len(req.Tools) > 0 {
		cloned.Tools = append([]llm.ToolDef(nil), req.Tools...)
	}
	return cloned
}

func decodePayloadMap(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case json.RawMessage:
		var decoded map[string]any
		if err := json.Unmarshal(v, &decoded); err != nil {
			return nil, false
		}
		return decoded, true
	case []byte:
		var decoded map[string]any
		if err := json.Unmarshal(v, &decoded); err != nil {
			return nil, false
		}
		return decoded, true
	case string:
		var decoded map[string]any
		if err := json.Unmarshal([]byte(v), &decoded); err != nil {
			return nil, false
		}
		return decoded, true
	default:
		return nil, false
	}
}

func validateChildRunCounts(trace runtimeevents.RunTreeRecord, parentRunID string, expected map[string]int) error {
	if len(expected) == 0 {
		return nil
	}
	actual := agentCallCounts(trace.Events)
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("child run counts mismatch: got %v want %v", actual, expected)
	}
	return nil
}

func agentCallCounts(events []runtimeevents.EventRecord) map[string]int {
	counts := map[string]int{}
	for _, event := range events {
		if event.Name == runtimeevents.EventAgentCallStarted {
			addAgentCallPayloadCounts(counts, event.Payload)
		}
	}
	if len(counts) > 0 {
		return counts
	}
	for _, event := range events {
		if !isToolStartEvent(event.Name) {
			continue
		}
		if toolName, _ := event.Payload["tool"].(string); toolName != "callagent" {
			continue
		}
		input, ok := decodePayloadMap(event.Payload["input"])
		if !ok {
			continue
		}
		addAgentCallPayloadCounts(counts, input)
	}
	return counts
}

func addAgentCallPayloadCounts(counts map[string]int, payload map[string]any) {
	if len(payload) == 0 {
		return
	}
	if mode, _ := payload["mode"].(string); mode == "parallel" {
		for _, agentID := range collectParallelTaskAgents(payload["tasks"]) {
			counts[agentID]++
		}
		return
	}
	if agentID, _ := payload["target_agent"].(string); strings.TrimSpace(agentID) != "" {
		counts[agentID]++
	}
}
