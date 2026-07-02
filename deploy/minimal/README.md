# Minimal Dual-Machine Deploy Closure

This directory is the smallest supported handoff for:

- running `rclaude-server` on one Linux machine,
- running `rclaude-daemon` on another machine,
- verifying the mounted `/workspace/<user_id>` FUSE view from the server side,
- and manually attaching the PTY entry with `rclaude-claude` from the daemon/client side.

It is intentionally not a production bundle. There is still no Docker, `systemd`, TLS, installer, or log rotation in this phase.

## Adapter Model

Rclaude keeps the Claude TUI and remote file access as two separate paths:

- `RemotePTY.Attach` carries only terminal bytes, resize events, detach, and exit/error frames between `rclaude-claude` and the Server-side PTY process.
- `RemoteFS.Connect` plus FUSE exposes `/workspace/<user_id>` on the Server, and redirects ordinary file operations back to the user's daemon workspace.

The Server starts `pty.binary` inside `/workspace/<user_id>`. For the real Claude Code entry, this means the Server machine must have `claude` installed, available on `PATH` or by absolute path, and already authenticated for the Server OS user. A Claude login on the Daemon machine is not reused by the Server-side `claude` process.

## Prerequisites

- The Server machine is Linux and can mount FUSE.
- The Daemon machine can reach the Server machine over `<server_ip>:<port>`.
- You have built, copied, or installed `rclaude-server`, `rclaude-daemon`, and `rclaude-claude`.
- The Daemon machine points `workspace.path` at the real local workspace using an absolute path.
- The Server machine has the executable referenced by `pty.binary` on `PATH` or at an absolute path.

## Build Binaries

Build on each target machine, or build once and copy the resulting binaries with this `deploy/minimal/` directory:

```sh
go build -o ./bin/rclaude-server ./app/server
go build -o ./bin/rclaude-daemon ./app/client
go build -o ./bin/rclaude-claude ./app/clientpty
```

The startup scripts first look for `./bin/rclaude-server`, `./bin/rclaude-daemon`, or `./bin/rclaude-claude`, then fall back to `PATH`. You can also point them at custom binaries with `RCLAUDE_SERVER_BIN`, `RCLAUDE_DAEMON_BIN`, or `RCLAUDE_PTY_BIN`.

## Server Machine

1. Copy the example config and edit the token, listen address, mountpoint, `pty.binary`, and optional `pty.args`.

```sh
mkdir -p /etc/rclaude
cp ./deploy/minimal/server.example.yaml /etc/rclaude/server.yaml
```

Key PTY-related fields in `server.yaml`:

```yaml
pty:
  binary: "claude"
  args: []
  workspace_root: "/workspace"
  env_passthrough: ["TERM", "LANG", "LC_ALL", "LC_CTYPE", "PATH", "HOME", "SHELL", "CLAUDE_CONFIG_DIR"]
  frame_max_bytes: 65536
  graceful_shutdown_timeout: "5s"
  ratelimit:
    attach_qps: 1
    attach_burst: 3
    stdin_bps: 1048576
    stdin_burst: 262144
```

`pty.args` is appended to the configured server-side binary as argv, for example `args: ["--model", "sonnet"]`. It is intentionally not accepted from the daemon-side PTY client, so the Server machine remains the authority for what process and flags are launched.

Keep `HOME`, `SHELL`, and `CLAUDE_CONFIG_DIR` in `pty.env_passthrough` when using the real `claude` binary. Rclaude starts `claude` with an explicit environment, so omitting `HOME` can prevent Claude Code from finding the Server OS user's login and configuration files, omitting `SHELL` can change how Claude Code detects the shell it should use for command execution, and omitting `CLAUDE_CONFIG_DIR` can break Server users who keep Claude credentials in a non-default config directory.

