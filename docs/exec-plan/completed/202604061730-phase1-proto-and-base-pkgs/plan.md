# Phase 1 — 协议与公共基础包

> 阶段目录：`docs/exec-plan/active/202604061730-phase1-proto-and-base-pkgs/`
> 上游 ROADMAP：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 1
> 关联设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)、[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)
> 上一阶段：[`docs/exec-plan/completed/202604061700-phase0-skeleton/`](/e:/Rclaude/docs/exec-plan/completed/202604061700-phase0-skeleton/)

## Context

Phase 0 已交付项目骨架、proto 工具链与 Makefile `proto` 目标。本阶段是 ROADMAP Phase 1：

- **当前状态**：`api/proto/remotefs/v1/remotefs.proto` 仅有 syntax + package + go_package 三行占位；`pkg/{config,logx,safepath,fstree,auth,session,syncer,transport,ratelimit}` 与 `internal/testutil` 全为只含 `doc.go` 的空包；`go.mod` 仅锁定 proto 工具链；无任何业务代码与单测。
- **触发的 ROADMAP 子项**（`docs/design/ROADMAP.md` Phase 1）：
  1. 定义 `remotefs` proto，覆盖文件树 / 读写 / 目录读取 / 属性 / 心跳
  2. 实现 `pkg/config`、`pkg/logx`、`pkg/auth`、`pkg/safepath`、`pkg/fstree`
  3. 上述包补齐基于 `testify` 的测试
- **目标**：完成本阶段后，Phase 2（Daemon MVP）可以直接 import 这五个 pkg + 生成的 proto 类型，无需再回头补类型或工具。
- **本阶段不包含**（明确边界）：
  - `pkg/transport`、`pkg/session`、`pkg/syncer`、`pkg/ratelimit`、`internal/testutil` 的业务实现 → 留给 Phase 2~6
  - 任何 Daemon / Server / FUSE 主流程
  - 任何 `app/client` / `app/server` 命令行装配

## 模块拆分（Modules）

### M1 — Proto 完整字段定义 + 代码生成
**职责**：把 `remotefs.v1` proto 从占位扩到 MVP 完整版，覆盖 `PLAN.md §3.2 Proto 定义`。
**边界**：仅改动 `api/proto/remotefs/v1/remotefs.proto`，跑 `make proto` 重新生成。
**proto 字段最小集**：
- 数据结构：`FileInfo`、`FileTree`、`FileChange`（含 `ChangeType` enum）
- Server→Daemon 请求：`FileRequest` (oneof: ReadFile/WriteFile/Stat/ListDir/Delete/Mkdir/Rename)
- Daemon→Server 响应：`FileResponse`
- 双向流封装：`DaemonMessage`、`ServerMessage`、`Heartbeat`
- Service：`RemoteFS.Connect(stream DaemonMessage) returns (stream ServerMessage)`
- 文件读取本阶段**不**引入 chunk stream，由 Phase 4 评估

### M2 — pkg/safepath（路径安全）
**职责**：纯函数路径校验、规范化、防穿越。
**公开 API**：`Clean / Join / IsWithin / ToSlash / FromSlash`
**禁止**：本阶段不暴露 symlink 解析、glob、配额检查。

### M3 — pkg/logx（结构化日志工厂）
**职责**：基于 `log/slog` + `slog.JSONHandler` 提供项目统一 logger。
**公开 API**：`New(opts) / FromContext / WithContext`
**边界**：不引入第三方日志库；不维护全局单例。

### M4 — pkg/config（YAML + viper 配置加载）
**职责**：定义 Daemon 与 Server 两类配置 struct，提供 `Load*` 函数。
**新增依赖**：`github.com/spf13/viper`
**校验规则**：地址非空 + 工作区/挂载点必须绝对路径 + 至少 1 个 token。

### M5 — pkg/auth（token 鉴权 + grpc 拦截器）
**职责**：静态 map 验证器 + gRPC unary/stream 拦截器，userID 注入 context。
**新增依赖**：`google.golang.org/grpc`（升级为 direct）

### M6 — pkg/fstree（内存文件树）
**职责**：维护单个用户工作区文件树元数据，线程安全。
**依赖**：M1 生成的 proto 类型
**公开 API**：`New / Insert / Delete / Lookup / List / Apply / Snapshot`

## Todo 列表

| ID | 目标 | 涉及模块 | 依赖 | 状态 |
|---|---|---|---|---|
| T0 | 创建阶段三件套目录 | — | — | [x] done |
| T1 | 写完整 proto + `make proto` 重新生成 | M1 | — | [x] done |
| T2 | 实现 safepath + testify 单测 | M2 | — | [x] done |
| T3 | 实现 logx + testify 单测 | M3 | — | [x] done |
| T4 | 实现 config + testify 单测 | M4 | — | [x] done |
| T5 | 实现 auth + testify 单测 | M5 | — | [x] done |
| T6 | 实现 fstree + testify 单测 | M6 | T1 | [x] done |
| T7 | 阶段总验收 | — | T1~T6 | [x] done |

依赖图：
```
T0 ──► T1 ──► T6 ──► T7
   ├──► T2 ────────► T7
   ├──► T3 ────────► T7
   ├──► T4 ────────► T7
   └──► T5 ────────► T7
```
T2/T3/T4/T5 与 T1 之间无依赖。本计划按 T1→T2→T3→T4→T5→T6→T7 顺序串行推进。

## 验证（端到端）

每个 Todo 完成后跑包级单测：
```bash
go test -race -count=1 -timeout 60s ./pkg/<pkg>/...
```

阶段终点（T7）：
```bash
mingw32-make fmt           # 不应产生 diff
mingw32-make lint          # exit 0
mingw32-make test          # exit 0
mingw32-make build         # exit 0
mingw32-make proto         # exit 0
go mod tidy                # 不应产生 diff
```

## 风险与应对

| 风险 | 应对 |
|---|---|
| `make proto` 因 service 出现报错 | Phase 0 `make tools` 已写好 protoc-gen-go-grpc 安装步骤 |
| 生成代码触发 lint | 必要时在 `.golangci.yml` 加 `exclude-dirs: ["api/proto"]` |
| `go mod tidy` 把 viper / grpc 又剪掉 | 本阶段每个 pkg 都真实 import，不会再剪 |
| fstree 并发死锁 | 单一锁、不在锁内调外部接口 |
| `cyclop ≤10` / `gocognit ≤15` 限制 | 拆子函数；不动 lint 阈值 |
| Windows 与 Linux 路径差异 | safepath 内部统一 forward slash，用 `path` 包而非 `filepath` |

## 关键参考文件

- `docs/design/PLAN.md §3.2` — proto 字段权威来源
- `docs/design/PLAN.md §3.1` — DaemonConfig 字段对齐
- `docs/ARCHITECTURE.md §三 / §五` — 库选型 + 实现原则
- `pkg/*/doc.go` — 包注释已就位
