# Phase 2 — Daemon MVP

> 阶段目录：`docs/exec-plan/active/202604061800-phase2-daemon-mvp/`
> 上游 ROADMAP：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 2
> 关联设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)、[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)
> 上一阶段：[`docs/exec-plan/completed/202604061730-phase1-proto-and-base-pkgs/`](/e:/Rclaude/docs/exec-plan/completed/202604061730-phase1-proto-and-base-pkgs/)

## Context

Phase 1 已交付完整的 `remotefs.v1` proto 与五个基础包：`pkg/safepath`、`pkg/logx`、`pkg/config`、`pkg/auth`、`pkg/fstree`。本阶段是 ROADMAP Phase 2：

- **当前状态**：
  - `pkg/transport`、`pkg/syncer`、`pkg/ratelimit`、`pkg/session`、`internal/testutil` 都只有 `doc.go` 占位
  - `app/client`、`app/server` 也只有 `doc.go` 占位
  - 没有任何 Daemon 运行入口和可执行二进制
- **触发的 ROADMAP 子项**（`docs/design/ROADMAP.md` Phase 2）：
  1. 启动、配置加载与 gRPC 建连
  2. 工作区扫描与初始文件树上报
  3. 按请求读文件、列目录、返回属性
  4. `fsnotify` 变更采集与增量推送
- **目标**：完成本阶段后，Daemon 能连接一个 mock Server（bufconn 夹具），完成首次文件树上报、响应 Read/Stat/ListDir 请求、推送 fsnotify 增量变更、保持心跳；断线后指数退避重连。
- **本阶段不包含**（明确边界）：
  - 任何写操作（Write/Create/Delete/Mkdir/Rename）→ Phase 4
  - Server 端 FUSE 挂载、会话路由、内容缓存、预取 → Phase 3~4
  - `pkg/session`、`pkg/syncer` 内部的缓存层 → Phase 3~4
  - `pkg/ratelimit` 实际接入 → Phase 6
  - 真实 Server 进程、端到端集成测试 → Phase 5

## 模块拆分（Modules）

### M1 — pkg/syncer/scan.go（工作区扫描器）
**职责**：递归 walk 工作区根目录，应用 gitignore 风格 exclude 规则，产出相对路径 `*remotefsv1.FileInfo` 列表。
**公开 API**：
```go
// ScanOptions 控制扫描范围。
type ScanOptions struct {
    Root     string   // 绝对路径；必须存在且是目录
    Excludes []string // doublestar 模式，相对 Root；空表示不排除
}

// Scan 递归扫描 opts.Root，返回相对路径的 FileInfo 列表（不含根自身）。
// 返回值中路径统一用 forward slash；不保证顺序。
func Scan(opts ScanOptions) ([]*remotefsv1.FileInfo, error)
```
**边界**：
- 不监听变更（那是 M2 的事）
- 不解析 symlink（`filepath.WalkDir` 默认行为）
- 软失败策略：单个文件 `os.Lstat` 失败时记录并跳过，不终止整个扫描
- 使用 `pkg/safepath.ToSlash` 保证路径分隔符
- 使用 `github.com/bmatcuk/doublestar/v4` 做 exclude 匹配

### M2 — pkg/syncer/watch.go（fsnotify 监听）
**职责**：基于 `fsnotify.Watcher` 递归监听工作区，把 OS 事件翻译成 `*remotefsv1.FileChange`，通过 channel 对外暴露。
**公开 API**：
```go
// WatchOptions 配置 Watcher 行为。
type WatchOptions struct {
    Root     string
    Excludes []string
    Events   chan<- *remotefsv1.FileChange // caller 提供，满了会阻塞
    Logger   *slog.Logger                  // nil 则用 slog.Default()
}

// Watch 启动监听，阻塞直到 ctx.Done() 或内部 fatal error 返回。
// 返回时保证底层 fsnotify.Watcher 已关闭。
func Watch(ctx context.Context, opts WatchOptions) error
```
**边界**：
- fsnotify 不递归，Watch 自己在启动时扫一遍目录树并逐个 `watcher.Add`
- 新建目录时动态 `Add`；删除目录时依赖 fsnotify 自动回收
- CREATE/WRITE→`CHANGE_TYPE_CREATE|MODIFY`，REMOVE→`DELETE`，RENAME→作为 `DELETE` 对待（新名字由 CREATE 上报）
- 被 exclude 命中的路径跳过，不上报
- 事件合并、去抖不在本阶段范围，留给 Phase 6

