#!/bin/sh
set -eu

usage() {
  echo "usage: $0 <config-path>" >&2
  exit 2
}

find_server_bin() {
  if [ -n "${RCLAUDE_SERVER_BIN:-}" ]; then
    printf '%s\n' "$RCLAUDE_SERVER_BIN"
    return 0
  fi

  if [ -x "./bin/rclaude-server" ]; then
    printf '%s\n' "./bin/rclaude-server"
    return 0
  fi

  if command -v rclaude-server >/dev/null 2>&1; then
    command -v rclaude-server
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
  echo "server config does not exist: $config_path" >&2
  exit 1
fi

if ! server_bin=$(find_server_bin); then
  echo "rclaude-server binary not found; build ./app/server or set RCLAUDE_SERVER_BIN" >&2
  exit 1
fi

listen=$(sed -n 's/^listen:[[:space:]]*//p' "$config_path" | head -n 1)
mountpoint=$(sed -n 's/^[[:space:]]*mountpoint:[[:space:]]*//p' "$config_path" | head -n 1)

echo "==> starting rclaude-server"
echo "binary: $server_bin"
echo "config: $config_path"

if [ -n "$listen" ]; then
  echo "listen: $(strip_quotes "$listen")"
fi

if [ -n "$mountpoint" ]; then
  echo "mountpoint: $(strip_quotes "$mountpoint")"
fi

exec "$server_bin" --config "$config_path"
