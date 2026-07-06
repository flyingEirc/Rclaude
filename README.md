# Rclaude

Language: English | [中文](README_ZH.md)

`Rclaude` is a remote file access system. Its goal is not to copy a whole workspace to a cloud machine. Instead, it exposes a local workspace to a remote execution environment through ordinary file paths.

From the execution side, files still look like normal local filesystem paths. From the system side, the actual source of data remains the daemon-side workspace.

Typical flow:

- A cloud Agent or task runner executes `cat /workspace/{user_id}/main.go`.
- The Server-side FUSE filesystem handles that path.
- The request is forwarded over a bidirectional gRPC stream to the daemon connected for that user.
- The daemon reads the configured local workspace and returns the content.

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
- unified `rclaude` local entry that starts the daemon and the RemotePTY attach together, with dependency-ordered startup and coordinated retry (`pkg/startup`)
- Server-side terminal passthrough that defaults to the user's interactive login shell in `/workspace/{user_id}` (ls/cd, then launch `claude`/`codex` yourself); pin a fixed program with `pty.binary` / `pty.args` if you prefer
- graceful shutdown on terminal close: SIGINT/SIGTERM/SIGHUP drain in-flight file streams and the PTY before the daemon and session exit
- file-based structured logging (JSON by default, size/age rotation) that never writes to the terminal, so the PTY passthrough stays clean
- optional audit log persisting remote file operations to SQLite / MySQL / PostgreSQL

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

On the daemon machine these run through the unified `rclaude` entry
(`app/rclaude`), which starts the daemon (`RemoteFS.Connect`) and the terminal
attach (`RemotePTY.Attach`) together and coordinates their startup so the PTY
only attaches after the daemon has registered. `rclaude-daemon` (`app/client`)
and `rclaude-claude` (`app/clientpty`) remain as split single-purpose entries
for diagnostics.

Design points:

- The daemon initiates the connection to the Server, so the Server does not need to dial back into a user's local machine.
- FUSE is the primary integration surface.
- Server-side cache and prefetch are built into the architecture.
- The two client roles (file sync and terminal) share one config and one coordinated lifecycle in the unified entry, but stay independent gRPC streams.
- The compatibility target is ordinary path-based file semantics, not one specific model or Agent.

For the minimal dual-machine deployment and manual acceptance flow, see [deploy/minimal/README.md](deploy/minimal/README.md).

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
app/rclaude/            rclaude unified local entry (daemon + PTY, coordinated startup)
app/server/             rclaude-server entrypoint
app/client/             rclaude-daemon entrypoint (daemon only, split diagnostics)
app/clientpty/          rclaude-claude PTY client entrypoint (PTY only, split diagnostics)
pkg/config/             YAML and environment configuration loading
pkg/logx/               file-based structured logging (never writes to the terminal)
pkg/startup/            startup coordinator for the unified entry (dependency gating + retries)
pkg/auth/               token authentication
pkg/safepath/           workspace path validation and boundary protection
pkg/fstree/             file-tree metadata index
pkg/session/            Server-side user sessions and request routing
pkg/contentcache/       Server-side whole-file content cache
pkg/fusefs/             FUSE filesystem view
pkg/syncer/             daemon-side scan, watch, sync, and request handling
pkg/ptyhost/            Server-side PTY child-process spawn (login shell or pinned binary)
pkg/ptyservice/         Server-side RemotePTY gRPC service
pkg/ptyclient/          daemon-side terminal <-> PTY gRPC bridge
pkg/ptyattach/          local terminal attach (raw mode, resize, exit codes)
pkg/audit/              optional DB audit log for remote file operations
pkg/transport/          gRPC connection and stream wrappers
pkg/ratelimit/          daemon-side byte-rate limiting
internal/inmemtest/     in-memory end-to-end test harness
internal/testutil/      shared test fixtures and helpers
deploy/minimal/         minimal remote/local test closure (configs + start/preflight scripts)
tools/                  proto codegen tool-version pin (tools.go)
```

## Build

Install the development tools used by this repository:

```bash
make tools
```

Build the binaries:

```bash
# Server (remote Linux) and the unified local entry cover the normal flow.
go build -o ./bin/rclaude-server ./app/server
go build -o ./bin/rclaude ./app/rclaude
# Optional split single-purpose entries, useful for diagnostics.
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
listen: ":9326"                      # gRPC listen address; ":port" binds all interfaces. Required.
auth:
  tokens:
    "example-token": "example-user"  # token -> user_id mapping; a daemon trades its token for a user_id. At least one entry required.
