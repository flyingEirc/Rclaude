# Phase 6B — 敏感文件过滤设计

> 本文档是 Phase 6B 的设计 spec，不承担实施计划。
> 实施计划落在 `docs/exec-plan/active/{时间}-phase6b-sensitive-file-filter/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](/root/Rclaude/docs/design/PLAN.md)、[`docs/design/ROADMAP.md`](/root/Rclaude/docs/design/ROADMAP.md) Phase 6
> 上一阶段：[`docs/exec-plan/completed/202604071013-phase6a-offline-readonly/`](/root/Rclaude/docs/exec-plan/completed/202604071013-phase6a-offline-readonly/)
> 实现架构：[`docs/ARCHITECTURE.md`](/root/Rclaude/docs/ARCHITECTURE.md)

## 0. 范围与原则

Phase 6A 已经补齐 daemon 断线后的 TTL 限定离线只读降级，但 Phase 6 中另一条明确目标“敏感文件过滤”仍未开始。当前 daemon 会把工作区中所有未命中 `workspace.exclude` 的路径都暴露给 server，这意味着 `.env`、私钥、证书等高风险文件会进入文件树、目录浏览和读写链路。

Phase 6B 只解决一个问题：**在不改协议、不改 server 会话模型的前提下，阻止敏感路径进入远端工作区视图，并拒绝执行环境对敏感路径的写入或重命名。**

范围内：

- daemon 侧内置敏感模式
- daemon 配置追加自定义敏感模式
- 初始扫描、目录监听、读请求、写请求统一复用同一过滤规则
- 读取侧“完全隐藏”
- 写入、创建、删除、重命名、截断对敏感路径统一拒绝
- 单测与 inmem 集成验证

范围外：

- 基于内容扫描的 secrets detection
- 允许用户关闭或覆盖内置敏感规则
- server 侧额外策略中心或审计日志
- Phase 6 其余子题：小文件预取、读写限流

设计原则：

- 敏感规则必须在 daemon 侧生效，不能只依赖 server/FUSE 隐藏
- “看不到”和“不能写进去”必须由同一套规则决定，避免链路分裂
- 内置规则默认强制启用，自定义规则只能追加，不能削弱安全边界
- 读取相关操作优先表现为“路径不存在”，避免把敏感文件暴露成可见目标
- 写入相关操作优先表现为“permission denied”，明确拒绝执行环境把内容写进敏感名

## 1. 对外行为语义

### 1.1 读取侧：完全隐藏

命中敏感规则的路径，在执行环境看来等同于不存在：

- 初始文件树不上报该路径
- `Readdir` 看不到该文件或目录
- `Lookup` / `Getattr` / `Stat` 返回不存在
- `Read` / `cat` / `sed` / `grep` 访问该路径返回不存在

如果命中的是目录，则整棵子树都不进入远端视图。

### 1.2 写入侧：直接拒绝

以下操作只要源路径或目标路径命中敏感规则，就立即拒绝：

- `Write`
- `Create`
- `Mkdir`
- `Delete`
- `Truncate`
- `Rename`

统一语义：

- 返回 `permission denied`
- daemon 本地不执行对应文件系统修改
- 不产生“部分成功”结果

特殊规则：

- `Rename normal -> sensitive`：拒绝
- `Rename sensitive -> normal`：拒绝
- `Delete sensitive`：拒绝

这意味着执行环境即使主动猜测 `.env`、`id_rsa` 这类路径名，也不能借由写操作改变本地敏感文件。

### 1.3 本地已有敏感文件的表现

如果用户工作区中本来就存在敏感文件：

- 远端 `ls` / `find` / `stat` / `cat` 看不到它
- watcher 不会上报它的新增、修改、删除事件
- 执行环境不能直接删除、覆盖、截断或重命名它

本地用户仍然可以在真实工作区中手动修改这些文件；只是这些变化不会进入远端工作区视图。

## 2. 模式模型与配置策略

### 2.1 配置入口

只新增 daemon 侧配置：

```yaml
workspace:
  path: /repo
  exclude: [".git", "node_modules"]
  sensitive_patterns:
    - "secrets/**"
    - "*.local.env"
