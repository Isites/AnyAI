# AnyAI 最终设计整合文档

版本日期：2026-05-05

## 1. 文档定位

本文档将 `docs/` 目录下既有设计稿重新整合为一份最终版设计说明，覆盖基础架构、当前实现、运行时智能增强、自驱机制、重构收敛方向和后续判断标准。

本文以当前代码库实现为准。旧文档中的历史概念如果已经被新实现替换，会在本文中用最终命名收敛，例如：

- 历史 `delegate` 心智收敛为模型侧 `callagent` 工具和 runtime 内部 `doTask(kind=agent)`。
- 历史 `task_wait`、`background`、`parallel_background` 不再作为模型协作主路径。
- 旧文档中的 `internal/agent`、`internal/tools` 等早期路径，以当前 `internal/runtime/agent`、`internal/runtime/tool` 等真实路径为准。

## 2. 产品与框架定位

AnyAI 是一个“项目优先”的 Agent 开发框架与运行时，而不是预置业务产品。

项目作者通过以下文件和目录组装自己的 Agent 工程：

- `anyai.yaml`：项目级模型、provider、runtime、memory、channel、安全与日志配置。
- `agent.md`：Agent 身份、模型、工具权限、workspace、系统指令与行为边界。
- `SKILL.md`：可按系统级、项目共享级、Agent 私有级装载的能力说明。
- `anyai/`：运行时数据目录，承载 session、memory、events、logs 等长期状态。

AnyAI 的目标是让非框架开发者也能以较低门槛构建可运行、可组合、可扩展、可长期运行的 Agent 系统。

核心体验不应要求用户理解内部 task、run tree、projection、gateway facade 或 event replay。用户主要理解：

- 用 `agent.md` 定义 Agent。
- 用 `anyai.yaml` 定义项目运行参数。
- 用 `callagent` 连接 Agent。
- 用 `skills/` 和 `anyai/memory/` 提供可复用上下文。

## 3. 最终设计原则

### 3.1 Agent 是最小运行单元

一个目录中的一个 `agent.md` 定义一个 Agent。Agent 是 AnyAI 的最小行为单元，也是多 Agent 协作的基本节点。

AnyAI 不引入独立的 `workflow.md`、节点图文件或强制工作流 DSL。复杂工作流由 Agent 调用树表达。

### 3.2 Workflow 是受控 Agent 调用树

复杂任务的执行方式是：

- 入口 Agent 理解用户目标。
- 能直接完成时直接完成。
- 需要专业分工时调用其他 Agent。
- 子 Agent 在独立 run/session 中完成任务。
- 父 Agent 汇总结构化结果并继续推理或回答用户。

因此，AnyAI 的工作流不是额外系统，而是 runtime 管理的 `run/task/agent.call` 事件树。

### 3.3 Runtime 是内核

AnyAI 的框架本体在 `internal/runtime`：

- Agent think-act loop。
- LLM provider 适配。
- Tool 执行与恢复。
- Session、compaction、transcript hygiene。
- Task / process / agent call 编排。
- Turn 生命周期。
- Goal 自驱判断。
- Memory、Skill、Resource catalog。
- Events、Projection、Control、Daemon。

Runtime 不反向依赖 gateway、channel 或 HTTP/UI。

### 3.4 Gateway 是统一外部暴露层

所有外部输入必须先进入 Gateway，再进入 Runtime。

适用入口包括：

- CLI。
- HTTP API / SSE / Web UI。
- Telegram。
- WhatsApp。
- 后续 Slack、Feishu、Discord 等消息渠道。

Channel 只做输入输出适配和事件展示，不承载 Agent 编排、session lane、memory lifecycle 或 task 调度。

### 3.5 Markdown 优先，Runtime 承担复杂性

`agent.md` 主要表达业务意图、角色、原则和边界。

Runtime 负责补齐：

- 工具真实可用能力与边界。
- 项目根、workspace、session、memory、agent 目录等环境事实。
- 当前请求焦点。
- skill 和 memory 召回。
- 工具失败恢复建议。
- 运行时 completion/goal 规则。
- 多 Agent 协作契约。

智能提升来自“prompt 骨架 + 运行时事实 + 工具恢复 + session hygiene + 可观察性”的组合，而不是单纯把系统提示词写长。

## 4. 当前事实架构

当前代码库的主路径可以概括为：

```text
cmd
  -> registry/config 解析项目
  -> startup 组装 runtime/gateway/channel/http/daemon
  -> gateway 暴露 ingress/control/observe/channel supervisor
  -> runtime/execution 处理 ingress、session lane、managed run
  -> runtime/agent 执行 think-act loop
  -> runtime/task 执行 agent/tool/process task
  -> runtime/events 持久化事实事件
  -> runtime/projection 构建 run/session/task/memory 读模型
  -> gateway replay / HTTP API / Web UI / channel event view
```

当前顶层分层为：

```text
Project Layer
  registry / config / agent.md / SKILL.md
  把项目定义解析成 runtime 可消费的 Config 和 RuntimeSpec

Composition Layer
  startup
  assemble / start / stop / watcher wiring / updater wiring

Gateway Layer
  ingress facade
  observe facade
  control facade
  channel supervisor / router

Product Shell
  CLI
  HTTP API / SSE / Web UI
  Telegram / WhatsApp

Runtime Kernel
  execution
  agent
  task / turn / plan / goal
  tool
  session
  memory
  resources / skill
  events / projection / control / daemon
```