`pty.env_passthrough` entries can be exact variable names or shell-style patterns such as `ANTHROPIC_*` and `CLAUDE_CODE_*`. The default list is conservative and does not forward auth tokens or provider routing variables. If the Server-side `claude` uses environment-based auth, proxy, or gateway configuration, add only the exact variables or narrow patterns you intend to expose to the spawned PTY process.

2. Run the Server preflight on the Server machine.

```sh
sh ./deploy/minimal/preflight-server.sh /etc/rclaude/server.yaml
```

The preflight does not start the server or mount FUSE. It checks the Server-side assumptions that commonly make the real `claude` attach fail later:

- Linux Server OS,
- `/dev/fuse` availability,
- absolute `fuse.mountpoint` and `pty.workspace_root`,
- mountpoint parent readiness,
- `pty.binary` available on `PATH` or by absolute path,
- optional util-linux `script(1)` support when `RCLAUDE_PREFLIGHT_CHECK_SCRIPT=1`.

The preflight cannot prove that the Server OS user is logged in to Claude Code. That still needs the real manual `claude` acceptance step below.

3. Start the server in the foreground.

```sh
sh ./deploy/minimal/start-server.sh /etc/rclaude/server.yaml
```

Expected result:

- The process stays in the foreground.
- Startup logs include `server started`.
- The mountpoint exists and becomes the root for `/workspace/<user_id>`.

## Daemon Machine

1. Copy the example config and edit `server.address`, `server.token`, and `workspace.path`.

```sh
cp ./deploy/minimal/daemon.example.yaml ./daemon.yaml
```

The same daemon YAML is reused by `rclaude-daemon` and `rclaude-claude`; there is no separate PTY client config file in this phase.
If you lower `pty.frame_max_bytes` on the Server machine, mirror the same value in the daemon YAML so `rclaude-claude` chunks stdin frames to the same cap.

2. Run the Daemon/client preflight on the Daemon machine.

```sh
sh ./deploy/minimal/preflight-daemon.sh ./daemon.yaml
```

The preflight does not start `rclaude-daemon` or `rclaude-claude`, and it does not modify the workspace. It checks:

- `server.address` and `server.token`,
- `workspace.path` is absolute and accessible,
- local `rclaude-daemon` and `rclaude-claude` binaries,
- optional TCP reachability to `server.address` when `RCLAUDE_PREFLIGHT_CHECK_SERVER=1`,
- optional interactive terminal readiness for `rclaude-claude` when `RCLAUDE_PREFLIGHT_REQUIRE_TTY=1`.

3. Start the daemon in the foreground.

```sh
sh ./deploy/minimal/start-daemon.sh ./daemon.yaml
```

Expected result:

- The process stays in the foreground.
- The daemon connects back to the server.
- The configured token resolves to the matching `/workspace/<user_id>` view on the server.

## FUSE Smoke Verification

Run the file smoke script on the Server machine after the daemon is connected:

```sh
sh ./deploy/minimal/smoke-remote.sh demo README.md
```

The script performs:

- `ls -la /workspace/<user_id>`
- `cat /workspace/<user_id>/<expected_file>`
- create a temporary `.rclaude-smoke-*` file
- rename the file
- delete the file

If your mountpoint is not `/workspace`, override it for the smoke run:

```sh
RCLAUDE_MOUNTPOINT=/custom/mount sh ./deploy/minimal/smoke-remote.sh demo README.md
```

## PTY Manual Acceptance

Run the PTY client on the Daemon machine with the same daemon YAML:

```sh
sh ./deploy/minimal/start-pty.sh ./daemon.yaml
```

Manual acceptance checklist for the real `claude` entry:

- The attach succeeds and you land in a remote PTY session.
- The remote working directory resolves under `/workspace/<user_id>`.
- Resizing the local terminal updates the remote PTY layout.
- `Ctrl+C` / normal exit returns control to the local terminal and surfaces the exit status.

## PTY Scripted Smoke

`tools/pty-smoke.sh` has two modes:

