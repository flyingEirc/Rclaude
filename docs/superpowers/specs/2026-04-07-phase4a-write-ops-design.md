# Phase 4a — 写操作主链路设计

> 本文档是 Phase 4a 的设计 spec（brainstorming 阶段产物），不承担实施计划。
> 实施计划由 writing-plans skill 在 spec 通过后生成于 `docs/exec-plan/active/{时间}-phase4a-write-ops/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)、[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 4
> 上一阶段：[`docs/exec-plan/completed/202604071046-phase3-server-fuse-mvp/`](/e:/Rclaude/docs/exec-plan/completed/202604071046-phase3-server-fuse-mvp/)
> 实现架构：[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)

## 0. 范围与原则

ROADMAP 把 Phase 4 定义为「文件树缓存 / 内容缓存 / 缓存失效 + Write/Create/Mkdir/Rename/Unlink + 写透与超时控制」。Phase 4a 仅承担其中**写操作主链路**和**自我写抑制**部分；任何 server 端缓存（文件树缓存、内容缓存、缓存失效）一律推迟到 Phase 4b。

### 目标（一句话）

让 FUSE 挂载点下的写操作（`echo > x`、`vim` 保存、`mkdir`、`mv`、`rm`、`truncate`）以**同步写透**的方式真实落到 daemon 工作区，errno 与本地文件系统语义一致；不引入任何缓存。

### 范围内

- proto 微调：`WriteFileReq` 增加 `offset` 字段；新增 `TruncateReq`
- daemon `pkg/syncer/handle.go`：补齐 Write / Create / Mkdir / Delete / Rename / Truncate 处理
- daemon 新增 `pkg/syncer/locker.go`（按 path 串行化）和 `pkg/syncer/selfwrite.go`（self-write 过滤器）
- daemon `pkg/syncer/watch.go`：接受 self-write filter，命中事件不再上送
- server `pkg/session`：暴露 `Apply*` 写后立即可见入口；新增 `RequestTimeout` 配置入口
- server `pkg/config`：`ServerConfig` 增加 `RequestTimeout`；`DaemonConfig` 增加 `SelfWriteTTL`
- server `pkg/fusefs/view.go`：新增六个跨平台写 helper；扩展错误关键字表
- server `pkg/fusefs/mount_linux.go`：在 `workspaceNode` 上实现写相关 FUSE Node 接口；放开 `Open` 的写 flag
- 新增 `internal/testutil/inmem_transport.go`：内存版双向 transport，用于端到端测试
- 测试：单测 + in-memory 端到端冒烟，`go test -race` 必须通过

### 范围外

- 任何 server 端缓存（文件树缓存、内容缓存、缓存失效）→ Phase 4b
- Setattr 的 mode/atime/mtime/uid/gid → Phase 6（4a 内默认接受但不下发）
- Symlink / Hardlink / Mknod → 不在系统目标内
- 大文件预取、限流、敏感文件过滤 → Phase 6
- Linux 真实 mount 的自动化 CI、断线降级、离线只读 → Phase 5/6
- daemon 跨设备 rename、daemon 端 fsync 控制 → 由 Go 标准库自然语义决定，不在本阶段设计

### 设计原则

- **简单优先**：CLAUDE.md 明确「用普通函数和简单模式优于抽象」。新引入的两个工具（`pathLocker`、`selfWriteFilter`）作为 `pkg/syncer` 内部组件，不下沉到独立包
- **同步写透**：所有写 RPC 在 FUSE 节点接口里同步等待响应；不引入异步缓冲、合并或回写队列
- **错误就是字符串**：daemon 用 `%v` 透传 Go error 原文；view 层按关键字归类成 typed error；`errnoFromError` 翻成 syscall.Errno。proto 不引入 ErrorCode enum
- **echo 从源头消除**：daemon 自己刚写过的 path 在短窗口内屏蔽 fsnotify 回声，不在 server 侧做后置去重
- **写后立刻可见**：daemon 在写响应里回填最新 `FileInfo`，server 端 `Session` 立刻 apply 到本地树，消除「Create 后立刻 Lookup 拿不到」的窗口

### 用户决策记录

下列决策已在 brainstorming 中由用户确认，作为本 spec 的硬约束：

| ID | 决策 | 取值 |
|---|---|---|
| Q1 | Phase 4 拆分 | 拆成 4a（写操作）+ 4b（缓存）；本 spec 仅覆盖 4a |
| Q2 | 写入语义 | 同步写透（write-through, synchronous） |
| Q3 | 写操作集合 | Create 复用 WriteFileReq；新增 TruncateReq；DeleteReq 共用 Unlink/Rmdir；不实现 Setattr 其它属性 / Symlink / Hardlink |
| Q3' | proto 改动 | `WriteFileReq` 增加 `int64 offset = 5`；新增 `TruncateReq{path, size}` 挂到 `FileRequest.operation` |
| Q4-1 | 超时策略 | 配置可调 + 默认值（C）；`config.ServerConfig.RequestTimeout` 默认 10s |
| Q4-2 | 错误映射 | 字符串 + 关键字解析（路径 2） |
| Q5-1 | echo 处理 | daemon 端 self-write 抑制（B），TTL 默认 2s |
| Q5-2 | daemon 写并发 | 按 path 加锁，不同 path 并发 |
| Q6 | 测试边界 | 在 `internal/testutil` 加内存 transport，端到端冒烟（B），不引入 Linux runner CI |
| Q7 | 代码组织 | 新组件放 `pkg/syncer` 内（方案 X），不抽独立包 |
| 一致性窗口 | Create 后立刻 Lookup | 修法 A：daemon 写响应回填 FileInfo + `Session.Apply*` |

## 1. 顶层数据流

```text
执行环境  echo hi > /workspace/u1/path/foo.txt
   │
   ▼
