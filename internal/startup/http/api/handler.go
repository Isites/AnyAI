package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/startup/http/server"
	"github.com/go-chi/chi/v5"
)

type InventoryPlane interface {
	AgentInventoryPayload() any
	ChannelViewsPayload() any
	CatalogPayload() any
	CatalogEndpointsPayload() any
	OverviewPayload() any
	ListJobs() []gateway.Job
	PauseJob(name string) error
	ResumeJob(name string) error
	RemoveJob(name string) error
	UpdateJobSchedule(name, schedule string) error
}

type RuntimePlane interface {
	RebuildProjections() error
	EventStorageDir() string
	AttachmentBaseDir() string
}

type RunPlane interface {
	RunList() []gateway.Run
	StartAcceptedRun(
		ctx context.Context,
		agentID, text string,
		inputs []gateway.InputBlock,
		sessionID, messageID, channel, senderID, accountID string,
		chatType gateway.ChatType,
		sessionPrefix string,
	) (*gateway.ManagedRun, gateway.Run, error)
	RunRecord(runID string) (gateway.Run, bool)
	RunEvents(runID string) []gateway.Event
	SubscribeRunReplay(runID string) ([]gateway.Event, <-chan gateway.Event, func(), error)
	CancelRun(runID string) error
	RunTreeRecord(runID string) (gateway.RunTree, bool)
	RunTree(runID string) ([]gateway.RunNode, bool)
	SubscribeRunTreeReplay(runID string) ([]gateway.Event, <-chan gateway.Event, func(), error)
}

type SessionPlane interface {
	SessionList(agentID string) ([]gateway.SessionInfo, error)
	SessionCreate(agentID, requestedKey, prefix string) (string, error)
	SessionLoad(agentID, sessionID string) (gateway.SessionView, error)
	SessionEvents(agentID, sessionID string) []gateway.Event
	SubscribeSession(agentID, sessionID string) (<-chan gateway.Event, func(), error)
	SessionDelete(agentID, sessionID string) error
}

type MemoryPlane interface {
	MemoryStats() gateway.MemoryStats
	MemorySearch(query string, maxItems int, scope gateway.MemoryScope, layers ...gateway.MemoryLayer) []gateway.MemorySearchMatch
	MemoryGet(id string, scope gateway.MemoryScope) (gateway.MemoryEntry, bool)
	MemoryStaleCleanup(now time.Time) (int, error)
	MemoryReindex() (int, error)
	MemoryPromoteEligible(now time.Time) (int, error)
}

type LogPlane interface {
	LogEntriesPayload(limit int) []map[string]any
	SubscribeLogs() (<-chan gateway.LogEntry, func())
}

type ConfigPlane interface {
	ConfigSnapshot() *config.Config
	SaveConfig(raw []byte) error
}

type TaskPlane interface {
	TaskList() []gateway.Task
	TaskRecord(taskID string) (gateway.Task, bool)
	TaskEvents(taskID string) []gateway.Event
	SubscribeTask(taskID string) (<-chan gateway.Event, func(), error)
	CancelTask(taskID string) error
}

type Plane interface {
	InventoryPlane
	RuntimePlane
	RunPlane
	SessionPlane
	MemoryPlane
	LogPlane
	ConfigPlane
	TaskPlane
}

type HandlerPlanes struct {
	Inventory InventoryPlane
	Runtime   RuntimePlane
	Run       RunPlane
	Session   SessionPlane
	Memory    MemoryPlane
	Log       LogPlane
	Config    ConfigPlane
	Task      TaskPlane
}

type Handler struct {
	inventory InventoryPlane
	runtime   RuntimePlane
	run       RunPlane
	session   SessionPlane
	memory    MemoryPlane
	log       LogPlane
	config    ConfigPlane
	task      TaskPlane
	metrics   *server.Metrics
}

