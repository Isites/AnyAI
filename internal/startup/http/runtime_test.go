package httpchannel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	airuntime "github.com/Isites/anyai/internal/runtime"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	response         string
	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *mockProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *mockProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "test-model", Name: "Test Model", Provider: "test"}}
}

func (p *mockProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "http channel compact summary"}, nil
}

func testRuntime(t *testing.T) (*Service, *runtimeport.DependencySet, *gateway.ChannelManager) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = 18789
	cfg.Agents.List = []config.AgentConfig{
		{ID: "assistant", Name: "Assistant", Model: "test/model", Workspace: t.TempDir(), Entry: true},
	}

	providers := map[string]llm.LLMProvider{
		"test": &mockProvider{response: "Hello from agent!"},
	}
	deps := runtimeport.NewDependencySet(providers, session.NewStore(t.TempDir()), cfg)
	deps.SetRecorder(runtimeevents.NewRecorder())
	deps.SetTaskStore(task.NewStore())
	memMgr := memory.NewManager(t.TempDir())
	require.NoError(t, memMgr.Load())
	deps.SetMemory(memMgr)
	runtimeService := airuntime.WrapDependencies(deps)
	gatewayService := gateway.New(runtimeService)
	channelManager := gateway.NewChannelManager(gatewayService, "respond")
	gatewayService.SetVersion("test")
	gatewayService.SetChannelManager(channelManager)
	deps.SetSender(channelManager)
	executionDeps := deps.ExecutionDeps()
	resources, err := runtimeresources.BuildCatalog(cfg, runtimeresources.BuildDeps{
		Sender:       executionDeps.Sender,
		AgentRunner:  executionDeps.AgentRunner,
		JobScheduler: executionDeps.JobScheduler,
		Memory:       executionDeps.Memory,
	})
	require.NoError(t, err)
	deps.SetResources(resources)
	deps.SetSkills(resources.GlobalLoader())
	service := NewService(ServiceOptions{
		Config:  cfg,
		Gateway: gatewayService,
	})
	return service, deps, channelManager
}

func TestNewRuntimeBuildsHostedHTTPServiceOutsideChannelInventory(t *testing.T) {
	runtime, _, channelManager := testRuntime(t)

	require.NotNil(t, runtime.Server())
	assert.Empty(t, channelManager.AvailableChannels())
}

func TestNewRuntimeUsesGatewayRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = 18789
	cfg.Agents.List = []config.AgentConfig{
		{ID: "assistant", Name: "Assistant", Model: "test/model", Workspace: t.TempDir(), Entry: true},
	}

	deps := runtimeport.NewDependencySet(map[string]llm.LLMProvider{
		"test": &mockProvider{response: "Hello from agent!"},
	}, session.NewStore(t.TempDir()), cfg)
	runtimeService := airuntime.WrapDependencies(deps)
	gatewayService := gateway.New(runtimeService)
	gatewayService.SetVersion("test")
	runtime := NewService(ServiceOptions{
		Config:  cfg,
		Gateway: gatewayService,
	})

	require.NotNil(t, runtime.plane)
	require.NotNil(t, runtime.plane.run)
	require.NotNil(t, runtime.plane.config)
	assert.Same(t, gatewayService, runtime.plane.run)
	assert.Same(t, runtimeService, gatewayService.RawRuntime())
}

