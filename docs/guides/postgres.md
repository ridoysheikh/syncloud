# PostgreSQL

Managed PostgreSQL 18 or 17, each database on its own containers and volumes, with replicas, automatic failover ([Patroni](https://patroni.readthedocs.io)), optional extensions, backups and point-in-time restore.

## Create one

**Databases → New database → PostgreSQL**, or a project's **Databases** tab. Or:

```sh
synctl db create orders --engine postgres -p shop --memory 1024 --replicas 1
synctl db credentials orders          # URLs, user and password
```

A database is either:

- **in a project environment:** it follows the project's allowed nodes, and its environment can connect by default;
- **standalone** (`--standalone`): not tied to a project. You choose who may connect, like a cloud-managed database.

Names are unique in the cluster.

> **The first PostgreSQL database takes a few minutes longer.** Its image comes with the SynCloud release (about 400 MB). The controller downloads it once from the release, loads it into the built-in registry, and nodes pull it from there. The database shows *waiting for the PostgreSQL image* meanwhile. On an air-gapped host, put `syncloud-postgres-<tag>-linux-amd64.tar.gz` from the release into `/usr/local/lib/syncloud/downloads/images/` first. The image is built for x86-64 nodes.

## Connect

| URL | Goes to |
| --- | --- |
| `url`: `orders.<env>.<project>.syncloud.internal:5432` | the primary (reads and writes) |
| `readUrl`: `orders-ro.…:5432` | the replicas, or the primary when there are none |
| `haUrl`: every member, with `target_session_attrs=read-write` | whichever member accepts writes; works even while the controller is down |

Apps connect as `app`, which owns the database `orders` (a `-` in the name becomes `_`). `app` isn't a superuser, but it has `pg_monitor`. Paste the URL from `synctl db credentials` into the service's variables, for example as `DATABASE_URL`.

**Who may connect** is the database's **access list**, under **Connectivity**: projects, environments, services, security groups, IPs or CIDRs. A project database starts with its own environment on the list ([Valkey › access list](valkey.md#who-may-connect) explains the entries; they're the same).

```sh
synctl db network orders --add-access environment:billing/production
synctl db network orders --public on --allow 203.0.113.0/24     # a public endpoint
```

The public endpoint gives the database two ports of its own, from 21000–21999: read-write and read-only. `synctl db credentials orders` prints the URLs. Each port takes:

- **TLS:** `sslmode=require`, `verify-full`, and PostgreSQL 17's direct TLS (`sslnegotiation=direct`). Clients that send no host name (SNI) work too.
- **Plain:** `sslmode=disable`. It sends the password and data unencrypted, so use it only from trusted networks. **Require TLS** under **Connectivity** (`synctl db network orders --require-tls on`) refuses plain connections.

The older shared address `orders.db.<base-domain>:5432` still works for TLS clients that send SNI (libpq 14 or later). Turning the endpoint on or off restarts Traefik for a moment. If a cloud firewall sits in front of your servers, allow 21000–21999/tcp.

## Size and replicas

| Setting | Notes |
| --- | --- |
| **Memory** | sets `shared_buffers` (25%), `effective_cache_size` (75%) and `max_connections` (about one per 8 MiB, 50–500) |
| **Read replicas** | 0–5. They stream from the primary on other nodes and serve `readUrl`. |
| **CPU** | reserved per member |

Members run on different nodes when there are enough. On a single-server cluster they share the node, which protects against a failed container but not a lost server; the database page says so. A database without replicas is a single server: if its node goes down, the database is down until the node returns. Its data stays on the node.

## Failover

Patroni holds its leader lock in an etcd that SynCloud runs for all databases. When the primary stops responding, the most up-to-date replica is promoted, usually within about 30 seconds, **without needing the controller**. The `url` and `readUrl` addresses follow, and the old primary rejoins as a replica.

**Failover** on the database page performs a planned switchover, for example before node maintenance.

### Replication modes

Set these in the wizard or on the **Configuration** tab. Changes apply without restarts.

| Mode | A failover can lose… |
| --- | --- |
| **Asynchronous** (default) | the last moments of writes |
| **Synchronous** | nothing; with no replica left, writes continue unprotected |
| **Synchronous, strict** | nothing; writes stop rather than continue without a replica |

```sh
synctl db replication orders                                   # what the primary sees: lag, slots
synctl db replication set orders --mode sync --sync-replicas 1
```

## Add-ons

A new database is plain PostgreSQL. Enable extensions when you need them, in the wizard, on the **Configuration** tab, or with `synctl db addon enable orders vector`. Then run `CREATE EXTENSION` in the databases that use them.

| Add-on | For |
| --- | --- |
| `vector` | pgvector: embeddings and similarity search |
| `timescaledb` | TimescaleDB (Apache edition): hypertables and time functions |
| `postgis` | geospatial types and indexes |
| `pg_duckdb` | DuckDB for analytical queries and Parquet files |
| `pg_cron` | scheduled SQL |
| `pg_partman` | partition management |
| `hypopg` | hypothetical indexes |

The contrib modules (`pg_trgm`, `pgcrypto`, `citext`…) are always available. Add-ons with a library (`timescaledb`, `pg_duckdb`, `pg_cron`) restart the members one at a time when enabled.

## Parameters

About 50 curated settings (memory, parallelism, planner, WAL, timeouts, autovacuum, logging…) can be changed. Values are checked before they're saved. Settings that need a restart roll through the members, replicas first.

```sh
synctl db settings orders                          # every parameter with its live value
synctl db config set orders work_mem=64MB statement_timeout=30s
```

---

More: [Administration: roles, privileges, SQL console](postgres-admin.md) · [Backups and point-in-time restore](postgres-backups.md)
