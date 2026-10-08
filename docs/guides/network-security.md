# Network security

There are three layers, from the inside out:

| Layer | Controls | Default |
| --- | --- | --- |
| **Security groups** | which containers may talk to which | services of the same environment reach each other; outbound is open |
| **Middlewares** | what public HTTP requests reach a service | nothing extra |
| **Host firewall** | what reaches each node from outside | only what the cluster needs |

## Security groups

A security group is a set of allow rules attached to services. A service uses the groups attached to it. If it has none, it uses its project's **default group**: same-environment traffic in, everything out. Anything not allowed is dropped.

```sh
cat <<'JSON' | synctl sg apply -p shop -f -
{"name": "db", "description": "Postgres",
 "inbound": [{"protocol": "tcp", "ports": "5432", "peers": ["environment:self", "service:billing/production/worker"]}],
 "outbound": [{"protocol": "udp", "ports": "53", "peers": ["any"]}],
 "services": ["production/db"]}
JSON
```

Useful commands:

- `synctl sg apply --dry-run` shows what a change would do before it applies.
- `synctl sg check shop/production/web shop/production/db` answers: can this reach that? And why?
- `synctl sg service web -p shop` shows the rules that apply to a service, with hit counters.

The **drop log** (**Network → Security groups**) shows connection attempts that were blocked. Databases have their own [access list](valkey.md#who-may-connect).

## Middlewares

Attach ready-made Traefik middlewares to a service's public routes under **Network → Routing**:

| Middleware | Does |
| --- | --- |
| IP allow-list | only these client addresses may connect |
| Rate limit | requests per second per client IP, with a burst |
| Basic auth | a username and password prompt |
| CORS | cross-origin access from chosen origins |
| Security headers | HSTS, frame-deny, nosniff, referrer policy |
| Redirect www | `www.example.com` → `example.com` |
| Circuit breaker | stop forwarding while the service fails |
| Compress | gzip, Brotli and Zstandard responses |
| Retry | retry on another task |

```sh
echo '{"name":"limit","type":"rate-limit","config":{"average":20,"burst":40},"services":["production/web"]}' \
  | synctl middlewares apply -p shop -f -
```

For anything else, the routing page has a validated raw YAML editor.

## Host firewall

Each node's firewall (nftables) is managed from the controller. It allows only what the cluster needs (80/443 where Traefik runs, the agent port, WireGuard, and public service ports), and drops the rest. Add your own rules, such as SSH from the office, as **host policies**:

```sh
cat <<'JSON' | synctl firewall apply -f -
{"name": "ssh", "targets": ["*"], "rules": [
  {"protocol": "tcp", "ports": "22", "sources": ["203.0.113.5"], "description": "SSH from the office"}
]}
JSON
synctl firewall effective w1        # every rule a node enforces, built-in ones included
```

> Before you install, make sure SSH stays reachable: either add a host policy for it, or keep the provider's own firewall open for SSH.

## Global Traefik settings

Global HTTP settings are covered in [Traefik settings](../operations/traefik.md): minimum TLS version, HSTS, trusted proxies, timeouts.
