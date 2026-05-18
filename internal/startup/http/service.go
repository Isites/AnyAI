package httpchannel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	httpapi "github.com/Isites/anyai/internal/startup/http/api"
	httpserver "github.com/Isites/anyai/internal/startup/http/server"
	httpui "github.com/Isites/anyai/internal/startup/http/ui"
)

// ServiceOptions describes how to assemble the unified HTTP channel service.
type ServiceOptions struct {
	Config  *config.Config
	Gateway *gateway.Service
}

// Service bundles the HTTP API, metrics, and UI.
type Service struct {
	server  *httpserver.Server
	metrics *httpserver.Metrics
	plane   *ControlPlane
}

// NewService assembles the authoritative HTTP channel service used by startup.
func NewService(opts ServiceOptions) *Service {
	metrics := httpserver.NewMetrics()
	plane := &ControlPlane{
		inventory: opts.Gateway,
		runtime:   opts.Gateway,
		run:       opts.Gateway,
		ingress:   opts.Gateway,
		session:   opts.Gateway,
		memory:    opts.Gateway,
		logs:      opts.Gateway,
		configCtl: opts.Gateway,
		task:      opts.Gateway,
	}

	apiHandler := httpapi.NewHandlerWithPlanes(httpapi.HandlerPlanes{
		Inventory: plane,
		Runtime:   plane,
		Run:       plane,
		Session:   plane,
		Memory:    plane,
		Log:       plane,
		Config:    plane,
		Task:      plane,
	}, metrics)

	portal := httpui.NewPortal(plane, embeddedUIAssets())
	cfg := opts.Config
	if cfg == nil && opts.Gateway != nil {
		cfg = opts.Gateway.ConfigSnapshot()
	}
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	server := httpserver.NewServer(cfg.Gateway.Host, cfg.Gateway.Port, httpserver.ServerOptions{
		AuthToken: cfg.Gateway.Auth.Token,
		Metrics:   metrics,
		Modules: []httpserver.Module{
			httpserver.Mount("/api", apiHandler),
			httpserver.Get("/metrics", httpserver.NewMetricsHandler(metrics)),
			portal,
		},
	})

	return &Service{
		server:  server,
		metrics: metrics,
		plane:   plane,
	}
}

func (r *Service) Server() *httpserver.Server   { return r.server }
func (r *Service) Metrics() *httpserver.Metrics { return r.metrics }
func (r *Service) Serve() error                 { return r.server.Start() }
func (r *Service) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.server.Shutdown(ctx)
}

// ControlPlane adapts gateway facades into the narrower HTTP API/UI planes.
type ControlPlane struct {
	inventory inventorySurface
	runtime   runtimeSurface
	run       runSurface
	ingress   ingressSurface
	session   sessionSurface
	memory    memorySurface
	logs      logSurface
	configCtl configSurface
	task      taskSurface
}

type channelView struct {
	Name        string                `json:"name"`
	Status      gateway.ChannelStatus `json:"status"`
	Category    string                `json:"category"`
	Description string                `json:"description"`
}

type countsView struct {
	Agents      int `json:"agents"`
	Channels    int `json:"channels"`
	Runs        int `json:"runs"`
	RunningRuns int `json:"running_runs"`
	FailedRuns  int `json:"failed_runs"`
	Jobs        int `json:"jobs"`
	Logs        int `json:"logs"`
	Memory      int `json:"memory"`
}

type memoryOverviewView struct {
	Enabled            bool      `json:"enabled"`
	Dir                string    `json:"dir,omitempty"`
	Total              int       `json:"total"`
	Episodic           int       `json:"episodic"`
	Candidates         int       `json:"candidates"`
	LongTerm           int       `json:"long_term"`
	LastReindexAt      time.Time `json:"last_reindex_at,omitempty"`
	LastPromotionAt    time.Time `json:"last_promotion_at,omitempty"`
	LastPromotionCount int       `json:"last_promotion_count,omitempty"`
	LastCleanupAt      time.Time `json:"last_cleanup_at,omitempty"`
	LastCleanupRemoved int       `json:"last_cleanup_removed,omitempty"`
}

type overviewView struct {
	ProjectName string               `json:"project_name,omitempty"`
	Version     string               `json:"version"`
	GeneratedAt time.Time            `json:"generated_at"`
	Gateway     map[string]any       `json:"gateway"`
	Channels    []channelView        `json:"channels"`
	Counts      countsView           `json:"counts"`
	Memory      memoryOverviewView   `json:"memory"`
	Agents      []config.AgentConfig `json:"agents"`
	RecentRuns  []gateway.Run        `json:"recent_runs"`
	Jobs        []map[string]any     `json:"jobs"`
}

type endpointDoc struct {
	Method          string     `json:"method"`
	Path            string     `json:"path"`
	Group           string     `json:"group"`
	Transport       string     `json:"transport"`
	Summary         string     `json:"summary"`
	Description     string     `json:"description"`
	QueryFields     []fieldDoc `json:"query_fields,omitempty"`
	RequestFields   []fieldDoc `json:"request_fields,omitempty"`
	ResponseFields  []fieldDoc `json:"response_fields,omitempty"`
	Example         any        `json:"example,omitempty"`
	ResponseExample any        `json:"response_example,omitempty"`
	Events          []string   `json:"events,omitempty"`
	Notes           []string   `json:"notes,omitempty"`
	Curl            string     `json:"curl,omitempty"`
}

type fieldDoc struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description"`
}

type authDoc struct {
	Required    bool   `json:"required"`
	Header      string `json:"header,omitempty"`
	QueryParam  string `json:"query_param,omitempty"`
	Description string `json:"description"`
}

type transportDoc struct {
	Name        string   `json:"name"`
	Summary     string   `json:"summary"`
	BestFor     []string `json:"best_for,omitempty"`
	Entrypoints []string `json:"entrypoints,omitempty"`
}

type workflowStepDoc struct {
	Title       string `json:"title"`
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Description string `json:"description"`
}

type workflowDoc struct {
	Name    string            `json:"name"`
	Summary string            `json:"summary"`
	Outcome string            `json:"outcome,omitempty"`
	Steps   []workflowStepDoc `json:"steps"`
}