type createRunRequest struct {
	AgentID   string               `json:"agent_id"`
	Text      string               `json:"text,omitempty"`
	Inputs    []gateway.InputBlock `json:"inputs"`
	SessionID string               `json:"session_id,omitempty"`
	MessageID string               `json:"message_id,omitempty"`
	Stream    bool                 `json:"stream,omitempty"`
}

type createSessionRequest struct {
	Name string `json:"name"`
}

type updateJobScheduleRequest struct {
	Schedule string `json:"schedule"`
}

func NewHandler(plane Plane, metrics *server.Metrics) http.Handler {
	return NewHandlerWithPlanes(HandlerPlanes{
		Inventory: plane,
		Runtime:   plane,
		Run:       plane,
		Session:   plane,
		Memory:    plane,
		Log:       plane,
		Config:    plane,
		Task:      plane,
	}, metrics)
}

func NewHandlerWithPlanes(planes HandlerPlanes, metrics *server.Metrics) http.Handler {
	r := chi.NewRouter()
	h := &Handler{
		inventory: planes.Inventory,
		runtime:   planes.Runtime,
		run:       planes.Run,
		session:   planes.Session,
		memory:    planes.Memory,
		log:       planes.Log,
		config:    planes.Config,
		task:      planes.Task,
		metrics:   metrics,
	}

	r.Get("/agents", h.handleAgents)
	r.Get("/channels", h.handleChannels)
	r.Get("/catalog", h.handleCatalog)
	r.Get("/runtime/overview", h.handleOverview)
	r.Post("/runtime/rebuild-projections", h.handleRuntimeRebuildProjections)
	r.Get("/memory/stats", h.handleMemoryStats)
	r.Get("/memory/search", h.handleMemorySearch)
	r.Get("/memory/item", h.handleMemoryGet)
	r.Post("/memory/stale-cleanup", h.handleMemoryStaleCleanup)
	r.Post("/memory/reindex", h.handleMemoryReindex)
	r.Post("/memory/promote", h.handleMemoryPromote)

	r.Get("/runs", h.handleRuns)
	r.Post("/runs", h.handleRunCreate)
	r.Post("/chat", h.handleChatCreate)
	r.Post("/attachments", h.handleAttachmentUpload)
	r.Get("/runs/{runID}", h.handleRunGet)
	r.Get("/runs/{runID}/events", h.handleRunEvents)
	r.Get("/runs/{runID}/tree", h.handleRunTree)
	r.Get("/runs/{runID}/tree/events", h.handleRunTreeEvents)
	r.Post("/runs/{runID}/cancel", h.handleRunCancel)

	r.Get("/sessions/{agentID}", h.handleSessionList)
	r.Post("/sessions/{agentID}", h.handleSessionCreate)
	r.Get("/sessions/{agentID}/{sessionID}", h.handleSessionGet)
	r.Get("/sessions/{agentID}/{sessionID}/events", h.handleSessionEvents)
	r.Delete("/sessions/{agentID}/{sessionID}", h.handleSessionDelete)

	r.Get("/jobs", h.handleJobList)
	r.Post("/jobs/{jobName}/pause", h.handleJobPause)
	r.Post("/jobs/{jobName}/resume", h.handleJobResume)
	r.Post("/jobs/{jobName}/remove", h.handleJobRemove)
	r.Post("/jobs/{jobName}/schedule", h.handleJobUpdateSchedule)

	r.Get("/tasks", h.handleTaskList)
	r.Get("/tasks/{taskID}", h.handleTaskGet)
	r.Get("/tasks/{taskID}/events", h.handleTaskEvents)
	r.Post("/tasks/{taskID}/cancel", h.handleTaskCancel)

	r.Get("/logs", h.handleLogs)
	r.Get("/logs/stream", h.handleLogsStream)

	r.Get("/config", h.handleConfigGet)
	r.Post("/config", h.handleConfigSave)

	return r
}

func (h *Handler) handleAgents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.inventory.AgentInventoryPayload())
}

