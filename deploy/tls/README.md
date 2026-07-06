# TLS via Caddy (minimal)

Put a Caddy reverse proxy in front of the plaintext `rclaude-server` so the
public link is TLS, while the server binary stays h2c (no TLS code). Design:
`docs/design/caddy-tls-termination.md`.

```
client ──TLS(HTTP/2)──▶ Caddy :443 ──h2c(cleartext HTTP/2)──▶ rclaude-server 127.0.0.1:<port>
```

The client change is one config block (`server.tls`), consumed by the single
dial site `pkg/transport/client.go`. The server needs no change.

## Files

| File | Role |
| --- | --- |
| `Caddyfile.example` | Documented Caddyfile template (h2c upstream + long-stream notes). |
| `install-caddy.sh` | SSHes to the remote, installs Caddy, renders `/etc/caddy/Caddyfile`, validates + restarts it. |
| `fetch-internal-ca.sh` | Exports Caddy's internal root CA to `.list/` for the client `ca_file` (internal mode only). |

## Two cert modes

- **acme** — automatic HTTPS (Let's Encrypt), publicly trusted. Requires the
  domain's DNS to point **straight at this origin** and `:80/:443` reachable.
  Client uses system roots (`server.tls.ca_file` empty).
- **internal** (default) — Caddy's local CA (self-signed). Works with no public
  ACME and bypasses any CDN. Client trusts the exported root via
  `server.tls.ca_file`.

### Cloudflare caveat

If the domain is **proxied** through Cloudflare (orange cloud), it will not work:
gRPC bidirectional streaming isn't supported through the proxy and the ~100s
edge timeout kills long-lived interactive PTY streams; source-side ACME also
fails. Set the record to **DNS only** (grey cloud) for `acme`, or use `internal`
mode with the client pointed at the origin IP (SNI = domain).

## Test-flow usage (remote box from the rclaude-remote-local-test skill)

Server first (unchanged): `sh deploy/minimal/start-server.sh deploy/minimal/server.test.yaml`

Then Caddy in front of it:

```sh
# Publicly-trusted (domain must be DNS-only to the origin):
RCLAUDE_DOMAIN=rclaude.example.com RCLAUDE_UPSTREAM=127.0.0.1:7969 \
  RCLAUDE_TLS_MODE=acme sh deploy/tls/install-caddy.sh

# Or internal CA (bypasses CDN, no public ACME):
RCLAUDE_DOMAIN=rclaude.example.com RCLAUDE_UPSTREAM=127.0.0.1:7969 \
  RCLAUDE_TLS_MODE=internal sh deploy/tls/install-caddy.sh
sh deploy/tls/fetch-internal-ca.sh            # -> .list/caddy-internal-root.crt
```

Point the daemon at the TLS endpoint by adding a `server.tls` block to
`deploy/minimal/daemon.test.yaml` (see `deploy/minimal/daemon.example.yaml` for
the documented shape), then run `sh deploy/minimal/start-rclaude.sh` as usual.
