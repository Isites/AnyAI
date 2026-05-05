# AnyAI

AnyAI 是一个“项目优先”的 Agent 开发框架与运行时。把 `anyai.yaml`、`agent.md`、`SKILL.md` 放进一个目录后，就可以通过 CLI、浏览器页面、HTTP API 和 SSE 流式事件跑起来。

它的核心目标是：让非框架开发者也能用 Markdown 和少量 YAML，构建可运行、可组合、可扩展的 AI Agent 系统。

## 3 分钟跑起来

### 1. 确认可执行文件

仓库根目录约定使用这个可执行文件：

```bash
./anyai version
```

如果你看到版本输出，说明可以继续下一步。

### 2. 准备模型

多数示例默认可以走本地 Ollama：

```bash
ollama list
ollama run qwen3:1.7b
```

如果 `qwen3:1.7b` 还没下载，`ollama run` 会自动拉取。要切云端模型，改对应示例里的 `models.default` 和 `providers` 即可。

### 3. 先跑 CLI

```bash
./anyai chat --project ./examples/runtime-lab
```

`runtime-lab` 是当前最适合学习运行时能力的示例，能看到统一输入、plan/todo、memory、`callagent` 和 task 观测这些主链路。

### 4. 再启动浏览器和 API

```bash
./anyai start --project ./examples/ecommerce-cs
```

`ecommerce-cs` 显式监听 `127.0.0.1:18890`。启动后可以打开：

- Web UI: `http://127.0.0.1:18890/chat`
- API Catalog: `http://127.0.0.1:18890/api/catalog`
- API Deck: `http://127.0.0.1:18890/ui/api`
- SSE Chat: `POST http://127.0.0.1:18890/api/chat?stream=1`

示例请求：

```bash
curl -N -X POST 'http://127.0.0.1:18890/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "session_id": "demo-ecommerce",
    "text": "帮我查一下订单 ORD123456 的状态，还有 iPhone 15 有库存吗？"
  }'
```

当前推荐的实时接入方式是 HTTP + SSE，不再把 WebSocket 作为主入口。

## 现在应该怎么理解 AnyAI

AnyAI 不是一个预置业务产品，而是一个 Agent Runtime Framework。

项目作者主要写：

- `anyai.yaml`：模型、Provider、runtime、memory、channel、安全与日志配置。
- `agent.md`：Agent 身份、模型、工具权限、workspace、系统指令与行为边界。
- `SKILL.md`：系统级、项目共享级、Agent 私有级的可复用能力说明。
- `anyai/`：运行时数据目录，保存 sessions、memory、events、logs。

运行时负责：

- Gateway 统一接入 CLI、HTTP/SSE、Web UI 和消息渠道。
- `callagent` 通过内部 `doTask(kind=agent)` 调用子 Agent，并自动 fan-out / fan-in。
- Tool / Process / Agent 调用共享 task 生命周期、事件、取消和观测。
- Session、Transcript Hygiene、Plan/Todo、Memory、Skill 自动进入 Agent 上下文。
- 工具失败会结构化记录，包含 retry、warning、repair、suggested next moves 等恢复信息。
- Goal/Plan 机制用于长期任务的继续判断和收口。

## 常用命令

```bash
./anyai chat --project ./examples/runtime-lab
./anyai start --project ./examples/ecommerce-cs
./anyai init demo-project
```

- `chat [agent-id]`：进入当前项目的 CLI 聊天。
- `start [agent-id]`：启动长期运行的 Gateway，提供 Web UI、HTTP API 和 SSE。
- `init [dir]`：生成一个新的 AnyAI 项目模板。

`--project` 可以指向项目目录，也可以指向某个 `agent.md`。

## 按目标选择示例

- `examples/runtime-lab`：学习运行时能力，包含统一输入、plan/todo、memory、`callagent`、task 观测。
- `examples/ecommerce-cs`：完整业务案例，展示多 Agent 分流、并行查询和私有技能。
- `examples/parallel-workflow`：最小多 Agent 并行协作示例。
- `examples/single-agent`：最小单 Agent 形态，适合看 `agent.md` 如何表达行为。
- `examples/harness-analytics`：分析工作流和共享技能。
- `examples/harness-coding`：编码、测试、审查协作流。
- `examples/harness-google-review`：审核闭环和共享规则库。

