# Rclaude

Language: English | [中文](README_ZH.md)

---

## 1. What is this project?

`Rclaude` is a remote file access system paired with a remote agent terminal.

It does **not** copy your workspace up to a cloud machine. Instead it exposes a
local workspace to a remote execution environment through ordinary file paths,
and lets an interactive coding agent (such as Claude Code or Codex) run on that
remote machine against those same paths.

- From the execution side, files look like normal local filesystem paths under
  `/workspace/{user_id}/{project}/`.
- From the system side, the real source of data stays the daemon-side workspace
  on your own machine.

A single unified `rclaude` entry runs on your machine and carries two
independent gRPC streams to the server:

- **File path** — the daemon (`RemoteFS.Connect`) exposes your local workspace;
  the server publishes it through a FUSE mount.
- **Terminal path** — the PTY attach (`RemotePTY.Attach`) forwards only terminal
  bytes, resize events, and exit status, and drops you straight into the agent
  program running on the server inside your workspace.

```text
Local workspace
    ^
    | read/write files and watch changes
    v
rclaude  (daemon + PTY attach, one unified entry)
    ^
    | two independent bidirectional gRPC streams
    v
rclaude-server
    ^
    | FUSE mount + agent PTY
    v
/workspace/{user_id}/{project}/...
    ^
    | cat / sed / grep / ls / stat / mv / rm ...  and the agent itself
    v
Execution environment (remote Linux)
```

## 2. Why does it exist? What problem does it solve?

The goal is to run a cloud/remote coding agent **without** shipping your whole
project to the cloud and without a custom file SDK.

- **Files stay local and authoritative.** The daemon-side workspace remains the
  single source of truth; the server holds only cache and metadata, never a full
  copy.
- **Ordinary path semantics.** Because the server surface is a FUSE mount,
  standard shell tools — `cat`, `sed`, `grep`, `ls`, `stat`, `mv`, `rm` — and the
  agent's own file access work unchanged. No file API to integrate against.
- **The daemon dials out.** The connection is initiated from your machine to the
  server, so the server never has to reach back into a user's local network.
- **The agent runs remotely, next to fast compute,** while still seeing your
  local files. The terminal is a clean passthrough into exactly one declared
  agent program — no shell, no way to roam the server.

Typical flow: a remote agent runs `cat /workspace/{user_id}/{project}/main.go`;
the server-side FUSE layer handles the path, forwards the request over the gRPC
stream to your daemon, and the daemon reads the local file and returns it.

Implemented today:

- read ops: `Lookup`, `Getattr`, `Readdir`, `Open`, `Read`
- write ops: create, overwrite, offset write, append, `mkdir`, `rename`,
  `delete`, `truncate`
- per-user isolation under `/workspace/{user_id}/{project}/`; one active daemon
  session per user (reconnecting from another project replaces the previous one)
- file-tree metadata cache, whole-file content cache, small-file prefetch after
  directory reads
- temporary read-only cache fallback after a daemon disconnect
- sensitive-file filtering (`.env`, private keys, certs, custom patterns),
  workspace boundary and path-traversal protection
- static token authentication mapped to `user_id`
- server-side terminal passthrough confined to the agent declared on the
  command line (`-g/--agent`), landing in `/workspace/{user_id}/{project}`
- coordinated startup (PTY waits for the daemon to register) and graceful
  shutdown draining in-flight file streams and the PTY
- file-based structured logging that never touches the terminal
- optional TLS via a front proxy (Caddy) and optional SQLite/MySQL/PostgreSQL
  audit log of remote file operations

## 3. How to get it running quickly

### Requirements

- **Server side:** Linux with usable FUSE support (`/dev/fuse`). Cannot run on
  macOS.
- **Daemon side (your machine):** Linux, macOS, or Windows. No FUSE needed
  locally.
- **Go:** this repo's `go.mod` pins Go `1.25.2`.

### Build

```bash
make tools                                   # dev tools used by the repo

# The server (remote Linux) and the unified local entry are the only entrypoints.
go build -o ./bin/rclaude-server ./app/server
go build -o ./bin/rclaude        ./app/rclaude

# Or a repo-wide compile check:
go build ./...
```

### Server config (`server.yaml`)

```yaml
listen: ":9326"                      # gRPC listen address; ":port" binds all interfaces. Required.
auth:
  tokens:
    "example-token": "example-user"  # token -> user_id; a daemon trades its token for a user_id. At least one entry required.
fuse:
  mountpoint: "/workspace"           # FUSE mount root (absolute); each project appears at {mountpoint}/{user_id}/{project}.
cache:
  max_bytes: 268435456               # whole-file content cache cap in bytes; <=0 disables the cache.
prefetch:
  enabled: true                      # prefetch small files after a directory read (needs cache.max_bytes > 0).
  max_file_bytes: 102400             # max single-file size eligible for prefetch.
  max_files_per_dir: 16              # max files prefetched per directory read.
request_timeout: 10s                 # per-request timeout; <=0 falls back to 10s.
offline_readonly_ttl: 5m             # how long cached content stays read-only after the daemon disconnects.
```

