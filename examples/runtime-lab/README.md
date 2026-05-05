# Runtime Lab

这个示例专门用来验证和学习 AnyAI 的运行时能力，而不是某个业务场景。它是当前最适合新用户理解“Runtime 在背后做了什么”的示例。

默认模型为本地 `ollama/qwen3:1.7b`。如果要切云端模型，改 `anyai.yaml` 里的 `models.default` 和对应 provider。

## 适合验证什么

- 统一输入块：文本、文件、目录、URL、图片、PDF。
- 输入清单和附件元信息工具：`input_manifest`、`attachment_get`。
- 会话内计划与待办：`update_plan`、`todo`。
- 记忆检索：`memory_search`、`memory_get`。
- inline `callagent` 编排：单子 Agent、并行子 Agent、自动 fan-in。
- Runtime task 观测：`GET /api/tasks`、`GET /api/tasks/{taskID}`、`GET /api/tasks/{taskID}/events?stream=1`。
- Run tree 观测：`GET /api/runs/{runID}/tree`。

## Agent

- 入口 Agent：`runtime-lab`
- 子 Agent：`summary-worker`

## CLI 快速体验

```bash
anyai chat --project ./examples/runtime-lab
```

可以直接试这些自然语言：

- “请根据当前输入先帮我列一个两步计划。”
- “请从记忆里找出这个示例的长期规则和当前演示重点。”
- “请让 summary-worker 这个子 Agent 先准备摘要，然后把结果告诉我。”
- “请同时让两个子 Agent 分头处理，再把结果合并给我。”
- “请同时让三个子 Agent 并行完成不同子任务，再统一汇总。”

模型侧只需要使用 `callagent`，Runtime 会负责提交 `doTask(kind=agent)`、等待结果、fan-in 并把结果回注给父 Agent。

## CLI 输入引用

```bash
anyai chat --project ./examples/runtime-lab
```

然后在 CLI 中输入：

```text
请结合 @file:./examples/runtime-lab/fixtures/brief.txt 和 @dir:./examples/runtime-lab/fixtures/reference 先整理输入，再给我一个两步计划。
```

## HTTP + SSE

启动 Gateway：

```bash
anyai start --project ./examples/runtime-lab
```

本示例未显式配置 `channels.http.listen`，默认监听 `127.0.0.1:2333`。

请求示例：

```bash
curl -N -X POST 'http://127.0.0.1:2333/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "agent_id":"runtime-lab",
    "session_id":"runtime-demo",
    "inputs":[
      {"type":"text","text":"请先梳理输入，再建立计划。"},
      {"type":"file","name":"brief.txt","path":"./examples/runtime-lab/fixtures/brief.txt"},
      {"type":"dir","name":"reference","path":"./examples/runtime-lab/fixtures/reference"},
      {"type":"url","url":"https://example.com/runtime-lab"}
    ]
  }'
```

常用观测：

```bash
curl -s http://127.0.0.1:2333/api/tasks | jq
curl -s http://127.0.0.1:2333/api/sessions/runtime-lab/runtime-demo | jq
curl -s http://127.0.0.1:2333/api/memory/stats | jq
```
