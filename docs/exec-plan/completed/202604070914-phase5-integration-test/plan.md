# Phase 5 — 集成测试与 Linux 真 FUSE 冒烟 实施计划

> 上游 spec：[`docs/superpowers/specs/2026-04-07-phase5-integration-test-design.md`](/e:/Rclaude/docs/superpowers/specs/2026-04-07-phase5-integration-test-design.md)
> 上游 ROADMAP：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 5
> 上一阶段：[`docs/exec-plan/completed/202604071605-phase4b-cache/`](/e:/Rclaude/docs/exec-plan/completed/202604071605-phase4b-cache/)

**Goal:** 建立以跨平台 `inmem` 为主体、Linux 真 FUSE 为补充的 Phase 5 测试体系；覆盖读写回归、缓存失效、多用户隔离、断线/超时 fault hooks，并把 Linux 真 FUSE 冒烟默认纳入 `make test`。

## 任务

- [x] T1 扩展 `internal/inmemtest`，实现多用户 harness、用户 handle、fault hooks、请求计数与 change/wait helper
- [x] T2 重构并扩展 `pkg/fusefs/inmem_e2e_test.go`，覆盖读写回归、缓存命中/失效、多用户隔离、断线、超时
- [x] T3 新增 Linux 真 FUSE 自动化冒烟测试，默认进入 `go test`，环境不满足时明确 `skip`
- [x] T4 新增 Linux 手动冒烟脚本入口，并保持与自动冒烟口径一致
- [x] T5 执行 `make fmt`、`make lint`、`make test`、`go build ./...`，把结果写入 `开发流程.md`

## 验收标准

- `inmem` 集成测试覆盖读、写、缓存、多用户隔离、断线、超时主场景
- Linux 真 FUSE 自动化冒烟默认被 `make test` 执行，环境阻塞时给出明确 `skip`
- Linux 手动脚本可完成 `ls` / `cat` / 写文件 / `mv` / `rm` 的最小验证
- 全量 `make fmt`、`make lint`、`make test`、`go build ./...` 通过
