#!/bin/sh
set -eu

# Export Caddy's internal-CA root certificate from the remote box to a local
# file, so the rclaude client can trust it via server.tls.ca_file when
# RCLAUDE_TLS_MODE=internal. The root cert is not secret, but we drop it under
# .list/ (gitignored) to keep test material out of git. See
# docs/design/caddy-tls-termination.md §5.

SSH_HOST="${RCLAUDE_SSH_HOST:-root@69.63.208.133}"
SSH_KEY="${RCLAUDE_SSH_KEY:-.list/server_private_key}"
OUT="${RCLAUDE_CA_OUT:-.list/caddy-internal-root.crt}"

if [ ! -f "$SSH_KEY" ]; then
  echo "ssh key not found: $SSH_KEY" >&2
  exit 1
fi
chmod 600 "$SSH_KEY" 2>/dev/null || true

SSH="ssh -i $SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"

# Caddy (systemd, running as the caddy user) keeps its local CA under the
# service data dir. Cover the common locations.
REMOTE_FIND='
for p in \
  /var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt \
  /var/lib/caddy/pki/authorities/local/root.crt \
  /root/.local/share/caddy/pki/authorities/local/root.crt ; do
  if [ -f "$p" ]; then cat "$p"; exit 0; fi
done
echo "no caddy internal root.crt found" >&2
exit 1
'

echo "==> exporting Caddy internal root CA from $SSH_HOST -> $OUT"
mkdir -p "$(dirname "$OUT")"
$SSH "$SSH_HOST" "$REMOTE_FIND" > "$OUT"

if [ ! -s "$OUT" ]; then
  echo "failed: exported CA file is empty" >&2
  rm -f "$OUT"
  exit 1
fi
echo "==> wrote $OUT"
echo "    set daemon.test.yaml server.tls.ca_file to this path"
