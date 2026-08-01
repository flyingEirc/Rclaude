# Rclaude

语言：[English](README.md) | 中文

---

## 1. 这个项目是什么？

`Rclaude` 是一套「远程文件访问 + 远程 agent 终端」系统。

它**不会**把你的工作区整体上传到云端机器，而是通过普通文件路径，把本地工作区
暴露给远程执行环境，并让一个交互式编码 agent（如 Claude Code、Codex）在那台远程
机器上、针对这些同样的路径运行。

- 从执行侧看，文件就是 `/workspace/{user_id}/{project}/` 下的普通本地文件路径。
- 从系统侧看，数据的真实来源始终是你本机上的 daemon 侧工作区。

本机只跑一个统一入口 `rclaude`，它向 server 建立两条相互独立的 gRPC 流：

- **文件通道** —— daemon（`RemoteFS.Connect`）暴露本地工作区，server 通过 FUSE
  挂载对外发布。
- **终端通道** —— PTY attach（`RemotePTY.Attach`）只转发终端字节、resize 事件和退出
  状态，直接把你送进运行在 server 上、位于工作区内的 agent 程序。

```text
本地工作区
    ^
    | 读写文件、监听变更
    v
rclaude  （daemon + PTY attach，单一统一入口）
    ^
    | 两条相互独立的双向 gRPC 流
    v
rclaude-server
    ^
    | FUSE 挂载 + agent PTY
    v
/workspace/{user_id}/{project}/...
    ^
    | cat / sed / grep / ls / stat / mv / rm ...  以及 agent 本身
    v
执行环境（远程 Linux）
```

## 2. 为什么存在？解决什么问题？

目标是：在**不**把整个项目搬到云端、也**不**引入自定义文件 SDK 的前提下，运行一个
云端/远程编码 agent。

- **文件留在本地且为权威源。** daemon 侧工作区始终是唯一真相来源，server 只持有
  缓存与元数据，永不保存完整副本。
- **普通路径语义。** 因为 server 侧表面是 FUSE 挂载，标准 shell 工具
  （`cat`、`sed`、`grep`、`ls`、`stat`、`mv`、`rm`）以及 agent 自身的文件访问都能
  原样工作，无需对接任何文件 API。
- **由 daemon 主动外连。** 连接从你的机器发起到 server，server 无需反向拨回用户的
  本地网络。
- **agent 跑在远端、贴近算力，** 同时仍看到你的本地文件。终端是一条干净的
  passthrough，只进入唯一声明的 agent 程序 —— 没有 shell，也无法在 server 上乱逛。

典型流程：远程 agent 执行 `cat /workspace/{user_id}/{project}/main.go`，server 侧
FUSE 处理该路径，经 gRPC 流转发请求给你的 daemon，daemon 读取本地文件并返回。

当前已实现：

- 读操作：`Lookup`、`Getattr`、`Readdir`、`Open`、`Read`
- 写操作：创建、覆写、偏移写、追加、`mkdir`、`rename`、`delete`、`truncate`
- 按用户隔离于 `/workspace/{user_id}/{project}/`；每用户仅一个活跃 daemon 会话
  （从另一个项目重连会替换旧会话）
- 文件树元数据缓存、整文件内容缓存、目录读后的小文件预取
- daemon 断连后临时只读缓存兜底
- 敏感文件过滤（`.env`、私钥、证书、自定义模式）、工作区边界与路径穿越防护
- 静态 token 认证映射到 `user_id`
- server 侧终端 passthrough 严格限定于命令行声明的 agent（`-g/--agent`），落点在
  `/workspace/{user_id}/{project}`
- 协调式启动（PTY 等待 daemon 注册完成）与优雅关停（先排空在途文件流与 PTY）
- 只写文件、绝不污染终端的结构化日志
- 可选的前置代理 TLS（Caddy），以及可选的 SQLite/MySQL/PostgreSQL 文件操作审计日志

## 3. 怎么快速跑起来？