- default `manual`: directly runs `rclaude-claude --config <daemon.yaml>` for a human-driven check.
- `RCLAUDE_PTY_MODE=scripted`: feeds a deterministic shell script through a PTY and checks the transcript.

The scripted mode is only for transport smoke. It requires util-linux `script(1)` with `-c` support on the machine running `rclaude-claude`; the BSD/macOS `script(1)` is not sufficient for this automated mode. Before using it, temporarily point `pty.binary` at a shell such as `/bin/sh` on the Server machine.

```sh
RCLAUDE_PTY_MODE=scripted sh ./tools/pty-smoke.sh ./daemon.yaml
```

To prove that the PTY process can also read the FUSE-mounted daemon files, ask the scripted smoke to `cat` a file relative to the remote PTY cwd:

```sh
RCLAUDE_PTY_MODE=scripted \
RCLAUDE_PTY_EXPECT_CWD=/workspace/demo \
RCLAUDE_PTY_EXPECT_FILE=README.md \
RCLAUDE_PTY_EXPECT_FILE_CONTAINS='Rclaude' \
sh ./tools/pty-smoke.sh ./daemon.yaml
```

The scripted mode validates:

- attach succeeds,
- the remote process sees a PTY (`stty size` runs),
- stdin reaches the remote PTY,
- stdout returns to the local side,
- optional remote-file reads can see the daemon-backed FUSE workspace,
- and the remote exit code is surfaced back to the caller.

Use this order when diagnosing a failed real `claude` attach:

1. Run `smoke-remote.sh` first. If it fails, fix daemon connectivity, token mapping, FUSE mount, or file permissions before looking at PTY.
2. Run `RCLAUDE_PTY_MODE=scripted tools/pty-smoke.sh` with `pty.binary: "/bin/sh"`. If it fails, fix the `RemotePTY` stream, terminal handling, or process spawn path.
3. Restore `pty.binary: "claude"` and any required `pty.args`, then run the manual PTY check. If only this step fails, check Server-side Claude Code install, login state, `PATH`, configured args, and whether commands such as `pwd` and project discovery work inside `/workspace/<user_id>`.

While running these checks, inspect the Server logs for PTY lifecycle events. `pty attach requested` means the authenticated PTY stream reached the Server. `pty attach rejected` includes a `reason` such as `daemon_not_connected`, `session_busy`, or `attach_rate_limited`. `pty spawn starting` records the resolved Server-side binary, `args_count`, and cwd. `pty spawn failed` carries the Server-side spawn error. `pty attached` confirms the session id and cwd sent to the client, and `pty exited` records the remote exit code and signal.

## First-Line Troubleshooting

- `mountpoint does not exist`: verify the server config uses an absolute `fuse.mountpoint` and that the machine supports FUSE.
- `server preflight failed`: fix the reported Server-machine prerequisite before starting `rclaude-server`.
- `daemon/client preflight failed`: fix the reported Daemon-machine prerequisite before starting `rclaude-daemon` or `rclaude-claude`.
- `user root does not exist`: confirm the daemon is connected and the token maps to the expected `user_id`.
- `expected file does not exist`: verify the path is relative to the daemon workspace root.
- `rclaude-claude binary not found`: build `./app/clientpty` or export `RCLAUDE_PTY_BIN`.
- PTY attach failures usually mean the daemon is not connected, the token maps to the wrong `user_id`, or the server-side `pty.binary` is missing.
- `failed to start remote claude process on Server`: inspect the detail after the colon, then check Server `pty.binary`, `pty.args`, `PATH`, `/workspace/<user_id>`, and Server-side Claude Code login.
- The scripted PTY smoke requires util-linux `script(1)` with `-c` support and a shell-like `pty.binary`; for the real `claude` binary, use the default manual mode.
- The repository-root `daemon-localtest.yaml` is only for developer local debugging; keep deploy handoff based on `deploy/minimal/*.yaml`.
- If you are running the scripts from a synced or mounted workspace that does not preserve executable bits, invoke them with `sh` as shown above.
