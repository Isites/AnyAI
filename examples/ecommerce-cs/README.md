# 电商客服系统

这个示例展示一个标准的 AnyAI 多 Agent 项目如何完成意图识别、任务分发、并行查询、多层 Agent 调用和结果汇总。默认模型为本地 `ollama/qwen3:1.7b`。

它也是最适合体验 Web UI、HTTP API 和 SSE 的示例，因为 `anyai.yaml` 显式监听 `127.0.0.1:18890`。

## Agent 拓扑

- `main-cs`：根入口，负责理解用户意图、收集信息、调用子 Agent 和汇总结果。
- `refund-specialist`：退款、退货和售后。
- `logistics-specialist`：物流查询和配送问题。
- `product-specialist`：商品咨询和推荐。
- `order-query`：订单状态和详情。
- `inventory-query`：库存和到货通知。
- `user-history`：购买历史与用户偏好。

## 运行

CLI：

```bash
anyai chat --project ./examples/ecommerce-cs
```

Gateway：

```bash
anyai start --project ./examples/ecommerce-cs
```

启动后：

- Web UI：`http://127.0.0.1:18890/chat`
- API Catalog：`http://127.0.0.1:18890/api/catalog`
- API Deck：`http://127.0.0.1:18890/ui/api`

SSE 请求：

```bash
curl -N -X POST 'http://127.0.0.1:18890/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "session_id": "demo-ecommerce",
    "text": "帮我查一下订单 ORD123456 的状态，还有 iPhone 15 有库存吗？"
  }'
```

## 协作方式

用户只说自然语言，主 Agent 自己决定是否调用子 Agent。

意图分发：

```text
“我想退款，订单号是 ORD123456”
→ main-cs
→ refund-specialist
```

并行查询：

```text
“帮我查订单 ORD123456 的状态，还有 iPhone 15 有没有库存”
→ main-cs
→ 并行 callagent:
   - order-query
   - inventory-query
→ main-cs fan-in 汇总
```

多层调用：

```text
main-cs
→ refund-specialist
→ order-query
```

这里的 `callagent` 是 inline 协作语义。Runtime 会把它收敛到内部 `doTask(kind=agent)`，自动跟踪 task、run tree、事件和取消，不需要模型自己轮询任务状态。

## 目录结构

```text
ecommerce-cs/
├── anyai.yaml
├── agent.md                    # main-cs（默认入口）
├── agents/
│   ├── refund-specialist/agent.md
│   ├── logistics-specialist/
│   │   ├── agent.md
│   │   └── skills/logistics/SKILL.md
│   ├── product-specialist/agent.md
│   ├── order-query/agent.md
│   ├── inventory-query/agent.md
│   └── user-history/agent.md
├── data/
├── tests/
├── README.md
└── TEST_CASES.md
```

## 数据文件

- `data/orders.json`：订单样本。
- `data/inventory.json`：库存样本。
- `data/users.json`：用户样本。

## 推荐测试问题

- “你好”
- “我想退款，订单号是 ORD123456”
- “帮我查一下订单 ORD123456 的状态，还有 iPhone 15 有库存吗？”
- “我是老客户，帮我推荐一个手机”

## 运行时观测

```bash
curl -s http://127.0.0.1:18890/api/tasks | jq
curl -s http://127.0.0.1:18890/api/runs | jq
curl -s http://127.0.0.1:18890/api/sessions/main-cs/demo-ecommerce | jq
```