### 环境要求

- **server 侧：** 具备可用 FUSE 支持（`/dev/fuse`）的 Linux，**不能**跑在 macOS。
- **daemon 侧（你的机器）：** Linux、macOS 或 Windows，本地无需 FUSE。
- **Go：** 本仓库 `go.mod` 固定 Go `1.25.2`。

### 构建

```bash
make tools                                   # 仓库使用的开发工具

# server（远程 Linux）与统一本地入口是仅有的两个入口
go build -o ./bin/rclaude-server ./app/server
go build -o ./bin/rclaude        ./app/rclaude

# 或做一次全仓编译检查：
go build ./...
```

### server 配置（`server.yaml`）

```yaml
listen: ":9326"                      # gRPC 监听地址；":port" 绑定所有网卡。必填。
auth:
  tokens:
    "example-token": "example-user"  # token -> user_id；daemon 用 token 换取 user_id。至少一条。
fuse:
  mountpoint: "/workspace"           # FUSE 挂载根（绝对路径）；每个项目出现在 {mountpoint}/{user_id}/{project}。
cache:
  max_bytes: 268435456               # 整文件内容缓存上限（字节）；<=0 关闭缓存。
prefetch:
  enabled: true                      # 目录读后预取小文件（需 cache.max_bytes > 0）。
  max_file_bytes: 102400             # 可预取的单文件最大字节数。
  max_files_per_dir: 16              # 每次目录读预取的最大文件数。
request_timeout: 10s                 # 单请求超时；<=0 回退到 10s。
offline_readonly_ttl: 5m             # daemon 断连后缓存内容保持只读可访问的时长。
```

没有 `pty:` 或 `log:` 段 —— PTY 与日志行为已写死（见 §6）。

### daemon 配置（`daemon.yaml`）

```yaml
server:
  address: "127.0.0.1:9326"          # server gRPC 地址。必填。
  token: "example-token"             # 必须匹配 server 的 auth.tokens 之一。
  # 前置有 TLS 终止器（如 Caddy）时的可选 TLS：
  # tls:
  #   enabled: true
  #   server_name: ""                # 直连 IP 但证书签给域名时必填
  #   ca_file: ""                    # 自签/内部 CA 的 PEM；公网可信证书留空
workspace:
  exclude:                           # 扫描/监听时排除的 glob
    - ".git"
    - "node_modules"
    - "vendor"
  sensitive_patterns:                # 在内置规则之上追加的敏感路径
    - "secrets/**"
self_write_ttl: 2s                   # 忽略 daemon 自身写事件的时间窗；<=0 回退到 2s。
```

没有 `workspace.path`：工作区根就是你启动 `rclaude` 的那个目录，所以**要从项目根
目录运行它**。该目录名会成为 server 上的项目名（必须是单个安全路径段：不含 `/`、
`\`、控制字符，且不为 `.`/`..`）。

### 启动两侧

```bash
# 远程 Linux server：
./bin/rclaude-server --config ./server.yaml

