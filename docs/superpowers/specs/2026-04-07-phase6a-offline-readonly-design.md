# Phase 6A — 离线只读降级设计

> 本文档是 Phase 6A 的设计 spec，不承担实施计划。
> 实施计划落在 `docs/exec-plan/active/{时间}-phase6a-offline-readonly/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](/root/Rclaude/docs/design/PLAN.md)、[`docs/design/ROADMAP.md`](/root/Rclaude/docs/design/ROADMAP.md) Phase 6
> 上一阶段：[`docs/exec-plan/completed/202604070914-phase5-integration-test/`](/root/Rclaude/docs/exec-plan/completed/202604070914-phase5-integration-test/)
> 实现架构：[`docs/ARCHITECTURE.md`](/root/Rclaude/docs/ARCHITECTURE.md)

## 0. 范围与原则

Phase 5 已经把在线读写、内容缓存、异常注入与 Linux 真 FUSE 冒烟稳定下来，但 daemon 断线后的行为仍然只有“完全失败”这一种结果。Phase 6A 只补齐 Phase 6 中最小、最紧迫的一段能力：**离线只读降级**。

本阶段目标是：在 daemon 断线后的短时间窗口内，继续暴露已有目录树和已缓存文件内容，避免短暂网络抖动把整个工作区立即打成不可访问；但任何需要新 RPC 的读操作和所有写操作都必须立即失败，避免伪成功和脏写。

范围内：

- daemon 断线后保留只读视图
- 只读视图的 TTL 配置与过期清理
- 已缓存内容可读、未缓存读失败
- 所有写操作在离线窗口内立即失败
- 断线重连后 live session 替换离线视图

范围外：

- 小文件预取
- 敏感文件过滤
- 读取/上传限流
- 基于旧缓存的陈旧直读兜底
- 后台定时清理 goroutine
- FUSE errno 映射重构

设计原则：

- 离线降级只复用现有 `Session.tree` 和内容缓存，不复制第二套快照模型
- 目录可读与缓存可读必须严格区分于“还能继续请求 daemon”
- 只读窗口必须有明确 TTL，避免陈旧视图无限悬挂
- TTL 过期前允许目录浏览和缓存命中读；TTL 过期后完全下线
- 新连接建立后必须直接替换旧离线视图，恢复 live 行为

## 1. 生命周期与行为语义

### 1.1 三态模型

本阶段把单个用户视图收敛为三种状态：

- `live`
  daemon 在线，行为保持现状
- `offline-readonly`
  daemon 已断开，但 server 继续保留该用户的 `Session`、文件树和内容缓存
- `expired`
  离线只读 TTL 到期后，从 `session.Manager` 中移除，用户彻底下线

### 1.2 状态流转

```text
bootstrap success
  -> live

