# Phase 8b — Remote PTY service 装配与集成测试

**Goal:** 在现有 `phase8a` PTY 基础包之上，完成 `RemotePTY.Attach` 服务端装配、`session` / `ratelimit` 接入、`app/server` 注册，以及可回归的集成测试闭环。

**前置状态确认：**
- `docs/exec-plan/completed/202604191500-phase8a-pty-foundation/202604191500-phase8a-pty-foundation.md` 标记为 `done`。
- 但当前工作树中 `api/proto/remotefs/v1/pty.proto`、`pkg/ptyhost/`、`pkg/ptyclient/` 与 `phase8a` 文档仍未入当前分支，故本阶段以“本地已有 8a 基础实现但尚未提交”为真实基线继续推进。

**本阶段包含：**
- `RemotePTY.Attach` server handler
- `pkg/session` 增补 daemon/pty 查询与互斥状态
- `pkg/ratelimit` 增补 PTY attach / stdin 限流能力
- `app/server/main.go` 装配 `RemotePTY`
- `internal/inmemtest` / `internal/testutil` 的 PTY 测试夹具
- PTY 集成测试与错误降级矩阵覆盖

**本阶段不包含（留给 Phase 8c）：**
- `app/clientpty/main.go` 新二进制
- `tools/pty-smoke.sh`
- `deploy/minimal/` 配置与说明更新
- `docs/ARCHITECTURE.md` / `docs/reference/pty-protocol.md` / `docs/design/ROADMAP.md` 文档收口

## 并行任务划分

- [x] Worker A: `pkg/session` + `pkg/ratelimit`
  - 目标：补齐 `LookupDaemon` / `RegisterPTY` / `UnregisterPTY` 与 PTY 限流器最小接口，并补单测。
  - 预期文件：`pkg/session/*.go`、`pkg/ratelimit/*.go`
- [x] Worker B: `pkg/ptyservice`（或等价 server 侧 PTY handler 包）+ `app/server/main.go`
  - 目标：实现 `RemotePTY.Attach` 处理链路并在 gRPC server 注册。
  - 预期文件：新 PTY service 包、`app/server/main.go`
- [x] Worker C: `internal/inmemtest` + `internal/testutil` + PTY 集成测试
  - 目标：补可控的 PTY 测试夹具，覆盖 attach 正常流、限流、daemon 不在线、session busy、客户端断开、resize 透传。
  - 预期文件：`internal/inmemtest/*.go`、`internal/testutil/*.go`、新增 PTY 集成测试文件
- [x] Review: 独立 codereview agent
  - 触发条件：A/B/C 均完成并回传结果后
  - 范围：只看 `phase8b` 相关改动，优先找并发、资源释放、流关闭与错误降级问题

## 验收

- [x] `go test ./pkg/session/... ./pkg/ratelimit/...`
- [x] `go test ./internal/... ./app/server/...`
- [x] `make fmt`（当前环境无 `make`，以 `gofumpt -w` 完成本阶段 Go 文件格式化）
- [x] `make lint`（当前环境无 `make`，已执行 phase8 相关包 `golangci-lint run`）
- [x] `make test`（当前环境无 `make`，已执行 phase8 相关包 `go test -count=1`）

## 风险

- [x] `ptyclient` 首帧 `Recv` goroutine 回收风险已复核并保留为后续观察项，不阻塞 8b 收口
- [x] 当前工作树中的 unrelated 改动/CRLF 噪音已在完成摘要中明确提交边界
- [x] worker 间写集在本阶段执行中保持隔离，最终整合仅由主代理完成
