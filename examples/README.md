# Examples

这些目录是 AnyAI 的示例 Agent 工程。每个示例都是一个完整项目：`anyai.yaml` 定义运行配置，`agent.md` 定义入口 Agent，`agents/*/agent.md` 定义可被 `callagent` 调用的子 Agent。

多数示例默认可以走本地 `ollama/qwen3:1.7b`。个别 harness 示例可能带有用于验证的远端 provider 配置，实际运行前请以对应项目的 `anyai.yaml` 为准。

## 前置条件

本地模式需要 Ollama 已启动，并且模型可用：

```bash
ollama list
ollama run qwen3:1.7b
```

云端模型只需要改示例里的 `models.default` 和对应 `providers`。

## 常用进入方式

### CLI 聊天

在仓库根目录：

```bash
anyai chat --project ./examples/runtime-lab
```

或者进入示例目录后直接运行：

```bash
cd ./examples/runtime-lab
anyai chat
```

### Gateway / Web UI / HTTP + SSE

```bash
anyai start --project ./examples/ecommerce-cs
```

如果项目没有显式配置 `channels.http.listen`，默认监听 `127.0.0.1:2333`。`ecommerce-cs` 显式监听 `127.0.0.1:18890`，适合直接体验浏览器和 API：

- Web UI：`http://127.0.0.1:18890/chat`
- API Catalog：`http://127.0.0.1:18890/api/catalog`
- API Deck：`http://127.0.0.1:18890/ui/api`
- SSE Chat：`POST http://127.0.0.1:18890/api/chat?stream=1`

当前推荐实时接入方式是 HTTP + SSE。

## 按能力选示例

- `runtime-lab`：统一输入、plan/todo、memory 工具、inline `callagent`、task/run tree 观测。
- `ecommerce-cs`：业务型多 Agent 分流、并行查询、私有技能和 HTTP/SSE 示例。
- `parallel-workflow`：自然语言驱动的最小多 Agent 并行协作。
- `single-agent`：最小单 Agent、静态 memory、基础会话续接。
- `harness-analytics`：分析工作流、共享技能和多角色结论校验。
- `harness-coding`：编码、测试、审查协作流。
- `harness-google-review`：审核闭环和共享规则库。

## 每个示例值得试什么

### `runtime-lab`

- 安利：最适合理解当前 AnyAI 运行时能力。
- 看点：统一输入、计划/待办、记忆检索、inline 子 Agent、task 观测。
- 入口 Agent：`runtime-lab`

```bash
anyai chat --project ./examples/runtime-lab
anyai start --project ./examples/runtime-lab
```

### `ecommerce-cs`

- 安利：最接近日常业务的分流示例。
- 看点：主 Agent 把订单、库存、退款、物流拆给不同子 Agent；`logistics-specialist` 有私有技能。
- 入口 Agent：`main-cs`
- HTTP：`127.0.0.1:18890`

```bash
anyai chat --project ./examples/ecommerce-cs
anyai start --project ./examples/ecommerce-cs
```

### `parallel-workflow`

- 安利：最小多 Agent 并行协作示例。
- 看点：用户说自然语言，主 Agent 内部自己并行 `callagent`，Runtime 自动 fan-out / fan-in。
- 入口 Agent：`parallel-researcher`

```bash
anyai chat --project ./examples/parallel-workflow
anyai start --project ./examples/parallel-workflow
```

### `single-agent`

- 安利：最小单 Agent 入门盘。
- 看点：`agent.md` 如何表达单 Agent 行为；目录里有静态 `anyai/memory/` 示例。
- 入口 Agent：`single-agent`

```bash
anyai chat --project ./examples/single-agent
anyai start --project ./examples/single-agent
```

### `harness-analytics`

- 安利：强调“先验证，再分析，再优化”的循环分析流。
- 看点：共享技能、数据验证、竞争分析、趋势预测、报告生成。
- 入口 Agent：`data-analyst`

```bash
anyai chat --project ./examples/harness-analytics
anyai start --project ./examples/harness-analytics
```

### `harness-coding`

- 安利：把架构、实现、测试、审查放在一个循环门控里。
- 看点：共享编码规范和多 Agent 编码协作。
- 入口 Agent：`tech-lead`