```

新增字段：

- `workspace.sensitive_patterns []string`

约束：

- 内置敏感规则始终生效
- `sensitive_patterns` 只做追加，不能关闭或覆盖内置规则
- 自定义模式非法时，daemon 启动直接失败

`workspace.exclude` 继续保留原语义：表示“功能性忽略”。  
`workspace.sensitive_patterns` 表示“安全隐藏”，除了不进入远端视图，还会额外拦截主动写入。

### 2.2 匹配语义

敏感模式复用当前 `exclude` 的匹配语义，保持使用成本一致：

- 不含 `/` 的模式按 basename 匹配
- 含 `/` 的模式按相对路径匹配
- 路径统一使用 forward slash
- 命中目录规则时，目录本身和其子树都视为敏感

示例：

- `*.pem` 匹配任意目录下的 `a.pem`
- `secrets/**` 只匹配相对路径中的 `secrets/` 子树
- `.env.*` 匹配 `.env.dev`、`.env.local`

### 2.3 内置规则

内置规则收敛为“高风险且误伤成本可控”的一组：

- `.env`
- `.env.*`
- `*.pem`
- `*.key`
- `*.p12`
- `*.pfx`
- `*.crt`
- `*.cer`
- `*.p8`
- `id_rsa`
- `id_dsa`
- `id_ecdsa`
- `id_ed25519`
- `*_secret`
- `*_secret.*`

明确不作为默认内置规则的模式：

- `*token*`
- `*credential*`
- 过于宽泛的 `*secret*`

原因是这些模式很容易误伤正常源码文件，例如 `token.go`、`credential_test.go`、`secret_manager.md`。如果业务确实需要这类更宽的过滤，应由 `workspace.sensitive_patterns` 显式追加。

SSH 公钥如 `id_ed25519.pub` 不在内置隐藏范围内；Phase 6B 只默认隐藏私钥名。

## 3. 模块改动

### M1 — `pkg/config`

为 `Workspace` 新增：

- `SensitivePatterns []string`

要求：

- `LoadDaemon` 能正确加载该字段
- 默认值为空切片或零值，表示仅使用内置规则
- 不新增 server 配置项

### M2 — `pkg/syncer/sensitive_filter.go`

在 `pkg/syncer` 内新增一个小型过滤组件，统一承载敏感路径判断。

职责：

- 合并内置规则与 `workspace.sensitive_patterns`
- 在 daemon 启动时一次性校验所有模式是否合法
- 暴露统一判断方法，例如 `Match(relPath string) bool`

该组件只负责“路径是否敏感”，不直接决定返回 `not found` 还是 `permission denied`；具体错误语义由 `Scan`、`Watch`、`Handle` 按操作类型解释。

之所以放在 `pkg/syncer` 而不是新建通用包，是因为当前能力只被 daemon 链路使用，范围和依赖都收敛在 `syncer` 内。

### M3 — `pkg/syncer/scan.go`

`ScanOptions` 新增敏感过滤依赖。

行为要求：

- 敏感文件不上报
- 敏感目录直接 `SkipDir`
- 非敏感路径保持当前逻辑不变

这样 server 收到的初始文件树天然不包含敏感路径，后续 `Lookup` / `List` 会直接把它们视为不存在。

### M4 — `pkg/syncer/watch.go`

`WatchOptions` 新增敏感过滤依赖。

行为要求：

- 初始递归添加 watcher 时跳过敏感目录
- 事件到达时，命中敏感规则的路径不向 server 投递变更
- 新建敏感目录时不递归 `watcher.Add`

重命名场景的目标行为：

- `normal -> sensitive`：server 至少收到旧可见路径的删除，新的敏感路径不进入视图
- `sensitive -> normal`：server 可以收到新的普通路径创建事件，使其进入视图

Phase 6B 不需要额外扩展协议去显式标记“这是敏感过滤事件”；只需保证 server 视图与敏感规则一致。

### M5 — `pkg/syncer/handle.go` / `handle_write.go`

`HandleOptions` 新增敏感过滤依赖，所有请求都在 daemon 本地再次检查，避免出现“扫描已隐藏，但主动 RPC 仍能碰到真实文件”的漏洞。

读类请求：

- `Read`
- `Stat`
- `ListDir`

行为：

- 路径命中敏感规则时，返回格式化后的 `fs.ErrNotExist`

写类请求：

- `Create`
- `Write`
- `Mkdir`
- `Delete`
- `Rename`
- `Truncate`

行为：

- 源路径或目标路径命中敏感规则时，返回格式化后的 `fs.ErrPermission`

说明：

- 当前协议中的 `Create` 仍通过现有 `WriteFileReq` 创建链路落地，因此不需要新增 proto 分支，只需在 `handleWrite` 中覆盖“目标路径敏感”判断

这样做的直接收益是：

- 不改 proto
- 不改响应结构
- 继续复用现有 server/FUSE 的字符串错误归类，把“no such file”映射为 `ENOENT`，把“permission denied”映射为 `EACCES`

### M6 — `pkg/syncer/daemon.go`

daemon 启动流程增加一步：

1. 从配置读取 `workspace.sensitive_patterns`
2. 构造敏感过滤器
3. 注入 `ScanOptions`、`WatchOptions`、`HandleOptions`

如果过滤器构造失败，daemon 启动失败，不进入连接和扫描阶段。

### M7 — server/FUSE 侧

本阶段不计划修改 proto、`pkg/session`、`pkg/transport` 或 FUSE 主逻辑。

原因：

- 敏感路径在文件树层面已被隐藏，`Lookup/List` 会自然表现为不存在
- 主动写入敏感路径时，daemon 返回的 `permission denied` 已能被现有 `classifyError` 识别
- 现有 errno 映射已经覆盖 `ENOENT` 与 `EACCES`

server/FUSE 侧只需要补充测试，确认这两种错误语义在端到端路径上保持稳定。

## 4. 顶层数据流

### 4.1 初始扫描

```text
daemon.Run
  -> build SensitiveFilter(builtins + config additions)
  -> Scan(root)
  -> skip sensitive entries
  -> send FileTree(without sensitive paths)
  -> server session tree 不包含敏感路径
```

### 4.2 目录监听

```text
fsnotify event
  -> watchRelativePath(rel)
  -> SensitiveFilter.Match(rel)?
       yes -> drop event
       no  -> forward FileChange
```

### 4.3 读请求

```text
FUSE read/stat/list
  -> session tree lookup
       not found -> ENOENT
       found     -> request daemon
  -> daemon Handle(read-like)
       sensitive -> fs.ErrNotExist
       normal    -> real filesystem op
```

双层保护的意义：

- 正常情况下，敏感路径根本不会出现在 session tree 中
- 即使执行环境主动猜路径，daemon 侧也会再次拦截

### 4.4 写请求

```text
FUSE write/create/delete/rename/truncate
  -> request daemon
  -> daemon Handle(mutating)
       source or target sensitive -> fs.ErrPermission
       normal                     -> real filesystem mutation
```

## 5. 错误语义

Phase 6B 保持“读隐藏、写拒绝”的固定规则。

读取相关：

- `Lookup` / `Stat` / `Read` / `ListDir` 命中敏感路径时，表现为 `path not found`
- FUSE errno 应落到 `ENOENT`

写入相关：

- `Write` / `Create` / `Mkdir` / `Delete` / `Rename` / `Truncate` 命中敏感路径时，表现为 `permission denied`
- FUSE errno 应落到 `EACCES`

本阶段不新增新的 typed error，也不修改 gRPC 响应结构；继续依赖现有 `syncer` 错误字符串和 `fusefs.classifyError` 的映射规则。

## 6. 测试与验收

### 6.1 单测

至少覆盖以下层次：

- `pkg/config/config_test.go`
  - `sensitive_patterns` 正常加载
  - 空配置仅保留内置规则
- `pkg/syncer/sensitive_filter_test.go`
  - 内置规则命中
  - 自定义规则追加
  - 非法模式构造失败
- `pkg/syncer/scan_test.go`
  - `.env`、`*.pem`、私钥名不进入扫描结果
  - 敏感目录整棵子树被跳过
- `pkg/syncer/watch_test.go`
  - 敏感路径事件不会投递
  - 新建敏感目录不会继续递归监听
- `pkg/syncer/handle_test.go` / `handle_write_test.go`
  - `Read/Stat/ListDir` 命中敏感路径返回不存在
  - `Write/Delete/Mkdir/Rename/Truncate` 命中敏感路径返回 permission denied
  - `Rename normal -> sensitive` 与 `Rename sensitive -> normal` 都被拒绝

### 6.2 集成测试

在现有 `inmem` 测试链路上补一组端到端验证：

- 本地存在 `.env`，远端 `ls` 看不到
- 远端 `cat` / `stat` `.env` 返回不存在
- 远端尝试创建 `.env` 或把普通文件重命名成 `.env` 返回权限错误
- 普通文件路径行为不受回归影响

### 6.3 阶段验收

Phase 6B 完成时，应满足：

- `go build ./...` 通过
- `make fmt`、`make lint`、`make test` 全绿
- inmem 集成链路能验证“读隐藏、写拒绝”
- Linux 真 FUSE 环境下，敏感路径的 errno 维持 `ENOENT/EACCES` 预期

## 7. 风险与取舍

| 风险 | 说明 | 应对 |
|---|---|---|
| 误伤正常源码文件 | 过宽模式可能把正常代码从远端视图中隐藏 | 内置规则只保留高风险且较窄的模式；更宽规则必须显式配置 |
| 规则散落多处 | 扫描、watch、handle 若各自维护规则，容易行为不一致 | 统一通过 `SensitiveFilter` 判断 |
| 只在 server 隐藏不够安全 | 主动 RPC 仍可能打到真实文件 | 强制在 daemon `Handle` 再做一层拦截 |
| 删除敏感路径返回权限错误会暴露“受保护”语义 | 与纯隐藏相比会给出更明确反馈 | 这是当前产品选择，用来清晰拒绝远端写入敏感名；读取面仍保持完全隐藏 |