func TestOverviewEndpointIncludesRegisteredChannels(t *testing.T) {
	runtime, _, _ := testRuntime(t)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Overview struct {
			Version  string `json:"version"`
			Channels []struct {
				Name string `json:"name"`
			} `json:"channels"`
		} `json:"overview"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "test", payload.Overview.Version)
	assert.Empty(t, payload.Overview.Channels)
}

func TestRuntimeRebuildProjectionsEndpointUsesRecorderStorage(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = 18789
	cfg.Agents.List = []config.AgentConfig{
		{ID: "assistant", Name: "Assistant", Model: "test/model", Workspace: t.TempDir(), Entry: true},
	}

	recorder, err := runtimeevents.NewPersistentRecorder(t.TempDir())
	require.NoError(t, err)

	deps := runtimeport.NewDependencySet(map[string]llm.LLMProvider{
		"test": &mockProvider{response: "Hello from agent!"},
	}, session.NewStore(t.TempDir()), cfg)
	deps.SetRecorder(recorder)
	deps.SetTaskStore(task.NewStore())

	runtimeService := airuntime.WrapDependencies(deps)
	gatewayService := gateway.New(runtimeService)
	channelManager := gateway.NewChannelManager(gatewayService, "respond")
	gatewayService.SetChannelManager(channelManager)
	runtime := NewService(ServiceOptions{
		Config:  cfg,
		Gateway: gatewayService,
	})

	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Model:     "test/model",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: time.Now().UTC(),
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "run.started",
	})
	recorder.FinishRun("run_1", runtimeevents.RunStatusCompleted, "done", "")

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/rebuild-projections", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"ok"`)
	assert.Contains(t, rec.Body.String(), `"runs":2`)
}