### M3 — pkg/syncer/handle.go（文件请求处理）
**职责**：把 `*remotefsv1.FileRequest` 落到本地文件系统读操作，组装 `*remotefsv1.FileResponse`。
**公开 API**：
```go
// HandleOptions 把配置传给 Handle 避免隐式全局。
type HandleOptions struct {
    Root        string // workspace 绝对路径
    MaxReadSize int64  // 0 表示不限制（Phase 6 接入）
}

// Handle 同步执行请求并返回响应。永不返回 error；内部错误落在 FileResponse.error。
func Handle(req *remotefsv1.FileRequest, opts HandleOptions) *remotefsv1.FileResponse
```
**支持的 operation（Phase 2 子集）**：
- `ReadFileReq`：`safepath.Join(Root, path)` → `os.ReadFile` + offset/length 截取
- `StatReq`：`safepath.Join` → `os.Lstat` → `FileInfo`
- `ListDirReq`：`os.ReadDir` → `[]FileInfo`
- 其余（Write/Delete/Mkdir/Rename）：返回 `FileResponse{success:false, error:"operation not supported in phase 2"}`

### M4 — pkg/transport/client.go（gRPC 客户端）
**职责**：封装 Daemon 端的 `grpc.NewClient` + Connect stream。
**公开 API**：
```go
// DialOptions 是 Dial 必需参数。
type DialOptions struct {
    Address string       // Server 地址，例如 "1.2.3.4:9000"
    Token   string       // 鉴权 token
    TLS     bool         // Phase 2 固定 false；后续 Phase 7 再接 TLS
    DialTO  time.Duration // 连接超时，默认 10s
}

// Dial 建立到 Server 的 grpc 连接（未开 stream）。调用方负责 Close。
func Dial(ctx context.Context, opts DialOptions) (*grpc.ClientConn, error)

// OpenStream 在已有 conn 上开 Connect 双向流，并把 token 注入 outgoing metadata。
func OpenStream(ctx context.Context, conn *grpc.ClientConn, token string) (remotefsv1.RemoteFS_ConnectClient, error)
```
**边界**：
- Phase 2 固定使用 `insecure.NewCredentials()`，TLS 为 false 走明文
- 不做重试、不做心跳（那是 daemon.go 的编排层职责）
- 失败包装成 wrapped error（`fmt.Errorf("transport: dial %q: %w", ...)`）

### M5 — internal/testutil（bufconn 夹具 + 临时工作区）
**职责**：给 syncer 的集成测试提供可控 Server 与 workspace。
**公开 API**：
```go
// NewBufconnServer 启动一个 bufconn-based grpc Server，注册用户自定义的 RemoteFSServer 实现。
// 返回 Dialer 供 Daemon 侧 grpc.NewClient 使用，以及 Stop 函数。
func NewBufconnServer(tb testing.TB, srv remotefsv1.RemoteFSServer) (dialer func(context.Context, string) (net.Conn, error), stop func())

// NewTempWorkspace 在 tb.TempDir() 下按 map 建立文件结构并返回根目录。
// map 的 key 是 forward-slash 相对路径，value 是文件内容；key 以 "/" 结尾表示目录。
func NewTempWorkspace(tb testing.TB, layout map[string]string) string

// RecordingServer 是线程安全的 RemoteFSServer mock，记录所有收到的 DaemonMessage
// 并允许测试用 SendRequest 主动下发 ServerMessage。
type RecordingServer struct { /* 不含外部字段 */ }
func NewRecordingServer() *RecordingServer
func (s *RecordingServer) Received() []*remotefsv1.DaemonMessage
func (s *RecordingServer) SendRequest(req *remotefsv1.ServerMessage) error
```
**边界**：
- 只在 `*_test.go` 里被引用；但 testutil 自身是 `package testutil`，不能加 `_test` 后缀
- bufconn 使用 `google.golang.org/grpc/test/bufconn`
- 不做 auth 验证，Phase 2 测试直接不插拦截器

