# Rclaude

Language: 中文 | [English](README.md)

`Rclaude` 是一个远程文件访问系统。它的目标不是“同步一份代码副本到云端”，而是让远端执行环境通过普通文件路径访问 daemon 侧本地工作区。

对执行侧来说，文件依然表现为“本地文件系统上的真实路径”；对系统来说，真正的数据来源仍然是 daemon 侧的本地项目工作区（启动 rclaude 时所在的项目根目录）。

典型场景：

- 云端 Agent / 任务执行器运行 `cat /workspace/{user_id}/{project}/main.go`
- Server 侧 FUSE 文件系统接管该路径访问
- 请求通过 gRPC 双向流转发到该用户对应的 daemon
- daemon 读取本地项目工作区并返回内容

这样可以兼容现成的 `cat`、`sed`、`grep`、`ls`、`stat` 等 shell 工具，而不需要改造工具调用方式。

## 这个项目当前已经实现了什么

当前代码基线已经具备一条可工作的主链路：

- 统一入口 `rclaude` 内的 daemon 启动后扫描工作区，并通过 gRPC 双向流主动连接 Server
- Server 维护每个用户的会话、文件树元数据和内容缓存
- Server 在 Linux 上通过 FUSE 暴露 `/workspace/{user_id}/{project}/` 虚拟工作区（`{project}` 即本地项目目录名）
- 执行环境可直接对该挂载路径执行文件命令，无需专用 SDK 或专用 CLI

当前已实现能力包括：

- 读操作：`Lookup` / `Getattr` / `Readdir` / `Open` / `Read`
- 写操作：创建、覆盖写、偏移写、追加写、`mkdir`、`rename`、`delete`、`truncate`
- 多用户隔离：每个用户独立挂载到 `/workspace/{user_id}/{project}/`；同一用户同时只有一个 daemon 会话，换项目重连会替换旧会话
- 文件树缓存：支撑目录浏览与属性查询
- 整文件内容缓存：降低重复读取时的网络往返
- 小文件预取：目录读取后预热高概率被继续访问的小文件
- 离线只读降级：daemon 断线后，在 TTL 窗口内允许基于缓存只读访问
- 敏感文件过滤：默认隐藏 `.env`、私钥、证书等敏感文件，并拒绝相关写操作
- daemon 侧读写字节限流：限制读回与落盘的字节速率
- 工作区边界保护：路径校验、防路径穿越、限制只访问指定 workspace
- 配置加载与环境变量覆盖：基于 YAML + `RCLAUDE_*` 环境变量
- 静态 token 鉴权：token 映射到 `user_id`
- 统一入口 `rclaude`：本地一次启动 daemon 与 RemotePTY attach，按依赖顺序协调启动与重试（`pkg/startup`）
- Server 侧终端透传：只运行用户在 rclaude 命令行声明的 agent 程序（`-g/--agent`，如 `claude`、`codex`）；附着后直接进入 agent，agent 退出即结束会话，不提供 shell 兜底，argv 也不受客户端控制
- 关闭终端时优雅退出：SIGINT/SIGTERM/SIGHUP 会先让在途文件流与 PTY 收尾，再关闭 daemon 与会话
- 文件化结构日志：默认 JSON、按大小/时间轮转，且从不写终端，保证 PTY 透传干净
- 可选审计日志：把远端文件操作记录持久化到 SQLite / MySQL / PostgreSQL

## 架构概览

系统由三部分组成：

1. `rclaude`（统一本地入口：daemon + PTY attach）
2. `rclaude-server`
3. Server 所在环境中的普通 shell / Agent / 自动化任务

核心数据流如下：

```text
本地工作区
    ^
    | 读写文件 / 监听变更
    v
rclaude（daemon + pty attach）
    ^
    | gRPC 双向流
    v
rclaude-server
    ^
    | FUSE 挂载
    v
/workspace/{user_id}/{project}/...
    ^
    | cat / sed / grep / ls / stat / mv / rm ...
    v
执行环境
```

在 daemon 机器上，一切通过统一入口 `rclaude`（`app/rclaude`）运行：它同时拉起
daemon（`RemoteFS.Connect`）与终端 attach（`RemotePTY.Attach`），并协调二者的启动，
使 PTY 只在 daemon 完成向 Server 注册后才 attach。它是唯一的本地入口；远端会话
要运行的 agent 程序在启动命令行声明（`rclaude -g <agent> -c <config>`）。

设计重点：