func TestMemoryEndpointsExposeStatsSearchGetAndStaleCleanup(t *testing.T) {
	runtime, surface, _ := testRuntime(t)
	memMgr := surface.Memory()
	require.NotNil(t, memMgr)

	now := time.Now().UTC()
	require.NoError(t, memMgr.SaveToLayer(memory.LayerCandidates, "stale-observation", httpManagedDoc("Stale Observation", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "candidate",
		"Expire At":  now.Add(-time.Hour).Format(time.RFC3339),
	}, "Observed", "stale observation about rollout risk")))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerEpisodic, "current-focus", httpManagedDoc("Current Focus", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "episodic",
		"Expire At":  now.Add(time.Hour).Format(time.RFC3339),
	}, "Summary", "focus on rollout validation and trace clarity")))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerEpisodic, "session-focus", httpManagedDoc("Session Focus", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "episodic",
		"Scope":      "session",
		"Agent":      "lead",
		"Session":    "sess-a",
		"Expire At":  now.Add(time.Hour).Format(time.RFC3339),
	}, "Summary", "Aurora codename remains Polaris in session A")))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "release-rule", httpManagedDoc("Release Rule", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "long-term",
	}, "Rule", "blue green deployment is mandatory for production release")))

	cleanupReq := httptest.NewRequest(http.MethodPost, "/api/memory/stale-cleanup", nil)
	cleanupRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(cleanupRec, cleanupReq)
	require.Equal(t, http.StatusOK, cleanupRec.Code)
	assert.Contains(t, cleanupRec.Body.String(), `"removed":1`)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/memory/stats", nil)
	statsRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code)

	var statsPayload struct {
		Stats struct {
			Total      int `json:"total"`
			Candidates int `json:"candidates"`
			Episodic   int `json:"episodic"`
			LongTerm   int `json:"long_term"`
		} `json:"stats"`
	}
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &statsPayload))
	assert.Equal(t, 3, statsPayload.Stats.Total)
	assert.Equal(t, 0, statsPayload.Stats.Candidates)
	assert.Equal(t, 2, statsPayload.Stats.Episodic)
	assert.Equal(t, 1, statsPayload.Stats.LongTerm)

	searchReq := httptest.NewRequest(http.MethodGet, "/api/memory/search?q=rollout%20validation&layer=episodic", nil)
	searchRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(searchRec, searchReq)
	require.Equal(t, http.StatusOK, searchRec.Code)

	var searchPayload struct {
		Matches []struct {
			Entry struct {
				ID    string       `json:"id"`
				Layer memory.Layer `json:"layer"`
			} `json:"entry"`
			MatchedTerms []string `json:"matched_terms"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal(searchRec.Body.Bytes(), &searchPayload))
	require.Len(t, searchPayload.Matches, 1)
	assert.Equal(t, "episodic/current-focus", searchPayload.Matches[0].Entry.ID)
	assert.Equal(t, memory.LayerEpisodic, searchPayload.Matches[0].Entry.Layer)
	assert.ElementsMatch(t, []string{"rollout", "validation"}, searchPayload.Matches[0].MatchedTerms)

	getReq := httptest.NewRequest(http.MethodGet, "/api/memory/item?id=episodic/current-focus", nil)
	getRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	var getPayload struct {
		Entry struct {
			ID    string       `json:"id"`
			Layer memory.Layer `json:"layer"`
		} `json:"entry"`
	}
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &getPayload))
	assert.Equal(t, "episodic/current-focus", getPayload.Entry.ID)
	assert.Equal(t, memory.LayerEpisodic, getPayload.Entry.Layer)

	scopedSearchReq := httptest.NewRequest(http.MethodGet, "/api/memory/search?q=polaris%20aurora&layer=episodic&agent_id=lead&session_id=sess-a", nil)
	scopedSearchRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(scopedSearchRec, scopedSearchReq)
	require.Equal(t, http.StatusOK, scopedSearchRec.Code)
	require.NoError(t, json.Unmarshal(scopedSearchRec.Body.Bytes(), &searchPayload))
	require.Len(t, searchPayload.Matches, 1)
	assert.Equal(t, "episodic/session-focus", searchPayload.Matches[0].Entry.ID)

	unscopedSearchReq := httptest.NewRequest(http.MethodGet, "/api/memory/search?q=polaris%20aurora&layer=episodic", nil)
	unscopedSearchRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(unscopedSearchRec, unscopedSearchReq)
	require.Equal(t, http.StatusOK, unscopedSearchRec.Code)
	require.NoError(t, json.Unmarshal(unscopedSearchRec.Body.Bytes(), &searchPayload))
	assert.Len(t, searchPayload.Matches, 0)

	scopedGetReq := httptest.NewRequest(http.MethodGet, "/api/memory/item?id=episodic/session-focus&agent_id=lead&session_id=sess-a", nil)
	scopedGetRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(scopedGetRec, scopedGetReq)
	require.Equal(t, http.StatusOK, scopedGetRec.Code)

	unscopedGetReq := httptest.NewRequest(http.MethodGet, "/api/memory/item?id=episodic/session-focus", nil)
	unscopedGetRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(unscopedGetRec, unscopedGetReq)
	require.Equal(t, http.StatusNotFound, unscopedGetRec.Code)
}

func TestMemoryMaintenanceEndpointsExposeReindexAndPromote(t *testing.T) {
	runtime, surface, _ := testRuntime(t)
	memMgr := surface.Memory()
	require.NotNil(t, memMgr)

	now := time.Date(2026, 4, 24, 13, 0, 0, 0, time.UTC)
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "seed", "# Seed\n\nBaseline."))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerEpisodic, "promote-ready", httpManagedDoc("Promote Ready", map[string]string{
		"Managed By":                 "test",
		"Lifecycle":                  "episodic",
		"Recall Count":               "2",
		"Promote After Recall Count": "2",
		"Promotion Status":           "observing",
		"Expire At":                  now.Add(time.Hour).Format(time.RFC3339),
	}, "Summary", "ready to promote")))

	seed, ok := memMgr.Get("long-term/seed")
	require.True(t, ok)
	baseDir := filepath.Dir(filepath.Dir(seed.FilePath))
	externalPath := filepath.Join(baseDir, "long-term", "external.md")
	require.NoError(t, os.WriteFile(externalPath, []byte("# External\n\nReindex should load me."), 0o644))

	reindexReq := httptest.NewRequest(http.MethodPost, "/api/memory/reindex", nil)
	reindexRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(reindexRec, reindexReq)
	require.Equal(t, http.StatusOK, reindexRec.Code)

	var reindexPayload struct {
		Reindexed int `json:"reindexed"`
		Stats     struct {
			LastReindexAt time.Time `json:"last_reindex_at"`
			Total         int       `json:"total"`
		} `json:"stats"`
	}
	require.NoError(t, json.Unmarshal(reindexRec.Body.Bytes(), &reindexPayload))
	assert.Equal(t, 3, reindexPayload.Reindexed)
	assert.Equal(t, 3, reindexPayload.Stats.Total)
	assert.False(t, reindexPayload.Stats.LastReindexAt.IsZero())

	promoteReq := httptest.NewRequest(http.MethodPost, "/api/memory/promote", nil)
	promoteRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(promoteRec, promoteReq)
	require.Equal(t, http.StatusOK, promoteRec.Code)

	var promotePayload struct {
		Promoted int `json:"promoted"`
		Stats    struct {
			LastPromotionAt    time.Time `json:"last_promotion_at"`
			LastPromotionCount int       `json:"last_promotion_count"`
		} `json:"stats"`
	}
	require.NoError(t, json.Unmarshal(promoteRec.Body.Bytes(), &promotePayload))
	assert.Equal(t, 1, promotePayload.Promoted)
	assert.Equal(t, 1, promotePayload.Stats.LastPromotionCount)
	assert.False(t, promotePayload.Stats.LastPromotionAt.IsZero())

	entry, ok := memMgr.Get("long-term/promote-ready")
	require.True(t, ok)
	assert.Equal(t, memory.LayerLongTerm, entry.Layer)
}

func TestOverviewEndpointIncludesMemorySummary(t *testing.T) {
	runtime, surface, _ := testRuntime(t)
	memMgr := surface.Memory()
	require.NotNil(t, memMgr)
	now := time.Date(2026, 4, 24, 14, 0, 0, 0, time.UTC)
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "policy", httpManagedDoc("Policy", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "long-term",
	}, "Rule", "keep interfaces stable")))
	require.NoError(t, memMgr.SaveToLayer(memory.LayerEpisodic, "focus", httpManagedDoc("Focus", map[string]string{
		"Managed By":                 "test",
		"Lifecycle":                  "episodic",
		"Recall Count":               "2",
		"Promote After Recall Count": "2",
		"Promotion Status":           "observing",
		"Expire At":                  now.Add(time.Hour).Format(time.RFC3339),
	}, "Summary", "watch current rollout")))

	policy, ok := memMgr.Get("long-term/policy")
	require.True(t, ok)
	baseDir := filepath.Dir(filepath.Dir(policy.FilePath))
	externalPath := filepath.Join(baseDir, "long-term", "external.md")
	require.NoError(t, os.WriteFile(externalPath, []byte("# External\n\nOverview reindex target."), 0o644))
	_, err := memMgr.Reindex()
	require.NoError(t, err)
	_, err = memMgr.PromoteEligible(now)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Overview struct {
			Counts struct {
				Memory int `json:"memory"`
			} `json:"counts"`
			Memory struct {
				Total              int       `json:"total"`
				Episodic           int       `json:"episodic"`
				LongTerm           int       `json:"long_term"`
				LastReindexAt      time.Time `json:"last_reindex_at"`
				LastPromotionAt    time.Time `json:"last_promotion_at"`
				LastPromotionCount int       `json:"last_promotion_count"`
			} `json:"memory"`
		} `json:"overview"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, 4, payload.Overview.Counts.Memory)
	assert.Equal(t, 4, payload.Overview.Memory.Total)
	assert.Equal(t, 1, payload.Overview.Memory.Episodic)
	assert.Equal(t, 3, payload.Overview.Memory.LongTerm)
	assert.False(t, payload.Overview.Memory.LastReindexAt.IsZero())
	assert.False(t, payload.Overview.Memory.LastPromotionAt.IsZero())
	assert.Equal(t, 1, payload.Overview.Memory.LastPromotionCount)
}

