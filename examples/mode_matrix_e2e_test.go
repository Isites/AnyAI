package examples

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type liveModeMatrixScenario struct {
	Project      string
	CLITurns     []string
	ChannelTurns []channelWorkflowTurn
}

type liveTurnValidationError struct {
	Turn    int
	Outcome exampleRunOutcome
	Cause   error
}

func (e *liveTurnValidationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"turn %d live delegation: %v\noutput=%q\nevents=%s",
		e.Turn,
		e.Cause,
		strings.TrimSpace(e.Outcome.Run.Output),
		liveEventSummary(e.Outcome.Events),
	)
}

func TestExampleProjectsCLIWebUIAndAPIE2E(t *testing.T) {
	opts := liveE2EOptionsFromEnv(t)

	runs := 0
	for _, scenario := range liveModeMatrixScenarios() {
		scenario := scenario
		if opts.only != "" && scenario.Project != opts.only {
			continue
		}
		runs++

		t.Run(scenario.Project, func(t *testing.T) {
			exampleDir := filepath.Join(opts.root, scenario.Project)

			t.Run("cli", func(t *testing.T) {
				harness := newLiveChatHarness(t, exampleDir, opts)
				runLiveCLIModeScenario(t, harness, scenario)
			})

			t.Run("api", func(t *testing.T) {
				harness := newLiveStartHarness(t, exampleDir, opts)
				runLiveAPIModeScenario(t, harness, scenario)
			})

			t.Run("webui", func(t *testing.T) {
				harness := newLiveStartHarness(t, exampleDir, opts)
				runLiveWebUIModeScenario(t, harness, scenario)
			})
		})
	}

	if runs == 0 {
		t.Fatalf("example project %q not found under %s", opts.only, opts.root)
	}
}

func liveModeMatrixScenarios() []liveModeMatrixScenario {
	index := make(map[string]channelWorkflowScenario)
	for _, scenario := range channelWorkflowScenarios() {
		index[scenario.Project] = scenario
	}

	parallelBase := index["parallel-workflow"]
	parallel := buildLiveModeMatrixScenario(parallelBase)
	parallel.ChannelTurns = []channelWorkflowTurn{
		{
			Prompt:      "请同时覆盖网络资料、本地文档、结构化数据三个角度给我一个研究结论。网络角度只使用 workspace 内预置的离线研究材料，不要联网，也不要先输出分析框架。",
			Steps:       parallelBase.Turns[0].Steps,
			ChildCounts: parallelBase.Turns[0].ChildCounts,
		},
		parallelBase.Turns[1],
		parallelBase.Turns[2],
	}

	ecommerceBase := index["ecommerce-cs"]
	ecommerce := liveModeMatrixScenario{
		Project: "ecommerce-cs",
		CLITurns: []string{
			ecommerceBase.Turns[0].Prompt,
			ecommerceBase.Turns[3].Prompt,
			ecommerceBase.Turns[4].Prompt,
		},
		ChannelTurns: []channelWorkflowTurn{
			ecommerceBase.Turns[0],
			{
				Prompt: ecommerceBase.Turns[3].Prompt,
			},
			ecommerceBase.Turns[4],
		},
	}

	return []liveModeMatrixScenario{
		buildLiveModeMatrixScenario(index["single-agent"]),
		parallel,
		buildLiveModeMatrixScenario(index["runtime-lab"]),
		ecommerce,
	}
}

func buildLiveModeMatrixScenario(base channelWorkflowScenario) liveModeMatrixScenario {
	return liveModeMatrixScenario{
		Project:      base.Project,
		CLITurns:     firstNTextOnlyTurns(base, 3),
		ChannelTurns: firstNChannelTurns(base, 3),
	}
}

func firstNTextOnlyTurns(base channelWorkflowScenario, n int) []string {
	if n <= 0 {
		return nil
	}

	turns := make([]string, 0, n)
	for _, turn := range base.Turns {
		if turn.BuildEnvelope != nil {
			continue
		}
		prompt := strings.TrimSpace(turn.Prompt)
		if prompt == "" {
			continue
		}
		turns = append(turns, prompt)
		if len(turns) == n {
			return turns
		}
	}
	return turns
}

func firstNChannelTurns(base channelWorkflowScenario, n int) []channelWorkflowTurn {
	if n <= 0 || len(base.Turns) == 0 {
		return nil
	}
	if len(base.Turns) < n {
		n = len(base.Turns)
	}

	turns := make([]channelWorkflowTurn, 0, n)
	for i := 0; i < n; i++ {
		turns = append(turns, base.Turns[i])
	}
	return turns
}