live --(stream 结束/断线)--> offline-readonly
offline-readonly --(TTL 到期)--> expired
offline-readonly --(同 user_id 重连并 bootstrap 成功)--> live
```

关键规则：

- 只有已经 bootstrap 成功、拥有有效文件树的 session，才允许进入 `offline-readonly`
- 新连接成功后，新的 live session 直接覆盖旧离线 session
- 如果旧 session 在断线前已经被新连接替换，则旧 session 的收尾动作不得把新 session 从 manager 中删掉

### 1.3 离线只读期间的可见行为

在 `offline-readonly` 期间：

- `Lookup` / `Getattr` / `Readdir` 继续走保留的文件树，因此目录浏览可用
- 文件读取只有在内容缓存命中时才成功
- 一旦读取需要发 `ReadFileReq` 到 daemon，立即失败，不等待重连
- `Write` / `Create` / `Mkdir` / `Rename` / `Delete` / `Truncate` 全部立即失败
- TTL 过期后，该用户在逻辑上等同于完全离线，不再暴露目录树或缓存

### 1.4 配置策略

新增服务端配置：

- `server.offline_readonly_ttl`

规则：

- 默认值：`5m`
- `<= 0` 表示禁用离线只读保留，行为退回现状，即断线后立即下线

## 2. 模块改动

### M1 — `pkg/config`

为 `ServerConfig` 新增：

- `OfflineReadOnlyTTL time.Duration`

并在默认配置中写入：

- `OfflineReadOnlyTTL = 5 * time.Minute`

`Validate` 不额外拒绝零值或负值，因为它们被明确解释为“禁用该能力”。

### M2 — `pkg/session/session.go`

继续复用现有 `Session` 作为离线只读快照的唯一载体，不新增独立 snapshot 类型。

新增状态字段：

- 是否处于 `offline-readonly`
- 离线截止时间

新增行为方法：

- `RetainOffline(until time.Time)`：在 stream 结束后把当前 session 切换为离线只读视图
- `IsOfflineReadonly(now time.Time) bool`
- `IsExpired(now time.Time) bool`

行为要求：

- `Lookup` / `List` / `GetCachedContent` 在离线只读下继续可用
- `Request(...)` 在离线只读下直接返回关闭错误，不再尝试发请求
- `Bootstrap` 仍然只用于 live session 初始化，不用于恢复离线视图

### M3 — `pkg/session/manager.go`

`ManagerOptions` 新增：

- `OfflineReadOnlyTTL time.Duration`

`Manager` 保存该默认配置，并承担离线保留与惰性过期清理。

新增一个统一的断线收尾入口，语义类似：

- `HandleDisconnect(current *Session, serveErr error)`

逻辑要求：

1. 只有当 `current` 仍然是该 `user_id` 在 manager 中的当前条目时，才允许保留或移除
2. 若 `OfflineReadOnlyTTL <= 0`，直接移除
3. 若 TTL > 0，则把 session 标记为 `offline-readonly`，并记录离线截止时间
4. 若 session 已经过期，则从 manager 中移除

惰性清理策略：

- 不新增后台 goroutine
- 在 `Get`、`UserIDs`、`Register` 等已有入口上顺手清掉已过期的离线 session

这样对当前 1-20 用户规模足够简单，也不引入新的生命周期线程。

### M4 — `pkg/session/service.go`

当前 `Connect` 在 `Serve` 返回后使用 `defer manager.Remove(current)` 无条件移除 session，这会直接丢失离线只读窗口。

本阶段改为：

- `Serve` 返回后调用 `manager.HandleDisconnect(current, err)`
- 若旧 session 已被新连接替换，`HandleDisconnect` 必须识别并忽略，不得误删新 session
- `ErrSessionReplaced` 仍保持原语义，不进入离线保留

### M5 — `pkg/fusefs/view.go`

读路径调整为：

1. 先查元数据和内容缓存
2. 若缓存命中，直接返回
3. 若 session 处于 `offline-readonly` 且缓存未命中，立即返回 `ErrSessionOffline`
4. 若 session 仍为 live，则维持现有 RPC 读路径

写路径调整为：

- `writeChunk`
- `createFile`
- `mkdirAt`
- `removePath`
- `renamePath`
- `truncatePath`

上述 helper 在 session 处于 `offline-readonly` 时直接返回 `ErrSessionOffline`，不再进入 `requestFileOp`

### M6 — `app/server/main.go`

把 `cfg.OfflineReadOnlyTTL` 注入：

- `session.NewManager(session.ManagerOptions{...})`

## 3. 顶层数据流

### 3.1 断线保留

```text
Service.Connect
  -> bootstrap success
  -> manager.Register(live session)
  -> current.Serve(...)
  -> stream 断开
  -> manager.HandleDisconnect(current, serveErr)
       TTL <= 0 -> Remove(current)
       TTL > 0  -> current.RetainOffline(now + ttl)
