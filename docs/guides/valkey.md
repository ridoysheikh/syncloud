# Valkey (Redis-compatible)

[Valkey](https://valkey.io) is the BSD-licensed continuation of Redis: same protocol, same commands, same client libraries. Each database gets its own containers, volumes and password.

## Create one

**Databases → New database → Valkey**, or:

```sh
synctl db create cache -p shop --replicas 1 --max-replicas 3
synctl db create sessions --memory 256 --max-memory 2048 --access project:shop --public   # standalone, public
synctl db credentials cache
```

The wizard's steps are Engine, Database (name, standalone or project), Capacity (memory and replicas, both can autoscale), Data (persistence, eviction, nodes) and Network.

## Connect

| Database | Read-write (the primary) | Read-only (the replicas) |
| --- | --- | --- |
| in a project | `<db>.<env>.<project>.syncloud.internal:6379` | `<db>-ro.<env>.<project>.syncloud.internal:6379` |
| standalone | `<db>.db.syncloud.internal:6379` | `<db>-ro.db.syncloud.internal:6379` |

The user is `default`. Find the password under **Connect → Show password** or with `synctl db credentials` (which is audited). Administration commands (`CONFIG`, `ACL`, `SHUTDOWN`, …) are reserved for the platform.

### Who may connect

The **access list** works like a cloud security group for the database. Its entries are:

- `project:shop`: every service of a project;
- `environment:shop/production`: every service of one environment;
- `service:shop/production/api`: one service;
- `group:shop/backend`: the members of a security group;
- an IP address or CIDR, or `cluster` (every node).

```sh
synctl db network sessions --add-access environment:billing/production
```

Changes apply within seconds, without restarts.

### From outside the cluster

Turn on the **public endpoint**, and the database gets two ports of its own on the controller and the edge nodes, from 21000–21999: one read-write (the primary) and one read-only (the replicas, or the primary when there are none).

```sh
synctl db network sessions --public on --allow 203.0.113.0/24
synctl db credentials sessions        # publicUrl, publicPlainUrl, … with the ports
redis-cli --tls -u 'rediss://default:<password>@sessions.db.<base-domain>:21000'   # TLS
redis-cli -u 'redis://default:<password>@sessions.db.<base-domain>:21000'          # plain
```

Each port takes **TLS and plain** connections. The dashboard's **Connect** panel lists both URLs and a `redis-cli` command to copy.

- **TLS:** Traefik terminates TLS with the database's Let's Encrypt certificate. A client that sends no host name (SNI), such as `redis-cli` without `--sni`, gets the platform's certificate instead, which still verifies, so no extra flags are needed.
- **Plain:** a plain connection sends the password and your data unencrypted. Use it only from networks you trust, and limit the allowed addresses. To refuse plain connections, turn on **Require TLS** under **Connectivity**, or run `synctl db network sessions --require-tls on`.
- **Shared port:** the older address `<db>.db.<base-domain>:6379` still works for TLS clients that send SNI.
- **Traefik restart:** turning the endpoint on or off restarts Traefik for a moment, because the ports are added or removed.

If a cloud firewall sits in front of your servers, allow 21000–21999/tcp from the client addresses. With your own base domain, point `*.db.<your-domain>` at the controller or the edge nodes.

- **`certificate verify failed`**: the certificate is still being issued, in the first minute after turning the endpoint on.
- **`Protocol error, got "H"`** or **`I/O error`** with `redis://`: you used the shared port 6379, which is TLS only, or the database requires TLS. Use its own port, or `rediss://`.

## Failover and autoscaling

- **Failover:** with replicas, three Sentinels on different nodes watch the primary. If it stops answering for 5 seconds, the most up-to-date replica is promoted, and clients only need to reconnect.
- **Memory** (`maxmemory`) scales online: +50% above 85% used, −25% after 30 minutes under 40%.
- **Read replicas** follow read CPU (60% of a core by default): one more after a minute above, one fewer after 10 minutes under half.

The **Autoscaling** tab lists every change with its reason.

## Persistence

| Setting | A crash loses |
| --- | --- |
| `aof` (default): append-only file, fsync every second | at most about 1 second of writes |
| `rdb`: snapshots every 1–60 minutes | writes since the last snapshot |
| `none`: memory only, a pure cache | everything on that member |

Data lives in node-local volumes, and deleting the database deletes them.

## Explorer and console

On the database's page:

- **Explorer:** browse keys by pattern and type; view and edit strings, hashes, lists, sets and sorted sets; set TTLs.
- **Console:** run commands. They're audited; blocking and administration commands are refused.
- **Metrics:** ops/s, memory, hit rate, evictions, replication lag.

```sh
synctl db keys cache 'user:*'
synctl db cmd cache HGETALL user:1
synctl db metrics cache --range 6h
```