Linux kernel FUSE
   │   Setattr(size=0)             ← truncate
   │   Open(O_WRONLY|O_CREAT)
   │   Write(off=0, "hi")
   │   Flush
   │
   ▼
pkg/fusefs/mount_linux.go workspaceNode
   │   每个 Node 接口调用 → pkg/fusefs/view.go 的 helper
   │
   ▼
view.go helper
   │   构造 FileRequest (TruncateReq / WriteFileReq{offset,...} / ...)
   │   ctx, cancel := context.WithTimeout(parentCtx, manager.RequestTimeout())
   │   resp, err := session.Request(ctx, req)
   │
   ▼
pkg/session.Session.Request
   │   注册 pending[req_id]，把 ServerMessage{request} 推到 send queue
   │
   ▼ gRPC bidi stream
   │
daemon 接收 ServerMessage → syncer.Handle(req, opts)
   │   pathLocker.Lock(req.path)                ← 同 path 串行化
   │   selfwrites.Remember(req.path, ttl=2s)    ← 在落盘之前登记
   │   执行真实 fs 操作 (os.WriteAt / os.Mkdir / os.Remove / os.Rename / os.Truncate)
   │   构造 FileResponse{Success: true, Result: &FileResponse_Info{Info: lstatToInfo(abs)}}
   │   pathLocker.Unlock
   │
   ▼ DaemonMessage{response}
   │
pkg/session 收到 response → pending[req_id] <- resp → view.go helper 返回
   │
   ▼
view.go helper：
   │   resp.success == false → classifyRequestErr(err, resp.error) → typed error
   │   resp.success == true  → manager.session(userID).ApplyWriteResult(resp.Info) → 返回 nil
   │
   ▼
mount_linux.go：errnoFromError → 返回 syscall.Errno → FUSE → kernel → 调用方

并行在 daemon 内：
   fsnotify 触发对 path 的 Write 事件
   watch 内的 selfwrites.ShouldSuppress(path) == true → 丢弃，不发 FileChange
```

关键不变式：

1. **Self-write 登记必须在落盘之前**，否则 fsnotify 可能比 `Remember()` 先到达 watch loop
2. **TTL 选 2s**：远大于 fsnotify 的事件延迟，远小于业务可感知时间
3. **写响应回填 FileInfo**：消除 Create/Write/Mkdir/Truncate/Rename 后立即查询的一致性窗口
4. **每条 FUSE 写调用都自带 timeout context**：FUSE 自己的 ctx 与 `WithTimeout(manager.RequestTimeout())` 复合
5. **`session.Session.Request` 不需要任何改动**：现有的 pending map + send queue 是泛型的，Phase 3 只是恰好只走过 Read

## 2. proto 改动

`api/proto/remotefs/v1/remotefs.proto` 增量：

```proto
message WriteFileReq {
  string path = 1;
  bytes content = 2;
  bool append = 3;
  uint32 mode = 4;
  int64 offset = 5;   // 新增：append=false 时按 offset WriteAt；append=true 时忽略 offset
}

message TruncateReq {
  string path = 1;
  int64 size = 2;     // 目标大小，>=0
}

message FileRequest {
  string request_id = 1;
  oneof operation {
    ReadFileReq read = 2;
    WriteFileReq write = 3;
    StatReq stat = 4;
    ListDirReq list_dir = 5;
    DeleteReq delete = 6;
    MkdirReq mkdir = 7;
    RenameReq rename = 8;
    TruncateReq truncate = 9;   // 新增
  }
}
```

`FileResponse` **不动**：

- 写操作的成功只看 `success`
- 失败时 daemon 在 `error` 字段里写约定字符串
- 成功时通过现有 `oneof result` 的 `FileInfo info = 5` 槽位回填最新 FileInfo（delete 除外）

protoc 生成产物（`remotefs.pb.go` / `remotefs_grpc.pb.go`）按仓库约定一并入库。

## 3. 模块拆分

### M1 — `pkg/syncer/locker.go`（按 path 串行化）

职责：相同 path 的写请求互斥；不同 path 的写请求并发。

```go
type pathLocker struct {
    mu    sync.Mutex
    locks map[string]*pathEntry
}

type pathEntry struct {
    mu       sync.Mutex
    refCount int
}

