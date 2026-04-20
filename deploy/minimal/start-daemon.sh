#!/bin/sh
set -eu

usage() {
  echo "usage: $0 <config-path>" >&2
  exit 2
}

find_daemon_bin() {
  if [ -n "${RCLAUDE_DAEMON_BIN:-}" ]; then
    printf '%s\n' "$RCLAUDE_DAEMON_BIN"
    return 0
  fi

  if [ -x "./bin/rclaude-daemon" ]; then
    printf '%s\n' "./bin/rclaude-daemon"
    return 0
  fi

  if command -v rclaude-daemon >/dev/null 2>&1; then
    command -v rclaude-daemon
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

if ! daemon_bin=$(find_daemon_bin); then
  echo "rclaude-daemon binary not found; build ./app/client or set RCLAUDE_DAEMON_BIN" >&2
  exit 1
fi

server_address=$(sed -n 's/^[[:space:]]*address:[[:space:]]*//p' "$config_path" | head -n 1)
workspace_path=$(sed -n 's/^[[:space:]]*path:[[:space:]]*//p' "$config_path" | head -n 1)

echo "==> starting rclaude-daemon"
echo "binary: $daemon_bin"
echo "config: $config_path"

if [ -n "$server_address" ]; then
  echo "server.address: $(strip_quotes "$server_address")"
fi

if [ -n "$workspace_path" ]; then
  echo "workspace.path: $(strip_quotes "$workspace_path")"
fi

exec "$daemon_bin" --config "$config_path"
