# Remote Claudecode PTY 设计

> 状态：草案确认完毕，待转入 writing-plans 出实施计划。
> 上游设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)
> 系统 ROADMAP：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md)
> 实现架构：[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)
> 草案输入：[`remote_claudecode_pty.md`](/e:/Rclaude/remote_claudecode_pty.md)

## 1. 背景与目标

现有系统已经把"用户本地文件"通过 daemon ↔ Server gRPC 双向流 + Server 端 FUSE 暴露成 `/workspace/{user_id}/`，让任何在 Server 侧通过 bash/PowerShell 访问文件的执行环境都能透明读写用户的本地文件。

但目前缺一条**用户与 Server 端交互式 AI 的通道**：用户没有办法直接打开一个远端的 claude code 会话，让 claude 工作在自己的 `/workspace/{user_id}/` 上。这条通道补齐之前，`PLAN.md` 的"远端文件访问 + 远端执行环境"是一个只有半边的产品。

本设计为这条通道（远端 PTY）给出可落地方案。

### 目标

- 本地一条命令 `rclaude-claude` 即可拉起一个远端 claude code 交互会话；
- 远端 claude 的 cwd 落在 `/workspace/{user_id}/`，自动看见用户本地文件；
- 用户终端的 stdin / stdout / 窗口大小变化与远端 PTY 完全双向透传；
- 不引入新的认证体系、不引入新的传输协议栈、不破坏现有 daemon 与 Server 的责任边界。

### 非目标（MVP 显式 YAGNI）

- 多并发 PTY、tmux 风格的持久会话/分离/重连/输出回放；
- 录屏与操作回放审计；
- CLI 端指定 cwd 子目录、追加 env、指定 binary；
- 单独的 control-frame 信号通道（Ctrl+C 直接走 PTY 字节）；
- setuid / 容器化 / 沙箱化 claude 子进程；
- 浏览器里跑 claude TUI 的 Web 入口。

## 2. 决策摘要

| 议题 | 选择 | 理由 |
|---|---|---|
| 本地入口形态 | 独立二进制 `rclaude-claude`（`app/clientpty/`），复用 `rclaude-daemon` 配置中的 `server.address` / `auth.token` / `tls.*` | daemon 是常驻同步进程，不应承载短生命周期交互流；CLI 与 daemon 共享身份与目标地址即可，不需要独立 token；新二进制比侵入现有 cobra 结构更干净 |
| 传输协议 | 新增 gRPC service `RemotePTY`，与现有 `RemoteFS` 并列 | 复用 TLS / auth interceptor / proto 工具链；gRPC bidi stream 自带流控；与 RemoteFS 装配对称 |
| 会话模型 | 单 PTY、断即终结；同一 `user_id` 同时只允许 1 个活跃 PTY；proto 预留 `session_id` 字段 | MVP 最小可用；为持久会话留协议扩展位 |
| cwd | 强制 `/workspace/{user_id}/`，CLI 不可覆盖 | 系统目的本身；防越权 |
| binary | server config `pty.binary`（默认 `claude`），CLI 不可指定 | 防止变成任意命令执行通道 |
| OS 用户 | 与 server 进程同用户，FUSE 已按 user_id 隔离 | MVP 不引入 setuid/容器化 |
| 环境变量 | 白名单透传 (`TERM`/`LANG`/`LC_*`/`PATH`)，其余清空 | 避免 server 侧泄漏 |
| daemon 耦合 | attach 必须有同 user_id daemon 在连；attach 后 daemon 断开**不杀** PTY，依赖 Phase 6A 的 TTL 只读降级 | 无 daemon 即无 FUSE 文件，attach 没意义；网络抖动不该毁掉 claude 上下文 |
| 信号 | Ctrl+C / Ctrl+D 走 PTY 行规自然处理（stdin 字节），只为窗口尺寸单独留 `resize` 帧 | MVP 简化；无需 control-frame |
| 认证 | CLI 复用 daemon 的 `auth.token` | 一套身份 |
| TLS | 复用 `pkg/transport` 现有 TLS 配置 | 一套信任根 |
| 限流 | 复用 `pkg/ratelimit`，新增 `pty.attach.qps` 与 `pty.bytes.in.bps` | 复用既有令牌桶基础设施 |
| 单帧上限 | 64 KiB，超限 server 端拒收并下发 `Error{PROTOCOL}` 后关流 | 防恶意大 frame 撑爆内存 |
| 审计 | `attach` / `attached` / `detach` / `exit` / `error` / `daemon_offline` 各打一条 `logx` 结构化日志，不录字节流 | 隐私 + 体积 |

