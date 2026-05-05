package examples

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Isites/anyai/internal/gateway"
	"github.com/Isites/anyai/internal/registry"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	inputpkg "github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/startup"
)

type liveE2EOptions struct {
	root            string
	only            string
	model           string
	provider        string
	providerBaseURL string
	timeout         time.Duration
}

type liveStartHarness struct {
	t       *testing.T
	project *registry.Project
	entry   string
	result  *startup.Result
	baseURL string

	serverErr chan error
	timeout   time.Duration
}

type liveChatHarness struct {
	t       *testing.T
	project *registry.Project
	entry   string
	result  *startup.Result
	store   *session.Store
	timeout time.Duration
}

type agentInventoryAgent struct {
	ID     string `json:"id"`
	Skills struct {
		Effective []struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		} `json:"effective"`
	} `json:"skills"`
}

type agentInventoryResponse struct {
	Agents []agentInventoryAgent `json:"agents"`
}

type liveMemoryStatsResponse struct {
	Stats struct {
		Total      int `json:"total"`
		Candidates int `json:"candidates"`
		Episodic   int `json:"episodic"`
		LongTerm   int `json:"long_term"`
	} `json:"stats"`
}

type liveMemorySearchResponse struct {
	Matches []struct {
		Entry struct {
			ID    string `json:"id"`
			Layer string `json:"layer"`
		} `json:"entry"`
		MatchedTerms []string `json:"matched_terms"`
	} `json:"matches"`
}

type liveMemoryItemResponse struct {
	Entry struct {
		ID      string            `json:"id"`
		Layer   string            `json:"layer"`
		Content string            `json:"content"`
		Meta    map[string]string `json:"metadata"`
	} `json:"entry"`
}

type liveMemoryCleanupResponse struct {
	Removed int `json:"removed"`
	Stats   struct {
		Total int `json:"total"`
	} `json:"stats"`
}

func TestExampleProjectsChatModeE2E(t *testing.T) {
	opts := liveE2EOptionsFromEnv(t)

	exampleDirs, err := discoverExampleProjects(opts.root)
	require.NoError(t, err)

	cases := validationCases()
	runs := 0
	for _, exampleDir := range exampleDirs {
		name := filepath.Base(exampleDir)
		if opts.only != "" && name != opts.only {
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
				Isolation: "不要调用工具。请只回复：这是一个全新会话。",
			}
		}

		runs++
		t.Run(name, func(t *testing.T) {
			harness := newLiveChatHarness(t, exampleDir, opts)

			mainSender := "local-main"
			isolatedSender := "local-isolated"
			var expectedTurns []string
			for _, turn := range tc.Turns {
				expectedTurns = append(expectedTurns, turn)
				harness.injectAndWait(t, mainSender, turn, expectedTurns)
			}

			primaryBefore := harness.sessionHistory(t, mainSender)
			if err := validateSessionConversation(primaryBefore, expectedTurns); err != nil {
				t.Fatalf("chat primary session continuation: %v", err)
			}

			isolationPrompt := firstNonEmpty(tc.Isolation, "不要调用工具。请只回复：这是一个全新会话。")
			harness.injectAndWait(t, isolatedSender, isolationPrompt, []string{isolationPrompt})

			primaryAfter := harness.sessionHistory(t, mainSender)
			isolated := harness.sessionHistory(t, isolatedSender)
			if err := validateSessionIsolation(primaryBefore, primaryAfter, isolated, expectedTurns, isolationPrompt); err != nil {
				t.Fatalf("chat session isolation: %v", err)
			}
		})
	}

	if runs == 0 {
		t.Fatalf("example project %q not found under %s", opts.only, opts.root)
	}
}

