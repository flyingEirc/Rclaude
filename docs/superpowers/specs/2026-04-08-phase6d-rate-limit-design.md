# Phase 6D — Daemon 侧读写字节限流设计

> 本文档是 Phase 6D 的设计 spec，不承担实施计划。
> 实施计划落在 `docs/exec-plan/active/{时间}-phase6d-rate-limit/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](/root/Rclaude/docs/design/PLAN.md)、[`docs/design/ROADMAP.md`](/root/Rclaude/docs/design/ROADMAP.md) Phase 6
> 上一阶段：[`docs/exec-plan/completed/202604071256-phase6c-prefetch/`](/root/Rclaude/docs/exec-plan/completed/202604071256-phase6c-prefetch/)
> 实现架构：[`docs/ARCHITECTURE.md`](/root/Rclaude/docs/ARCHITECTURE.md)

## 0. 范围与原则

Phase 6A/6B/6C 已分别完成离线只读、敏感文件过滤和小文件预取，但 ROADMAP 中仍有一个未完成子题：**上传/读取限流**。当前 daemon 对来自 server 的读写请求没有任何字节速率控制，这意味着前台 `cat/read`、后台预取和写入回放都会直接按本地文件系统与网络可提供的峰值速度执行。

Phase 6D 只解决一个问题：**在不修改 proto、不过度扩展调度模型的前提下，为 daemon 增加可配置的读/写字节速率限制。**

本阶段目标是：

- 限制 daemon 返回给 server 的读取内容字节速率
- 限制 daemon 从 server 落盘到本地文件的写入内容字节速率
- 默认关闭，只有显式配置时才生效
- 继续复用现有请求/响应、超时和错误归类链路

范围内：

- daemon 侧 `rate_limit` 配置
- `pkg/ratelimit` 小型字节令牌桶封装
- `syncer.Handle(Read)` 与 `syncer.Handle(Write)` 接入限流
- stream 请求上下文透传到 handler，用于打断等待
- 单测与 daemon 集成测试覆盖

范围外：

- proto 改动
- server 侧限流、优先级或调度中心
- 并发数限制、队列长度限制
- 对 `Stat/ListDir/Mkdir/Delete/Rename/Truncate` 等非字节负载操作的节流
- 基于真实 gRPC 帧大小的网络层限速
- 限流指标埋点、审计日志或动态热更新

设计原则：

- 只做最小闭环的 daemon 侧字节节流，不额外引入新协议和新错误码
- 默认配置必须不改变现有部署行为
- 限流等待必须可被 `context` 取消或超时打断，避免无限阻塞
- 前台读与预取读共享同一个读取预算，不额外拆分优先级
- 节流是“平滑变慢”，不是“超限即拒绝”

## 1. 对外配置与行为语义

### 1.1 新增配置

只在 daemon 侧新增：

```yaml
rate_limit:
  read_bytes_per_sec: 0
  write_bytes_per_sec: 0
```

新增结构语义：

- `read_bytes_per_sec`
  - 限制 daemon 返回给 server 的 `ReadFileReq` 内容字节速率
- `write_bytes_per_sec`
  - 限制 daemon 将 `WriteFileReq.content` 写入本地文件前允许通过的字节速率

默认值：

- `0` 表示关闭
- 负数视为非法配置，daemon 启动失败

不新增 server 配置项，因为本阶段只做 daemon 侧保护。

### 1.2 哪些操作受限流影响

受限流影响的只有两类字节负载操作：

- `ReadFileReq`
- `WriteFileReq`

不受限流影响：

- `StatReq`
- `ListDirReq`
- `MkdirReq`
- `DeleteReq`
- `RenameReq`
- `TruncateReq`
- 心跳与变更事件

原因是这些操作不承载稳定可度量的数据上传/下载字节流；若把它们一并纳入，会把“读取/上传限流”扩成通用请求调度，与本阶段目标不符。

### 1.3 生效口径

读取限流按**最终返回的有效内容字节数**计费，而不是按原文件大小计费。具体来说：

1. 先按现有逻辑读取文件
2. 再应用 `offset/length`
3. 再应用 `MaxReadSize`
4. 对最后真正要放进 `FileResponse.content` 的 `len(content)` 做限流等待

写入限流按 `WriteFileReq.content` 的长度计费：

- `Create` 通过空内容 `WriteFileReq` 建文件时，不消耗写预算
- `append` 与 `offset write` 都按写入内容字节数消耗

### 1.4 与现有预取的关系

server 侧预取最终仍然通过普通 `ReadFileReq(offset=0,length=0)` 向 daemon 拉内容，因此天然进入同一个 `read` 预算。

本阶段接受如下 tradeoff：

- 预取可能占用读取预算，从而拖慢后续前台读取
- 不为前台请求设置更高优先级
- 不单独为预取拆分 limiter

如果后续需要区分“前台读”和“后台预取”预算，应单开新阶段处理。

## 2. 方案选择

本阶段评估过三种方向：

### 方案 A — daemon 内双令牌桶

在 daemon 运行时持有两个 limiter：

- `read` limiter
- `write` limiter

`Handle(Read)` 在返回内容前等待读取令牌，`Handle(Write)` 在真正落盘前等待写入令牌。

优点：

- 不改 proto
- 不改 server 请求模型
- 改动面小，直接覆盖前台读、后台预取和写入回放

缺点：

- 限的是业务负载字节，不是精确的 gRPC 帧流量
- 现有 `Read` 仍是整文件读入内存后切片，本阶段不改善内存占用

### 方案 B — transport / stream 层限流

在连接或流层做统一限速。

优点：

- 更接近真实网络字节

缺点：

- 会把心跳、控制消息也卷进去
- 与“读取/上传限流”语义不再严格对齐
- 对 gRPC 封装侵入更深

### 方案 C — server 侧限流

在 `session/fusefs` 发请求前限制请求速率或字节预算。

优点：

- server 侧实现相对集中

缺点：

- 不符合“只做 daemon 侧”边界
- 限制的是请求发起而不是 daemon 实际读写出的字节
- 对本地资源保护不够直接

### 推荐结论

采用**方案 A：daemon 内双令牌桶**。

它能在不改变当前协议和 server/FUSE 语义的前提下，把 roadmap 中“读取/上传限流”的缺口补成一个最小且完整的闭环。

## 3. 模块改动

### M1 — `pkg/config`

为 daemon 配置新增：

```go
type RateLimitConfig struct {
    ReadBytesPerSec  int64 `mapstructure:"read_bytes_per_sec"`
    WriteBytesPerSec int64 `mapstructure:"write_bytes_per_sec"`
}