关键代码落点：

| 层 | 主要路径 | 当前职责 |
| --- | --- | --- |
| CLI | `cmd/main.go` | `chat`、`start`、`init`、`version` 命令 |
| 项目解析 | `internal/registry`、`internal/config` | 扫描 `agent.md`，合并 `anyai.yaml`，生成运行时配置 |
| 启动装配 | `internal/startup` | 组装 core runtime、gateway、channel、HTTP、daemon、watcher |
| Gateway | `internal/gateway` | 路由、ingress、control、observe、replay、channel manager |
| HTTP 产品壳 | `internal/startup/http` | REST API、SSE、Web UI、metrics |
| Channel | `internal/channel` | CLI、Telegram、WhatsApp 适配 |
| Runtime 内核 | `internal/runtime` | Agent、Task、Tool、Session、Memory、Skill、Events、Projection |

## 5. 项目模型

### 5.1 推荐目录结构

```text
project/
├── anyai.yaml
├── agent.md
├── skills/
├── common/
│   └── skills/
├── agents/
│   ├── coder/
│   │   ├── agent.md
│   │   └── skills/
│   └── reviewer/
│       ├── agent.md
│       └── skills/
└── anyai/
    ├── sessions/
    ├── memory/
    │   ├── candidates/
    │   ├── episodic/
    │   └── long-term/
    ├── events/
    └── runtime.log
```

目录规则：

- `anyai.yaml` 可选，但用于项目级运行配置。
- 根目录 `agent.md` 是默认入口候选。
- 任意子目录中的 `agent.md` 都定义一个可用 Agent。
- `<agent_dir>/skills/` 是 Agent 私有技能。
- `<project>/common/skills/` 是项目共享技能。
- `<project>/anyai/memory/` 是正式 memory 根目录。
- 旧 `memory/*.md`、`memory/entries/*.md` 等历史布局只作为兼容来源，不再是推荐主路径。

### 5.2 `anyai.yaml`

`anyai.yaml` 的职责是项目运行配置，不替代 `agent.md`，也不负责声明扫描目录。

当前主要字段：

| 字段 | 说明 |
| --- | --- |
| `name` | 项目名称 |
| `models.default` | 默认模型 |
| `models.aliases` | 模型别名 |
| `providers` | OpenAI、Anthropic、Gemini、Qwen、OpenAI-compatible 等 provider 配置 |
| `runtime.idle_timeout_ms` | run/task/agent call 默认空闲超时 |
| `runtime.agent_call.depth_limit` | Agent 调用深度限制 |
| `runtime.agent_call.max_parallel` | 并行 Agent 调用上限 |
| `runtime.tools.max_attempts` | 工具恢复最大尝试次数 |
| `runtime.tools.retry_backoff_ms` | 工具 retry backoff |
| `runtime.tools.loop_detection` | 工具循环检测 |
| `runtime.tools.preflight` | deterministic tool preflight 开关 |
| `runtime.sessions.queue*` | 同 session active run 队列策略 |
| `runtime.sessions.compaction` | session compact 策略 |
| `memory` | memory 启用、注入上限、auto capture 生命周期参数 |
| `channels` | gateway/http/telegram/whatsapp 等渠道配置 |
| `security` | exec approvals、DM policy、group policy 等 |
| `logging` | 文件日志、stderr、WhatsApp 库日志与 rotation |

Provider 支持固定 headers 与环境变量 JSON headers，便于接入代理、路由或鉴权扩展。

### 5.3 `agent.md`

`agent.md` 由 YAML frontmatter 和 Markdown 正文组成。

当前 frontmatter 主要字段：

| 字段 | 说明 |
| --- | --- |
| `id` | Agent 唯一 ID，缺省时按目录名推导 |
| `name` | 展示名 |
| `description` | 职责说明 |
| `entry` | 是否是目录模式入口 |
| `model` | 当前 Agent 模型，缺省时使用项目默认模型 |
| `fallbacks` | 模型 fallback 列表 |
| `workspace` | 工具执行工作目录，必须在项目根内 |
| `max_turns` | 单 run 最大模型/工具循环轮数 |
| `tools.allow` | 允许工具 |
| `tools.deny` | 禁止工具 |
| `skills.inherit_shared` | 是否继承项目共享技能，默认 true |
| `tags` | 展示和推荐标签 |

正文是 Agent 的行为主体。Runtime 不把正文解析成工作流图，只把它作为 Agent 指令参与 prompt 组装。

### 5.4 入口选择规则

目录模式入口解析顺序：

1. 命令行指定 agent ID 时使用该 Agent。
2. 唯一 `entry: true` Agent。
3. 项目根目录 `agent.md`。
4. 只有一个 Agent 时自动使用它。
5. 多 Agent 且无法唯一确定入口时启动失败，并列出可选 ID。

文件模式：

- 目标路径是 `agent.md` 时，该文件对应 Agent 是显式入口。
- 文件模式会向上查找最近 `anyai.yaml` 作为项目根。
- 找不到 `anyai.yaml` 时按单 Agent 项目处理。
- 文件模式不可再额外指定 agent override。

### 5.5 Registry 规则

`internal/registry/project.go` 当前负责：

- 解析项目根。
- 读取 `anyai.yaml`。
- 扫描所有 `agent.md`。
- 校验重复 ID。
- 校验 `workspace` 不越出项目根。
- 解析共享技能、私有技能和 memory 目录。
- 合并项目模型别名、provider、runtime、memory、channel、安全配置。
- 生成 `config.Config` 供 runtime 使用。

