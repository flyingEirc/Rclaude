# Phase 1 完成摘要 — 协议与公共基础包

> 阶段目录：`docs/exec-plan/completed/202604061730-phase1-proto-and-base-pkgs/`
> 完成时间：2026-04-06
> 上游 ROADMAP：`docs/design/ROADMAP.md` Phase 1
> 上一阶段：`docs/exec-plan/completed/202604061700-phase0-skeleton/`

## 完成状态

**done** — 全部 8 个 Todo（T0–T7）按 plan 顺序完成；`测试错误.md` 中 E001 / E002 均 FIXED，无 OPEN。

## 交付清单

### 新增 / 重写源码
- `api/proto/remotefs/v1/remotefs.proto` — 重写至 130 行（PLAN.md §3.2 全量 message + service）
- `api/proto/remotefs/v1/remotefs.pb.go` — 重新生成（1354 行）
- `api/proto/remotefs/v1/remotefs_grpc.pb.go` — 首次生成（119 行）
- `pkg/safepath/{safepath.go, safepath_test.go}` — 路径安全（5 个公开 API + 4 个错误集 + 27 个测试用例）
- `pkg/logx/{logx.go, logx_test.go}` — slog 工厂（New/WithContext/FromContext + 9 个测试用例）
- `pkg/config/{config.go, config_test.go}` — viper YAML 加载（DaemonConfig/ServerConfig + Validate + 9 个测试用例）
- `pkg/auth/{auth.go, auth_test.go}` — token 鉴权 + gRPC 拦截器（StaticVerifier + Unary/Stream interceptor + 11 个测试用例）
- `pkg/fstree/{fstree.go, fstree_test.go}` — 内存文件树（Tree + 6 个公开方法 + 26 个测试用例，含 1000-goroutine 并发）

### 修改基础设施
- `Makefile` — 新增 `RACEFLAG` 变量，Windows 上跳过 `-race`，Linux/CI 仍跑 race（应对 E001）
- `.golangci.yml` — 从 `formatters.enable` 移除 `gofumpt`，仅保留 `gci`；standalone gofumpt 仍是 `make fmt` 中权威格式化器（应对 E002）

### go.mod 变化
- 新增 direct require：
  - `google.golang.org/grpc v1.80.0`（由 indirect 升级）
  - `github.com/stretchr/testify v1.11.1`
  - `github.com/spf13/viper v1.21.0`
- 新增间接依赖：viper 链路（fsnotify、afero、cast、pflag、go-toml/v2、mapstructure/v2、locafero、conc、gotenv、yaml.in/yaml/v3）+ grpc 链路（golang.org/x/{net,sys,text}、google.golang.org/genproto/googleapis/rpc）+ testify 链路（go-spew、go-difflib、yaml.v3）

## 验收结果

| 命令 | 结果 |
|---|---|
| `mingw32-make fmt` | exit 0，无 diff |
| `mingw32-make lint` | `0 issues.` |
| `mingw32-make test` | 5 个 pkg 全 ok（auth/config/fstree/logx/safepath） |
| `go build ./...` | exit 0 |
| `mingw32-make proto` | exit 0，幂等 |
| `go mod tidy` | exit 0，go.mod 稳定 |

## 与 plan 的偏离

| # | 偏离点 | 原因 | 影响 |
|---|---|---|---|
| 1 | proto enum 用 `CHANGE_TYPE_*` 前缀（`CHANGE_TYPE_UNSPECIFIED=0` 起） | PLAN.md 草图写的是 `CREATE=0`；改为 protobuf style guide 推荐前缀避免命名冲突 | 调用方需用 `remotefsv1.ChangeType_CHANGE_TYPE_CREATE` 而非 `ChangeType_CREATE`；无功能差异 |
| 2 | Makefile 增 `RACEFLAG` 变量 | E001：mingw-w64 8.1 + Go 1.25.2 race runtime 不兼容（STATUS_ENTRYPOINT_NOT_FOUND） | Windows 本地不跑 race；Linux/CI 仍保留 |
| 3 | `.golangci.yml` 从 formatters.enable 移除 gofumpt | E002：v2.1.6 bundled 的 gofumpt 与 gci 三段输出有幂等冲突 | standalone gofumpt v0.9.2 仍由 `make fmt` 强制执行，效果等价 |
| 4 | `pkg/fstree/fstree_test.go` 并发用例使用 `//nolint:errcheck,gosec` | `errcheck.check-blank: true` + v2 path-based exclude-rules 在当前配置下未生效 | 局部 inline 抑制；不修改 lint 配置，避免影响其它 pkg |

## 遗留问题

- **`.golangci.yml` v2 path-based exclude-rules 未生效** — `_test.go` 的 errcheck 排除规则在 v2 语法下未自动豁免；目前用 inline `//nolint` 处理，未来如有更多类似情况可重写为 v2 `linters.exclusions.rules`。Phase 1 范围内不动 lint 配置。
- **proto 生成与 gci 顺序差异** — `make proto` 生成的文件 import 分组与 gci 三段式不一致，每次重生后 `make fmt` 会再格式化 2 个 .pb.go 文件。可接受（gci 输出稳定，再次运行无 diff）。
- **`pkg/{transport,session,syncer,ratelimit}` 与 `internal/testutil`** 仍是空包 → 留给 Phase 2~6（在范围内，符合 plan 边界）。

## 下一步

→ 等待用户显式批准进入 **Phase 2 (Daemon MVP)**。
Phase 2 入口：`docs/design/ROADMAP.md` Phase 2 + `docs/design/PLAN.md`。