func TestExampleLiveCapabilitiesE2E(t *testing.T) {
	opts := liveE2EOptionsFromEnv(t)

	t.Run("single-agent-memory-recall", func(t *testing.T) {
		if opts.only != "" && opts.only != "single-agent" {
			t.Skip("filtered by ANYAI_EXAMPLE_ONLY")
		}

		harness := newLiveStartHarness(t, filepath.Join(opts.root, "single-agent"), opts)
		outcome := harness.runHTTP(t, "single-agent", "live-memory-recall", inputpkg.NewEnvelopeFromText("live-memory-recall", "请只用一句中文回答：这个 single-agent 示例主要验证什么边界？"))

		assert.Contains(t, outcome.Run.Output, "会话")
		assert.Contains(t, outcome.Run.Output, "记忆")
	})

	t.Run("harness-analytics-shared-skills", func(t *testing.T) {
		if opts.only != "" && opts.only != "harness-analytics" {
			t.Skip("filtered by ANYAI_EXAMPLE_ONLY")
		}

		harness := newLiveStartHarness(t, filepath.Join(opts.root, "harness-analytics"), opts)
		inventory := harness.agentInventory(t)
		validator := inventoryAgentByID(t, inventory, "data-validator")
		effective := effectiveSkillNames(validator)
		assert.Contains(t, effective, "metrics-dictionary")
		assert.Contains(t, effective, "channel-knowledge")

		outcome := harness.runHTTP(t, "data-validator", "live-shared-skill", inputpkg.NewEnvelopeFromText("live-shared-skill", "如有需要，你可以调用 `skill_get` 读取完整技能，但不要调用其他工具，也不要执行 bash。请回答三点：1. CAC 的公式；2. ARPU 的公式；3. Meta 渠道的一个典型特点。"))
		assert.Equal(t, "data-validator", outcome.Run.AgentID)
		if err := validateNoDelegation(outcome.Events, outcome.RunTree, outcome.Run.ID); err != nil {
			t.Fatalf("expected direct shared-skill run without delegation: %v", err)
		}
	})

	t.Run("ecommerce-cs-private-skills", func(t *testing.T) {
		if opts.only != "" && opts.only != "ecommerce-cs" {
			t.Skip("filtered by ANYAI_EXAMPLE_ONLY")
		}

		harness := newLiveStartHarness(t, filepath.Join(opts.root, "ecommerce-cs"), opts)
		inventory := harness.agentInventory(t)

		mainAgent := inventoryAgentByID(t, inventory, "main-cs")
		logisticsAgent := inventoryAgentByID(t, inventory, "logistics-specialist")
		assert.NotContains(t, effectiveSkillNames(mainAgent), "logistics-reference")
		assert.Contains(t, effectiveSkillNames(logisticsAgent), "logistics-reference")

		outcome := harness.runHTTP(t, "logistics-specialist", "live-private-skill", inputpkg.NewEnvelopeFromText("live-private-skill", "如有需要，你可以调用 `skill_get` 读取完整技能，但不要调用其他工具，也不要委派。请直接回答：SF 表示哪家快递公司？PENDING 表示什么状态？"))
		assert.Contains(t, outcome.Run.Output, "顺丰")
		assert.Contains(t, outcome.Run.Output, "待发货")
	})

	t.Run("runtime-lab-tools-and-memory", func(t *testing.T) {
		if opts.only != "" && opts.only != "runtime-lab" {
			t.Skip("filtered by ANYAI_EXAMPLE_ONLY")
		}

		harness := newLiveStartHarness(t, filepath.Join(opts.root, "runtime-lab"), opts)
		projectDir := harness.project.RootDir

		inputOutcome := harness.runHTTPAllowEmptyOutput(t, "runtime-lab", "live-runtime-input", inputpkg.InputEnvelope{
			SessionID: "live-runtime-input",
			Blocks: []inputpkg.InputBlock{
				{Type: "text", Text: "请严格遵守以下约束：只能先调用 input_manifest 查看当前输入清单；随后只能通过 attachment_get 读取 brief.txt、diagram.png 和 spec.pdf，禁止猜路径，也禁止使用 read_file；然后用 update_plan 写一个两步计划，再用 todo 新增一个待办，最后总结输入类型。"},
				{Type: "file", Name: "brief.txt", Path: filepath.Join(projectDir, "fixtures", "brief.txt")},
				{Type: "dir", Name: "reference", Path: filepath.Join(projectDir, "fixtures", "reference")},
				{Type: "url", URL: "https://example.com/runtime-lab"},
				{Type: "image", Name: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
				{Type: "pdf", Name: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.4 runtime lab")},
			},
		})
		inputTools := calledToolNames(inputOutcome.Events)
		assert.Contains(t, inputTools, "input_manifest")
		assert.Contains(t, inputTools, "update_plan")
		assert.Contains(t, inputTools, "todo")
		assert.Contains(t, inputTools, "attachment_get")

		memoryOutcome := harness.runHTTP(t, "runtime-lab", "live-runtime-memory", inputpkg.NewEnvelopeFromText("live-runtime-memory", "请严格按这两个步骤执行：第一步只能调用一次 memory_search，先找出 Runtime Lab 的长期规则和当前演示重点对应的记忆 ID；第二步必须调用 memory_get 读取其中最相关的一条完整记忆，再基于读取结果告诉我结论。不要重复 memory_search。"))
		memoryTools := calledToolNames(memoryOutcome.Events)
		assert.Contains(t, memoryTools, "memory_search")
		assert.True(t,
			strings.Contains(memoryOutcome.Run.Output, "自然语言") ||
				strings.Contains(memoryOutcome.Run.Output, "演示重点") ||
				strings.Contains(memoryOutcome.Run.Output, "后台"),
			"expected memory-backed summary in output, got: %s", memoryOutcome.Run.Output,
		)
	})

	t.Run("single-agent-seeded-memory-api-and-recall-events", func(t *testing.T) {
		if opts.only != "" && opts.only != "single-agent" {
			t.Skip("filtered by ANYAI_EXAMPLE_ONLY")
		}

		harness := newLiveStartHarness(t, filepath.Join(opts.root, "single-agent"), opts)
		stats := harness.memoryStats(t)
		assert.GreaterOrEqual(t, stats.Stats.Total, 2)
		assert.GreaterOrEqual(t, stats.Stats.Episodic, 1)
		assert.GreaterOrEqual(t, stats.Stats.LongTerm, 1)

		search := harness.memorySearch(t, "single-agent 会话 记忆 边界", "episodic,long-term")
		require.NotEmpty(t, search.Matches)
		assert.NotEmpty(t, search.Matches[0].MatchedTerms)

		item := harness.memoryItem(t, search.Matches[0].Entry.ID)
		assert.True(t,
			strings.Contains(item.Entry.Content, "single-agent") ||
				strings.Contains(item.Entry.Content, "会话") ||
				strings.Contains(item.Entry.Content, "记忆"),
			"expected seeded single-agent memory content, got: %s", item.Entry.Content,
		)

		secondSession := "live-memory-recall"
		secondOutcome := harness.runHTTP(t, "single-agent", secondSession, inputpkg.NewEnvelopeFromText(secondSession, "请简短说明：当前这组 single-agent 示例主要验证什么，以及你应该用什么风格回答？"))
		assert.NotEmpty(t, strings.TrimSpace(secondOutcome.Run.Output))
		if err := validateRunHasMemoryRecall(secondOutcome.Events); err != nil {
			t.Fatalf("expected memory recall event in second run: %v", err)
		}

		expiredPath := filepath.Join(harness.project.Config.Memory.Dir, "episodic", "candidates", "expired-e2e.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(expiredPath), 0o755))
		require.NoError(t, os.WriteFile(expiredPath, []byte(expiredE2EMemoryDoc()), 0o644))

		cleanup := harness.cleanupMemory(t)
		assert.GreaterOrEqual(t, cleanup.Removed, 1)
	})
}

func liveE2EOptionsFromEnv(t *testing.T) liveE2EOptions {
	t.Helper()

	if os.Getenv("ANYAI_EXAMPLE_E2E") == "" {
		t.Skip("set ANYAI_EXAMPLE_E2E=1 to run real example E2E validation")
	}

	return liveE2EOptions{
		root:            examplesRoot(t),
		only:            strings.TrimSpace(os.Getenv("ANYAI_EXAMPLE_ONLY")),
		model:           firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_MODEL"), "ollama/qwen3:1.7b"),
		provider:        firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_PROVIDER"), "ollama"),
		providerBaseURL: firstNonEmpty(os.Getenv("ANYAI_EXAMPLE_BASE_URL"), "http://127.0.0.1:11434/v1"),
		timeout:         parseDurationOrDefault(os.Getenv("ANYAI_EXAMPLE_TIMEOUT"), 2*time.Minute),
	}
}

func newLiveStartHarness(t *testing.T, sourceDir string, opts liveE2EOptions) *liveStartHarness {
	t.Helper()

	projectDir := cloneExampleProject(t, sourceDir)
	project, err := registry.LoadProject(projectDir)
	require.NoError(t, err)
	entry, err := project.SelectEntry("")
	require.NoError(t, err)

	applyValidationProvider(project.Config, opts.model, opts.provider, opts.providerBaseURL)
	host, port, err := reserveLocalGatewayAddr()
	if err != nil && isLoopbackListenPermissionError(err) {
		t.Skipf("loopback listen unavailable in this environment: %v", err)
	}
	require.NoError(t, err)
	project.Config.Gateway.Host = host
	project.Config.Gateway.Port = port

	result, err := startup.StartGatewayWithConfig(project.Config, e2eGatewayVersion, startup.Options{
		ConnectTimeout:  2 * time.Second,
		LaunchMode:      startup.LaunchModeStart,
		FallbackAgentID: entry.ID,
	})
	require.NoError(t, err)

	h := &liveStartHarness{
		t:         t,
		project:   project,
		entry:     entry.ID,
		result:    result,
		baseURL:   fmt.Sprintf("http://%s:%d", host, port),
		serverErr: make(chan error, 1),
		timeout:   opts.timeout,
	}

	go func() {
		err := result.Serve()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.serverErr <- err
			return
		}
		h.serverErr <- nil
	}()

	require.NoError(t, waitForHealth(h.baseURL, 10*time.Second, h.serverErr))
	t.Cleanup(func() {
		result.Cleanup()
		<-h.serverErr
	})

	return h
}

func (h *liveStartHarness) runHTTP(t *testing.T, agentID, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	return h.runHTTPWithOutputPolicy(t, agentID, sessionID, env, true)
}

func (h *liveStartHarness) runHTTPAllowEmptyOutput(t *testing.T, agentID, sessionID string, env inputpkg.InputEnvelope) exampleRunOutcome {
	return h.runHTTPWithOutputPolicy(t, agentID, sessionID, env, false)
}

func (h *liveStartHarness) runHTTPWithOutputPolicy(t *testing.T, agentID, sessionID string, env inputpkg.InputEnvelope, requireOutput bool) exampleRunOutcome {
	t.Helper()

	if strings.TrimSpace(agentID) == "" {
		agentID = h.entry
	}
	run, err := createHTTPRunWithEnvelope(h.baseURL, agentID, sessionID, env)
	require.NoError(t, err)

	finalRun, err := waitForRun(h.baseURL, run.ID, h.timeout)
	require.NoError(t, err)
	require.Equal(t, runtimeevents.RunStatusCompleted, finalRun.Status)
	if requireOutput {
		require.NotEmpty(t, strings.TrimSpace(finalRun.Output))
	}

	events, err := fetchRunEvents(h.baseURL, finalRun.ID)
	require.NoError(t, err)
	trace, err := fetchRunTree(h.baseURL, finalRun.ID)
	require.NoError(t, err)

	return exampleRunOutcome{
		Run:     finalRun,
		Events:  events,
		RunTree: trace,
	}
}

func (h *liveStartHarness) agentInventory(t *testing.T) agentInventoryResponse {
	t.Helper()

	var inventory agentInventoryResponse
	require.NoError(t, fetchJSON(strings.TrimRight(h.baseURL, "/")+"/api/agents", &inventory))
	require.NotEmpty(t, inventory.Agents)
	return inventory
}

func (h *liveStartHarness) memoryStats(t *testing.T) liveMemoryStatsResponse {
	t.Helper()

	var stats liveMemoryStatsResponse
	require.NoError(t, fetchJSON(strings.TrimRight(h.baseURL, "/")+"/api/memory/stats", &stats))
	return stats
}

func (h *liveStartHarness) memorySearch(t *testing.T, query, layer string) liveMemorySearchResponse {
	t.Helper()

	var response liveMemorySearchResponse
	url := fmt.Sprintf("%s/api/memory/search?q=%s&layer=%s", strings.TrimRight(h.baseURL, "/"), queryEscape(query), queryEscape(layer))
	require.NoError(t, fetchJSON(url, &response))
	return response
}

func (h *liveStartHarness) memoryItem(t *testing.T, id string) liveMemoryItemResponse {
	t.Helper()

	var response liveMemoryItemResponse
	url := fmt.Sprintf("%s/api/memory/item?id=%s", strings.TrimRight(h.baseURL, "/"), queryEscape(id))
	require.NoError(t, fetchJSON(url, &response))
	return response
}

func (h *liveStartHarness) cleanupMemory(t *testing.T) liveMemoryCleanupResponse {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(h.baseURL, "/")+"/api/memory/stale-cleanup", nil)
	require.NoError(t, err)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var payload liveMemoryCleanupResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return payload
}

func newLiveChatHarness(t *testing.T, sourceDir string, opts liveE2EOptions) *liveChatHarness {
	t.Helper()

	projectDir := cloneExampleProject(t, sourceDir)
	project, err := registry.LoadProject(projectDir)
	require.NoError(t, err)
	entry, err := project.SelectEntry("")
	require.NoError(t, err)

	applyValidationProvider(project.Config, opts.model, opts.provider, opts.providerBaseURL)
	result, err := startup.StartGatewayWithConfig(project.Config, e2eGatewayVersion, startup.Options{
		ConnectTimeout:  2 * time.Second,
		FallbackAgentID: entry.ID,
		LaunchMode:      startup.LaunchModeChat,
		NonInteractive:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, result.CLIChannel())

	t.Cleanup(result.Cleanup)

	return &liveChatHarness{
		t:       t,
		project: project,
		entry:   entry.ID,
		result:  result,
		store:   session.NewStore(filepath.Join(project.Config.RuntimeDataDir(), "sessions")),
		timeout: opts.timeout,
	}
}

func (h *liveChatHarness) injectAndWait(t *testing.T, senderID, text string, expectedUserTurns []string) []exampleSessionItem {
	t.Helper()

	err := h.result.CLIChannel().InjectMessage(gateway.InboundMessage{
		SenderID:   senderID,
		SenderName: "User",
		Text:       text,
		ChatType:   gateway.ChatTypeDirect,
	})
	require.NoError(t, err)

	deadline := time.Now().Add(h.timeout)
	for time.Now().Before(deadline) {
		history := h.sessionHistory(t, senderID)
		if reflect.DeepEqual(sessionMessagesForRole(history, "user"), expectedUserTurns) &&
			len(sessionMessagesForRole(history, "assistant")) >= len(expectedUserTurns) {
			return history
		}
		time.Sleep(200 * time.Millisecond)
	}

	var runIDs []string
	var sessions []session.SessionInfo
	var logs []map[string]any
	if gw := h.result.Gateway(); gw != nil {
		logs = gw.LogEntriesPayload(30)
		if rt := gw.RawRuntime(); rt != nil {
			for _, run := range rt.ListRuns() {
				runIDs = append(runIDs, run.ID+":"+run.SessionID+":"+string(run.Status))
			}
			sessions, _ = rt.ListSessions(h.entry)
		}
	}

	t.Fatalf(
		"timed out waiting for chat history for sender %q after %s; expected=%v runs=%v sessions=%v logs=%v",
		senderID,
		text,
		expectedUserTurns,
		runIDs,
		sessions,
		logs,
	)
	return nil
}

func (h *liveChatHarness) sessionHistory(t *testing.T, senderID string) []exampleSessionItem {
	t.Helper()

	sess, err := h.store.Load(h.entry, headlessCLISessionKey(senderID))
	require.NoError(t, err)

	var history []exampleSessionItem
	for _, entry := range sess.History() {
		if entry.Type != session.EntryTypeMessage {
			continue
		}
		var msg session.MessageData
		require.NoError(t, json.Unmarshal(entry.Data, &msg))
		history = append(history, exampleSessionItem{
			Type: "message",
			Role: entry.Role,
			Text: msg.Text,
		})
	}
	return history
}

func inventoryAgentByID(t *testing.T, inventory agentInventoryResponse, agentID string) agentInventoryAgent {
	t.Helper()

	for _, agent := range inventory.Agents {
		if agent.ID == agentID {
			return agent
		}
	}
	t.Fatalf("agent %q not found in inventory", agentID)
	return agentInventoryAgent{}
}

func validateRunHasMemoryRecall(events []runtimeevents.EventRecord) error {
	for _, event := range events {
		if event.Name != "memory.recalled" {
			continue
		}
		entries, ok := event.Payload["entries"].([]any)
		if ok && len(entries) > 0 {
			return nil
		}
		if entries, ok := event.Payload["entries"].([]map[string]any); ok && len(entries) > 0 {
			return nil
		}
		return fmt.Errorf("memory.recalled had no entries")
	}
	return fmt.Errorf("memory.recalled event not found")
}

func expiredE2EMemoryDoc() string {
	return strings.TrimSpace(`
# Expired E2E Memory

Generated at: 2026-04-18T00:00:00Z

- Managed By: e2e
- Lifecycle: candidate
- Expire At: 2026-04-18T00:00:00Z

## Summary
- this candidate should be cleaned up by the HTTP cleanup endpoint
`) + "\n"
}

func queryEscape(value string) string {
	replacer := strings.NewReplacer(
		"%", "%25",
		" ", "%20",
		"/", "%2F",
		",", "%2C",
		"?", "%3F",
		"&", "%26",
	)
	return replacer.Replace(value)
}

func effectiveSkillNames(agent agentInventoryAgent) []string {
	names := make([]string, 0, len(agent.Skills.Effective))
	for _, skill := range agent.Skills.Effective {
		names = append(names, skill.Name)
	}
	slices.Sort(names)
	return names
}

func headlessCLISessionKey(senderID string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
	)
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		senderID = "local"
	}
	return "cli_" + replacer.Replace(senderID)
}

func cloneExampleProject(t *testing.T, sourceDir string) string {
	t.Helper()

	destDir := filepath.Join(t.TempDir(), filepath.Base(sourceDir))
	err := filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(destDir, relPath)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, 0o644)
	})
	require.NoError(t, err)
	return destDir
}