type eventTypeDoc struct {
	Name        string     `json:"name"`
	Channel     string     `json:"channel"`
	Summary     string     `json:"summary"`
	Terminal    bool       `json:"terminal,omitempty"`
	Fields      []fieldDoc `json:"fields,omitempty"`
	TriggeredBy string     `json:"triggered_by,omitempty"`
}

type schemaDoc struct {
	Name        string     `json:"name"`
	Summary     string     `json:"summary"`
	Fields      []fieldDoc `json:"fields"`
	Example     any        `json:"example,omitempty"`
	Description string     `json:"description,omitempty"`
}

type catalogView struct {
	Title       string         `json:"title"`
	Overview    string         `json:"overview"`
	BaseURL     string         `json:"base_url"`
	GeneratedAt time.Time      `json:"generated_at"`
	Auth        authDoc        `json:"auth"`
	Transports  []transportDoc `json:"transports,omitempty"`
	Workflows   []workflowDoc  `json:"workflows,omitempty"`
	Endpoints   []endpointDoc  `json:"endpoints"`
	EventTypes  []eventTypeDoc `json:"event_types,omitempty"`
	Schemas     []schemaDoc    `json:"schemas,omitempty"`
	Notes       []string       `json:"notes,omitempty"`
}

func (p *ControlPlane) config() *config.Config {
	if p == nil || p.configCtl == nil {
		return nil
	}
	return p.configCtl.ConfigSnapshot()
}

func (p *ControlPlane) overview() overviewView {
	cfg := p.config()
	version := ""
	if p != nil && p.inventory != nil {
		version = p.inventory.Version()
	}
	overview := overviewView{
		Version:     version,
		GeneratedAt: time.Now().UTC(),
		Channels:    p.channels(),
		Jobs:        p.jobs(),
	}
	if cfg == nil {
		return overview
	}
	overview.Gateway = map[string]any{
		"host":          cfg.Gateway.Host,
		"port":          cfg.Gateway.Port,
		"auth_required": strings.TrimSpace(cfg.Gateway.Auth.Token) != "",
	}
	overview.Agents = append([]config.AgentConfig(nil), cfg.Agents.List...)
	if cfg.ProjectName != "" {
		overview.ProjectName = cfg.ProjectName
	}

	overview.Counts.Agents = len(overview.Agents)
	overview.Counts.Channels = len(overview.Channels)
	overview.Counts.Jobs = len(overview.Jobs)
	overview.Memory.Enabled = cfg.Memory.Enabled
	overview.Memory.Dir = strings.TrimSpace(cfg.Memory.Dir)

	overview.Counts.Logs = len(p.logEntries(0))

	stats := p.MemoryStats()
	overview.Counts.Memory = stats.Total
	overview.Memory.Total = stats.Total
	overview.Memory.Episodic = stats.Episodic
	overview.Memory.Candidates = stats.Candidates
	overview.Memory.LongTerm = stats.LongTerm
	overview.Memory.LastReindexAt = stats.LastReindexAt
	overview.Memory.LastPromotionAt = stats.LastPromotionAt
	overview.Memory.LastPromotionCount = stats.LastPromotionCount
	overview.Memory.LastCleanupAt = stats.LastCleanupAt
	overview.Memory.LastCleanupRemoved = stats.LastCleanupRemoved

	if runs := p.listRuns(); len(runs) > 0 {
		overview.Counts.Runs = len(runs)
		for _, run := range runs {
			switch run.Status {
			case gateway.RunStatusRunning, gateway.RunStatusQueued:
				overview.Counts.RunningRuns++
			case gateway.RunStatusFailed, gateway.RunStatusAborted:
				overview.Counts.FailedRuns++
			}
		}
		if len(runs) > 8 {
			runs = runs[:8]
		}
		overview.RecentRuns = runs
	}

	return overview
}

func (p *ControlPlane) channels() []channelView {
	category := map[string]struct {
		name string
		desc string
	}{
		"http":     {name: "Channel", desc: "HTTP channel exposing gateway REST API and SSE ingress."},
		"cli":      {name: "Interactive", desc: "Local terminal interface backed by Bubble Tea."},
		"telegram": {name: "Messaging", desc: "Telegram bot ingress for direct and group conversations."},
		"whatsapp": {name: "Messaging", desc: "WhatsApp bridge for mobile-centric interactions."},
	}

	var channels []gateway.ChannelInfo
	if p != nil && p.inventory != nil {
		channels = p.inventory.Channels()
	}
	views := make([]channelView, 0, len(channels))
	for _, ch := range channels {
		meta := category[ch.Name()]
		if meta.name == "" {
			meta.name = "Custom"
			meta.desc = "Registered runtime channel."
		}
		views = append(views, channelView{
			Name:        ch.Name(),
			Status:      ch.Status(),
			Category:    meta.name,
			Description: meta.desc,
		})
	}
	return views
}

func (p *ControlPlane) jobs() []map[string]any {
	if p == nil || p.inventory == nil {
		return nil
	}
	jobs := p.inventory.ListJobs()
	result := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, map[string]any{
			"name":     job.Name,
			"schedule": job.Schedule,
			"prompt":   job.Prompt,
			"paused":   job.Paused,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprint(result[i]["name"]) < fmt.Sprint(result[j]["name"])
	})
	return result
}

func (p *ControlPlane) logEntries(limit int) []map[string]any {
	if p == nil || p.logs == nil {
		return nil
	}
	return p.logs.LogEntriesPayload(limit)
}

