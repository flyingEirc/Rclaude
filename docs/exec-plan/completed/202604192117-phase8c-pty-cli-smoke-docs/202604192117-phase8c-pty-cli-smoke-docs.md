# Phase 8c - Remote PTY CLI、smoke 与文档收口（完成摘要）

- 完成状态：done
- 验收结果：
  - `C:\Users\stillmoke\go\bin\gofumpt.exe -w app/clientpty/run.go app/clientpty/terminal_windows.go pkg/ptyservice/grpc_integration_test.go pkg/ptyservice/service.go`
  - `C:\Users\stillmoke\go\bin\golangci-lint.exe run ./app/clientpty/... ./app/server/... ./pkg/config ./pkg/ptyservice/... ./pkg/session/... ./pkg/ratelimit/...` 输出 `0 issues.`
  - `go test -count=1 ./api/proto/remotefs/v1 ./pkg/config ./internal/testutil ./pkg/session/... ./pkg/ratelimit/... ./pkg/ptyservice/... ./app/server/... ./app/clientpty/...` 全部通过
  - `go build -o C:\Users\stillmoke\.codex\memories\clientpty.exe ./app/clientpty` 通过
- 与 plan 的偏离：
  - 当前环境无 `make`，因此未通过 `make fmt` / `make lint` / `make test` 入口执行，而是使用等价命令完成本阶段格式化、lint 与测试校验。
  - 受当前 Windows + Git Bash / MSYS 环境限制，未能在本机完成 `sh -n tools/pty-smoke.sh ...`；脚本与部署目录已对齐，真机 PTY smoke 需在目标 Linux/PTTY 环境执行。
  - `rclaude-claude` 现会读取 daemon YAML 中可选的 `pty.frame_max_bytes`，用于与 server 侧 stdin 分帧上限保持一致；这属于 review 后补上的兼容性修正。
- 遗留问题：
  - 真机 PTY smoke 与人工 attach 仍需在目标部署环境执行一次，作为仓库外的运行期验收。