func (h *Handler) handleChannels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"channels": h.inventory.ChannelViewsPayload()})
}

func (h *Handler) handleCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"catalog":   h.inventory.CatalogPayload(),
		"endpoints": h.inventory.CatalogEndpointsPayload(),
	})
}

func (h *Handler) handleOverview(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"overview": h.inventory.OverviewPayload()})
}

func (h *Handler) handleRuntimeRebuildProjections(w http.ResponseWriter, _ *http.Request) {
	if err := h.runtime.RebuildProjections(); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not available") {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"runs":       len(h.run.RunList()),
		"events_dir": h.runtime.EventStorageDir(),
		"rebuild_at": time.Now().UTC(),
	})
}

func (h *Handler) handleMemoryStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"stats": h.memory.MemoryStats()})
}

func (h *Handler) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "q is required"})
		return
	}

	maxItems, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("max_items")))
	maxItems = normalizeMemoryMaxResults(maxItems, 5)
	scope := normalizeMemoryScope(gateway.MemoryScope{
		AgentID:   strings.TrimSpace(r.URL.Query().Get("agent_id")),
		SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
	})
	layers := parseMemoryLayers(r.URL.Query().Get("layer"))
	matches := h.memory.MemorySearch(query, maxItems, scope, layers...)
	writeJSON(w, http.StatusOK, map[string]any{"matches": matches})
}

func (h *Handler) handleMemoryGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is required"})
		return
	}
	scope := normalizeMemoryScope(gateway.MemoryScope{
		AgentID:   strings.TrimSpace(r.URL.Query().Get("agent_id")),
		SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
	})
	entry, ok := h.memory.MemoryGet(id, scope)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry": entry})
}

func (h *Handler) handleMemoryStaleCleanup(w http.ResponseWriter, _ *http.Request) {
	removed, err := h.memory.MemoryStaleCleanup(time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "stats": h.memory.MemoryStats()})
}

func (h *Handler) handleMemoryReindex(w http.ResponseWriter, _ *http.Request) {
	total, err := h.memory.MemoryReindex()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reindexed": total, "stats": h.memory.MemoryStats()})
}

func (h *Handler) handleMemoryPromote(w http.ResponseWriter, _ *http.Request) {
	promoted, err := h.memory.MemoryPromoteEligible(time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"promoted": promoted, "stats": h.memory.MemoryStats()})
}

func (h *Handler) handleRuns(w http.ResponseWriter, _ *http.Request) {
	runs := h.run.RunList()
	if len(runs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []gateway.Run{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *Handler) handleRunCreate(w http.ResponseWriter, r *http.Request) {
	h.handleRunSubmission(w, r, "api")
}

func (h *Handler) handleChatCreate(w http.ResponseWriter, r *http.Request) {
	h.handleRunSubmission(w, r, "chat")
}

func (h *Handler) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	if h.runtime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "runtime not available"})
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid multipart upload"})
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		if single := r.MultipartForm.File["file"]; len(single) > 0 {
			files = single
		}
	}
	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "at least one file is required"})
		return
	}

	baseDir := filepath.Join(h.runtime.AttachmentBaseDir(), "uploads")
	store := input.NewAttachmentStore(baseDir)
	blocks := make([]gateway.InputBlock, 0, len(files))
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("open upload: %v", err)})
			return
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("read upload: %v", err)})
			return
		}
		name := filepath.Base(strings.TrimSpace(header.Filename))
		if name == "" || name == "." {
			name = "upload"
		}
		mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
		if mimeType == "" || mimeType == "application/octet-stream" {
			if guessed := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); guessed != "" {
				mimeType = guessed
			}
		}
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		blockType := input.BlockTypeForAttachmentName(name, mimeType)
		att, err := store.Save(name, mimeType, data)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("store upload: %v", err)})
			return
		}
		blocks = append(blocks, gateway.InputBlock{
			ID:           att.ID,
			Type:         blockType,
			Name:         att.Name,
			Path:         att.Path,
			AttachmentID: att.ID,
			MimeType:     att.MimeType,
			Meta: map[string]any{
				"attachment_id": att.ID,
				"size":          att.Size,
				"uploaded":      true,
			},
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"inputs": blocks})
}

