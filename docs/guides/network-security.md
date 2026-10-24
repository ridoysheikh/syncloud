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

Every node and the controller are sealed by default. The host firewall (nftables, managed from the controller) drops everything except:

- what SynCloud needs: WireGuard between nodes, the agent port and 80/443 on the controller, 80/443 on edge and load-balancer nodes;
- **SSH**, always, for disaster recovery;
- ports SynCloud opens for you: public service ports, public databases, and your **host policies**.

Nothing else on the node can open a way in. A drop in SynCloud's table wins over any other tool's accept (ufw, firewalld, Docker), ports published outside SynCloud (`docker run -p`) are closed by the forward seal, and changes made to the table by hand are undone within seconds.

### SSH access

SSH is open on port 22 and on every port `sshd` listens on, on every node, and it can't be removed. **Network → Firewall → SSH access** decides who may connect and how often:

```sh
synctl firewall ssh                                    # current settings
synctl firewall ssh --sources 203.0.113.0/24           # only from the office
synctl firewall ssh --anywhere --rate 10 --burst 5 --auto-ban --ban-minutes 30
```

The default allows SSH from anywhere, with at most 10 new connections a minute per source (burst 5). A source over the limit is **auto-banned** on that node for 10 minutes. Cluster nodes are never limited.

### Blocking addresses

A blocked IP or CIDR is dropped before anything else, on every node or on one, for a while or until removed:

```sh
synctl firewall block 203.0.113.9 --for 1h
synctl firewall block 198.51.100.0/24 --node w1 --reason scanner
synctl firewall blocks
synctl firewall unblock blk_1a2b3c
```

Every drop-log row and every attempt on a node's Firewall tab has a **Block** button. Cluster nodes' addresses can't be blocked, and blocking your own address needs `--force`.

### Watching a node

**Compute → Nodes → a node → Firewall** (or `synctl firewall node ID`) shows:

- **Open ports:** each port, why it is open (SynCloud, SSH, a service, a database, a load balancer or a policy) and its hit count.
- **Listening:** every socket on the node, with its process and whether the firewall lets it be reached: open to anyone, some sources, closed by the firewall, this host only, or private network only.
- **Unauthorized attempts:** connections dropped on closed ports or over a rate limit, with source, port and packets.
- **Blocked:** the blocks that apply to the node.

The nodes list shows each node's attempts in the last hour.

### Host policies

Open more ports with **host policies**:

```sh
cat <<'JSON' | synctl firewall apply -f -
{"name": "monitoring", "targets": ["*"], "rules": [
  {"protocol": "tcp", "ports": "9100", "sources": ["203.0.113.5"], "description": "node exporter"}
]}
JSON
synctl firewall effective w1        # every rule a node enforces, built-in ones included
```

SYN cookies are on and pings are limited to 10 a second per source. Turning the whole firewall off takes the controller flag `--firewall=0`.

## Global Traefik settings

Global HTTP settings are covered in [Traefik settings](../operations/traefik.md): minimum TLS version, HSTS, trusted proxies, timeouts.
