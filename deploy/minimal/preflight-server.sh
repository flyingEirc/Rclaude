#!/bin/sh
set -eu

usage() {
  cat >&2 <<'EOF'
usage: deploy/minimal/preflight-server.sh <server-config-path>

Checks the Server-machine prerequisites for RemoteFS/FUSE and RemotePTY before
starting rclaude-server. It does not start the server, mount FUSE, or modify the
config.

Optional environment:
  RCLAUDE_PREFLIGHT_CHECK_SCRIPT=1  Also require util-linux script(1) with -c support.
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
    *) return 1 ;;
  esac
}

check_abs_path() {
  name=$1
  value=$2
  if [ -z "$value" ]; then
    fail "$name is empty or missing"
    return
  fi
  if is_abs_path "$value"; then
    pass "$name is absolute: $value"
  else
    fail "$name must be absolute: $value"
  fi
}

check_mountpoint_parent() {
  mountpoint=$1
  parent=$(dirname "$mountpoint")

  if [ -d "$mountpoint" ]; then
    pass "mountpoint exists: $mountpoint"
    return
  fi
  if [ -d "$parent" ] && [ -w "$parent" ]; then
    pass "mountpoint parent is writable: $parent"
    return
  fi

  fail "mountpoint does not exist and parent is not writable: $mountpoint"
}

# With pty.binary unset the server spawns the user's interactive login shell
# ($SHELL -> bash -> zsh -> sh), so preflight must verify a shell is usable
# instead of failing on the empty value.
check_login_shell() {
  for candidate in "${SHELL:-}" bash zsh sh /bin/bash /bin/sh; do
    [ -n "$candidate" ] || continue
    if is_abs_path "$candidate"; then
      if [ -x "$candidate" ]; then
        pass "pty.binary unset; login shell available: $candidate"
        return
      fi
    elif resolved=$(command -v "$candidate" 2>/dev/null); then
      pass "pty.binary unset; login shell available: $candidate -> $resolved"
      return
    fi
  done
  fail "pty.binary unset and no login shell found (\$SHELL/bash/zsh/sh)"
}

check_pty_binary() {
  binary=$1
  if [ -z "$binary" ]; then
    check_login_shell
    return
  fi

  if is_abs_path "$binary"; then
    if [ -x "$binary" ]; then
      pass "pty.binary is executable: $binary"
    else
      fail "pty.binary is not executable: $binary"
    fi
    return
  fi

  if resolved=$(command -v "$binary" 2>/dev/null); then
    pass "pty.binary resolves on PATH: $binary -> $resolved"
  else
    fail "pty.binary does not resolve on PATH: $binary"
  fi
}

check_script_command() {
  if [ "${RCLAUDE_PREFLIGHT_CHECK_SCRIPT:-0}" != "1" ]; then
    return
  fi

  if ! command -v script >/dev/null 2>&1; then
    fail "script(1) is not on PATH"
    return
  fi
  if script -qefc "true" /dev/null >/dev/null 2>&1; then
    pass "script(1) supports util-linux -c mode"
  else
    fail "script(1) exists but does not support util-linux -c mode"
  fi
}

if [ "$#" -ne 1 ]; then
  usage
fi

config_path=$1
failures=0

if [ ! -f "$config_path" ]; then
  echo "error: server config does not exist: $config_path" >&2
  exit 1
fi

listen=$(first_yaml_value "listen" "$config_path")
mountpoint=$(first_yaml_value "mountpoint" "$config_path")
workspace_root=$(first_yaml_value "workspace_root" "$config_path")
pty_binary=$(first_yaml_value "binary" "$config_path")

echo "==> Rclaude server preflight"
echo "config: $config_path"

if [ -n "$listen" ]; then
  pass "listen is set: $listen"
else
  fail "listen is empty or missing"
fi

if [ "$(uname -s)" = "Linux" ]; then
  pass "server OS is Linux"
else
  fail "server OS must be Linux for FUSE: $(uname -s)"
fi

if [ -e /dev/fuse ]; then
  pass "/dev/fuse exists"
  if [ -r /dev/fuse ] && [ -w /dev/fuse ]; then
    pass "/dev/fuse is readable and writable by this user"
  else
    warn "/dev/fuse is not readable and writable by this user; rclaude-server may need different permissions"
  fi
else
  fail "/dev/fuse does not exist"
fi

check_abs_path "fuse.mountpoint" "$mountpoint"
if [ -n "$mountpoint" ] && is_abs_path "$mountpoint"; then
  check_mountpoint_parent "$mountpoint"
fi

check_abs_path "pty.workspace_root" "$workspace_root"
if [ -n "$mountpoint" ] && [ -n "$workspace_root" ] && [ "$mountpoint" != "$workspace_root" ]; then
  warn "pty.workspace_root differs from fuse.mountpoint; this is advanced and must be intentional"
fi

check_pty_binary "$pty_binary"
check_script_command

if [ "$failures" -ne 0 ]; then
  echo "server preflight failed with $failures error(s)" >&2
  exit 1
fi

echo "server preflight passed"