func newPathLocker() *pathLocker

// Lock 取出（或创建）path 对应的 mutex 并加锁，返回 unlock 函数；
// unlock 会减少 refCount 并在归零时回收 entry，避免 map 无限增长。
func (l *pathLocker) Lock(path string) (unlock func())
```

实现要点：

- 用 `safepath.ToSlash` 把 path 统一成 forward slash 后再做 key
- `refCount` 在 `Lock` 时 +1、`unlock` 时 -1；归零时从 map 删除
- 不暴露第二个返回值或 channel；保持「取锁→操作→释放」最简语义

### M2 — `pkg/syncer/selfwrite.go`（self-write 过滤器）

职责：daemon 自己刚写过的 path 在短窗口内屏蔽 fsnotify 回声。

```go
type selfWriteFilter struct {
    ttl     time.Duration
    nowFn   func() time.Time   // 可注入，便于测试
    mu      sync.Mutex
    entries map[string]time.Time // path → expiry
}

func newSelfWriteFilter(ttl time.Duration) *selfWriteFilter

// Remember 标记 path 在 now+ttl 之前应被抑制。
// 多个 path 一次性登记（rename 时同时登记 old/new）。
func (f *selfWriteFilter) Remember(paths ...string)

// ShouldSuppress 若 path 在 ttl 窗口内被登记过则返回 true。
// 调用本身不消耗记录（同一窗口内可能多次 fsnotify 事件需要全部抑制）。
// 同时顺手清理已过期的 entry。
func (f *selfWriteFilter) ShouldSuppress(path string) bool
```

实现要点：

- 不使用 timer，惰性 GC：`ShouldSuppress` 顺路清理过期 entry
- TTL 默认 2s，构造时通过参数注入
- 测试通过 `nowFn` 注入虚拟时钟，所有时间断言不依赖墙钟

### M3 — `pkg/syncer/handle_write.go`（五个写处理函数）

> daemon 侧只有 5 个 handler，因为 Create 在 proto 层复用 `WriteFileReq{content: nil}`，由 `handleWrite` 同时承担。view 层的 6 个 helper（含独立的 `createFile`）属于 server 侧 API 区分。

```go
type writeDeps struct {
    locker     *pathLocker
    selfWrites *selfWriteFilter
}