## 3. 架构

### 3.1 总体架构

```
┌─────────────────────── 用户本地机 ───────────────────────┐
│                                                         │
│  ┌────────────┐   gRPC RemoteFS.Connect   ┌───────────┐ │
│  │  daemon    │ ─────────────────────────►│           │ │
│  │ (常驻)     │                            │           │ │
│  └────────────┘                            │           │ │
│                                            │           │ │
│  ┌────────────┐   gRPC RemotePTY.Attach    │           │ │
│  │ daemon     │ ─────────────────────────►│           │ │
│  │  claude    │                            │           │ │
│  │ (短生命)   │ ◄─────────────────────────│           │ │
│  └────────────┘                            │           │ │
│       │                                    │           │ │
│       ▼ raw mode stdin/stdout              │           │ │
│  ┌────────────┐                            │           │ │
│  │ 用户终端   │                            │           │ │
│  └────────────┘                            │           │ │
└────────────────────────────────────────────│ Server    │─┘
                                             │           │
                          ┌──────────────────┤           │
                          ▼                  │           │
              FUSE /workspace/{user_id}/     │           │
                          ▲                  │           │
                          │                  │           │
                  ┌───────┴───────┐          │           │
                  │ claude (PTY)  │◄─────────┤ pty mgr   │
                  │ cwd=workspace │  spawn   │           │
                  └───────────────┘          └───────────┘
```

`RemoteFS.Connect`（既有，长连接）与 `RemotePTY.Attach`（新增，短连接）并存，互不干扰；两条流都按 `token → user_id` 解析身份，server 内部按 `user_id` 把 PTY 会话与 daemon 会话关联。

### 3.2 模块划分

| 路径 | 职责 | 新建/复用 |
|---|---|---|
| `api/proto/remotefs/v1/pty.proto` | `RemotePTY` service + frame 类型 | 新建 |
| `pkg/ptyhost/` | server 侧 PTY 进程管理：spawn / 读写泵 / resize / kill / exit 收集 | 新建 |
| `pkg/ptyhost/policy.go` | 进程上下文构建：cwd 拼接 + safepath 校验 + env 白名单 + binary 解析 | 新建 |
| `pkg/ptyclient/` | 本地 CLI 侧：终端 raw 模式、SIGWINCH 监听、stdin/stdout 与 gRPC stream 桥接 | 新建 |
| `app/clientpty/main.go` | `rclaude-claude` 二进制入口（cobra root command，与 `rclaude-daemon` 平级） | 新建 |
| `app/server/main.go` | 注册 `RemotePTY` service，把 `session.Manager` 注入 ptyhost | 小改 |
| `pkg/session/` | 增加 `LookupDaemon(userID) (DaemonSession, ok)` / `RegisterPTY` / `UnregisterPTY` | 小改 |
| `pkg/auth/` | — | 复用 |
| `pkg/transport/` | — | 复用 |
| `pkg/ratelimit/` | 增加 `pty.attach.qps` / `pty.bytes.in.bps` 两个限额配置项 | 小改 |
| `pkg/config/` | 增加 `pty:` 配置块 | 小改 |

#### 责任边界

- `pkg/ptyhost` 只懂"怎么开/管/关一个 PTY 进程"，不依赖 gRPC：对外接口收 `io.Reader`(stdin) / 出 `io.Writer`(stdout) / 出 `<-chan Resize` / 出 `Wait() ExitInfo`，可在不依赖网络的前提下单测。
- `pkg/ptyclient` 只懂"怎么把本地终端两端搭到一对 stream 上"，不懂 PTY 进程本身。
- `app/server/main.go` 是唯一把 ptyhost、session、ratelimit、auth、TLS、cobra 装配起来的地方。
- `app/clientpty/main.go` 只做"读 daemon 同款配置 → 拨号 → 调 ptyclient → 等退出码"，与 `app/client/main.go` 平级、互不依赖。
- `creack/pty` 仅作为 `pkg/ptyhost` 内部实现细节，不允许漏出包外。