# 你的机器，从项目根目录运行，并声明远端跑哪个 agent：
./bin/rclaude -g claude -c ./daemon.yaml
```

若你在名为 `myproj` 的目录启动 `rclaude`，server 现在会暴露
`/workspace/example-user/myproj/`，且你的终端已 attach 到运行在其中的 `claude`。

最小可用的真实远程/本地闭环见
[deploy/minimal/README.md](deploy/minimal/README.md)。

## 4. 怎么使用？

### 从 server 侧做文件操作

server 上任意进程（shell、agent、自动化）都用普通路径：

```bash
ls -la /workspace/example-user/myproj
cat   /workspace/example-user/myproj/README.md
grep -R "TODO" /workspace/example-user/myproj
mkdir /workspace/example-user/myproj/tmp
printf 'hello\n' > /workspace/example-user/myproj/tmp/demo.txt
mv    /workspace/example-user/myproj/tmp/demo.txt /workspace/example-user/myproj/tmp/demo2.txt
truncate -s 2 /workspace/example-user/myproj/tmp/demo2.txt
rm    /workspace/example-user/myproj/tmp/demo2.txt
```

### 远程 agent（PTY）入口

agent 按会话在命令行声明 —— 不写进配置，也不由客户端 argv 控制：

```bash
rclaude -g claude -c ./daemon.yaml                    # 裸名，经 server 的 PATH 解析
rclaude -g codex  -c ./daemon.yaml
rclaude -g /root/.local/bin/codex -c ./daemon.yaml    # server 上的绝对路径
```

server 就在 `/workspace/{user_id}/{project}` 内启动那个程序，程序退出即会话结束。
没有 shell 兜底、没有客户端可控 argv，所以会话无法在 server 上 `ls`/`cd`、看不到
远端工作区路径、也离不开 agent 界面。进程跑在 **server** 机器上，因此 server 的
系统用户必须能解析该二进制并持有该 CLI 所需的登录态/环境 —— 你本机的登录不会被
server 侧复用。

已观测状态：

- `/bin/sh` 脚本化 PTY 加 FUSE 文件读：通过。
- Codex CLI TUI attach、`codex exec` 读取 daemon 支撑的 FUSE 文件、远端退出码透传：
  通过。
- Claude Code TUI 可经 RemotePTY 渲染，但主提示词是否可用取决于 server 系统用户的
  Claude Code 引导/登录。

### 可选 TLS（前置代理）

server 二进制保持明文 h2c；在其前面放 Caddy 终止 TLS，并打开 daemon 的 `server.tls`
段即可。`acme` 与 `internal` CA 两种模式，以及 Cloudflare 注意事项（双向 gRPC 流需
**DNS-only**/灰云记录）见 [deploy/tls/README.md](deploy/tls/README.md)。

### 可选文件操作审计

默认关闭；在 daemon 配置中启用，可把每次远程文件操作落到本地数据库：

```yaml
audit:
  enabled: true             # 默认 false
  driver: "sqlite"          # sqlite | mysql | postgres（sqlite3/postgresql/pgsql 别名亦可）
  dsn: "file:audit.db"      # 驱动相关 DSN；启用时必填
  table: "file_audit_log"   # 仅字母/数字/下划线
  queue_size: 256           # 写入阻塞前的内存缓冲大小
