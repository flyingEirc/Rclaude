# Phase 8a — Remote PTY 基础包与协议

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地远端 claude PTY 特性所需的协议与基础包：`api/proto/remotefs/v1/pty.proto`、`pkg/ptyhost/`、`pkg/ptyclient/`、`pkg/config` 的 PTY 配置块。本阶段不做服务装配、不动 `app/server` 或新建 `app/clientpty`，只交付可单独编译、可单测验证的"积木"。

**Architecture:** server 端的 PTY 进程管理（`pkg/ptyhost`）与本地 CLI 的 PTY 桥接（`pkg/ptyclient`）通过新增的 `RemotePTY.Attach` gRPC bidi stream 串联。本阶段只交付包内逻辑与协议定义，不接 service handler；下一阶段（Phase 8b）再装配。

**Tech Stack:**
- Proto: `api/proto/remotefs/v1`（已有），用 `make proto` 生成
- Go: `github.com/creack/pty v1.1.24`（PTY 进程管理，仅 server 侧 unix）、`golang.org/x/term v0.42.0`（CLI raw 模式）、`golang.org/x/sys/windows`（CLI Windows 平台轮询，本阶段只占位）、`testify`（断言）、`golang.org/x/sync/errgroup`（已有）
- 构建：`make fmt` / `make lint` / `make test` / `make proto`

**上游依据：**
- 设计 spec：[`docs/superpowers/specs/2026-04-19-remote-claudecode-pty-design.md`](/e:/Rclaude/docs/superpowers/specs/2026-04-19-remote-claudecode-pty-design.md)
- 系统 ROADMAP（待新增 Phase 8 条目）：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md)

**本阶段不包含（留给 Phase 8b/8c）：**
- `RemotePTY.Attach` server handler 实现
- `app/server/main.go` service 装配
- `app/clientpty/main.go` 新二进制
- `pkg/session.Manager` 增加 `LookupDaemon`/`RegisterPTY`/`UnregisterPTY`
- `pkg/ratelimit` 增加 `pty.attach.qps` / `pty.bytes.in.bps`（本阶段只在 config schema 占位，不接 limiter）
- `internal/inmemtest` PTY 子套件
- `tools/pty-smoke.sh`、文档、ROADMAP 8a/8b/8c 条目

---

## 文件变更总览

**新建：**

- `api/proto/remotefs/v1/pty.proto`
- `api/proto/remotefs/v1/pty.pb.go`（`make proto` 生成，入库）
- `api/proto/remotefs/v1/pty_grpc.pb.go`（`make proto` 生成，入库）
- `pkg/ptyhost/doc.go`
- `pkg/ptyhost/types.go`
- `pkg/ptyhost/policy.go`
- `pkg/ptyhost/policy_test.go`
- `pkg/ptyhost/host_unix.go`（`//go:build unix`）
- `pkg/ptyhost/host_other.go`（`//go:build !unix`，仅占位返回 ErrUnsupported）
- `pkg/ptyhost/host_unix_test.go`（`//go:build unix`）
- `pkg/ptyclient/doc.go`
- `pkg/ptyclient/types.go`
- `pkg/ptyclient/client.go`
- `pkg/ptyclient/client_test.go`
- `pkg/ptyclient/resize_unix.go`（`//go:build unix`）
- `pkg/ptyclient/resize_windows.go`（`//go:build windows`）
- `pkg/ptyclient/resize_test.go`

**修改：**

- `pkg/config/config.go`（新增 `PTYConfig` 与默认值常量，挂到 `ServerConfig`）
- `pkg/config/config_test.go`（PTY 配置块解析与默认值测试）

**不动：**

- `app/server/`、`app/client/`、`pkg/session/`、`pkg/ratelimit/`、`pkg/transport/`、`pkg/auth/`、`pkg/fusefs/`、`pkg/syncer/`

---

## 进度

- [x] Task 1: 阶段目录三件套与起步说明
- [x] Task 2: 新增 `pty.proto` 并生成代码
- [x] Task 3: `pkg/config` PTY 配置块
- [x] Task 4: `pkg/ptyhost` types + policy
- [x] Task 5: `pkg/ptyhost` Host 进程管理（unix + 占位 stub）
- [x] Task 6: `pkg/ptyclient` types + Client 主循环
- [x] Task 7: `pkg/ptyclient` resize 数据源（跨平台）
- [x] Task 8: 阶段验收：`make fmt` / `make lint` / `make test` 全绿，归档完成摘要

---

## Task 1: 阶段目录三件套与起步说明

**Files:**
- Create: `docs/exec-plan/active/202604191500-phase8a-pty-foundation/plan.md`（即本文件）
- Create: `docs/exec-plan/active/202604191500-phase8a-pty-foundation/开发流程.md`
- Create: `docs/exec-plan/active/202604191500-phase8a-pty-foundation/测试错误.md`

- [ ] **Step 1:** 三个文件已经在阶段目录中。开发者第一次接手时，把今天的日期写入 `开发流程.md` 顶端，确认 git 状态干净到只剩这三个新文件 + 即将动的 `pkg/config` 改动。

- [ ] **Step 2:** 提交三件套骨架。

```bash
git add docs/exec-plan/active/202604191500-phase8a-pty-foundation/
git commit -m "docs(phase8a): scaffold remote pty foundation exec-plan"
```

---

## Task 2: 新增 `pty.proto` 并生成代码

**Files:**
- Create: `api/proto/remotefs/v1/pty.proto`
- Create (生成): `api/proto/remotefs/v1/pty.pb.go`
- Create (生成): `api/proto/remotefs/v1/pty_grpc.pb.go`

- [ ] **Step 1:** 创建 `api/proto/remotefs/v1/pty.proto`，内容如下（注意 `package` 与 `go_package` 必须与现有 `remotefs.proto` 一致）：

```protobuf
syntax = "proto3";

package remotefs.v1;

option go_package = "flyingEirc/Rclaude/api/proto/remotefs/v1;remotefsv1";

service RemotePTY {
  // 建立 PTY 会话，双向流。
  // 第一帧必须是 ClientFrame.attach；服务端首条响应必须是 ServerFrame.attached 或 error。
  rpc Attach(stream ClientFrame) returns (stream ServerFrame);
}

message ClientFrame {
  oneof payload {
    AttachReq attach = 1;
    bytes     stdin  = 2;
    Resize    resize = 3;
    Detach    detach = 4;
  }
}

message AttachReq {
  string session_id = 1;
  Resize initial_size = 2;
  string term = 3;
  repeated string extra_env = 15;
}

message Resize {
  uint32 cols = 1;
  uint32 rows = 2;
  uint32 x_pixel = 3;
  uint32 y_pixel = 4;
}

message Detach {}

message ServerFrame {
  oneof payload {
    Attached attached = 1;
    bytes    stdout  = 2;
    Exited   exited  = 3;
    Error    error   = 4;
  }
}

message Attached {
  string session_id = 1;
  string cwd        = 2;
}

message Exited {
  int32  code   = 1;
  uint32 signal = 2;
}

message Error {
  enum Kind {
    KIND_UNSPECIFIED          = 0;
    KIND_UNAUTHENTICATED      = 1;
    KIND_DAEMON_NOT_CONNECTED = 2;
    KIND_SESSION_BUSY         = 3;
    KIND_SPAWN_FAILED         = 4;
    KIND_PROTOCOL             = 5;
    KIND_RATE_LIMITED         = 6;
    KIND_INTERNAL             = 99;
  }
  Kind   kind    = 1;
  string message = 2;
}
```

