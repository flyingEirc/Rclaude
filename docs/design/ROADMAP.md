# 远程文件访问系统 — 系统级 ROADMAP

> 本文档是系统级阶段划分与长期验收基线，属于 `docs/design/` 层。
> **不在此处跟踪任何阶段执行进度**；具体阶段进度看 `docs/exec-plan/{active|completed}/{时间}-{阶段名}/`。
> 上游设计：[`PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)
> 实现架构：[`../ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)

## 背景

系统方案统一采用方案 A：基于 FUSE 的虚拟文件系统。

- Server 在 `/workspace/{user_id}/` 暴露虚拟工作区
- Server 通过 FUSE 响应执行环境的文件访问
- Daemon 通过 gRPC 双向流提供真实文件数据与变更事件
- Server 维护文件树缓存、内容缓存与必要的预取能力

## 目标

交付一个可用的方案 A MVP，使 Server 端执行环境能够通过 `cat` / `sed` / `grep` / `ls` 等命令透明访问用户本地工作区文件。

## 范围

包含：

- gRPC 双向流协议与会话管理
- Daemon 工作区扫描、文件读取、增量变更上报
- Server 端 FUSE 挂载与请求路由
- 文件树缓存、内容缓存、基础预取
- 基本认证、超时、错误处理
- 单测、集成测试与基础交付物

不包含：

- 真实文件全量镜像到 Server
- 基于磁盘副本的双向同步
- 反向目录监听驱动的"Server 本地文件即真实工作区"模型

## 阶段与里程碑

| Phase | 主题 | 产出 | 里程碑验收 |
|---|---|---|---|
| Phase 0 | 骨架与工具链 | 可构建工程骨架、proto 工具锁、Makefile proto 目标 | `go build ./...` 通过、`make fmt/lint/test` 全绿 |
| Phase 1 | 协议与公共基础包 | 完整 remotefs proto、`config` / `logx` / `auth` / `safepath` / `fstree` | 包级单测通过 |
| Phase 2 | Daemon MVP | 启动、连接、文件树扫描、读文件、变更上报 | 能响应 mock server 请求 |
| Phase 3 | Server + FUSE MVP | 会话管理、FUSE 挂载、`Lookup` / `Getattr` / `Readdir` / `Open` / `Read` | `ls` / `cat` / `stat` 可走通 |
| Phase 4 | 缓存与写操作 | 文件树缓存、内容缓存、写透、`Write` / `Create` / `Mkdir` / `Rename` / `Unlink` | 读写路径稳定 |
| Phase 5 | 集成测试 | 端到端 + 异常场景 + 多用户隔离 | FUSE 路径访问可验证 |
| Phase 6 | 性能与安全优化 | 预取、限流、敏感文件过滤、离线降级 | 大部分读请求命中缓存 |
| Phase 7 | 部署与运维 | Docker、systemd、运行说明 | 可在 Linux Server 侧部署 |
| Phase 8 | Remote PTY | 交互式 PTY 主线 | 可从本地附着到 server 侧 PTY |

## 各 Phase 范围摘要

> 范围只描述"这个 Phase 应该交付什么"，不承载具体 Todo。具体 Todo / 模块拆分 / 验证命令在该 Phase 真正开工时写到 `docs/exec-plan/active/{时间}-{阶段名}/plan.md`。

### Phase 0 — 骨架与工具链
- 固定 proto 工具链与基础依赖
- 建立 `api/` / `app/` / `pkg/` / `internal/` / `tools/` / `deploy/` 基础目录
- 补齐 `Makefile` 中的 `proto`、`build`、`test`、`lint` 目标

### Phase 1 — 协议与公共基础包
- 定义 `remotefs` proto，覆盖文件树、读写、目录读取、属性、心跳
- 实现 `config`、`logx`、`auth`、`safepath`、`fstree`
- 上述包补齐基于 `testify` 的测试

### Phase 2 — Daemon MVP
- 启动、配置加载与 gRPC 建连
- 工作区扫描与初始文件树上报
- 按请求读文件、列目录、返回属性
- `fsnotify` 变更采集与增量推送

### Phase 3 — Server + FUSE MVP
- 会话注册与 user_id 路由
- 集成 `hanwen/go-fuse/v2`
- `Lookup` / `Getattr` / `Readdir` / `Open` / `Read`
- 在 `/workspace/{user_id}/` 挂载虚拟工作区

### Phase 4 — 缓存与写操作
- 文件树缓存、内容缓存、缓存失效
- `Write` / `Create` / `Mkdir` / `Rename` / `Unlink`
- 写透与超时控制

### Phase 5 — 集成测试
- 端到端：`ls` / `cat` / `grep` / 写文件 / 重命名 / 删除
- 断线、超时、缓存命中、缓存失效
- 多用户隔离与挂载目录隔离

### Phase 6 — 性能与安全优化
- 小文件预取
- 大文件限制与敏感文件过滤
- 上传/读取限流
- 离线降级只读策略

### Phase 7 — 部署与运维
- Docker 构建
- systemd 单元
- 部署与排障文档

### Phase 8 — Remote PTY
- 在 `RemoteFS` 文件面之外补齐交互式 PTY 主线，让用户可从本地附着到 server 侧 PTY
- 继续复用既有 token -> `user_id` 身份映射，并把远端工作目录约束在 `/workspace/{user_id}`
- 阶段边界：
  - Phase 8a：协议与基础库
  - Phase 8b：服务装配与联调
  - Phase 8c：CLI、smoke、deploy 示例与文档收口

## 关键风险

| 风险 | 说明 | 应对 |
|---|---|---|
| FUSE 部署能力 | Server 运行环境可能不支持 FUSE | 提前验证 `/dev/fuse`、挂载权限和容器 capability |
| 读取延迟 | 读文件链路经过 FUSE + gRPC + 本地文件系统 | 引入文件树缓存、内容缓存和预取 |
| 断线阻塞 | Daemon 断连时 FUSE 读写会阻塞 | 设置超时、离线只读和明确错误返回 |
| 大文件成本 | 大文件放大网络延迟与缓存压力 | 限制大小、分段读取、过滤二进制产物 |
| 并发写冲突 | 本地编辑和执行环境写入可能冲突 | 初版采用 last-write-wins，后续补冲突检测 |

## 系统级验收标准

- `go build ./...` 通过
- `make fmt`、`make lint`、`make test` 全绿
- FUSE 挂载成功
- `ls /workspace/{user_id}` 能列出远端工作区
- `cat /workspace/{user_id}/...` 能读取本地真实文件内容
- 写文件、重命名、删除可通过 FUSE 路径反映到本地工作区
- Daemon 断开时，系统能返回明确错误或只读降级

## 执行约束

- 开发前以 `PLAN.md` + 本文件为准，不得回退到方案 B
- 任何阶段的 Todo / 验证 / 偏离记录都必须落到 `docs/exec-plan/active/{时间}-{阶段名}/`
- 新增测试统一使用 `testify/assert` 或 `testify/require`
- 每个阶段完成后必须执行 `make fmt`、`make lint`、`make test` 并把摘要写进同名完成文档
- 若方案 A 在部署条件上被阻塞，再单独新建一个阶段评估回退方案，不在本 ROADMAP 内混用
