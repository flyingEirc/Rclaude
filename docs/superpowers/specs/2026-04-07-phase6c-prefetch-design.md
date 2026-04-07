# Phase 6C — 小文件预取设计

> 本文档是 Phase 6C 的设计 spec，不承担实施计划。
> 实施计划落在 `docs/exec-plan/active/{时间}-phase6c-prefetch/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](/root/Rclaude/docs/design/PLAN.md)、[`docs/design/ROADMAP.md`](/root/Rclaude/docs/design/ROADMAP.md) Phase 6
> 上一阶段：[`docs/exec-plan/completed/202604071148-phase6b-sensitive-file-filter/`](/root/Rclaude/docs/exec-plan/completed/202604071148-phase6b-sensitive-file-filter/)
> 实现架构：[`docs/ARCHITECTURE.md`](/root/Rclaude/docs/ARCHITECTURE.md)

## 0. 范围与原则

Phase 4B 已经具备服务端整文件内容缓存，Phase 5 和 Phase 6A/6B 则把写路径、异常回归、离线只读和敏感文件过滤稳定下来。但当前读性能仍然完全依赖“首次读取后再缓存”，典型的 `ls` 后连续 `cat` 场景依然会为每个文件单独走一次 `FUSE -> gRPC -> daemon -> 本地文件系统` 链路。

`PLAN.md` 已明确给出推荐方向：在目录读取后，异步预取该目录下的小文件内容，提高后续读请求的缓存命中率。Phase 6C 只落实这一条能力，不扩展为完整的调度、批量协议或限流系统。

本阶段目标是：**在不改变现有读写语义的前提下，让 `Readdir` 成功后自动触发保守的小文件后台预取，并把结果写入现有内容缓存。**

范围内：

- 服务端 `prefetch` 配置与默认值
- `Readdir` 成功后的异步小文件预取
- 复用现有 `contentcache`、`Session.Request`、缓存失效链路
- 轻量的同路径并发去重，避免重复预取
- 单测与 `inmem` 集成测试覆盖预取命中、跳过与失效

范围外：

- proto 改动
- daemon 端批量预取或任何行为变更
- 独立后台调度器 / 队列服务
- 递归目录预取
- 大文件分段预热
- 读取/上传限流
- 预取指标埋点或完整日志体系

设计原则：

- 预取是 opportunistic optimization，不得影响 `Readdir` 主路径结果
- 只复用现有内容缓存，不引入第二套缓存模型
- 预取触发点保持单一，只挂在目录读取成功之后
- 默认策略必须保守，避免一次 `ls` 触发过多额外 I/O
- 预取失败必须静默降级，不能改变用户可见 errno 语义

## 1. 配置与行为语义

### 1.1 新增配置

为 `ServerConfig` 新增：

- `prefetch.enabled`
- `prefetch.max_file_bytes`
- `prefetch.max_files_per_dir`

推荐默认值：

- `enabled: true`
- `max_file_bytes: 102400`
- `max_files_per_dir: 16`

语义约束：

- `enabled=false` 时完全禁用预取
- `max_file_bytes<=0` 时不预取任何文件
- `max_files_per_dir<=0` 时不预取任何文件
- 若 `cache.max_bytes<=0`，即使 `prefetch.enabled=true` 也必须直接跳过，因为无处落缓存

### 1.2 触发条件

预取只在以下条件同时满足时触发：

1. `Readdir` / 目录列举成功
2. 对应 `Session` 当前不是离线只读
3. 内容缓存功能已启用
4. `prefetch.enabled=true`

触发后只处理“当前目录的直接子文件”，不递归子目录。

### 1.3 候选文件筛选

候选文件必须满足：

- 不是目录
- `size > 0`
- `size <= prefetch.max_file_bytes`
- 当前内容缓存中尚未命中
- 当前不在预取中的 in-flight 集合里

候选数量上限为 `prefetch.max_files_per_dir`。如果目录中满足条件的文件更多，则按目录项排序后的稳定顺序截断。

## 2. 模块改动

### M1 — `pkg/config`

新增：

- `type PrefetchConfig struct`
- `ServerConfig.Prefetch PrefetchConfig`

默认配置中写入保守默认值。`Validate` 不把零值视作配置错误，因为它们有明确的“关闭预取”语义。

### M2 — `pkg/session/manager.go`

`ManagerOptions` 新增预取配置字段，并在 `Manager` 中保存只读配置快照，供 `fusefs` 查询：

- `PrefetchEnabled bool`
- `PrefetchMaxFileBytes int64`
- `PrefetchMaxFilesPerDir int`

`Manager` 不承担预取执行本身，只承担配置透传，避免把读优化逻辑塞入会话管理层。

### M3 — `pkg/session/session.go`

`Session` 增加一个很小的预取进行中集合，用于同路径并发去重：

- `prefetchMu sync.Mutex`
- `prefetching map[string]struct{}`

新增辅助方法，语义类似：

- `TryStartPrefetch(relPath string) bool`
- `FinishPrefetch(relPath string)`

规则：

- 同一路径同一时刻最多允许一个后台预取
- goroutine 退出时必须清理标记
- 预取集合不参与持久化，不影响已有关闭/替换语义

### M4 — `pkg/fusefs/view.go`

保持 `listInfos(...)` 的职责单一，只返回目录项。新增一个 fire-and-forget 的预取入口，放在目录读取成功之后调用，职责如下：

1. 读取 `Manager` 上的预取配置
2. 快速判断是否需要跳过
3. 基于 `listInfos` 返回结果筛选候选文件
4. 为每个候选文件启动轻量 goroutine
5. goroutine 内执行整文件 `ReadFileReq(offset=0,length=0)`，成功后写入现有内容缓存

预取写缓存时继续使用：

- `Session.PutCachedContent(relPath, info, content)`

因此签名校验仍然基于现有 `size + mod_time`。

### M5 — `pkg/fusefs/mount_linux.go`

Linux FUSE 侧不改变 `Readdir` 行为和返回值，只在目录项收集成功后调用新的预取入口。无论预取是否启动或失败，`fs.NewListDirStream(entries)` 的返回值都保持不变。

## 3. 顶层数据流

### 3.1 命中场景

```text
执行环境执行 ls /workspace/user/dir
  -> workspaceNode.Readdir
  -> listInfos(manager, user, "dir")
  -> 立即返回目录项给 FUSE
  -> 后台触发 prefetch(dir children)
       -> 对每个候选文件发 ReadFileReq(offset=0,length=0)
       -> success -> PutCachedContent(path, info, content)