> 注：枚举名按现有 `ChangeType` 风格加 `KIND_` 前缀，避免与 Go 端命名冲突；spec 表格中的简短名（`UNAUTHENTICATED` 等）属于"逻辑名"，proto 真实标识符以本任务为准。

- [ ] **Step 2:** 生成代码。

```bash
make proto
```

Expected output: 一行 `>>> protoc`，无报错；`api/proto/remotefs/v1/` 下出现 `pty.pb.go` 与 `pty_grpc.pb.go`，`remotefs.pb.go` 与 `remotefs_grpc.pb.go` 内容应保持不变（diff 为空）。

- [ ] **Step 3:** 编写最小冒烟测试，证明类型与 service 描述符存在。新建 `api/proto/remotefs/v1/pty_smoke_test.go`：

```go
package remotefsv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPTYTypesPresent(t *testing.T) {
	cf := &ClientFrame{Payload: &ClientFrame_Attach{Attach: &AttachReq{
		SessionId:   "",
		InitialSize: &Resize{Cols: 80, Rows: 24},
		Term:        "xterm-256color",
	}}}
	require.NotNil(t, cf.GetAttach())
	require.Equal(t, uint32(80), cf.GetAttach().GetInitialSize().GetCols())

	sf := &ServerFrame{Payload: &ServerFrame_Error{Error: &Error{
		Kind:    Error_KIND_PROTOCOL,
		Message: "smoke",
	}}}
	require.Equal(t, "smoke", sf.GetError().GetMessage())

	require.NotNil(t, RemotePTY_ServiceDesc.Streams, "RemotePTY service descriptor must register at least one stream")
}
```

- [ ] **Step 4:** 跑包内测试。

```bash
go test ./api/proto/remotefs/v1/...
```

Expected: PASS（包含新增 `TestPTYTypesPresent` 与已有 proto 包测试，如果有的话）。

- [ ] **Step 5:** 跑全仓库 `make test`，确认 proto 改动没有破坏现有依赖。

```bash
make test
```

Expected: 全绿。

- [ ] **Step 6:** 提交。

```bash
git add api/proto/remotefs/v1/pty.proto api/proto/remotefs/v1/pty.pb.go api/proto/remotefs/v1/pty_grpc.pb.go api/proto/remotefs/v1/pty_smoke_test.go
git commit -m "feat(proto): add RemotePTY service and frame types"
```

---

## Task 3: `pkg/config` PTY 配置块

**Files:**
- Modify: `pkg/config/config.go`（新增 PTY 类型与默认常量，挂到 `ServerConfig`）
- Modify: `pkg/config/config_test.go`（默认值 + YAML 解析 + 校验测试）

- [ ] **Step 1:** 先写失败测试。在 `pkg/config/config_test.go` 文件末尾追加：

```go
func TestServerConfig_PTYDefaults(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "server.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(`
listen: ":9000"
auth:
  tokens:
    user1: token1
fuse:
  mountpoint: "/tmp/workspace"
`), 0o600))

	cfg, err := config.LoadServer(yamlPath)
	require.NoError(t, err)

	require.Equal(t, "claude", cfg.PTY.Binary)
	require.Equal(t, "/workspace", cfg.PTY.WorkspaceRoot)
	require.Equal(t, []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "PATH"}, cfg.PTY.EnvPassthrough)
	require.Equal(t, int64(64*1024), cfg.PTY.FrameMaxBytes)
	require.Equal(t, 5*time.Second, cfg.PTY.GracefulShutdownTimeout)
	require.Equal(t, 1, cfg.PTY.RateLimit.AttachQPS)
	require.Equal(t, 3, cfg.PTY.RateLimit.AttachBurst)
	require.Equal(t, int64(1<<20), cfg.PTY.RateLimit.StdinBPS)
	require.Equal(t, int64(256*1024), cfg.PTY.RateLimit.StdinBurst)
}

func TestServerConfig_PTYExplicit(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "server.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(`
listen: ":9000"
auth:
  tokens:
    user1: token1
fuse:
  mountpoint: "/tmp/workspace"
pty:
  binary: "/usr/local/bin/claude"
  workspace_root: "/srv/workspace"
  env_passthrough: ["TERM", "PATH"]
  frame_max_bytes: 32768
  graceful_shutdown_timeout: "10s"
  ratelimit:
    attach_qps: 2
    attach_burst: 6
    stdin_bps: 524288
    stdin_burst: 131072
`), 0o600))

	cfg, err := config.LoadServer(yamlPath)
	require.NoError(t, err)

	require.Equal(t, "/usr/local/bin/claude", cfg.PTY.Binary)
	require.Equal(t, []string{"TERM", "PATH"}, cfg.PTY.EnvPassthrough)
	require.Equal(t, 10*time.Second, cfg.PTY.GracefulShutdownTimeout)
	require.Equal(t, 2, cfg.PTY.RateLimit.AttachQPS)
}

func TestServerConfig_PTYInvalidWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "server.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(`
listen: ":9000"
auth:
  tokens:
    user1: token1
fuse:
  mountpoint: "/tmp/workspace"
pty:
  workspace_root: "relative/path"
`), 0o600))

	_, err := config.LoadServer(yamlPath)
	require.ErrorIs(t, err, config.ErrPTYWorkspaceRootNotAbs)
}
```

如果 `config_test.go` 还没有 `time` 或 `os`/`filepath` 等 import，按编译报错补齐。

- [ ] **Step 2:** 跑失败测试，确认它真的因为类型不存在或字段不存在挂掉。

```bash
go test ./pkg/config/...
```

Expected: FAIL，错误是 `cfg.PTY undefined` / `config.ErrPTYWorkspaceRootNotAbs undefined`。

- [ ] **Step 3:** 在 `pkg/config/config.go` 加默认值常量（放到顶部 `const` 块）：

```go
const (
	DefaultPTYBinary                  = "claude"
	DefaultPTYWorkspaceRoot           = "/workspace"
	DefaultPTYFrameMaxBytes     int64 = 64 * 1024
	DefaultPTYGracefulShutdown        = 5 * time.Second
	DefaultPTYAttachQPS               = 1
	DefaultPTYAttachBurst             = 3
	DefaultPTYStdinBPS          int64 = 1 << 20
	DefaultPTYStdinBurst        int64 = 256 * 1024
)
```

`DefaultPTYEnvPassthrough` 用 `var` 而非 `const`（slice 不能 const）：

```go
var DefaultPTYEnvPassthrough = []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "PATH"}
```

- [ ] **Step 4:** 在 `pkg/config/config.go` 的 `var (...)` 错误块加：

```go
var (
	// ... existing errors
	ErrPTYWorkspaceRootNotAbs = errors.New("config: pty.workspace_root must be absolute")
	ErrPTYFrameMaxBytesNegative = errors.New("config: pty.frame_max_bytes must be > 0")
	ErrPTYRateLimitNegative   = errors.New("config: pty.ratelimit values must be >= 0")
)
```

- [ ] **Step 5:** 在 `pkg/config/config.go` 增加类型：

```go
type PTYRateLimitConfig struct {
	AttachQPS   int   `mapstructure:"attach_qps"`
	AttachBurst int   `mapstructure:"attach_burst"`
	StdinBPS    int64 `mapstructure:"stdin_bps"`
	StdinBurst  int64 `mapstructure:"stdin_burst"`
}

type PTYConfig struct {
	Binary                  string             `mapstructure:"binary"`
	WorkspaceRoot           string             `mapstructure:"workspace_root"`
	EnvPassthrough          []string           `mapstructure:"env_passthrough"`
	FrameMaxBytes           int64              `mapstructure:"frame_max_bytes"`
	GracefulShutdownTimeout time.Duration      `mapstructure:"graceful_shutdown_timeout"`
	RateLimit               PTYRateLimitConfig `mapstructure:"ratelimit"`
}
```

