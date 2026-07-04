#!/bin/sh
set -eu

usage() {
  cat >&2 <<'EOF'
usage: deploy/minimal/preflight-daemon.sh <daemon-config-path>

Checks the Daemon/client-machine prerequisites before starting the unified
rclaude entry (daemon + pty). It does not start anything and does not modify the
workspace or config.

Optional environment:
  RCLAUDE_BIN                        Override the local rclaude binary path.
  RCLAUDE_PREFLIGHT_CHECK_SERVER=1   Require a TCP check to server.address with nc.
  RCLAUDE_PREFLIGHT_REQUIRE_TTY=1    Require stdin and stdout to be TTYs for PTY use.
EOF
  exit 2
}

strip_quotes() {
  printf '%s' "$1" | tr -d '"'
}

trim_spaces() {
  printf '%s' "$1" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

first_yaml_value() {
  key=$1
  file=$2
  sed -n "s/^[[:space:]]*$key:[[:space:]]*//p" "$file" | head -n 1 | while IFS= read -r value; do
    strip_quotes "$(trim_spaces "$value")"
  done
}

pass() {
  printf 'ok: %s\n' "$1"
}

fail() {
  printf 'error: %s\n' "$1" >&2
  failures=$((failures + 1))
}

warn() {
  printf 'warn: %s\n' "$1" >&2
}

is_abs_path() {
  case "$1" in
    /*) return 0 ;;
    [A-Za-z]:/*) return 0 ;;
    [A-Za-z]:\\*) return 0 ;;
    *) return 1 ;;
  esac
}

find_binary() {
  env_name=$1
  local_path=$2
  binary_name=$3

  eval "override=\${$env_name:-}"
  if [ -n "$override" ]; then
    printf '%s\n' "$override"
    return 0
  fi

  if [ -x "$local_path" ]; then
    printf '%s\n' "$local_path"
    return 0
  fi

  if command -v "$binary_name" >/dev/null 2>&1; then
    command -v "$binary_name"
    return 0
  fi

  return 1
}

check_executable() {
  label=$1
  path=$2
  if [ -x "$path" ]; then
    pass "$label is executable: $path"
  else
    fail "$label is not executable: $path"
  fi
}

check_workspace() {
  workspace_path=$1
  if [ -z "$workspace_path" ]; then
    fail "workspace.path is empty or missing"
    return
  fi
  if ! is_abs_path "$workspace_path"; then
    fail "workspace.path must be absolute: $workspace_path"
    return
  fi
  pass "workspace.path is absolute: $workspace_path"

  if [ ! -d "$workspace_path" ]; then
    fail "workspace.path is not a directory: $workspace_path"
    return
  fi
  pass "workspace.path exists"

  if [ -r "$workspace_path" ] && [ -x "$workspace_path" ]; then
    pass "workspace.path is readable and searchable"
  else
    fail "workspace.path must be readable and searchable by this user"
  fi

  if [ -w "$workspace_path" ]; then
    pass "workspace.path is writable by this user"
  else
    warn "workspace.path is not writable by this user; read-only smoke may work but write smoke will fail"
  fi
}

split_host_port() {
  address=$1
  host=${address%:*}
  port=${address##*:}

  if [ -z "$host" ] || [ -z "$port" ] || [ "$host" = "$address" ]; then
    return 1
  fi
  printf '%s\n%s\n' "$host" "$port"
}

check_server_address() {
  server_address=$1
  if [ -z "$server_address" ]; then
    fail "server.address is empty or missing"
    return
  fi
  case "$server_address" in
    *SERVER_IP* | *server_ip* | *"<server"* | *"<SERVER"*)
      fail "server.address still contains a placeholder: $server_address"
      return
      ;;
  esac
  pass "server.address is set: $server_address"

  if [ "${RCLAUDE_PREFLIGHT_CHECK_SERVER:-0}" != "1" ]; then
    return
  fi

  if ! host_port=$(split_host_port "$server_address"); then
    fail "server.address must be host:port for TCP preflight: $server_address"
    return
  fi
  host=$(printf '%s\n' "$host_port" | sed -n '1p')
  port=$(printf '%s\n' "$host_port" | sed -n '2p')

  if ! command -v nc >/dev/null 2>&1; then
    fail "nc is required for RCLAUDE_PREFLIGHT_CHECK_SERVER=1"
    return
  fi
  if nc -z "$host" "$port" >/dev/null 2>&1; then
    pass "server.address accepts TCP connections: $server_address"
  else
    fail "server.address is not reachable over TCP: $server_address"
  fi
}

check_tty() {
  if [ "${RCLAUDE_PREFLIGHT_REQUIRE_TTY:-0}" != "1" ]; then
    return
  fi

  if [ -t 0 ] && [ -t 1 ]; then
    pass "stdin and stdout are TTYs"
  else
    fail "stdin and stdout must both be TTYs for the rclaude pty attach"
  fi
}

if [ "$#" -ne 1 ]; then
  usage
fi

config_path=$1
failures=0

if [ ! -f "$config_path" ]; then
  echo "error: daemon config does not exist: $config_path" >&2
  exit 1
fi

server_address=$(first_yaml_value "address" "$config_path")
server_token=$(first_yaml_value "token" "$config_path")
workspace_path=$(first_yaml_value "path" "$config_path")

echo "==> Rclaude daemon/client preflight"
echo "config: $config_path"

check_server_address "$server_address"

if [ -n "$server_token" ]; then
  pass "server.token is set"
else
  fail "server.token is empty or missing"
fi

check_workspace "$workspace_path"

if rclaude_bin=$(find_binary "RCLAUDE_BIN" "./bin/rclaude" "rclaude"); then
  check_executable "rclaude" "$rclaude_bin"
else
  fail "rclaude binary not found; build ./app/rclaude or set RCLAUDE_BIN"
fi

check_tty

if [ "$failures" -ne 0 ]; then
  echo "daemon/client preflight failed with $failures error(s)" >&2
  exit 1
fi

echo "daemon/client preflight passed"
