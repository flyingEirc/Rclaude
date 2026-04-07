# Phase 4b — 服务端整文件内容缓存设计

> 本文档是 Phase 4b 的设计 spec，不承担实施计划。
> 实施计划落在 `docs/exec-plan/active/{时间}-phase4b-cache/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)、[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 4
> 上一阶段：[`docs/exec-plan/completed/202604071253-phase4a-write-ops/`](/e:/Rclaude/docs/exec-plan/completed/202604071253-phase4a-write-ops/)
> 实现架构：[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)

## 0. 范围与原则

Phase 4a 已完成写透、超时和写后元数据即时可见。Phase 4b 只补齐剩余的服务端缓存能力：

- 把 `Session.tree` 正式收口为文件树缓存
- 增加服务端整文件内容缓存
- 让写响应和 daemon `FileChange` 统一触发缓存失效

范围外：

- 小文件预取
- 离线只读降级
- 旧缓存兜底读
- rename 后缓存搬迁复用

### 目标

让同一会话下的重复读请求优先命中服务端内存缓存，并在写入或 daemon 变更后立即失效，保持路径级一致性。

### 设计原则

- 内容缓存仅缓存普通文件完整字节，不缓存目录
- 缓存预算直接复用 `server.cache.max_bytes`
- `cache.max_bytes <= 0` 明确表示禁用内容缓存
- 失效优先于复用，只要路径发生变更就删缓存再走读透
- 读透逻辑不改变现有超时、错误分类和 FUSE errno 映射

## 1. 顶层数据流

```text
workspaceNode.Read
  -> fusefs.readChunk(relPath, off, size)
  -> Session.Lookup(relPath) 拿元数据
  -> Session.GetCachedContent(relPath, info.signature)
      hit  -> 本地切片返回
      miss -> 判断 size <= cache.max_bytes
                yes -> 发全量 ReadFileReq{offset:0,length:0}
                       Session.PutCachedContent(relPath, info, content)
                       切片返回
                no  -> 保持现有范围读取，不入缓存

写路径 / daemon change:
  -> Session.ApplyWriteResult / ApplyDelete / ApplyRename / applyChange / Bootstrap
  -> 更新 tree
  -> Invalidate(path) / InvalidatePrefix(path) / Clear()
```

## 2. 模块变更

### M1 — `pkg/contentcache`

新增独立包，提供字节预算 LRU：

- `New(maxBytes int64) *Cache`
- `Get(path string, sig Signature) ([]byte, bool)`
- `Put(path string, sig Signature, content []byte) bool`
- `Invalidate(path string)`
- `InvalidatePrefix(path string)`
- `Clear()`

其中 `Signature = {Size int64, ModTime int64}`。`Put` 对超预算对象返回 `false`，且不污染现有缓存。

### M2 — `pkg/session`

- `Session` 新增内容缓存字段
- `NewSession` 改为接受会话选项，注入 `CacheMaxBytes`
- 新增缓存辅助方法，供 `fusefs` 和会话内部变更流复用
- `Bootstrap` 在替换树之后清空内容缓存
- `applyChange`、`ApplyWriteResult`、`ApplyDelete`、`ApplyRename` 在更新树时同步做路径失效

### M3 — `pkg/session/manager.go` 与 `service.go`

- `ManagerOptions` 增加 `CacheMaxBytes int64`
- `Manager` 保存默认会话配置
- `Manager.NewSession(userID)` 统一构造带缓存配置的 `Session`
- `Service.Connect` 改为通过 `manager.NewSession(userID)` 创建会话

### M4 — `pkg/fusefs/view.go`

`readChunk` 改成三段式：

1. 先查会话元数据与内容缓存
2. 未命中且文件可缓存时，发一次全量读并写入缓存
3. 不可缓存时保留现有范围读取行为

写 helper 不新增功能，只继续依赖 `Session.Apply*` 触发失效。

### M5 — `app/server/main.go`

把 `cfg.Cache.MaxBytes` 注入 `session.NewManager(session.ManagerOptions{...})`。

## 3. 测试矩阵

- `pkg/contentcache`：命中、签名不匹配、LRU 淘汰、超预算不入缓存、前缀失效
- `pkg/session`：Bootstrap 清空缓存；ApplyWrite/Delete/Rename/change 后缓存正确失效；并发访问无 race
- `pkg/fusefs`：重复读只首读下发 RPC；写后失效；change/rename/delete 失效；禁用缓存时全部走直读
- `internal/inmemtest` + `pkg/fusefs/inmem_e2e_test.go`：重复读命中缓存；变更后重新拉取；Phase 4a 写链路不回归

## 4. 公开接口变更

- `session.ManagerOptions` 新增 `CacheMaxBytes int64`
- `session.Manager` 新增统一的 `NewSession(userID string) *Session`
- `session.Session` 新增缓存辅助方法：
  - `GetCachedContent`
  - `PutCachedContent`
  - `InvalidateContent`
  - `InvalidateContentPrefix`

## 5. 默认与假设

- 以当前工作区中的 Phase 4a 为基线继续开发，不回头重构 4a 代码布局
- 文件树缓存继续使用 `fstree.Tree`
- 内容缓存仅是在线加速层，不承担可用性兜底
- 最终 `go test -race` 以 Linux 环境结果为准
