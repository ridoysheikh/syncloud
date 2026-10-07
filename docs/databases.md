# Managed databases (Valkey)

SynCloud runs dedicated **Valkey** databases for your projects. Valkey is the BSD-licensed continuation of Redis: same protocol, same commands, same client libraries. Each database gets its own containers, volumes and password. Nothing is shared with other databases.

## Create one

In the dashboard, open **Databases → New database**, or a project's **Databases** tab. The wizard asks for:

- **Database:** project, environment, name and Valkey version.
- **Capacity:** memory and read replicas (both can autoscale), and CPU.
- **Data:** persistence, what happens when memory is full, and which nodes to use.

Or from the CLI:

```sh
synctl db create cache -p shop --memory 256 --max-memory 2048 --replicas 1 --max-replicas 3
synctl db credentials cache -p shop
```

## Connect

Every database has two endpoints inside the cluster, reachable from any service:

| Endpoint | Use |
| --- | --- |
| `<db>.<env>.<project>.syncloud.internal:6379` | reads and writes (always the current primary) |
| `<db>-ro.<env>.<project>.syncloud.internal:6379` | reads, spread over the replicas (the primary when there are none) |

The user is `default`. The password is under **Connect → Show password**, or `synctl db credentials`; revealing it is recorded in the audit log. A ready-made URL is `redis://default:<password>@<host>:6379`.

Apps can't run administration commands (`CONFIG`, `ACL`, `REPLICAOF`, `SHUTDOWN`, `MODULE`, `DEBUG`, …). SynCloud manages those. The environment's security groups apply to databases as they do to services.

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
synctl db keys cache 'user:*' -p shop
synctl db key set cache user:1 --type hash name=Ada role=admin -p shop
synctl db cmd cache -p shop HGETALL user:1
synctl db info cache memory -p shop
synctl db metrics cache --range 6h -p shop
```

## Persistence

| Setting | On disk | A crash loses |
| --- | --- | --- |
| `aof` (default) | append-only file, fsync every second, plus snapshots | at most ~1 second of writes |
| `rdb` | snapshots every 1–60 minutes, depending on activity | writes since the last snapshot |
| `none` | nothing | everything on that member (replicas resync from the primary) |

Data lives in node-local volumes (`syncloud-db-<id>-m<n>`). Deleting a database deletes its volumes.

## Placement

Members of one database always run on different nodes. A database follows its project's allowed nodes, and can be limited further under **Settings → Nodes** (see [Nodes](nodes.md)). Database reservations count when the scheduler places services. A member reserves its CPU and about 1.2× its current memory.
