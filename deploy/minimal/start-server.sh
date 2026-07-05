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

# Retry short-lived ssh/scp ops: the link to the test host occasionally drops a
# transfer mid-way. scp has no resume, so re-run the whole op up to 3 times.
retry_net() {
  attempt=1
  while true; do
    if "$@"; then
      return 0
    fi
    if [ "$attempt" -ge 3 ]; then
      return 1
    fi
    echo "  network op failed; retry $attempt/2 in 3s..." >&2
    attempt=$((attempt + 1))
    sleep 3
  done
}

# 1. Cross-compile the latest server for the remote (Ubuntu 22.04 x86_64).
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
server_bin="$build_dir/rclaude-server"
echo "==> building rclaude-server for linux/amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$server_bin" ./app/server

# 2. Ship the fresh binary + config to the remote /etc/rclaude.
config_name="$(basename "$config_path")"
echo "==> shipping rclaude-server + $config_name to $SSH_HOST:$REMOTE_DIR"
retry_net $SSH "$SSH_HOST" "mkdir -p '$REMOTE_DIR'"
# Upload to a .new path: the live rclaude-server may still hold the real path,
# and overwriting a running executable fails with ETXTBSY. We swap it in below.
retry_net $SCP "$server_bin" "$SSH_HOST:$REMOTE_DIR/rclaude-server.new"
retry_net $SCP "$config_path" "$SSH_HOST:$REMOTE_DIR/$config_name"

# 3. (Re)start rclaude-server on the remote, detached, then confirm it is up.
echo "==> (re)starting rclaude-server on $SSH_HOST"
$SSH "$SSH_HOST" "sh -s" <<REMOTE
set -eu
cd '$REMOTE_DIR'
chmod +x rclaude-server.new
mkdir -p '$REMOTE_MOUNT'
# Stop the previous instance (which holds the running binary) and clear a stale
# mount, then swap the freshly uploaded binary in by rename (avoids ETXTBSY).
pkill -x rclaude-server 2>/dev/null || true
fusermount -u '$REMOTE_MOUNT' 2>/dev/null || umount '$REMOTE_MOUNT' 2>/dev/null || true
sleep 1
mv -f rclaude-server.new rclaude-server
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