func handleWrite(reqID string, r *remotefsv1.WriteFileReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse
func handleMkdir(reqID string, r *remotefsv1.MkdirReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse
func handleDelete(reqID string, r *remotefsv1.DeleteReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse
func handleRename(reqID string, r *remotefsv1.RenameReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse
func handleTruncate(reqID string, r *remotefsv1.TruncateReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse
```

每个函数的统一骨架：

```text
1. safepath.Join(root, rel) → abs；失败返回 errResponse(reqID, "syncer: unsafe path: ...")
2. unlock := deps.locker.Lock(rel); defer unlock()
3. deps.selfWrites.Remember(rel)              ← 必须在落盘之前
4. 执行 os.* 操作
5. 失败：errResponse(reqID, formatErr(op, rel, err))
6. 成功：组装 FileResponse{Success: true, Result: &FileResponse_Info{Info: lstatToInfo(abs, rel)}}
       （delete 不回填 info）
```

各操作的具体 fs 调用：

| op | os 调用 | 备注 |
|---|---|---|
| Write (append=false) | `f, _ := os.OpenFile(abs, O_RDWR\|O_CREATE, 0o644); f.WriteAt(content, offset); f.Close()` | offset<0 视为非法参数 |
| Write (append=true)  | `f, _ := os.OpenFile(abs, O_WRONLY\|O_APPEND\|O_CREATE, 0o644); f.Write(content); f.Close()` | 忽略 offset |
| Mkdir (recursive=false) | `os.Mkdir(abs, perm)` | perm 默认 0o755 |
| Mkdir (recursive=true)  | `os.MkdirAll(abs, perm)` | |
| Delete | `os.Remove(abs)` | 同时支持文件和空目录 |
| Rename | `safepath.Join(root, old)` + `safepath.Join(root, new)` + `os.Rename(absOld, absNew)` | 跨设备由 `os.Rename` 自然返回 EXDEV |
| Truncate | `os.Truncate(abs, size)` | 不存在则失败，不自动 create；size<0 视为非法参数 |

并发与一致性约束：

- **Rename 必须同时锁 old/new 两个 path**，按字典序加锁避免死锁；同时 `selfWrites.Remember(oldRel, newRel)` 一次性登记两条
- **Write 不主动 fsync**：FUSE 的 Flush 不下发 RPC，由 OS 文件 close 触发普通 flush 即可
- **O_CREATE 用 0o644 占位**：协议 mode=0 时表示「使用默认」

### M4 — `pkg/syncer/handle.go`（dispatch 改造）

```go
type HandleOptions struct {
    Root        string
    MaxReadSize int64
    Locker      *pathLocker      // 新增
    SelfWrites  *selfWriteFilter // 新增
}

func Handle(req *remotefsv1.FileRequest, opts HandleOptions) *remotefsv1.FileResponse
```

dispatch 在现有 read/stat/list_dir 的 case 之外补：

```go
case *remotefsv1.FileRequest_Write:
    return handleWrite(reqID, op.Write, opts, deps)
case *remotefsv1.FileRequest_Mkdir:
    return handleMkdir(reqID, op.Mkdir, opts, deps)
case *remotefsv1.FileRequest_Delete:
    return handleDelete(reqID, op.Delete, opts, deps)
case *remotefsv1.FileRequest_Rename:
    return handleRename(reqID, op.Rename, opts, deps)
case *remotefsv1.FileRequest_Truncate:
    return handleTruncate(reqID, op.Truncate, opts, deps)
```

### M5 — `pkg/syncer/watch.go`（接受 self-write filter）

```go
type WatchOptions struct {
    // ... 现有字段
    SelfWrites *selfWriteFilter // nil 时不抑制
}
```

`watchChangeFromEvent` 在 `matchExclude` 判断之后、构造 `FileChange` 之前补一行：

```go
if opts.SelfWrites != nil && opts.SelfWrites.ShouldSuppress(rel) {
    return nil, false
}
```

### M6 — `pkg/syncer/daemon.go`（装配）

构造 daemon 时同时构造 locker + selfWrites，作为 daemon 进程内单例传给 `Handle` 和 `Watch`：

```go
locker := newPathLocker()
selfWrites := newSelfWriteFilter(opts.SelfWriteTTL)

handleOpts := HandleOptions{
    Root: opts.Root, MaxReadSize: opts.MaxReadSize,
    Locker: locker, SelfWrites: selfWrites,
}

watchOpts := WatchOptions{
    Root: opts.Root, Excludes: opts.Excludes,
    Events: eventsCh, Logger: logger,
    SelfWrites: selfWrites, // 同一实例
}
```

`RunOptions` 增加 `SelfWriteTTL time.Duration`，从 `config.DaemonConfig.SelfWriteTTL` 透传，默认 `2 * time.Second`。

### M7 — `pkg/config`

```go
// ServerConfig
type ServerConfig struct {
    // ... 现有字段
    RequestTimeout time.Duration `mapstructure:"request_timeout"` // 默认 10s
}

// DaemonConfig
type DaemonConfig struct {
    // ... 现有字段
    SelfWriteTTL time.Duration `mapstructure:"self_write_ttl"`   // 默认 2s
}
```

`defaultServerConfig()` / `defaultDaemonConfig()` 设默认值，加载时校验 `<=0` 回退默认。两个值只在 YAML 暴露，不上 CLI flag。

### M8 — `pkg/session`（暴露 RequestTimeout 与 Apply\*）

```go
type ManagerOptions struct {
    RequestTimeout time.Duration
}

func NewManager(opts ManagerOptions) *Manager
func (m *Manager) RequestTimeout() time.Duration
```

`Session` 新增三个 apply 入口（与 `change` 事件走相同的内部更新路径，相同的 mutex）：

```go
// ApplyWriteResult 把写响应里携带的最新 FileInfo 合并到本地树。
// 用于消除 "写完后立刻 lookup 但 fsnotify 还没到" 的窗口。
func (s *Session) ApplyWriteResult(info *remotefsv1.FileInfo)

// ApplyDelete 在本地树中移除一条 path（含子树自动清理由内部更新逻辑负责）
func (s *Session) ApplyDelete(relPath string)

// ApplyRename 移除 oldRel 并写入 newInfo
func (s *Session) ApplyRename(oldRel string, newInfo *remotefsv1.FileInfo)
```

`Session.Request` 本身不动。

### M9 — `pkg/fusefs/view.go`（写 helper + 错误关键字扩展）

新增六个跨平台 helper：

```go
func writeChunk(ctx context.Context, manager *session.Manager, userID, relPath string,
    offset int64, data []byte) error

func createFile(ctx context.Context, manager *session.Manager, userID, relPath string) error

func mkdirAt(ctx context.Context, manager *session.Manager, userID, relPath string,
    recursive bool) error

func removePath(ctx context.Context, manager *session.Manager, userID, relPath string) error

func renamePath(ctx context.Context, manager *session.Manager, userID, oldRel, newRel string) error

func truncatePath(ctx context.Context, manager *session.Manager, userID, relPath string,
    size int64) error
```

每个 helper 内部：

```text
1. requireSession(manager, userID)
2. ctx, cancel := withRequestTimeout(ctx, manager); defer cancel()
3. resp, err := current.Request(ctx, &FileRequest{...})
4. if err != nil || resp == nil || !resp.GetSuccess():
       return classifyRequestErr(err, resp.GetError())
5. if resp.GetInfo() != nil:
       current.ApplyWriteResult(resp.GetInfo()) (rename 走 ApplyRename，delete 走 ApplyDelete)
6. return nil
```

`withRequestTimeout` 公共封装：

```go
func withRequestTimeout(ctx context.Context, manager *session.Manager) (context.Context, context.CancelFunc) {
    timeout := manager.RequestTimeout()
    if timeout <= 0 {
        return ctx, func() {}
    }
    return context.WithTimeout(ctx, timeout)
}
```

`classifyError`（替代原 `classifyReadError`，读写共用）扩充关键字表：

| 关键字（小写匹配） | typed error | errno |
|---|---|---|
| `no such file`, `cannot find the file`, `cannot find the path` | `ErrPathNotFound` | `ENOENT` |
| `permission denied`, `access is denied` | `ErrPermissionDenied` | `EACCES` |
| `file exists`, `already exists` | `ErrAlreadyExists` | `EEXIST` |
| `not a directory` | `ErrNotDirectory` | `ENOTDIR` |
| `is a directory` | `ErrIsDirectory` | `EISDIR` |
| `directory not empty` | `ErrDirectoryNotEmpty` | `ENOTEMPTY` |
| `cross-device`, `invalid cross-device` | `ErrCrossDevice` | `EXDEV` |
| `invalid argument` | `ErrInvalidArgument` | `EINVAL` |
| 其它任意失败字符串 | `ErrIOFailed` | `EIO` |

匹配自上而下，先命中为准。关键字表必须同时覆盖 Linux 与 Windows 文案（daemon 跨平台编译要求）。

`classifyRequestErr` 单独处理 ctx error：

```go
func classifyRequestErr(reqErr error, respErr string) error {
    if reqErr != nil {
        switch {
        case errors.Is(reqErr, context.DeadlineExceeded):
            return ErrRequestTimeout                          // 新增哨兵
        case errors.Is(reqErr, context.Canceled):
            return context.Canceled                            // 透传，errnoFromError 翻成 EINTR
        default:
            return fmt.Errorf("%w: %v", ErrSessionFailed, reqErr) // 新增哨兵
        }
    }
    if respErr == "" {
        return nil
    }
    return classifyError(respErr)
}
```

新增哨兵全部加到 view.go 的 var 块：

```go
var (
    // ... 现有
    ErrPermissionDenied  = errors.New("fusefs: permission denied")
    ErrAlreadyExists     = errors.New("fusefs: already exists")
    ErrDirectoryNotEmpty = errors.New("fusefs: directory not empty")
    ErrCrossDevice       = errors.New("fusefs: cross device")
    ErrInvalidArgument   = errors.New("fusefs: invalid argument")
    ErrIOFailed          = errors.New("fusefs: io failed")
    ErrRequestTimeout    = errors.New("fusefs: request timeout")
    ErrSessionFailed     = errors.New("fusefs: session failed")
)
```

### M10 — `pkg/fusefs/mount_linux.go`（写 FUSE Node 接口）

`workspaceNode` 增加实现的接口：

```go
var (
    _ = (fs.NodeWriter)((*workspaceNode)(nil))
    _ = (fs.NodeCreater)((*workspaceNode)(nil))
    _ = (fs.NodeMkdirer)((*workspaceNode)(nil))
    _ = (fs.NodeUnlinker)((*workspaceNode)(nil))
    _ = (fs.NodeRmdirer)((*workspaceNode)(nil))
    _ = (fs.NodeRenamer)((*workspaceNode)(nil))
    _ = (fs.NodeSetattrer)((*workspaceNode)(nil))
)
```

每个方法都极薄：参数处理 → view 层 helper → `errnoFromError`。

`Open` 改造：去掉 `EROFS`，接受 `O_RDONLY` / `O_WRONLY` / `O_RDWR`。

`Create`（FUSE 的"父节点上创建子文件并打开"）：

```go
func (n *workspaceNode) Create(ctx, name, flags, mode, out) (
    *fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
    childRel := childPath(n.relPath, name)
    if err := createFile(ctx, n.manager, n.userID, childRel); err != nil {
        return nil, nil, 0, errnoFromError(err)
    }
    info, err := lookupInfo(n.manager, n.userID, childRel) // 借助 ApplyWriteResult 已可见
    if err != nil { return nil, nil, 0, errnoFromError(err) }
    fillEntryOut(out, info)
    child := &workspaceNode{manager: n.manager, userID: n.userID, relPath: childRel}
    inode := n.NewInode(ctx, child, fs.StableAttr{Mode: stableMode(info)})
    return inode, nil, 0, 0
}
```

`Setattr`（只处理 size，其它属性 swallow 并回填当前 attr）：

```go
func (n *workspaceNode) Setattr(ctx, fh, in, out) syscall.Errno {
    if in.Valid&fuse.FATTR_SIZE != 0 {
        if err := truncatePath(ctx, n.manager, n.userID, n.relPath, int64(in.Size)); err != nil {
            return errnoFromError(err)
        }
    }
    return n.Getattr(ctx, fh, out)
}
```

`errnoFromError` 扩充新哨兵：

```go
func errnoFromError(err error) syscall.Errno {
    switch {
    case err == nil:                                        return 0
    case errors.Is(err, context.Canceled):                  return syscall.EINTR
    case errors.Is(err, ErrRequestTimeout),
         errors.Is(err, context.DeadlineExceeded):          return syscall.ETIMEDOUT
    case errors.Is(err, ErrSessionOffline),
         errors.Is(err, ErrSessionFailed):                  return syscall.EIO
    case errors.Is(err, ErrPathNotFound):                   return syscall.ENOENT
    case errors.Is(err, ErrPermissionDenied):               return syscall.EACCES
    case errors.Is(err, ErrAlreadyExists):                  return syscall.EEXIST
    case errors.Is(err, ErrNotDirectory):                   return syscall.ENOTDIR
    case errors.Is(err, ErrIsDirectory):                    return syscall.EISDIR
    case errors.Is(err, ErrDirectoryNotEmpty):              return syscall.ENOTEMPTY
    case errors.Is(err, ErrCrossDevice):                    return syscall.EXDEV
    case errors.Is(err, ErrInvalidArgument):                return syscall.EINVAL
    default:                                                return syscall.EIO
    }
}
```

### M11 — `internal/testutil/inmem_transport.go`

进程内闭环 transport，把 `pkg/fusefs.view` helper、`pkg/session.Manager`、`pkg/syncer.Handle` 串成端到端可测：

```go
type InmemPair struct {
    Manager *session.Manager
    UserID  string
    Cleanup func()
}

func StartInmemPair(t *testing.T, daemonRoot string) *InmemPair
```

实现要点：

- 创建一对 `chan *remotefsv1.ServerMessage`（server→daemon）和 `chan *remotefsv1.DaemonMessage`（daemon→server）
- 启动 goroutine 模拟 daemon 端：从 server→daemon channel 读 ServerMessage，调 `syncer.Handle`，把响应包成 DaemonMessage 推回；同时初始上送一个空的 file_tree 完成首报
- 包一层 `remotefsv1.RemoteFS_ConnectServer` 的 mock，把 manager 的 `Session.Run` 接进来
- 不引入 fsnotify watch；端到端测试只覆盖请求-响应链路 + Apply\*
- `Cleanup` 关闭 channel、等待 goroutine 退出、删除临时目录

### M12 — `app/server/main.go`（最小改动）

```go
manager := session.NewManager(session.ManagerOptions{
    RequestTimeout: cfg.RequestTimeout,
})
```

不在 `fusefs.Mount` 的 `Options` 上暴露 timeout 旋钮 —— fusefs 通过 `manager.RequestTimeout()` 取，避免双真实来源。

## 4. 错误处理、超时与一致性窗口

### 4.1 daemon 端 error → string 格式约定

```go
// 模板：syncer: <op> <path>: <go error 原文>
//
// 例：
//   syncer: write "a/b.txt": open a/b.txt: permission denied
//   syncer: rename "a"->"b": rename a b: invalid cross-device link
//   syncer: delete "x": remove x: directory not empty
//   syncer: mkdir "p": mkdir p: file exists
func formatErr(op, path string, err error) string {
    return fmt.Sprintf("syncer: %s %q: %v", op, path, err)
}

func formatRenameErr(oldPath, newPath string, err error) string {
    return fmt.Sprintf("syncer: rename %q->%q: %v", oldPath, newPath, err)
}
```

约束：

1. 保留 Go error 原文（`%v`），由 view 层关键字归类
2. 不擅自把 errno 翻译成英语词，避免双重翻译
3. daemon 在 Linux/Windows 都要能编译并跑单测，关键字表必须同时覆盖两边的 errno 文案

### 4.2 错误日志策略

- daemon 端：写处理失败时 `slog.Warn("handle write", "op", op, "path", rel, "err", err)`，成功不打日志
- server 端 view helper：`slog.Debug("write helper", ...)`；EIO / Timeout 升 Warn
- mount_linux.go 节点接口层：不打日志（避免每次 4KB Write 都打一行）

### 4.3 一致性窗口：修法 A

问题：

```text
T0  fusefs.Create -> createFile RPC -> daemon 写 0 字节文件 -> 成功
T1  fusefs.Create 收到 success
T2  fusefs.Create 内 lookupInfo("path") -> session.Lookup -> 文件树没有 -> ENOENT
T3  fsnotify Create 事件到达 daemon -> 上送 FileChange -> session 树更新（晚了）
```

修法 A：daemon 写响应回填 FileInfo + `Session.Apply*`。

落地清单：

1. **proto/handle 层**：write/create/mkdir/truncate 的成功响应在 `Success: true` 的基础上把 `Result` 设成 `&FileResponse_Info{Info: lstatToFileInfo(abs, rel)}`；rename 成功也回新路径的 info；delete 成功不回 info
2. **session 层**：`Session.ApplyWriteResult / ApplyDelete / ApplyRename` 三个方法走与 `change` 事件相同的内部更新路径，相同 mutex
3. **view 层**：每个写 helper 在收到 success 之后，根据 op 类型调对应 Apply 方法，再返回 nil。Create helper 在 Apply 之后再 lookup，一定能拿到
4. **fsnotify echo 抑制保留**：避免 `ApplyWriteResult` 之后又被 fsnotify 的 change 重复 apply（idempotent，但浪费）

## 5. 测试策略

### 5.1 测试矩阵

| 模块 | 类型 | 跨平台 | 关键覆盖点 |
|---|---|---|---|
| `pkg/syncer/locker.go` | 单测 | ✅ | 同 path 串行 / 不同 path 并发 / refCount 归零回收 |
| `pkg/syncer/selfwrite.go` | 单测 | ✅ | TTL 内命中 / TTL 外失效 / 多次 Remember 续期 / 注入虚拟时钟 |
| `pkg/syncer/handle_write.go` | 单测 | ✅ | 5 op × (成功/不存在/权限/已存在/跨设备/目录非空/非法参数) |
| `pkg/syncer/watch.go` | 增量单测 | ✅ | self-write 命中事件丢弃 / 未登记事件正常上送 |
| `pkg/session/manager.go` | 单测 | ✅ | RequestTimeout getter |
| `pkg/session/session.go` | 增量单测 | ✅ | ApplyWriteResult / ApplyDelete / ApplyRename 后 Lookup 正确；并发安全 |
| `pkg/fusefs/view.go` | 单测 | ✅ | 6 helper × 错误关键字分类（Linux/Windows 文案各一组） |
| `pkg/fusefs/mount_linux.go` | 单测 | linux | Create/Write/Setattr(size)/Rename/Unlink/Rmdir/Mkdir 节点接口逻辑 |
| `pkg/fusefs/mount_unsupported.go` | 单测 | !linux | 写接口（如有 stub）返回 unsupported |
| `internal/testutil/inmem_transport.go` | helper | ✅ | — |
| 端到端 in-mem | 集成单测 | ✅ | view → session → in-mem transport → handle → 临时目录真实文件 |

### 5.2 关键测试要点

**locker_test.go**

- `TestPathLocker_SerializesSamePath`：两个 goroutine 拿同一 path 锁，交错断言
- `TestPathLocker_ConcurrentDifferentPaths`：不同 path 可同时持有
- `TestPathLocker_RefCountReclaim`：unlock 后 entry 被回收（暴露测试用 `len()`）
- 不引入真实 sleep；用 channel 同步

**selfwrite_test.go**

- `TestSelfWriteFilter_RememberWithinTTL`、`_ExpiredAfterTTL`、`_MultiplePaths`、`_LazyGC`
- 用 `nowFn` 注入虚拟时钟，全部不依赖墙钟

**handle_write_test.go**

table-driven，对 5 op 各覆盖：成功 / 路径越界（unsafe path）/ 不存在 / 已存在 / 目录非空 / 权限失败（Linux build tag）/ 非法参数。

self-write filter 的交互单独测：构造真实 filter 传入 `HandleOptions`，调 handleWrite 后断言 `filter.ShouldSuppress(rel) == true`。

跨 path 加锁的死锁回归：rename `a→b` 与 `b→a` 并发，`time.AfterFunc` 兜底超时强制 fail。

**watch_test.go**（增量）

`TestWatch_SuppressesSelfWrite`：

1. 启动 Watch 传入真实 filter
2. 创建 `x.txt` 之前先 `filter.Remember("x.txt")`
3. 等待最多 1s 看 events channel：必须无事件
4. 再写未登记的 `y.txt`：必须能收到事件

**session_test.go**（增量）

- `TestSession_ApplyWriteResult_VisibleImmediately`
- `TestSession_ApplyDelete`
- `TestSession_ApplyRename`
- `TestSession_ApplyWriteResult_ConcurrentWithChange`（`go test -race`）

`TestManager_RequestTimeout_Default` / `_Custom`。

**view_test.go**（增量）

`classifyError` 每个 errno 给 Linux / Windows 两组文案：

```go
{name: "linux not found",   in: `... open x: no such file or directory`,            want: ErrPathNotFound},
{name: "windows not found", in: `... open x: The system cannot find the file specified.`, want: ErrPathNotFound},
{name: "linux denied",      in: `... open x: permission denied`,                    want: ErrPermissionDenied},
{name: "windows denied",    in: `... open x: Access is denied.`,                    want: ErrPermissionDenied},
// ...
```

6 个写 helper 用 fake / stub `session.Manager` 测：注入预设 `FileResponse`、断言返回 typed error 与 Apply\* 调用。

**mount_linux_test.go**（增量，build tag `//go:build linux`）

用 fake `session.Session` 不真正挂载内核 FUSE，只测节点接口的 Go 侧逻辑：

- `TestWorkspaceNode_Create_Success`
- `TestWorkspaceNode_Create_AlreadyExists` → `EEXIST`
- `TestWorkspaceNode_Write_OffsetForwarded`
- `TestWorkspaceNode_Setattr_OnlySize`（携带 size + mode，只下发 Truncate）
- `TestWorkspaceNode_Open_AcceptsWriteFlags`（O_WRONLY/O_RDWR 都返回 0）
- `TestWorkspaceNode_Rename_FullPath`
- `TestWorkspaceNode_Unlink_VsRmdir`

非 Linux 不需要新增节点接口测试。

**inmem_e2e_test.go**（普通 `_test.go`，跨平台都跑）

最小用例集：

1. `TestInmem_CreateAndWrite`
2. `TestInmem_Mkdir_Unlink_Rmdir`
3. `TestInmem_Truncate`
4. `TestInmem_Rename_AcrossDir`
5. `TestInmem_WriteFailure_NotFound`
6. `TestInmem_ApplyWriteResult_Visible`
7. `TestInmem_PathLocker_NoDeadlockOnRename`

端到端测试不引入 fsnotify。

### 5.3 验收命令

```bash
gofumpt -w .
gci write --section standard --section default --section "prefix(flyingEirc/Rclaude)" .
golangci-lint run ./...
go mod tidy
go test -race -count=1 -timeout 180s ./...
go build ./...
GOOS=linux GOARCH=amd64 go build ./app/server
```

特别注意：

- `-race` 必须打开（Phase 4a 引入 path locker / self-write filter / Apply\*，并发面增加）
- `go test ./...` 在 Windows 与 Linux 都要跑通

### 5.4 不在 Phase 4a 跑的验收

- Linux 真实 FUSE 内核挂载下用 `bash -c 'echo hi > /mountpoint/u1/x'` 之类命令的真机验收：沿用 Phase 3 模式，由用户在自己的 Linux 环境手动验收，结果写入完成摘要 `phase4a-write-ops.md`
- 端到端 + Linux runner CI：Phase 5

## 6. 风险与应对

| 风险 | 说明 | 应对 |
|---|---|---|
| daemon 跨平台错误文案差异 | Linux/Windows 的 errno 文案不同 | view 层关键字表同时覆盖两边；测试也按平台各一组用例 |
| Self-write 窗口太短 | fsnotify 偶发延迟可能 >2s | TTL 暴露成 `daemon.SelfWriteTTL` 配置项；必要时调到 3s/5s |
| Self-write 窗口太长 | 真实外部修改在窗口内被错误屏蔽 | 同一 path 第二次外部修改会被 fsnotify 重复发送，TTL 过期后正常恢复；并且 echo 只影响通知，不影响 view 端的写后 Apply |
| Rename 死锁 | 同时持有 old/new 两把锁的并发场景 | 按 path 字典序加锁；单测覆盖跨 rename 并发回归 |
| 一致性窗口仍然存在于跨进程 | 别的 session 看到的更新依然依赖 fsnotify | 4a 暂不解决；记录到 Phase 4b 缓存设计的输入 |
| FUSE Setattr 其它属性被静默吞 | mode/atime/uid 不下发，但返回 0 | 在 4a 文档中明确写出；Phase 6 视需要补 |
| Windows 没法做 FUSE 集成验收 | 开发机限制 | mount_linux_test 用 fake session，端到端用 in-mem，Linux 真机由用户手动验收 |

## 7. 阶段交付物

- `api/proto/remotefs/v1/remotefs.proto` 增量 + 重新生成的 `.pb.go` / `.pb_grpc.go`
- `pkg/syncer/locker.go` + `locker_test.go`
- `pkg/syncer/selfwrite.go` + `selfwrite_test.go`
- `pkg/syncer/handle_write.go` + `handle_write_test.go`
- `pkg/syncer/handle.go`（dispatch 改造）
- `pkg/syncer/watch.go`（增量）+ `watch_test.go` 增量
- `pkg/syncer/daemon.go`（装配）
- `pkg/config/server.go` + `pkg/config/daemon.go`（增量）
- `pkg/session/manager.go` + `pkg/session/session.go`（增量 Apply\*）+ 单测增量
- `pkg/fusefs/view.go`（增量 helper、错误哨兵、关键字表、ctx 复合）+ 单测增量
- `pkg/fusefs/mount_linux.go`（增量 Node 接口、Open 改造、Create/Setattr）+ 单测增量
- `pkg/fusefs/inmem_e2e_test.go`
- `internal/testutil/inmem_transport.go`
- `app/server/main.go`（NewManager 调用调整）
- `docs/exec-plan/active/{时间}-phase4a-write-ops/{plan.md, 开发流程.md, 测试错误.md}`（由 writing-plans skill 生成）

## 8. 后续阶段铺垫

Phase 4a 完成后，Phase 4b（缓存）的设计输入：

- `Session.ApplyWriteResult / Apply*` 已经是写后立即可见的权威更新源；4b 的内容缓存失效逻辑可以挂在同一组方法上
- self-write filter 抑制了 fsnotify 回声，避免缓存被自家 echo 反复 invalidate
- `pathLocker` 可以扩展成「写时持锁刷新缓存」的同步原语
- in-mem transport 已经能跑端到端，4b 的缓存命中/失效用例可以直接复用

Phase 5（集成测试）的准备：

- `internal/testutil.StartInmemPair` 可以扩充成更完整的测试夹具
- Linux 真实挂载冒烟可以基于 4a 已有的 Node 接口实现直接跑
