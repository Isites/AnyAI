---
name: harness-google-review-http
description: H5 上线预审技能。通过 HTTP 调用独立的 harness-google-review AnyAI 项目，提交站点审核/复审任务并轮询 run 完成，返回审核结论和问题清单。
tags:
  - h5
  - online
  - 上线
  - 审核
  - review
  - http
  - microservice
---

# Harness Google Review HTTP Skill

使用本技能时，把 `examples/harness-google-review/` 当作独立微服务项目。不要修改该目录内容。

## 服务地址

默认服务地址：`http://127.0.0.1:2331`

默认 agent：

- `review-lead`

`h5-online` 只调用已存在的 HTTP 网关，不负责启动、停止、重启或杀死 `harness-google-review` 进程。如果该地址不可达，直接报告外部依赖阻塞，让用户或外部编排系统处理服务生命周期。

## 什么时候使用

每次需要站点初审或复审时使用本技能。主 agent 只负责提供站点 URL、源码路径、审核轮次和上一轮修复摘要；本技能负责把这些信息转成正式 HTTP 调度脚本调用。

## 调用方式

**⚠️ 重要：必须使用 python 工具执行**

由于审核任务可能需要较长运行时间（超过2分钟），必须使用 python 工具而不是 bash 工具来执行 `anyai_http_run.py` 脚本。

### Python 工具调用

```json
{
  "tool": "python",
  "input": {
    "file": "common/skills/scripts/anyai_http_run.py",
    "args": [
      "--base-url", "$H5_REVIEW_BASE_URL",
      "--agent-id", "review-lead",
      "--session-id", "h5-online-review-<site-slug>-r<round>",
      "--timeout", "3600",
      "--text", "审核任务内容"
    ],
    "timeout": 3600
  }
}
```

### 超时配置说明

- **脚本超时 (`--timeout`)**: 至少 3600秒（60分钟），控制脚本等待远端 run 完成的最大时间
- **Python 超时 (`timeout`)**: 至少 3600秒（60分钟），必须与脚本超时保持一致
- **重要**: 两个超时参数必须都设置且至少为 3600 秒，避免提前超时导致长时间运行的审核任务中断

### Session 复用机制

**自动复用**：
- 你只负责给脚本传入稳定的 `session_id`
- run_id 恢复、状态文件位置、活动 run 复用、failed/aborted 后的新 run 创建，都由 `anyai_http_run.py` 内部保证
- 同一轮次（rN）的前 3 次重试必须使用完全相同的 `session_id`，以复用远端 session 和已有进度
- 只有同一个 `session_id` 连续 3 次仍未成功时，才允许切换到新的 `session_id` 脱困；新 session_id 必须追加 `-retry<N>` 后缀，例如 `h5-online-review-<site-slug>-r<round>-retry1`
- 不要向脚本传递 run_id，也不要读取、删除或依赖脚本内部状态文件

## 执行步骤

1. **生成稳定的 session_id**
   - 使用 `site_slug` 和 `round` 生成：`h5-online-review-<site-slug>-r<round>`
   - 确保同一轮次的前 3 次调用都使用相同的 `session_id`
   - 如果 3 次仍失败，第 4 次才使用 `h5-online-review-<site-slug>-r<round>-retry1`

2. **使用 python file 模式执行**
   - 必须使用 `python` 工具的 `file` 参数执行脚本，不能用 bash 模式，不能用 heredoc/inline script 包装
   - 设置适当的超时时间（建议3600秒）
   - 传递审核任务内容

3. **解析结果**
   - 无论 Python 工具退出码是 0、1 还是 2，都先解析 stdout JSON
   - 脚本固定返回 `ok`、`error_message`、`error_type`、`output`
   - `ok=false` 表示本次调用出错；读取 `error_message`、`error_type` 和 `output.run_id` 判断错误原因
   - `ok=true` 表示远端 run 已完成；从 `output.text` 解析审核结论 JSON

## 重试和错误处理

- 只要 `ok=false`、`output.text` 为空，或无法解析结构化结论，就按本 skill 的同一轮次重试规则继续重试
- 同一轮次前 3 次重试必须使用完全相同的 `session-id`
- 普通重试不修改 `session-id`，让 `anyai_http_run.py` 在脚本内部复用或恢复 run
- 同一个 `session-id` 连续 3 次仍失败后，第 4 次才换成带 `-retry<N>` 后缀的新 `session-id`
- 每次重试先确认本 skill 的调用方式，保持默认 agent、base URL、任务正文和输出要求正确
- 连接失败、远端失败、脚本超时、run failed/aborted、调度参数错误都按上述规则处理；不要自行编造审核结果
- 必须使用 `python` 工具的 `file` 模式，不要使用 bash 模式

## 审核任务正文模板

每次提交给 `review-lead` 的文本必须包含：

```text
请审核以下 H5 站点，并输出可被 h5-online 编排器判定的结构化结论。

目标 URL: <url>
源码路径: <source_path 或 未提供>
审核轮次: <initial_review 或 regression_review_rN>
上一轮修复摘要: <复审时填写>

输出要求：
1. 必须完成 harness-google-review 的正式审核流程。
2. 最终回复必须包含一个 JSON 代码块，字段如下：
   - has_issues: boolean
   - submit_ready: boolean
   - issues: array，每项包含 priority、category、title、evidence、fix_hint
   - artifact_root: string
   - report_path: string
   - summary: string
3. 如果报告中仍有任何 P0/P1/P2 问题，has_issues 必须为 true。
4. 只有没有发现问题且可上线时，has_issues=false 且 submit_ready=true。
5. 结论不明确时，不要返回 has_issues=false。
```

## 返回结果处理

读取脚本输出中的：

- `ok`
- `error_message`
- `error_type`
- `output.run_id`
- `output.text`（结构化结论所在文本）

判定：

- `ok=false`：本次审核调用失败，按重试规则继续
- `output.text` 为空：本次审核未完成，按重试规则继续
- `output.text` 中 JSON 结论为 `has_issues=false` 且 `submit_ready=true`：审核通过，闭环结束
- 其他情况：视为发现问题，进入 coding 修复

如果输出没有 JSON 结论，要求 review 服务返工一次，明确输出上述字段。返工仍不明确，则视为发现问题，交给 coding 服务根据报告和文本摘要修复。

## 返回给主控的摘要

每次完成后，把结果整理成下面形态供 h5-online 判定：

```json
{
  "service": "harness-google-review",
  "session_id": "h5-online-review-<site-slug>-r<round>",
  "status": "completed|failed|aborted|blocked",
  "has_issues": true,
  "submit_ready": false,
  "issues": [],
  "artifact_root": "",
  "report_path": "",
  "summary": "",
  "raw_output_excerpt": ""
}
```

不要把完整 HTTP JSON 原样展示给用户；主控需要的是上述摘要。

## 失败处理

- HTTP 连接失败：按同一轮次规则重试；连续失败后报告 `harness-google-review` 网关不可达，提示用户或外部编排系统检查 `H5_REVIEW_BASE_URL` 和服务状态；不要尝试启动、重启或杀死进程
- 其他 `ok=false` 或超时：按同一轮次规则重试；仍失败则报告阻塞，不要自行编造审核结果