```

### 3.2 离线读路径

```text
workspaceNode.Read
  -> fusefs.readChunk(relPath, off, size)
  -> lookupInfo / requireSession
  -> Session.GetCachedContent(relPath, info)
       hit  -> 本地切片返回
       miss -> Session.IsOfflineReadonly(now)?
                 yes -> ErrSessionOffline
                 no  -> 维持现有 requestRead RPC
```

### 3.3 离线写路径

```text
workspaceNode.Write/Create/Mkdir/Rename/Unlink/Setattr(truncate)
  -> fusefs helper
  -> Session.IsOfflineReadonly(now)?
       yes -> ErrSessionOffline
       no  -> 维持现有 requestFileOp RPC
```

### 3.4 重连恢复

```text
new daemon stream
  -> bootstrap success
  -> manager.Register(new live session)
  -> old offline session 被替换
  -> 后续所有请求都路由到新 live session
```

## 4. 错误语义

本阶段保持“系统调用语义稳定，离线原因明确”的原则。

- 离线只读期间：
  - 目录浏览成功
  - 缓存命中读成功
  - 未缓存读返回 `ErrSessionOffline`
  - 所有写操作返回 `ErrSessionOffline`
- TTL 过期后：
  - `manager.Get(userID)` 不再返回该 session
  - 后续访问同样表现为 `ErrSessionOffline`
- live session 中真正的请求失败：
  - 继续保留现有 `ErrRequestTimeout` / `ErrSessionFailed` 区分
  - 不与离线只读失败语义混淆

FUSE errno 映射保持现状：

- `ErrSessionOffline` -> `EIO`
- `ErrRequestTimeout` -> `ETIMEDOUT`
- `ErrSessionFailed` -> `EIO`

本阶段不引入新的 FUSE errno 类型。

## 5. 测试矩阵

### 5.1 `pkg/session`

- 断线后 session 被保留为 `offline-readonly`
- TTL 窗口内 `manager.Get(userID)` 仍返回该 session
- TTL 过期后在 `Get` / `UserIDs` / `Register` 上被惰性清理
- 新连接注册后替换旧离线 session
- `ErrSessionReplaced` 场景不进入离线只读保留

### 5.2 `pkg/fusefs/view_test.go`

- 离线时 `lookupInfo` / `listInfos` 继续可用
- 已缓存文件在离线窗口内可读
- 未缓存文件读取立即返回 `ErrSessionOffline`
- `writeChunk` / `createFile` / `mkdirAt` / `removePath` / `renamePath` / `truncatePath` 在离线窗口内全部返回 `ErrSessionOffline`

### 5.3 `pkg/fusefs/inmem_phase5_test.go`

- 先在线读出缓存，再模拟断线，验证缓存命中读仍可用
- 断线后读取未缓存路径失败
- 断线后写操作失败
- TTL 过期后目录浏览与读操作都失败
- 同一 `user_id` 重连后恢复 live 行为并替换旧离线视图

## 6. 兼容性与非目标

兼容性：

- 在线路径保持现状，不改变已有缓存、超时和写透逻辑
- `ErrSessionOffline` 仍复用既有错误分类与 FUSE errno 映射
- 配置默认开启离线只读保留，但可以通过 `<= 0` 关闭

非目标：

- 不做后台清理线程
- 不做离线期间的陈旧树修正
- 不在离线窗口里尝试自动补拉新内容
- 不把 Phase 6 的预取、敏感文件过滤、限流并入当前阶段

## 7. 验收基线

本阶段完成后，以下口径应成立：

- daemon 断线后，在 `offline_readonly_ttl` 窗口内，`ls` / `stat` 风格目录浏览仍可用
- 已缓存文件可继续读取
- 未缓存文件读取立即失败，不阻塞等待重连
- 所有写操作立即失败，不产生本地脏写
- TTL 到期后该用户完全下线
- 同一用户重连后恢复在线访问
- `make fmt`、`make lint`、`make test` 通过