func TestTaskEventsEndpointStreamsLifecycleUpdates(t *testing.T) {
	runtime, surface, _ := testRuntime(t)
	store := surface.TaskStore()
	require.NotNil(t, store)

	tk := store.Create("assistant", "summarize", "session_1", "run_1", task.Contract{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+tk.ID+"/events?stream=1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.Server().Router().ServeHTTP(rec, req)
	}()

	time.Sleep(20 * time.Millisecond)
	store.Complete(tk.ID, task.Result{Status: task.StatusCompleted, Summary: "done"})
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task event stream to exit")
	}

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "task.completed")
	assert.Contains(t, rec.Body.String(), "done")
}

func TestSessionEndpointsCreateListAndHistory(t *testing.T) {
	runtime, surface, _ := testRuntime(t)
	store := surface.SessionStore()
	require.NotNil(t, store)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/assistant", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var created struct {
		Session struct {
			AgentID string `json:"agent_id"`
			ID      string `json:"id"`
		} `json:"session"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, "assistant", created.Session.AgentID)
	assert.Contains(t, created.Session.ID, "http_")

	sess, err := store.Load("assistant", created.Session.ID)
	require.NoError(t, err)
	sess.Append(session.UserMessageEntry("你好"))
	sess.Append(session.AssistantMessageEntry("你好，这里是 AnyAI。"))

	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions/assistant", nil)
	listRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(listRec, listReq)

	require.Equal(t, http.StatusOK, listRec.Code)
	assert.Contains(t, listRec.Body.String(), created.Session.ID)

	getReq := httptest.NewRequest(http.MethodGet, "/api/sessions/assistant/"+created.Session.ID, nil)
	getRec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code)

	var history struct {
		Session struct {
			History []map[string]any `json:"history"`
		} `json:"session"`
	}
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &history))
	require.Len(t, history.Session.History, 2)
	assert.Equal(t, "message", history.Session.History[0]["type"])
	assert.Equal(t, "user", history.Session.History[0]["role"])
	assert.Equal(t, "你好，这里是 AnyAI。", history.Session.History[1]["text"])
}

func TestCatalogEndpointIncludesIntegrationGuide(t *testing.T) {
	runtime, _, _ := testRuntime(t)

	req := httptest.NewRequest(http.MethodGet, "/api/catalog", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Catalog struct {
			Title      string `json:"title"`
			BaseURL    string `json:"base_url"`
			Workflows  []any  `json:"workflows"`
			EventTypes []any  `json:"event_types"`
			Endpoints  []struct {
				Path string `json:"path"`
			} `json:"endpoints"`
		} `json:"catalog"`
		Endpoints []struct {
			Path string `json:"path"`
		} `json:"endpoints"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "AnyAI Gateway HTTP Channel API", payload.Catalog.Title)
	assert.Equal(t, "http://127.0.0.1:18789", payload.Catalog.BaseURL)
	assert.NotEmpty(t, payload.Catalog.Workflows)
	assert.NotEmpty(t, payload.Catalog.EventTypes)
	assert.NotEmpty(t, payload.Catalog.Endpoints)
	assert.NotEmpty(t, payload.Endpoints)
}

