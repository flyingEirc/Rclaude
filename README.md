# Rclaude

`Rclaude` 是一个远程文件访问系统。它的目标不是“同步一份代码副本到云端”，而是让云端执行环境直接通过普通文件路径访问用户本地工作区。

对执行侧来说，文件依然表现为“本地文件系统上的真实路径”；对系统来说，真正的数据来源仍然是用户本地机器上的工作区。

典型场景：

- 云端 Agent / 任务执行器运行 `cat /workspace/alice/main.go`
- Server 侧 FUSE 文件系统接管该路径访问
- 请求通过 gRPC 双向流转发到 Alice 本地 daemon
- daemon 读取 Alice 本地真实文件并返回内容

这样可以兼容现成的 `cat`、`sed`、`grep`、`ls`、`stat` 等 shell 工具，而不需要改造工具调用方式。

## 这个项目当前已经实现了什么

当前代码基线已经具备一条可工作的主链路：

- 本地 `rclaude-daemon` 启动后扫描工作区，并通过 gRPC 双向流主动连接 Server
- Server 维护每个用户的会话、文件树元数据和内容缓存
- Server 在 Linux 上通过 FUSE 暴露 `/workspace/{user_id}/` 虚拟工作区
- 执行环境可直接对该挂载路径执行文件命令，无需专用 SDK 或专用 CLI

当前已实现能力包括：

- 读操作：`Lookup` / `Getattr` / `Readdir` / `Open` / `Read`
- 写操作：创建、覆盖写、偏移写、追加写、`mkdir`、`rename`、`delete`、`truncate`
- 多用户隔离：每个用户独立挂载到 `/workspace/{user_id}/`
- 文件树缓存：支撑目录浏览与属性查询
- 整文件内容缓存：降低重复读取时的网络往返
- 小文件预取：目录读取后预热高概率被继续访问的小文件
- 离线只读降级：daemon 断线后，在 TTL 窗口内允许基于缓存只读访问
- 敏感文件过滤：默认隐藏 `.env`、私钥、证书等敏感文件，并拒绝相关写操作
- daemon 侧读写字节限流：限制读回与落盘的字节速率
- 工作区边界保护：路径校验、防路径穿越、限制只访问指定 workspace
- 配置加载与环境变量覆盖：基于 YAML + `RCLAUDE_*` 环境变量
- 静态 token 鉴权：token 映射到 `user_id`

## 架构概览

系统由三部分组成：

1. `rclaude-daemon`
2. `rclaude-server`
3. Server 所在环境中的普通 shell / Agent / 自动化任务

核心数据流如下：

```text
本地工作区
    ^
    | 读写文件 / 监听变更
    v
rclaude-daemon
    ^
    | gRPC 双向流
    v
rclaude-server
    ^
    | FUSE 挂载
    v
/workspace/{user_id}/...
    ^
    | cat / sed / grep / ls / stat / mv / rm ...
    v
执行环境
```

设计重点：

- Daemon 主动连 Server，避免要求 Server 反向连接用户本地机器
- FUSE 是主方案，不是兼容层
- Server 端缓存和预取是架构内的一等能力，不是附加优化
- 执行环境的兼容目标是“普通文件路径语义”，而不是某个特定模型或 Agent

更完整的背景与方案对比见：

- [docs/design/PLAN.md](docs/design/PLAN.md)
- [docs/design/ROADMAP.md](docs/design/ROADMAP.md)
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

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
app/client/             rclaude-daemon 命令入口
app/server/             rclaude-server 命令入口
pkg/config/             YAML / 环境变量配置加载
pkg/auth/               token 鉴权
pkg/safepath/           工作区路径校验与边界保护
pkg/fstree/             文件树元数据索引
pkg/session/            Server 侧用户会话与请求路由
pkg/contentcache/       Server 侧整文件内容缓存
pkg/fusefs/             FUSE 文件系统视图
pkg/syncer/             daemon 侧同步、扫描、监听、请求处理
pkg/transport/          gRPC 连接与 stream 封装
pkg/ratelimit/          daemon 侧字节限流
internal/inmemtest/     in-memory 端到端测试夹具
docs/                   设计、架构、工作流、执行记录
```

## 构建

先安装本仓库约定的开发工具：

```bash
make tools
```

编译两个主程序：

```bash
go build -o ./bin/rclaude-server ./app/server
go build -o ./bin/rclaude-daemon ./app/client
```

也可以直接做全仓库构建检查：

```bash
go build ./...
```

## 快速启动

### 1. 准备 Server 配置

示例 `server.yaml`：

```yaml
listen: ":9326"
auth:
  tokens:
    "tok-alice": "alice"
