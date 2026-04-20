# FUSE `cwd` / 模块发现兼容性修复实施计划

## 背景

在 `/workspace/localdemo` 挂载视图中，常规文件访问已经可用：

- `ls`
- `cat`
- `write`
- `mv`
- `rm`

但依赖当前工作目录语义和模块递归发现的工具仍会失败，典型表现为：

- `golangci-lint run ./...`
- `go test ./...`
- `go list ./...`

错误核心都是 `getwd: no such file or directory`。这说明当前 FUSE 实现已经满足“按路径访问文件”的最小需求，但还不满足“稳定工作目录 / 目录身份 / 模块发现”的更强语义。

本任务的目标是把这个问题作为一个独立后续阶段跟踪，不和当前 Phase 7 最小部署闭环混在一起。

## 阶段目标

修复 `/workspace/{user_id}` 在 Linux FUSE 挂载下对工作目录语义的兼容性，使依赖 `cwd` 和 `./...` 包发现的 Go 工具链能够正常工作。

目标行为：

- `pwd` 在挂载目录中稳定可用
- `go list ./...` 可用
- `go test ./...` 可用
- `golangci-lint run ./...` 可用

## 范围

- 分析当前 FUSE 目录节点的稳定身份问题
- 评估 `fs.StableAttr` / inode / 目录父子关系对 `getcwd` 的影响
- 必要时调整目录节点创建与缓存策略
- 补充针对 `cwd`、`go list ./...`、`go test ./...` 的回归测试

## 范围外

- 部署脚本本身的继续扩展
- Docker / systemd
- 非 Go 工具链的完整兼容矩阵
- 与本问题无关的 FUSE 功能优化

## 现状判断

从已知现象看：

- 路径型文件访问是通的
- 依赖目录身份回溯的工具不稳定
- 这更像是 FUSE 目录语义缺口，不像业务层 `session` 或 `syncer` 的错误

优先怀疑方向：

- 目录节点没有稳定 inode 身份
- FUSE 返回给内核的目录属性不够完整
- 目录重建和缓存失效策略使 `getcwd` 依赖的父链不稳定

## Todo

- [ ] 复现并最小化 `getwd` / `go list ./...` / `golangci-lint` 失败场景
- [ ] 审计 `pkg/fusefs/mount_linux.go` 中目录节点的 `StableAttr` 与属性填充逻辑
- [ ] 设计目录稳定身份方案，明确 inode 来源和生命周期
- [ ] 实现 FUSE 目录身份修复
- [ ] 补充 Linux 真 FUSE 回归测试
- [ ] 验证 `pwd`、`go list ./...`、`go test ./...`、`golangci-lint run ./...`

## 验收标准

- 在 `/workspace/{user_id}` 内执行 `pwd` 不再出现 `getwd` 相关错误
- `go list ./...` 可以从挂载目录正确枚举模块包
- `go test ./...` 可以直接在挂载目录运行
- `golangci-lint run ./...` 可以直接在挂载目录运行
- 现有 `ls` / `cat` / `write` / `mv` / `rm` 能力不回退

## 风险与应对

- 目录 inode 策略改动可能影响已有目录缓存行为
- 真 FUSE 回归测试容易受环境影响，需要继续保留 `skip` 逻辑
- 如果问题来自 go-fuse 内部行为而不是当前装配层，可能需要引入更深一层的节点稳定化设计