后续 cat /workspace/user/dir/a.txt
  -> workspaceNode.Read
  -> readChunk(...)
  -> GetCachedContent("dir/a.txt", latest info)
       -> hit -> 直接返回缓存内容
```

### 3.2 跳过场景

```text
Readdir 成功
  -> prefetch enabled?
       no  -> return
  -> cache enabled?
       no  -> return
  -> session offline-readonly?
       yes -> return
  -> candidate files after filtering == 0?
       yes -> return
```

### 3.3 预取失败场景

```text
goroutine prefetch file
  -> Request(ReadFileReq whole file)
       timeout / offline / rpc error / response error
       -> discard and FinishPrefetch(path)
  -> success
       -> PutCachedContent(path, info, content)
       -> FinishPrefetch(path)
```

## 4. 关键语义与错误处理

### 4.1 不影响主路径

`Readdir` 是否成功只由目录读取本身决定。预取发生在目录项已经拿到之后，因此：

- 预取不参与 FUSE errno 决策
- 预取失败不会让 `ls` 失败
- 预取超时不会阻塞 `Readdir` 返回

### 4.2 与离线只读共存

离线只读窗口中允许目录浏览，但不允许继续向 daemon 发起新读请求。因此：

- session 处于 `offline-readonly` 时禁止启动新的预取
- 若预取 goroutine 启动后 session 在执行过程中变为 `offline-readonly`，请求失败后直接丢弃结果

### 4.3 与缓存失效共存

本阶段不新增专用失效逻辑，继续复用现有：

- `ApplyWriteResult`
- `ApplyDelete`
- `ApplyRename`
- `applyChange`

如果文件在预取期间被修改：

- 旧内容即使已写入 cache，后续 `GetCachedContent` 也会因 `FileInfo` 的 `size/mod_time` 变化而判定签名不匹配，并自动失效

### 4.4 并发去重边界

in-flight 去重只保证“同一路径同一时刻最多一个预取 goroutine”，不保证目录级串行，也不尝试跨 session 去重。这足以解决连续 `ls` 或短时间内重复 `Readdir` 导致的同文件重复预取问题。

## 5. 测试策略

### 5.1 `pkg/config`

覆盖：

- 默认值加载
- YAML 显式配置加载
- `enabled=false`、`max_file_bytes=0`、`max_files_per_dir=0` 的关闭语义

### 5.2 `pkg/fusefs/view_test.go`

覆盖：

- 目录读取后会对小文件发起整文件读，并把内容写入 cache
- 预取是异步的，不阻塞 `listInfos` / `Readdir` 返回
- 超过 `max_file_bytes` 的文件跳过
- 超过 `max_files_per_dir` 的候选截断
- `cache.max_bytes=0` 时跳过
- `offline-readonly` 时跳过
- 同一路径重复目录读取不会并发重复预取

### 5.3 `pkg/fusefs/inmem` 集成测试

覆盖：

- `ls` 后读取同目录小文件，后续 `read` 命中缓存，daemon 读次数下降
- daemon 变更后旧预取内容不会继续命中

## 6. 验收标准

- 新增 `prefetch` 配置可加载且有默认值
- `Readdir` 成功后异步预取当前目录内的小文件
- 后续 `Read` 能命中现有 `contentcache`
- 预取失败不改变目录读取结果或 errno
- 写入、重命名、删除、daemon 变更后缓存仍能正确失效
- `make fmt`、`make lint`、`make test` 全部通过

## 7. 风险与限制

- 一次 `ls` 会主动增加一些额外读请求，因此默认阈值必须保守
- 当前阶段不接限流，目录中文件很多时只能通过 `max_files_per_dir` 做数量硬截断
- 不做指标埋点意味着命中收益主要依赖测试和后续人工验证
- 预取只覆盖“目录列举后读取”的热路径，不提升直接按路径首次读取的大文件或冷文件场景
