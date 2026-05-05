package examples

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	"github.com/Isites/anyai/internal/registry"
	airuntime "github.com/Isites/anyai/internal/runtime"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeexecution "github.com/Isites/anyai/internal/runtime/execution"
	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	inputpkg "github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorypipe "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	httpchannel "github.com/Isites/anyai/internal/startup/http"
)

type liveExampleHarness struct {
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
	timeout   time.Duration
}

type liveExampleChannelHarness struct {
	t          *testing.T
	projectDir string
	base       *liveExampleHarness
	server     *httpchannel.Service
	baseURL    string
	serverErr  chan error
	timeout    time.Duration
}

func TestExampleProjectsMultiChannelFiveTurnE2E(t *testing.T) {
	if os.Getenv("ANYAI_EXAMPLE_CHANNEL_E2E") == "" {
		t.Skip("set ANYAI_EXAMPLE_CHANNEL_E2E=1 to run multi-channel 5-turn example E2E validation")
	}

	root := examplesRoot(t)
	only := strings.TrimSpace(os.Getenv("ANYAI_EXAMPLE_ONLY"))
	model := firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_MODEL"), "ollama/qwen3:1.7b")
	provider := firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_PROVIDER"), "ollama")
	baseURL := firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_BASE_URL"), "http://127.0.0.1:11434/v1")
	timeout := parseDurationOrDefault(os.Getenv("ANYAI_EXAMPLE_TIMEOUT"), 5*time.Minute)

	scenarios := liveChannelWorkflowScenarios()
	runs := 0
	for _, scenario := range scenarios {
		scenario := scenario
		if only != "" && scenario.Project != only {
			continue
		}
		runs++
		t.Run(scenario.Project, func(t *testing.T) {
			harness := newLiveExampleChannelHarness(t, filepath.Join(root, scenario.Project), model, provider, baseURL, timeout)

			t.Run("direct", func(t *testing.T) {
				runLiveDirectChannelScenario(t, harness, scenario)
			})

			t.Run("http", func(t *testing.T) {
				runLiveHTTPChannelScenario(t, harness, scenario)
			})
		})
	}

	if runs == 0 {
		t.Fatalf("example project %q not found under %s", only, root)
	}
}

func newLiveExampleChannelHarness(
	t *testing.T,
	projectDir, model, providerName, providerBaseURL string,
	timeout time.Duration,
) *liveExampleChannelHarness {
	t.Helper()
	return &liveExampleChannelHarness{
		t:          t,
		projectDir: projectDir,
		base:       newLiveExampleHarness(t, projectDir, model, providerName, providerBaseURL, timeout),
		timeout:    timeout,
	}
}

func newLiveExampleHarness(
	t *testing.T,
	projectDir, model, providerName, providerBaseURL string,
	timeout time.Duration,
) *liveExampleHarness {
	t.Helper()

	project, err := registry.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("load project %s: %v", projectDir, err)
	}
	for i := range project.Config.Agents.List {
		project.Config.Agents.List[i].Workspace = project.RootDir
	}
	applyValidationProvider(project.Config, model, providerName, providerBaseURL)
	project.Config.Memory.AutoCapture = config.MemoryAutoCaptureConfig{}

	entry, err := project.SelectEntry("")
	if err != nil {
		t.Fatalf("select entry for %s: %v", projectDir, err)
	}

	providers := runtimefactory.InitProviders(project.Config, nil)
	if _, ok := providers[providerName]; !ok {
		t.Fatalf("provider %q was not initialized for %s", providerName, projectDir)
	}

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

	return &liveExampleHarness{
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
		timeout:   timeout,
	}
}

func (h *liveExampleHarness) executionDeps() runtimeport.ExecutionDeps {
	return runtimeport.ExecutionDeps{
		Providers:    h.providers,
		Config:       h.cfg,
		SessionStore: h.store,
		AgentRunner:  h.runtime,
		Recorder:     h.recorder,
		Memory:       h.memoryMgr,
		Pipeline:     h.pipeline,
		TaskStore:    h.taskStore,
	}
}

func (h *liveExampleHarness) runEnvelopeForAgent(t *testing.T, agentID, sessionID, channel string, env inputpkg.InputEnvelope) exampleRunOutcome {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()

	if env.SessionID == "" {
		env.SessionID = sessionID
	}

	run, err := runtimeexecution.StartManagedRun(ctx, h.executionDeps(), runtimeport.RunRequest{
		AgentID:   agentID,
		SessionID: sessionID,
		Envelope:  env,
		Channel:   channel,
	})
	if err != nil {
		t.Fatalf("start %s run for %s: %v", channel, agentID, err)
	}

	for event := range run.Events {
		if message := runtimeevents.FailureMessage(event); message != "" {
			t.Fatalf("%s run %s returned error: %s", channel, run.RunID, message)
		}
	}

	finalRun, ok := h.recorder.GetRun(run.RunID)
	if !ok {
		t.Fatalf("%s run %s not recorded", channel, run.RunID)
	}
	if finalRun.Status != runtimeevents.RunStatusCompleted {
		t.Fatalf("%s run %s status=%s error=%s", channel, run.RunID, finalRun.Status, strings.TrimSpace(finalRun.Error))
	}
	if strings.TrimSpace(finalRun.Output) == "" {
		t.Fatalf("%s run %s produced empty output", channel, run.RunID)
	}

	trace, ok := h.recorder.GetRunTree(run.RunID)
	if !ok {
		t.Fatalf("%s run tree %s not recorded", channel, run.RunID)
	}

	return exampleRunOutcome{
		Run:     finalRun,
		Events:  h.recorder.ListRunEvents(run.RunID),
		RunTree: trace,
	}
}