挂到 `ServerConfig`：

```go
type ServerConfig struct {
	// ... existing fields
	PTY PTYConfig `mapstructure:"pty"`
}
```

- [ ] **Step 6:** 在 `defaultServerConfig()` 函数内追加 PTY 默认值（位置：找到 `defaultServerConfig` 函数，在 `return ServerConfig{...}` 字段中加）：

```go
PTY: PTYConfig{
	Binary:                  DefaultPTYBinary,
	WorkspaceRoot:           DefaultPTYWorkspaceRoot,
	EnvPassthrough:          append([]string(nil), DefaultPTYEnvPassthrough...),
	FrameMaxBytes:           DefaultPTYFrameMaxBytes,
	GracefulShutdownTimeout: DefaultPTYGracefulShutdown,
	RateLimit: PTYRateLimitConfig{
		AttachQPS:   DefaultPTYAttachQPS,
		AttachBurst: DefaultPTYAttachBurst,
		StdinBPS:    DefaultPTYStdinBPS,
		StdinBurst:  DefaultPTYStdinBurst,
	},
},
```

- [ ] **Step 7:** 在 `(*ServerConfig).Validate()` 末尾加 PTY 校验：

```go
if !filepath.IsAbs(c.PTY.WorkspaceRoot) {
	return ErrPTYWorkspaceRootNotAbs
}
if c.PTY.FrameMaxBytes <= 0 {
	return ErrPTYFrameMaxBytesNegative
}
if c.PTY.RateLimit.AttachQPS < 0 || c.PTY.RateLimit.AttachBurst < 0 ||
	c.PTY.RateLimit.StdinBPS < 0 || c.PTY.RateLimit.StdinBurst < 0 {
	return ErrPTYRateLimitNegative
}
```

- [ ] **Step 8:** 跑测试。

```bash
go test ./pkg/config/...
```

Expected: PASS。

- [ ] **Step 9:** 跑全仓库测试，确认没有打到别的依赖。

```bash
make test
```

Expected: 全绿。

- [ ] **Step 10:** 提交。

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): add PTY config block and validation"
```

---

## Task 4: `pkg/ptyhost` types + policy

**Files:**
- Create: `pkg/ptyhost/doc.go`
- Create: `pkg/ptyhost/types.go`
- Create: `pkg/ptyhost/policy.go`
- Create: `pkg/ptyhost/policy_test.go`

- [ ] **Step 1:** 写 `pkg/ptyhost/doc.go`：

```go
// Package ptyhost manages PTY-bound child processes on the server side.
//
// It is intentionally transport-agnostic: callers feed it stdin via an
// io.Writer, drain stdout via an io.Reader, push window-size updates, and
// wait for the child to exit. The gRPC layer is a separate concern wired up
// in app/server. See docs/superpowers/specs/2026-04-19-remote-claudecode-pty-design.md.
package ptyhost
```

- [ ] **Step 2:** 写 `pkg/ptyhost/types.go`：

```go
package ptyhost

import (
	"errors"
	"time"
)

// ErrUnsupportedPlatform is returned when ptyhost cannot spawn a PTY on the
// current GOOS (only unix is supported).
var ErrUnsupportedPlatform = errors.New("ptyhost: PTY spawn is unsupported on this platform")

// SpawnReq describes how to launch a PTY-bound child process.
//
// All fields are validated by Spawn before exec; callers do not need to
// pre-sanitize Cwd / Env / Binary against the workspace policy — that is
// the policy.go layer's job.
type SpawnReq struct {
	Binary   string
	Cwd      string
	Env      []string
	InitSize WindowSize

	// GracefulTimeout is the upper bound between SIGHUP and SIGKILL when
	// Shutdown(graceful=true) is called. Zero means "use default 5s".
	GracefulTimeout time.Duration
}

// WindowSize is the terminal geometry sent through TIOCSWINSZ.
type WindowSize struct {
	Cols   uint32
	Rows   uint32
	XPixel uint32
	YPixel uint32
}

// ExitInfo captures a finished child process result.
//
// Code is the exit status; Signal is non-zero only when the process died
// to a signal (in which case Code is implementation-defined and should be
// ignored by callers).
type ExitInfo struct {
	Code   int32
	Signal uint32
}
```

- [ ] **Step 3:** 写 `pkg/ptyhost/policy.go`。先写测试再写实现。

先写 `pkg/ptyhost/policy_test.go`：

```go
package ptyhost_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/ptyhost"
)

func TestResolveCwd_BasicUserScope(t *testing.T) {
	got, err := ptyhost.ResolveCwd("/workspace", "alice")
	require.NoError(t, err)
	require.Equal(t, "/workspace/alice", got)
}

func TestResolveCwd_RejectsTraversalUserID(t *testing.T) {
	_, err := ptyhost.ResolveCwd("/workspace", "../etc")
	require.ErrorIs(t, err, ptyhost.ErrUnsafeUserID)
}

func TestResolveCwd_RejectsEmpty(t *testing.T) {
	_, err := ptyhost.ResolveCwd("/workspace", "")
	require.ErrorIs(t, err, ptyhost.ErrUnsafeUserID)

	_, err = ptyhost.ResolveCwd("", "alice")
	require.ErrorIs(t, err, ptyhost.ErrWorkspaceRootNotAbs)
}

func TestResolveCwd_RejectsRelativeRoot(t *testing.T) {
	_, err := ptyhost.ResolveCwd("workspace", "alice")
	require.ErrorIs(t, err, ptyhost.ErrWorkspaceRootNotAbs)
}

func TestBuildEnv_WhitelistOnly(t *testing.T) {
	source := map[string]string{
		"TERM":           "xterm-256color",
		"LANG":           "en_US.UTF-8",
		"PATH":           "/usr/bin:/bin",
		"AWS_SECRET_KEY": "leakme",
		"HOME":           "/root",
	}
	whitelist := []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "PATH"}

	got := ptyhost.BuildEnv(source, whitelist, "")
	joined := strings.Join(got, "\n")

	require.Contains(t, joined, "TERM=xterm-256color")
	require.Contains(t, joined, "LANG=en_US.UTF-8")
	require.Contains(t, joined, "PATH=/usr/bin:/bin")
	require.NotContains(t, joined, "AWS_SECRET_KEY")
	require.NotContains(t, joined, "HOME=")
}

func TestBuildEnv_ClientTermOverride(t *testing.T) {
	source := map[string]string{"TERM": "dumb"}
	whitelist := []string{"TERM"}

	got := ptyhost.BuildEnv(source, whitelist, "xterm-256color")
	require.Equal(t, []string{"TERM=xterm-256color"}, got)
}

func TestBuildEnv_RejectsBadClientTerm(t *testing.T) {
	// 客户端 term 不在白名单字符集（含空格）→ 忽略，回落到 server env
	source := map[string]string{"TERM": "dumb"}
	whitelist := []string{"TERM"}

	got := ptyhost.BuildEnv(source, whitelist, "weird term")
	require.Equal(t, []string{"TERM=dumb"}, got)
}

func TestResolveBinary_AbsolutePath(t *testing.T) {
	// 绝对路径直接返回（不在 lookup 阶段做 stat，留给 exec 报错）
	got, err := ptyhost.ResolveBinary("/usr/local/bin/claude")
	require.NoError(t, err)
	require.Equal(t, "/usr/local/bin/claude", got)
}

