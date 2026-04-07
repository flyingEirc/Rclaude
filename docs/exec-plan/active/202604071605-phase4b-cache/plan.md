# Phase 4b — 服务端整文件内容缓存 实施计划

> 上游 spec：[`docs/superpowers/specs/2026-04-07-phase4b-cache-design.md`](/e:/Rclaude/docs/superpowers/specs/2026-04-07-phase4b-cache-design.md)
> 上游 ROADMAP：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 4
> 上一阶段：[`docs/exec-plan/completed/202604071253-phase4a-write-ops/`](/e:/Rclaude/docs/exec-plan/completed/202604071253-phase4a-write-ops/)

**Goal:** 为当前服务端读链路增加整文件内容缓存，命中时直接本地切片返回；写入和 daemon 变更会立即失效；不引入预取、离线降级或旧缓存兜底。

## 任务

- [ ] T1 新增 `pkg/contentcache`，实现按字节预算的 LRU 缓存与单测
- [ ] T2 扩展 `pkg/session`，注入缓存配置并补齐缓存辅助方法与失效逻辑
- [ ] T3 扩展 `pkg/session/manager.go`、`pkg/session/service.go`，统一从 manager 构造带缓存配置的会话
- [ ] T4 改造 `pkg/fusefs/view.go` 的 `readChunk`，实现缓存命中、全量读透和禁用缓存降级
- [ ] T5 扩展 `internal/inmemtest` 与 `pkg/fusefs/*_test.go`，覆盖重复读命中、变更失效、写后失效
- [ ] T6 执行 `make fmt`、`make lint`、`make test`、`go build ./...`，把结果写入 `开发流程.md`

## 验收标准

- 同一路径重复读，第二次不再下发 daemon `ReadFileReq`
- `ApplyWriteResult`、`ApplyDelete`、`ApplyRename` 和 daemon `Change` 会使对应缓存失效
- `cache.max_bytes <= 0` 时保持现有非缓存行为
- 全量测试通过，Windows 若 race 受限则在 `测试错误.md` 记录并以 Linux 为最终依据
