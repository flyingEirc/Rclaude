# Phase 7 — 双机最小部署闭环与冒烟验证设计

> 本文档是 Phase 7 当前子题的设计 spec，不承担实施计划。
> 实施计划落在 `docs/exec-plan/active/{时间}-phase7-minimal-deploy-smoke/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](../../design/PLAN.md)、[`docs/design/ROADMAP.md`](../../design/ROADMAP.md) Phase 7
> 实现架构：[`docs/ARCHITECTURE.md`](../../ARCHITECTURE.md)
> 上一阶段：[`docs/exec-plan/completed/202604081419-phase6d-rate-limit/`](../../exec-plan/completed/202604081419-phase6d-rate-limit/)

## 0. 范围与目标

当前仓库已经完成协议、daemon、server、FUSE、写操作、缓存、预取、敏感过滤、离线只读与字节限流，但 `deploy/` 仍是空目录，Phase 7 的“部署与运维”还没有任何可直接拿来跑双机闭环的最小交付物。

本阶段不追求生产部署完整度，而是优先补齐一套**双机最小可验证闭环**：

1. 在 Linux Server 机器上启动 `rclaude-server`
2. 在另一台机器上启动 `rclaude-daemon`
3. 在 Server 机器上通过 `/workspace/{user_id}` 验证远端工作区可见、可读、可写、可重命名、可删除

本阶段目标是：**给仓库增加一组最小部署模板、启动脚本和冒烟脚本，使双机场景能以最少步骤跑通。**

范围内：

- `deploy/minimal/README.md`
- `deploy/minimal/server.example.yaml`
- `deploy/minimal/daemon.example.yaml`
- `deploy/minimal/start-server.sh`
- `deploy/minimal/start-daemon.sh`
- `deploy/minimal/smoke-remote.sh`
- 必要的可执行权限与脚本注释
- 对 README 与脚本的自动化最小验证

范围外：

- Dockerfile
- systemd 单元
- TLS / 证书发放
- 安装器或包管理
- 多用户批量部署
- 跨平台代理包装脚本
- 生产级日志轮转、监控与告警

设计原则：

- 只交付第一次跑通双机链路所需的最小集合
- 脚本保持短小、可读、可复制，不做复杂 CLI
- README 明确区分 Server 机器和 Daemon 机器步骤
- 冒烟脚本只验证核心链路，不引入测试框架依赖
- 所有产物都要能作为后续 Docker/systemd 的基础，而不是一次性 throwaway 文件

## 1. 目标场景

### 1.1 部署形态

本阶段默认场景：

- **Server 机器**：Linux，具备 FUSE 条件，运行 `rclaude-server`
- **Daemon 机器**：任意受支持平台，运行 `rclaude-daemon`
- **执行/验证位置**：Server 机器，直接操作 `/workspace/{user_id}`

这与真实架构一致：

- daemon 主动连接 server
- server 通过 FUSE 暴露 `/workspace/{user_id}`
- shell 命令直接在 server 侧读写该挂载路径

### 1.2 最小验证路径

最小闭环只验证 5 个动作：

1. `ls /workspace/{user_id}`
2. `cat /workspace/{user_id}/{expected_file}`
3. `echo "..." > /workspace/{user_id}/.rclaude-smoke-*`
4. `mv`
5. `rm`

如果这 5 个动作成立，说明以下主链路已经打通：

- daemon 建连认证
- 初始文件树同步
- server 侧 FUSE 挂载
- 读路径
- 写路径
- rename / delete 路径

## 2. 交付结构

新增目录固定为：

```text
deploy/minimal/
├── README.md
├── server.example.yaml
├── daemon.example.yaml
├── start-server.sh
├── start-daemon.sh
└── smoke-remote.sh
```

约束：

- 文件名固定，避免 README 和脚本引用漂移
- 不在本阶段额外拆子目录
- 样例配置保持 `.example.yaml`
- 脚本统一 POSIX shell，避免 bash 高级特性过多

## 3. 文件职责

### 3.1 `server.example.yaml`

只提供 Linux Server 机器的最小样例配置，字段收敛为：

```yaml
listen: ":9326"
auth:
  tokens:
    "tok-demo": "demo"
fuse:
  mountpoint: "/workspace"
log:
  level: "info"
  format: "text"
```

要求：

- 示例值必须能直接作为 README 命令示例使用
- token -> user_id 的映射要清晰，方便和 smoke 参数对应
- 不在本阶段加入大量可选性能配置，避免样例被配置噪声淹没

### 3.2 `daemon.example.yaml`

只提供 Daemon 机器的最小样例配置，字段收敛为：

```yaml
server:
  address: "SERVER_IP:9326"
  token: "tok-demo"
workspace:
  path: "/absolute/path/to/workspace"
  exclude:
    - ".git"
    - "node_modules"
    - "vendor"
log:
  level: "info"
  format: "text"