### M6 — pkg/syncer/daemon.go（Daemon 编排）
**职责**：把 M1/M2/M3/M4/M5 穿起来的主 Run 函数。
**公开 API**：
```go
// RunOptions 汇总 Daemon 启动所需的全部依赖。
type RunOptions struct {
    Config *config.DaemonConfig
    Logger *slog.Logger
    // Dialer 可选；非 nil 时用于 bufconn 测试注入，生产环境留 nil。
    Dialer func(context.Context, string) (net.Conn, error)
}

// Run 阻塞执行 Daemon 主循环；收到 ctx.Done() 时干净退出。
// 内部会按 backoff 重连；返回 error 表示不可恢复的故障（例如配置错误）。
func Run(ctx context.Context, opts RunOptions) error
```
**关键行为**：
1. 初始扫描：`Scan(Root, Excludes)` → 发送 `DaemonMessage.file_tree`
2. 主循环的三个 goroutine：
   - **recv loop**：`stream.Recv()` → 分派 `ServerMessage.request` → `Handle` → `stream.Send` response
   - **watch loop**：读 watcher 产出的 `FileChange` → `stream.Send` change
   - **heartbeat loop**：`time.Ticker(15s)` → 发 `Heartbeat`
3. 用 `errgroup.WithContext` 协同三条 goroutine，任一返回则取消另外两条
4. 断线时按指数退避（`backoff/v4`，1s→30s）重新 Dial，然后回到第 1 步
5. `ctx.Done()` 时立刻返回 nil，不再重连

### M7 — app/client 入口（cobra）
**职责**：daemon 二进制入口。
**文件**：`app/client/main.go`（`package main`），替换 `doc.go` 占位。
**命令**：`rclaude-daemon --config /path/to/daemon.yaml`
**流程**：`cobra.Command` → 解析 `--config` → `config.LoadDaemon` → `logx.New` → `signal.NotifyContext(os.Interrupt)` → `syncer.Run`

## Todo 列表

| ID | 目标 | 涉及模块 | 依赖 | 状态 |
|---|---|---|---|---|
| T0 | 建立阶段三件套目录 | — | — | [x] done |
| T1 | 新增 cobra/backoff/v4/doublestar/v4/fsnotify direct 依赖，`go mod tidy` | — | T0 | [x] done |
| T2 | 实现 `pkg/syncer/scan.go` + 单测 | M1 | T1 | [x] done |
| T3 | 实现 `pkg/syncer/watch.go` + 单测 | M2 | T1 | [x] done |
| T4 | 实现 `pkg/syncer/handle.go` + 单测 | M3 | T1 | [x] done |
| T5 | 实现 `pkg/transport/client.go` + 单测 | M4 | T1 | [x] done |
| T6 | 实现 `internal/testutil/{bufconn.go, workspace.go}` | M5 | T1 | [x] done |
| T7 | 实现 `pkg/syncer/daemon.go` + 集成测试 | M6 | T2~T6 | [x] done |
| T8 | 实现 `app/client/main.go`（cobra 装配） | M7 | T7 | [x] done |
| T9 | 阶段总验收 | — | T0~T8 | [x] done |

依赖图：
```
T0 ─► T1 ─┬─► T2 ──┐
          ├─► T3 ──┤
          ├─► T4 ──┼─► T7 ─► T8 ─► T9
          ├─► T5 ──┤
          └─► T6 ──┘
```

## 文件清单

本阶段新增或重写：

