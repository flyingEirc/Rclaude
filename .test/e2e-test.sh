#!/bin/sh
set -eu

usage() {
  cat >&2 <<'EOF'
usage: .test/e2e-test.sh [--check]

Automated file-plane integration test: build the latest rclaude-daemon
(local arch) and rclaude-server (cross-built linux/amd64), deploy the
server binary + the real-token server config (deploy/minimal/server.test.yaml)
to the remote test host as server.yaml, restart it, then start a freshly
built local daemon and run an inlined FUSE file-plane smoke against the live
mount. The server config is refreshed on every run for reproducibility. This
script does not do the PTY attach; for real interactive acceptance use
deploy/minimal/start-rclaude.sh.

Database testing is unified on sqlite: the local daemon's audit db
(<run-dir>/audit.db) is always written next to that run's log directory
(<run-dir>/logs), never mysql/postgres.

Options:
  --check   Build, render config, and preflight only; no remote changes.

Environment:
  RCLAUDE_REMOTE_HOST   Default: 69.63.208.133
  RCLAUDE_REMOTE_USER   Default: root
  RCLAUDE_REMOTE_KEY    Default: .list/server_private_key
  RCLAUDE_REMOTE_DIR    Default: /tmp/rclaude-e2e-test
  RCLAUDE_REMOTE_PORT   Default: 7969
  RCLAUDE_TOKEN_FILE    Default: .list/token.yaml
  RCLAUDE_WORKSPACE     Default: .workspace under repo root
  RCLAUDE_SMOKE_FILE    Default: hello_world.py (must exist under the workspace)
  RCLAUDE_SERVER_CONFIG Default: deploy/minimal/server.test.yaml (deployed as remote server.yaml)
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
REMOTE_DIR=${RCLAUDE_REMOTE_DIR:-/tmp/rclaude-e2e-test}
REMOTE_PORT=${RCLAUDE_REMOTE_PORT:-7969}
TOKEN_FILE=${RCLAUDE_TOKEN_FILE:-.list/token.yaml}
WORKSPACE=${RCLAUDE_WORKSPACE:-.workspace}
SMOKE_FILE=${RCLAUDE_SMOKE_FILE:-hello_world.py}
SERVER_CONFIG=${RCLAUDE_SERVER_CONFIG:-deploy/minimal/server.test.yaml}

[ -f "$REMOTE_KEY" ] || fail "missing SSH key: $REMOTE_KEY"
[ -f "$TOKEN_FILE" ] || fail "missing token file: $TOKEN_FILE"
[ -f "$SERVER_CONFIG" ] || fail "missing server config: $SERVER_CONFIG (generate deploy/minimal/server.test.yaml)"
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

# Multiplex all ssh/scp over one connection so the many short-lived calls this
# script makes do not trip the remote sshd connection-rate limit (MaxStartups).
SSH_CTRL="/tmp/rclaude-e2e-ctrl-$$"
SSH_OPTS="-o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o ControlMaster=auto -o ControlPath=$SSH_CTRL -o ControlPersist=60s"

ssh_c() {
  ssh -i "$KEY_ABS" $SSH_OPTS "$REMOTE_USER@$REMOTE_HOST" "$@"
}

scp_c() {
  scp -i "$KEY_ABS" $SSH_OPTS "$@"
}

DAEMON_PID=""
cleanup() {
  if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill -INT "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  ssh -i "$KEY_ABS" -o ControlPath="$SSH_CTRL" -O exit "$REMOTE_USER@$REMOTE_HOST" 2>/dev/null || true
  rm -f "$SSH_CTRL" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# --- 1. build ---------------------------------------------------------
msg "building local rclaude-daemon (file-plane only; no PTY attach here)"
go build -o "$BIN_DIR/rclaude-daemon" ./app/client

msg "cross-building rclaude-server for remote (linux/amd64)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$BIN_DIR/rclaude-server" ./app/server

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

msg "prerequisites validated (ssh key, token, workspace, smoke file, daemon config rendered)"

if [ "$MODE" = "check" ]; then
  msg "check complete (no remote changes made)"
  exit 0
fi

# --- 3. ship server binary + real-token server config to remote -------
msg "uploading server binary + $SERVER_CONFIG (as server.yaml) to $REMOTE_HOST:$REMOTE_DIR"
ssh_c "mkdir -p '$REMOTE_DIR'"
scp_c "$BIN_DIR/rclaude-server" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/rclaude-server.new"
scp_c "$SERVER_CONFIG" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/server.yaml"
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
# Kill any stray instance (e.g. a manual start-server.sh run) so it cannot
# hold '$MOUNTPOINT' or the port; then clear a possibly stale FUSE mount.
pkill -x rclaude-server 2>/dev/null || true
sleep 1
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

# --- 6. FUSE file-plane smoke, inlined and run on the remote live mount -
msg "running FUSE file-plane smoke on remote for user $USERID"
ssh_c "MP='$MOUNTPOINT' UID_='$USERID' FILE='$SMOKE_FILE' sh -s" <<'SMOKE'
set -eu
user_root="${MP%/}/$UID_"
expected="$user_root/$FILE"
stamp=$(date -u +%Y%m%d%H%M%S)-$$
tmp="$user_root/.rclaude-smoke-$stamp.txt"
moved="$user_root/.rclaude-smoke-$stamp.moved.txt"
cleanup() { rm -f "$tmp" "$moved" 2>/dev/null || true; }
trap cleanup EXIT INT HUP TERM
[ -d "$MP" ] || { echo "mountpoint missing: $MP" >&2; exit 1; }
[ -d "$user_root" ] || { echo "user root missing: $user_root" >&2; exit 1; }
[ -f "$expected" ] || { echo "expected file missing: $expected" >&2; exit 1; }
echo "==> ls $user_root"; ls -la "$user_root"
echo "==> cat $expected"; cat "$expected"
echo "==> write $tmp"; printf 'e2e smoke %s\n' "$stamp" >"$tmp"; test -f "$tmp"
echo "==> mv -> $moved"; mv "$tmp" "$moved"
i=0; while [ "$i" -lt 20 ] && [ ! -f "$moved" ]; do sleep 0.2; i=$((i + 1)); done
[ -f "$moved" ] || { echo "renamed path not visible: $moved" >&2; exit 1; }
echo "==> rm $moved"; rm "$moved"
echo "remote file-plane smoke passed for $UID_"
SMOKE

msg "e2e test passed"
msg "local logs:   $LOG_DIR"
msg "local audit db (sqlite): $AUDIT_DB"
msg "remote log:   $REMOTE_DIR/server.log"
msg "PTY manual acceptance (real claude attach) is not covered here; use deploy/minimal/start-rclaude.sh."