func (h *Handler) handleRunSubmission(w http.ResponseWriter, r *http.Request, sessionPrefix string) {
	var req createRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}

	if h.metrics != nil {
		h.metrics.IncLLMCalls()
	}

	run, record, err := h.run.StartAcceptedRun(
		context.Background(),
		req.AgentID,
		req.Text,
		req.Inputs,
		req.SessionID,
		req.MessageID,
		"http",
		"http",
		"http",
		gateway.ChatTypeDirect,
		sessionPrefix,
	)
	if err != nil {
		status := http.StatusBadRequest
		if isProviderUnavailable(err) {
			status = http.StatusServiceUnavailable
		} else if strings.Contains(err.Error(), "runtime not available") {
			status = http.StatusServiceUnavailable
		}
		if h.metrics != nil {
			h.metrics.IncErrors()
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}

	// TODO: 临时不定，后期需要废弃ManagedRun的events，进作为一个句柄，可以操控本次生命周期停止和继续，其余的消费只暴露recorder/replay/subscribe即可
	drainManagedRunEvents(run)
	h.observeRunMetrics(run.RunID)

	if req.Stream || wantsEventStream(r) {
		h.streamAcceptedRun(w, r, run.RunID, map[string]any{"run": record})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"run": record})
}

func drainManagedRunEvents(run *gateway.ManagedRun) {
	if run == nil || run.Events == nil {
		return
	}
	go func() {
		for range run.Events {
		}
	}()
}

func (h *Handler) handleRunGet(w http.ResponseWriter, r *http.Request) {
	run, ok := h.run.RunRecord(chi.URLParam(r, "runID"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (h *Handler) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	events := h.run.RunEvents(runID)
	if len(events) == 0 {
		if _, ok := h.run.RunRecord(runID); !ok {
			http.NotFound(w, r)
			return
		}
	}
	if !wantsEventStream(r) {
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
		return
	}

	h.streamAcceptedRun(w, r, runID, nil)
}

func (h *Handler) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	if err := h.run.CancelRun(chi.URLParam(r, "runID")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) observeRunMetrics(runID string) {
	if h == nil || h.run == nil || strings.TrimSpace(runID) == "" {
		return
	}

	events, ch, cancel, err := h.run.SubscribeRunReplay(runID)
	if err != nil || cancel == nil {
		return
	}

	for _, event := range events {
		h.observeRunMetricEvent(event)
	}
	if ch == nil || (len(events) > 0 && isTerminalEvent(events[len(events)-1])) {
		cancel()
		return
	}

	go func() {
		defer cancel()
		for event := range ch {
			h.observeRunMetricEvent(event)
		}
	}()
}

func (h *Handler) observeRunMetricEvent(event gateway.Event) {
	if h == nil || h.metrics == nil {
		return
	}
	switch event.Name {
	case "tool.call.started":
		if toolName := eventToolName(event); toolName != "" {
			h.metrics.IncToolCalls(toolName)
		}
	case "run.failed":
		h.metrics.IncErrors()
	}
}

func (h *Handler) streamAcceptedRun(w http.ResponseWriter, r *http.Request, runID string, acceptedPayload any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming not supported"})
		return
	}

	events, ch, cancel, err := h.run.SubscribeRunReplay(runID)
	if err != nil || cancel == nil {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "event recorder not available"})
		return
	}
	defer cancel()

	prepareEventStream(w)
	if acceptedPayload != nil {
		if err := writeSSEJSON(w, "run.accepted", acceptedPayload); err != nil {
			return
		}
		flusher.Flush()
	}
	for _, event := range events {
		if err := writeSSEJSON(w, event.Name, event); err != nil {
			return
		}
		flusher.Flush()
	}
	if len(events) > 0 && isTerminalEvent(events[len(events)-1]) {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSEJSON(w, event.Name, event); err != nil {
				return
			}
			flusher.Flush()
			if isTerminalEvent(event) {
				return
			}
		}
	}
}