- Daemon 主动连 Server，避免要求 Server 反向连接用户本地机器
- FUSE 是主方案，不是兼容层
- Server 端缓存和预取是架构内的一等能力，不是附加优化
- 文件同步与终端两条职责在统一入口中共享一份配置和一条协调生命周期，但仍是各自独立的 gRPC 流
- 执行环境的兼容目标是“普通文件路径语义”，而不是某个特定模型或 Agent

最小双机部署与手工验收流程见 [deploy/minimal/README_ZH.md](deploy/minimal/README_ZH.md)。

## 平台与运行要求

- Server 端当前要求 Linux
- Server 端要求可用 FUSE 环境
- daemon 端按实现目标支持 Linux / macOS / Windows
- 项目当前 `go.mod` 固定为 Go `1.25.2`

需要特别注意：

- 非 Linux 平台不会真正挂载 FUSE；对应实现会返回不支持错误
- 当前仓库已有最小可用运行路径，但还不是完整生产化交付
- 当前没有内建 TLS、Docker、systemd、安装器、审计链路或运维面板

## 仓库结构

```text
api/                    gRPC 协议与生成代码
app/rclaude/            rclaude 统一本地入口（daemon + PTY，协调启动）
app/server/             rclaude-server 命令入口
pkg/config/             YAML / 环境变量配置加载
pkg/logx/               文件化结构日志（从不写终端）
pkg/startup/            统一入口的启动协调器（依赖门控 + 重试）
pkg/auth/               token 鉴权
pkg/safepath/           工作区路径校验与边界保护
pkg/fstree/             文件树元数据索引
pkg/session/            Server 侧用户会话与请求路由
pkg/contentcache/       Server 侧整文件内容缓存
pkg/fusefs/             FUSE 文件系统视图
pkg/syncer/             daemon 侧同步、扫描、监听、请求处理
pkg/ptyhost/            Server 侧 PTY 子进程拉起（attach 声明的 agent）
pkg/ptyservice/         Server 侧 RemotePTY gRPC 服务
pkg/ptyclient/          daemon 侧终端 <-> PTY 的 gRPC 桥接
pkg/ptyattach/          本地终端 attach（raw 模式、resize、退出码）
pkg/audit/              可选的远端文件操作审计落库
pkg/transport/          gRPC 连接与 stream 封装
pkg/ratelimit/          daemon 侧字节限流
internal/inmemtest/     in-memory 端到端测试夹具
internal/testutil/      共享测试夹具与辅助工具
deploy/minimal/         最小远程/本地测试闭包（配置 + 启动脚本）
tools/                  proto 代码生成插件版本锁定 (tools.go)
```

## 构建

先安装 Rclaude 仓库约定的开发工具：

```bash
make tools
```

编译主程序：

```bash
# Server（远程 Linux）与统一本地入口是仅有的两个程序入口
go build -o ./bin/rclaude-server ./app/server
go build -o ./bin/rclaude ./app/rclaude
```

也可以直接做全仓库构建检查：

```bash
go build ./...
```

## 快速启动

### 1. 准备 Server 配置

示例 `server.yaml`：

```yaml
listen: ":9326"                      # gRPC 监听地址，":端口" 表示监听所有网卡；必填
auth:
  tokens:
    "example-token": "example-user"  # token -> user_id 映射；daemon 用 token 换取自己的 user_id，至少要有一条
fuse:
  mountpoint: "/workspace"           # FUSE 挂载根目录，必须是绝对路径；每个用户会挂在 {mountpoint}/{user_id} 下
cache:
  max_bytes: 268435456               # Server 侧整文件内容缓存上限（字节），<=0 表示关闭缓存
prefetch:
  enabled: true                      # 是否在读目录后预取小文件内容（依赖 cache.max_bytes > 0，否则自动跳过）
  max_file_bytes: 102400             # 触发预取的单文件大小上限（字节），超过此大小的文件不预取
  max_files_per_dir: 16              # 单次目录预取的最大文件数
request_timeout: 10s                 # 单次文件请求（Lookup/Getattr/Read/Write 等）超时时间，<=0 时回退默认值 10s
offline_readonly_ttl: 5m             # daemon 断线后，Server 侧缓存内容维持只读可访问的时长，默认 5m
log:
  level: "info"                      # 日志级别：debug | info | warn | error
  format: "text"                     # 日志格式：json（默认）| text
```

### 2. 准备 daemon 配置

示例 `daemon.yaml`：