## 4. 协议（proto）

新增 `api/proto/remotefs/v1/pty.proto`，与现有 `remotefs.proto` 同包：

```protobuf
syntax = "proto3";
package remotefs.v1;
option go_package = "flyingEirc/Rclaude/api/proto/remotefs/v1;remotefsv1";

service RemotePTY {
  // 建立 PTY 会话，双向流。
  // 第一帧必须是 ClientFrame.attach；服务端首条响应必须是 ServerFrame.attached 或 error。
  rpc Attach(stream ClientFrame) returns (stream ServerFrame);
}

message ClientFrame {
  oneof payload {
    AttachReq attach   = 1;   // 仅首帧
    bytes     stdin    = 2;   // PTY 输入字节
    Resize    resize   = 3;   // 终端尺寸变化
    Detach    detach   = 4;   // 客户端主动结束
  }
}

message AttachReq {
  string session_id = 1;        // MVP 留空 = 新建一次性会话；预留持久会话扩展
  Resize initial_size = 2;
  string term = 3;              // 在白名单内才透传
  repeated string extra_env = 15; // 预留；MVP 忽略
}

message Resize {
  uint32 cols = 1;
  uint32 rows = 2;
  uint32 x_pixel = 3;
  uint32 y_pixel = 4;
}

message Detach {}

message ServerFrame {
  oneof payload {
    Attached attached = 1;     // 首条响应
    bytes    stdout  = 2;      // PTY 输出字节（stdout/stderr 在 PTY 层已合一）
    Exited   exited  = 3;      // 进程退出，紧跟流关闭
    Error    error   = 4;
  }
}

message Attached {
  string session_id = 1;       // server 分配的稳定 ID
  string cwd        = 2;       // 一定是 /workspace/{user_id}/...
}

message Exited {
  int32  code   = 1;
  uint32 signal = 2;
}

message Error {
  enum Kind {
    UNKNOWN              = 0;
    UNAUTHENTICATED      = 1;
    DAEMON_NOT_CONNECTED = 2;
    SESSION_BUSY         = 3;
    SPAWN_FAILED         = 4;
    PROTOCOL             = 5;
    RATE_LIMITED         = 6;
    INTERNAL             = 99;
  }
  Kind   kind    = 1;
  string message = 2;
}
```

### 4.1 帧大小与背压

- 单帧 `stdin` / `stdout` payload 上限 **64 KiB**（`pty.frame_max_bytes` 可配）。超限：CLI 端拒发；server 端收到拒收并下发 `Error{PROTOCOL}` 后关流。
- gRPC 自带 stream-level 流控；ptyhost 读 PTY 用 64 KiB 缓冲，写满即 `Send`，让 gRPC 背压自然反传到 PTY 内核缓冲。
- 反方向：server 收 `stdin` 直接写进 PTY master fd，PTY 内核缓冲是天然 backpressure 点。

### 4.2 协议状态机

```
client                                  server
  │── ClientFrame{attach}  ───────────▶ │   验 token / 查 daemon / 抢 PTY 槽位 / spawn claude
  │ ◄──────────  ServerFrame{attached}──│
  │                                     │
  │── stdin / resize  ─────────────────▶│ ── 写 PTY master
  │ ◄──── stdout  ─────────────────────│ ── 读 PTY master
  │            ……                       │
  │── detach  ─────────────────────────▶│   或 client 直接 close stream
  │ ◄── exited{code, signal}  ─────────│   或 PTY 自然退出 → server 主动发 exited 后关流
                  关流（GOAWAY / EOF）
```

- 首帧不是 `attach` → server 立刻 `Error{PROTOCOL}` 关流。
- `attached` 之前发 stdin/resize → 同上。
- client 主动 `detach` → server 给 PTY 发 `SIGHUP`，等 ≤ `pty.graceful_shutdown_timeout`（默认 5s）自然退；超时再 `SIGKILL`，发 `exited` 关流。
- client 直接 close stream（网络断）→ server 视同 `detach`，并多打一条 `peer_eof` 审计。
- PTY 进程自然退出 → server 把残余 stdout 帧排空后发 `exited` 然后关流。

