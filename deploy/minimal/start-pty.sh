#!/bin/sh
set -eu

usage() {
  echo "usage: $0 <daemon-config-path>" >&2
  exit 2
}

find_pty_bin() {
  if [ -n "${RCLAUDE_PTY_BIN:-}" ]; then
    printf '%s\n' "$RCLAUDE_PTY_BIN"
    return 0
  fi

  if [ -x "./bin/rclaude-claude" ]; then
    printf '%s\n' "./bin/rclaude-claude"
    return 0
  fi

  if command -v rclaude-claude >/dev/null 2>&1; then
    command -v rclaude-claude
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

if ! pty_bin=$(find_pty_bin); then
  echo "rclaude-claude binary not found; build ./app/clientpty or set RCLAUDE_PTY_BIN" >&2
  exit 1
fi

server_address=$(sed -n 's/^[[:space:]]*address:[[:space:]]*//p' "$config_path" | head -n 1)
workspace_path=$(sed -n 's/^[[:space:]]*path:[[:space:]]*//p' "$config_path" | head -n 1)

echo "==> starting rclaude-claude"
echo "binary: $pty_bin"
echo "config: $config_path"

if [ -n "$server_address" ]; then
  echo "server.address: $(strip_quotes "$server_address")"
fi

if [ -n "$workspace_path" ]; then
  echo "workspace.path: $(strip_quotes "$workspace_path")"
fi

exec "$pty_bin" --config "$config_path"