项目扫描逻辑不应散落到 startup、gateway 或 runtime 执行路径中。

## 6. 启动与热更新

### 6.1 Composition Root

`internal/startup` 是 composition root。它负责：

- 读取或接收 `config.Config`。
- 构建 `RuntimeSpec`。
- 构建 base components：providers、session store、tool registry、skill loader、memory manager、recorder、task runtime、turn store。
- 包装 `runtime.Runtime`。
- 构建 `gateway.Service`。
- 注册 channel。
- 启动 HTTP service。
- 启动 daemon。
- 启动 project/config watcher。

### 6.2 RuntimeSpec

当前已存在 `internal/runtime/factory.RuntimeSpec`：

```go
type RuntimeSpec struct {
    Config    *config.Config
    Layout    ProjectLayout
    Providers map[string]llm.LLMProvider
    Resources *resources.Catalog
    Skills    *skill.Loader
}
```

它是一次完整 runtime 规格的内部表达。

### 6.3 RuntimeUpdater

当前已存在 `internal/startup/runtime_updater.go`：

- `ApplyConfig(ctx, cfg, source)`。
- `ApplyProject(ctx, project, source)`。

更新流程：

```text
project/config changed
  -> registry.LoadProject 或 config.Load
  -> factory.BuildRuntimeSpec
  -> spec.WithRuntimeResources
  -> runtime.ApplySpec
  -> gateway.ApplyRuntimeConfig
```

这使 `startup` 不再直接散落调用一组资源 setter。后续继续收敛时，应保持 watcher 只触发 updater，而不是亲自修改 runtime 内部组件。

## 7. Gateway、Channel 与 HTTP 产品壳

### 7.1 Gateway 职责

`gateway.Service` 当前是 runtime 能力的统一暴露层，承担：

- Ingress：路由并提交外部输入。
- Observe：run、run tree、session、task、memory 读模型与 replay stream。
- Control：cancel、session create/delete、projection rebuild、memory maintenance、config save。
- Channel supervisor：注册、连接、停止 channel。
- Log stream 与 runtime overview。

当前已拆出较窄 facade：

- `IngressFacade`。
- `ObserveFacade`。
- `ControlFacade`。
- `ChannelPort`。

实现体仍是一个 `gateway.Service`，但使用方应依赖窄接口。

### 7.2 Channel 职责

`internal/channel` 只做：

- 接收外部消息。
- 转换为 `runtimeport.IngressRequest` / `input.InputEnvelope`。
- 调用 Gateway。
- 消费 `runtimeevents.EventRecord` 或 channel `RunEvent`。
- 输出文本、状态、工具事件或 run tree 事件。

Channel 不做：

- Agent 调度。
- Tool 调度。
- Memory lifecycle。
- Session lane。
- Task join。

### 7.3 HTTP/API/Web UI

HTTP 产品壳位于 `internal/startup/http`。

当前它不是 runtime 内核，也不是普通消息 channel 的业务逻辑延伸，而是通过 gateway facade 组合：

- REST API。
- SSE event stream。
- Web UI。
- Metrics。
- Runtime catalog 和 overview。

HTTP API 当前已拆成多个 plane：

- `InventoryPlane`。
- `RuntimePlane`。
- `RunPlane`。
- `SessionPlane`。
- `MemoryPlane`。
- `LogPlane`。
- `ConfigPlane`。
- `TaskPlane`。

这比旧的单一大 `Plane` 更清晰。后续如果迁移目录命名，可考虑把 `internal/startup/http` 改为更明确的产品壳位置，但短期不需要为了命名做大范围 churn。

## 8. 统一输入模型

### 8.1 InputEnvelope

当前统一输入模型在 `internal/runtime/input`：

```go
type InputEnvelope struct {
    SessionID string
    Blocks    []InputBlock
}

type InputBlock struct {
    ID       string
    Type     string // text, file, dir, image, pdf, url
    Name     string
    Text     string
    Path     string
    URL      string
    MimeType string
    Data     []byte
    Meta     map[string]any
}
```

设计规则：

- 所有入口先归一化为 `InputEnvelope`。
- Runtime 只处理一种输入结构。
- 输入负责“带入内容或引用”，工具负责“按需深入读取”。

### 8.2 输入生命周期事件

`runtime/execution/ingress.go` 当前记录：

- `input.received`。
- `attachment.stored`。
- `input.normalized`。
- `session.input.stored`。

附件在当前实现里主要通过 inline manifest、输入 block 和工具 adapter 暴露；`AttachmentStore` 已有基础结构，但完整上传/持久附件产品体验仍可继续增强。

### 8.3 输入工具

当前 runtime-scoped 输入工具：

- `input_manifest`：返回当前 run 的输入 block 清单。
- `attachment_get`：按附件 ID 读取附件元信息。

默认策略是：文本直接进入请求，文件/PDF/目录/URL/图片先以摘要或引用进入上下文，需要时再通过工具深入。

## 9. Agent Runtime 与 Prompt 架构

### 9.1 Prompt 分层

当前 `internal/runtime/agent/prompt.go` 以框架拥有的稳定骨架组装系统提示词。

高层顺序为：

```text
Agent Instructions
Runtime Identity
Runtime Contract
Runtime Capabilities
Environment Facts
Collaboration Contract
Current Request Focus
Relevant Skills
Relevant Memory
Goal / Plan runtime facts
```

其中：

