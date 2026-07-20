# Minimal Test Closure

Language: English | [中文](README_ZH.md)

The smallest supported flow for a real remote/local Rclaude linkage test:

- `rclaude-server` runs on a remote Linux box (FUSE-capable),
- the unified `rclaude` entry (daemon + remote agent PTY attach) runs on your
  local machine and connects back to that server.

It is intentionally not a production bundle: no Docker, `systemd`, TLS, or
installer. Fixed connection facts come from the `rclaude-remote-local-test`
skill: remote access is Tailscale SSH (`ssh root@dmit`, no key material);
the token (and the legacy fallback SSH key) live in `.list/` (gitignored).

## Topology

- **Server machine** — remote Linux, mounts `/workspace/<user_id>/<project>` over FUSE and
  starts the agent program the attach declares (e.g. the real `claude`) inside
  it. Must be Linux with `/dev/fuse`; it cannot run on macOS.
- **Local machine** — runs `rclaude` **from your project root directory**; the
  daemon exposes that directory to the server (its name becomes `<project>`)
  and the terminal attaches to the remote PTY. The agent the session runs is
  declared on the command line: `rclaude -g <agent> -c <config>`.
  The client side needs no FUSE, so macOS is fine here.

`RemotePTY.Attach` carries only terminal bytes/resize/exit between `rclaude` and
the server PTY process, plus the declared agent name. `RemoteFS.Connect` + FUSE
exposes `/workspace/<user_id>/<project>` and redirects file ops back to your
daemon workspace. The server launches the declared agent inside
`/workspace/<user_id>/<project>`; the session ends when it exits and there is
no shell fallback. When you declare `claude`, the **server** machine must have
it installed, on `PATH` (or pass an absolute server path to `-g`), and already
logged in for the server OS user — a local Claude login is not reused
server-side.

## Files

| File | Role |
| --- | --- |
| `start-server.sh` | Cross-compiles `app/server` for linux/amd64, ships it + the server config to `<remote>:/etc/rclaude`, and (re)starts `rclaude-server` there. |
| `start-rclaude.sh` | Runs the unified `rclaude` (daemon + pty) locally in the foreground: `sh start-rclaude.sh <agent> <config>`. Needs an interactive TTY. |
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
   `daemon.test.yaml` uses `server.address: "69.63.208.133:7969"` and the same
   token. There is no `workspace.path`: the workspace root is the directory you
   start `rclaude` in, so run it from the project root you want to expose.

`start-server.sh` cross-compiles and deploys the server for you, so you do not
build `rclaude-server` by hand.

## Test Process

### 1. Start the server

```sh
sh ./deploy/minimal/start-server.sh ./deploy/minimal/server.test.yaml
```

Expected: the script builds, ships to `<remote>:/etc/rclaude`, restarts
`rclaude-server` detached, and prints `remote: rclaude-server running (pid …)`.
Tail the remote log with the command it prints on success. **Success criterion:
the server is up and listening.**

### 2. Start the local entry

From a real interactive terminal, declaring the agent to run:

```sh
sh ./deploy/minimal/start-rclaude.sh claude ./deploy/minimal/daemon.test.yaml
```

(or directly: `./bin/rclaude -g claude -c ./deploy/minimal/daemon.test.yaml`)

Expected: `daemon started` then `pty started` print, and you land in the remote
agent PTY session under `/workspace/<user_id>/<project>`. Everything else goes to
`rclaude.log` (default `~/.rclaude/logs`). **Success criterion: both components
start and the PTY attach succeeds.** `Ctrl+C` / normal exit ends the session and
surfaces the remote exit status.

## Troubleshooting

- `server config does not exist`: run the one-time setup to create
  `deploy/minimal/server.test.yaml`.
- SSH fails: confirm the tailnet is up (`ssh root@dmit` from a shell). For the
  raw-IP fallback, set `RCLAUDE_SSH_HOST`/`RCLAUDE_SSH_KEY` and confirm
  `.list/server_private_key` exists (perms `600`).
- `remote: rclaude-server failed to start`: read the tailed remote log; usually
  a busy `/dev/fuse`, a stale `/workspace` mount, or a missing mountpoint.
- `daemon start failed` / `pty start failed` locally: the server is not
  reachable, the token maps to the wrong `user_id`, or the `-g` agent does not
  resolve on the server (PATH or absolute path).
- PTY attaches but the agent misbehaves: check the **server** install of that
  agent, its login state, and `PATH`. `HOME`/`SHELL`/`CLAUDE_CONFIG_DIR` are
  forwarded automatically — they are part of the fixed built-in env passthrough
  whitelist (no longer a config field). For a transport-only check, temporarily
  attach with `-g /bin/sh`.
- Running from a synced/mounted workspace that drops the executable bit: invoke
  the scripts with `sh` as shown above.
