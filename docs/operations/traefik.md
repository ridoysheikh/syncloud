# Traefik settings

**Network → Traefik** holds the options for every Traefik replica, on the controller and on edge nodes. The same options are available as `synctl traefik settings`.

## Applied live

These are part of the configuration Traefik polls, so they apply within about two seconds.

| Setting | Default | Notes |
| --- | --- | --- |
| Redirect HTTP to HTTPS | on | When off, services also answer on plain HTTP. |
| Minimum TLS version | 1.2 | Set 1.3 for modern clients only. |
| Strict SNI | off | Refuses TLS clients that don't name a known domain. |
| HSTS max-age | 0 (off) | |
| Retry attempts | 2 | On another task, idempotent requests only. 0 turns retries off. A service's retry middleware replaces it. |
| Compress responses | off | |
| Request body limit | unlimited | Larger requests get 413. |

These run on every service route before the service's own middlewares.

## Restart Traefik

These become Traefik command-line flags. Saving a change restarts every replica, which takes a few seconds each.

| Setting | Default | Notes |
| --- | --- | --- |
| Read, write and idle timeouts | Traefik's: 60s, none, 180s | Of the public entrypoints. |
| Trusted proxies | none | IPs or CIDRs whose `X-Forwarded-*` headers are kept. **Use Cloudflare ranges** fills in Cloudflare's published list. |
| PROXY protocol | off | From trusted proxies only. |
| HTTP/3 | off | Over UDP on the HTTPS port. |
| Connect timeout | 2s | So a request to a crashed node's task is retried on another one quickly. |
| Response header timeout and idle connections per task | Traefik's | For connections to tasks. |
| Log level | INFO | |

The page shows the exact flags these settings add.

Example:

```sh
synctl traefik settings
synctl traefik settings set minTls=1.3 hstsSeconds=31536000 compress=true
synctl traefik settings set trustedIPs=173.245.48.0/20,103.21.244.0/22 readTimeout=120s
```

## Other routing pages

- **Network → Routing:** per-service middlewares (rate limits, passwords, allow-lists, CORS and more), a validated raw YAML editor for anything else, and the exact configuration served to Traefik.
- **Network → Traffic:** live load, the traffic map and the request tail.

---

Related: [Domains and ports](../guides/domains-and-ports.md) · [Network security](../guides/network-security.md)