- `Agent Instructions` 来自 `agent.md` 正文。
- `Runtime Contract` 是框架规则。
- `Runtime Capabilities` 来自当前真实工具集合。
- `Environment Facts` 注入 project root、workspace、config、data、session、memory、agent definition、agents root、shared skills 等路径。
- `Collaboration Contract` 区分入口 run 与 agent call，并注入 task contract。
- `Current Request Focus` 显式说明当前要回复的用户目标。
- `Relevant Skills` 和 `Relevant Memory` 是动态检索内容。
- Goal/Plan 事实用于自驱检查和完成验证。

### 9.2 Request Focus

`internal/runtime/agent/request_focus.go` 当前负责从以下来源提取本轮焦点：

- 当前 inbound user message。
- session 历史尾部。
- collect followup 合成输入。
- compaction summary。

Focus 会驱动：

- prompt query。
- skill match。
- memory search。
- system prompt 中的 current request section。

这避免模型把旧 transcript 尾部误当作当前主目标。

### 9.3 Transcript Hygiene

`internal/runtime/agent/transcript_policy.go` 和 `transcript_repair.go` 当前执行 request-local transcript repair。

已实现策略：

- 连续 user turn 合并或降权为上下文。
- compaction/meta summary 不再作为普通当前 user message 送模。
- orphan tool result 丢弃。
- tool pair 结构修复。
- repair 只影响本次模型请求，不直接改写磁盘 session。

后续可继续细分 provider-aware legality，例如 Anthropic/OpenAI/Gemini 不同 role/tool 约束。

### 9.4 Tool Preflight

`internal/runtime/agent/tool_preflight.go` 当前在工具真正执行前做确定性修补：

- tool name trim / case normalization。
- stringified JSON 解包。
- 常见字段别名修补。
- repair metadata 写入 tool result。

边界：

- 只做确定性修补。
- 不猜测用户意图。
- 不扩展工具本身语义。

### 9.5 工具失败恢复

`internal/runtime/agent/tool_recovery.go` 当前是工具恢复主入口。

能力包括：

- 错误分类。
- retryable 判定。
- 有界自动重试。
- backoff。
- `suggested_next_moves`。
- loop warning/block。
- tool result shaping。
- structured metadata 持久化。

常见错误类别包括：

- `path_not_found`。
- `path_is_directory`。
- `permission_denied`。
- `validation_error`。
- `timeout`。
- `network_error`。
- `transient_provider_error`。
- `loop_detected`。
- `unknown`。

工具失败不再只是裸 error，而是会进入：

- session tool result。
- 下一轮模型 transcript。
- runtime event。
- API/UI history。

### 9.6 Incomplete Turn Recovery

`internal/runtime/agent/run_recovery.go` 当前检测中途失败或无最终回复的 incomplete turn。

在需要时会：

- 生成 fallback assistant message。
- 将 fallback 写入 session。
- 发出 `run.incomplete` / `run.fallback_reply`。
- 对可能有副作用的中断场景提示用户核实。

目标是避免用户看到空白，或下一轮模型在未收口尾部上继续推理。

### 9.7 Runtime Hooks

`internal/runtime/agent/hooks.go` 当前定义 runtime hook 家族：

- `BeforePromptBuild`。
- `BeforeModelResolve`。
- `BeforeToolCall`。
- `AfterToolCall`。
- `ToolResultShape`。
- `LoopDetect`。
- `BeforeCompaction`。
- `AfterCompaction`。
- `AgentEnd`。

Hook 是 Go 内部扩展点，不是重量级插件系统。默认行为仍由 runtime 主链路负责。

## 10. 统一任务编排：doTask

### 10.1 核心判断

AnyAI 的内部编排核心是：

```text
task.Runtime.DoTask + callback + JoinAllSettled
```

它覆盖：

- `task.KindAgent`：子 Agent 调用。
- `task.KindTool`：普通工具调用。
- `task.KindProcess`：bash/python 等外部进程类工具。

未来扩展 workflow/http/daemon job 时，也应优先作为 task kind，而不是新增平行调度系统。

### 10.2 对模型暴露的协作工具

模型侧当前使用 `callagent`。

`callagent` 支持：

- 单 Agent inline 调用。
- `mode: "parallel"` 的多 Agent fan-out / fan-in。

父 Agent 不需要自行轮询 task。Runtime 会：

- 创建 task。
- 执行子 run。
- 收集 child result。
- 在 callback 中把结构化结果回注为 tool result。
- 继续父 Agent 下一轮推理。

### 10.3 不再作为主路径的旧语义

以下不再是模型默认协作语义：

- `task_wait`。
- `background`。
- `parallel_background`。
- `followup` 作为内部 task 编排机制。
- 独立 `delegate` shadow runtime。

注意：session queue 中仍有 `collect/followup/interrupt`，那是同一 session 的 ingress 队列策略，不是 runtime 内部 task 编排语义。

### 10.4 Agent Task 链路

当前 `callagent` 真实链路：

```text
agent receives callagent tool call
  -> agent/tool_batch.go intercepts workflow tool
  -> runtime.Runtime.DoAgent / DoAgentParallel
  -> runtime.Runtime.DoTask(kind=agent)
  -> task.Runtime
  -> task/agentexec.Executor
  -> execution.StartManagedRun(child)
  -> child agent produces events
  -> task completion callback
  -> parent receives structured tool result
```

### 10.5 Tool 与 Process Task 链路

普通工具调用也会进入 task runtime：

