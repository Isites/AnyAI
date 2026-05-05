# 电商客服系统测试清单

## 准备

CLI：

```bash
goclaw run ./examples/ecommerce-cs
```

HTTP Gateway：

```bash
goclaw start --project ./examples/ecommerce-cs
```

## 核心测试

### TC-1 简单问候

输入：

```text
你好
```

预期：

- `main-cs` 正确识别普通问候
- 返回友好回复
- 询问用户具体需要什么帮助

### TC-2 退款分发

输入：

```text
我想退款，订单号是 ORD123456
```

预期：

- `main-cs` 识别退款意图
- 委派给 `refund-specialist`
- 保留订单号上下文

### TC-3 并行查询

输入：

```text
帮我查一下订单 ORD123456 的状态，还有 iPhone 15 有库存吗？
```

预期：

- `main-cs` 识别出两个独立子任务
- 使用 `callagent` 并行调用 `order-query` 和 `inventory-query`
- 汇总两个结果后统一回复用户

参考委派载荷：

```json
{
  "mode": "parallel",
  "tasks": [
    { "target_agent": "order-query", "task": "查询订单 ORD123456 状态" },
    { "target_agent": "inventory-query", "task": "查询 iPhone 15 库存" }
  ]
}
```

### TC-4 多层委派

输入：

```text
我要退款，订单号是 ORD123456
```

预期调用链：

```text
main-cs
→ refund-specialist
→ order-query
```

### TC-5 用户历史增强推荐

输入：

```text
我是老客户，帮我推荐一个手机
```

预期：

- 先查看 `user-history`
- 再调用 `product-specialist`
- 推荐结果体现历史偏好

## HTTP Smoke Test

```bash
curl -X POST http://127.0.0.1:18890/api/v1/chat \
  -H "Content-Type: application/json" \
  -d '{
    "agentId": "main-cs",
    "text": "我想退款，订单号是 ORD123456"
  }'
```

可额外检查：

- `GET /health`
- `GET /api/v1/agents`
- `GET /api/v1/agents/main-cs/status`
- `POST /api/v1/chat/stream`
echo "=== 测试完成 ==="
```

---

## 测试检查清单

| 测试用例 | 状态 | 备注 |
|---------|------|------|
| TC-1 单 Agent 对话 | ⬜ | |
| TC-2 意图识别分发 | ⬜ | |
| TC-3 并行调用 | ⬜ | 关键测试 |
| TC-4 多层调用 | ⬜ | |
| TC-5 用户历史影响 | ⬜ | |
| TC-6 错误处理 | ⬜ | |
| TC-7 退款全流程 | ⬜ | 综合测试 |
| TC-8 HTTP Channel | ⬜ | |
| TC-9 流式响应 | ⬜ | |
| TC-10 性能测试 | ⬜ | |
