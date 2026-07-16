#!/bin/sh
set -eu

# Install + configure Caddy on the fixed remote Linux box so it terminates TLS
# in front of the plaintext (h2c) rclaude-server. See
# docs/design/caddy-tls-termination.md. Connection facts mirror
# deploy/minimal/start-server.sh (skill: rclaude-remote-local-test); secrets stay
# in .list/ and are never printed.
#
# Usage:
#   RCLAUDE_DOMAIN=rclaude.example.com \
#   [RCLAUDE_UPSTREAM=127.0.0.1:7969] \
#   [RCLAUDE_TLS_MODE=internal|acme] \
#     sh deploy/tls/install-caddy.sh
#
#   RCLAUDE_TLS_MODE=acme     -> automatic HTTPS via Let's Encrypt (public cert).
#                                Requires DNS pointing straight at this origin
#                                (Cloudflare record must be "DNS only"/grey) and
#                                reachable :80/:443. Client uses system roots.
#   RCLAUDE_TLS_MODE=internal -> Caddy's local CA (self-signed). Bypasses any CDN
#                                and needs no public ACME. Client must trust the
#                                exported root CA (deploy/tls/fetch-internal-ca.sh)
#                                via server.tls.ca_file. This is the default.

SSH_HOST="${RCLAUDE_SSH_HOST:-root@69.63.208.133}"
SSH_KEY="${RCLAUDE_SSH_KEY:-.list/server_private_key}"
UPSTREAM="${RCLAUDE_UPSTREAM:-127.0.0.1:7969}"
TLS_MODE="${RCLAUDE_TLS_MODE:-internal}"
DOMAIN="${RCLAUDE_DOMAIN:-}"

if [ -z "$DOMAIN" ]; then
  echo "RCLAUDE_DOMAIN is required (e.g. rclaude.example.com)" >&2
  exit 2
fi
if [ ! -f "$SSH_KEY" ]; then
  echo "ssh key not found: $SSH_KEY" >&2
  exit 1
fi
chmod 600 "$SSH_KEY" 2>/dev/null || true

case "$TLS_MODE" in
  internal) TLS_LINE="	tls internal" ;;
  acme) TLS_LINE="" ;;
  *)
    echo "RCLAUDE_TLS_MODE must be 'internal' or 'acme' (got: $TLS_MODE)" >&2
    exit 2
    ;;
esac

SSH="ssh -i $SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"

echo "==> installing + configuring Caddy on $SSH_HOST"
echo "    domain=$DOMAIN upstream=h2c://$UPSTREAM tls_mode=$TLS_MODE"

# Render the Caddyfile locally (no secrets in it) and stream it over stdin so the
# remote heredoc stays free for the setup script.
caddyfile="$DOMAIN {
	reverse_proxy h2c://$UPSTREAM {
		transport http {
			versions h2c
			dial_timeout 5s
		}
		flush_interval -1
	}
$TLS_LINE
}
"

printf '%s' "$caddyfile" | $SSH "$SSH_HOST" "cat > /etc/caddy/Caddyfile.rclaude.new" 2>/dev/null || {
  # /etc/caddy may not exist yet before install; stage in /tmp and move later.
  printf '%s' "$caddyfile" | $SSH "$SSH_HOST" "cat > /tmp/Caddyfile.rclaude.new"
}

$SSH "$SSH_HOST" "sh -s" <<REMOTE
set -eu

# 1. Install Caddy from the official apt repo if absent.
if ! command -v caddy >/dev/null 2>&1; then
  echo "remote: installing caddy via official apt repo"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gnupg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq
  apt-get install -y -qq caddy
else
  echo "remote: caddy already installed ($(caddy version | head -n1))"
fi

mkdir -p /etc/caddy
# Move the staged Caddyfile into place (whichever path landed).
if [ -f /tmp/Caddyfile.rclaude.new ]; then
  mv -f /tmp/Caddyfile.rclaude.new /etc/caddy/Caddyfile
elif [ -f /etc/caddy/Caddyfile.rclaude.new ]; then
  mv -f /etc/caddy/Caddyfile.rclaude.new /etc/caddy/Caddyfile
fi
echo "remote: /etc/caddy/Caddyfile ->"
sed 's/^/    | /' /etc/caddy/Caddyfile

# 2. Validate then (re)load. caddy validate catches syntax before we bounce it.
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
systemctl enable caddy >/dev/null 2>&1 || true
systemctl restart caddy
sleep 2
if systemctl is-active --quiet caddy; then
  echo "remote: caddy running; listeners:"
  (ss -ltnp 2>/dev/null | grep -E ':(80|443)[[:space:]]' || true)
else
  echo "remote: caddy failed to start; recent journal:" >&2
  journalctl -u caddy --no-pager -n 30 >&2 || true
  exit 1
fi
REMOTE

echo "==> Caddy configured on $SSH_HOST for $DOMAIN"
if [ "$TLS_MODE" = "internal" ]; then
  echo "    next: sh deploy/tls/fetch-internal-ca.sh   # export root CA for the client"
fi
