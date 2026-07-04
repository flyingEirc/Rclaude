#!/bin/sh
set -eu

usage() {
  cat >&2 <<'EOF'
usage: .test/e2e-test.sh [--check]

Full dual-machine integration test: build the latest rclaude-daemon /
rclaude-claude (local arch) and rclaude-server (cross-built linux/amd64),
ship the server binary to the remote test host, restart it, then start a
freshly built local daemon and run the FUSE file-plane smoke check against
it. Config values are left at their current remote defaults; only the
latest deploy/minimal/server.example.yaml is refreshed on the remote for
reference, and only if server.yaml is missing there is it bootstrapped
from that example. This script does not touch the real Claude/Codex PTY
attach step; use tools/rclaude-codex.sh or deploy/minimal/start-pty.sh for
that manual acceptance.

Database testing is unified on sqlite: the local daemon's audit db
(<run-dir>/audit.db) is always written next to that run's log directory
(<run-dir>/logs), never mysql/postgres.

Options:
  --check   Build, render config, and preflight only; no remote changes.

Environment:
  RCLAUDE_REMOTE_HOST   Default: 69.63.208.133
  RCLAUDE_REMOTE_USER   Default: root
  RCLAUDE_REMOTE_KEY    Default: .list/server_private_key
  RCLAUDE_REMOTE_DIR    Default: /tmp/rclaude-codex-test
  RCLAUDE_REMOTE_PORT   Default: 7969
  RCLAUDE_TOKEN_FILE    Default: .list/token.yaml
  RCLAUDE_WORKSPACE     Default: .workspace under repo root
  RCLAUDE_SMOKE_FILE    Default: hello_world.py (must exist under the workspace)
EOF
  exit 2
}

msg() { printf '==> %s\n' "$1"; }
fail() { printf 'error: %s\n' "$1" >&2; exit 1; }