func runLiveCLIModeScenario(t *testing.T, harness *liveChatHarness, scenario liveModeMatrixScenario) {
	t.Helper()

	require.Len(t, scenario.CLITurns, 3)

	senderID := scenario.Project + "-cli-live-user"
	expectedTurns := make([]string, 0, len(scenario.CLITurns))
	for _, turn := range scenario.CLITurns {
		expectedTurns = append(expectedTurns, turn)
		harness.injectAndWait(t, senderID, turn, expectedTurns)
	}

	history := harness.sessionHistory(t, senderID)
	if err := validateSessionConversation(history, expectedTurns); err != nil {
		t.Fatalf("cli session continuation: %v", err)
	}
}

func runLiveAPIModeScenario(t *testing.T, harness *liveStartHarness, scenario liveModeMatrixScenario) {
	t.Helper()

	require.Len(t, scenario.ChannelTurns, 3)

	for attempt := 1; attempt <= 2; attempt++ {
		sessionID := fmt.Sprintf("%s-api-live-main-%d", scenario.Project, attempt)
		expectedTurns, err := runMatrixChannelTurns(scenario.ChannelTurns, func(turn channelWorkflowTurn) exampleRunOutcome {
			env := buildChannelTurnEnvelope(turn, harness.project.RootDir)
			return harness.runHTTP(t, "", sessionID, env)
		}, harness.project.RootDir)
		if err != nil {
			if attempt < 2 && shouldRetryFirstTurnFallback(err) {
				continue
			}
			t.Fatal(err)
		}

		sessionState, err := fetchSession(harness.baseURL, harness.entry, sessionID)
		require.NoError(t, err)
		if err := validateSessionConversation(sessionState.Session.History, expectedTurns); err != nil {
			t.Fatalf("api session continuation: %v", err)
		}
		return
	}

	t.Fatalf("api scenario %s exhausted retry budget", scenario.Project)
}

func runLiveWebUIModeScenario(t *testing.T, harness *liveStartHarness, scenario liveModeMatrixScenario) {
	t.Helper()

	require.Len(t, scenario.ChannelTurns, 3)

	harness.assertChatShell(t)
	for attempt := 1; attempt <= 2; attempt++ {
		sessionID := fmt.Sprintf("%s-webui-live-main-%d", scenario.Project, attempt)
		expectedTurns, err := runMatrixChannelTurns(scenario.ChannelTurns, func(turn channelWorkflowTurn) exampleRunOutcome {
			env := buildChannelTurnEnvelope(turn, harness.project.RootDir)
			return harness.runHTTP(t, "", sessionID, env)
		}, harness.project.RootDir)
		if err != nil {
			if attempt < 2 && shouldRetryFirstTurnFallback(err) {
				continue
			}
			t.Fatal(err)
		}

		sessionState, err := fetchSession(harness.baseURL, harness.entry, sessionID)
		require.NoError(t, err)
		if err := validateSessionConversation(sessionState.Session.History, expectedTurns); err != nil {
			t.Fatalf("webui session continuation: %v", err)
		}
		return
	}

	t.Fatalf("webui scenario %s exhausted retry budget", scenario.Project)
}

func runMatrixChannelTurns(
	turns []channelWorkflowTurn,
	runner func(turn channelWorkflowTurn) exampleRunOutcome,
	projectDir string,
) ([]string, error) {
	var expectedTurns []string
	for idx, turn := range turns {
		outcome := runner(turn)
		expectedTurns = append(expectedTurns, resolvedEnvelopeText(buildChannelTurnEnvelope(turn, projectDir)))

		if strings.TrimSpace(outcome.Run.Output) == "" {
			return expectedTurns, &liveTurnValidationError{
				Turn:    idx + 1,
				Outcome: outcome,
				Cause:   fmt.Errorf("turn %d produced empty output", idx+1),
			}
		}
		if len(turn.Steps) == 0 {
			continue
		}
		if err := validateLiveDelegation(outcome, turn); err != nil {
			return expectedTurns, &liveTurnValidationError{
				Turn:    idx + 1,
				Outcome: outcome,
				Cause:   err,
			}
		}
	}
	return expectedTurns, nil
}

func shouldRetryFirstTurnFallback(err error) bool {
	var turnErr *liveTurnValidationError
	if !errors.As(err, &turnErr) {
		return false
	}
	if turnErr.Turn != 1 {
		return false
	}
	return strings.TrimSpace(turnErr.Outcome.Run.Output) == "本轮没有生成可见答复，请重试。"
}

func (h *liveStartHarness) assertChatShell(t *testing.T) {
	t.Helper()

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(strings.TrimRight(h.baseURL, "/") + "/chat")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	shell := string(body)
	require.Contains(t, shell, "/ui/assets/app.js")
	require.Contains(t, shell, "data-page=\"chat\"")
}
