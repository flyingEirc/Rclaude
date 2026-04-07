# Phase 3 — Server + FUSE MVP

> 阶段目录：`docs/exec-plan/active/202604071046-phase3-server-fuse-mvp/`
> 上游 ROADMAP：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 3
> 关联设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)、[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)
> 上一阶段：[`docs/exec-plan/completed/202604061800-phase2-daemon-mvp/`](/e:/Rclaude/docs/exec-plan/completed/202604061800-phase2-daemon-mvp/)

## Context

Phase 2 已交付 Daemon MVP：本地工作区扫描、文件树首报、Read/Stat/ListDir 请求处理、增量变更上报、心跳和重连均已具备。当前仍缺少 ROADMAP Phase 3 的 Server 侧主链路：

- `app/server` 仍是占位文件，没有真实启动入口
- `pkg/session` 仍为空壳，没有 user_id 到 stream 的会话路由
- 仓库尚未接入 `github.com/hanwen/go-fuse/v2`
- Server 还不能把 Daemon 暴露成 `/workspace/{user_id}/...` 的虚拟工作区

本阶段目标是补齐只读 MVP：Server 接收 Daemon 连接、维护每个用户的文件树快照、通过 FUSE 向执行环境提供 `Lookup / Getattr / Readdir / Open / Read` 能力。

## 目标与范围

### 本阶段目标

1. Server 能通过 token 识别 user_id，并维持 `user_id -> session` 路由
2. Server 收到 `file_tree` / `change` / `response` / `heartbeat` 后能更新会话状态
3. FUSE 挂载点下暴露动态根目录 `/workspace/{user_id}`，支持只读访问
4. `cat` / `ls` / `stat` 对 `/workspace/{user_id}/...` 的访问能经由会话路由到对应 Daemon
5. 在非 Linux 环境保持仓库可编译，FUSE 功能显式返回 unsupported

### 明确不包含

- `Write` / `Create` / `Mkdir` / `Rename` / `Unlink`：留给 Phase 4
- 内容缓存 / 预取 / 只读降级：留给 Phase 4~6
- 断线恢复后的缓存复用策略：留给后续阶段
- Docker / systemd / 生产部署说明：留给 Phase 7

## 模块拆分

### M1 — `pkg/session`（会话管理与请求路由）

职责：

- 管理在线 Daemon 会话：注册、替换、注销、按 user_id 查找
- 在单个 session 内维护：
  - 最新文件树快照（`pkg/fstree.Tree`）
  - 到 Daemon 的发送队列
  - 按 `request_id` 关联的 pending 请求
  - 最近心跳时间
- 提供同步请求入口：Server/FUSE 发起 `Read` / `Stat` / `ListDir`，等待对应 `FileResponse`

API 草案：

```go
type Manager struct { ... }

func NewManager() *Manager
func (m *Manager) Register(userID string) (*Session, error)
func (m *Manager) Get(userID string) (*Session, bool)
func (m *Manager) Remove(userID string, s *Session)
func (m *Manager) UserIDs() []string

type Session struct { ... }

func (s *Session) Run(ctx context.Context, stream remotefsv1.RemoteFS_ConnectServer) error
func (s *Session) Request(ctx context.Context, req *remotefsv1.FileRequest) (*remotefsv1.FileResponse, error)
func (s *Session) Lookup(path string) (*remotefsv1.FileInfo, bool)
func (s *Session) List(path string) ([]*remotefsv1.FileInfo, bool)
func (s *Session) LastHeartbeat() time.Time
```

### M2 — `pkg/session/service.go`（gRPC Connect 服务）

职责：

- 实现 `remotefsv1.RemoteFSServer`
- 从 auth interceptor 注入的 ctx 中提取 user_id
- 注册 session，接管双向 stream 生命周期
- 首条消息必须是 `file_tree`，否则返回错误
- 后续处理 `change` / `response` / `heartbeat`

约束：

- 本阶段只实现 stream 服务，不实现额外 unary RPC
- 重复登录采用“新连接顶掉旧连接”，避免一个 user_id 挂多条会话