abs_path() {
  case "$1" in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "$(pwd)" "$1" ;;
  esac
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
cd "$ROOT_DIR"

REMOTE_HOST=${RCLAUDE_REMOTE_HOST:-69.63.208.133}
REMOTE_USER=${RCLAUDE_REMOTE_USER:-root}
REMOTE_KEY=${RCLAUDE_REMOTE_KEY:-.list/server_private_key}
REMOTE_DIR=${RCLAUDE_REMOTE_DIR:-/tmp/rclaude-codex-test}
REMOTE_PORT=${RCLAUDE_REMOTE_PORT:-7969}
TOKEN_FILE=${RCLAUDE_TOKEN_FILE:-.list/token.yaml}
WORKSPACE=${RCLAUDE_WORKSPACE:-.workspace}
SMOKE_FILE=${RCLAUDE_SMOKE_FILE:-hello_world.py}

[ -f "$REMOTE_KEY" ] || fail "missing SSH key: $REMOTE_KEY"
[ -f "$TOKEN_FILE" ] || fail "missing token file: $TOKEN_FILE"
[ -d "$WORKSPACE" ] || fail "missing workspace dir: $WORKSPACE"

KEY_ABS=$(abs_path "$REMOTE_KEY")
WORKSPACE_ABS=$(CDPATH= cd -- "$WORKSPACE" && pwd)

USERID=$(sed -n 's/^userid:[[:space:]]*//p' "$TOKEN_FILE" | head -n1)
TOKEN=$(sed -n 's/^token:[[:space:]]*//p' "$TOKEN_FILE" | head -n1)
[ -n "$USERID" ] && [ -n "$TOKEN" ] || fail "userid/token missing in $TOKEN_FILE"

[ -f "$WORKSPACE_ABS/$SMOKE_FILE" ] || fail "smoke file not found under workspace: $WORKSPACE_ABS/$SMOKE_FILE"

BIN_DIR="$ROOT_DIR/.test/bin"
RUN_ID=$(date -u +%Y%m%d%H%M%S)
RUN_DIR="$ROOT_DIR/.test/run/$RUN_ID"
LOG_DIR="$RUN_DIR/logs"
AUDIT_DB="$RUN_DIR/audit.db"
DAEMON_CONFIG="$RUN_DIR/daemon.yaml"

mkdir -p "$BIN_DIR" "$LOG_DIR"

ssh_c() {
  ssh -i "$KEY_ABS" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 \
    -o StrictHostKeyChecking=accept-new "$REMOTE_USER@$REMOTE_HOST" "$@"
}

scp_c() {
  scp -i "$KEY_ABS" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 \
    -o StrictHostKeyChecking=accept-new "$@"
}

DAEMON_PID=""
cleanup() {
  if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill -INT "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# --- 1. build ---------------------------------------------------------
msg "building local rclaude-daemon / rclaude-claude"
go build -o "$BIN_DIR/rclaude-daemon" ./app/client
go build -o "$BIN_DIR/rclaude-claude" ./app/clientpty

msg "cross-building rclaude-server for remote (linux/amd64)"
GOOS=linux GOARCH=amd64 go build -o "$BIN_DIR/rclaude-server" ./app/server

# --- 2. render local daemon config (sqlite audit db lives next to logs) --
msg "writing local daemon test config: $DAEMON_CONFIG"
cat > "$DAEMON_CONFIG" <<EOF
server:
  address: "$REMOTE_HOST:$REMOTE_PORT"
  token: "$TOKEN"
workspace:
  path: "$WORKSPACE_ABS"
  exclude:
    - ".git"
    - "node_modules"
    - "vendor"
log:
  level: "info"
  format: "json"
  dir: "$LOG_DIR"
pty:
  frame_max_bytes: 65536
audit:
  enabled: true
  driver: "sqlite"
  dsn: "$AUDIT_DB"
EOF
chmod 600 "$DAEMON_CONFIG"

msg "daemon/client preflight"
RCLAUDE_DAEMON_BIN="$BIN_DIR/rclaude-daemon" RCLAUDE_PTY_BIN="$BIN_DIR/rclaude-claude" \
  sh "$ROOT_DIR/deploy/minimal/preflight-daemon.sh" "$DAEMON_CONFIG"

if [ "$MODE" = "check" ]; then
  msg "check complete (no remote changes made)"
  exit 0
fi

# --- 3. ship latest server binary + example config to remote ----------
msg "uploading server binary + latest server.example.yaml to $REMOTE_HOST:$REMOTE_DIR"
ssh_c "mkdir -p '$REMOTE_DIR'"
scp_c "$BIN_DIR/rclaude-server" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/rclaude-server.new"
scp_c "$ROOT_DIR/deploy/minimal/server.example.yaml" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/server.example.yaml"

# server.yaml is hand-tuned on the remote (pty.binary, cache, prefetch, ...);
# never overwrite it here. Only bootstrap it from the example when absent.
ssh_c "cd '$REMOTE_DIR' && [ -f server.yaml ] || cp server.example.yaml server.yaml"
ssh_c "chmod +x '$REMOTE_DIR/rclaude-server.new' && mv -f '$REMOTE_DIR/rclaude-server.new' '$REMOTE_DIR/rclaude-server'"

MOUNTPOINT=$(ssh_c "sed -n 's/^[[:space:]]*mountpoint:[[:space:]]*//p' '$REMOTE_DIR/server.yaml' | head -n1 | tr -d '\"'")
[ -n "$MOUNTPOINT" ] || MOUNTPOINT=/workspace

# --- 4. restart remote server ------------------------------------------
msg "restarting remote rclaude-server"
ssh_c 'sh -s' <<EOF
set -eu
cd '$REMOTE_DIR'
if [ -f server.pid ] && kill -0 "\$(cat server.pid)" 2>/dev/null; then
  OLD_PID=\$(cat server.pid)
  kill -INT "\$OLD_PID"
  i=0
  while [ \$i -lt 20 ]; do
    kill -0 "\$OLD_PID" 2>/dev/null || break
    sleep 0.3
    i=\$((i + 1))
  done
  if kill -0 "\$OLD_PID" 2>/dev/null; then
    kill -9 "\$OLD_PID" 2>/dev/null || true
  fi
fi
# Defensive: a non-graceful stop can leave the FUSE mount stale.
fusermount3 -u '$MOUNTPOINT' 2>/dev/null || umount -l '$MOUNTPOINT' 2>/dev/null || true
nohup ./rclaude-server --config server.yaml > server.log 2>&1 < /dev/null &
echo \$! > server.pid
sleep 1
if kill -0 "\$(cat server.pid)" 2>/dev/null; then
  echo "remote server restarted, pid \$(cat server.pid)"
else
  echo "remote server failed to start" >&2
  tail -n 40 server.log >&2
  exit 1
fi
EOF

msg "checking server TCP $REMOTE_HOST:$REMOTE_PORT"
if command -v nc >/dev/null 2>&1; then
  nc -z -w 5 "$REMOTE_HOST" "$REMOTE_PORT" >/dev/null 2>&1 || fail "server not reachable at $REMOTE_HOST:$REMOTE_PORT"
fi

# --- 5. start local daemon ----------------------------------------------
msg "starting local rclaude-daemon"
"$BIN_DIR/rclaude-daemon" --config "$DAEMON_CONFIG" &
DAEMON_PID=$!
sleep 2
kill -0 "$DAEMON_PID" 2>/dev/null || fail "local daemon exited early; check $LOG_DIR"

# --- 6. FUSE file-plane smoke, run on the remote against the live mount -
msg "running FUSE file-plane smoke on remote for user $USERID"
ssh_c "RCLAUDE_MOUNTPOINT='$MOUNTPOINT' sh -s -- '$USERID' '$SMOKE_FILE'" \
  < "$ROOT_DIR/deploy/minimal/smoke-remote.sh"

msg "e2e test passed"
msg "local logs:   $LOG_DIR"
msg "local audit db (sqlite): $AUDIT_DB"
msg "remote log:   $REMOTE_DIR/server.log"
msg "PTY manual acceptance (real claude/codex attach) is not covered here; use tools/rclaude-codex.sh or deploy/minimal/start-pty.sh."