fuse:
  mountpoint: "/workspace"
cache:
  max_bytes: 268435456
prefetch:
  enabled: true
  max_file_bytes: 102400
  max_files_per_dir: 16
request_timeout: 10s
offline_readonly_ttl: 5m
log:
  level: "info"
  format: "text"
```

说明：

- `listen`：gRPC 监听地址
- `auth.tokens`：`token -> user_id` 映射
- `fuse.mountpoint`：绝对路径，Server 会在此处挂载工作区根目录
- `cache.max_bytes`：Server 侧内容缓存大小，`0` 表示关闭
- `prefetch.*`：目录读取后的预取策略
- `request_timeout`：单次文件请求超时
- `offline_readonly_ttl`：daemon 断线后缓存只读保留时长

### 2. 准备 daemon 配置

示例 `daemon.yaml`：

```yaml
server:
  address: "127.0.0.1:9326"
  token: "tok-alice"
workspace:
  path: "/absolute/path/to/workspace"
  exclude:
    - ".git"
    - "node_modules"
    - "vendor"
  sensitive_patterns:
    - "secrets/**"
rate_limit:
  read_bytes_per_sec: 0
  write_bytes_per_sec: 0
self_write_ttl: 2s
log:
  level: "info"
  format: "text"
```

说明：

- `server.address`：Server 地址
- `server.token`：与 Server 侧 `auth.tokens` 中某个 token 对应
- `workspace.path`：必须是绝对路径
- `workspace.exclude`：扫描和监听时排除的路径模式
- `workspace.sensitive_patterns`：在默认敏感规则之外追加的敏感路径模式
- `rate_limit.read_bytes_per_sec`：daemon 返回读取内容的字节速率限制，`0` 为关闭
- `rate_limit.write_bytes_per_sec`：daemon 落盘写入的字节速率限制，`0` 为关闭
- `self_write_ttl`：用于抑制 daemon 自写回产生的回环监听事件

### 3. 启动

先启动 Server：

```bash
./bin/rclaude-server --config ./server.yaml
```

再启动 daemon：

```bash
./bin/rclaude-daemon --config ./daemon.yaml
```

启动成功后，Server 侧会出现：

```text
/workspace/alice/
```

此时执行环境可直接读取：

```bash
ls -la /workspace/alice
cat /workspace/alice/README.md
grep -R "TODO" /workspace/alice
```

如果走的是写操作链路，也会回写到本地真实工作区，例如：

```bash
mkdir /workspace/alice/tmp
printf 'hello\n' > /workspace/alice/tmp/demo.txt
mv /workspace/alice/tmp/demo.txt /workspace/alice/tmp/demo2.txt
truncate -s 2 /workspace/alice/tmp/demo2.txt
rm /workspace/alice/tmp/demo2.txt
```

## 环境变量覆盖

配置通过 `viper` 加载，支持 `RCLAUDE_*` 环境变量覆盖。

例如：

```bash
export RCLAUDE_SERVER_ADDRESS=127.0.0.1:9999
./bin/rclaude-daemon --config ./daemon.yaml
```

点号会自动映射成下划线，所以：

- `server.address` -> `RCLAUDE_SERVER_ADDRESS`
- `fuse.mountpoint` -> `RCLAUDE_FUSE_MOUNTPOINT`

## 测试与开发命令

本仓库约定的标准流程：

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

## 文档入口

- 设计总方案：[docs/design/PLAN.md](docs/design/PLAN.md)
- 系统级路线图：[docs/design/ROADMAP.md](docs/design/ROADMAP.md)
- 当前实现架构：[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 开发工作流：[docs/workflow.md](docs/workflow.md)
- 文档治理说明：[docs/README.md](docs/README.md)
- 已完成阶段记录：[docs/exec-plan/completed/README.md](docs/exec-plan/completed/README.md)

如果你要继续开发这个项目，建议按下面顺序阅读：

1. `docs/design/PLAN.md`
2. `docs/design/ROADMAP.md`
3. `docs/ARCHITECTURE.md`
4. `docs/workflow.md`
5. 最新的 `docs/exec-plan/completed/{时间}-{阶段名}/`