func TestResolveBinary_NameLookup(t *testing.T) {
	// "go" 在当前仓库的开发/CI 环境中应当在 PATH 内，且跨平台更稳定
	got, err := ptyhost.ResolveBinary("go")
	require.NoError(t, err)
	require.NotEmpty(t, got)
}

func TestResolveBinary_NotFound(t *testing.T) {
	_, err := ptyhost.ResolveBinary("definitely-not-a-real-binary-zzz")
	require.ErrorIs(t, err, ptyhost.ErrBinaryNotFound)
}

func TestResolveBinary_RejectsEmpty(t *testing.T) {
	_, err := ptyhost.ResolveBinary("")
	require.ErrorIs(t, err, ptyhost.ErrBinaryEmpty)
}
```

- [ ] **Step 4:** 跑测试，确认全失败（编译失败也算失败）。

```bash
go test ./pkg/ptyhost/...
```

Expected: 编译失败 / 函数未定义。

- [ ] **Step 5:** 写 `pkg/ptyhost/policy.go`：

```go
package ptyhost

import (
	"errors"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrUnsafeUserID         = errors.New("ptyhost: user id contains unsafe characters or path elements")
	ErrWorkspaceRootNotAbs  = errors.New("ptyhost: workspace root must be an absolute path")
	ErrBinaryEmpty          = errors.New("ptyhost: binary name is empty")
	ErrBinaryNotFound       = errors.New("ptyhost: binary not found in PATH")
)