### 4.3 stdout/stderr 不分流

PTY 行规天然把两路合并到 master 端，从内核拿出来已经是单一字节流，无法可靠区分。这与 ssh / tmux 行为一致，对 claude TUI 也是预期行为。

## 5. 运行流程

### 5.1 CLI 端（`rclaude-claude`）

```
1. 解析 cobra flags：--server / --config / --debug
2. 加载 rclaude-daemon 同款 config schema（pkg/config 已有），取 server.address / auth.token / tls.*
3. 检查 stdin/stdout 是否 TTY；不是 TTY → 报错退出（claude 需要交互终端）
4. golang.org/x/term 把本地终端切 raw 模式（defer 恢复 + signal.Notify SIGINT/SIGTERM 双保险）
5. 拨号 server（pkg/transport.Dial，复用 TLS + auth interceptor）
6. 调 RemotePTY.Attach 拿到双向 stream
7. 发 ClientFrame{attach: {initial_size, term=$TERM}}
8. 等首条 ServerFrame：
     - Error → 打 stderr、恢复终端、按 Error.Kind 映射 exit code
     - Attached → 进入泵循环
9. errgroup 三条 goroutine：
     a. stdin 泵：os.Stdin → 64 KiB 缓冲 → ClientFrame{stdin}
        （不能用 io.Copy：要按 64 KiB 切帧 + 可被 ctx 取消）
     b. resize 泵：Linux/macOS 监听 SIGWINCH；Windows 用 250ms 轮询
                  GetConsoleScreenBufferInfo，变化才发帧
     c. stdout 泵：stream.Recv() → oneof 分派
                   stdout → os.Stdout 直写
                   exited → 记 exit info、cancel
                   error  → 同上但记错误信息
10. errgroup.Wait() 完成后：
     - 恢复终端原状态
     - 按 exited.code 退出（被信号杀则 128+signal）
     - error 路径按 Kind 映射 exit code
```

### 5.2 Server 端（`pkg/ptyhost` + `RemotePTY.Attach` handler）

```
RemotePTY.Attach handler:
1. auth interceptor 已校验 token，从 ctx 取出 user_id
2. 读首帧；非 attach → Error{PROTOCOL} + close
3. session.Manager.LookupDaemon(user_id):
     - 不在 → Error{DAEMON_NOT_CONNECTED} + close
4. session.Manager.RegisterPTY(user_id):
     - 已有活跃 PTY → Error{SESSION_BUSY} + close
     - 否则注册占位，拿到 sessionID
5. ratelimit.Acquire("pty.attach", user_id):
     - 拒 → UnregisterPTY + Error{RATE_LIMITED} + close
6. ptyhost.Spawn(SpawnReq{
       Binary:   cfg.PTY.Binary,             // 默认 "claude"
       Cwd:      "/workspace/" + user_id,    // safepath 校验
       Env:      buildEnv(cfg, attach),      // 白名单透传
       InitSize: attach.initial_size,
   }) → 失败 → Error{SPAWN_FAILED} + close
7. 发 ServerFrame{attached: {session_id, cwd}}
8. errgroup 两条 goroutine：
     a. PTY → stream：64 KiB 读 PTY master → ServerFrame{stdout}
     b. stream → PTY：循环 stream.Recv()
                       stdin  → ratelimit.Acquire("pty.bytes.in", n) → 写 PTY master
                       resize → ptyhost.Resize(cols, rows)
                       detach → ptyhost.Shutdown(graceful=true)
9. ptyhost.Wait() → ExitInfo
10. 排空 PTY 残余输出 → ServerFrame{exited} → close stream
11. defer：UnregisterPTY、释放 fd、ratelimit 归还
```

`pkg/ptyhost` 内部用 `creack/pty` 做 spawn，对外只暴露 `Spawn / Resize / Shutdown / Wait / Stdin() io.Writer / Stdout() io.Reader`。

## 6. 配置

`pkg/config` 新增 `pty:` 块，仅 server 端生效；`rclaude-claude` 端复用 `rclaude-daemon` 现有 `server.*` / `auth.*` / `tls.*`，不引入新字段。