There is no `pty:` or `log:` block — PTY and logging are hardcoded (see §6).

### Daemon config (`daemon.yaml`)

```yaml
server:
  address: "127.0.0.1:9326"          # Server gRPC address. Required.
  token: "example-token"             # must match one of the Server's auth.tokens.
  # Optional TLS when a terminator (e.g. Caddy) sits in front of the server:
  # tls:
  #   enabled: true
  #   server_name: ""                # required if dialing an IP but the cert is for a domain
  #   ca_file: ""                    # PEM for a self-signed/internal CA; leave empty for public certs
workspace:
  exclude:                           # globs excluded from scanning/watching
    - ".git"
    - "node_modules"
    - "vendor"
  sensitive_patterns:                # extra sensitive paths on top of built-in rules
    - "secrets/**"
self_write_ttl: 2s                   # window to ignore the daemon's own write events; <=0 falls back to 2s.
```

There is no `workspace.path`: the workspace root is the directory you start
`rclaude` in, so **run it from your project root**. That directory's name
becomes the project name on the server (a single safe path segment — no `/`,
`\`, or control chars, and not `.`/`..`).

### Start both sides

```bash
# On the remote Linux server:
./bin/rclaude-server --config ./server.yaml

# On your machine, from the project root, declaring which agent runs remotely:
./bin/rclaude -g claude -c ./daemon.yaml
```

If you started `rclaude` in a directory named `myproj`, the server now exposes
`/workspace/example-user/myproj/`, and your terminal is attached to the `claude`
agent running there.

For the smallest real remote/local closure, see
[deploy/minimal/README.md](deploy/minimal/README.md).

## 4. How to use it

### File operations from the server side

Any process on the server (a shell, the agent, automation) uses ordinary paths:

```bash
ls -la /workspace/example-user/myproj
cat   /workspace/example-user/myproj/README.md
grep -R "TODO" /workspace/example-user/myproj
mkdir /workspace/example-user/myproj/tmp
printf 'hello\n' > /workspace/example-user/myproj/tmp/demo.txt
mv    /workspace/example-user/myproj/tmp/demo.txt /workspace/example-user/myproj/tmp/demo2.txt
truncate -s 2 /workspace/example-user/myproj/tmp/demo2.txt
rm    /workspace/example-user/myproj/tmp/demo2.txt
```

### The remote agent (PTY) entry

The agent is declared per session on the command line — never in config, never
controlled by the client argv:

```bash
rclaude -g claude -c ./daemon.yaml                    # bare name, resolved via the server's PATH
rclaude -g codex  -c ./daemon.yaml
rclaude -g /root/.local/bin/codex -c ./daemon.yaml    # absolute path on the server
```

The server launches exactly that program inside `/workspace/{user_id}/{project}`
and the session ends when it exits. There is no shell fallback and no
client-controlled argv, so the session cannot `ls`/`cd` the server, see the
remote workspace path, or leave the agent UI. The process runs on the **server**
machine, so the server OS user must be able to resolve the binary and hold
whatever login state / env that CLI needs — a login on your local machine is not
reused server-side.

Observed status:

- `/bin/sh` scripted PTY plus FUSE file reads: pass.
- Codex CLI TUI attach, `codex exec` reading a daemon-backed FUSE file, and
  remote exit-code propagation: pass.
- Claude Code TUI renders through RemotePTY, but main-prompt acceptance depends
  on the server OS user's Claude Code onboarding/login.

### Optional TLS (front proxy)

The server binary stays plaintext h2c; put Caddy in front to terminate TLS and
turn on the daemon's `server.tls` block. See
[deploy/tls/README.md](deploy/tls/README.md) for `acme` vs `internal` CA modes
and the Cloudflare caveat (bidirectional gRPC streams need a **DNS-only** / grey
cloud record).

### Optional file-operation audit

Off by default; enable in the daemon config to persist each remote file op to a
local DB:

```yaml
audit:
  enabled: true             # default false
  driver: "sqlite"          # sqlite | mysql | postgres (sqlite3/postgresql/pgsql aliases ok)
  dsn: "file:audit.db"      # driver-specific DSN; required when enabled
  table: "file_audit_log"   # letters/digits/underscores only
  queue_size: 256           # in-memory buffer before writes block
```

### Environment overrides

Config loads via `viper` with `RCLAUDE_*` overrides (dots → underscores):

```bash
export RCLAUDE_SERVER_ADDRESS=127.0.0.1:9999   # server.address
export RCLAUDE_FUSE_MOUNTPOINT=/workspace      # fuse.mountpoint
./bin/rclaude -g claude -c ./daemon.yaml
```

## 5. How to contribute / develop

### Repository layout

```text
api/                    gRPC protocol and generated code
app/rclaude/            rclaude unified local entry (daemon + PTY, coordinated startup)
app/server/             rclaude-server entrypoint
pkg/config/             YAML + environment configuration loading
pkg/logx/               file-based structured logging (never writes to the terminal)
pkg/startup/            startup coordinator for the unified entry (dependency gating + retries)
pkg/auth/               token authentication
pkg/safepath/           workspace path validation and boundary protection
pkg/fstree/             file-tree metadata index
pkg/session/            server-side user sessions and request routing
pkg/contentcache/       server-side whole-file content cache
pkg/fusefs/             FUSE filesystem view
pkg/syncer/             daemon-side scan, watch, sync, and request handling
pkg/ptyhost/            server-side PTY child-process spawn (attach-declared agent)
pkg/ptyservice/         server-side RemotePTY gRPC service
pkg/ptyclient/          daemon-side terminal <-> PTY gRPC bridge
pkg/ptyattach/          local terminal attach (raw mode, resize, exit codes)
pkg/audit/              optional DB audit log for remote file operations
pkg/transport/          gRPC connection and stream wrappers (single client dial site)
pkg/ratelimit/          server-side PTY attach/stdin rate limiting
internal/inmemtest/     in-memory end-to-end test harness
internal/testutil/      shared test fixtures and helpers
deploy/minimal/         minimal remote/local test closure (configs + start scripts)
deploy/tls/             optional Caddy TLS termination in front of the server
tools/                  proto codegen tool-version pin (tools.go)
```

### Development commands

```bash
make fmt          # format
make lint         # static checks (also applies to test files)
make test         # unit + integration tests
make all          # fmt + lint + test
make check        # same gate as CI
make test-cover   # tests with coverage
go build ./...    # repo-wide compile check
```

Test baseline: package-level unit tests, cross-platform in-memory integration
tests, Linux real-FUSE smoke tests (`Mount -> kernel/FUSE -> session -> daemon`,
skipped where FUSE is unavailable), and Linux RemotePTY + FUSE smoke checks.

For code or behavior changes, run `make fmt` / `make lint` / `make test` before
finishing.

## 6. Design, limits, and future

### Design points

- The daemon initiates the connection; the server never dials back into a user's
  machine.
- FUSE is the primary integration surface — the compatibility target is ordinary
  path-based file semantics, not one specific agent.
- Server-side cache and prefetch are built into the architecture.
- The two client roles (file sync, terminal) share one config and one
  coordinated lifecycle but stay **independent** gRPC streams.
- **Logging is hardcoded, not configurable:** both sides always write all levels
  (debug and up) as JSON to `~/.rclaude/logs` with default rotation, never to the
  terminal — so the PTY passthrough stays clean. The unified entry writes
  `rclaude.log`; the server writes `rclaude-server.log`. The terminal shows only
  one status line per component.
- **PTY behavior is hardcoded:** working dir root equals `fuse.mountpoint` (cwd
  `{mountpoint}/{user_id}/{project}`), a fixed env passthrough whitelist (`TERM`,
  `LANG`, `LC_ALL`, `LC_CTYPE`, `PATH`, `HOME`, `SHELL`, `CLAUDE_CONFIG_DIR`),
  65536-byte frame max, 5s graceful stop, fixed attach/stdin rate limits.
- **Startup is coordinated, not raced:** the PTY's first attach waits until the
  daemon has registered instead of failing with `daemon not connected`; residual
  failures fall back to event-bus retry (3 retries, 5s apart).
- **Shutdown is graceful:** `SIGINT` / `SIGTERM` / `SIGHUP` cancel the run
  context so in-flight file streams and the PTY finish before exit; a second
  signal or a 10s grace timeout forces immediate exit.

### Current limits

- The server must run on Linux with FUSE support.
- Authentication is static token mapping, not a full identity system.
- Best suited to small teams (~1–20 users).
- Rclaude does not mirror a full workspace to the server by design.
- Disconnect handling is temporary read-only cache fallback only — no offline
  write-back.
- No built-in Docker bundle, systemd unit, installer, or operations dashboard
  yet (TLS is available as an optional front-proxy setup under `deploy/tls/`).
- This repo is a working minimal path, not yet a full production distribution.

### Future direction

- Broaden platform / packaging support (containers, service units, installers).
- Stronger identity beyond static tokens.
- Richer disconnect handling beyond read-only fallback.
- Operational surfaces (metrics, dashboards, audit pipelines).

## Related guides

- Chinese README: [README_ZH.md](README_ZH.md)
- Minimal dual-machine deployment: [deploy/minimal/README.md](deploy/minimal/README.md)
- Optional TLS via Caddy: [deploy/tls/README.md](deploy/tls/README.md)