```

要求：

- `SERVER_IP` 明确提示需要替换
- `workspace.path` 必须提示使用绝对路径
- `exclude` 与 README 保持一致

### 3.3 `start-server.sh`

职责：

- 接收单一配置文件路径参数
- 检查 `rclaude-server` 二进制是否存在
- 检查配置文件是否存在
- 提示挂载点 / 监听地址来自配置
- 直接执行 `rclaude-server --config <path>`

明确不做：

- 自动安装 FUSE
- 自动创建 systemd
- 自动生成配置
- 守护化 / nohup / 日志轮转

原因是这些能力都已经超出“最小闭环”，而且把进程前台运行更利于第一次排障。

### 3.4 `start-daemon.sh`

职责：

- 接收单一配置文件路径参数
- 检查 `rclaude-daemon` 二进制是否存在
- 检查配置文件是否存在
- 直接执行 `rclaude-daemon --config <path>`

明确不做：

- 平台安装逻辑
- 开机自启
- 后台守护

### 3.5 `smoke-remote.sh`

目标是让 Server 机器用一个脚本完成最小链路验证。

建议接口：

```bash
./deploy/minimal/smoke-remote.sh <user_id> <expected_file>
```

执行内容固定为：

1. 检查 `/workspace/<user_id>` 是否存在
2. `ls -la /workspace/<user_id>`
3. `cat /workspace/<user_id>/<expected_file>`
4. 在挂载目录下创建 `.rclaude-smoke-<timestamp>.txt`
5. 重命名为 `.rclaude-smoke-<timestamp>.moved.txt`
6. 删除该文件
7. 输出成功摘要

设计约束：

- 失败即退出（`set -eu`）
- 不使用项目测试框架
- 不依赖 git
- 写入文件名必须足够明确且低冲突

### 3.6 `README.md`

README 只解决一个问题：**第一次双机跑通时按什么顺序操作。**

结构建议：

1. 前提条件
2. 构建二进制
3. Server 机器步骤
4. Daemon 机器步骤
5. 冒烟验证
6. 常见失败排查

README 必须明确说明：

- Server 机器要有 FUSE
- daemon 机器要能访问 server 的 `<ip>:<port>`
- smoke 脚本在 Server 机器执行

## 4. 数据流与执行顺序

### 4.1 Server 机器

```text
go build -o bin/rclaude-server ./app/server
cp deploy/minimal/server.example.yaml /etc/rclaude/server.yaml
编辑 token / mountpoint / listen
./deploy/minimal/start-server.sh /etc/rclaude/server.yaml
```

### 4.2 Daemon 机器

```text
go build -o bin/rclaude-daemon ./app/client
cp deploy/minimal/daemon.example.yaml ./daemon.yaml
编辑 server.address / token / workspace.path
./deploy/minimal/start-daemon.sh ./daemon.yaml
```

### 4.3 回到 Server 机器验证

```text
./deploy/minimal/smoke-remote.sh demo README.md
```

如果验证通过，说明 `/workspace/demo/README.md` 已能被 server 侧 shell 直接访问。

## 5. 错误与排障口径

README 中至少要覆盖以下最小排障项：

### 5.1 Server 启动失败

- `/dev/fuse` 不存在
- 挂载点已有 stale FUSE 挂载
- 监听端口被占用
- 配置文件路径错误

### 5.2 Daemon 无法连上 Server

- `server.address` 不可达
- token 不匹配
- server 未启动
- 本地工作区路径不是绝对路径

### 5.3 Smoke 失败

- `/workspace/<user_id>` 不存在：通常是 daemon 未认证成功或初始文件树未注册
- `cat` 失败：通常是 `expected_file` 不存在或工作区路径不对
- 写 / mv / rm 失败：检查 daemon 工作区权限与 server/daemon 日志

## 6. 测试策略

本阶段不新增新的端到端测试框架，而是用“脚本正确性 + 仓库门禁”组合验证。

### 6.1 静态与脚本级验证

至少覆盖：

- `shellcheck` 不作为强依赖，但脚本语法必须可通过 `sh -n`
- README 中的命令与脚本参数保持一致
- 样例 YAML 能被现有配置加载器读取

### 6.2 仓库验证

本阶段完成前仍执行：

- `make fmt`
- `make lint`
- `make test`

并额外执行：

- `sh -n deploy/minimal/start-server.sh`
- `sh -n deploy/minimal/start-daemon.sh`
- `sh -n deploy/minimal/smoke-remote.sh`

### 6.3 冒烟口径

如果当前环境允许，可用现有真实 `/workspace/{user_id}` 路径手动执行一次 `smoke-remote.sh`，但这不是 Phase 7 文档交付的唯一验收标准。当前阶段以“产物齐全、语义自洽、脚本语法正确、仓库门禁通过”为主。

## 7. 验收标准

以下 5 项同时满足才算完成：

1. `deploy/minimal/server.example.yaml` 和 `daemon.example.yaml` 可直接复制修改
2. `start-server.sh` 与 `start-daemon.sh` 能以“单一配置路径参数”启动对应程序
3. `smoke-remote.sh <user_id> <expected_file>` 覆盖 `ls` / `cat` / 写 / `mv` / `rm`
4. `README.md` 明确写出双机顺序：Server 机器 -> Daemon 机器 -> 回到 Server 机器 smoke
5. `make fmt`、`make lint`、`make test` 通过

## 8. 非目标

以下事项明确不在本阶段：

- Dockerfile
- systemd unit
- TLS
- 真正的安装器
- 多用户批量部署
- 生产级运维文档

## 9. 后续衔接

本阶段交付完成后，后续可以自然沿两条路径扩展：

1. **运维包装化**
   - Docker
   - systemd
   - 日志与进程管理

2. **部署强化**
   - TLS
   - 更明确的配置分层
   - 多用户与多 daemon 部署说明

因此本阶段的设计关键不是“尽可能多做”，而是先给仓库补上一套真实可跑通的最小双机入口。
