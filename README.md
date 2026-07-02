# Rclaude

Language: English | [中文](README_ZH.md)

`Rclaude` is a remote file access system. Its goal is not to copy a whole workspace to a cloud machine. Instead, it lets a remote execution environment access a user's local workspace through ordinary file paths.

From the execution side, files still look like normal local filesystem paths. From the system side, the actual source of data remains the user's local machine.

Typical flow:

- A cloud Agent or task runner executes `cat /workspace/alice/main.go`.
- The Server-side FUSE filesystem handles that path.
- The request is forwarded over a bidirectional gRPC stream to Alice's local daemon.
- The daemon reads Alice's real local file and returns the content.

This keeps standard shell tools such as `cat`, `sed`, `grep`, `ls`, `stat`, `mv`, and `rm` usable without a custom file SDK.

## Current Capabilities

The current codebase already provides a working main path:

- `rclaude-daemon` scans a local workspace and connects to the Server with a bidirectional gRPC stream.
- The Server tracks user sessions, file-tree metadata, and content cache.
- On Linux, the Server exposes a FUSE view at `/workspace/{user_id}/`.
- The execution environment can use ordinary filesystem commands against that mount.

Implemented features include:

- read operations: `Lookup`, `Getattr`, `Readdir`, `Open`, `Read`
- write operations: create, overwrite, offset write, append, `mkdir`, `rename`, `delete`, `truncate`
- per-user isolation under `/workspace/{user_id}/`
- file-tree cache for directory and attribute lookups
- whole-file content cache
- small-file prefetch after directory reads
- temporary read-only fallback from cache after daemon disconnect
- sensitive-file filtering for `.env`, private keys, certificates, and custom patterns
- daemon-side read/write byte-rate limiting
- workspace boundary protection and path traversal defense
- YAML configuration with `RCLAUDE_*` environment overrides
- static token authentication mapped to `user_id`
- RemotePTY attach from local `rclaude-claude` to a Server-side PTY process
- Server-side Agent entry through `pty.binary` and `pty.args`, including `claude`, `codex`, or shell-like tools

## Architecture

The system has three main parts:

1. `rclaude-daemon`
2. `rclaude-server`
3. ordinary shell, Agent, or automation running on the Server side

```text
Local workspace
    ^
    | read/write files and watch changes
    v
rclaude-daemon
    ^
    | bidirectional gRPC stream
    v
rclaude-server
    ^
    | FUSE mount
    v
/workspace/{user_id}/...
    ^
    | cat / sed / grep / ls / stat / mv / rm ...
    v
Execution environment
```

Design points:

- The daemon initiates the connection to the Server, so the Server does not need to dial back into a user's local machine.
- FUSE is the primary integration surface.
- Server-side cache and prefetch are built into the architecture.
- The compatibility target is ordinary path-based file semantics, not one specific model or Agent.

More background, deployment, and acceptance records:

- [deploy/minimal/README.md](deploy/minimal/README.md)
- [docs/reference/claude-code-pty-adapter.md](docs/reference/claude-code-pty-adapter.md)
- [docs/exec-plan/active/202607020936-fuse-cwd-pty-adapter/remote-linkage-report.md](docs/exec-plan/active/202607020936-fuse-cwd-pty-adapter/remote-linkage-report.md)
- [docs/exec-plan/active/202607021406-remote-codex-smoke/plan.md](docs/exec-plan/active/202607021406-remote-codex-smoke/plan.md)

## Requirements

- Server side: Linux with usable FUSE support.
- Daemon side: intended to support Linux, macOS, and Windows.
- Go version: this repository's `go.mod` currently pins Go `1.25.2`.

Notes:

- Non-Linux platforms do not mount FUSE in this implementation.
- This repository has a minimal working path, but it is not yet a full production distribution.
- There is no built-in TLS, Docker bundle, systemd unit, installer, audit pipeline, or operations dashboard yet.

## Repository Layout

