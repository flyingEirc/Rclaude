#!/bin/sh
set -eu

# Test-flow server launcher for the fixed remote Linux box described by the
# rclaude-remote-local-test skill. It:
#   1. cross-compiles the current app/server for linux/amd64,
#   2. ships the binary + server config to <remote>:/etc/rclaude,
#   3. (re)starts rclaude-server there, detached, and confirms it is up.
#
# All connection facts come from the skill and are treated as fixed. Override
# them with the RCLAUDE_* env vars below if they ever change. The private key
# and token live in .list/ (gitignored) and are never printed by this script.

SSH_HOST="${RCLAUDE_SSH_HOST:-root@69.63.208.133}"
SSH_KEY="${RCLAUDE_SSH_KEY:-.list/server_private_key}"
REMOTE_DIR="${RCLAUDE_REMOTE_DIR:-/etc/rclaude}"
REMOTE_MOUNT="${RCLAUDE_REMOTE_MOUNT:-/workspace}"

usage() {
  echo "usage: $0 [server-config-path]   (default: deploy/minimal/server.test.yaml)" >&2
  exit 2
}

case "${1:-}" in
  -h | --help) usage ;;
esac

config_path="${1:-deploy/minimal/server.test.yaml}"

if [ ! -f "$config_path" ]; then
  echo "server config does not exist: $config_path" >&2
  echo "generate deploy/minimal/server.test.yaml first (see deploy/minimal/README.md)" >&2
  exit 1
fi
if [ ! -f "$SSH_KEY" ]; then
  echo "ssh key not found: $SSH_KEY" >&2
  exit 1
fi
chmod 600 "$SSH_KEY" 2>/dev/null || true

SSH="ssh -i $SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
SCP="scp -i $SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"

# 1. Cross-compile the latest server for the remote (Ubuntu 22.04 x86_64).
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
server_bin="$build_dir/rclaude-server"
echo "==> building rclaude-server for linux/amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$server_bin" ./app/server

# 2. Ship the fresh binary + config to the remote /etc/rclaude.
config_name="$(basename "$config_path")"
echo "==> shipping rclaude-server + $config_name to $SSH_HOST:$REMOTE_DIR"
$SSH "$SSH_HOST" "mkdir -p '$REMOTE_DIR'"
$SCP "$server_bin" "$SSH_HOST:$REMOTE_DIR/rclaude-server"
$SCP "$config_path" "$SSH_HOST:$REMOTE_DIR/$config_name"

# 3. (Re)start rclaude-server on the remote, detached, then confirm it is up.
echo "==> (re)starting rclaude-server on $SSH_HOST"
$SSH "$SSH_HOST" "sh -s" <<REMOTE
set -eu
cd '$REMOTE_DIR'
chmod +x rclaude-server
mkdir -p '$REMOTE_MOUNT'
# Free a previous instance and any stale FUSE mount before restarting.
pkill -x rclaude-server 2>/dev/null || true
fusermount -u '$REMOTE_MOUNT' 2>/dev/null || umount '$REMOTE_MOUNT' 2>/dev/null || true
sleep 1
setsid nohup ./rclaude-server --config '$REMOTE_DIR/$config_name' >'$REMOTE_DIR/server.out' 2>&1 </dev/null &
sleep 2
if pgrep -x rclaude-server >/dev/null 2>&1; then
  echo "remote: rclaude-server running (pid \$(pgrep -x rclaude-server | tr '\n' ' '))"
else
  echo "remote: rclaude-server failed to start; last log lines:" >&2
  tail -n 40 '$REMOTE_DIR/server.out' >&2 || true
  exit 1
fi
REMOTE

echo "==> server started on $SSH_HOST"
echo "    tail remote log: $SSH $SSH_HOST 'tail -f $REMOTE_DIR/server.out'"
echo "    then run locally: sh ./deploy/minimal/start-rclaude.sh ./deploy/minimal/daemon.test.yaml"
