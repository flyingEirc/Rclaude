#!/bin/sh
set -eu

usage() {
  cat >&2 <<'EOF'
usage: tools/rclaude-codex.sh [--check]

Build local Rclaude client binaries, generate a temporary daemon config from
.list/token.yaml, start rclaude-daemon, then attach to the remote PTY. The
remote server must be running with pty.binary set to codex.

Options:
  --check   Run build, config, server reachability, and remote codex checks only.

Environment:
  RCLAUDE_REMOTE_HOST              Default: 69.63.208.133
  RCLAUDE_REMOTE_USER              Default: root
  RCLAUDE_REMOTE_KEY               Default: .list/server_private_key
  RCLAUDE_REMOTE_PORT              Default: 7969
  RCLAUDE_WORKSPACE                Default: .workspace under repo root
  RCLAUDE_CLIENT_BIN_DIR           Default: ${TMPDIR:-/tmp}/rclaude-codex-client-bin
  RCLAUDE_SKIP_REMOTE_CODEX_CHECK  Set to 1 to skip the SSH codex check.
EOF
  exit 2
}

msg() {
  printf '==> %s\n' "$1"
}

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

need_file() {
  if [ ! -f "$1" ]; then
    fail "missing file: $1"
  fi
}

need_dir() {
  if [ ! -d "$1" ]; then
    fail "missing directory: $1"
  fi
}

abs_path() {
  case "$1" in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "$(pwd)" "$1" ;;
  esac
}

ssh_target() {
  printf '%s@%s\n' "$REMOTE_USER" "$REMOTE_HOST"
}

ssh_base() {
  ssh -i "$REMOTE_KEY_ABS" \
    -o IdentitiesOnly=yes \
    -o BatchMode=yes \
    -o ConnectTimeout=10 \
    -o StrictHostKeyChecking=accept-new \
    "$(ssh_target)" "$@"
}

check_remote_codex() {
  if [ "${RCLAUDE_SKIP_REMOTE_CODEX_CHECK:-0}" = "1" ]; then
    msg "remote codex check skipped"
    return 0
  fi

  msg "checking remote codex"
  if ssh_base 'bash -ic '"'"'command -v codex >/dev/null 2>&1'"'"' 2>/dev/null'; then
    ssh_base 'bash -ic '"'"'printf "remote codex: "; command -v codex; codex --version 2>&1 | sed -n "1p"'"'"' 2>/dev/null'
    return 0
  fi

  fail "remote codex is not on interactive PATH for $(ssh_target); install/login codex on the server or set RCLAUDE_SKIP_REMOTE_CODEX_CHECK=1 to attach anyway"
}

check_server_tcp() {
  msg "checking server TCP ${REMOTE_HOST}:${REMOTE_PORT}"
  if command -v nc >/dev/null 2>&1; then
    nc -z -w 5 "$REMOTE_HOST" "$REMOTE_PORT" >/dev/null 2>&1 ||
      fail "server is not reachable at ${REMOTE_HOST}:${REMOTE_PORT}"
    msg "server TCP reachable"
    return 0
  fi

  msg "nc not found; skipping TCP check"
}

build_clients() {
  msg "building local Rclaude clients"
  mkdir -p "$BIN_DIR"
  (cd "$ROOT_DIR" && go build -o "$BIN_DIR/rclaude-daemon" ./app/client)
  (cd "$ROOT_DIR" && go build -o "$BIN_DIR/rclaude-claude" ./app/clientpty)
}

write_daemon_config() {
  msg "writing temporary daemon config"
  CONFIG_PATH=$(mktemp "${TMPDIR:-/tmp}/rclaude-codex-daemon.XXXXXX")
  export CONFIG_PATH TOKEN_FILE WORKSPACE_ABS REMOTE_HOST REMOTE_PORT
  python3 - <<'PY'
from pathlib import Path
import json
import os

token_file = Path(os.environ["TOKEN_FILE"])
values = {}
for raw in token_file.read_text().splitlines():
    if ":" not in raw:
        continue
    key, value = raw.split(":", 1)
    values[key.strip()] = value.strip()

token = values.get("token", "")
if not token:
    raise SystemExit("token is missing in .list/token.yaml")

workspace = os.environ["WORKSPACE_ABS"]
address = f'{os.environ["REMOTE_HOST"]}:{os.environ["REMOTE_PORT"]}'
config = f"""server:
  address: {json.dumps(address)}
  token: {json.dumps(token)}
workspace:
  path: {json.dumps(workspace)}
  exclude:
    - ".git"
    - "node_modules"
    - "vendor"
log:
  level: "info"
  format: "text"
pty:
  frame_max_bytes: 65536
"""
Path(os.environ["CONFIG_PATH"]).write_text(config)
PY
  chmod 600 "$CONFIG_PATH"
}

cleanup() {
  if [ -n "${DAEMON_PID:-}" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill -INT "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  if [ -n "${CONFIG_PATH:-}" ]; then
    rm -f "$CONFIG_PATH"
  fi
}

run_preflight() {
  RCLAUDE_DAEMON_BIN="$BIN_DIR/rclaude-daemon" \
    RCLAUDE_PTY_BIN="$BIN_DIR/rclaude-claude" \
    sh "$ROOT_DIR/deploy/minimal/preflight-daemon.sh" "$CONFIG_PATH"
}

start_daemon() {
  DAEMON_LOG="${TMPDIR:-/tmp}/rclaude-codex-daemon.log"
  msg "starting local daemon; log: $DAEMON_LOG"
  "$BIN_DIR/rclaude-daemon" --config "$CONFIG_PATH" >"$DAEMON_LOG" 2>&1 &
  DAEMON_PID=$!
  sleep 1
  if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    sed -n '1,80p' "$DAEMON_LOG" >&2 || true
    fail "local daemon exited early"
  fi
}

attach_pty() {
  if [ ! -t 0 ] || [ ! -t 1 ]; then
    fail "stdin and stdout must be TTYs; run this from an interactive terminal"
  fi
  msg "attaching to remote codex"
  "$BIN_DIR/rclaude-claude" --config "$CONFIG_PATH"
}

MODE=run
case "${1:-}" in
  "") ;;
  --check) MODE=check ;;
  -h|--help) usage ;;
  *) usage ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

REMOTE_HOST=${RCLAUDE_REMOTE_HOST:-69.63.208.133}
REMOTE_USER=${RCLAUDE_REMOTE_USER:-root}
REMOTE_PORT=${RCLAUDE_REMOTE_PORT:-7969}
REMOTE_KEY=${RCLAUDE_REMOTE_KEY:-.list/server_private_key}
BIN_DIR=${RCLAUDE_CLIENT_BIN_DIR:-${TMPDIR:-/tmp}/rclaude-codex-client-bin}
TOKEN_FILE="$ROOT_DIR/.list/token.yaml"

cd "$ROOT_DIR"
REMOTE_KEY_ABS=$(abs_path "$REMOTE_KEY")
WORKSPACE_ABS=$(abs_path "${RCLAUDE_WORKSPACE:-.workspace}")

need_file "$REMOTE_KEY_ABS"
need_file "$TOKEN_FILE"
need_dir "$WORKSPACE_ABS"

build_clients
write_daemon_config
trap cleanup EXIT INT TERM
run_preflight
check_server_tcp
check_remote_codex

if [ "$MODE" = "check" ]; then
  msg "check complete"
  exit 0
fi

start_daemon
attach_pty