func TestPortalServesUnifiedShell(t *testing.T) {
	runtime, _, _ := testRuntime(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/api", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Unified Runtime Deck")
	assert.Contains(t, body, `href="/ui/api"`)
	assert.Contains(t, body, `href="/ui/assets/vendor/github-markdown-light.min.css"`)
	assert.Contains(t, body, `src="/ui/assets/vendor/marked.min.js"`)
	assert.Contains(t, body, `src="/ui/assets/app.js"`)
	assert.NotContains(t, body, "cdn.jsdelivr.net")
}

func TestAgentsEndpointReturnsCapabilityInventory(t *testing.T) {
	runtime, _, _ := testRuntime(t)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Agents []struct {
			ID     string `json:"id"`
			Direct struct {
				Recommended bool `json:"recommended"`
			} `json:"direct_request"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"agents"`
		Notes []string `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Agents, 1)
	assert.Equal(t, "assistant", payload.Agents[0].ID)
	assert.True(t, payload.Agents[0].Direct.Recommended)
	assert.NotEmpty(t, payload.Agents[0].Tools)
	assert.NotEmpty(t, payload.Notes)
}

func TestRunTreeEndpointReturnsHierarchicalTree(t *testing.T) {
	runtime, surface, _ := testRuntime(t)
	recorder := surface.Recorder()
	require.NotNil(t, recorder)

	startedAt := time.Now().UTC()
	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_root",
		AgentID:   "assistant",
		SessionID: "sess_root",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: startedAt,
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_root",
		AgentID:   "assistant",
		SessionID: "sess_root",
		Name:      "run.started",
		Timestamp: startedAt,
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_root",
		AgentID:   "researcher",
		SessionID: "sess_child",
		Name:      "run.started",
		Timestamp: startedAt.Add(time.Second),
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_root",
		AgentID:   "researcher",
		SessionID: "sess_child",
		Name:      "run.completed",
		Timestamp: startedAt.Add(2 * time.Second),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runs/run_root/tree", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Tree []runtimeevents.RunNode `json:"tree"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Tree, 1)
	assert.Equal(t, "run_root", payload.Tree[0].Run.ID)
	require.Empty(t, payload.Tree[0].Children)
	require.Len(t, payload.Tree[0].Events, 3)
	assert.Equal(t, "researcher", payload.Tree[0].Events[1].AgentID)
	assert.Equal(t, "run.completed", payload.Tree[0].Events[2].Name)
}

