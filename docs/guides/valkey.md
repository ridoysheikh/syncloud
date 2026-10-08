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

Turn on the **public endpoint**, and the database is served with TLS at `<db>.db.<base-domain>:6379` (and `<db>-ro…`):

```sh
synctl db network sessions --public on --allow 203.0.113.0/24
redis-cli --tls --sni sessions.db.<base-domain> -u 'rediss://default:<password>@sessions.db.<base-domain>:6379'
```

The endpoint is TLS only, and the client must send the host name (SNI): one port serves every public database, and the name says which one. Client libraries do this on their own; `redis-cli` and `valkey-cli` need `--sni`. The dashboard's **Connect** panel has the command ready to copy.

- **`certificate verify failed`**: SNI is missing (add `--sni`), or the certificate is still being issued in the first minute after turning the endpoint on.
- **`Protocol error, got "H"`** or **`I/O error`**: the client connected without TLS (`redis://`). Use `rediss://` or `--tls`.

With your own base domain, point `*.db.<your-domain>` at the controller or the edge nodes.

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