```text
api/                    gRPC protocol and generated code
app/client/             rclaude-daemon entrypoint
app/clientpty/          rclaude-claude PTY client entrypoint
app/server/             rclaude-server entrypoint
pkg/config/             YAML and environment configuration loading
pkg/auth/               token authentication
pkg/safepath/           workspace path validation and boundary protection
pkg/fstree/             file-tree metadata index
pkg/session/            Server-side user sessions and request routing
pkg/contentcache/       Server-side whole-file content cache
pkg/fusefs/             FUSE filesystem view
pkg/syncer/             daemon-side scan, watch, sync, and request handling
pkg/transport/          gRPC connection and stream wrappers
pkg/ratelimit/          daemon-side byte-rate limiting
internal/inmemtest/     in-memory end-to-end test harness
deploy/minimal/         minimal dual-machine deployment and smoke scripts
tools/                  developer and smoke-test tools
docs/                   references and phase execution records
```

## Build

Install the development tools used by this repository:

```bash
make tools
```

Build the binaries:

```bash
go build -o ./bin/rclaude-server ./app/server
go build -o ./bin/rclaude-daemon ./app/client
go build -o ./bin/rclaude-claude ./app/clientpty
```

Or run a repository-wide build check:

```bash
go build ./...
```

## Quick Start

Prepare a Server config:

```yaml
listen: ":9326"
auth:
  tokens:
    "tok-alice": "alice"
fuse:
  mountpoint: "/workspace"
cache:
  max_bytes: 268435456
prefetch:
  enabled: true
  max_file_bytes: 102400
  max_files_per_dir: 16
request_timeout: 10s
offline_readonly_ttl: 5m
log:
  level: "info"
  format: "text"
pty:
  binary: "claude"
  args: []
  workspace_root: "/workspace"
```

Prepare a daemon config:

```yaml
server:
  address: "127.0.0.1:9326"
  token: "tok-alice"
workspace:
  path: "/absolute/path/to/workspace"
  exclude:
    - ".git"
    - "node_modules"
    - "vendor"
  sensitive_patterns:
    - "secrets/**"
rate_limit:
  read_bytes_per_sec: 0
  write_bytes_per_sec: 0
self_write_ttl: 2s
log:
  level: "info"
  format: "text"
```

Start the Server:

```bash
./bin/rclaude-server --config ./server.yaml
```

Start the daemon:

```bash
./bin/rclaude-daemon --config ./daemon.yaml
```

After startup, the Server side should expose:

```text
/workspace/alice/
```

Example file operations from the Server side:

```bash
ls -la /workspace/alice
cat /workspace/alice/README.md
grep -R "TODO" /workspace/alice
mkdir /workspace/alice/tmp
printf 'hello\n' > /workspace/alice/tmp/demo.txt
mv /workspace/alice/tmp/demo.txt /workspace/alice/tmp/demo2.txt
truncate -s 2 /workspace/alice/tmp/demo2.txt
rm /workspace/alice/tmp/demo2.txt
```

## Remote PTY And Agent Entry

Rclaude keeps interactive Agent support split into two paths:

- File path: `rclaude-daemon` exposes the user's local workspace through `RemoteFS.Connect`; the Server publishes it through FUSE at `/workspace/{user_id}`.
- Terminal path: `rclaude-claude` uses `RemotePTY.Attach` to forward only terminal bytes, resize events, exit status, and errors.

The Server starts `pty.binary` inside `/workspace/{user_id}`. Therefore, the actual `claude` or `codex` process runs on the Server machine, not on the daemon machine. The Server OS user must be able to resolve that binary and must have the login state or environment variables required by that CLI.

Example PTY config for Claude Code:

```yaml
pty:
  binary: "claude"
  args: []
  workspace_root: "/workspace"
  env_passthrough:
    - "TERM"
    - "LANG"
    - "LC_ALL"
    - "LC_CTYPE"
    - "PATH"
    - "HOME"
    - "SHELL"
    - "CLAUDE_CONFIG_DIR"
  frame_max_bytes: 65536
```