func (h *liveExampleHarness) sessionHistory(t *testing.T, sessionID string) []exampleSessionItem {
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

func (h *liveExampleChannelHarness) ensureServer(t *testing.T) {
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

func (h *liveExampleChannelHarness) runDirect(t *testing.T, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	t.Helper()
	return h.base.runEnvelopeForAgent(t, h.base.entry.ID, sessionID, "direct", env)
}

func (h *liveExampleChannelHarness) runHTTP(t *testing.T, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	t.Helper()
	h.ensureServer(t)

	run, err := createHTTPRunWithEnvelope(h.baseURL, h.base.entry.ID, sessionID, env)
	if err != nil {
		t.Fatalf("create http run: %v", err)
	}

	finalRun, err := waitForRun(h.baseURL, run.ID, h.timeout)
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
func runLiveDirectChannelScenario(t *testing.T, harness *liveExampleChannelHarness, scenario channelWorkflowScenario) {
	t.Helper()

	sessionID := scenario.Project + "-direct-live-main"
	isolatedSessionID := scenario.Project + "-direct-live-isolated"
	expectedTurns := runLiveChannelTurns(t, scenario, func(turn channelWorkflowTurn) exampleRunOutcome {
		env := buildChannelTurnEnvelope(turn, harness.projectDir)
		return harness.runDirect(t, sessionID, env)
	}, harness.projectDir)

	primaryBeforeIsolation := harness.base.sessionHistory(t, sessionID)
	if err := validateSessionConversation(primaryBeforeIsolation, expectedTurns); err != nil {
		t.Fatalf("direct primary session continuation: %v", err)
	}

	harness.runDirect(t, isolatedSessionID, inputpkg.NewEnvelopeFromText(isolatedSessionID, scenario.IsolatedPrompt))

	primaryAfterIsolation := harness.base.sessionHistory(t, sessionID)
	isolatedHistory := harness.base.sessionHistory(t, isolatedSessionID)
	if err := validateSessionIsolation(primaryBeforeIsolation, primaryAfterIsolation, isolatedHistory, expectedTurns, scenario.IsolatedPrompt); err != nil {
		t.Fatalf("direct session isolation: %v", err)
	}
}

func runLiveHTTPChannelScenario(t *testing.T, harness *liveExampleChannelHarness, scenario channelWorkflowScenario) {
	t.Helper()

	sessionID := scenario.Project + "-http-live-main"
	isolatedSessionID := scenario.Project + "-http-live-isolated"
	expectedTurns := runLiveChannelTurns(t, scenario, func(turn channelWorkflowTurn) exampleRunOutcome {
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

	harness.runHTTP(t, isolatedSessionID, inputpkg.NewEnvelopeFromText(isolatedSessionID, scenario.IsolatedPrompt))

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

func runLiveChannelTurns(
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

		if strings.TrimSpace(outcome.Run.Output) == "" {
			t.Fatalf("turn %d produced empty output", idx+1)
		}
		if len(turn.Steps) == 0 {
			continue
		}
		if err := validateLiveDelegation(outcome, turn); err != nil {
			t.Fatalf(
				"turn %d live delegation: %v\noutput=%q\nevents=%s",
				idx+1,
				err,
				strings.TrimSpace(outcome.Run.Output),
				liveEventSummary(outcome.Events),
			)
		}
	}
	return expectedTurns
}

func liveEventSummary(events []runtimeevents.EventRecord) string {
	if len(events) == 0 {
		return "[]"
	}
	items := make([]string, 0, len(events))
	for _, event := range events {
		item := event.Name
		if toolName := runtimeevents.ToolName(event); toolName != "" {
			item += ":" + toolName
		}
		items = append(items, item)
	}
	return "[" + strings.Join(items, ", ") + "]"
}

func validateLiveDelegation(outcome exampleRunOutcome, turn channelWorkflowTurn) error {
	if err := validateAgentCallSteps(outcome.Events, turn.Steps); err == nil {
		return nil
	}
	if err := validateLiveParallelEquivalents(outcome.Events, turn.Steps); err == nil {
		if len(turn.ChildCounts) == 0 {
			return nil
		}
		if err := validateChildRunCounts(outcome.RunTree, outcome.Run.ID, turn.ChildCounts); err == nil {
			return nil
		}
	}
	if len(turn.ChildCounts) == 0 {
		return validateAgentCallSteps(outcome.Events, turn.Steps)
	}
	if err := validateChildRunCounts(outcome.RunTree, outcome.Run.ID, turn.ChildCounts); err == nil {
		return nil
	}
	stepErr := validateAgentCallSteps(outcome.Events, turn.Steps)
	childErr := validateChildRunCounts(outcome.RunTree, outcome.Run.ID, turn.ChildCounts)
	return fmt.Errorf("event validation failed: %v; child-run validation failed: %v", stepErr, childErr)
}

func validateLiveParallelEquivalents(events []runtimeevents.EventRecord, expected []agentCallStepExpectation) error {
	if len(expected) != 1 || expected[0].Mode != "parallel" || len(expected[0].Agents) == 0 {
		return fmt.Errorf("no live parallel equivalence")
	}

	actualSet := make(map[string]struct{}, len(expected[0].Agents))
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
		if mode, _ := input["mode"].(string); strings.TrimSpace(mode) == "parallel" {
			continue
		}
		agentID, _ := input["target_agent"].(string)
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			continue
		}
		actualSet[agentID] = struct{}{}
	}

	for _, agentID := range expected[0].Agents {
		if _, ok := actualSet[agentID]; !ok {
			return fmt.Errorf("missing parallel-equivalent single call for agent %q", agentID)
		}
	}
	return nil
}

func liveChannelWorkflowScenarios() []channelWorkflowScenario {
	return channelWorkflowScenarios()
}
