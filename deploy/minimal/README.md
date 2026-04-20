# Minimal Dual-Machine Deploy Closure

This directory is the smallest supported handoff for:

- running `rclaude-server` on one Linux machine,
- running `rclaude-daemon` on another machine,
- verifying the mounted `/workspace/<user_id>` FUSE view from the server side,
- and manually attaching the PTY entry with `rclaude-claude` from the daemon/client side.

It is intentionally not a production bundle. There is still no Docker, `systemd`, TLS, installer, or log rotation in this phase.

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

1. Copy the example config and edit the token, listen address, mountpoint, and `pty.binary`.

```sh
mkdir -p /etc/rclaude
cp ./deploy/minimal/server.example.yaml /etc/rclaude/server.yaml
```

Key PTY-related fields in `server.yaml`:

```yaml
pty:
  binary: "claude"
  workspace_root: "/workspace"
  frame_max_bytes: 65536
  graceful_shutdown_timeout: "5s"
  ratelimit:
    attach_qps: 1
    attach_burst: 3
    stdin_bps: 1048576
    stdin_burst: 262144
```

2. Start the server in the foreground.

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

2. Start the daemon in the foreground.

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

The scripted mode is only for transport smoke. Before using it, temporarily point `pty.binary` at a shell such as `/bin/sh` on the Server machine.

```sh
RCLAUDE_PTY_MODE=scripted sh ./tools/pty-smoke.sh ./daemon.yaml
```

The scripted mode validates:

- attach succeeds,
- the remote process sees a PTY (`stty size` runs),
- stdin reaches the remote PTY,
- stdout returns to the local side,
- and the remote exit code is surfaced back to the caller.

## First-Line Troubleshooting

- `mountpoint does not exist`: verify the server config uses an absolute `fuse.mountpoint` and that the machine supports FUSE.
- `user root does not exist`: confirm the daemon is connected and the token maps to the expected `user_id`.
- `expected file does not exist`: verify the path is relative to the daemon workspace root.
- `rclaude-claude binary not found`: build `./app/clientpty` or export `RCLAUDE_PTY_BIN`.
- PTY attach failures usually mean the daemon is not connected, the token maps to the wrong `user_id`, or the server-side `pty.binary` is missing.
- The scripted PTY smoke requires the `script(1)` utility and a shell-like `pty.binary`; for the real `claude` binary, use the default manual mode.
- The repository-root `daemon-localtest.yaml` is only for developer local debugging; keep deploy handoff based on `deploy/minimal/*.yaml`.
- If you are running the scripts from a synced or mounted workspace that does not preserve executable bits, invoke them with `sh` as shown above.