```yaml
server:
  address: "127.0.0.1:9326"           # Server gRPC 地址，必填
  token: "example-token"              # 必须对应 Server 侧 auth.tokens 中的某个 token
workspace:
  exclude:                            # 扫描/监听时排除的路径模式（glob）
    - ".git"
    - "node_modules"
    - "vendor"
  sensitive_patterns:                 # 在内置敏感规则（.env、私钥、证书等）之外追加的敏感路径模式
    - "secrets/**"
# 注意：没有 workspace.path 配置项。工作区根目录 = 启动 rclaude 时
# 所在的目录，请在项目根目录下运行；该目录名会成为 Server 侧的项目名
# （必须是单段安全路径：不含 / \ 与控制字符，且不能是 . / ..）。
rate_limit:
  read_bytes_per_sec: 0               # daemon 返回读取内容的字节速率上限，<=0 表示不限速
  write_bytes_per_sec: 0              # daemon 落盘写入的字节速率上限，<=0 表示不限速
self_write_ttl: 2s                    # daemon 自己写回文件后，短时间内忽略由此触发的本地监听事件，避免自触发回环；<=0 时回退默认值 2s
log:
  level: "info"                       # 日志级别：debug | info | warn | error
  format: "text"                      # 日志格式：json（默认）| text
```

### 3. 启动

先启动 Server：

```bash
./bin/rclaude-server --config ./server.yaml
```

再在项目根目录下启动统一入口，并声明远端会话要运行的 agent：

```bash
./bin/rclaude -g claude -c ./daemon.yaml
```

启动成功后（假设在本地项目目录 `myproj` 下启动），Server 侧会出现：

```text
/workspace/example-user/myproj/
```

此时执行环境可直接读取：

```bash
ls -la /workspace/example-user/myproj
cat /workspace/example-user/myproj/README.md
grep -R "TODO" /workspace/example-user/myproj
```

如果走的是写操作链路，也会回写到本地真实工作区，例如：

```bash
mkdir /workspace/example-user/myproj/tmp
printf 'hello\n' > /workspace/example-user/myproj/tmp/demo.txt
mv /workspace/example-user/myproj/tmp/demo.txt /workspace/example-user/myproj/tmp/demo2.txt
truncate -s 2 /workspace/example-user/myproj/tmp/demo2.txt
rm /workspace/example-user/myproj/tmp/demo2.txt
```

## 环境变量覆盖

配置通过 `viper` 加载，支持 `RCLAUDE_*` 环境变量覆盖。

例如：

```bash
export RCLAUDE_SERVER_ADDRESS=127.0.0.1:9999
./bin/rclaude -g claude -c ./daemon.yaml
```

点号会自动映射成下划线，所以：

- `server.address` -> `RCLAUDE_SERVER_ADDRESS`
- `fuse.mountpoint` -> `RCLAUDE_FUSE_MOUNTPOINT`

## 远程 PTY 与 Agent 入口

Rclaude 的交互 Agent 适配在同一个 `rclaude` 入口内保持两条链路分离：

- 文件链路：daemon 通过 `RemoteFS.Connect` 把本地 workspace 暴露给 Server，Server 再通过 FUSE 提供 `/workspace/{user_id}/{project}`。
- 终端链路：PTY attach 通过 `RemotePTY.Attach` 只转发终端字节流、窗口尺寸、退出码和错误帧，外加声明的 agent 名。

要运行的 agent 由用户在启动命令行按会话声明：

```bash
rclaude -g claude -c ./daemon.yaml                    # 裸名，走 Server 侧 PATH 解析
rclaude -g codex -c ./daemon.yaml
rclaude -g /root/.local/bin/codex -c ./daemon.yaml    # Server 上的绝对路径
```

Server 在 `/workspace/{user_id}/{project}` 中直接启动该 agent 程序，agent 退出即整个会话结束。不提供 shell 兜底，argv 也不受客户端控制，用户无法在 Server 上 `ls`/`cd`、无法查看远程工作区路径，交互被限制在 agent 界面内。进程运行在 Server，而不是 daemon 所在机器；Server OS user 必须能找到对应二进制（裸名走 `PATH`，或直接给绝对路径），并具备该 CLI 所需的登录态或环境变量。

Server 侧 PTY 配置示例：

```yaml
pty:
  workspace_root: "/workspace"        # PTY 工作目录根路径，必须是绝对路径；应与 fuse.mountpoint 保持一致，实际 cwd 为 {workspace_root}/{user_id}/{project}
  env_passthrough:                    # 允许从 Server 进程环境透传给 PTY 子进程的变量白名单，默认即为下面这份列表
    - "TERM"
    - "LANG"
    - "LC_ALL"
    - "LC_CTYPE"
    - "PATH"
    - "HOME"
    - "SHELL"
    - "CLAUDE_CONFIG_DIR"
  frame_max_bytes: 65536               # 单帧 PTY 数据的最大字节数，必须 > 0，默认 65536（64KiB）
```