Example PTY config for Codex:

```yaml
pty:
  binary: "/root/.local/bin/codex"
  args: []
  workspace_root: "/workspace"
```

For a repeatable Codex read check, the Server can launch `codex exec` through fixed `pty.args`:

```yaml
pty:
  binary: "/root/.local/bin/codex"
  args:
    - "exec"
    - "--skip-git-repo-check"
    - "--sandbox"
    - "read-only"
    - "Read README.md in the current directory and reply with the exact first line only."
```

The minimal dual-machine deployment guide is [deploy/minimal/README.md](deploy/minimal/README.md). Recommended validation order:

1. Run `preflight-server.sh` on the Server machine.
2. Run `preflight-daemon.sh` on the daemon machine.
3. Run `smoke-remote.sh <user_id> <expected_file>` on the Server to verify the FUSE file plane.
4. Temporarily point `pty.binary` to `/bin/sh`, then run `RCLAUDE_PTY_MODE=scripted tools/pty-smoke.sh <daemon.yaml>` to verify PTY transport and FUSE reads.
5. Restore `pty.binary` to `claude`, `codex`, or the target CLI, then run `deploy/minimal/start-pty.sh <daemon.yaml>` for real interactive acceptance.

Current observed status:

- `/bin/sh` scripted PTY plus FUSE file reads pass.
- Codex CLI TUI attach, cwd `/workspace/{user_id}`, `codex exec` reading a daemon-backed FUSE file, and remote exit code `0` propagation pass.
- Claude Code TUI can render through RemotePTY, but main-prompt acceptance depends on Claude Code onboarding/login for the Server OS user. A Claude login on the daemon machine is not reused by the Server-side process.

## Environment Overrides

Configuration is loaded with `viper` and supports `RCLAUDE_*` environment overrides.

Example:

```bash
export RCLAUDE_SERVER_ADDRESS=127.0.0.1:9999
./bin/rclaude-daemon --config ./daemon.yaml
```

Dots map to underscores:

- `server.address` -> `RCLAUDE_SERVER_ADDRESS`
- `fuse.mountpoint` -> `RCLAUDE_FUSE_MOUNTPOINT`

## Tests And Development Commands

Standard workflow:

```bash
make fmt
make lint
make test
```

Other common commands:

```bash
make all
make check
make test-cover
go build ./...
```

The current test baseline includes:

- package-level unit tests
- cross-platform in-memory integration tests
- Linux real-FUSE smoke tests
- Linux RemotePTY plus FUSE smoke compilation checks

The real Linux FUSE tests exercise the path `Mount -> kernel/FUSE -> session -> daemon`. If the environment does not support FUSE, those tests skip by design.

## Current Limits

- The Server must run on Linux with FUSE support.
- Authentication is currently static token mapping, not a complete identity system.
- The current shape is better suited to small teams, roughly 1-20 users.
- Rclaude does not mirror a complete workspace to the Server by design.
- Disconnect handling only supports temporary read-only cache fallback, not offline write-back.
- Production packaging such as Docker, systemd, TLS, log rotation, and operational dashboards is not included yet.

## Documentation Entry Points

- Chinese README: [README_ZH.md](README_ZH.md)
- Minimal dual-machine deployment: [deploy/minimal/README.md](deploy/minimal/README.md)
- Claude Code PTY adapter reference: [docs/reference/claude-code-pty-adapter.md](docs/reference/claude-code-pty-adapter.md)
- FUSE cwd / PTY adapter linkage report: [docs/exec-plan/active/202607020936-fuse-cwd-pty-adapter/remote-linkage-report.md](docs/exec-plan/active/202607020936-fuse-cwd-pty-adapter/remote-linkage-report.md)
- Codex smoke phase plan: [docs/exec-plan/active/202607021406-remote-codex-smoke/plan.md](docs/exec-plan/active/202607021406-remote-codex-smoke/plan.md)
- Active phase records: [docs/exec-plan/active/](docs/exec-plan/active/)
