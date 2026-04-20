# ci-format-diff

## 背景

GitHub Actions 在 `make fmt` 后执行 `git diff --exit-code` 失败，差异集中在 `app/clientpty/terminal_unix_bsd.go` 与 `app/clientpty/terminal_unix_other.go` 的 Go 格式化输出。

## 范围

- 修复 CI 暴露出的 Go 格式化差异。
- 修复该格式化差异提交后同一 CI 门禁会继续暴露的 lint 与 race/test 失败。
- 不调整 PTY 协议或部署脚本。
- 不处理当前主工作区中与本次问题无关的未跟踪文件。

## Todo

- [x] 基于 `origin/master` 复现格式化差异。
- [x] 运行项目标准格式化入口。
- [x] 修复后续 lint 失败。
- [x] 修复后续 race/test 失败。
- [x] 执行 `make fmt`、`make lint` 与 `make test`，记录结果。

## 验收

- `make fmt` 执行成功。
- 修复提交前 `git diff --exit-code` 展示预期格式化 diff；将本阶段 diff 提交后，该步骤不再产生格式化差异。
- `make lint` 通过。
- `make test` 通过。