func (p *ControlPlane) runtimeBaseURL() string {
	host := "127.0.0.1"
	port := 18789
	if cfg := p.config(); cfg != nil {
		if value := strings.TrimSpace(cfg.Gateway.Host); value != "" && value != "0.0.0.0" {
			host = value
		}
		if cfg.Gateway.Port > 0 {
			port = cfg.Gateway.Port
		}
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func (p *ControlPlane) catalog() catalogView {
	baseURL := p.runtimeBaseURL()
	authRequired := false
	if cfg := p.config(); cfg != nil {
		authRequired = strings.TrimSpace(cfg.Gateway.Auth.Token) != ""
	}

	return catalogView{
		Title:       "AnyAI Gateway HTTP Channel API",
		Overview:    "同一套入口覆盖 HTTP 请求与 SSE 流式追踪，适合浏览器控制台、自动化脚本和其他 agent 框架接入。",
		BaseURL:     baseURL,
		GeneratedAt: time.Now().UTC(),
		Auth: authDoc{
			Required:    authRequired,
			Header:      "Authorization: Bearer <token>",
			QueryParam:  "token",
			Description: "除 /health 外，其余 HTTP 与 SSE 路由都会复用同一套 Bearer 鉴权；浏览器 EventSource 无法自定义头时，可在查询参数中附带 token。",
		},
		Transports: []transportDoc{
			{
				Name:        "HTTP",
				Summary:     "创建运行、直接拿流式输出、读取会话、获取总览和管理任务的主控制面。",
				BestFor:     []string{"服务到服务调用", "脚本化集成", "API 网关转发"},
				Entrypoints: []string{"/api/chat", "/api/runs", "/api/sessions/{agentID}", "/api/runtime/overview"},
			},
			{
				Name:        "SSE",
				Summary:     "在保持简单 HTTP 语义的前提下获得增量输出与状态流。",
				BestFor:     []string{"浏览器实时输出", "run/task/run tree 长连接追踪", "无状态后端回放"},
				Entrypoints: []string{"/api/runs/{runID}/events?stream=1", "/api/tasks/{taskID}/events?stream=1", "/api/logs/stream"},
			},
		},
		Workflows: []workflowDoc{
			{
				Name:    "标准会话聊天",
				Summary: "先显式创建 session，再通过单次 POST 同时提交消息并获得 SSE 流，最后回读会话历史。",
				Outcome: "拿到稳定的会话 ID、增量输出、run 元数据和最终历史，便于 UI 持续对话。",
				Steps: []workflowStepDoc{
					{Title: "创建或复用会话", Method: "POST", Path: "/api/sessions/{agentID}", Description: "为用户建立一个稳定 session ID；如果已经有 session_id，可直接跳过。"},
					{Title: "提交消息并直接拿流", Method: "POST", Path: "/api/chat?stream=1", Description: "传入 agent_id、session_id 与 inputs/text，响应体会先发 run.accepted，再持续推送 text.delta、tool.call.started、agent.call.started 等事件。"},
					{Title: "回读会话历史", Method: "GET", Path: "/api/sessions/{agentID}/{sessionID}", Description: "在 run 结束后读取归档历史，避免只依赖临时前端状态。"},
				},
			},
			{
				Name:    "跨 Agent 任务观测",
				Summary: "当运行中触发 callagent 工具时，可以继续追踪 task 与 run tree，构建更完整的内部执行状态视图。",
				Outcome: "工程师可以同时看到主对话、后台子 Agent 调用任务和整棵 run tree 的层级结构。",
				Steps: []workflowStepDoc{
					{Title: "监听 run 事件", Method: "GET", Path: "/api/runs/{runID}/events?stream=1", Description: "捕获 agent.call.started / agent.call.completed / agent.call.failed / agent.fanout.completed，以及 tool.call.started / tool.completed / tool.failed / tool.fanout.completed。"},
					{Title: "查看任务列表", Method: "GET", Path: "/api/tasks", Description: "通过 run_id 与 session_id 关联到后台任务，观察同一次运行中的委派与工具执行。"},
					{Title: "订阅单任务状态", Method: "GET", Path: "/api/tasks/{taskID}/events?stream=1", Description: "实时获取 task.started、task.running、task.completed、task.failed、task.cancelled。"},
					{Title: "查看整棵 run tree", Method: "GET", Path: "/api/runs/{runID}/tree", Description: "用于画出主 agent 与子 agent 之间的父子运行关系。"},
				},
			},
			{
				Name:    "外部 Agent 框架接入",
				Summary: "如果对端已经有自己的前端和状态机，可以只把 AnyAI 视为统一运行时入口。",
				Outcome: "使用最少的接口完成输入、回流输出、恢复历史和观察工具/委派状态。",
				Steps: []workflowStepDoc{
					{Title: "拉取运行时能力", Method: "GET", Path: "/api/catalog", Description: "读取 transports、schemas、workflows 与事件契约，自动生成接入配置。"},
					{Title: "发现可用 agent", Method: "GET", Path: "/api/agents", Description: "读取 agent、有效工具、共享/私有技能以及是否适合作为直连入口。"},
					{Title: "建立 session 与直接流式交互", Method: "POST", Path: "/api/sessions/{agentID} + /api/chat?stream=1", Description: "按你自己的用户 ID 映射 session_id，再通过单一 POST 拿到 run.accepted 与后续事件流。"},
					{Title: "按需拆分提交与订阅", Method: "GET", Path: "/api/runs/{runID}/events?stream=1", Description: "默认优先用 HTTP+SSE；需要断线恢复或独立订阅时，再把创建 run 与订阅 replay 拆开。"},
				},
			},
		},
		Endpoints: []endpointDoc{
			{
				Method:      "POST",
				Path:        "/api/chat",
				Group:       "Runs",
				Transport:   "HTTP / SSE",
				Summary:     "推荐的单入口聊天与运行接口",
				Description: "同一条 POST 既可以创建 run，也可以在 `stream=1` 或 `Accept: text/event-stream` 时直接返回 SSE 流。适合浏览器聊天台、服务端代理和其他 agent 框架接入。",
				QueryFields: []fieldDoc{
					{Name: "stream", Type: "string", Description: "设置为 1 时，响应使用 text/event-stream 并先发 run.accepted。"},
					{Name: "token", Type: "string", Description: "当启用鉴权且客户端无法设置 Authorization 头时可附带 token。"},
				},
				RequestFields: []fieldDoc{
					{Name: "agent_id", Type: "string", Description: "目标 agent ID。可为空，此时按入口 agent / 路由默认规则解析。"},
					{Name: "session_id", Type: "string", Description: "建议显式传入，便于持续会话和历史恢复。"},
					{Name: "message_id", Type: "string", Description: "可选的客户端消息 ID；用于 session/replay 去重与恢复。"},
					{Name: "text", Type: "string", Description: "纯文本场景可直接传 text；与 inputs 二选一即可。"},
					{Name: "inputs", Type: "[]InputBlock", Description: "多模态、文件或结构化输入时使用；上传附件先调用 /api/attachments，再提交返回的引用块。"},
				},
				ResponseFields: []fieldDoc{
					{Name: "run.id", Type: "string", Description: "非流式模式下返回的运行 ID。"},
					{Name: "run.session_id", Type: "string", Description: "服务端实际关联的 session。"},
				},
				Example: map[string]any{
					"agent_id":   "assistant",
					"session_id": "demo-session",
					"message_id": "msg_001",
					"text":       "请概述当前项目的 HTTP 架构",
				},
				ResponseExample: map[string]any{
					"run": map[string]any{
						"id":         "run_123",
						"agent_id":   "assistant",
						"session_id": "demo-session",
						"status":     "running",
					},
				},
				Events: []string{"run.accepted", "run.started", "text.delta", "tool.call.started", "agent.call.started", "run.completed", "run.failed"},
				Notes: []string{
					"推荐作为默认聊天入口；如果你需要显式拿到 run_id 再分离提交与订阅，可以改用 /api/runs。",
					"如果显式指定了非入口 agent_id，请注意它会使用自己的 session 命名空间，而不是入口 agent 的上下文。",
				},
				Curl: `curl -N -X POST 'http://127.0.0.1:18789/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"assistant","session_id":"demo-session","message_id":"msg_001","text":"你好"}'`,
			},
			{
				Method:      "POST",
				Path:        "/api/attachments",
				Group:       "Inputs",
				Transport:   "HTTP",
				Summary:     "上传附件并返回可复用输入块",
				Description: "multipart/form-data 上传文件到项目 anyai/assets/uploads 下，响应里的 inputs 可直接传给 /api/chat 或 /api/runs；图片和 PDF 会按 MIME/扩展名自动标记为 image/pdf。",
				RequestFields: []fieldDoc{
					{Name: "files", Type: "multipart file[]", Description: "一个或多个文件。"},
					{Name: "file", Type: "multipart file", Description: "单文件兼容字段。"},
				},
				ResponseFields: []fieldDoc{
					{Name: "inputs[].type", Type: "string", Description: "file/image/pdf。"},
					{Name: "inputs[].attachment_id", Type: "string", Description: "持久化附件 ID。"},
					{Name: "inputs[].path", Type: "string", Description: "服务端 assets 目录中的文件路径引用。"},
					{Name: "inputs[].mime_type", Type: "string", Description: "附件 MIME 类型。"},
				},
				Example: map[string]any{
					"multipart": "files=@diagram.png",
				},
				ResponseExample: map[string]any{
					"inputs": []map[string]any{{
						"type":          "image",
						"name":          "diagram.png",
						"attachment_id": "att_123",
						"path":          "/project/anyai/assets/uploads/att_123/diagram.png",
						"mime_type":     "image/png",
					}},
				},
				Curl: `curl -F 'files=@diagram.png' http://127.0.0.1:18789/api/attachments`,
			},
			{
				Method:      "POST",
				Path:        "/api/runs",
				Group:       "Runs",
				Transport:   "HTTP",
				Summary:     "结构化运行入口",
				Description: "提交结构化输入块后，会立即返回 run 记录；若附带 `stream=1`，也可以直接返回 SSE。适合调用方显式管理 run_id 与后续订阅链路。",
				RequestFields: []fieldDoc{
					{Name: "agent_id", Type: "string", Description: "目标 agent ID。可为空，此时按入口 agent / 路由默认规则解析。"},
					{Name: "session_id", Type: "string", Description: "建议由调用方显式传入，便于持续会话和历史恢复。"},
					{Name: "message_id", Type: "string", Description: "可选的客户端消息 ID；用于 session/replay 去重与恢复。"},
					{Name: "inputs", Type: "[]InputBlock", Required: true, Description: "至少一个合法输入块，常见为 text、file、dir、image、pdf、url。"},
				},
				ResponseFields: []fieldDoc{
					{Name: "run.id", Type: "string", Description: "本次运行 ID。"},
					{Name: "run.session_id", Type: "string", Description: "服务端实际关联的 session。"},
					{Name: "run.status", Type: "string", Description: "初始通常为 running。"},
				},
				Example: map[string]any{
					"agent_id":   "assistant",
					"session_id": "demo-session",
					"inputs":     []map[string]any{{"type": "text", "text": "请概述当前项目的 HTTP 架构"}},
				},
				ResponseExample: map[string]any{
					"run": map[string]any{
						"id":         "run_123",
						"agent_id":   "assistant",
						"session_id": "demo-session",
						"status":     "running",
					},
				},
				Events: []string{"run.started", "text.delta", "tool.call.started", "agent.call.started", "run.completed", "run.failed"},
				Notes: []string{
					"如果还在使用两段式接入，发起后建议立即订阅 /api/runs/{runID}/events?stream=1，而不是轮询输出文本。",
					"如果你在外部框架中维护用户线程，可把线程 ID 直接映射为 session_id。",
				},
				Curl: `curl -X POST http://127.0.0.1:18789/api/runs \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"assistant","session_id":"demo-session","inputs":[{"type":"text","text":"你好"}]}'`,
			},
			{
				Method:      "GET",
				Path:        "/api/runs",
				Group:       "Runs",
				Transport:   "HTTP",
				Summary:     "列出最近运行",
				Description: "返回当前 recorder 中的全部运行，按 started_at 倒序排序，适合工作台列表和故障排查页。",
				ResponseFields: []fieldDoc{
					{Name: "runs[].id", Type: "string", Description: "运行 ID。"},
					{Name: "runs[].agent_id", Type: "string", Description: "执行的 agent。"},
					{Name: "runs[].status", Type: "string", Description: "queued/running/completed/failed/aborted。"},
					{Name: "runs[].input", Type: "string", Description: "输入摘要；适合列表页快速预览。"},
				},
			},
			{
				Method:      "GET",
				Path:        "/api/runs/{runID}",
				Group:       "Runs",
				Transport:   "HTTP",
				Summary:     "读取单次运行详情",
				Description: "适合恢复页面时补齐 run 元数据，或在事件流之外读取最终 output / error。",
				ResponseFields: []fieldDoc{
					{Name: "run.output", Type: "string", Description: "最终输出文本。"},
					{Name: "run.error", Type: "string", Description: "失败时的错误信息。"},
				},
			},
			{
				Method:      "GET",
				Path:        "/api/runs/{runID}/events?stream=1",
				Group:       "Runs",
				Transport:   "SSE",
				Summary:     "实时订阅某次运行事件",
				Description: "以 text/event-stream 推送增量输出、工具调用、委派和结束信号。适合断线恢复、只拿回放，或仍使用两段式 run 接入的调用方。",
				QueryFields: []fieldDoc{
					{Name: "stream", Type: "string", Description: "设置为 1 开启持续 SSE；否则返回 JSON 快照。"},
					{Name: "token", Type: "string", Description: "当启用鉴权且 EventSource 无法携带 Authorization 头时可附带 Bearer token。"},
				},
				ResponseFields: []fieldDoc{
					{Name: "event", Type: "string", Description: "事件名，例如 text.delta。"},
					{Name: "data", Type: "EventRecord JSON", Description: "包含 run_id、agent_id、timestamp 与 payload。"},
				},
				Events: []string{"run.started", "text.delta", "tool.call.started", "tool.completed", "tool.failed", "tool.fanout.completed", "agent.call.started", "agent.call.completed", "agent.call.failed", "agent.fanout.completed", "run.completed", "run.failed"},
				Notes: []string{
					"同一个连接会先回放已有事件，再继续推送未来事件。",
					"收到 run.completed 或 run.failed 后，服务端会结束该 SSE 连接。",
				},
			},
			{
				Method:      "GET",
				Path:        "/api/runtime/overview",
				Group:       "Runtime",
				Transport:   "HTTP",
				Summary:     "获取统一运行态总览",
				Description: "返回项目、通道、运行、任务、memory、日志数量和最近活动，是仪表盘首页与健康概览页的聚合接口。",
			},
			{
				Method:      "GET",
				Path:        "/api/memory/stats",
				Group:       "Memory",
				Transport:   "HTTP",
				Summary:     "读取 memory 分层统计",
				Description: "返回 candidates、episodic、long-term 总量，以及最近一次 stale cleanup、reindex、promotion 的维护结果。",
			},
			{
				Method:      "GET",
				Path:        "/api/memory/search",
				Group:       "Memory",
				Transport:   "HTTP",
				Summary:     "搜索 memory 并附带 explainability",
				Description: "按 query 搜索 memory，返回 layer、matched_terms、metadata 和 score，适合外部 agent 工程师调试召回质量。",
				QueryFields: []fieldDoc{
					{Name: "q", Type: "string", Description: "搜索关键词。"},
					{Name: "max_items", Type: "int", Description: "返回条目数，默认 5。"},
					{Name: "layer", Type: "string", Description: "可选；candidates、episodic、long-term，支持逗号分隔或 all。"},
					{Name: "agent_id", Type: "string", Description: "可选；按 agent scope 过滤 session-scoped memory。"},
					{Name: "session_id", Type: "string", Description: "可选；按 session scope 过滤 lifecycle memory，避免跨会话串味。"},
				},
			},
			{
				Method:      "GET",
				Path:        "/api/memory/item?id={memoryID}",
				Group:       "Memory",
				Transport:   "HTTP",
				Summary:     "读取单条 memory",
				Description: "通过 query 参数读取单条 memory；适合包含斜杠 ID 的场景，例如 candidates/decision-xxx。对于 session-scoped memory，可额外带 agent_id 和 session_id 做访问控制。",
			},
			{
				Method:      "POST",
				Path:        "/api/memory/stale-cleanup",
				Group:       "Memory",
				Transport:   "HTTP",
				Summary:     "执行一次 stale cleanup",
				Description: "主动清理过期 candidates / episodic 条目，并返回本次移除数量与最新统计。",
			},
			{
				Method:      "POST",
				Path:        "/api/memory/reindex",
				Group:       "Memory",
				Transport:   "HTTP",
				Summary:     "强制重建 memory 索引",
				Description: "重新扫描磁盘 memory 目录并重建索引，让外部写入文件立即对 runtime 可见。",
			},
			{
				Method:      "POST",
				Path:        "/api/memory/promote",
				Group:       "Memory",
				Transport:   "HTTP",
				Summary:     "执行一次 eligible promotion",
				Description: "扫描已满足 recall 阈值的 episodic memory，并提升为 long-term memory。",
			},
			{
				Method:      "GET",
				Path:        "/api/channels",
				Group:       "Runtime",
				Transport:   "HTTP",
				Summary:     "列出全部通道状态",
				Description: "查看 HTTP、CLI、Telegram、WhatsApp 等运行时通道的连接状态与说明。",
			},
			{
				Method:      "GET",
				Path:        "/api/agents",
				Group:       "Runtime",
				Transport:   "HTTP",
				Summary:     "列出全部 agent",
				Description: "为前端或其他 agent 框架提供 agent 发现能力，包含名称、描述、模型、workspace、有效工具、共享/私有技能，以及直连是否推荐。",
			},
			{
				Method:      "GET",
				Path:        "/api/sessions/{agentID}",
				Group:       "Sessions",
				Transport:   "HTTP",
				Summary:     "列出指定 agent 的会话",
				Description: "返回 session id、创建时间、最后活动时间和条目数量，适合构建会话侧栏。",
				ResponseFields: []fieldDoc{
					{Name: "sessions[].key", Type: "string", Description: "会话唯一键。"},
					{Name: "sessions[].entryCount", Type: "int", Description: "JSONL 中的条目数量。"},
					{Name: "sessions[].lastActivity", Type: "timestamp", Description: "最后一条消息或状态写入时间。"},
				},
			},
			{
				Method:      "POST",
				Path:        "/api/sessions/{agentID}",
				Group:       "Sessions",
				Transport:   "HTTP",
				Summary:     "创建一个新会话",
				Description: "显式创建空会话，方便前端先拿到稳定 session_id，再异步发送第一条消息。",
				RequestFields: []fieldDoc{
					{Name: "name", Type: "string", Description: "可选；为空时服务端会自动生成 http_ 时间戳 session_id。"},
				},
				ResponseFields: []fieldDoc{
					{Name: "session.id", Type: "string", Description: "新会话 ID。"},
					{Name: "session.agent_id", Type: "string", Description: "所属 agent。"},
				},
				ResponseExample: map[string]any{
					"session": map[string]any{
						"agent_id": "assistant",
						"id":       "http_20260418T101500",
					},
				},
			},
			{
				Method:      "GET",
				Path:        "/api/sessions/{agentID}/{sessionID}",
				Group:       "Sessions",
				Transport:   "HTTP",
				Summary:     "读取单个会话历史",
				Description: "返回会话历史的序列化视图，包含消息、工具调用结果、plan、todo 与 meta 条目。",
				ResponseFields: []fieldDoc{
					{Name: "session.history[].type", Type: "string", Description: "message/tool_call/tool_result/meta/plan/todo。"},
					{Name: "session.history[].role", Type: "string", Description: "消息角色，通常为 user / assistant / system。"},
					{Name: "session.history[].text", Type: "string", Description: "当 type=message 或 meta 时存在。"},
				},
			},
			{
				Method:      "DELETE",
				Path:        "/api/sessions/{agentID}/{sessionID}",
				Group:       "Sessions",
				Transport:   "HTTP",
				Summary:     "删除一个会话",
				Description: "移除底层 JSONL 文件。适合管理后台，不建议在普通聊天 UI 中作为默认操作。",
			},
			{
				Method:      "GET",
				Path:        "/api/tasks/{taskID}/events?stream=1",
				Group:       "Tasks",
				Transport:   "SSE",
				Summary:     "实时订阅后台任务事件",
				Description: "对子 Agent 委派和后台任务做可视化追踪，支持 task.started/task.running/task.completed/task.failed 等事件。",
				Events:      []string{"task.queued", "task.started", "task.running", "task.completed", "task.failed", "task.cancelled"},
			},
			{
				Method:      "GET",
				Path:        "/api/tasks",
				Group:       "Tasks",
				Transport:   "HTTP",
				Summary:     "列出后台任务",
				Description: "返回 task store 中的所有任务，适合在运行列表之外单独构建委派任务中心。",
			},
			{
				Method:      "GET",
				Path:        "/api/tasks/{taskID}",
				Group:       "Tasks",
				Transport:   "HTTP",
				Summary:     "读取单个任务",
				Description: "获取 agent_id、status、summary、run_id 和 session_id。",
			},
			{
				Method:      "POST",
				Path:        "/api/tasks/{taskID}/cancel",
				Group:       "Tasks",
				Transport:   "HTTP",
				Summary:     "取消任务",
				Description: "对 queued 或 running 的任务发起取消，成功后任务会进入 cancelled。",
			},
			{
				Method:      "GET",
				Path:        "/api/runs/{runID}/tree/events?stream=1",
				Group:       "Runs",
				Transport:   "SSE",
				Summary:     "实时订阅整棵 run tree 事件",
				Description: "适合跨多 Agent、多次委派的全链路观测展示。",
			},
			{
				Method:      "GET",
				Path:        "/api/runs/{runID}/tree",
				Group:       "Runs",
				Transport:   "HTTP",
				Summary:     "读取 run tree 树形结构",
				Description: "返回同一个 run_id 下的运行树视图与事件流，便于跨 agent 追踪。",
			},
			{
				Method:      "GET",
				Path:        "/api/jobs",
				Group:       "Jobs",
				Transport:   "HTTP",
				Summary:     "列出定时任务",
				Description: "返回当前已注册的 cron 作业，并可继续调用 pause/resume/remove/schedule 接口控制。",
			},
			{
				Method:      "POST",
				Path:        "/api/jobs/{jobName}/schedule",
				Group:       "Jobs",
				Transport:   "HTTP",
				Summary:     "更新定时任务表达式",
				Description: "请求体为 {\"schedule\":\"*/5 * * * *\"}，适合对接外部运维面板。",
			},
			{
				Method:      "GET",
				Path:        "/api/logs",
				Group:       "Logs",
				Transport:   "HTTP",
				Summary:     "读取日志快照",
				Description: "按 limit 返回当前日志缓冲区内容；适合页面初次加载时回放最近日志。",
			},
			{
				Method:      "GET",
				Path:        "/api/logs/stream",
				Group:       "Logs",
				Transport:   "SSE",
				Summary:     "订阅网关日志",
				Description: "用于页面中的运行日志流、告警提示和问题排查。",
			},
			{
				Method:      "GET",
				Path:        "/api/catalog",
				Group:       "Runtime",
				Transport:   "HTTP",
				Summary:     "读取完整 API 文档目录",
				Description: "返回 endpoints、schemas、workflow 和 event_types，适合作为自动生成 SDK 或接入向导的数据源。",
			},
			{
				Method:      "POST",
				Path:        "/api/config",
				Group:       "Config",
				Transport:   "HTTP",
				Summary:     "保存配置",
				Description: "上传完整配置 JSON，服务端会校验、写盘，并通过回调更新当前运行态。",
			},
		},
		EventTypes: []eventTypeDoc{
			{Name: "run.accepted", Channel: "run", Summary: "流式 POST 已受理，返回 run / session 元数据。", TriggeredBy: "POST /api/chat?stream=1 或 POST /api/runs?stream=1", Fields: []fieldDoc{{Name: "run.id", Type: "string", Description: "运行 ID"}}},
			{Name: "run.started", Channel: "run", Summary: "运行真正进入执行态。", TriggeredBy: "run 创建成功后", Fields: []fieldDoc{{Name: "run_id", Type: "string", Description: "运行 ID"}}},
			{Name: "input.received", Channel: "run", Summary: "入口输入已进入 runtime 契约。", TriggeredBy: "ingress 接收外部输入", Fields: []fieldDoc{{Name: "payload.block_count", Type: "number", Description: "输入块数量"}, {Name: "payload.attachment_count", Type: "number", Description: "附件数量"}}},
			{Name: "input.normalized", Channel: "run", Summary: "输入块已经标准化为统一 InputEnvelope。", TriggeredBy: "runtime 完成输入归一化", Fields: []fieldDoc{{Name: "payload.blocks[].type", Type: "string", Description: "标准输入块类型"}}},
			{Name: "attachment.stored", Channel: "run", Summary: "附件已登记到当前运行的输入清单。", TriggeredBy: "runtime 建立附件引用", Fields: []fieldDoc{{Name: "payload.attachment_id", Type: "string", Description: "附件 ID"}, {Name: "payload.type", Type: "string", Description: "附件类型"}}},
			{Name: "attachment.resolved", Channel: "run", Summary: "附件已被解析为当前运行可消费的上下文引用。", TriggeredBy: "run 初始化输入适配器", Fields: []fieldDoc{{Name: "payload.attachment_id", Type: "string", Description: "附件 ID"}, {Name: "payload.path", Type: "string", Description: "运行时引用路径"}}},
			{Name: "text.delta", Channel: "run", Summary: "模型输出的增量文本分片。", TriggeredBy: "assistant 文本生成中", Fields: []fieldDoc{{Name: "payload.text", Type: "string", Description: "本次增量内容"}}},
			{Name: "tool.call.requested", Channel: "run", Summary: "模型产出了一次工具调用计划。", TriggeredBy: "LLM 输出 tool call", Fields: []fieldDoc{{Name: "payload.tool", Type: "string", Description: "工具名"}, {Name: "payload.id", Type: "string", Description: "工具调用 ID"}}},
			{Name: "tool.call.started", Channel: "run", Summary: "普通工具真正开始执行。", TriggeredBy: "runtime 调度工具 effect", Fields: []fieldDoc{{Name: "payload.tool", Type: "string", Description: "工具名"}, {Name: "payload.id", Type: "string", Description: "工具调用 ID"}}},
			{Name: "tool.completed", Channel: "run", Summary: "普通工具执行完成。", TriggeredBy: "工具执行成功返回", Fields: []fieldDoc{{Name: "payload.output", Type: "string", Description: "工具输出"}, {Name: "payload.error", Type: "string", Description: "失败时为空"}}},
			{Name: "tool.failed", Channel: "run", Summary: "普通工具执行失败。", TriggeredBy: "工具执行失败或被中断", Fields: []fieldDoc{{Name: "payload.output", Type: "string", Description: "工具部分输出"}, {Name: "payload.error", Type: "string", Description: "错误信息"}}},
			{Name: "tool.fanout.completed", Channel: "run", Summary: "当前批次工具 fan-in 已完成。", TriggeredBy: "本轮所有已启动工具进入 terminal", Fields: []fieldDoc{{Name: "payload.status", Type: "string", Description: "completed / failed / cancelled"}, {Name: "payload.failed_count", Type: "number", Description: "失败工具数量"}}},
			{Name: "memory.recalled", Channel: "run", Summary: "运行前注入了哪些 memory 以及来源。", TriggeredBy: "memory 搜索命中后注入 prompt", Fields: []fieldDoc{{Name: "payload.query", Type: "string", Description: "召回查询"}, {Name: "payload.entries[].id", Type: "string", Description: "memory ID"}, {Name: "payload.entries[].matched_terms", Type: "[]string", Description: "本次匹配到的关键词"}}},
			{Name: "agent.call.started", Channel: "run", Summary: "父 run 已发起子 agent 调用。", TriggeredBy: "callagent 任务进入执行态", Fields: []fieldDoc{{Name: "payload.target_agent", Type: "string", Description: "目标 agent"}, {Name: "payload.task", Type: "string", Description: "子 agent 任务内容"}, {Name: "payload.id", Type: "string", Description: "callagent 工具调用 ID"}}},
			{Name: "agent.call.completed", Channel: "run", Summary: "子 agent 调用成功完成。", TriggeredBy: "后台任务完成", Fields: []fieldDoc{{Name: "payload.summary", Type: "string", Description: "子 agent 摘要"}, {Name: "payload.task_id", Type: "string", Description: "后台任务 ID"}}},
			{Name: "agent.call.failed", Channel: "run", Summary: "子 agent 调用失败或被取消。", TriggeredBy: "后台任务失败或取消", Fields: []fieldDoc{{Name: "payload.error", Type: "string", Description: "失败原因"}, {Name: "payload.task_id", Type: "string", Description: "后台任务 ID"}}},
			{Name: "agent.fanout.completed", Channel: "run", Summary: "当前批次子 agent fan-in 已完成。", TriggeredBy: "同一父 run 下注册的后台子任务全部进入 terminal", Fields: []fieldDoc{{Name: "payload.status", Type: "string", Description: "completed / failed / cancelled"}, {Name: "payload.total_count", Type: "number", Description: "子任务总数"}}},
			{Name: "run.completed", Channel: "run", Summary: "主运行完成。", Terminal: true, TriggeredBy: "agent 正常完成"},
			{Name: "run.failed", Channel: "run", Summary: "主运行失败或被中止。", Terminal: true, TriggeredBy: "agent 出错或 abort", Fields: []fieldDoc{{Name: "payload.message", Type: "string", Description: "错误消息"}}},
			{Name: "maintenance.memory.stale_cleanup.completed", Channel: "maintenance", Summary: "完成一次过期 memory 清理。", Fields: []fieldDoc{{Name: "payload.removed", Type: "number", Description: "移除的 memory 数量"}, {Name: "payload.at", Type: "timestamp", Description: "执行时间"}}},
			{Name: "maintenance.memory.reindexed", Channel: "maintenance", Summary: "完成一次 memory 重建索引。", Fields: []fieldDoc{{Name: "payload.total", Type: "number", Description: "重建后索引中的 memory 总数"}}},
			{Name: "maintenance.memory.promotion.completed", Channel: "maintenance", Summary: "完成一次 episodic 到 long-term 的 promotion。", Fields: []fieldDoc{{Name: "payload.promoted", Type: "number", Description: "本次提升的 memory 数量"}, {Name: "payload.at", Type: "timestamp", Description: "执行时间"}}},
			{Name: "task.queued", Channel: "task", Summary: "后台任务进入排队态。"},
			{Name: "task.started", Channel: "task", Summary: "后台任务开始执行。"},
			{Name: "task.running", Channel: "task", Summary: "后台任务仍在运行，并上报一次保活/活动事件。", Fields: []fieldDoc{{Name: "payload.last_activity_at", Type: "timestamp", Description: "最近一次活动时间"}}},
			{Name: "task.completed", Channel: "task", Summary: "后台任务成功完成。", Terminal: true, Fields: []fieldDoc{{Name: "payload.summary", Type: "string", Description: "任务摘要"}}},
			{Name: "task.failed", Channel: "task", Summary: "后台任务失败。", Terminal: true, Fields: []fieldDoc{{Name: "payload.error", Type: "string", Description: "失败原因"}}},
			{Name: "task.cancelled", Channel: "task", Summary: "后台任务被取消。", Terminal: true},
			{Name: "log", Channel: "log", Summary: "运行时日志流事件。", Fields: []fieldDoc{{Name: "message", Type: "string", Description: "日志文本"}, {Name: "level", Type: "string", Description: "日志等级"}}},
		},
		Schemas: []schemaDoc{
			{
				Name:        "ChatRequest",
				Summary:     "推荐的单入口请求体，可直接用于 `/api/chat`。",
				Description: "纯文本场景可只传 text；复杂输入改传 inputs。配合 `stream=1` 时会直接返回 SSE。",
				Fields: []fieldDoc{
					{Name: "agent_id", Type: "string", Description: "目标 agent。"},
					{Name: "session_id", Type: "string", Description: "可选但强烈建议传入。"},
					{Name: "message_id", Type: "string", Description: "可选的客户端消息 ID；重复 replay/live 事件可按该 ID 去重。"},
					{Name: "text", Type: "string", Description: "纯文本输入。"},
					{Name: "inputs", Type: "[]InputBlock", Description: "多模态或文件输入；附件引用可来自 /api/attachments。"},
				},
				Example: map[string]any{"agent_id": "assistant", "session_id": "demo-session", "message_id": "msg_001", "text": "你好"},
			},
			{
				Name:        "InputBlock",
				Summary:     "统一输入块模型，HTTP 请求与 SSE 相关入口共用。",
				Description: "推荐在外部 SDK 中直接复用该结构，避免单独为文本、文件和图片建多套协议。",
				Fields: []fieldDoc{
					{Name: "type", Type: "string", Description: "text/file/dir/image/pdf/url。"},
					{Name: "text", Type: "string", Description: "当 type=text 时携带用户文本。"},
					{Name: "path", Type: "string", Description: "当 type=file/dir/image/pdf 时传服务端可读取路径，WebUI/HTTP 上传场景使用 assets 引用路径。"},
					{Name: "attachment_id", Type: "string", Description: "已持久化附件 ID，用于工具读取、回显与 session 中的小引用。"},
					{Name: "url", Type: "string", Description: "当 type=url 时可传远端地址。"},
					{Name: "mime_type", Type: "string", Description: "二进制或图像类型说明。"},
					{Name: "meta", Type: "object", Description: "调用方自定义扩展字段。"},
				},
				Example: map[string]any{"type": "text", "text": "请总结最近一次运行失败原因"},
			},
			{
				Name:    "AgentCapability",
				Summary: "`/api/agents` 返回的单个 agent 能力视图。",
				Fields: []fieldDoc{
					{Name: "id", Type: "string", Description: "agent ID。"},
					{Name: "entry", Type: "bool", Description: "是否是推荐入口 agent。"},
					{Name: "direct_request.recommended", Type: "bool", Description: "是否推荐被外部系统直接调用。"},
					{Name: "tools[].name", Type: "string", Description: "当前 agent 可用的有效工具。"},
					{Name: "skills.effective[].name", Type: "string", Description: "当前 agent 最终可见的技能集合。"},
				},
			},
			{
				Name:    "RunRecord",
				Summary: "一次 agent 运行的持久化记录。",
				Fields: []fieldDoc{
					{Name: "id", Type: "string", Description: "运行 ID。"},
					{Name: "agent_id", Type: "string", Description: "执行 agent。"},
					{Name: "session_id", Type: "string", Description: "归属会话。"},
					{Name: "input", Type: "string", Description: "用户输入摘要。"},
					{Name: "output", Type: "string", Description: "最终输出。"},
					{Name: "status", Type: "string", Description: "queued/running/completed/failed/aborted。"},
				},
			},
			{
				Name:    "EventRecord",
				Summary: "run / run tree SSE 事件的统一结构。",
				Fields: []fieldDoc{
					{Name: "sequence", Type: "int", Description: "该 run 内的事件序号。"},
					{Name: "name", Type: "string", Description: "事件名。"},
					{Name: "timestamp", Type: "timestamp", Description: "UTC 时间。"},
					{Name: "payload", Type: "object", Description: "随事件携带的附加数据。"},
				},
			},
			{
				Name:    "SessionHistoryEntry",
				Summary: "会话历史序列化后的条目视图。",
				Fields: []fieldDoc{
					{Name: "type", Type: "string", Description: "message/tool_call/tool_result/meta/plan/todo。"},
					{Name: "role", Type: "string", Description: "type=message 时常见为 user/assistant/system。"},
					{Name: "text", Type: "string", Description: "消息或 meta 文本。"},
					{Name: "tool", Type: "string", Description: "工具调用名称。"},
					{Name: "output", Type: "string", Description: "工具执行输出。"},
				},
			},
			{
				Name:    "Task",
				Summary: "后台委派任务模型。",
				Fields: []fieldDoc{
					{Name: "id", Type: "string", Description: "任务 ID。"},
					{Name: "agent_id", Type: "string", Description: "负责执行的 agent。"},
					{Name: "run_id", Type: "string", Description: "所属运行。"},
					{Name: "session_id", Type: "string", Description: "关联会话。"},
					{Name: "status", Type: "string", Description: "queued/running/completed/failed/cancelled。"},
				},
			},
		},
		Notes: []string{
			"HTTP + SSE 是最容易接入且最稳定的组合；新接入优先用 `/api/chat?stream=1`，只在兼容或断线恢复场景下再单独使用 `/api/runs/{runID}/events`。",
			"会话历史是事实来源；前端可以做乐观渲染，但在 run 结束后最好重新读取 session 历史以获得稳定结果。",
			"显式直连非入口 agent 会绕过入口编排，并使用该 agent 自己的 session 历史；这对专家位调度很有用，但不适合作为默认用户入口。",
		},
	}
}

func (p *ControlPlane) saveConfig(raw []byte) error {
	if p == nil || p.configCtl == nil {
		return fmt.Errorf("config not available")
	}
	return p.configCtl.SaveConfig(raw)
}