```text
LLM tool call
  -> preflight / recovery
  -> agent.submitToolTask
  -> task.Runtime.DoTask(kind=tool)
  -> builtin.ToolExecutor
  -> callback
  -> session.ToolResultEntryWithMetadata
```

bash/python 等 process-backed tool 会下沉为：

```text
task.KindProcess
  -> builtin.ProcessExecutor
```

这让 tool、process、agent call 都拥有统一 lifecycle、events、cancel、retry 和 join 语义。

## 11. Turn 生命周期

### 11.1 当前实现

`internal/runtime/turn` 已实现 Turn 级生命周期：

- `Turn`。
- `Store`。
- context 绑定与 rebind。
- `Touch` 保活。
- task 注册/反注册。
- complete/cancel/timeout 状态。

`execution.StartManagedRun` 会：

- 确保 `TurnStore` 存在。
- 以 run ID 创建或绑定 Turn。
- 将 run context 与 Turn 绑定。
- run 完成后 complete Turn，并在 owning run 场景清理 TurnStore。

`task.Runtime` 会：

- 共享 `TurnStore`。
- 在 task 执行时绑定当前 run 的 Turn。
- 注册 task。
- 工具/子任务 activity 触发 Turn touch。

### 11.2 当前边界

旧设计提出在 `task.Record` 中持久化 `TurnID`，当前代码尚未这么做。当前实现以 runtime context 和 `run_id` 绑定 Turn，而非把 TurnID 作为 task record 的持久字段。

因此最终判断是：

- Turn 作为生命周期机制已经进入主路径。
- Task store 仍保留 `IdleTimeoutMS`、`LastActivityAt` 等兼容字段。
- 后续若要进一步收敛，可增加清晰的 Turn projection 或 task -> turn 显式关系，但不应破坏当前 context 级生命周期语义。

## 12. Goal 自驱与 Plan

### 12.1 Goal Runtime

`internal/runtime/task/goal.go` 已实现 runtime 主导的 Goal 检查。

Goal 状态：

- `in_progress`。
- `awaiting_input`。
- `completed`。
- `abandoned`。

Runtime 检查的客观事实：

- 是否有运行中的 task。
- 是否有 pending tool call。
- 是否有未完成 checkpoint。
- 是否有 open todo。
- 是否有 ready/blocked/failed/paused structured plan。
- 是否达到 max turns 或 max duration hard stop。

### 12.2 Goal 工具

当前工具：

- `goal_complete`：模型声明目标完成，但 runtime 会验证。
- `await_user_input`：模型声明需要用户输入，Goal 进入 awaiting input。

`goal_complete` 不是“模型说完就完”，而是 runtime 检查通过后才完成。

### 12.3 自驱继续

`internal/runtime/agent/goal_after_done.go` 当前在 agent 打算结束时检查 Goal：

- 如果无未完成客观工作，可完成。
- 如果需要调用 `goal_complete` 但模型没有显式完成，runtime 可追加 completion prompt。
- 如果仍有未完成工作，runtime 追加 continuation prompt 并继续下一轮。
- 如果 hard stop，runtime 生成用户可读 handoff。

这对应当前最终设计中的核心目标：Runtime 决定是否应该继续，模型决定如何继续。

### 12.4 Structured Plan

`internal/runtime/plan` 当前提供：

- `Plan`。
- `Step`。
- `StepAction`。
- `Engine`。
- `Executor`。
- ready/blocked/all-completed evaluation。

`update_plan` 工具当前要求结构化 plan，不再鼓励 legacy text plan。

Goal runtime 会读取 session 中最新 structured plan，并把 ready/blocked/paused/failed 状态纳入继续判断。

Plan executor 已提供 dispatch abstraction：

- tool step。
- agent step。
- user_input step。
- checkpoint step。

自动执行能力已进入 `goal_after_done` 的协调链路，但后续仍应围绕真实复杂任务继续补强测试与 UI 可视化。

## 13. Session 与长期运行

### 13.1 Session 是 Working Memory

Session 负责：

- 当前多轮对话历史。
- tool call / tool result 配对。
- plan / todo 状态。
- compaction summary。
- 输入输出事件重建。
- API/UI history view。

它不是 long-term memory。

### 13.2 Session Entry

当前 entry 类型：

- `message`。
- `tool_call`。
- `tool_result`。
- `meta`。
- `compaction`。
- `plan`。
- `todo`。

Tool result 支持：

- `output`。
- `error`。
- `is_error`。
- `metadata`。
- `images`。

这使工具恢复信息可跨 turn 保留。

### 13.3 Plan/Todo

Plan/Todo 是 session 级 workflow state。

当前：

- `update_plan` 写入 `EntryTypePlan`。
- `todo` 写入 `EntryTypeTodo`。
- `session/state.StateStore` 负责写 session 与发 runtime events。
- compaction 会保留最新 plan 和 todo 状态。
- Goal runtime 会读取 open todo 作为未完成客观事实。

### 13.4 Rolling Session Summary

长会话不应无限重放完整历史。

当前已实现：

- `BuildRollingSummary`。
- `RewriteHistoryWithCompaction`。
- model-authored compaction entry。
- request-local summary context 下沉。
- compact 时保留最新 state entries 和 recent suffix。

Compaction 与 memory pipeline 是两条不同链路：

- Session compaction 是 working memory 压缩。
- Memory pipeline 是跨 session 的记忆治理。

### 13.5 Session Busy Queue

