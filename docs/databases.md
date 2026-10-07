# Managed databases

SynCloud runs dedicated managed databases. Each one gets its own containers, volumes and password, and nothing is shared with other databases.

The first engine is **Valkey**, the BSD-licensed continuation of Redis: same protocol, same commands, same client libraries. PostgreSQL is planned, and `synctl db engines` lists what is available.

A database is either:

- **standalone:** not tied to any project. You choose which projects, environments or services may connect, like an AWS-managed database.
- **in a project environment:** it follows the project's allowed nodes, and its environment can connect by default.

Database names are unique in the cluster, whichever kind they are.

## Create one

In the dashboard, open **Databases → New database**, or a project's **Databases** tab. The wizard asks for:

- **Engine:** the engine and its version.
- **Database:** the name, and standalone or a project environment.
- **Capacity:** memory and read replicas (both can autoscale), and CPU.
- **Data:** persistence, what happens when memory is full, and which nodes to use.
- **Network:** who may connect inside the cluster, and the optional public endpoint.

Or from the CLI:

```sh
# standalone, open to project shop, with a public TLS endpoint
synctl db create sessions --memory 256 --max-memory 2048 --access project:shop --public
# in a project environment
synctl db create cache -p shop --replicas 1 --max-replicas 3
synctl db credentials sessions
```

## Connect

### Inside the cluster

Every database has two internal endpoints:

| Database | Read-write (always the current primary) | Read-only (the replicas; the primary when there are none) |
| --- | --- | --- |
| standalone | `<db>.db.syncloud.internal:6379` | `<db>-ro.db.syncloud.internal:6379` |
| in a project | `<db>.<env>.<project>.syncloud.internal:6379` | `<db>-ro.<env>.<project>.syncloud.internal:6379` |

Only the services on the database's **access list** can connect. The list works like an AWS security group for the database. Entries are:

- `project:shop`: every service of the project;
- `environment:shop/production`: every service of one environment;
- `service:shop/production/api`: one service;
- `group:shop/backend`: the members of a security group;
- an IP address or CIDR, or `cluster` (every node).

A project database starts with its own environment on the list. A standalone database starts with an empty list. Edit it under **Connectivity**, or:

```sh
synctl db network sessions --add-access environment:billing/production
synctl db network sessions --remove-access project:shop
```

Changes apply within seconds, without restarts. The project's **Databases** tab also lists the databases elsewhere that its environment may reach.

### From outside the cluster (public endpoint)

Turn on the public endpoint under **Connectivity**, or with `synctl db network sessions --public on`. The database then gets a URL like an AWS endpoint:

| Endpoint | Use |
| --- | --- |
| `<db>.db.<base-domain>:6379` | reads and writes |
| `<db>-ro.db.<base-domain>:6379` | reads |

- **TLS is required.** Use `rediss://default:<password>@<db>.db.<base-domain>:6379`, or `redis-cli --tls -h <db>.db.<base-domain>`.
- **How it is served:** Traefik on the controller and on edge nodes terminates TLS with the platform's certificate for that name. It then forwards to the current primary over the private network, and follows a failover within seconds.
- **Allowed client addresses:** `--allow 203.0.113.0/24` limits who can connect. The default is anywhere, and the password still protects it.
- **Firewall:** the host firewall opens port 6379 on the controller and edge nodes only while at least one database is public.
- **Base domain:** public endpoints need one (Settings › Domains), because their certificates are issued for it.
  - With the default sslip.io domain, the names resolve on their own.
  - With your own domain, point `*.db.<your-domain>` at the controller (or at the edge nodes) with a wildcard DNS record.

### Credentials

The user is `default`. The password is under **Connect → Show password**, or `synctl db credentials`, which also prints the ready-made URLs. Revealing the password is recorded in the audit log.

Apps can't run administration commands (`CONFIG`, `ACL`, `REPLICAOF`, `SHUTDOWN`, `MODULE`, `DEBUG`, …). SynCloud manages those.

## Failover

When a database can have replicas (maximum replicas ≥ 1), three **Sentinels** run on different nodes and watch the primary. If the primary stops answering for 5 seconds, they promote the most up-to-date replica. The read-write endpoint follows within a few more seconds, so clients only need to reconnect; most client libraries do this automatically. The old primary rejoins as a replica when it comes back. In the end-to-end test, writes resumed about 10 seconds after the primary froze.

- Health is **healthy** only when failover is possible: all members are running, and every Sentinel knows every replica.
- **Failover** on the database page, or `synctl db failover`, promotes a replica on purpose, e.g. before node maintenance.
- A database without replicas is a single server. If its node goes down, the database is down until the node returns. Its data is kept on the node.
- A replica or Sentinel whose node is gone for 5 minutes is replaced on another node. A new replica copies the data from the primary.

## Autoscaling

Both dimensions scale only when their minimum is below their maximum.

- **Memory** (`maxmemory`) is changed online, with no restart:
  - **Up:** +50% when more than 85% is used for 30 seconds, and every member's node has room.
  - **Down:** −25% after 30 minutes under 40%. Never below 1.3× what's used.
- **Read replicas** follow the read CPU, as % of one core: the replicas' average, or the primary's when there are none.
  - **Out:** one more replica after a minute above the target (60% by default).
  - **In:** one fewer after 10 minutes below half the target.

Each change waits 2 minutes after the previous one in the same dimension. Every change is listed on the **Autoscaling** tab with its reason, along with the current measurements and anything blocking a scale-up, such as a node without room.

Members' containers are created for the memory maximum. Raising the maximum beyond that size restarts the members one at a time: replicas first, then the primary after a failover. The same rolling restart applies to changing persistence or the eviction policy.

## Explorer and console

The database page has:

- **Explorer:** browse keys by pattern (`user:*`) and type, with TTL and size. You can:
  - view and edit strings, hashes, lists, sets and sorted sets, and view streams (the first 500 items, 100 for streams);
  - set or remove TTLs;
  - add and delete keys.
- **Console:** run commands on the primary. Administration commands and commands that block or stream (`SUBSCRIBE`, `MONITOR`) are refused. Commands are audited.
- **Metrics:** history from 15 minutes to 7 days, covering:
  - operations per second, memory against `maxmemory`, keys and clients;
  - hit rate, evictions and expirations;
  - replication lag, CPU and network.
- **Logs:** every member's and Sentinel's output.

The same from the CLI:

```sh
synctl db keys cache 'user:*'
synctl db key set cache user:1 --type hash name=Ada role=admin
synctl db cmd cache HGETALL user:1
synctl db info cache memory
synctl db metrics cache --range 6h
```

## Persistence

| Setting | On disk | A crash loses |
| --- | --- | --- |
| `aof` (default) | append-only file, fsync every second, plus snapshots | at most ~1 second of writes |
| `rdb` | snapshots every 1–60 minutes, depending on activity | writes since the last snapshot |
| `none` | nothing | everything on that member (replicas resync from the primary) |

Data lives in node-local volumes (`syncloud-db-<id>-m<n>`). Deleting a database deletes its volumes.

## Placement

Members of one database always run on different nodes. A project database follows its project's allowed nodes. A standalone database may use any node. Either can be limited further under **Settings → Nodes** (see [Nodes](nodes.md)). Database reservations count when the scheduler places services. A member reserves its CPU and about 1.2× its current memory.