type DaemonConfig struct {
    ...
    RateLimit RateLimitConfig `mapstructure:"rate_limit"`
}
```

要求：

- `LoadDaemon` 能正确加载 `rate_limit`
- 默认零值表示关闭
- `Validate` 拒绝负数值
- 不新增 server 侧对应配置

### M2 — `pkg/ratelimit`

把当前空包落实为一个很小的字节 limiter 封装，底层继续复用 `golang.org/x/time/rate`。

建议暴露的接口类似：

```go
type ByteLimiter struct { ... }

func NewBytesPerSecond(limit int64) *ByteLimiter
func (l *ByteLimiter) Enabled() bool
func (l *ByteLimiter) WaitBytes(ctx context.Context, n int) error
```

语义要求：

- `limit<=0` 时返回 disabled limiter，`WaitBytes` 立即成功
- `WaitBytes` 必须支持大于底层 burst 的字节数，通过分块等待实现
- `ctx` 取消或 deadline 到达时立即返回错误

之所以封装成 `pkg/ratelimit` 而不是在 `syncer` 内直接拼 `rate.Limiter`，是为了把节流逻辑与 daemon 业务操作解耦，并保持后续扩展空间。

### M3 — `pkg/syncer/handle.go`

当前 `Handle` 没有 `context.Context` 参数，这会让限流等待无法被 stream 取消打断。因此本阶段把入口调整为：

```go
func Handle(ctx context.Context, req *remotefsv1.FileRequest, opts HandleOptions) *remotefsv1.FileResponse
```

同时 `HandleOptions` 增加：

```go
type HandleOptions struct {
    ...
    ReadLimiter  *ratelimit.ByteLimiter
    WriteLimiter *ratelimit.ByteLimiter
}
```

这样 `Read` / `Write` handler 在等待令牌时可以直接复用请求生命周期上下文。

### M4 — `pkg/syncer/handle.go` 读路径

`handleRead` 保持当前逻辑顺序：

1. 路径校验
2. 敏感路径拦截
3. `os.ReadFile`
4. `offset/length` 切片
5. `MaxReadSize` 截断
6. 对最终 `sliced` 长度调用 `ReadLimiter.WaitBytes(ctx, len(sliced))`
7. 返回内容

关键点：

- 对空切片读不等待
- 配置关闭时不等待
- 限流等待失败时直接按现有错误链路返回

### M5 — `pkg/syncer/handle_write.go` 写路径

`handleWrite` 在实际写入前接入：

1. 请求校验
2. 路径与敏感规则校验
3. 加锁与 self-write 标记
4. 对 `len(content)` 调用 `WriteLimiter.WaitBytes(ctx, len(content))`
5. `openWriteTarget`
6. `writeContent`
7. 返回新的 `FileInfo`

关键点：

- `len(content)==0` 时不等待
- 只限制 `WriteFileReq.content`
- `Mkdir/Delete/Rename/Truncate` 保持当前行为，不进入 limiter

### M6 — `pkg/syncer/daemon.go`

daemon 运行时在启动链路里构造 limiter，并注入 `HandleOptions`：

- `rate_limit.read_bytes_per_sec -> ReadLimiter`
- `rate_limit.write_bytes_per_sec -> WriteLimiter`

`runRecvLoop` 需要把当前 stream 上下文透传给 `Handle(ctx, ...)`，保证：

- daemon 断连时等待中的限流可以退出
- 测试中的 `context.WithTimeout` 可以稳定打断等待

## 4. 顶层数据流

### 4.1 读取路径

```text
Server/FUSE 发 ReadFileReq
  -> daemon runRecvLoop 收到请求
  -> Handle(ctx, req, opts)
  -> handleRead
       -> os.ReadFile(abs)
       -> sliceContent(offset,length)
       -> MaxReadSize cap
       -> ReadLimiter.WaitBytes(ctx, len(payload))
       -> FileResponse{content: payload}