```

### 环境变量覆盖

配置经 `viper` 加载，支持 `RCLAUDE_*` 覆盖（点号 → 下划线）：

```bash
export RCLAUDE_SERVER_ADDRESS=127.0.0.1:9999   # server.address
export RCLAUDE_FUSE_MOUNTPOINT=/workspace      # fuse.mountpoint
./bin/rclaude -g claude -c ./daemon.yaml
```

## 5. 怎么参与开发？

### 仓库结构

```text
api/                    gRPC 协议与生成代码
app/rclaude/            rclaude 统一本地入口（daemon + PTY，协调式启动）
app/server/             rclaude-server 入口
pkg/config/             YAML + 环境变量配置加载
pkg/logx/               只写文件、绝不写终端的结构化日志
pkg/startup/            统一入口的启动协调器（依赖门控 + 重试）
pkg/auth/               token 认证
pkg/safepath/           工作区路径校验与边界防护
pkg/fstree/             文件树元数据索引
pkg/session/            server 侧用户会话与请求路由
pkg/contentcache/       server 侧整文件内容缓存
pkg/fusefs/             FUSE 文件系统视图
pkg/syncer/             daemon 侧扫描、监听、同步与请求处理
pkg/ptyhost/            server 侧 PTY 子进程启动（attach 声明的 agent）
pkg/ptyservice/         server 侧 RemotePTY gRPC 服务
pkg/ptyclient/          daemon 侧 终端 <-> PTY gRPC 桥接
pkg/ptyattach/          本地终端 attach（raw 模式、resize、退出码）
pkg/audit/              可选的远程文件操作 DB 审计日志
pkg/transport/          gRPC 连接与流封装（客户端唯一 dial 点）
pkg/ratelimit/          server 侧 PTY attach/stdin 限流
internal/inmemtest/     内存端到端测试骨架
internal/testutil/      共享测试夹具与辅助
deploy/minimal/         最小远程/本地测试闭环（配置 + 启动脚本）
deploy/tls/             server 前置的可选 Caddy TLS 终止
tools/                  proto 代码生成工具版本固定（tools.go）
```

### 开发命令

```bash
make fmt          # 格式化
make lint         # 静态检查（对测试文件同样生效）
make test         # 单元 + 集成测试
make all          # fmt + lint + test
make check        # 与 CI 一致的门禁
make test-cover   # 带覆盖率的测试
go build ./...    # 全仓编译检查
```

测试基线：包级单元测试、跨平台内存集成测试、Linux 真实 FUSE 冒烟测试
（`Mount -> kernel/FUSE -> session -> daemon`，无 FUSE 环境自动跳过），以及
Linux RemotePTY + FUSE 冒烟检查。

涉及代码/程序行为变更时，完成前跑 `make fmt` / `make lint` / `make test`。

## 6. 有哪些设计、限制、未来规划？

### 设计要点

- 由 daemon 发起连接，server 从不反向拨回用户机器。
- FUSE 是主集成面 —— 兼容目标是普通的、基于路径的文件语义，而非某个特定 agent。
- server 侧缓存与预取内建于架构中。
- 两个客户端角色（文件同步、终端）共享一份配置与一套协调生命周期，但保持**相互
  独立**的 gRPC 流。
- **日志写死、不可配置：** 两侧始终以 JSON 写出全部级别（debug 及以上）到
  `~/.rclaude/logs`（默认轮转），绝不写终端 —— 从而保持 PTY passthrough 干净。统一
  入口写 `rclaude.log`，server 写 `rclaude-server.log`。终端每个组件只显示一行状态。
- **PTY 行为写死：** 工作目录根等于 `fuse.mountpoint`（cwd 为
  `{mountpoint}/{user_id}/{project}`），固定环境透传白名单（`TERM`、`LANG`、
  `LC_ALL`、`LC_CTYPE`、`PATH`、`HOME`、`SHELL`、`CLAUDE_CONFIG_DIR`），帧上限
  65536 字节，优雅停止 5s，固定 attach/stdin 限流。
- **启动是协调的、不是竞速的：** PTY 首次 attach 会等 daemon 注册完成，而非以
  `daemon not connected` 失败；残余失败回退到事件总线重试（3 次，间隔 5s）。
- **关停是优雅的：** `SIGINT` / `SIGTERM` / `SIGHUP` 取消运行上下文，让在途文件流
  与 PTY 完成后再退出；第二次信号或 10s 宽限超时会强制立即退出。

### 当前限制

- server 必须跑在支持 FUSE 的 Linux 上。
- 认证目前是静态 token 映射，不是完整身份系统。
- 更适合小团队（约 1–20 人）。
- Rclaude 按设计不会把完整工作区镜像到 server。
- 断连处理仅支持临时只读缓存兜底，没有离线写回。
- 尚无内置 Docker 打包、systemd 单元、安装器或运维面板（TLS 已作为 `deploy/tls/` 下
  的可选前置代理方案提供）。
- 本仓库是可运行的最小路径，尚非完整生产发行版。

### 未来方向

- 拓宽平台/打包支持（容器、服务单元、安装器）。
- 超越静态 token 的更强身份体系。
- 超越只读兜底的更丰富断连处理。
- 运维面（指标、面板、审计管线）。

## 相关指引

- 英文 README：[README.md](README.md)
- 最小双机部署：[deploy/minimal/README.md](deploy/minimal/README.md)
- 可选 Caddy TLS：[deploy/tls/README.md](deploy/tls/README.md)