`internal/runtime/execution.Coordinator` 当前处理同一 agent/session 的 active run 协调。

配置：

- `runtime.sessions.queueMode`: `collect` / `followup` / `interrupt`。
- `runtime.sessions.queueDebounceMS`。
- `runtime.sessions.queueMaxPending`。
- `runtime.sessions.queueDropPolicy`: `summarize` / `drop_oldest` / `drop_newest`。

行为：

- active run 存在时，新消息先入 pending queue。
- queued message 不直接污染 session transcript。
- `collect` 模式合并 queued turns，并显式标出 earlier context 与 current message。
- `interrupt` 模式取消 active run，并保留最新 pending run。

这是 ingress/session 体验机制，不是内部 task 编排机制。

## 14. Skill 系统

### 14.1 Skill 层级

Skill 可见性分三层：

- system skills。
- shared project skills：`common/skills/`。
- private agent skills：`<agent_dir>/skills/`。

合并优先级：

```text
private > shared > system
```

当 `skills.inherit_shared: false` 时，当前 Agent 不继承 shared skills。

### 14.2 Resource Catalog

`internal/runtime/resources.Catalog` 当前构建：

- system/shared/private/effective skills 描述。
- 每个 Agent 的可见工具描述。
- 每个 Agent 的 scoped loader。
- global loader。

Catalog 用于能力发现、UI/API 展示和 runtime loader 复用。

注意：Catalog 中的 capability provider 是 metadata 占位，只用于描述工具能力，不是实际运行时依赖。

### 14.3 Skill 注入与按需读取

Prompt 默认只注入匹配 skill 的摘要。

如需完整正文，模型使用：

- `skill_get`。

这样保持低 token 成本，并避免把所有 skill 全量塞入 prompt。

## 15. Memory 系统

### 15.1 三层记忆

当前 memory layer：

- `candidates`：候选记忆。
- `episodic`：阶段性、近期任务态记忆。
- `long-term`：稳定事实、规则、偏好、决策。

用户心智：

- Session 是 working memory。
- `anyai/memory/` 是持久化 memory。
- `memory_search` / `memory_get` / `memory_save` 是主动回忆与写回工具。

### 15.2 读取路径

Memory manager 当前：

- 以 Markdown 文件为真相源。
- 使用 BM25 检索。
- 支持 scope：agent/session。
- 支持 explainable matches。
- read path 会按 refresh interval 感知磁盘变化。
- 相关性接近时结合更新、稳定性和 layer。

Prompt 默认注入 memory card/summary，而不是全文。

### 15.3 写回路径

Durable 写回主路径是：

- `memory_save`。

Runtime 不再通过自然语言正则或“用户确认口吻”自动沉淀 durable memory。

MemoryPipeline 当前保守：

- session end / agent call complete hook 主要做 cleanup 和未来结构化入口预留。
- 不从自由聊天文本自动生成长期记忆。

这样避免低质量对话、阶段性 chatter、多语言措辞差异污染 long-term memory。

### 15.4 Memory Control

HTTP/API 和 gateway control 当前提供：

- stats。
- search。
- get。
- stale cleanup。
- reindex。
- promote eligible。

Memory lifecycle 状态会进入 overview 和 UI。

## 16. 工具体系与安全

### 16.1 工具分类

当前工具可按职责理解为：

Core：

- `read_file`。
- `write_file`。
- `edit_file`。
- `bash`。
- `python`。
- `web_fetch`。
- `web_search`。
- `browser`。

Collaboration：

- `callagent`。

Context：

- `memory_search`。
- `memory_get`。
- `memory_save`。
- `skill_get`。
- `input_manifest`。
- `attachment_get`。

Workflow：

- `update_plan`。
- `todo`。
- `goal_complete`。
- `await_user_input`。
- `save_output`。

Daemon / messaging：

- `send_message`。
- `cron`。

实际可见工具由工具注册表、runtime extras、Agent allow/deny 和安全策略共同决定。

### 16.2 Tool Policy

每个 Agent 的最终工具集合由：

- 全局注册工具。
- Agent workspace-aware registry。
- runtime scoped extras。
- `tools.allow`。
- `tools.deny`。
- security exec policy。

共同决定。

当 Agent 配置 allow list 时，runtime 会额外确保 goal 工具可用于完成验证。

### 16.3 文件与命令安全

安全原则：

- 文件工具以 workspace 为相对路径基准。
- Agent workspace 必须在项目根内。
- 命令执行受 `security.exec_approvals` 策略和 allowlist 控制。
- Web 访问有 SSRF 防护。
- Provider 密钥优先来自环境变量或安全注入，不应硬编码到公开项目。

### 16.4 工具语义不偷改

工具恢复不通过偷改工具职责完成。

例如：

- `read_file` 读文件，不读目录。
- 遇到目录时，runtime 通过错误分类和建议引导模型使用 `bash` 检查目录。
- 参数修补只做确定性归一，不做猜测型补全。

## 17. 事件、Projection 与可观察性

### 17.1 事实事件

当前事件常量位于 `internal/runtime/events/event_names.go`。

Run lifecycle：

- `run.accepted`。
- `run.routed`。
- `run.queued`。
- `run.started`。
- `run.activity`。
- `run.incomplete`。
- `run.fallback_reply`。
- `run.completed`。
- `run.failed`。
- `run.aborted`。

Input / session：

- `input.received`。
- `input.normalized`。
- `attachment.stored`。
- `session.input.stored`。
- `session.output.stored`。
- `session.compact.requested`。
- `session.compact.completed`。

