# Phase 8c — Remote PTY CLI、smoke 与文档收口

**Goal:** 在 `phase8b` 服务端链路稳定后，补齐 `rclaude-claude` 本地入口、部署样例、`pty-smoke` 脚本与文档更新，形成真机验收入口。

**依赖：**

- `phase8b` 完成 `RemotePTY.Attach` 服务端装配
- `phase8a` 的 `pkg/ptyclient` / `pkg/ptyhost` 与 `pkg/config` PTY 配置块已可复用

**本阶段包含：**

- `app/clientpty/main.go` 新二进制
- 复用 daemon 配置中的 `server.address` / `server.token`
- `tools/pty-smoke.sh`
- `deploy/minimal/` 的 PTY 示例与说明
- `docs/ARCHITECTURE.md`
- `docs/reference/pty-protocol.md`
- `docs/design/ROADMAP.md` 的 Phase 8 条目补充

## 并行任务划分

- [x] Worker D: `app/clientpty` + 复用 `pkg/transport` / `pkg/config` 的客户端接入
- [x] Worker E: `tools/pty-smoke.sh` + `deploy/minimal/` + `daemon-localtest.yaml`
- [x] Worker F: `docs/ARCHITECTURE.md` + `docs/reference/pty-protocol.md` + `docs/design/ROADMAP.md`
- [x] Review: 与 `phase8b` 汇合后统一做一轮 code review 和联调校正

## 验收

- [x] `go build ./app/clientpty`
- [x] 相关脚本与当前最小部署目录一致（Windows 本机未完成 `sh -n`，真机 smoke 需在 Linux 目标环境执行）
- [x] 文档与实际配置字段一致
- [x] `make fmt`（当前环境无 `make`，以 `gofumpt -w` 完成本阶段 Go 文件格式化）
- [x] `make lint`（当前环境无 `make`，已执行 phase8 相关包 `golangci-lint run`）
- [x] `make test`（当前环境无 `make`，已执行 phase8 相关包 `go test -count=1`）

## 风险

- [x] `phase8b` 的错误映射与 `frame_max_bytes` 行为调整已同步到 8c CLI / deploy / docs
- [x] `app/clientpty` / `RemotePTY` service wiring 已完整落地，并通过 lint / test / build 验证