```

### 4.2 写入路径

```text
Server/FUSE 发 WriteFileReq
  -> daemon runRecvLoop 收到请求
  -> Handle(ctx, req, opts)
  -> handleWrite
       -> validate + safe path + sensitive check
       -> path lock + self write remember
       -> WriteLimiter.WaitBytes(ctx, len(content))
       -> open + write
       -> FileResponse{info: updated file info}
```

### 4.3 关闭场景

```text
daemon 在 WaitBytes 中等待
  -> stream 断开 / ctx cancel / timeout
  -> WaitBytes(ctx, n) 返回 ctx error
  -> handleRead/handleWrite 返回 error response
  -> runRecvLoop 或上游 goroutine 继续走现有关闭流程
```

## 5. 错误语义

本阶段不新增新的 proto 错误结构，也不引入“rate limited”专用错误码。

行为规则如下：

- 限流成功：行为与当前完全一致
- 限流等待过程中 `ctx` 被取消：返回现有 context cancellation 错误
- 限流等待过程中 deadline 到达：返回现有 deadline exceeded 错误

server/FUSE 侧继续复用现有超时/取消归类逻辑：

- 不新增新的 errno 映射
- 不改现有 `ErrRequestTimeout` / `ErrSessionFailed` 等语义边界

因此本阶段交付的是“平滑变慢”，不是“超限立即拒绝”。

## 6. 测试策略

### 6.1 `pkg/config/config_test.go`

覆盖：

- daemon 默认值中 `read_bytes_per_sec=0`、`write_bytes_per_sec=0`
- YAML 显式配置加载
- 负数配置校验失败

### 6.2 `pkg/ratelimit`

新增单测覆盖：

- disabled limiter 不阻塞
- enabled limiter 会产生可观察的等待
- `WaitBytes` 支持大于单次 burst 的请求
- `ctx cancel` / deadline 能及时返回

### 6.3 `pkg/syncer/handle_test.go`

覆盖：

- `Read` 会命中 `ReadLimiter`
- `Write` 会命中 `WriteLimiter`
- `Create` 空内容不消耗写预算
- `Stat/ListDir/Mkdir/Delete/Rename/Truncate` 不命中 limiter
- `ctx` 被取消时 `Read` / `Write` 返回失败

为保证测试稳定，可在测试中注入一个低速 limiter，并校验等待是否发生，或注入可观测的 fake limiter。

### 6.4 `pkg/syncer/daemon_test.go`

覆盖：

- daemon 在启用读取限流后，对 `ReadFileReq` 不是立即返回，而是发生可观测延迟
- daemon 在启用写入限流后，对 `WriteFileReq` 不是立即返回，而是发生可观测延迟
- 取消 daemon 上下文后，等待中的请求能结束，不会永久挂住

本阶段不额外为预取单独新增 daemon 集成测试，因为预取最终复用普通 `ReadFileReq` 路径，限流能力会自动覆盖。

## 7. 验收标准

- daemon 配置可正确加载 `rate_limit.read_bytes_per_sec` 与 `rate_limit.write_bytes_per_sec`
- 默认配置下，现有行为无变化
- 启用 `read_bytes_per_sec` 后，普通读与预取读都进入统一读取预算
- 启用 `write_bytes_per_sec` 后，文件写入进入统一写入预算
- `ctx` 取消或超时时，限流等待不会无限阻塞
- `make fmt`、`make lint`、`make test` 通过

## 8. 风险与应对

### 风险 1：读取仍然整文件入内存

当前 `handleRead` 仍然先 `os.ReadFile` 再切片，所以 Phase 6D 只能控制返回速率，不能降低超大文件的内存占用。

应对：

- 保持 `MaxReadSize` 现有保护
- 把“分段流式读取”明确留给后续单独阶段，不在本阶段混入

### 风险 2：预取会与前台读抢预算

共享读取预算会让预取在某些场景拖慢前台读。

应对：

- 当前阶段接受该 tradeoff，以最小实现闭环为优先
- 如后续需要优先级或预算拆分，再单开阶段处理

### 风险 3：时间敏感测试易波动

限流测试若严格依赖墙钟时间，容易在 CI 上抖动。

应对：

- 使用较宽松阈值和最小可观测延迟断言
- 优先断言“明显被延后”而不是精确毫秒数
- 在 `pkg/ratelimit` 单测中尽量覆盖核心等待逻辑，把 daemon 集成测试保持在少量冒烟口径

## 9. 非目标

以下事项明确不在 Phase 6D 范围内：

- server 侧带宽控制
- 动态调速或热更新
- 并发数限制
- 背景任务与前台请求优先级隔离
- 指标、trace、审计日志
- 基于实际网络流量的精准计费