Rclaude 仓库包含一组最小远程/本地测试闭包，详见 [deploy/minimal/README_ZH.md](deploy/minimal/README_ZH.md)。推荐顺序：

1. 启动 Server：`deploy/minimal/start-server.sh` 交叉编译、部署并在远程启动 `rclaude-server`。
2. 启动本地：`deploy/minimal/start-rclaude.sh <agent> <config>` 运行统一入口（daemon + PTY attach），进入远程会话。

当前实测状态：

- `/bin/sh` scripted PTY + FUSE 文件读取已通过。
- Codex CLI TUI attach、cwd `/workspace/{user_id}/{project}`、`codex exec` 读取 daemon-backed FUSE 文件、远端 code `0` 回传已通过。
- Claude Code TUI 可以通过 RemotePTY 渲染，但主提示符验收取决于 Server OS user 的 Claude Code onboarding/login 状态；daemon 机器上的 Claude 登录态不会自动复用到 Server。

## 日志、启动与退出

日志从不写终端。统一入口 `rclaude` 会把终端交给远端 PTY，因此所有诊断信息都写入
会轮转的日志文件，保证终端输出是干净透传。两侧都通过 `log` 段控制：

```yaml
log:
  level: "info"         # 日志级别：debug | info | warn | error
  format: "json"        # json（默认）| text
  # dir: ""             # 日志目录，省略时用 ~/.rclaude/logs
  # max_size_mb: 100    # 单文件轮转大小（MB）
  # max_backups: 3      # 保留轮转文件个数
  # max_age_days: 7     # 轮转文件保留天数
```

统一入口写 `rclaude.log`；Server 写 `rclaude-server.log`。终端上你只会
看到每个组件一行状态（`daemon started`、`pty started`）。

启动是协调的，不是竞争的。daemon 与 PTY 一起启动，但 PTY 声明了对 daemon 的依赖，
因此它的首次 attach 会等到 daemon 完成向 Server 注册，而不是先失败在
`daemon not connected` 再重试。残余失败仍会回退到事件总线重试，可在 daemon 配置里调整：

```yaml
startup:
  max_retries: 3        # 初始尝试之外的重试次数（总尝试数 = 1 + max_retries）
  retry_delay: 1s       # 收到重试通知后再次尝试前的等待
```

退出是优雅的。`SIGINT`（Ctrl-C）、`SIGTERM` 与 `SIGHUP`（关闭整个终端窗口）都会取消
运行上下文，让在途文件流与 PTY 收尾后再关闭 daemon 与会话，并把退出写进日志。第二次
信号或 10s 宽限超时会强制立即退出。

## 可选的文件操作审计

daemon 可以把每次远端文件操作记录持久化到本地数据库，用于事后审计。默认关闭，在
daemon 配置里开启：

```yaml
audit:
  enabled: true             # 是否开启审计，默认 false（关闭）
  driver: "sqlite"          # sqlite | mysql | postgres（也接受 sqlite3/postgresql/pgsql 别名）
  dsn: "file:audit.db"      # 各驱动各自的 DSN；enabled 为 true 时必填
  table: "file_audit_log"   # 审计记录写入的表名，只能包含字母、数字、下划线，默认 file_audit_log
  queue_size: 256           # 写入前的内存缓冲队列大小，写满后落库会阻塞，默认 256
```

## 测试与开发命令

Rclaude 仓库约定的标准流程：

```bash
make fmt
make lint
make test
```

常用命令：

```bash
make all
make check
make test-cover
go build ./...
```

当前测试基线包括：

- 包级单元测试
- 跨平台 `inmem` 集成测试
- Linux 真 FUSE 自动化冒烟测试

其中 Linux 真 FUSE 测试会直接走 `Mount -> kernel/FUSE -> session -> daemon` 真实链路；如果环境不支持 FUSE，测试会按约定跳过而不是伪通过。

## 当前限制

当前仓库已经能支撑 MVP 到增强阶段的主要能力，但仍有明确边界：

- Server 必须运行在支持 FUSE 的 Linux 环境
- 鉴权当前是静态 token 映射，不是完整身份系统
- 当前更适合 1-20 人小团队场景
- 当前没有“把整个工作区完整镜像到 Server”的设计
- 断线时只支持基于缓存的临时只读降级，不支持离线写回
- 还没有完整生产部署物，如容器化、systemd 单元、TLS 与日志轮转

## 相关入口

- English README: [README.md](README.md)
- 最小双机部署（中文）：[deploy/minimal/README_ZH.md](deploy/minimal/README_ZH.md)
