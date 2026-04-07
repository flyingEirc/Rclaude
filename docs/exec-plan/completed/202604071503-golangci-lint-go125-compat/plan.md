# Golangci-Lint Go 1.25 兼容修复计划

## 背景与触发

- GitHub Actions 在 `make lint` 步骤失败，报错：
  - `the Go language version (go1.24) used to build golangci-lint is lower than the targeted Go version (1.25.2)`
- 当前仓库在 [`Makefile`](/root/Rclaude/Makefile) 中将 `GOLANGCI_LINT_VERSION` 固定为 `v2.1.6`
- 当前 [`docs/workflow.md`](/root/Rclaude/docs/workflow.md) 也同步记录为 `v2.1.6`

## 目标与范围

目标：

- 修复 CI 上 `golangci-lint` 与 Go 1.25.2 的兼容问题
- 保持仓库本地开发与 CI 工具链版本一致

范围内：

- 升级 `Makefile` 中的 `GOLANGCI_LINT_VERSION`
- 更新 `docs/workflow.md` 中的工具版本说明
- 重新执行本地门禁
- 尽可能复现一次“从 Makefile 安装工具”的路径验证

范围外：

- 不改 `.golangci.yml` 规则
- 不改 GitHub Actions 流程结构
- 不引入 `golangci-lint-action`

## Todo

- [x] 创建阶段目录与三件套
- [x] 升级 `GOLANGCI_LINT_VERSION` 到支持 Go 1.25 的版本
- [x] 同步更新 `docs/workflow.md`
- [x] 执行 `make fmt`
- [x] 执行 `make lint`
- [x] 执行 `make test`
- [x] 执行 `go build ./...`
- [x] 复现 Makefile 工具安装路径并确认新版本可安装

## 验收标准

- CI 不再因 `golangci-lint` 二进制 Go 版本过低而失败
- `Makefile` 与 `docs/workflow.md` 中的 linter 版本一致
- 本地 `make fmt`、`make lint`、`make test`、`go build ./...` 通过

## 风险与应对

- 新版 `golangci-lint` 可能引入额外 lint 规则或更严格行为：先本地跑全量门禁验证
- 受本地网络限制，工具安装验证可能需要单独放权执行
