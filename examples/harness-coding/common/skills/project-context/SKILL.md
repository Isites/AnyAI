---
name: project-context
description: 项目上下文信息，在使用 harness 前必须填写实际项目信息
source: local
tags:
  - context
  - project
---

# 项目上下文

> **使用说明**：将此文件内容替换为你项目的实际信息。
> 这些信息会自动注入到 Agent 上下文中，帮助 Agent 更好地理解项目。
> 未填写的部分会导致 Agent 猜测，可能产生不准确的方案。

## 项目基本信息

- **项目名称**: example-api-server（示例，请替换）
- **项目语言**: Go 1.22
- **框架**: Gin Web Framework
- **项目路径**: /home/user/projects/example-api

## 项目结构

```
example-api/
├── cmd/
│   └── server/        # 入口
├── internal/
│   ├── handler/       # HTTP handlers
│   ├── service/       # 业务逻辑
│   ├── repository/    # 数据访问
│   └── model/         # 数据模型
├── pkg/               # 可导出包
├── migrations/        # 数据库迁移
├── test/              # 集成测试
├── go.mod
└── go.sum
```

## 关键约定

- RESTful API 设计
- 依赖注入通过构造函数
- 数据库操作通过 repository 层
- 错误码统一格式：`{"code": "XXX", "message": "xxx"}`
- 日志使用 zap

## 外部依赖

- PostgreSQL 15（主数据库）
- Redis 7（缓存）
- 数据库迁移使用 golang-migrate

## 已知约束

- 必须兼容 Go 1.21+
- API 响应格式遵循项目规范
- 不引入新的 ORM，使用 database/sql