func (h *Handler) handleSessionList(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.session.SessionList(chi.URLParam(r, "agentID"))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *Handler) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentID")
	var req createSessionRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	key, err := h.session.SessionCreate(agentID, req.Name, "http")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session": map[string]any{
			"agent_id": agentID,
			"id":       key,
		},
	})
}

func (h *Handler) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentID")
	sessionID := chi.URLParam(r, "sessionID")
	sess, err := h.session.SessionLoad(agentID, sessionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session": map[string]any{
			"agent_id": agentID,
			"id":       sessionID,
			"history":  sess.History,
			"events":   h.session.SessionEvents(agentID, sessionID),
		},
	})
}

func (h *Handler) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentID")
	sessionID := chi.URLParam(r, "sessionID")
	events := h.session.SessionEvents(agentID, sessionID)
	if !wantsEventStream(r) {
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming not supported"})
		return
	}

	prepareEventStream(w)
	for _, event := range events {
		if err := writeSSEJSON(w, event.Name, event); err != nil {
			return
		}
		flusher.Flush()
	}

	ch, cancel, err := h.session.SubscribeSession(agentID, sessionID)
	if err != nil || cancel == nil {
		http.NotFound(w, r)
		return
	}
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSEJSON(w, event.Name, event); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentID")
	sessionID := chi.URLParam(r, "sessionID")
	if err := h.session.SessionDelete(agentID, sessionID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleRunTree(w http.ResponseWriter, r *http.Request) {
	tree, ok := h.run.RunTree(chi.URLParam(r, "runID"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tree": tree})
}

func (h *Handler) handleRunTreeEvents(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	tree, ok := h.run.RunTreeRecord(runID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !wantsEventStream(r) {
		writeJSON(w, http.StatusOK, map[string]any{"events": tree.Events})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming not supported"})
		return
	}

	snapshot, ch, cancel, err := h.run.SubscribeRunTreeReplay(runID)
	if err != nil || cancel == nil {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "event recorder not available"})
		return
	}
	defer cancel()

	prepareEventStream(w)
	for _, event := range snapshot {
		if err := writeSSEJSON(w, event.Name, event); err != nil {
			return
		}
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSEJSON(w, event.Name, event); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) handleJobList(w http.ResponseWriter, _ *http.Request) {
	jobs := h.inventory.ListJobs()
	if len(jobs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": []gateway.Job{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (h *Handler) handleJobPause(w http.ResponseWriter, r *http.Request) {
	h.handleJobAction(w, func() error { return h.inventory.PauseJob(chi.URLParam(r, "jobName")) })
}

func (h *Handler) handleJobResume(w http.ResponseWriter, r *http.Request) {
	h.handleJobAction(w, func() error { return h.inventory.ResumeJob(chi.URLParam(r, "jobName")) })
}

func (h *Handler) handleJobRemove(w http.ResponseWriter, r *http.Request) {
	h.handleJobAction(w, func() error { return h.inventory.RemoveJob(chi.URLParam(r, "jobName")) })
}

func (h *Handler) handleJobUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	var req updateJobScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Schedule) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "schedule is required"})
		return
	}
	if err := h.inventory.UpdateJobSchedule(chi.URLParam(r, "jobName"), strings.TrimSpace(req.Schedule)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleJobAction(w http.ResponseWriter, action func() error) {
	if err := action(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	writeJSON(w, http.StatusOK, map[string]any{"entries": h.log.LogEntriesPayload(limit)})
}

func (h *Handler) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming not supported"})
		return
	}
	ch, cancel := h.log.SubscribeLogs()
	if cancel == nil {
		http.NotFound(w, r)
		return
	}
	defer cancel()

	prepareEventStream(w)
	for _, entry := range h.log.LogEntriesPayload(0) {
		if err := writeSSEJSON(w, "log", entry); err != nil {
			return
		}
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			payload := map[string]any{
				"time":    entry.Time.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
				"level":   entry.Level,
				"message": entry.Message,
				"attrs":   entry.Attrs,
			}
			if err := writeSSEJSON(w, "log", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	cfg := h.config.ConfigSnapshot()
	if cfg == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "config not available"})
		return
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "marshal config"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h *Handler) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body"})
		return
	}
	if err := h.config.SaveConfig(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func wantsEventStream(r *http.Request) bool {
	return r.URL.Query().Get("stream") == "1" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func prepareEventStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeSSEJSON(w http.ResponseWriter, eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, data)
	return err
}

func isTerminalStatus(status gateway.RunStatus) bool {
	return status == gateway.RunStatusCompleted || status == gateway.RunStatusFailed || status == gateway.RunStatusAborted
}

func isTerminalEvent(event gateway.Event) bool {
	return event.Name == "run.completed" || event.Name == "run.failed"
}

func parseMemoryLayers(raw string) []gateway.MemoryLayer {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "all") {
		return []gateway.MemoryLayer{gateway.MemoryLayerEpisodic, gateway.MemoryLayerLongTerm, gateway.MemoryLayerCandidates}
	}
	parts := strings.Split(raw, ",")
	layers := make([]gateway.MemoryLayer, 0, len(parts))
	seen := map[gateway.MemoryLayer]struct{}{}
	for _, part := range parts {
		layer := gateway.MemoryLayer(strings.TrimSpace(part))
		switch layer {
		case gateway.MemoryLayerCandidates, gateway.MemoryLayerEpisodic, gateway.MemoryLayerLongTerm:
		default:
			continue
		}
		if _, ok := seen[layer]; ok {
			continue
		}
		seen[layer] = struct{}{}
		layers = append(layers, layer)
	}
	if len(layers) == 0 {
		return []gateway.MemoryLayer{gateway.MemoryLayerEpisodic, gateway.MemoryLayerLongTerm, gateway.MemoryLayerCandidates}
	}
	return layers
}

