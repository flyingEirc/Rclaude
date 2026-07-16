---
name: rclaude-remote-local-test
description: Use when preparing, running, or diagnosing Rclaude remote/local dual-machine integration tests in this repo, including SSH access, Linux FUSE server setup, daemon workspace config, token lookup, PTY smoke checks, and Claude Code manual acceptance.
---

# Rclaude Remote Local Test

## Overview

This is the project-local runbook for real remote/local Rclaude linkage tests. Keep secrets in `.list/`; reference their paths instead of copying token or key material into skill text, committed config, logs, or final replies.

## Known Inputs

| Item | Value |
| --- | --- |
| Remote SSH | `ssh root@dmit` (Tailscale SSH, no key material needed) |
| Legacy SSH fallback | `root@69.63.208.133` with `.list/server_private_key` / `.list/server_public_key` (only if the tailnet is down) |
| Remote OS | Ubuntu 22.04, x86_64 |
| Server port | `7969` |
| Auth material | read `userid` and `token` from `.list/token.yaml` |
| Local workspace | the directory `rclaude` is started from (cwd = project root); for repo tests, `cd` into repo-local `.workspace` first — its name becomes the server-side `<project>` |
| Current security posture | cleartext gRPC is allowed for now; TLS is a later phase |
| Claude Code acceptance | remote server has `claude`; verify install/login before real manual PTY |

Use SSH like:

```sh
ssh root@dmit
```

`dmit` is the Tailscale MagicDNS name of the test box; authentication happens
via the tailnet, so no `-i` key flag is needed. The raw-IP + private-key form
is a fallback only:

```sh
ssh -i .list/server_private_key -o IdentitiesOnly=yes root@69.63.208.133
```

## Safety Rules

- Do not print, paste, or commit the token or private key contents.
- Do not add `.list/` contents to generated configs that will be committed.
- Prefer temporary config files for live tests.
- Treat cleartext gRPC on `7969` as acceptable only for this controlled test; do not call it production-ready.

## Preflight

1. Confirm the remote host is reachable: `ssh root@dmit` (Tailscale SSH).
2. On remote, verify Linux FUSE support: `/dev/fuse` exists and the server user can open it.
3. Build the local unified entry: `go build -o ./bin/rclaude ./app/rclaude`. The server binary is cross-compiled and deployed for you by `deploy/minimal/start-server.sh`.
4. Resolve credentials from `.list/token.yaml`; the server view is `/workspace/<userid>/<project>` (`<project>` = basename of the daemon start directory).
5. Start the daemon from the workspace directory itself (`cd .workspace` first); there is no `workspace.path` config — cwd is the workspace root.

## Config Shape

The concrete test configs are the gitignored `deploy/minimal/server.test.yaml` and `deploy/minimal/daemon.test.yaml` (pattern `*.test.yaml`, never committed). They are generated once from `.list/token.yaml`; regenerate if the token rotates.

- `server.test.yaml`: `listen: ":7969"`, `auth.tokens: { <token>: <userid> }`, FUSE `mountpoint: /workspace`, `pty.workspace_root: /workspace`. There is no `pty.binary` — the agent is declared per session on the rclaude command line.
- `daemon.test.yaml`: `server.address: "69.63.208.133:7969"`, same token. No `workspace.path` — run `rclaude` from the directory you want to expose.

The agent is declared at attach time with `-g/--agent` (e.g. `-g claude` for a PATH lookup on the server, or `-g /root/.local/bin/codex` for an absolute server path). The attach lands directly in that agent under `/workspace/<userid>/<project>` — there is no login-shell fallback, no client-controlled argv, and the session (PTY + local daemon) ends when the agent exits.

## Verification Order

The test is considered passing when **both the remote server and the local entry start successfully** (the server listens on `:7969`; `rclaude` prints `daemon started` + `pty started` and the PTY attach lands under `/workspace/<userid>/<project>`).

1. **Start server** — `sh deploy/minimal/start-server.sh deploy/minimal/server.test.yaml`. It cross-compiles `app/server` for linux/amd64, ships it + the config to `<remote>:/etc/rclaude`, restarts `rclaude-server` detached, and must print `remote: rclaude-server running (pid …)`.
2. **Start local** — `sh deploy/minimal/start-rclaude.sh <agent> deploy/minimal/daemon.test.yaml` from an interactive TTY (equivalently `./bin/rclaude -g <agent> -c deploy/minimal/daemon.test.yaml`). Success = `daemon started` + `pty started` and a live remote PTY session.

If step 1 fails, read the tailed `<remote>:/etc/rclaude/server.out`: usually a busy `/dev/fuse`, a stale `/workspace` mount, or a missing mountpoint. If step 2 fails, the server is unreachable, the token maps to the wrong `userid`, or the `-g` agent does not resolve on the server (PATH or absolute path); diagnostics beyond startup live in `rclaude.log`.

## Useful Project References

- `deploy/minimal/README.md` for the full test-closure walkthrough.
- `deploy/minimal/server.example.yaml` and `deploy/minimal/daemon.example.yaml` for the documented config structure.
- `deploy/minimal/start-server.sh` (remote build + deploy + start) and `deploy/minimal/start-rclaude.sh <agent> <config>` (local daemon + pty).
- `docs/reference/claude-code-pty-adapter.md` for the PTY/FUSE separation model.
