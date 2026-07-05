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
| Remote SSH | `root@69.63.208.133` |
| SSH key files | `.list/server_private_key`, `.list/server_public_key` |
| Remote OS | Ubuntu 22.04, x86_64 |
| Server port | `7969` |
| Auth material | read `userid` and `token` from `.list/token.yaml` |
| Local workspace | repo-local `.workspace`, resolved to an absolute path |
| Current security posture | cleartext gRPC is allowed for now; TLS is a later phase |
| Claude Code acceptance | remote server has `claude`; verify install/login before real manual PTY |

Use SSH like:

```sh
ssh -i .list/server_private_key -o IdentitiesOnly=yes root@69.63.208.133
```

## Safety Rules

- Do not print, paste, or commit the token or private key contents.
- Do not add `.list/` contents to generated configs that will be committed.
- Prefer temporary config files for live tests.
- Treat cleartext gRPC on `7969` as acceptable only for this controlled test; do not call it production-ready.

## Preflight

1. Confirm the remote host is reachable with the key in `.list/server_private_key`.
2. On remote, verify Linux FUSE support: `/dev/fuse` exists and the server user can open it.
3. Build the local unified entry: `go build -o ./bin/rclaude ./app/rclaude`. The server binary is cross-compiled and deployed for you by `deploy/minimal/start-server.sh`.
4. Resolve credentials from `.list/token.yaml`; use the `userid` as `/workspace/<userid>`.
5. Resolve local workspace as `$(pwd)/.workspace`.

## Config Shape

The concrete test configs are the gitignored `deploy/minimal/server.test.yaml` and `deploy/minimal/daemon.test.yaml` (pattern `*.test.yaml`, never committed). They are generated once from `.list/token.yaml`; regenerate if the token rotates.

- `server.test.yaml`: `listen: ":7969"`, `auth.tokens: { <token>: <userid> }`, FUSE `mountpoint: /workspace`, `pty.workspace_root: /workspace`.
- `daemon.test.yaml`: `server.address: "69.63.208.133:7969"`, same token, absolute local `workspace.path`.

Leave server-side `pty.binary` unset (default): the attach lands in the user's login shell under `/workspace/<userid>`, where you `ls`/`cd` and launch `claude`/`codex` manually. Set `pty.binary` only to pin a fixed program (e.g. an absolute `codex` path) for scripted, non-interactive checks.

## Verification Order

The test is considered passing when **both the remote server and the local entry start successfully** (the server listens on `:7969`; `rclaude` prints `daemon started` + `pty started` and the PTY attach lands under `/workspace/<userid>`).

1. **Preflight** — `RCLAUDE_PREFLIGHT_CHECK_SERVER=1 sh deploy/minimal/preflight-daemon.sh deploy/minimal/daemon.test.yaml`; optionally run `deploy/minimal/preflight-server.sh` on the remote. Fix every reported error first.
2. **Start server** — `sh deploy/minimal/start-server.sh deploy/minimal/server.test.yaml`. It cross-compiles `app/server` for linux/amd64, ships it + the config to `<remote>:/etc/rclaude`, restarts `rclaude-server` detached, and must print `remote: rclaude-server running (pid …)`.
3. **Start local** — `sh deploy/minimal/start-rclaude.sh deploy/minimal/daemon.test.yaml` from an interactive TTY. Success = `daemon started` + `pty started` and a live remote PTY session.

If step 2 fails, read the tailed `<remote>:/etc/rclaude/server.out`: usually a busy `/dev/fuse`, a stale `/workspace` mount, or a missing mountpoint. If step 3 fails, the server is unreachable, the token maps to the wrong `userid`, or a pinned `pty.binary` is missing on the server (the default login shell only fails if the box has no shell at all); diagnostics beyond startup live in `rclaude.log`.

## Useful Project References

- `deploy/minimal/README.md` for the full test-closure walkthrough.
- `deploy/minimal/server.example.yaml` and `deploy/minimal/daemon.example.yaml` for the documented config structure.
- `deploy/minimal/start-server.sh` (remote build + deploy + start) and `deploy/minimal/start-rclaude.sh` (local daemon + pty).
- `docs/reference/claude-code-pty-adapter.md` for the PTY/FUSE separation model.
