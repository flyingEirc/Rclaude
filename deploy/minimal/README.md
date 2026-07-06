# Minimal Test Closure

Language: English | [中文](README_ZH.md)

The smallest supported flow for a real remote/local Rclaude linkage test:

- `rclaude-server` runs on a remote Linux box (FUSE-capable),
- the unified `rclaude` entry (daemon + remote claude PTY attach) runs on your
  local machine and connects back to that server.

It is intentionally not a production bundle: no Docker, `systemd`, TLS, or
installer. Fixed connection facts (remote IP, SSH key, token) come from the
`rclaude-remote-local-test` skill and live in `.list/` (gitignored).

## Topology

- **Server machine** — remote Linux, mounts `/workspace/<user_id>` over FUSE and
  starts the PTY process inside it: the user's login shell by default, or
  `pty.binary` (e.g. the real `claude`) when pinned. Must be Linux with
  `/dev/fuse`; it cannot run on macOS.
- **Local machine** — runs `rclaude`, which starts the daemon (exposes your
  local workspace to the server) and attaches the terminal to the remote PTY.
  The client side needs no FUSE, so macOS is fine here.

`RemotePTY.Attach` carries only terminal bytes/resize/exit between `rclaude` and
the server PTY process. `RemoteFS.Connect` + FUSE exposes `/workspace/<user_id>`
and redirects file ops back to your daemon workspace. The server launches the
PTY process inside `/workspace/<user_id>` — the login shell by default, or the
pinned `pty.binary`. When you pin `claude`, the **server** machine must have it
installed, on `PATH` (or absolute), and already logged in for the server OS
user — a local Claude login is not reused server-side.

## Files

| File | Role |
| --- | --- |
| `start-server.sh` | Cross-compiles `app/server` for linux/amd64, ships it + the server config to `<remote>:/etc/rclaude`, and (re)starts `rclaude-server` there. |
| `start-rclaude.sh` | Runs the unified `rclaude` (daemon + pty) locally in the foreground. Needs an interactive TTY. |
| `preflight-server.sh` | Read-only check of server prerequisites (Linux, `/dev/fuse`, absolute mountpoint, `pty.binary`). Run it on the server. |
| `preflight-daemon.sh` | Read-only check of local prerequisites (`server.address`/`token`, absolute `workspace.path`, `rclaude` binary, optional TCP/TTY checks). |
| `server.example.yaml` / `daemon.example.yaml` | Committed templates documenting every field. |
| `server.test.yaml` / `daemon.test.yaml` | **Gitignored** (`*.test.yaml`) test configs carrying the real remote IP + token. Never committed. |

## One-Time Setup

1. Build the local unified entry:

```sh
go build -o ./bin/rclaude ./app/rclaude
```

2. Generate the two gitignored test configs from `.list/token.yaml` (already
   done once; regenerate if the token rotates). `server.test.yaml` uses
   `listen: ":7969"` and `auth.tokens: { <token>: <user_id> }`;
   `daemon.test.yaml` uses `server.address: "69.63.208.133:7969"`, the same
   token, and an absolute local `workspace.path`.

`start-server.sh` cross-compiles and deploys the server for you, so you do not
build `rclaude-server` by hand.

## Test Process

### 1. Preflight

Local (daemon side):

```sh
RCLAUDE_PREFLIGHT_CHECK_SERVER=1 sh ./deploy/minimal/preflight-daemon.sh ./deploy/minimal/daemon.test.yaml
```

Server side (optional, run on the remote after copying the script + config):

```sh
sh ./deploy/minimal/preflight-server.sh /etc/rclaude/server.test.yaml
```

Fix every reported error before starting anything.

### 2. Start the server

```sh
sh ./deploy/minimal/start-server.sh ./deploy/minimal/server.test.yaml
```

Expected: the script builds, ships to `<remote>:/etc/rclaude`, restarts
`rclaude-server` detached, and prints `remote: rclaude-server running (pid …)`.
Tail the remote log with the command it prints on success. **Success criterion:
the server is up and listening.**

### 3. Start the local entry

From a real interactive terminal:

```sh
sh ./deploy/minimal/start-rclaude.sh ./deploy/minimal/daemon.test.yaml
```

Expected: `daemon started` then `pty started` print, and you land in the remote
claude PTY session under `/workspace/<user_id>`. Everything else goes to
`rclaude.log` (default `~/.rclaude/logs`). **Success criterion: both components
start and the PTY attach succeeds.** `Ctrl+C` / normal exit ends the session and
surfaces the remote exit status.

## Troubleshooting

- `server config does not exist`: run the one-time setup to create
  `deploy/minimal/server.test.yaml`.
- `ssh key not found`: confirm `.list/server_private_key` exists (perms `600`).
- `remote: rclaude-server failed to start`: read the tailed remote log; usually
  a busy `/dev/fuse`, a stale `/workspace` mount, or a missing mountpoint.
- `daemon start failed` / `pty start failed` locally: the server is not
  reachable, the token maps to the wrong `user_id`, or the server-side
  `pty.binary` is missing. Re-run preflight with `RCLAUDE_PREFLIGHT_CHECK_SERVER=1`.
- PTY attaches but `claude` misbehaves: check the **server** Claude install,
  login state, `PATH`, and that `HOME`/`SHELL`/`CLAUDE_CONFIG_DIR` are in
  `pty.env_passthrough`. For a transport-only check, temporarily set the server
  `pty.binary` to `/bin/sh`.
- Running from a synced/mounted workspace that drops the executable bit: invoke
  the scripts with `sh` as shown above.