```yaml
pty:
  binary: "claude"               # 也可写绝对路径
  workspace_root: "/workspace"   # 与 FUSE 挂载根一致；cwd = ${workspace_root}/${user_id}
  env_passthrough:
    - TERM
    - LANG
    - LC_ALL
    - LC_CTYPE
    - PATH
  frame_max_bytes: 65536
  graceful_shutdown_timeout: "5s"
  ratelimit:
    attach_qps: 1                # 每个 user_id 每秒最多 1 次 attach
    attach_burst: 3
    stdin_bps: 1048576           # 1 MiB/s
    stdin_burst: 262144
```

## 7. 错误降级矩阵

| 触发条件 | server 行为 | CLI 行为 | exit code |
|---|---|---|---|
| token 无效 | `Error{UNAUTHENTICATED}` + close | 打 "auth failed"，恢复终端 | 1 |
| 同 user_id 无 daemon 连接 | `Error{DAEMON_NOT_CONNECTED}` + close | 打 "daemon offline, run daemon first" | 2 |
| 同 user_id 已有活跃 PTY | `Error{SESSION_BUSY}` + close | 打 "another claude session is active" | 3 |
| `claude` 二进制不存在 | `Error{SPAWN_FAILED}` + close | 打具体错误消息 | 4 |
| attach 限流 | `Error{RATE_LIMITED}` + close | 打 "too many attaches" | 5 |
| stdin 限流 | server 阻塞等令牌（不丢字节） | 打字会卡顿，不报错 | — |
| 首帧不是 attach / 帧超限 | `Error{PROTOCOL}` + close | 打 protocol error | 6 |
| attach 后 daemon 断开 | **不动 PTY**，写审计日志；FUSE 进入 Phase 6A 只读降级 | 用户在 claude 里看到 EIO 类报错，自行决策 | — |
| attach 后 client 网断 | 视同 detach：SIGHUP → 5s → SIGKILL | 进程已死 | 130（信号） |
| claude 进程崩溃 | 排空 stdout → `exited{code, signal}` + close | 透传 exit code | claude.exit_code |
| claude 正常退出 | 同上 | 同上 | 0 |
| server 进程关闭 | 对所有活跃 PTY 走 detach 流程 | 透传 SIGHUP 退出 | 129 |

关键设计决策：

- **daemon 断开 ≠ 杀 claude**：网络抖动很常见，claude 上下文很值钱，已有 Phase 6A 的 TTL 只读降级足以让 claude 在断线窗口里继续读本地缓存，等 daemon 重连即可恢复读写。
- **stdin 限流走"阻塞"而不是"丢字节"**：丢字节会让 PTY 输入语义错乱（命令打一半被截）。
- **退出码非 0 都对应明确 Kind**，便于脚本化判断"是断网还是真的 claude 失败"。

## 8. 测试基线

沿用项目现有的"包级 testify 单测 + `internal/inmemtest` 集成 + Linux 真 FUSE 冒烟"三层结构。

### 8.1 单元测试

| 包 | 用例要点 |
|---|---|
| `pkg/ptyhost` | spawn 成功 / binary 不存在 / cwd 不存在 / env 白名单生效 / Resize 传到 pty / Shutdown 触发 SIGHUP→超时→SIGKILL / Wait 返回正确 ExitInfo |
| `pkg/ptyhost/policy.go` | safepath 拒绝 `..` / 拒绝绝对路径越权 / env 白名单过滤 |
| `pkg/ptyclient` | stdin 切帧（≤64 KiB）/ 收 stdout 写 stdout / 收 exited 触发结束 / 收 error 按 Kind 退出 / SIGWINCH → resize 帧 |
| `pkg/config` | `pty:` 块 schema 校验 / 默认值 / 限流参数解析 |
| `pkg/ratelimit` | 新加的 attach + stdin 两条阈值生效 |
| `pkg/session` | RegisterPTY 互斥（SESSION_BUSY） / Daemon 断开后 LookupDaemon 返回 false / UnregisterPTY 幂等 |