### M3 — `pkg/fusefs`（只读 FUSE 文件系统）

职责：

- 在挂载点提供动态根目录，列出当前在线 user_id
- `Lookup/Getattr/Readdir` 优先依赖 session 内文件树
- `Open/Read` 通过 `Session.Request()` 下发 `ReadFileReq`
- 根目录和用户目录都是动态节点；文件/目录节点基于相对路径构造

实现边界：

- 真实挂载仅在 Linux 启用
- 非 Linux 提供同名 API stub，返回 `ErrUnsupportedPlatform`
- 只做只读路径，不实现写相关节点接口

### M4 — `app/server/main.go`（Server 启动入口）

职责：

- 解析 `--config`
- 加载 `config.LoadServer`
- 构建 logger、auth verifier、session manager、gRPC server、FUSE mount
- 绑定退出信号并清理挂载

### M5 — 测试夹具与阶段验证

职责：

- 为 `pkg/session` 增加 stream mock / 路由测试
- 为 `pkg/fusefs` 增加以 manager/session fake 为基础的单测
- Linux 环境下增加最小挂载冒烟；非 Linux 只验证 unsupported 分支

## Todo 列表

| ID | 目标 | 涉及模块 | 依赖 | 状态 |
|---|---|---|---|---|
| T0 | 建立 Phase 3 active 三件套 | — | — | [x] done |
| T1 | 引入 `github.com/hanwen/go-fuse/v2` 依赖并核对 Linux/非 Linux 编译边界 | M3 | T0 | [x] done |
| T2 | 实现 `pkg/session` Manager / Session / request-response 路由及测试 | M1 | T0 | [x] done |
| T3 | 实现 `pkg/session` 的 gRPC stream 服务及测试 | M2 | T2 | [x] done |
| T4 | 实现 `pkg/fusefs` 只读 FUSE 文件系统和跨平台 stub | M3 | T1,T2 | [x] done |
| T5 | 实现 `app/server/main.go` 装配入口 | M4 | T2,T3,T4 | [x] done |
| T6 | 补充阶段级验证与必要测试夹具 | M5 | T2~T5 | [x] done |
| T7 | 执行 fmt/lint/test/build，回写阶段记录 | — | T1~T6 | [x] done |

依赖图：

```text
T0 ─► T1 ─┐
T0 ─► T2 ─┼─► T4 ─┐
T2 ─► T3 ─┘       ├─► T5 ─► T6 ─► T7
```

## 验收标准

代码级：

- `go build ./...` 通过
- `go test -count=1 -timeout 120s ./...` 通过
- `pkg/session` 覆盖：
  - file_tree 首报建树
  - change 增量更新
  - request/response 匹配
  - 旧连接被新连接替换
- `pkg/fusefs` 覆盖：
  - 在线用户目录可枚举
  - 路径 lookup 能正确区分文件/目录/不存在
  - Read 走 session 请求链路
  - 非 Linux 返回明确 unsupported

运行级：

- Linux 环境下可在配置的 mountpoint 成功挂载
- `ls {mountpoint}` 能列出在线用户目录
- `cat {mountpoint}/{user_id}/...` 能读到 Daemon 工作区真实内容

## 风险与应对

| 风险 | 说明 | 应对 |
|---|---|---|
| `go-fuse` 主要面向 Linux | 当前开发机是 Windows | 使用 `linux`/`!linux` 分文件实现，保证跨平台编译 |
| FUSE 接口较底层 | 容易把节点生命周期和路径映射写乱 | 优先依赖 session 树快照，不做多余缓存层 |
| stream 并发收发复杂 | 请求响应和变更事件共用一个 bidi stream | 用 send queue + pending map，把责任收敛到 `pkg/session` |
| 断线与替换连接 | 同 user_id 重连时旧 session 可能泄漏 | 明确“新连接顶掉旧连接”，旧 session 主动关闭 pending |
| Windows 无法真实挂载 | 当前机上无法完整跑 FUSE 冒烟 | 单测覆盖逻辑；Linux 冒烟单测加 build tag 或 runtime skip |
