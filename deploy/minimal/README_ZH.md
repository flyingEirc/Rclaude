# 最小测试闭包

Language: 中文 | [English](README.md)

用于真实远程/本地 Rclaude 联调的最小可用流程：

- `rclaude-server` 跑在一台远程 Linux 机器上（需支持 FUSE），
- 统一入口 `rclaude`（daemon + 远端 claude PTY attach）跑在你的本地机器上，
  并反向连接到该 server。

它刻意不是生产交付物：没有 Docker、`systemd`、TLS 或安装器。固定的连接信息（远程 IP、
SSH key、token）来自 `rclaude-remote-local-test` skill，存放在 `.list/`（已 gitignore）。

## 拓扑

- **Server 机器** —— 远程 Linux，通过 FUSE 挂载 `/workspace/<user_id>` 并在其中启动 PTY
  进程：默认是用户的登录 shell，或在配置了 `pty.binary`（如真正的 `claude`）时启动固定程序。
  必须是带 `/dev/fuse` 的 Linux，无法跑在 macOS 上。
- **本地机器** —— 运行 `rclaude`，它启动 daemon（把你本地工作区暴露给 server）并把终端
  attach 到远端 PTY。客户端一侧不需要 FUSE，所以 macOS 也可以。

`RemotePTY.Attach` 只在 `rclaude` 与 server 端 PTY 进程之间传输终端字节/尺寸变更/退出码。
`RemoteFS.Connect` + FUSE 暴露 `/workspace/<user_id>`，并把文件操作重定向回你的 daemon 工作区。
Server 在 `/workspace/<user_id>` 内启动 PTY 进程 —— 默认是登录 shell，或配置了的 `pty.binary`。
当你固定成 `claude` 时，**server** 机器必须装好它、在 `PATH` 上（或用绝对路径），并且已经
为 server 的 OS 用户登录过 —— 本地的 Claude 登录态不会在 server 侧复用。

## 文件

| 文件 | 作用 |
| --- | --- |
| `start-server.sh` | 把当前 `app/server` 交叉编译成 linux/amd64，连同 server 配置一起送到 `<remote>:/etc/rclaude`，并在远程（重）启动 `rclaude-server`。 |
| `start-rclaude.sh` | 在本地前台运行统一入口 `rclaude`（daemon + pty）。需要交互式 TTY。 |
| `preflight-server.sh` | 只读检查 server 侧前置条件（Linux、`/dev/fuse`、绝对 mountpoint、`pty.binary`）。在 server 上运行。 |
| `preflight-daemon.sh` | 只读检查本地前置条件（`server.address`/`token`、绝对 `workspace.path`、`rclaude` 二进制，可选 TCP/TTY 检查）。 |
| `server.example.yaml` / `daemon.example.yaml` | 已提交的模板，记录每个字段。 |
| `server.test.yaml` / `daemon.test.yaml` | **已 gitignore**（`*.test.yaml`），携带真实远程 IP + token 的测试配置。绝不提交。 |

## 一次性准备

1. 编译本地统一入口：

```sh
go build -o ./bin/rclaude ./app/rclaude
```

2. 从 `.list/token.yaml` 生成两份 gitignore 的测试配置（已生成过一次；token 轮换后需重新生成）。
   `server.test.yaml` 使用 `listen: ":7969"` 和 `auth.tokens: { <token>: <user_id> }`；
   `daemon.test.yaml` 使用 `server.address: "69.63.208.133:7969"`、同一个 token，以及一个
   绝对的本地 `workspace.path`。

`start-server.sh` 会替你交叉编译并部署 server，所以你不用手动编译 `rclaude-server`。

## 测试流程

### 1. Preflight

本地（daemon 侧）：

```sh
RCLAUDE_PREFLIGHT_CHECK_SERVER=1 sh ./deploy/minimal/preflight-daemon.sh ./deploy/minimal/daemon.test.yaml
```

Server 侧（可选，把脚本 + 配置拷到远程后运行）：

```sh
sh ./deploy/minimal/preflight-server.sh /etc/rclaude/server.test.yaml
```

在启动任何东西之前，先修掉每一个报告出来的 error。

### 2. 启动 server

```sh
sh ./deploy/minimal/start-server.sh ./deploy/minimal/server.test.yaml
```

预期：脚本完成构建、送到 `<remote>:/etc/rclaude`、以分离方式重启 `rclaude-server`，
并打印 `remote: rclaude-server running (pid …)`。用它成功时打印的命令去 tail 远程日志。
**成功判据：server 已起来并在监听。**

### 3. 启动本地入口

在一个真实的交互式终端里：

```sh
sh ./deploy/minimal/start-rclaude.sh ./deploy/minimal/daemon.test.yaml
```

预期：先打印 `daemon started`，再打印 `pty started`，然后你落入 `/workspace/<user_id>`
下的远端 claude PTY 会话。其它一切都写进 `rclaude.log`（默认 `~/.rclaude/logs`）。
**成功判据：两个组件都启动，且 PTY attach 成功。** `Ctrl+C` / 正常退出会结束会话并
带出远端退出码。

## 排障

- `server config does not exist`：跑一次性准备，创建 `deploy/minimal/server.test.yaml`。
- `ssh key not found`：确认 `.list/server_private_key` 存在（权限 `600`）。
- `remote: rclaude-server failed to start`：读被 tail 出来的远程日志；通常是 `/dev/fuse`
  被占用、`/workspace` 残留挂载，或 mountpoint 缺失。
- 本地 `daemon start failed` / `pty start failed`：server 不可达、token 映射到了错误的
  `user_id`，或 server 侧的 `pty.binary` 缺失。带上 `RCLAUDE_PREFLIGHT_CHECK_SERVER=1`
  重跑 preflight。
- PTY attach 上了但 `claude` 行为异常：检查 **server** 侧 Claude 的安装、登录态、`PATH`，
  以及 `HOME`/`SHELL`/`CLAUDE_CONFIG_DIR` 是否在 `pty.env_passthrough` 里。若只想验证
  传输链路，可临时把 server 的 `pty.binary` 设成 `/bin/sh`。
- 从同步/挂载的工作区运行时丢了可执行位：按上面那样用 `sh` 来调用脚本。
