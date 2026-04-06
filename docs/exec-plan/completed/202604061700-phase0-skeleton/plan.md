# Phase 0 — 远程文件访问系统骨架与工具链落地

> 阶段目录：`docs/exec-plan/completed/202604061700-phase0-skeleton/`
> 上游 ROADMAP 条目：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 0
> 关联设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)、[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)

## Context

ROADMAP 把整体交付划分为 Phase 0 ~ Phase 7。本阶段为 Phase 0，目标是把 `docs/ARCHITECTURE.md §四 目录结构约束` 物化到磁盘，把 `tools/tools.go` 锁好 proto 插件版本，把基础三方依赖固定到 `go.mod`，并补齐 `make proto` 入口。完成后 `go build ./...`、`make fmt`、`make lint`、`make test` 均可通过，为 Phase 1 (proto + 公共基础包) 提供干净起点。

本阶段**不**包含：proto 业务字段定义、任何业务逻辑代码、FUSE / gRPC / Daemon 实现 —— 这些都属于 Phase 1 之后。

## 触发的 ROADMAP 子项

- [x] 固定 proto 工具链与基础依赖
- [x] 建立 `api/`、`app/`、`pkg/`、`internal/` 基础目录
- [x] 补齐 `Makefile` 中的 `proto`、`build`、`test`、`lint` 目标 *(build/test/lint 已存在，本次只补 proto)*

## 模块拆分（Modules）

### M1 — 目录骨架与可编译占位
**职责**：把 ARCHITECTURE.md §四 中所有目录建出来，并在每个 Go 包里放最小 `doc.go`，让 `go build ./...` 真正能跨全部包。
**边界**：只放 `package foo` + 一行 doc 注释，禁止任何业务代码、禁止 import 任何尚未引入的依赖。
**新增文件**：
- `api/proto/remotefs/v1/remotefs.proto` — 最小占位 (`syntax = "proto3"; package remotefs.v1; option go_package = "flyingEirc/Rclaude/api/proto/remotefs/v1;remotefsv1";`)
- `app/client/doc.go` — `// Package client 装配本地 Daemon 命令入口。`
- `app/server/doc.go` — `// Package server 装配 Server 命令入口。`
- `pkg/config/doc.go`
- `pkg/logx/doc.go`
- `pkg/safepath/doc.go`
- `pkg/fstree/doc.go`
- `pkg/transport/doc.go`
- `pkg/auth/doc.go`
- `pkg/session/doc.go`
- `pkg/syncer/doc.go`
- `pkg/ratelimit/doc.go`
- `internal/testutil/doc.go`
- `deploy/.gitkeep`

**注意 godot lint**：每条包注释必须以中文句号 `。` 或英文 `.` 结尾，否则 `make lint` 会失败。

### M2 — 依赖与工具版本锁
**职责**：把 ARCHITECTURE.md §三 技术栈表里 Phase 0 ~ Phase 3 必须用到的库一次性引入 `go.mod`，并用 `tools/tools.go` 把 proto 代码生成插件版本钉死。
**边界**：本阶段只 `require` + `go mod tidy`，不写任何调用代码。
**新增/修改文件**：
- `go.mod` — 通过 `go get` 引入：grpc / protobuf / fsnotify / viper / cobra / testify / backoff/v4 / x/sync / doublestar/v4 / x/time / hanwen/go-fuse/v2
- `tools/tools.go` — `//go:build tools` build tag，匿名 import：
  - `google.golang.org/protobuf/cmd/protoc-gen-go`
  - `google.golang.org/grpc/cmd/protoc-gen-go-grpc`

### M3 — Makefile `proto` 目标
**职责**：把 proto 代码生成入口加入 Makefile，让后续 Phase 1 可以直接 `make proto`。
**边界**：只动 Makefile，不改其它构建/测试目标的语义。
**修改文件**：
- `Makefile` — 新增 `PROTO_DIR` / `PROTO_FILES` 变量与 `proto` 目标，加入 `.PHONY`，**不**链入 `make all` / `make check`

## Todo 列表

| ID | 目标 | 涉及模块 | 状态 |
|---|---|---|---|
| T1 | 把 M1 目录与 doc.go 全部落地 | M1 | [x] done |
| T2 | 引入 M2 依赖与 `tools/tools.go`，运行 `go mod tidy` | M2 | [x] done |
| T3 | 修改 Makefile，加入 `proto` 目标 | M3 | [x] done |

依赖关系：T1 → T2 → T3（T2 需要 T1 的 `tools/` 目录存在；T3 不依赖前两步但放最后做整体校验）。

## 验证（端到端）

```bash
go build ./...                      # 必须 exit 0
make fmt                            # 不应产生 diff
make lint                           # exit 0
make test                           # exit 0（无测试用例时也是 0）
make proto                          # 有/无 .proto 都应 exit 0
```

## 风险与应对

| 风险 | 说明 | 应对 |
|---|---|---|
| `make lint` 在空 doc.go 上报 godot/whitespace | 包注释格式不规范 | 每个 `doc.go` 写完整 “// Package xxx 描述。” 单行 |
| `go get` 拉到不兼容版本 | 较新 grpc / fuse 可能要求更高 Go 版本 | 已锁 Go 1.25.2，远高于这些库要求 |
| `protoc` 未安装导致 `make proto` 报错 | 本机/CI 环境差异 | `proto` 目标在没有 `.proto` 文件时直接 `exit 0`；本阶段不强制执行 `make proto` |
| `tools/tools.go` 被普通构建包含 | build tag 写错 | 文件首行写 `//go:build tools`，并保留紧跟其后的空行 |

## 关键参考文件

- `docs/ARCHITECTURE.md` §四 目录结构约束 / §三 技术栈
- `docs/design/ROADMAP.md` Phase 0 章节
- `docs/workflow.md` — Make 目标语义
- `Makefile` — 当前已有目标
- `.golangci.yml` — 启用的 linter 集合（决定 doc.go 写法）