`pkg/ptyhost` 真实 spawn 用 `/bin/sh -c "echo hello; sleep 0.1"` 这类内置可执行，避免依赖 claude；Windows 单元侧用 `cmd.exe` 或 build tag 跳过（Phase 7 部署只面向 Linux）。

### 8.2 集成测试（`internal/inmemtest`）

新增 `pty` 子套件，复用既有夹具（已能拉起完整 server + 多 daemon + FUSE）：

| 场景 | 验收 |
|---|---|
| daemon 已连 + 正常 attach + echo + detach | exit 0 / stdout 包含期望串 / session 已注销 |
| daemon 没连 → attach | 收到 `Error{DAEMON_NOT_CONNECTED}` |
| 同 user_id 第二次并发 attach | 第二次收 `Error{SESSION_BUSY}` |
| attach 限流触发 | 收 `Error{RATE_LIMITED}` |
| attach 中 daemon 断开 → claude 还活着、读 FUSE 走 Phase 6A 降级 | PTY 不被杀 / 审计日志有 daemon_offline 事件 |
| client 网断（中间断 stream） | server 5s 内 SIGHUP 干掉子进程、UnregisterPTY |
| 子进程自然退出 | server 把残余 stdout 排空再发 exited |
| 首帧非 attach | `Error{PROTOCOL}` |
| 超大 stdin frame | `Error{PROTOCOL}` |
| Resize 帧穿透 | `ptyhost` 拿到的 cols/rows 与发的一致 |

集成测试用 fake claude（一个临时构建的小 Go 程序，把 stdin 回显并响应 SIGWINCH 打印当前尺寸），不依赖真 claude。

### 8.3 Linux 真 PTY 冒烟

在 `tools/fuse-smoke.sh` 旁新增 `tools/pty-smoke.sh`：用 fake claude 跑一次 `rclaude-claude`，验证终端 raw 模式生效、SIGWINCH 能传、Ctrl+C 关掉远端进程、退出码透传。

`make test` 默认带 inmem 套件；真 PTY 冒烟在脚本里，与 fuse-smoke 对称。

## 9. 可观测性

复用 `pkg/logx`，结构化日志事件：

- `event=pty.attach user_id=… session_id=… peer=…`
- `event=pty.attached cwd=…`
- `event=pty.detach reason=client|server|peer_eof`
- `event=pty.exit code=… signal=…`
- `event=pty.daemon_offline`
- `event=pty.error kind=… msg=…`

不录 stdin/stdout 字节流。后续真要做录屏，单独阶段做 asciinema 风格落地。

## 10. 部署 / 文档

- `deploy/minimal/` 已有 server 示例 compose，PTY 不引入新依赖（只要 `claude` 在 server 镜像 PATH 里）。
- `docs/reference/` 新增 `pty-protocol.md`：proto frame 表 + 错误 Kind 表 + 退出码映射。
- `docs/ARCHITECTURE.md` "实现架构"一节追加："`RemotePTY` 是与 `RemoteFS` 并列的第二条 gRPC 入口，承载用户与远端 claude 的交互流。"
- `README.md` 在"使用方式"加一行 `rclaude-claude --config …` 示例。

## 11. ROADMAP 衔接

PTY 不在现有 Phase 0–7 任何一项的延伸里，而是在 Phase 6/7 之上**新开的一条产品化主线**。建议在 `docs/design/ROADMAP.md` 新增 **Phase 8 — Remote PTY**，拆为：

- **Phase 8a**：proto + ptyhost + ptyclient + 单元测试
- **Phase 8b**：service 装配 + 集成测试 + 错误降级矩阵全覆盖
- **Phase 8c**：部署文档 + smoke 脚本 + 真 claude 端到端验收

每个子阶段按现有规则建 `docs/exec-plan/active/{时间}-phase8x-…/` 三件套。

## 12. 显式 YAGNI 清单

明确不做（每一项 proto 已留扩展位，不破协议）：

- 多并发 PTY、持久会话/重连、tmux 风格的输出回放
- 录屏 / 操作审计回放
- 客户端指定 cwd 子目录
- 客户端追加 env / 自定义 binary
- 单独的 control-frame 信号通道（Ctrl+C 直接走 PTY 字节）
- setuid / 容器化 / 沙箱化 claude 进程
- Web 入口（浏览器里跑 claude TUI）
