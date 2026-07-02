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
3. Build or copy `rclaude-server`, `rclaude-daemon`, and `rclaude-claude`.
4. Resolve credentials from `.list/token.yaml`; use the `userid` as `/workspace/<userid>`.
5. Resolve local workspace as `$(pwd)/.workspace`.

## Config Shape

Server config must use port `7969`, the token/userid from `.list/token.yaml`, and a FUSE mountpoint such as `/workspace`. Keep `pty.workspace_root` aligned with the mountpoint.

Daemon config must point at `69.63.208.133:7969`, use the same token, and set `workspace.path` to the absolute repo-local `.workspace` path.

For staged PTY verification, temporarily set server-side `pty.binary` to `/bin/sh`. For real Claude Code acceptance, restore it to `claude` or its absolute path.

## Verification Order

1. File plane: start server, start daemon, then run `deploy/minimal/smoke-remote.sh <userid> <expected_file>` on the server side.
2. PTY transport plane: with server `pty.binary: "/bin/sh"`, run `RCLAUDE_PTY_MODE=scripted tools/pty-smoke.sh <daemon.yaml>` from the daemon/client side.
3. PTY + FUSE read proof: add `RCLAUDE_PTY_EXPECT_CWD=/workspace/<userid>`, `RCLAUDE_PTY_EXPECT_FILE=<file>`, and `RCLAUDE_PTY_EXPECT_FILE_CONTAINS=<marker>`.
4. Real Claude Code: restore `pty.binary: "claude"` and run `deploy/minimal/start-pty.sh <daemon.yaml>` for manual acceptance.

If step 1 fails, diagnose daemon connectivity, token mapping, FUSE mount, or file permissions. If step 2 fails, diagnose RemotePTY, process spawn, terminal bridging, or frame limits. If only step 4 fails, diagnose remote-side Claude install/login/PATH/cwd behavior.

## Useful Project References

- `deploy/minimal/README.md` for the dual-machine handoff and smoke commands.
- `deploy/minimal/server.example.yaml` and `deploy/minimal/daemon.example.yaml` for config structure.
- `tools/pty-smoke.sh` for scripted and manual PTY verification.
- `docs/reference/claude-code-pty-adapter.md` for the PTY/FUSE separation model.
