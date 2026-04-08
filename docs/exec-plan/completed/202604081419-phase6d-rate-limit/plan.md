# Phase 6D — Daemon 侧读写字节限流实施计划

## 背景

`docs/design/ROADMAP.md` 的 Phase 6 仍有“上传/读取限流”未完成。此前 Phase 6A/6B/6C 已交付离线只读、敏感文件过滤和小文件预取，但 daemon 侧对来自 server 的读写请求仍无任何字节速率控制。

本阶段以上游设计 [`docs/superpowers/specs/2026-04-08-phase6d-rate-limit-design.md`](/root/Rclaude/docs/superpowers/specs/2026-04-08-phase6d-rate-limit-design.md) 为准，补齐最小闭环的 daemon 侧读/写字节限流能力。

## 阶段目标

在不修改 proto、不改变 server/FUSE 侧接口语义的前提下，为 daemon 增加可配置的读/写字节速率限制：

- `ReadFileReq` 按最终返回内容字节数限速
- `WriteFileReq.content` 按实际写入字节数限速
- 默认关闭，显式配置后生效
- 限流等待可被 `context` 取消或超时打断

## 范围

- `pkg/config` 增加 daemon `rate_limit` 配置、默认值与校验
- `pkg/ratelimit` 实现字节 limiter 封装
- `pkg/syncer` 将 `Handle` 改为接收 `context.Context`，并在读写路径接入 limiter
- `pkg/syncer/daemon.go` 构造 limiter 并透传到 handler
- `pkg/config`、`pkg/ratelimit`、`pkg/syncer` 单测与 daemon 集成测试覆盖

## 范围外

- proto 改动
- server 侧限流或优先级调度
- 并发数限制、队列长度限制
- `Stat/ListDir/Mkdir/Delete/Rename/Truncate` 等非字节负载操作节流
- 指标、审计日志、热更新或多级预算

## 模块拆分

### M1 — `pkg/config`

- 新增 `RateLimitConfig`
- `DaemonConfig` 增加 `RateLimit`
- 默认 `read_bytes_per_sec=0`、`write_bytes_per_sec=0`
- `Validate` 拒绝负数
- 补齐加载与校验测试

### M2 — `pkg/ratelimit`

- 实现 `ByteLimiter`
- 暴露 `NewBytesPerSecond`、`Enabled`、`WaitBytes`
- 处理 disabled、分块等待、context cancel
- 补齐包级单测

### M3 — `pkg/syncer/handle.go`

- `Handle` 增加 `context.Context`
- `HandleOptions` 增加 `ReadLimiter` / `WriteLimiter`
- `handleRead` 在最终 payload 上等待读取预算
- 保持现有错误包装口径

### M4 — `pkg/syncer/handle_write.go`

- `handleWrite` 在真正落盘前等待写入预算
- 空内容 create 不消耗写预算
- `Mkdir/Delete/Rename/Truncate` 保持不接 limiter

### M5 — `pkg/syncer/daemon.go`

- daemon 启动时从配置构造 limiter
- `runRecvLoop` 透传 stream 上下文到 `Handle`
- 补齐 daemon 集成测试，验证延迟与取消退出

## Todo

- [x] 配置层：新增 daemon `rate_limit` 配置、默认值、负数校验和加载测试
- [x] limiter 包：实现 `pkg/ratelimit.ByteLimiter` 及其单测
- [x] syncer 入口：`Handle` 改为接收 `context.Context`，更新调用点与单测
- [x] 读路径：`handleRead` 按最终 payload 字节数接入 `ReadLimiter`
- [x] 写路径：`handleWrite` 按写入内容字节数接入 `WriteLimiter`
- [x] daemon 装配：在 `daemon.go` 中构造 limiter 并透传
- [x] 测试：补齐 `pkg/config`、`pkg/ratelimit`、`pkg/syncer` 与 daemon 集成回归
- [x] 验证：执行 `make fmt`、`make lint`、`make test`

## 依赖图

```text
配置层(M1) ──┐
             ├──> daemon 装配(M5) ──┐
limiter 包(M2) ─┘                    │
                                      ├──> syncer 入口(M3) ──> 读路径(M4-read)
                                      │
                                      ╰──────────────────────> 写路径(M4-write)

M1 + M2 + M3 + M4 + M5 ──> 测试回归 ──> fmt/lint/test 验证
```

## 验收标准

- daemon 配置可正确加载 `rate_limit.read_bytes_per_sec` 与 `rate_limit.write_bytes_per_sec`
- 默认配置下现有行为不变
- `ReadFileReq` 在启用读取限流后进入统一读取预算，预取读天然复用该预算
- `WriteFileReq.content` 在启用写入限流后进入统一写入预算
- `ctx` 取消或超时时，等待中的限流能退出，不会永久阻塞
- `Stat/ListDir/Mkdir/Delete/Rename/Truncate` 不被本阶段限流误伤
- `make fmt`、`make lint`、`make test` 全部通过

## 风险与应对

- 读取仍是整文件入内存：本阶段只控返回速率，不处理流式读取；继续依赖现有 `MaxReadSize`
- 预取与前台读共享预算：作为最小闭环接受该 tradeoff，后续若要优先级隔离另开阶段
- 时间敏感测试易抖动：用宽松阈值验证“明显延迟”，避免精确毫秒断言