// ResolveCwd returns the absolute working directory under the workspace root
// for the given user id, with traversal protection.
//
// userID must not contain "/" or "\", must not be ".", "..", and must not be
// empty. workspaceRoot must be absolute.
func ResolveCwd(workspaceRoot, userID string) (string, error) {
	if !filepath.IsAbs(workspaceRoot) {
		return "", ErrWorkspaceRootNotAbs
	}
	if userID == "" || userID == "." || userID == ".." ||
		strings.ContainsAny(userID, `/\`) {
		return "", ErrUnsafeUserID
	}
	// Use forward slash join for transport-uniform paths; on linux this is
	// also a valid OS path.
	return path.Join(workspaceRoot, userID), nil
}

// BuildEnv returns a sorted KEY=VALUE slice for exec.Cmd.Env, restricted to
// the given whitelist drawn from source. clientTerm, when non-empty and
// composed only of safe chars (alnum, '-', '_', '.'), overrides TERM.
func BuildEnv(source map[string]string, whitelist []string, clientTerm string) []string {
	allow := make(map[string]bool, len(whitelist))
	for _, k := range whitelist {
		allow[k] = true
	}

	out := make([]string, 0, len(whitelist))
	for k, v := range source {
		if !allow[k] {
			continue
		}
		out = append(out, k+"="+v)
	}

	if allow["TERM"] && isSafeTerm(clientTerm) {
		// remove any TERM= we already added
		filtered := out[:0]
		for _, kv := range out {
			if !strings.HasPrefix(kv, "TERM=") {
				filtered = append(filtered, kv)
			}
		}
		out = append(filtered, "TERM="+clientTerm)
	}

	sort.Strings(out)
	return out
}

func isSafeTerm(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// ResolveBinary returns an absolute path to the given binary name. Absolute
// paths are returned unchanged (existence check is left to exec). Bare names
// are looked up via $PATH.
func ResolveBinary(name string) (string, error) {
	if name == "" {
		return "", ErrBinaryEmpty
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", ErrBinaryNotFound
	}
	return p, nil
}
```

- [ ] **Step 6:** 跑测试。

```bash
go test ./pkg/ptyhost/...
```

Expected: PASS。

- [ ] **Step 7:** 跑 `make lint`，确认 import 排序、命名等通过。

```bash
make lint
```

Expected: 全绿（如 lint 报序，先 `make fmt` 再重跑）。

- [ ] **Step 8:** 提交。

```bash
git add pkg/ptyhost/
git commit -m "feat(ptyhost): add types + cwd/env/binary policy with tests"
```

---

## Task 5: `pkg/ptyhost` Host 进程管理（unix + 占位 stub）

**Files:**
- Create: `pkg/ptyhost/host_unix.go`（`//go:build unix`）
- Create: `pkg/ptyhost/host_other.go`（`//go:build !unix`）
- Create: `pkg/ptyhost/host_unix_test.go`（`//go:build unix`）

`go.mod` 需要追加 `github.com/creack/pty`。

- [ ] **Step 1:** 拉依赖。

```bash
go get github.com/creack/pty@v1.1.24
go mod tidy
```

Expected: `go.mod` / `go.sum` 增加 `creack/pty` 行；编译仍通过。

- [ ] **Step 2:** 先写 unix 平台的失败测试 `pkg/ptyhost/host_unix_test.go`：

```go
//go:build unix

package ptyhost_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/ptyhost"
)

func TestSpawn_EchoAndExit(t *testing.T) {
	h, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/bin/sh",
		Cwd:      t.TempDir(),
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.NoError(t, err)

	// Drain stdout in background until EOF.
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, h.Stdout())
	}()

	_, err = h.Stdin().Write([]byte("echo hello-pty\n"))
	require.NoError(t, err)
	_, err = h.Stdin().Write([]byte("exit 0\n"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := h.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(0), info.Code)

	wg.Wait()
	require.Contains(t, buf.String(), "hello-pty")
}

func TestSpawn_BinaryNotFound(t *testing.T) {
	_, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/no/such/binary/here",
		Cwd:      t.TempDir(),
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.Error(t, err)
}

func TestSpawn_CwdMustExist(t *testing.T) {
	_, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/bin/sh",
		Cwd:      "/definitely/no/such/dir",
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.Error(t, err)
}

func TestResize_PropagatesToChild(t *testing.T) {
	// `stty size` prints "rows cols" by reading the controlling tty.
	h, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/bin/sh",
		Cwd:      t.TempDir(),
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.NoError(t, err)

	require.NoError(t, h.Resize(ptyhost.WindowSize{Cols: 132, Rows: 50}))

	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, h.Stdout())
	}()

	_, err = h.Stdin().Write([]byte("stty size; exit 0\n"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = h.Wait(ctx)
	require.NoError(t, err)
	wg.Wait()

	require.True(t,
		strings.Contains(buf.String(), "50 132"),
		"expected `stty size` to report 50 132, got: %q", buf.String(),
	)
}

func TestShutdown_GracefulThenKill(t *testing.T) {
	// `sh` 子进程会忽略 SIGHUP 上的简单 trap，所以我们用一个 trap 把 SIGHUP 接住、
	// 然后 sleep 久。Shutdown(graceful=true) 应该在 GracefulTimeout 后升级为 SIGKILL。
	h, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:          "/bin/sh",
		Cwd:             t.TempDir(),
		Env:             []string{"PATH=/usr/bin:/bin"},
		InitSize:        ptyhost.WindowSize{Cols: 80, Rows: 24},
		GracefulTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	_, err = h.Stdin().Write([]byte("trap '' HUP; sleep 30\n"))
	require.NoError(t, err)
	// 给 sh 一点时间装好 trap 并进入 sleep。
	time.Sleep(100 * time.Millisecond)

	go func() {
		_, _ = io.Copy(io.Discard, h.Stdout())
	}()

	require.NoError(t, h.Shutdown(true))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := h.Wait(ctx)
	require.NoError(t, err)
	require.NotEqual(t, uint32(0), info.Signal,
		"expected child to be terminated by signal after graceful timeout")
}
```

- [ ] **Step 3:** 跑测试，确认全失败（编译失败）。

```bash
go test ./pkg/ptyhost/...
```

Expected: 编译失败 / `Spawn` 等未定义。

- [ ] **Step 4:** 写 `pkg/ptyhost/host_unix.go`：

```go
//go:build unix

package ptyhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const defaultGracefulTimeout = 5 * time.Second

// Host is a running PTY-bound child process.
//
// Stdin / Stdout return io.Writer / io.Reader views over the master fd.
// Resize forwards a TIOCSWINSZ. Shutdown requests termination (graceful
// = SIGHUP then SIGKILL after GracefulTimeout). Wait blocks until the child
// exits or ctx fires; ctx.Err() is returned in the latter case (the child is
// not killed by Wait — call Shutdown for that).
type Host struct {
	cmd  *exec.Cmd
	ptmx *os.File

	graceful time.Duration

	exitOnce sync.Once
	exitErr  error
	exited   chan struct{}
	info     ExitInfo
}

// Spawn starts a PTY-bound child according to req.
func Spawn(req SpawnReq) (*Host, error) {
	if req.Binary == "" {
		return nil, ErrBinaryEmpty
	}

	cmd := exec.Command(req.Binary)
	cmd.Dir = req.Cwd
	cmd.Env = req.Env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: pty.Start: %w", err)
	}

	graceful := req.GracefulTimeout
	if graceful <= 0 {
		graceful = defaultGracefulTimeout
	}

	h := &Host{
		cmd:      cmd,
		ptmx:     ptmx,
		graceful: graceful,
		exited:   make(chan struct{}),
	}

	if req.InitSize.Cols > 0 || req.InitSize.Rows > 0 {
		if err := h.Resize(req.InitSize); err != nil {
			_ = h.Shutdown(false)
			return nil, fmt.Errorf("ptyhost: initial resize: %w", err)
		}
	}

	go h.reap()
	return h, nil
}

// Stdin returns the master fd as an io.Writer (PTY input).
func (h *Host) Stdin() io.Writer { return h.ptmx }

// Stdout returns the master fd as an io.Reader (PTY output, stdout+stderr).
func (h *Host) Stdout() io.Reader { return h.ptmx }

// Resize forwards window-size changes to the controlling terminal.
func (h *Host) Resize(ws WindowSize) error {
	return pty.Setsize(h.ptmx, &pty.Winsize{
		Cols: uint16(ws.Cols),
		Rows: uint16(ws.Rows),
		X:    uint16(ws.XPixel),
		Y:    uint16(ws.YPixel),
	})
}

// Shutdown asks the child to exit. graceful=true sends SIGHUP first and
// escalates to SIGKILL after GracefulTimeout; graceful=false sends SIGKILL
// immediately. Either way Shutdown returns once the signal is dispatched —
// use Wait to block on actual exit.
func (h *Host) Shutdown(graceful bool) error {
	if h.cmd == nil || h.cmd.Process == nil {
		return nil
	}
	if !graceful {
		_ = h.cmd.Process.Signal(syscall.SIGKILL)
		_ = h.ptmx.Close()
		return nil
	}

	if err := h.cmd.Process.Signal(syscall.SIGHUP); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("ptyhost: SIGHUP: %w", err)
	}

	go func() {
		select {
		case <-h.exited:
			return
		case <-time.After(h.graceful):
			_ = h.cmd.Process.Signal(syscall.SIGKILL)
			_ = h.ptmx.Close()
		}
	}()
	return nil
}

// Wait blocks until the child exits or ctx fires.
func (h *Host) Wait(ctx context.Context) (ExitInfo, error) {
	select {
	case <-h.exited:
		return h.info, h.exitErr
	case <-ctx.Done():
		return ExitInfo{}, ctx.Err()
	}
}

func (h *Host) reap() {
	defer close(h.exited)
	defer func() { _ = h.ptmx.Close() }()

	err := h.cmd.Wait()
	h.exitOnce.Do(func() {
		var sig uint32
		var code int32
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				if status.Signaled() {
					sig = uint32(status.Signal())
					code = -1
				} else {
					code = int32(status.ExitStatus())
				}
			}
		} else if err == nil {
			code = 0
		} else {
			h.exitErr = err
		}
		h.info = ExitInfo{Code: code, Signal: sig}
	})
}
```

- [ ] **Step 5:** 写 `pkg/ptyhost/host_other.go`：

```go
//go:build !unix

package ptyhost

import (
	"context"
	"io"
)

// Host is a stub on non-unix platforms; Spawn always returns ErrUnsupportedPlatform.
type Host struct{}

func Spawn(_ SpawnReq) (*Host, error)            { return nil, ErrUnsupportedPlatform }
func (h *Host) Stdin() io.Writer                 { return io.Discard }
func (h *Host) Stdout() io.Reader                { return nopReader{} }
func (h *Host) Resize(_ WindowSize) error        { return ErrUnsupportedPlatform }
func (h *Host) Shutdown(_ bool) error            { return ErrUnsupportedPlatform }
func (h *Host) Wait(_ context.Context) (ExitInfo, error) {
	return ExitInfo{}, ErrUnsupportedPlatform
}

type nopReader struct{}

func (nopReader) Read(_ []byte) (int, error) { return 0, io.EOF }
```

- [ ] **Step 6:** 跑包测试（在 unix 上）：

```bash
go test ./pkg/ptyhost/...
```

Expected: PASS（如果 `TestShutdown_GracefulThenKill` 在 CI 上 flaky，把 graceful 改大一点；本地 200ms 通常稳）。

- [ ] **Step 7:** 跑全仓库测试。

```bash
make test
```

Expected: 全绿。

- [ ] **Step 8:** `make lint` + `make fmt`。

```bash
make fmt
make lint
```

Expected: 无 diff（fmt 没东西改），lint 全绿。

- [ ] **Step 9:** 提交。

```bash
git add pkg/ptyhost/host_unix.go pkg/ptyhost/host_other.go pkg/ptyhost/host_unix_test.go go.mod go.sum
git commit -m "feat(ptyhost): add Host with spawn/resize/shutdown/wait on unix"
```

---

## Task 6: `pkg/ptyclient` types + Client 主循环

**Files:**
- Create: `pkg/ptyclient/doc.go`
- Create: `pkg/ptyclient/types.go`
- Create: `pkg/ptyclient/client.go`
- Create: `pkg/ptyclient/client_test.go`

设计要点：
- Client 不依赖任何具体 gRPC stub，而是依赖一对 `Stream` 接口（`Send(ClientFrame)` / `Recv() (ServerFrame, error)`）。这样测试用 fake stream 就能跑，不用拉真服务。
- Client 不依赖 `os.Stdin/os.Stdout`，而是接受 `io.Reader` / `io.Writer`，便于测试。
- Resize 数据源也是接口（`<-chan WindowSize`），由调用方按平台决定怎么填。

- [ ] **Step 1:** 写 `pkg/ptyclient/doc.go`：

```go
// Package ptyclient bridges a local terminal to a server-side PTY over a
// transport-agnostic Stream interface.
//
// The CLI binary at app/clientpty will instantiate a Client with: os.Stdin,
// os.Stdout, a SIGWINCH-driven resize source, and the Send/Recv pair of a
// real grpc.ClientStream. Tests substitute io.Pipes and a fake stream.
//
// See docs/superpowers/specs/2026-04-19-remote-claudecode-pty-design.md.
package ptyclient
```

- [ ] **Step 2:** 写 `pkg/ptyclient/types.go`：

```go
package ptyclient

import (
	"errors"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// Stream is the minimal bidi interface the client needs from a transport.
// It mirrors the surface of a generated grpc.ClientStream wrapper without
// depending on grpc directly.
type Stream interface {
	Send(*remotefsv1.ClientFrame) error
	Recv() (*remotefsv1.ServerFrame, error)
	CloseSend() error
}

// WindowSize is the local terminal geometry.
type WindowSize struct {
	Cols   uint32
	Rows   uint32
	XPixel uint32
	YPixel uint32
}

// ExitResult is what Run returns when the remote PTY (or stream) terminates.
//
// If Err is non-nil and ServerError is nil, the failure was a transport-level
// problem. If ServerError is non-nil, the server explicitly rejected or aborted
// the session and the kind/message are surfaced to the caller (CLI maps to
// exit code).
type ExitResult struct {
	Code        int32
	Signal      uint32
	ServerError *remotefsv1.Error
	Err         error
}

// ErrFirstFrameNotAttached is returned when the server's first response is
// neither Attached nor Error.
var ErrFirstFrameNotAttached = errors.New("ptyclient: first server frame is not attached/error")
```

- [ ] **Step 3:** 写失败测试 `pkg/ptyclient/client_test.go`：

```go
package ptyclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/ptyclient"
)

// fakeStream is a hand-rolled bidi stream backed by two channels.
type fakeStream struct {
	sent     chan *remotefsv1.ClientFrame
	incoming chan *remotefsv1.ServerFrame
	closed   chan struct{}
	once     sync.Once
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		sent:     make(chan *remotefsv1.ClientFrame, 32),
		incoming: make(chan *remotefsv1.ServerFrame, 32),
		closed:   make(chan struct{}),
	}
}

func (f *fakeStream) Send(c *remotefsv1.ClientFrame) error {
	select {
	case <-f.closed:
		return io.EOF
	case f.sent <- c:
		return nil
	}
}

func (f *fakeStream) Recv() (*remotefsv1.ServerFrame, error) {
	select {
	case <-f.closed:
		return nil, io.EOF
	case sf, ok := <-f.incoming:
		if !ok {
			return nil, io.EOF
		}
		return sf, nil
	}
}

func (f *fakeStream) CloseSend() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// pushAttached pushes the obligatory first response.
func (f *fakeStream) pushAttached(sessionID, cwd string) {
	f.incoming <- &remotefsv1.ServerFrame{Payload: &remotefsv1.ServerFrame_Attached{
		Attached: &remotefsv1.Attached{SessionId: sessionID, Cwd: cwd},
	}}
}

func (f *fakeStream) pushStdout(b []byte) {
	f.incoming <- &remotefsv1.ServerFrame{Payload: &remotefsv1.ServerFrame_Stdout{Stdout: b}}
}

func (f *fakeStream) pushExited(code int32, sig uint32) {
	f.incoming <- &remotefsv1.ServerFrame{Payload: &remotefsv1.ServerFrame_Exited{
		Exited: &remotefsv1.Exited{Code: code, Signal: sig},
	}}
	close(f.incoming)
}

func (f *fakeStream) pushError(kind remotefsv1.Error_Kind, msg string) {
	f.incoming <- &remotefsv1.ServerFrame{Payload: &remotefsv1.ServerFrame_Error{
		Error: &remotefsv1.Error{Kind: kind, Message: msg},
	}}
	close(f.incoming)
}

func TestClient_HappyPath(t *testing.T) {
	stream := newFakeStream()
	stdin := bytes.NewBufferString("hello\n")
	var stdout bytes.Buffer
	resize := make(chan ptyclient.WindowSize, 1)

	cli := ptyclient.New(ptyclient.Config{
		Stream:  stream,
		Stdin:   io.NopCloser(stdin),
		Stdout:  &stdout,
		Resizes: resize,
		Attach: ptyclient.AttachParams{
			InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24},
			Term:        "xterm-256color",
		},
	})

	go func() {
		// 模拟 server：先回 attached，再回一些 stdout，再回 exited(0)
		stream.pushAttached("sess-1", "/workspace/u1")
		stream.pushStdout([]byte("world\n"))
		// 给 client 一点时间把 stdin 帧推进来
		time.Sleep(20 * time.Millisecond)
		stream.pushExited(0, 0)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res := cli.Run(ctx)

	require.Nil(t, res.Err)
	require.Nil(t, res.ServerError)
	require.Equal(t, int32(0), res.Code)
	require.Contains(t, stdout.String(), "world")

	// First sent frame must be the AttachReq.
	first := <-stream.sent
	require.NotNil(t, first.GetAttach())
	require.Equal(t, "xterm-256color", first.GetAttach().GetTerm())
	require.Equal(t, uint32(80), first.GetAttach().GetInitialSize().GetCols())
}

func TestClient_ServerErrorBeforeAttached(t *testing.T) {
	stream := newFakeStream()
	cli := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin:  io.NopCloser(bytes.NewBufferString("")),
		Stdout: io.Discard,
	})

	go stream.pushError(remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, "no daemon")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res := cli.Run(ctx)

	require.NotNil(t, res.ServerError)
	require.Equal(t, remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, res.ServerError.GetKind())
}

func TestClient_FirstFrameWrongType(t *testing.T) {
	stream := newFakeStream()
	cli := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin:  io.NopCloser(bytes.NewBufferString("")),
		Stdout: io.Discard,
	})

	go func() {
		// 直接发 stdout 而非 attached → client 应当报错退出
		stream.pushStdout([]byte("rogue"))
		close(stream.incoming)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res := cli.Run(ctx)

	require.True(t, errors.Is(res.Err, ptyclient.ErrFirstFrameNotAttached),
		"unexpected err: %v", res.Err)
}

func TestClient_ResizeForwarded(t *testing.T) {
	stream := newFakeStream()
	resize := make(chan ptyclient.WindowSize, 1)

	cli := ptyclient.New(ptyclient.Config{
		Stream:  stream,
		Stdin:   io.NopCloser(bytes.NewBufferString("")),
		Stdout:  io.Discard,
		Resizes: resize,
		Attach:  ptyclient.AttachParams{InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24}},
	})

	go func() {
		stream.pushAttached("s", "/workspace/u")
		// 推一次 resize 进 client
		time.Sleep(20 * time.Millisecond)
		resize <- ptyclient.WindowSize{Cols: 132, Rows: 50}
		time.Sleep(20 * time.Millisecond)
		stream.pushExited(0, 0)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res := cli.Run(ctx)
	require.Nil(t, res.Err)

	var sawResize bool
	for {
		select {
		case f := <-stream.sent:
			if r := f.GetResize(); r != nil &&
				r.GetCols() == 132 && r.GetRows() == 50 {
				sawResize = true
			}
		default:
			require.True(t, sawResize, "expected a resize frame to be sent")
			return
		}
	}
}
```

- [ ] **Step 4:** 跑测试，确认全失败。

```bash
go test ./pkg/ptyclient/...
```

Expected: 编译失败 / `ptyclient.New` / `ptyclient.Client.Run` 未定义。

- [ ] **Step 5:** 写 `pkg/ptyclient/client.go`：

```go
package ptyclient

import (
	"bufio"
	"context"
	"errors"
	"io"

	"golang.org/x/sync/errgroup"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

const defaultStdinChunk = 64 * 1024

// AttachParams populate the first ClientFrame{attach} sent on the stream.
type AttachParams struct {
	SessionID   string
	InitialSize WindowSize
	Term        string
}

// Config wires the client up with its IO and transport collaborators.
//
// Stdin is read in chunks up to FrameMax (default 64 KiB) and forwarded as
// ClientFrame.stdin. Resizes is a channel of window-size updates produced by
// the platform-specific resize source. Stream is any bidi stream conforming
// to the Stream interface; in production this is a grpc.ClientStream wrapper.
type Config struct {
	Stream   Stream
	Stdin    io.ReadCloser
	Stdout   io.Writer
	Resizes  <-chan WindowSize
	Attach   AttachParams
	FrameMax int
}

// Client is a one-shot PTY bridge. Use New + Run; do not reuse.
type Client struct {
	cfg Config
}

// New returns a Client. It does not perform IO yet.
func New(cfg Config) *Client {
	if cfg.FrameMax <= 0 {
		cfg.FrameMax = defaultStdinChunk
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	return &Client{cfg: cfg}
}

// Run drives the bridge until the remote PTY exits, an error occurs, or ctx
// is cancelled. It always closes the stream's send side before returning.
func (c *Client) Run(ctx context.Context) ExitResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := c.cfg.Stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Attach{Attach: &remotefsv1.AttachReq{
			SessionId:   c.cfg.Attach.SessionID,
			InitialSize: toProtoSize(c.cfg.Attach.InitialSize),
			Term:        c.cfg.Attach.Term,
		}},
	}); err != nil {
		_ = c.cfg.Stream.CloseSend()
		return ExitResult{Err: err}
	}

	first, err := c.cfg.Stream.Recv()
	if err != nil {
		_ = c.cfg.Stream.CloseSend()
		return ExitResult{Err: err}
	}
	if e := first.GetError(); e != nil {
		_ = c.cfg.Stream.CloseSend()
		return ExitResult{ServerError: e}
	}
	if first.GetAttached() == nil {
		_ = c.cfg.Stream.CloseSend()
		return ExitResult{Err: ErrFirstFrameNotAttached}
	}

	var (
		exited *remotefsv1.Exited
		serr   *remotefsv1.Error
	)

	g, gctx := errgroup.WithContext(ctx)

	// stdin pump
	g.Go(func() error {
		buf := make([]byte, c.cfg.FrameMax)
		reader := bufio.NewReaderSize(c.cfg.Stdin, c.cfg.FrameMax)
		for {
			select {
			case <-gctx.Done():
				return gctx.Err()
			default:
			}
			n, err := reader.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				if sendErr := c.cfg.Stream.Send(&remotefsv1.ClientFrame{
					Payload: &remotefsv1.ClientFrame_Stdin{Stdin: cp},
				}); sendErr != nil {
					return sendErr
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	})

	// resize pump
	g.Go(func() error {
		if c.cfg.Resizes == nil {
			<-gctx.Done()
			return nil
		}
		for {
			select {
			case <-gctx.Done():
				return nil
			case ws, ok := <-c.cfg.Resizes:
				if !ok {
					return nil
				}
				if err := c.cfg.Stream.Send(&remotefsv1.ClientFrame{
					Payload: &remotefsv1.ClientFrame_Resize{Resize: toProtoSize(ws)},
				}); err != nil {
					return err
				}
			}
		}
	})

	// stdout pump (drives termination)
	g.Go(func() error {
		// When stdout pump returns, force the other pumps to wake up:
		// cancel the shared context, and close stdin so any blocking Read
		// unblocks.
		defer cancel()
		defer func() {
			if c.cfg.Stdin != nil {
				_ = c.cfg.Stdin.Close()
			}
		}()
		for {
			sf, err := c.cfg.Stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			switch p := sf.GetPayload().(type) {
			case *remotefsv1.ServerFrame_Stdout:
				if _, werr := c.cfg.Stdout.Write(p.Stdout); werr != nil {
					return werr
				}
			case *remotefsv1.ServerFrame_Exited:
				exited = p.Exited
				return nil
			case *remotefsv1.ServerFrame_Error:
				serr = p.Error
				return nil
			}
		}
	})

	waitErr := g.Wait()
	_ = c.cfg.Stream.CloseSend()

	res := ExitResult{ServerError: serr}
	if exited != nil {
		res.Code = exited.GetCode()
		res.Signal = exited.GetSignal()
	}
	if res.ServerError == nil && exited == nil && waitErr != nil &&
		!errors.Is(waitErr, context.Canceled) {
		res.Err = waitErr
	}
	return res
}

func toProtoSize(ws WindowSize) *remotefsv1.Resize {
	return &remotefsv1.Resize{
		Cols:   ws.Cols,
		Rows:   ws.Rows,
		XPixel: ws.XPixel,
		YPixel: ws.YPixel,
	}
}
```

- [ ] **Step 6:** 跑测试。

```bash
go test ./pkg/ptyclient/...
```

Expected: PASS。

- [ ] **Step 7:** 跑全仓库测试。

```bash
make test
```

Expected: 全绿。

- [ ] **Step 8:** 提交。

```bash
git add pkg/ptyclient/doc.go pkg/ptyclient/types.go pkg/ptyclient/client.go pkg/ptyclient/client_test.go
git commit -m "feat(ptyclient): add transport-agnostic bridge with stdin/stdout/resize pumps"
```

---

## Task 7: `pkg/ptyclient` resize 数据源（跨平台）

**Files:**
- Create: `pkg/ptyclient/resize_unix.go`（`//go:build unix`）
- Create: `pkg/ptyclient/resize_windows.go`（`//go:build windows`）
- Create: `pkg/ptyclient/resize_test.go`

设计：暴露一个 `NewSIGWINCHResize(ctx, fd) <-chan WindowSize` （unix）与 `NewPollResize(ctx, fd, interval) <-chan WindowSize`（windows）。Test 验证 unix 的 SIGWINCH 路径在收到信号时确实推 chan，windows 的 poll 路径要靠 mock，本阶段只在 unix 上测。

- [ ] **Step 1:** 写测试 `pkg/ptyclient/resize_test.go`：

```go
//go:build unix

package ptyclient_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/ptyclient"
)

// TestSIGWINCHResize_EmitsOnSignal can't easily fake a real TTY in test
// (golang.org/x/term needs a real fd). We instead verify the channel emits
// the *current* size on signal by sending SIGWINCH to the current process
// and reading from the chan with a generous timeout. The size value is not
// validated — only the fact that an event arrived.
func TestSIGWINCHResize_EmitsOnSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// fd=2 (stderr) is sometimes a TTY when running locally, but in CI it
	// often is not. The test must still pass: when fd is not a TTY,
	// querySize returns a zero size, but the channel should still emit
	// in response to SIGWINCH.
	ch := ptyclient.NewSIGWINCHResize(ctx, int(syscall.Stderr))

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGWINCH))

	select {
	case <-ch:
		// got an event, that's all we need
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive a resize event after SIGWINCH")
	}
}
```

- [ ] **Step 2:** 跑测试，预期失败。

```bash
go test ./pkg/ptyclient/...
```

Expected: 编译失败 / `NewSIGWINCHResize` 未定义。

- [ ] **Step 3:** 写 `pkg/ptyclient/resize_unix.go`：

```go
//go:build unix

package ptyclient

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// NewSIGWINCHResize returns a channel that emits the current terminal size
// once at startup and again on every SIGWINCH until ctx is done.
//
// fd should refer to the controlling tty (typically os.Stdout.Fd()). When
// fd is not a tty, the emitted size is zero — callers should still treat
// the event as a "size may have changed" signal.
func NewSIGWINCHResize(ctx context.Context, fd int) <-chan WindowSize {
	out := make(chan WindowSize, 4)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGWINCH)

	send := func() {
		ws := querySize(fd)
		select {
		case out <- ws:
		default:
			// drop on backpressure: only the latest size matters.
		}
	}

	go func() {
		defer signal.Stop(sigs)
		defer close(out)
		send()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigs:
				send()
			}
		}
	}()

	return out
}

func querySize(fd int) WindowSize {
	if !term.IsTerminal(fd) {
		return WindowSize{}
	}
	w, h, err := term.GetSize(fd)
	if err != nil {
		return WindowSize{}
	}
	return WindowSize{Cols: uint32(w), Rows: uint32(h)}
}
```

- [ ] **Step 4:** 写 `pkg/ptyclient/resize_windows.go`：

```go
//go:build windows

package ptyclient

import (
	"context"
	"time"

	"golang.org/x/term"
)

// NewPollResize returns a channel that polls the terminal size at the given
// interval and emits whenever it changes.
//
// Windows lacks SIGWINCH, so the CLI binary will use this. interval==0 falls
// back to 250 ms.
func NewPollResize(ctx context.Context, fd int, interval time.Duration) <-chan WindowSize {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	out := make(chan WindowSize, 4)

	go func() {
		defer close(out)

		query := func() WindowSize {
			w, h, err := term.GetSize(fd)
			if err != nil {
				return WindowSize{}
			}
			return WindowSize{Cols: uint32(w), Rows: uint32(h)}
		}

		last := query()
		select {
		case out <- last:
		default:
		}

		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := query()
				if cur != last {
					last = cur
					select {
					case out <- cur:
					default:
					}
				}
			}
		}
	}()

	return out
}
```

- [ ] **Step 5:** 拉依赖（如果未引入）。

```bash
go get golang.org/x/term@v0.42.0
go mod tidy
```

- [ ] **Step 6:** 跑测试（unix 上）。

```bash
go test ./pkg/ptyclient/...
```

Expected: PASS。

- [ ] **Step 7:** Windows 交叉编译验证（即使在 unix 开发机上也能跑）：

```bash
GOOS=windows GOARCH=amd64 go build ./pkg/ptyclient/...
```

Expected: 编译通过；如果报 `term.GetSize` 在 windows 不可用，按报错调整 import / 实现。

- [ ] **Step 8:** 提交。

```bash
git add pkg/ptyclient/resize_unix.go pkg/ptyclient/resize_windows.go pkg/ptyclient/resize_test.go go.mod go.sum
git commit -m "feat(ptyclient): add cross-platform resize source (SIGWINCH + windows poll)"
```

---

## Task 8: 阶段验收

**Files:**
- Modify: `docs/exec-plan/active/202604191500-phase8a-pty-foundation/plan.md`（勾选所有 Todo）
- Modify: `docs/exec-plan/active/202604191500-phase8a-pty-foundation/开发流程.md`（按时间倒序追加每条命令的实际输出摘要）
- Modify: `docs/exec-plan/active/202604191500-phase8a-pty-foundation/测试错误.md`（如执行过程中遇到失败，记录复盘；无失败则保留模板说明无失败）
- Move: `docs/exec-plan/active/202604191500-phase8a-pty-foundation/` → `docs/exec-plan/completed/202604191500-phase8a-pty-foundation/`
- Create: `docs/exec-plan/completed/202604191500-phase8a-pty-foundation/202604191500-phase8a-pty-foundation.md`（同名完成摘要）

- [ ] **Step 1:** 跑全套质量门。

```bash
make fmt
make lint
make test
```

Expected: fmt 没有 diff；lint 全绿；test 全绿（包括新增的 ptyhost / ptyclient / proto smoke / config）。
如有失败：把现象与排查步骤写入 `测试错误.md`，修复后重跑直至全绿。

- [ ] **Step 2:** 跨平台编译冒烟（避免本阶段产物在 Phase 8b 才发现 Windows 编译挂掉）。

```powershell
$env:GOOS = "windows"; $env:GOARCH = "amd64"; go build ./...
$env:GOOS = "linux";   $env:GOARCH = "amd64"; go build ./...
$env:GOOS = "darwin";  $env:GOARCH = "amd64"; go build ./...
Remove-Item Env:GOOS, Env:GOARCH
```

Expected: 三平台全部编译成功（其中 `pkg/ptyhost` 的 windows 走 `host_other.go` stub）。

- [ ] **Step 3:** 在本 plan.md 顶部 "进度" 区把所有 Task 勾上。

- [ ] **Step 4:** 写 `开发流程.md`：日期、执行了哪些命令、输出摘要、与 plan 的偏离（如果有）。

- [ ] **Step 5:** 写 `测试错误.md`：如果过程一帆风顺，明确写"本阶段无测试失败"。

- [ ] **Step 6:** 移动目录并写完成摘要。

```bash
git mv docs/exec-plan/active/202604191500-phase8a-pty-foundation docs/exec-plan/completed/202604191500-phase8a-pty-foundation
```

新建 `docs/exec-plan/completed/202604191500-phase8a-pty-foundation/202604191500-phase8a-pty-foundation.md`，内容遵循 CLAUDE.md 模板：

```markdown
# Phase 8a — Remote PTY 基础包与协议（完成摘要）

- 完成状态：done
- 验收命令：`make fmt` / `make lint` / `make test` 全绿；三平台 `go build ./...` 通过
- 与 plan 偏离：（如无写"无"，否则列出每一条偏离与原因）
- 遗留问题：进入 Phase 8b（service 装配与集成测试）的前置条件已经齐备
- 后续阶段入口：在 `docs/exec-plan/active/` 下另开 `…-phase8b-pty-service-wiring/` 三件套
```

- [ ] **Step 7:** 提交收尾。

```bash
git add docs/exec-plan/
git commit -m "docs(phase8a): mark complete, archive exec-plan"
```

---

## 自检 — 与 spec 的覆盖映射

| spec 章节 | 本阶段对应任务 |
|---|---|
| §3.2 模块划分（`api/proto/.../pty.proto`、`pkg/ptyhost`、`pkg/ptyclient`、`pkg/config` PTY 块） | Task 2 / 4 / 5 / 6 / 7 / 3 |
| §4 proto 定义（service + frames + Error.Kind） | Task 2 |
| §4.1 单帧上限 64 KiB | Task 3（config）+ Task 6（client 默认 FrameMax）|
| §4.3 stdout/stderr 不分流（PTY master 单流） | Task 5（host 实现）|
| §5.2 ptyhost spawn / wait / shutdown / resize | Task 4 / 5 |
| §5.1 ptyclient 主循环（attach 首帧、stdin/stdout/resize 三泵、Error.Kind 退出码） | Task 6 |
| §6 server 端 PTY 配置块 | Task 3 |

不在本阶段范围（明确推迟到后续阶段）：
- §5.2 中的 `RemotePTY.Attach` handler、session/ratelimit 接入、daemon 耦合校验 → Phase 8b
- §7 错误降级矩阵的端到端验收（attach 限流、daemon 断开、client 网断等） → Phase 8b 集成测试
- §8.2 `internal/inmemtest` PTY 子套件、§8.3 `tools/pty-smoke.sh` → Phase 8b/8c
- §10 `docs/ARCHITECTURE.md` 与 `docs/reference/pty-protocol.md` 更新、§11 ROADMAP Phase 8 条目 → Phase 8c