func TestRunTreeEventsEndpointReplaysSyntheticTextDelta(t *testing.T) {
	runtime, surface, _ := testRuntime(t)
	recorder := surface.Recorder()
	require.NotNil(t, recorder)

	startedAt := time.Now().UTC().Add(-2 * time.Second)
	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_root",
		AgentID:   "assistant",
		SessionID: "sess_root",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: startedAt,
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_root",
		AgentID:   "assistant",
		SessionID: "sess_root",
		Name:      "run.started",
		Timestamp: startedAt,
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_root",
		AgentID:   "assistant",
		SessionID: "sess_root",
		Name:      "run.completed",
		Timestamp: startedAt.Add(time.Second),
	})
	recorder.FinishRun("run_root", runtimeevents.RunStatusCompleted, "hello from trace replay", "")

	req := httptest.NewRequest(http.MethodGet, "/api/runs/run_root/tree/events", nil)
	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Events []runtimeevents.EventRecord `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Events, 3)
	assert.Equal(t, "run.started", payload.Events[0].Name)
	assert.Equal(t, "text.delta", payload.Events[1].Name)
	assert.Equal(t, "hello from trace replay", payload.Events[1].Payload["text"])
	assert.Equal(t, "run.completed", payload.Events[2].Name)
}

func TestChatEndpointStreamsLifecycleFromSinglePOST(t *testing.T) {
	runtime, _, _ := testRuntime(t)

	req := httptest.NewRequest(http.MethodPost, "/api/chat?stream=1", strings.NewReader(`{"agent_id":"assistant","session_id":"demo-session","text":"你好"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	rec := httptest.NewRecorder()
	runtime.Server().Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "event: run.accepted")
	assert.Contains(t, rec.Body.String(), "event: run.started")
	assert.Contains(t, rec.Body.String(), "event: text.delta")
	assert.Contains(t, rec.Body.String(), "Hello from agent!")
}

func httpManagedDoc(title string, meta map[string]string, section string, lines ...string) string {
	var builder strings.Builder
	builder.WriteString("# ")
	builder.WriteString(title)
	builder.WriteString("\n\n")
	builder.WriteString("Generated at: ")
	builder.WriteString(time.Now().UTC().Format(time.RFC3339))
	builder.WriteString("\n\n")
	for key, value := range meta {
		builder.WriteString("- ")
		builder.WriteString(key)
		builder.WriteString(": ")
		builder.WriteString(value)
		builder.WriteString("\n")
	}
	builder.WriteString("\n## ")
	builder.WriteString(section)
	builder.WriteString("\n")
	for _, line := range lines {
		builder.WriteString("- ")
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
}