| 文件 | 行为 | 说明 |
|---|---|---|
| `pkg/syncer/scan.go` | 新增 | 工作区扫描 |
| `pkg/syncer/scan_test.go` | 新增 | 扫描单测 |
| `pkg/syncer/watch.go` | 新增 | fsnotify watcher |
| `pkg/syncer/watch_test.go` | 新增 | watcher 单测 |
| `pkg/syncer/handle.go` | 新增 | 请求处理 |
| `pkg/syncer/handle_test.go` | 新增 | handle 单测 |
| `pkg/syncer/daemon.go` | 新增 | 编排层 |
| `pkg/syncer/daemon_test.go` | 新增 | 集成测试 |
| `pkg/syncer/doc.go` | 保持 | Phase 1 的占位已经写好 package 注释 |
| `pkg/transport/client.go` | 新增 | gRPC 客户端 |
| `pkg/transport/client_test.go` | 新增 | 客户端单测 |
| `pkg/transport/doc.go` | 保持 | 占位不改 |
| `internal/testutil/bufconn.go` | 新增 | bufconn Server 夹具 |
| `internal/testutil/workspace.go` | 新增 | 临时工作区 builder |
| `internal/testutil/recording_server.go` | 新增 | 记录 + 可控下发的 mock |
| `internal/testutil/doc.go` | 保持 | 占位不改 |
| `app/client/main.go` | 新增 | daemon 入口 |
| `app/client/doc.go` | 删除 | 被 main.go 替代 |
| `go.mod` / `go.sum` | 修改 | 新依赖 |

## 验证（端到端）

每个 Todo 完成后跑包级单测：
```bash
go test -count=1 -timeout 60s ./pkg/syncer/...       # Windows 无 race；Linux 上 Makefile 会加 -race
go test -count=1 -timeout 60s ./pkg/transport/...
```

阶段终点（T9）：
```bash
mingw32-make fmt           # 不应产生 diff
mingw32-make lint          # exit 0
mingw32-make test          # exit 0
mingw32-make build         # exit 0（会编出 daemon 二进制）
mingw32-make proto         # exit 0（幂等）
go mod tidy                # 不应产生 diff
```

手动冒烟（可选，如果 Linux 环境可用）：
```bash
./app/client/client --config testdata/daemon.yaml
# 另起一个 bufconn-based 测试能跑就算通过，不强求真实 Server
```

## 风险与应对

| 风险 | 说明 | 应对 |
|---|---|---|
| fsnotify 在 Windows 上对子目录递归支持有限 | Phase 2 需要监听整棵工作区 | 启动时自己 walk 加 watch，并在 CREATE 目录时动态 Add |
| 大工作区扫描慢 | 初始扫描阻塞上报 | Phase 2 不引入并发扫描，单目标 ≤数万文件即可；超出留给 Phase 6 |
| backoff 反复重连卡 CI | 集成测试 timeout | 测试场景把 `backoff.MaxElapsedTime` 压到 2s，生产保留默认 |
| bufconn 与 grpc.NewClient 版本不匹配 | `grpc.NewClient` 需要 `WithContextDialer` | testutil 返回的 dialer 直接传 `grpc.WithContextDialer(dialer)` |
| gocognit / cyclop 超阈 | Run 编排逻辑复杂 | 把 recv/watch/heartbeat 拆成三个 package-private 函数，Run 只做装配 |
| Windows 路径分隔符 | walk 返回 backslash | 所有对外字符串先过 `safepath.ToSlash` |
| exclude 模式语义 | doublestar v4 与 gitignore 有细微差异 | 接受 doublestar glob；不实现 gitignore 的否定规则，测试里写明 |
| `app/client/doc.go` 存在时再加 `package main` 冲突 | 同目录 package 不一致 | T8 先 `git rm` doc.go 再新建 main.go |
| signal.NotifyContext 在 Windows 行为差异 | Ctrl+C 在 bash shell 下可触发 | Phase 2 只用 os.Interrupt，不加 SIGTERM Windows 分支 |

## 关键参考文件

- `docs/design/PLAN.md §3.1` — Daemon 职责 / 配置格式
- `docs/design/PLAN.md §3.2` — Proto 字段语义
- `docs/ARCHITECTURE.md §三 / §五` — 依赖选型与实现原则
- `pkg/config/config.go` — `DaemonConfig` 字段
- `pkg/auth/auth.go` — `NewOutgoingContext` 注入 token 的方式
- `pkg/fstree/fstree.go` — `normalize` / `cloneInfo` 的路径约定，扫描器应一致
- `api/proto/remotefs/v1/remotefs.proto` — 消息定义
- `api/proto/remotefs/v1/remotefs_grpc.pb.go` — `RemoteFS_ConnectClient` / `RemoteFS_ConnectServer` 类型
