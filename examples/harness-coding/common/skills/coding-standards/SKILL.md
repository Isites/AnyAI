---
name: coding-standards
description: 项目编码规范和代码风格指南
source: local
tags:
  - coding
  - standards
  - style
---

# 编码规范

> ⚠️ 此技能文件需要按项目实际情况定制。当前为示例模板。

## 项目语言

**主要语言**: Go (可根据项目修改为 Python/JavaScript/Java 等)

## 通用原则

- 遵循项目现有代码风格，保持一致性
- 优先使用标准库，减少外部依赖
- 错误处理要完善，不要吞掉错误
- 公共 API 要有文档注释
- 保持函数简短，单一职责（<50行）
- 避免过度工程（YAGNI原则）

## Go 项目规范

### 代码风格
- `gofmt` 格式化，CI会检查
- `golint` / `staticcheck` 静态分析
- 行长度 ≤ 120 字符
- 包注释格式：`// Package xxx 提供...`

### 命名规范
- 导出符号：PascalCase（首字母大写）
- 未导出符号：camelCase
- 缩写词：全大写（HTTP, URL, ID）
- 避免无意义命名：`data`, `info`, `result` → 用具体含义

### 错误处理
- 错误必须处理，不能 `_` 丢弃
- 错误包装：`fmt.Errorf("operation failed: %w", err)`
- 错误类型定义：使用 `errors.New` 或自定义错误类型
- 不要 panic，除非不可恢复的错误

### 并发
- 共享状态用 `sync.Mutex` 或 channel
- goroutine 必须能退出，避免泄漏
- `context` 必须传递，用于取消和超时

### 接口设计
- 接口定义在消费方（依赖倒置）
- 接口要小，一个接口一个行为
- 不要提前抽象，有多个实现时再抽象

### 测试规范
- 测试文件名：`xxx_test.go`
- 测试函数：`TestXxx` 或 `TestXxx_WhenYyy_ShouldZzz`
- 覆盖率要求：≥ 80% 核心业务，≥ 60% 辅助代码
- 使用 `t.Parallel()` 加速测试

## 代码审查检查清单

架构师和审查员参考此清单评审代码：

- [ ] 函数长度 ≤ 50 行
- [ ] 参数数量 ≤ 4 个
- [ ] 嵌套深度 ≤ 3 层
- [ ] 错误处理完整
- [ ] 并发安全（如适用）
- [ ] 资源释放（defer close）
- [ ] 命名清晰有意义
- [ ] 关键逻辑有注释
- [ ] 测试覆盖主要路径

## Git 规范

- Conventional commits: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`
- 格式：`type(scope): description`
- 每个 commit 做一件事
- commit message 说 why，代码说 what
- 禁止提交：`git commit -m "fix bugs"` ❌
- 推荐提交：`git commit -m "fix(auth): 修复token过期未刷新问题"` ✅

## 参考资料

- Effective Go: https://golang.org/doc/effective_go
- Go Code Review Comments: https://github.com/golang/go/wiki/CodeReviewComments
- Uber Go Style Guide: https://github.com/uber-go/guide