fuse:
  mountpoint: "/workspace"           # FUSE mount root, must be an absolute path; each user is mounted at {mountpoint}/{user_id}.
cache:
  max_bytes: 268435456               # Server-side whole-file content cache cap in bytes; <=0 disables the cache.
prefetch:
  enabled: true                      # Whether to prefetch small files after a directory read (needs cache.max_bytes > 0, otherwise skipped automatically).
  max_file_bytes: 102400             # Max single-file size in bytes eligible for prefetch; larger files are skipped.
  max_files_per_dir: 16              # Max number of files prefetched per directory read.
request_timeout: 10s                # Per-request timeout (Lookup/Getattr/Read/Write, etc.); <=0 falls back to the 10s default.
offline_readonly_ttl: 5m            # How long cached content stays read-only accessible after the daemon disconnects.
log:
  level: "info"                      # Log level: debug | info | warn | error
  format: "text"                     # Log format: json (default) | text
pty:
  # binary unset (default): spawn the user's interactive login shell.
  args: []                           # Args passed to binary.
  workspace_root: "/workspace"       # PTY working-directory root, must be absolute; should match fuse.mountpoint. Actual cwd is {workspace_root}/{user_id}.
```

Prepare a daemon config:

```yaml
server:
  address: "127.0.0.1:9326"           # Server gRPC address. Required.
  token: "example-token"              # Must match one of the tokens in the Server's auth.tokens.
workspace:
  path: "/absolute/path/to/workspace" # Local workspace root, must be an absolute path.
  exclude:                            # Glob patterns excluded from scanning/watching.
    - ".git"
    - "node_modules"
    - "vendor"
  sensitive_patterns:                 # Extra sensitive-path patterns on top of the built-in rules (.env, private keys, certs, etc.).
    - "secrets/**"
rate_limit:
  read_bytes_per_sec: 0               # Byte-rate cap on content the daemon returns for reads; <=0 means unlimited.
  write_bytes_per_sec: 0              # Byte-rate cap on writes the daemon flushes to disk; <=0 means unlimited.
self_write_ttl: 2s                    # Window during which the daemon ignores its own write-triggered filesystem events, to avoid a feedback loop; <=0 falls back to the 2s default.
log:
  level: "info"                       # Log level: debug | info | warn | error
  format: "text"                      # Log format: json (default) | text
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
/workspace/example-user/
```

Example file operations from the Server side:

```bash
ls -la /workspace/example-user
cat /workspace/example-user/README.md
grep -R "TODO" /workspace/example-user
mkdir /workspace/example-user/tmp
printf 'hello\n' > /workspace/example-user/tmp/demo.txt
mv /workspace/example-user/tmp/demo.txt /workspace/example-user/tmp/demo2.txt
truncate -s 2 /workspace/example-user/tmp/demo2.txt
rm /workspace/example-user/tmp/demo2.txt
```

## Remote PTY And Agent Entry

Rclaude keeps interactive Agent support split into two paths:

- File path: `rclaude-daemon` exposes the user's local workspace through `RemoteFS.Connect`; the Server publishes it through FUSE at `/workspace/{user_id}`.
- Terminal path: `rclaude-claude` uses `RemotePTY.Attach` to forward only terminal bytes, resize events, exit status, and errors.

By default (no `pty.binary`) the Server spawns the user's interactive login shell inside `/workspace/{user_id}`, so the passthrough is a working terminal: run `ls`/`cd`, then start `claude`, `codex`, or anything else yourself. Setting `pty.binary` pins a fixed program launched in the same directory instead. Either way the process runs on the Server machine, not on the daemon machine, so the Server OS user must be able to resolve the shell/binary and have the login state or environment variables that CLI needs.

Example PTY config that pins Claude Code (omit `pty.binary` to get the login shell instead):

```yaml
pty:
  binary: "claude"                    # Fixed executable name/path to launch; leave unset (default) to spawn the login shell instead.
  args: []                            # Args passed to binary.
  workspace_root: "/workspace"        # PTY working-directory root, must be absolute; should match fuse.mountpoint. Actual cwd is {workspace_root}/{user_id}.
  env_passthrough:                    # Whitelist of env vars forwarded from the Server process into the PTY child; this list is also the built-in default.
    - "TERM"
    - "LANG"
    - "LC_ALL"
    - "LC_CTYPE"
    - "PATH"
    - "HOME"
    - "SHELL"
    - "CLAUDE_CONFIG_DIR"
  frame_max_bytes: 65536              # Max bytes per PTY frame, must be > 0; defaults to 65536 (64 KiB).
