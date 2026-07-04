#!/bin/sh
set -eu

# Unified daemon-machine entry. `rclaude` starts the local daemon and the
# remote claude PTY attach together, coordinating startup over the in-process
# event bus (pkg/startup). It runs the daemon and the remote pty attach together
# in one process. Everything except the per-component status lines goes to the log
# file (rclaude.log). Run it in a real interactive terminal: the PTY attach
# needs a TTY, so avoid pipes, `nohup`, and non-interactive CI shells here.

usage() {
  echo "usage: $0 <daemon-config-path>" >&2
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

if [ "$#" -ne 1 ]; then
  usage
fi

config_path=$1

if [ ! -f "$config_path" ]; then
  echo "daemon config does not exist: $config_path" >&2
  exit 1
fi

if ! rclaude_bin=$(find_rclaude_bin); then
  echo "rclaude binary not found; build ./app/rclaude or set RCLAUDE_BIN" >&2
  exit 1
fi

server_address=$(sed -n 's/^[[:space:]]*address:[[:space:]]*//p' "$config_path" | head -n 1)
workspace_path=$(sed -n 's/^[[:space:]]*path:[[:space:]]*//p' "$config_path" | head -n 1)

echo "==> starting rclaude (daemon + pty)"
echo "binary: $rclaude_bin"
echo "config: $config_path"

if [ -n "$server_address" ]; then
  echo "server.address: $(strip_quotes "$server_address")"
fi

if [ -n "$workspace_path" ]; then
  echo "workspace.path: $(strip_quotes "$workspace_path")"
fi

if [ ! -t 0 ] || [ ! -t 1 ]; then
  echo "warning: no interactive TTY detected; the pty attach needs a terminal" >&2
fi

exec "$rclaude_bin" --config "$config_path"
