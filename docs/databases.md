# Managed databases

SynCloud runs dedicated managed databases. Each one gets its own containers, volumes and password, and nothing is shared with other databases.

There are two engines (`synctl db engines` lists them):

- **Valkey**, the BSD-licensed continuation of Redis: same protocol, same commands, same client libraries.
- **PostgreSQL 18 or 17** with Patroni failover and extensions for vectors, time series, geospatial and analytics (see [PostgreSQL](#postgresql)).

The sections up to [Placement](#placement) describe Valkey and what both engines share.

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

## PostgreSQL

```sh
synctl db create orders --engine postgres -p shop --memory 1024 --replicas 1
synctl db credentials orders
```

Each member runs PostgreSQL under [Patroni](https://patroni.readthedocs.io). The members run on different nodes. One is the primary, and the others stream from it.

New databases get PostgreSQL 18. Pick 17 with `--version 17` or in the wizard. Every extension below ships for both. A restore keeps its source's major version. Upgrading a database from 17 to 18 isn't offered yet.

Members and the platform etcd address each other by name. On a node outside the private network (for example a single-node development setup) those names resolve through Docker's own DNS on the node.

### Extensions

The image `ghcr.io/syncloud/postgres` includes the extensions below. Enable one with `CREATE EXTENSION` in your database.

| Extension | For |
| --- | --- |
| `vector` | pgvector: embeddings and similarity search |
| `timescaledb` | TimescaleDB, Apache-licensed edition: hypertables and time functions |
| `pg_duckdb` | the DuckDB engine for analytical queries and Parquet files |
| `postgis` | geospatial types and indexes |
| `pg_partman`, `pg_cron`, `hypopg`, `pg_stat_statements` | partitions, scheduled jobs, hypothetical indexes, query statistics |
| contrib modules | `pg_trgm`, `pgcrypto`, `hstore` and the rest |

The libraries `pg_stat_statements`, `timescaledb`, `pg_cron` and `pg_duckdb` are preloaded. Memory sets the tuning: `shared_buffers` is 25% of it, `effective_cache_size` is 75%, and `max_connections` is about one per 8 MiB, between 50 and 500.

### Endpoints and users

| URL | Goes to |
| --- | --- |
| `url`: `orders.<env>.<project>.syncloud.internal:5432` | the primary |
| `readUrl`: `orders-ro.…:5432` | the replicas, or the primary when there are none |
| `haUrl`: `m0.orders…,m1.orders…` with `target_session_attrs=read-write` | each member; the client picks the one that accepts writes |

Applications connect as `app`, which owns the database `orders`. A hyphen in the name becomes `_`. `app` is not a superuser, but it is granted `pg_monitor`, so it can read settings and query statistics. The platform keeps the superuser for itself.

### Failover

Patroni keeps its leader lock in the platform etcd. SynCloud runs that etcd itself on up to three nodes, with one user per database. When the primary stops responding, Patroni promotes the replica that is furthest ahead, normally within about 30 seconds. This does not need the controller. The controller follows the new leader and moves the `url` and `readUrl` addresses to it. While the controller is down, the `haUrl` still finds the new primary. The old primary rejoins as a replica, using `pg_rewind` when needed.

**Failover** on the database page, or `POST /api/v1/databases/<name>/failover`, performs a planned switchover to a healthy replica. With **synchronous replication** on, each commit waits until a replica has it, so a failover never loses a committed transaction.

### Explorer and administration

A PostgreSQL database page has these administration tabs:

- **Databases:** the databases in the cluster, with owner, size and connections. You can create one, rename it, change its owner or connection limit, or drop it.
  - The cluster's own database keeps its name, because the credentials use it.
  - Dropping a database disconnects its sessions first.
- **Roles:** every role, with its attributes, memberships and owned databases. Creating or editing a role opens a full page with these settings:
  - **Sign-in:** whether the role can log in; its password (generated, typed, kept or removed); expiry; and connection limit.
  - **Abilities:** create databases, create roles, and inherit privileges.
  - **Membership:** other roles and the useful predefined ones, such as `pg_read_all_data` and `pg_monitor`, with an optional admin flag.
  - **Access to databases:** read only, read and write, or none for each database. This covers every schema, table and sequence, including tables created later.
  - **Drop:** first hands the role's objects in every database to another role.
- **Explorer:** a tree of schemas, tables, views, materialized views, sequences, functions and types. System schemas are behind a toggle.
  - A table shows its columns, keys, indexes (with size and scans), constraints, triggers, statistics, and its generated `CREATE` statement.
  - The **Data** tab pages through rows. You can sort by a column, filter (`=`, `<`, `like`, `is null`, …) and count matching rows. It is read-only.
  - Every database, schema, table, sequence and function has a **Privileges** grid with one row per grantee. Ticking boxes and choosing **Apply** runs the GRANT and REVOKE statements in one transaction and shows them.
  - Choose a role to see what it can actually do, including through PUBLIC and its memberships.
  - On a schema you can also grant on all its tables, sequences or functions at once, and set default privileges for objects created later.
  - The database node lists the extensions you can install, update or drop in that database.
- **Console:** runs SQL against any database in the cluster.
  - **Read-only:** this is the default. Each statement runs in its own read-only transaction, so a `COMMIT` in the text does not end it.
  - **Writes allowed:** statements commit, and the SQL text is recorded in the audit log.
  - **Run as:** the console runs as `app`, or as a role `app` may become through **Run as**. It never runs as the superuser.
  - Ctrl+Enter runs the selection, or everything. `EXPLAIN (ANALYZE, FORMAT JSON)` is drawn as a plan tree, and recent queries are kept in your browser.
- **Sessions:** client connections with their state, wait events, blocking sessions and query. You can cancel a query or end a session. The platform's own sessions are hidden unless you ask for them.

What is protected:

- `syncloud_admin`, `replicator` and the `postgres` database can be viewed but not changed.
- `SUPERUSER`, `REPLICATION`, `BYPASSRLS` and the file-access roles cannot be granted.
- `app` keeps its name, password and login, because the credentials and the console use them.

Passwords are hashed (SCRAM-SHA-256) before they reach the server. SynCloud does not store them, so a generated password is shown once.

Every change is audited. Each operation is its own IAM action, so browsing (`database:GetPgSchema`, `database:ReadPgRows`, `database:RunPgQuery`) can be granted without administration (`database:AlterPgRole`, `database:ChangePgPrivileges`, `database:ExecutePgQuery`).

The same operations are available from the CLI:

```sh
synctl db sql orders "SELECT count(*) FROM items"            # read-only
synctl db sql orders --write - < migration.sql                 # writes, from stdin
synctl db sql orders --as reporting "SELECT * FROM sales"      # as another role
synctl db roles orders
synctl db role create orders reporting --member-of pg_monitor  # prints the password once
synctl db grant orders reporting --access read                 # whole database, now and later
synctl db grant orders reporting --privileges SELECT,INSERT --on table:public.items
synctl db revoke orders reporting --privileges ALL --on all-tables:public
synctl db privileges orders --on table:public.items --role reporting
synctl db role drop orders reporting --reassign-to app
synctl db databases orders
synctl db database create orders analytics
synctl db schema orders
synctl db describe orders public.items --ddl
synctl db rows orders items --where 'status=open' --order id --desc --count
synctl db extension install orders vector --db analytics
synctl db sessions orders
synctl db session cancel orders 4242
```

### Backups and point-in-time recovery

PostgreSQL databases are backed up with [WAL-G](https://github.com/wal-g/wal-g) to any S3 endpoint registered under **Storage**: AWS S3, R2, B2, MinIO, and others. Turn backups on from the database's **Backups** tab, or from the CLI:

```sh
synctl db backup config orders --endpoint r2 --bucket pg-backups      # every 24 h, keep 7 + 7 days
synctl db backup config orders --every 6 --retain-full 14 --retain-days 30
```

- **Continuous archiving:** every member ships each WAL segment to S3 as it fills, and at least once a minute (`archive_timeout` 60 s). So no more than about a minute of committed writes is at risk.
- **Base backups:** a short-lived task takes a full copy on a schedule, and you can start one with **Back up now** or `synctl db backup now orders`.
  - The task copies from a streaming replica when there is one, so the primary does not pay for it.
  - It runs next to that member, using the member's volume (read-only) and network.
  - Old backups are pruned: the newest *N* are kept, and so is everything from the last *D* days.
- **Encryption:** backups are encrypted with a key that only this database's members hold.
- **Replicas:** new replicas start from the latest base backup instead of copying the primary.
- **Status:** the tab shows the base backups, recent runs, the last archived WAL (with a warning when archiving fails), and the **restore window**. That window runs from the oldest base backup to the newest archived WAL.

**Restore** always creates a **new** database; the source is never touched. Choose a moment in the window (or the latest state, a clone):

```sh
synctl db backups orders                                           # the window
synctl db restore orders orders-before-migration --time 2026-10-07T14:05:00Z
synctl db restore orders orders-copy --standalone                  # latest state
```

The new database:

- starts from the newest base backup that finished before that moment and replays the archived WAL up to it, then opens for writes on a new timeline;
- keeps the source's database name, roles and passwords, so apps can switch to it by changing only the host;
- archives to the same bucket under its own prefix.

The members and the restored database must be able to reach the S3 endpoint. For an S3 service inside the cluster (MinIO, Garage, …), that means its security group must let in the database: its project environment, or for a standalone database, the database itself.

Turning backups off stops archiving, but what is in S3 stays. Changing backup settings restarts the members one at a time (with a switchover).

Coming next: PgBouncer pooling, the public STARTTLS endpoint, parameters, metrics, replica autoscaling, and pg_duckdb reading from S3.
