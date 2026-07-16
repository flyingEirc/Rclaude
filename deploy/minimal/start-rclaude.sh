#!/bin/sh
set -eu

# Unified daemon-machine entry. `rclaude` starts the local daemon and the
# remote agent PTY attach together, coordinating startup over the in-process
# event bus (pkg/startup). The agent program the remote session runs (claude,
# codex, ...) is declared here on the command line and forwarded to the server
# with the attach request. Everything except the per-component status lines
# goes to the log file (rclaude.log). Run it in a real interactive terminal:
# the PTY attach needs a TTY, so avoid pipes, `nohup`, and non-interactive CI
# shells here.

usage() {
  echo "usage: $0 <agent> <daemon-config-path>" >&2
  echo "  <agent>: program the remote session runs — a bare name resolved" >&2
  echo "           via the server's PATH (e.g. claude, codex) or an absolute" >&2
  echo "           path on the server (e.g. /root/.local/bin/codex)" >&2
  exit 2
}

find_rclaude_bin() {
  if [ -n "${RCLAUDE_BIN:-}" ]; then
    printf '%s\n' "$RCLAUDE_BIN"
    return 0
  fi

  if [ -x "./bin/rclaude" ]; then
    printf '%s\n' "./bin/rclaude"
    return 0
  fi

  if command -v rclaude >/dev/null 2>&1; then
    command -v rclaude
    return 0
  fi

  return 1
}

strip_quotes() {
  printf '%s' "$1" | tr -d '"'
}

if [ "$#" -ne 2 ]; then
  usage
fi

agent=$1
config_path=$2

if [ -z "$agent" ]; then
  usage
fi

if [ ! -f "$config_path" ]; then
  echo "daemon config does not exist: $config_path" >&2
  exit 1
fi

if ! rclaude_bin=$(find_rclaude_bin); then
  echo "rclaude binary not found; build ./app/rclaude or set RCLAUDE_BIN" >&2
  exit 1
fi

server_address=$(sed -n 's/^[[:space:]]*address:[[:space:]]*//p' "$config_path" | head -n 1)

echo "==> starting rclaude (daemon + pty)"
echo "binary: $rclaude_bin"
echo "agent: $agent"
echo "config: $config_path"

if [ -n "$server_address" ]; then
  echo "server.address: $(strip_quotes "$server_address")"
fi

if [ ! -t 0 ] || [ ! -t 1 ]; then
  echo "warning: no interactive TTY detected; the pty attach needs a terminal" >&2
fi

exec "$rclaude_bin" --agent "$agent" --config "$config_path"
