# GitHub Actions CI — 实施计划

## 背景与触发

- 依据 [`docs/workflow.md`](/root/Rclaude/docs/workflow.md) 的开发规范，仓库需要统一的 CI 基线：`make fmt` → `git diff --exit-code` → `make lint` → `make test`
- 依据 [`docs/design/ROADMAP.md`](/root/Rclaude/docs/design/ROADMAP.md) 的系统级验收标准，CI 还应覆盖 `go build ./...`
- 用户本次明确要求“不写额外设计文档，直接落实”，因此本阶段跳过 `docs/superpowers/specs/` 设计稿，仅保留 `exec-plan` 执行记录

## 目标与范围

目标：

- 新增一个符合当前项目规范的 GitHub Actions 工作流
- 在 `push` 与 `pull_request` 上统一执行格式化、diff 校验、lint、测试和构建

范围内：

- 新增 `.github/workflows/ci.yml`
- 使用仓库现有 `Makefile` 入口执行检查
- 配置基础并发控制，避免同一分支重复运行旧任务

范围外：

- Release / tag / artifact / coverage 上传
- 多平台或多 Go 版本 matrix
- 修改现有 Go 代码或测试行为

## 模块拆分

### M1. 工作流结构

- 触发事件：`push`、`pull_request`
- 单 job 串行执行，保证与本地规范顺序一致
- `permissions` 最小化为 `contents: read`
- 配置 `concurrency` 取消同 ref 的旧任务

### M2. 环境准备

- `actions/checkout@v4`
- `actions/setup-go@v5`，Go 版本从 `go.mod` 读取
- 将 `$(go env GOPATH)/bin` 注入 `PATH`
- 通过 `make tools` 安装仓库要求的工具链

### M3. 校验步骤

- `make fmt`
- `git diff --exit-code`
- `make lint`
- `make test`
- `make build`

## Todo

- [x] 创建阶段目录与三件套
- [x] 新增 `.github/workflows/ci.yml`
- [x] 校对 workflow 与 `Makefile` / `docs/workflow.md` 的一致性
- [x] 执行本地验证命令
- [x] 归档完成目录到 `completed/`

## 验收标准

- 仓库出现可用的 `.github/workflows/ci.yml`
- 工作流在 `push` 与 `pull_request` 上触发
- job 顺序与当前项目规范一致
- 本地执行 `make fmt`、`make lint`、`make test`、`make build` 通过

## 风险与应对

- `make tools` 依赖网络安装工具：保持与仓库本地开发流程一致，不在 workflow 中引入第二套安装逻辑
- CI 上可能没有 `/dev/fuse`：沿用现有 smoke test 的 `Skip` 机制，不单独特判 GitHub Actions
- 格式化步骤会写回文件：在后续紧跟 `git diff --exit-code`，直接暴露未格式化提交