```

Example PTY config for Codex:

```yaml
pty:
  binary: "/root/.local/bin/codex"    # Fixed executable path (must resolve on the Server machine).
  args: []                            # Args passed to binary.
  workspace_root: "/workspace"        # PTY working-directory root, must be absolute.
```

For a repeatable Codex read check, the Server can launch `codex exec` through fixed `pty.args`:

```yaml
pty:
  binary: "/root/.local/bin/codex"    # Fixed executable path.
  args:                                # Args passed to binary; here it pins every attach to one read-only codex exec run.
    - "exec"
    - "--skip-git-repo-check"
    - "--sandbox"
    - "read-only"
    - "Read README.md in the current directory and reply with the exact first line only."
```

The minimal remote/local test closure is [deploy/minimal/README.md](deploy/minimal/README.md). Recommended order:

1. Preflight: run `preflight-daemon.sh` locally (and `preflight-server.sh` on the Server).
2. Start the Server: `deploy/minimal/start-server.sh` cross-builds, ships, and starts `rclaude-server` on the remote.
3. Start locally: `deploy/minimal/start-rclaude.sh` runs the unified daemon + PTY attach and lands you in the remote session.

Current observed status:

- `/bin/sh` scripted PTY plus FUSE file reads pass.
- Codex CLI TUI attach, cwd `/workspace/{user_id}`, `codex exec` reading a daemon-backed FUSE file, and remote exit code `0` propagation pass.
- Claude Code TUI can render through RemotePTY, but main-prompt acceptance depends on Claude Code onboarding/login for the Server OS user. A Claude login on the daemon machine is not reused by the Server-side process.

## Logging, Startup, And Shutdown

Logs never go to the terminal. Because the unified `rclaude` entry hands the
terminal to the remote PTY, all diagnostics are written to a rotating log file
instead, so terminal output stays a clean passthrough. The `log` block controls
this on both sides:

```yaml
log:
  level: "info"         # Log level: debug | info | warn | error
  format: "json"        # json (default) | text
  # dir: ""             # log directory; defaults to ~/.rclaude/logs
  # max_size_mb: 100    # rotate after this size (MB)
  # max_backups: 3      # rotated files to keep
  # max_age_days: 7     # days to keep rotated files
```

The unified entry writes `rclaude.log`; the split entries write
`rclaude-daemon.log` and their own files. On the terminal you only see one
status line per component (`daemon started`, `pty started`).

Startup is coordinated, not raced. The daemon and PTY start together, but the
PTY declares a dependency on the daemon, so its first attach waits until the
daemon has registered with the Server instead of failing with
`daemon not connected` and retrying. Residual failures still fall back to
event-bus retry, tunable in the daemon config:

```yaml
startup:
  max_retries: 3        # attempts beyond the first (total = 1 + max_retries)
  retry_delay: 1s       # wait after a retry notification before retrying
```

Shutdown is graceful. `SIGINT` (Ctrl-C), `SIGTERM`, and `SIGHUP` (closing the
whole terminal window) all cancel the run context so in-flight file streams and
the PTY finish before the daemon and session exit — and the exit is logged. A
second signal, or a 10s grace timeout, forces an immediate exit.

## Optional File-Operation Audit

The daemon can persist a record of each remote file operation to a local
database for after-the-fact auditing. It is off by default; enable it in the
daemon config:

```yaml
audit:
  enabled: true             # Whether auditing is on; defaults to false (off).
  driver: "sqlite"          # sqlite | mysql | postgres (aliases sqlite3/postgresql/pgsql also accepted)
  dsn: "file:audit.db"      # Driver-specific DSN; required when enabled is true.
  table: "file_audit_log"   # Table name for audit records; letters, digits, underscores only. Defaults to file_audit_log.
  queue_size: 256           # In-memory buffer size before writes block; defaults to 256.
```

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

## Related Guides

- Chinese README: [README_ZH.md](README_ZH.md)
- Minimal dual-machine deployment: [deploy/minimal/README.md](deploy/minimal/README.md)