LLM / text：

- `text.delta`。
- `llm.retrying`。

Tool：

- `tool.call.requested`。
- `tool.call.started`。
- `tool.retrying`。
- `tool.warning`。
- `tool.completed`。
- `tool.failed`。
- `tool.fanout.completed`。

Agent call：

- `agent.call.started`。
- `agent.call.submitted`。
- `agent.call.completed`。
- `agent.call.failed`。

Task：

- `task.queued`。
- `task.started`。
- `task.running`。
- `task.completed`。
- `task.failed`。
- `task.cancelled`。

Memory：

- `memory.recalled`。
- `memory.captured`。

### 17.2 Recorder

`internal/runtime/events.Recorder` 当前负责：

- run record。
- run event append。
- persistent event store。
- pub/sub。
- run tree subscription。
- session subscription。
- listener dispatch。

Recorder 仍保留 `StartRun` / `BeginRun` / `FinishRun` 等状态更新 helper，但事件 log 已越来越接近权威 lifecycle narrative。

长期目标仍是让业务状态变化尽可能由标准事件解释，projection 从事件构建视图，replay 只补消费体验缺口。

### 17.3 Replay

`runtimeevents.ReplayRunEvents` 当前会：

- 对 terminal run 合成缺失的 `text.delta`。
- 对 terminal run 合成缺失的 terminal event。
- 重新排序 run tree replay events。

这使 API/UI/channel 能看到更稳定的消费流，即使某些 transient text delta 没有持久化。

### 17.4 Projection

`internal/runtime/projection` 当前组合：

- `RunView`。
- `SessionView`。
- `TaskView`。

Projection 是查询面权威，ControlService 负责 rebuild、session compact、memory maintenance、cancel 等控制操作。

### 17.5 UI/API 可观察性

Gateway 和 HTTP API 暴露：

- runs。
- run events。
- run tree。
- run tree events。
- sessions。
- session events。
- tasks。
- task events。
- memory stats/search/get/maintenance。
- logs stream。
- config。
- runtime overview。

Web UI 消费同一套 API 与事件，而不是绕过 runtime 内核。

## 18. API 面向对象

AnyAI API 围绕以下对象建模：

- Agent。
- Channel。
- Run。
- RunTree。
- Session。
- Task。
- Memory。
- Config。
- Logs。
- Runtime catalog/overview。

AnyAI 不对外暴露独立 workflow DSL 解析接口。

典型能力：

- 创建 run。
- 流式观察 run。
- 查询 run tree。
- 查询 session history。
- 查询 task 状态和事件。
- 查询 memory。
- 执行控制操作。

## 19. 当前已落地的关键能力

| 能力 | 当前状态 | 主路径 |
| --- | --- | --- |
| Project-first 解析 | 已落地 | `internal/registry` |
| RuntimeSpec | 已落地 | `internal/runtime/factory/spec.go` |
| RuntimeUpdater | 已落地 | `internal/startup/runtime_updater.go` |
| Gateway facades | 已落地 | `internal/gateway/service.go` |
| HTTP Plane 拆分 | 已落地 | `internal/startup/http/api/handler.go` |
| InputEnvelope | 已落地 | `internal/runtime/input` |
| Session coordinator | 已落地 | `internal/runtime/execution/sessionlane.go` |
| Prompt 骨架 | 已落地 | `internal/runtime/agent/prompt.go` |
| Request focus | 已落地 | `internal/runtime/agent/request_focus.go` |
| Transcript hygiene | 已落地 | `internal/runtime/agent/transcript_repair.go` |
| Tool preflight | 已落地 | `internal/runtime/agent/tool_preflight.go` |
| Tool recovery | 已落地 | `internal/runtime/agent/tool_recovery.go` |
| Incomplete turn fallback | 已落地 | `internal/runtime/agent/run_recovery.go` |
| doTask 内核 | 已落地 | `internal/runtime/task/runtime.go` |
| callagent inline fan-in | 已落地 | `internal/runtime/agent_call.go`、`internal/runtime/tool/callagent.go` |
| process task | 已落地 | `internal/runtime/task/builtin/process.go` |
| Turn lifecycle | 已落地 | `internal/runtime/turn` |
| Goal runtime | 已落地 | `internal/runtime/task/goal.go` |
| Structured plan | 已落地 | `internal/runtime/plan` |
| Plan/Todo session state | 已落地 | `internal/runtime/session/state` |
| Memory layers/search/save | 已落地 | `internal/runtime/memory`、`internal/runtime/tool/memory_tool.go` |
| Skill scope/catalog | 已落地 | `internal/runtime/skill`、`internal/runtime/resources` |
| Persistent events/replay/projection | 已落地 | `internal/runtime/events`、`internal/runtime/projection` |

## 20. 当前仍需收敛的边界

### 20.1 ExecutionDeps 仍偏宽

`runtimeport.ExecutionDeps` 当前仍是 runtime 内部可变依赖快照，包含 provider、config、stores、sender、runner、scheduler、skills、resources、memory、recorder、pipeline、task runtime、turn store 等。

短期可保留，但要防止它继续变成跨层协调总线。

原则：

- 它是 runtime 内部快照。
- startup/gateway 不应把它当成产品级 API。
- 新能力优先通过 RuntimeSpec、runtime service、control/projection surface 表达。

### 20.2 Gateway Service 实现仍较宽