func normalizeMemoryScope(scope gateway.MemoryScope) gateway.MemoryScope {
	return gateway.MemoryScope{
		AgentID:   strings.TrimSpace(scope.AgentID),
		SessionID: strings.TrimSpace(scope.SessionID),
	}
}

func normalizeMemoryMaxResults(maxResults, fallback int) int {
	if fallback <= 0 {
		fallback = 5
	}
	if maxResults <= 0 {
		return fallback
	}
	return maxResults
}

func isProviderUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "provider") && strings.Contains(msg, "unavailable")
}

func eventToolName(event gateway.Event) string {
	if event.Payload == nil {
		return ""
	}
	for _, key := range []string{"tool", "name", "tool_name"} {
		if value, ok := event.Payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (h *Handler) handleTaskList(w http.ResponseWriter, _ *http.Request) {
	tasks := h.task.TaskList()
	if len(tasks) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"tasks": []gateway.Task{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (h *Handler) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	tk, ok := h.task.TaskRecord(taskID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": tk})
}

func (h *Handler) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	if !wantsEventStream(r) {
		if _, ok := h.task.TaskRecord(taskID); !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": h.task.TaskEvents(taskID)})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming not supported"})
		return
	}

	prepareEventStream(w)

	ch, cancel, err := h.task.SubscribeTask(taskID)
	if err != nil || cancel == nil {
		http.NotFound(w, r)
		return
	}
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSEJSON(w, event.Name, event); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	if err := h.task.CancelTask(chi.URLParam(r, "taskID")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
