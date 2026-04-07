# 远程文件访问系统架构说明

本文档用于承载远程文件访问系统的实现架构、技术选型和目录结构约束。

相关上游设计：

- [`PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)

相关执行计划：

- [`202604071046-phase3-server-fuse-mvp/`](/e:/Rclaude/docs/exec-plan/completed/202604071046-phase3-server-fuse-mvp/)
- [`202604071253-phase4a-write-ops/`](/e:/Rclaude/docs/exec-plan/completed/202604071253-phase4a-write-ops/)
- [`202604071605-phase4b-cache/`](/e:/Rclaude/docs/exec-plan/completed/202604071605-phase4b-cache/)
- [`202604070914-phase5-integration-test/`](/e:/Rclaude/docs/exec-plan/completed/202604070914-phase5-integration-test/)
- [`202604071013-phase6a-offline-readonly/`](/root/Rclaude/docs/exec-plan/completed/202604071013-phase6a-offline-readonly/)

## 一、实现架构

当前实现采用 `PLAN.md` 推荐的方案 A：基于 FUSE 的虚拟文件系统，作为主方案与 MVP 路径。

核心组件如下：

1. Local Daemon
2. gRPC 双向流
3. Server
4. Server 端执行环境

核心关系如下：

- Local Daemon 运行在用户本地，负责扫描工作区、上传文件、接收反向变更并回写本地。
- Server 运行在云端，负责连接管理、FUSE 挂载、会话路由、缓存和请求转发。
- Server 端执行环境通过 bash/powershell 等命令访问 `/workspace/{user_id}/` 下的虚拟工作区。
- Server 通过 FUSE 将读写请求转发给对应 Daemon，并在本地维护缓存与元数据。

FUSE 是当前主路径，不再作为后续可选升级项。

当前代码基线已完成：

- Phase 3：Server + FUSE MVP
- Phase 4a：写透与写操作主链路
- Phase 4b：服务端整文件内容缓存与失效
- Phase 5：跨平台 `inmem` 集成测试、Linux 真 FUSE 自动化冒烟与手动脚本入口
- Phase 6A：daemon 断线后的 TTL 限定离线只读降级

## 二、角色边界

- Server 不是某个特定 Agent 的代称。
- Server 是云端文件访问与同步服务，负责把用户本地文件映射到云端可访问工作区。
- “执行环境”表示在 Server 所在环境中发起文件访问的命令执行者，可以是 AI Agent，也可以是任何通过 bash/powershell 调用文件命令的流程。
- 系统的核心兼容目标不是某个具体模型，而是兼容基于文件路径执行 `cat`、`sed`、`grep`、`ls` 等命令的访问方式。

## 三、技术栈

| 类别 | 选型 | 理由 / 备注 |
|---|---|---|
| 语言 | Go 1.25.2 | `go.mod` 已固定 |
| RPC | `google.golang.org/grpc` + `google.golang.org/protobuf` | bidi stream 解决 NAT 反向连接 |
| Proto 生成 | `protoc-gen-go`, `protoc-gen-go-grpc` | 用 `tools/tools.go` 锁版本 |
| 文件监听 | `github.com/fsnotify/fsnotify` | 跨平台标准库 |
| 配置 | `github.com/spf13/viper` + YAML | 支持环境变量覆盖 |
| CLI | `github.com/spf13/cobra` | client/server 命令行 |
| 日志 | `log/slog` + `slog.JSONHandler` | 标准库，零额外运行时依赖 |
| 测试断言 | `github.com/stretchr/testify` | 项目约束要求 |
| 重试退避 | `github.com/cenkalti/backoff/v4` | 指数退避 |
| 并发原语 | `golang.org/x/sync/errgroup` | 多 goroutine 协同 |
| Glob/排除 | `github.com/bmatcuk/doublestar/v4` | gitignore 风格 |
| 限流 | `golang.org/x/time/rate` | 令牌桶 |
| 哈希 | `crypto/sha256` | 文件指纹 |
| Lint / Fmt | `golangci-lint v2.1.6`, `gofumpt v0.7.0`, `gci v0.13.5` | 由 Makefile 驱动 |
| FUSE | `github.com/hanwen/go-fuse/v2` | Server 端虚拟文件系统核心组件 |

## 四、目录结构约束

```text
E:\Rclaude\
├─ api/
│  └─ proto/remotefs/v1/
├─ app/
│  ├─ client/
│  └─ server/
├─ pkg/
│  ├─ config/
│  ├─ logx/
│  ├─ safepath/
│  ├─ fstree/
│  ├─ contentcache/
│  ├─ transport/
│  ├─ auth/
│  ├─ session/
│  ├─ syncer/
│  ├─ fusefs/
│  └─ ratelimit/
├─ internal/
│  ├─ inmemtest/
│  └─ testutil/
├─ tools/
├─ deploy/
├─ docs/
│  ├─ design/
│  │  └─ PLAN.md
│  ├─ exec-plan/
│  │  ├─ active/
│  │  └─ completed/
│  ├─ superpowers/
│  │  └─ specs/
│  ├─ reference/
│  ├─ ARCHITECTURE.md
│  └─ workflow.md
├─ Makefile
├─ .golangci.yml
├─ go.mod
└─ go.sum
```

目录职责约束如下：

- `api/` 只放协议源与生成产物。
- `app/` 只负责装配和命令入口，不承载业务逻辑。
- `pkg/` 中每个子包必须单一职责。
- `pkg/syncer/` 承载同步主逻辑。
- `pkg/fusefs/` 或等价模块承载 FUSE 文件系统实现。
- `pkg/contentcache/` 承载服务端整文件内容缓存。
- `internal/inmemtest/` 承载多用户、可故障注入的集成测试夹具。
- `internal/testutil/` 只放测试夹具。
- `docs/exec-plan/` 只放计划，不承载架构正文。

## 五、实现原则

- `api/` 只放协议源；生成产物可入库，避免 CI 强依赖 `protoc`。
- `pkg/` 内每个子包单一职责，便于并行开发和测试。
- `app/` 只做装配与命令行解析，不放业务逻辑。
- 路径在传输层统一使用 forward slash。
- FUSE 请求必须通过统一会话路由到对应 Daemon。
- 文件树缓存、内容缓存和预取是方案 A 的一等公民，而不是附加优化。
- 涉及缓存、预取、FUSE 等方案差异时，以 `PLAN.md` 为上位设计依据。

## 六、方案 A 的缓存说明

方案 A 下，缓存是系统设计的一部分，而不是可选增强。

- 文件树缓存用于支撑 `Lookup`、`Getattr`、`Readdir`
- 内容缓存用于降低重复 `Read` 的网络往返
- 缓存失效由 Daemon 变更事件驱动
- 小文件预取用于优化典型的 `ls` 后连续 `cat` 场景
- 断线期间已支持基于缓存的 TTL 限定只读降级策略

## 七、测试基线

当前自动化测试分为两层：

- 跨平台 `inmem` 集成测试：复用 `internal/inmemtest/`，覆盖读写回归、缓存命中/失效、多用户隔离、断线和超时
- Linux 真 FUSE 冒烟：直接走 `Mount -> kernel/FUSE -> workspaceNode -> session -> daemon` 真实链路；默认进入 `make test`，环境不满足时以 `skip` 处理

手动补验入口位于：

- `tools/fuse-smoke.sh`

该脚本用于对已挂载的 `/workspace/{user_id}` 视图执行最小 `ls` / `cat` / 写文件 / `mv` / `rm` 验证。