更详细的示例说明在 [examples/README.md](./examples/README.md)。

## 项目结构

推荐结构：

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
│       └── agent.md
└── anyai/
    ├── sessions/
    ├── memory/
    │   ├── candidates/
    │   ├── episodic/
    │   └── long-term/
    ├── events/
    └── runtime.log
```

规则很简单：

- 一个目录里有 `agent.md`，这个目录就定义一个 Agent。
- 根目录 `agent.md` 是默认入口候选。
- `agents/*/agent.md` 是可被 `callagent` 调用的子 Agent。
- `common/skills/` 是项目共享技能。
- `<agent_dir>/skills/` 是 Agent 私有技能。
- `anyai/` 是运行时数据目录，不是作者必须手写的业务配置。

## Provider 配置

`anyai.yaml` 负责运行配置，不替代 `agent.md`。

常见 Provider 形态：

```yaml
name: demo

models:
  default: ollama/qwen3:1.7b
  aliases:
    strong: anthropic/claude-sonnet-4-5

providers:
  ollama:
    kind: openai-compatible
    base_url: http://127.0.0.1:11434/v1

  openai:
    kind: openai
    api_key_env: OPENAI_API_KEY
    headers:
      HTTP-Referer: https://your-app.example
      X-Title: AnyAI Demo
    headers_env: OPENAI_HEADERS_JSON
```

`headers_env` 接收 JSON 对象字符串：

```bash
export OPENAI_HEADERS_JSON='{"HTTP-Referer":"https://your-app.example","X-Title":"AnyAI Demo"}'
```

这种方式适合接入代理、灰度路由或自定义鉴权，避免把敏感 header 写死在配置文件里。

## 切到云端模型

改两处即可：

1. 把 `models.default` 改成目标模型。
2. 配好对应 provider 的 API Key 环境变量。

例如 OpenAI：

```yaml
models:
  default: openai/gpt-4.1-mini

providers:
  openai:
    kind: openai
    api_key_env: OPENAI_API_KEY
```

```bash
export OPENAI_API_KEY=your-key
./anyai chat --project ./examples/runtime-lab
```

## HTTP / SSE 常用入口

启动 Gateway 后，优先使用：

- `POST /api/chat?stream=1`：推荐聊天入口，同一请求创建 run 并返回 SSE。
- `POST /api/runs`：显式创建 run，适合自己管理 run id 的调用方。
- `GET /api/runs/{runID}/events?stream=1`：订阅 run 事件。
- `GET /api/runs/{runID}/tree`：查看主 Agent 与子 Agent 的运行树。
- `GET /api/tasks`：查看 task 列表。
- `GET /api/tasks/{taskID}/events?stream=1`：订阅单个 task。
- `GET /api/sessions/{agentID}/{sessionID}`：读取会话历史。
- `GET /api/memory/stats`、`GET /api/memory/search`：查看和检索 memory。
- `GET /api/catalog`：读取 HTTP/SSE 端点、事件契约和请求样例。

## 常见问题

### 页面打不开

先确认你启动的是启用了 HTTP channel 的项目。例如：

```bash
./anyai start --project ./examples/ecommerce-cs
```

`ecommerce-cs` 配置了：

```yaml
channels:
  gateway:
    enabled:
      - cli
      - http
  http:
    listen: 127.0.0.1:18890
```

没有显式配置 HTTP listen 的项目会使用默认监听地址。以当前项目的 `anyai.yaml` 为准。

### 我完全不懂 Agent，先看哪里

先跑：

```bash
./anyai chat --project ./examples/runtime-lab
```

再看这几个文件：

- `examples/runtime-lab/anyai.yaml`
- `examples/runtime-lab/agent.md`
- `examples/runtime-lab/README.md`
- `docs/anyai-design.md`

先跑通，再看更复杂的多 Agent 示例，学习成本最低。