Facade 已经拆出，但实现体 `gateway.Service` 仍聚合较多能力。

后续改动应继续让使用方依赖窄接口，而不是把所有新方法继续直接平铺到所有消费端。

### 20.3 事件事实源还可更纯

当前 `Recorder` 仍同时有 run record 状态 helper 和 event append。

下一步应继续减少状态覆盖式更新，让标准事件更完整表达 run lifecycle，projection 从事件解释状态。

### 20.4 Provider-aware transcript legality

当前 transcript repair 是通用策略。未来可增加 provider-aware legality 层，针对 Anthropic/OpenAI/Gemini 的不同 tool/message 约束做送模前校验。

### 20.5 Tool preflight 扩展接口

当前 preflight 是 runtime 内置规则。未来可暴露 tool-level `ToolInputRepairer`，但仍坚持只做确定性修补。

### 20.6 AttachmentStore 产品化

`AttachmentStore` 基础结构已存在，输入事件和 input tools 已落地。未来可继续补齐 Web UI/API 上传、持久附件目录、权限与清理策略。

### 20.7 Turn projection

Turn 当前是生命周期机制，不是外部 read model。若 UI/trace 需要展示 Turn，可新增 projection，但不要让模型侧感知 Turn。

### 20.8 历史字段和命名

需要逐步清理的历史残余：

- `IdleTimeoutMS` / `LastActivityAt` 与 Turn 的关系。
- 旧文档、测试或 UI 中的 `delegate` 命名。
- `followup` 在不同语境中的歧义，尤其要区分 session queue 与 task orchestration。

### 20.9 测试矩阵补强

继续补强：

- `followup` / `interrupt` session queue 专门测试。
- provider-aware transcript legality。
-复杂 multi-agent `callagent` + process + goal continuation。
- plan executor 与 Goal 自驱的端到端场景。
- attachment/input manifest 跨 CLI/HTTP/UI 渠道一致性。

## 21. 非目标

当前阶段明确不做：

- 不把 AnyAI 改造成多租户平台产品。
- 不引入独立 workflow DSL 作为核心。
- 不恢复模型侧 `task_wait` / background delegate 主路径。
- 不让 HTTP 绕过 Gateway 直连 Runtime。
- 不把所有行为逻辑塞回 `agent.md`。
- 不通过偷改工具语义解决 prompt 问题。
- 不把 memory durable 写回建立在自由文本正则猜测上。
- 不为每个 channel 复制一套执行逻辑。

## 22. 后续实施顺序

推荐按收敛优先级推进：

1. 稳定当前主链路。
   保持 `gateway -> runtime/execution -> agent -> task -> events/projection` 不新增旁路。

2. 收紧事件事实源。
   让 run/task/session/memory 状态尽可能由标准事件解释，减少 replay 和 projection 特判。

3. 补齐 provider-aware transcript hygiene。
   在送模前验证 provider-specific role/tool ordering。

4. 继续压缩工具 batch 与 task batch 的重复边界。
   保持 `task.Runtime` 是唯一内部任务内核。

5. 完善附件与多模态输入产品体验。
   让 CLI、HTTP、Web UI、消息渠道共享同一输入和附件模型。

6. 强化 Goal/Plan E2E。
   让结构化计划、自驱继续、用户输入阻断、hard stop handoff 都有稳定测试和 UI 呈现。

7. 目录与命名整理。
   在行为稳定后，再考虑 `internal/startup/http` 等产品壳目录命名调整。

## 23. 判断标准

后续所有设计和重构都用以下问题校验：

1. 新入口是否仍然先进入 Gateway？
2. Runtime 是否仍然不反向依赖 Gateway/Channel/HTTP？
3. 新编排是否复用了 `task.Runtime.DoTask`？
4. 新状态是否能通过标准事件或 projection 解释？
5. 新能力是否保持 `agent.md + anyai.yaml + SKILL.md` 的作者心智？
6. Channel 是否只是适配与展示？
7. Tool 语义是否保持单一而清晰？
8. Memory 写回是否避免自由文本猜测？
9. Prompt 增强是否来自 runtime 事实，而不是业务模板硬编码？
10. 是否减少了重复层，而不是制造新的平行架构？

如果答案是否定的，说明改动正在偏离 AnyAI 的最终设计。

## 24. 最终结论

AnyAI 的最终形态可以压缩为一句话：

**用 `agent.md` 定义 Agent，用 `anyai.yaml` 定义项目运行规格，用 `SKILL.md` 与 `anyai/memory/` 提供可复用上下文，用 Gateway 统一接入，用 Runtime 内核统一执行、纠偏、记忆、事件与观测，用 `doTask` 管理所有 agent/tool/process 工作。**

当前代码已经从早期“Agent 文件化 + 委派工具”的基础设想，推进到更完整的运行时系统：

- 项目定义层已清晰。
- RuntimeSpec / RuntimeUpdater 已落地。
- Gateway facade 与 HTTP plane 已拆分。
- `doTask + callback + join` 已成为内部任务内核。
- `callagent` 已收敛为 inline 单任务/并行 fan-in 协作语义。
- Turn、Goal、Plan、Todo、Session compaction、Memory lifecycle、Tool recovery、Transcript hygiene、Request focus、Event replay/projection 已进入主链路。

因此，AnyAI 后续最重要的方向不是继续扩张概念，而是保持主路径稳定，清理历史命名和重复边界，把已有 runtime 智能沉淀成更可靠的事件事实、测试矩阵和产品体验。
