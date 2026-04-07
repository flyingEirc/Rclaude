# Phase 5 — 集成测试与 Linux 真 FUSE 冒烟设计

> 本文档是 Phase 5 的设计 spec，不承担实施计划。
> 实施计划落在 `docs/exec-plan/active/{时间}-phase5-integration-test/plan.md`。
>
> 上游设计：[`docs/design/PLAN.md`](/e:/Rclaude/docs/design/PLAN.md)、[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 5
> 上一阶段：[`docs/exec-plan/completed/202604071605-phase4b-cache/`](/e:/Rclaude/docs/exec-plan/completed/202604071605-phase4b-cache/)
> 实现架构：[`docs/ARCHITECTURE.md`](/e:/Rclaude/docs/ARCHITECTURE.md)

## 0. 范围与原则

Phase 5 的目标不是继续扩展 remotefs 核心能力，而是把 Phase 0~4 已经落地的读写、缓存和路由逻辑放进一套可重复、可验收、可定位故障的测试体系。

本阶段采用“两层自动化 + 一层手动补验”：

- 跨平台 `inmem` 集成测试作为主验收层
- Linux 真 FUSE 自动化冒烟作为真实路径补充
- Linux 手动脚本作为部署前和现场排障入口

### 目标

- 把 `ls` / `cat` / `grep` 风格读路径、写透路径、缓存命中与失效、多用户隔离做成稳定回归集
- 用测试专用 fault hooks 确定性触发断线和超时，而不是依赖随机 timing
- 让 Linux 环境下的 `make test` 默认包含真 FUSE 冒烟，但在缺少 `/dev/fuse` 或挂载权限时明确 `skip`

### 范围内

- `internal/inmemtest` 升级为多用户、可注入故障的测试 harness
- `pkg/fusefs` 下的跨平台端到端测试扩展
- Linux 专用真 FUSE 冒烟测试
- Linux 手动验收脚本/文档入口
- `make test` 默认纳入上述自动化测试

### 范围外

- Phase 6 的离线只读降级、预取与性能压测
- 新协议字段或新的生产功能开关
- 为了测试去重构生产路径边界
- 把真 FUSE 自动化扩大为完整故障注入平台

### 设计原则

- `inmem` 负责覆盖度和确定性，真 FUSE 负责真实路径冒烟
- fault hooks 只放测试夹具，不把测试分支渗进生产主链路
- Linux 真 FUSE 冒烟默认进入 `make test`，但环境不满足时 `skip` 而不是失败
- 多用户隔离必须在同一个 `session.Manager` 上验证，避免“单用户重复跑两次”伪装成隔离测试

## 1. 顶层验收结构

```text
make test
  -> 跨平台包级测试
  -> pkg/fusefs/inmem 集成测试
       覆盖读 / 写 / 缓存 / 多用户 / 断线 / 超时
  -> pkg/fusefs/linux 真 FUSE 冒烟测试
       启动前先探测 FUSE 环境
       满足条件 -> 真挂载并做最小读写验证
       不满足条件 -> t.Skip 并给出明确原因
```

三层职责拆分如下：

1. `inmem` 集成测试
   用于覆盖主行为矩阵，是 Phase 5 的主验收面。

2. Linux 真 FUSE 自动化冒烟
   用于验证 `Mount -> kernel/FUSE -> workspaceNode -> session -> daemon` 真实链路至少可走通。

3. Linux 手动脚本
   用于开发机、测试机或部署机做快速复验，口径与自动冒烟保持一致。

## 2. 测试夹具架构

### M1 — `internal/inmemtest` 升级为 harness

当前 `Start(t, "")` 只适合单用户 happy-path。Phase 5 改成两层模型：

- `Harness`
  - 持有共享 `session.Manager`
  - 持有多个用户 daemon handle
  - 统一 cleanup
- `UserHandle`
  - 绑定一个 `user_id`
  - 持有自己的 daemon 根目录、stream、session、请求计数、fault hooks

保留现有 `Start(t, "")` 作为单用户快捷入口，但其底层改为：

```text
NewHarness(t, HarnessOptions)
  -> AddUser(UserOptions)
  -> 返回兼容旧 Pair 语义的包装
```

这样可以在不破坏现有 Phase 4 测试调用方式的前提下，引入多用户和故障注入能力。

### M2 — fault hooks

fault hooks 不进入生产代码，只挂在测试夹具的 daemon 请求处理循环上。

建议接口语义如下：

- `BeforeHandle(req) Action`
- `AfterHandle(req, resp) Action`

其中 `Action` 只需要覆盖测试真正用到的几种结果：

- `Pass`：正常走 `syncer.Handle`
- `Delay(d time.Duration)`：延迟响应，用于确定性触发 timeout
- `DropConnection(err)`：主动结束 stream，用于触发 offline / session failed
- `Respond(resp)`：直接返回自定义错误响应

### M3 — 可观测性

为了让缓存命中、断线和超时可断言，harness 需要最小可观测能力：

- 按用户统计 `ReadFileReq` 次数
- 记录最后一次请求类型与路径
- 能主动向 session 推送 daemon `FileChange`
- 能等待 session 树更新到目标状态

这些能力只服务测试断言，不暴露给生产包。

## 3. 场景矩阵

### 3.1 `inmem` 主套件

主套件按行为分组，不按实现文件分组。

#### 读路径

- 目录列举后读取文件内容
- 同一路径重复读只首读下发 `ReadFileReq`
- daemon `Change` 后缓存失效并重新拉取
- 写后再次读取命中新内容，不读到旧缓存

#### 写路径回归

- `Create` / `Write` / `Truncate`
- `Mkdir` / `Rename` / `Unlink` / `Rmdir`
- 跨目录 rename
- 写路径完成后 session 树立刻可见

#### 多用户隔离

- 同一 manager 下挂两个用户，各自拥有同名文件时互不串读
- 根目录用户列表只暴露已注册用户
- A 用户 `Change` / 写操作不会污染 B 用户的 tree 或 content cache
- 跨用户 rename / 访问保持错误语义不变

#### fault hooks 异常

- 读请求延迟超过 `RequestTimeout` -> `ETIMEDOUT`
- 写请求延迟超过 `RequestTimeout` -> `ETIMEDOUT`
- 请求处理中主动断开 stream -> `EIO`
- 断线后新请求不再阻塞，并返回明确错误

这里的“`grep` 风格读路径”不需要真的启动 shell，只要通过分段 `readChunk` 和目录读取覆盖其依赖的读取模式即可。

### 3.2 Linux 真 FUSE 自动化冒烟

Linux 冒烟只保留真实挂载最有价值的最小集合：

- 挂载成功
- `/mountpoint/{user_id}` 可见
- 读取一个真实文件
- 写入并回读
- rename 后新旧路径行为正确
- 删除后路径消失

不在真 FUSE 层做 fault injection、多用户组合爆炸或缓存计数断言。这些都由 `inmem` 主套件负责。

### 3.3 Linux 手动脚本

手动脚本覆盖与自动冒烟相同的最小集合：

- mount
- `ls`
- `cat`
- 写文件
- `mv`
- `rm`

脚本用途是：

- 开发机快速验收
- CI 或部署机排障时人工复现
- FUSE 权限环境不稳定时给出统一的人工补验入口

## 4. Linux 真 FUSE 测试策略

### 4.1 默认进入 `make test`

真 FUSE 冒烟测试使用 Linux build tag，默认被 `go test ./...` 收进来；不再依赖 `RUN_FUSE_TESTS=1` 之类开关。

### 4.2 环境探测与 skip 规则

测试开始先做环境探测：

- 当前系统是 Linux
- `/dev/fuse` 存在且可访问
- 临时挂载目录可创建
- 调用 `Mount(...)` 时若遇到权限不足、设备不可用、内核不支持等环境阻塞，转为 `t.Skip`

这里的关键点是：**挂载失败不一律当功能失败。**

只有在“环境已满足且挂载已成功”之后，后续读写断言失败才算代码回归。

### 4.3 自动冒烟执行模型

测试中直接启动一套最小 in-process server/daemon 对：

- daemon 侧仍可复用 `internal/inmemtest` 或其底层部件
- server 侧通过 `Mount(...)` 真挂到临时目录
- 文件操作用标准库 `os.ReadFile`、`os.WriteFile`、`os.Rename`、`os.Remove` 等完成

这样可以避免 shell 依赖和脚本解析噪音，让自动测试只聚焦文件系统语义。

## 5. 手动验收入口

手动入口建议落在 `tools/`：

- `tools/fuse-smoke.sh`

脚本约束：

- 只依赖标准 shell 与 coreutils
- 退出码严格反映失败
- 输出每一步正在验证什么
- 对挂载点、user_id 和测试文件名使用临时变量，避免污染真实工作区

文档入口建议写进对应阶段的 `开发流程.md` 完成摘要，并在脚本头部注释中标明用途与前置条件。

## 6. 模块变更边界

### M1 — `internal/inmemtest`

- 新增 harness / user handle / options / fault hooks
- 保留单用户快捷 API，避免现有测试全量重写
- 增强请求计数、change 推送和等待工具

### M2 — `pkg/fusefs`

- 扩展或拆分 `inmem_e2e_test.go`
- 新增 Linux 真 FUSE 冒烟测试文件
- 视需要补少量测试 helper，但不改生产路径对外语义

### M3 — `tools/`

- 新增 Linux 手动冒烟脚本

### M4 — `docs/exec-plan/active/{时间}-phase5-integration-test/`

- `plan.md`
- `开发流程.md`
- `测试错误.md`

## 7. 风险与应对

| 风险 | 说明 | 应对 |
|---|---|---|
| 真 FUSE 环境波动 | 某些 Linux 机器缺少 `/dev/fuse` 或挂载权限 | 自动测试先探测环境，不满足则 `skip` |
| 测试不稳定 | 用真实时间等待会引入 flaky | fault hooks 直接控制延迟和断线，避免随机 timing |
| 多用户测试伪隔离 | 若每个用户单独一套 manager，会掩盖路由问题 | 多用户场景必须在同一 manager 上验证 |
| 测试代码侵入生产路径 | 为了注入故障去改生产逻辑 | fault hooks 只放 `internal/inmemtest` |
| 冒烟范围失控 | 真 FUSE 测试做得过重，拖慢 `make test` | 自动冒烟只保留最小高价值链路 |

## 8. Phase 5 验收标准

- 跨平台 `inmem` 主套件覆盖读、写、缓存、多用户隔离、断线、超时
- Linux 真 FUSE 自动化冒烟默认被 `make test` 执行
- Linux 环境不满足 FUSE 前置条件时，自动测试以明确原因 `skip`
- Linux 真 FUSE 自动化与手动脚本口径一致
- `make fmt`、`make lint`、`make test`、`go build ./...` 通过

## 9. 默认与假设

- Phase 4 的缓存与写操作实现已经是 Phase 5 测试基线，不在本阶段回头重做实现方案
- `internal/inmemtest` 可以自由扩展为更通用的测试夹具，因为它处于 `internal/` 且只服务测试
- Linux 真 FUSE 测试默认纳入 `make test`，但环境阻塞优先视为 `skip`，不是功能回归
- 断线与超时必须通过可控 fault hooks 触发，而不是通过不稳定的黑盒等待
