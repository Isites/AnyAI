# Harness Google Review

H5 站点 Google 审核循环修复系统。根 Agent `review-lead` 负责并行审计、生成修复需求、执行修复、回归验证和重新审计。

默认模型为本地 `ollama/qwen3:1.7b`。如需云端模型，修改 `anyai.yaml` 的 `models.default` 和 provider 配置。

## 规模

总计 7 个 Agent：

- 1 个主控：`review-lead`
- 4 个并行审计员：`quality-auditor`、`seo-auditor`、`ux-auditor`、`policy-auditor`
- 1 个需求生成：`requirement-generator`
- 1 个验证：`qa-verifier`

## 运行

CLI：

```bash
anyai chat --project ./examples/harness-google-review
```

Gateway / HTTP + SSE：

```bash
anyai start --project ./examples/harness-google-review
```

本示例未显式配置 HTTP 端口，默认监听 `127.0.0.1:2333`。

```bash
curl -N -X POST 'http://127.0.0.1:2333/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "agent_id": "review-lead",
    "session_id": "google-review-demo",
    "text": "请对这个 H5 站点做一次 Google 审核预检，先并行审计，再生成修复需求和验证计划。"
  }'
```

## 协作方式

`review-lead` 使用 inline `callagent` 组织审计和验证。Runtime 会统一管理 `doTask(kind=agent)`、task 生命周期、run tree、事件和取消，不需要模型自己轮询任务状态。

典型流程：

```text
review-lead
→ 并行 callagent:
   - quality-auditor
   - seo-auditor
   - ux-auditor
   - policy-auditor
→ requirement-generator
→ qa-verifier
→ review-lead 汇总下一轮建议
```

## 目录结构

```text
harness-google-review/
├── anyai.yaml
├── agent.md                    # Review Lead（默认入口）
├── agents/
│   ├── quality-auditor/agent.md
│   ├── seo-auditor/agent.md
│   ├── ux-auditor/agent.md
│   ├── policy-auditor/agent.md
│   ├── requirement-generator/agent.md
│   └── qa-verifier/agent.md
└── common/skills/
    ├── google-quality-guidelines/SKILL.md
    └── common-rejection-reasons/SKILL.md
```

## 适用场景

- 站点被 Google 拒审后做系统排查。
- 上线前做质量预审。
- 多次拒审后梳理结构性问题。
- 需要把“审计、需求、修复、验证”拆成标准循环。

## 运行时观测

```bash
curl -s http://127.0.0.1:2333/api/tasks | jq
curl -s http://127.0.0.1:2333/api/runs | jq
curl -s http://127.0.0.1:2333/api/sessions/review-lead/google-review-demo | jq
```
