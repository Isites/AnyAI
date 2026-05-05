# Harness Analytics

H5 买量与变现数据分析系统。根 Agent `data-analyst` 负责把业务问题拆成“数据验证、竞争分析、趋势预测、优化建议、报告输出”几个阶段，并通过 inline `callagent` 调度子 Agent。

默认模型为本地 `ollama/qwen3:1.7b`。如需云端模型，修改 `anyai.yaml` 的 `models.default` 和 provider 配置。

## 规模

总计 9 个 Agent：

- 1 个主控：`data-analyst`
- 1 个数据验证：`data-validator`
- 3 个竞争分析师：`analyst-growth`、`analyst-product`、`analyst-monetization`
- 1 个预测：`forecaster`
- 2 个优化：`ua-optimizer`、`monetization-optimizer`
- 1 个报告生成：`reporter`

## 运行

CLI：

```bash
anyai chat --project ./examples/harness-analytics
```

Gateway / HTTP + SSE：

```bash
anyai start --project ./examples/harness-analytics
```

本示例未显式配置 HTTP 端口，默认监听 `127.0.0.1:2333`。

```bash
curl -N -X POST 'http://127.0.0.1:2333/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "agent_id": "data-analyst",
    "session_id": "analytics-demo",
    "text": "最近 H5 买量成本上升但转化下降，请先验证数据，再做竞争分析和优化建议。"
  }'
```

## 协作方式

`data-analyst` 会根据任务阶段调用子 Agent。模型侧使用 `callagent`，Runtime 内部统一走 `doTask(kind=agent)`，并自动等待子结果、合并并行结果、记录 task 和 run tree。

典型流程：

```text
data-analyst
→ data-validator
→ 并行 callagent:
   - analyst-growth
   - analyst-product
   - analyst-monetization
→ forecaster
→ ua-optimizer / monetization-optimizer
→ reporter
→ data-analyst 汇总
```

## 目录结构

```text
harness-analytics/
├── anyai.yaml
├── agent.md                    # Data Analyst Lead（默认入口）
├── agents/
│   ├── data-validator/agent.md
│   ├── analyst-growth/agent.md
│   ├── analyst-product/agent.md
│   ├── analyst-monetization/agent.md
│   ├── forecaster/agent.md
│   ├── ua-optimizer/agent.md
│   ├── monetization-optimizer/agent.md
│   └── reporter/agent.md
└── common/skills/
    ├── metrics-dictionary/SKILL.md
    └── channel-knowledge/SKILL.md
```

## 适用场景

- 买量效果下降排查。
- 变现指标异常分析。
- H5 产品漏斗和体验诊断。
- 需要“多视角竞争分析 + 结论验证”的业务问题。

## 运行时观测

```bash
curl -s http://127.0.0.1:2333/api/tasks | jq
curl -s http://127.0.0.1:2333/api/runs | jq
curl -s http://127.0.0.1:2333/api/sessions/data-analyst/analytics-demo | jq
```