```bash
anyai chat --project ./examples/harness-coding
anyai start --project ./examples/harness-coding
```

### `harness-google-review`

- 安利：演示“审计 -> 需求 -> 修复 -> 验证 -> 重审”的治理型工作流。
- 看点：审核规则库、并行审计和闭环推进。
- 入口 Agent：`review-lead`

```bash
anyai chat --project ./examples/harness-google-review
anyai start --project ./examples/harness-google-review
```

## 会话续接和会话隔离

推荐用 `runtime-lab` 验证会话。先启动 Gateway：

```bash
anyai start --project ./examples/runtime-lab
```

`runtime-lab` 未显式配置端口，默认是 `127.0.0.1:2333`。

### 同一个会话继续对话

```bash
curl -N -X POST 'http://127.0.0.1:2333/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "agent_id": "runtime-lab",
    "session_id": "demo-a",
    "text": "请记住，我叫阿明。"
  }'
```

继续使用同一个 `session_id`：

```bash
curl -N -X POST 'http://127.0.0.1:2333/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "agent_id": "runtime-lab",
    "session_id": "demo-a",
    "text": "我刚才叫什么？"
  }'
```

读取会话历史：

```bash
curl -s http://127.0.0.1:2333/api/sessions/runtime-lab/demo-a | jq
```

新建 `demo-b` 就是另一条独立会话：

```bash
curl -N -X POST 'http://127.0.0.1:2333/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "agent_id": "runtime-lab",
    "session_id": "demo-b",
    "text": "我刚才叫什么？如果这是新会话就直接说你不知道。"
  }'
```

会话文件会落到项目运行目录：

```text
examples/runtime-lab/anyai/sessions/runtime-lab/demo-a.jsonl
examples/runtime-lab/anyai/sessions/runtime-lab/demo-b.jsonl
```

## HTTP/SSE 观测入口

启动 Gateway 后常用：

- `POST /api/chat?stream=1`：推荐聊天入口。
- `POST /api/runs`：显式创建 run。
- `GET /api/runs/{runID}/events?stream=1`：订阅 run 事件。
- `GET /api/runs/{runID}/tree`：查看主 Agent 与子 Agent 的运行树。
- `GET /api/tasks`：查看 task 列表。
- `GET /api/tasks/{taskID}/events?stream=1`：订阅单 task。
- `GET /api/sessions/{agentID}/{sessionID}`：读取会话历史。
- `GET /api/memory/stats`、`GET /api/memory/search`：查看和检索 memory。
- `GET /api/catalog`：读取 HTTP/SSE 端点、事件契约和请求样例。

## 示例侧验证

```bash
go test ./examples/...
```

默认会运行示例目录里的结构性测试。

真实 E2E：

```bash
ANYAI_EXAMPLE_E2E=1 go test ./examples -run TestExampleProjectsE2E -count=1
```

CLI 和能力型 E2E：

```bash
ANYAI_EXAMPLE_E2E=1 go test ./examples -run 'TestExampleProjectsChatModeE2E|TestExampleLiveCapabilitiesE2E' -count=1
```

常用可选环境变量：

- `ANYAI_EXAMPLE_ONLY=parallel-workflow`
- `ANYAI_EXAMPLE_MODEL=ollama/qwen3:1.7b`
- `ANYAI_EXAMPLE_PROVIDER=ollama`
- `ANYAI_EXAMPLE_BASE_URL=http://127.0.0.1:11434/v1`
- `ANYAI_EXAMPLE_TIMEOUT=5m`

真实多 channel 五轮 E2E：

```bash
ANYAI_EXAMPLE_CHANNEL_E2E=1 \
ANYAI_EXAMPLE_ONLY=parallel-workflow \
ANYAI_EXAMPLE_MODEL=ollama/qwen3:1.7b \
ANYAI_EXAMPLE_TIMEOUT=5m \
go test ./examples -run TestExampleProjectsMultiChannelFiveTurnE2E -count=1 -timeout 20m
```

对 `parallel-workflow` 和 `ecommerce-cs` 这类多 Agent 样例，建议显式加大 `go test -timeout`。
