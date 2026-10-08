# PostgreSQL administration

A PostgreSQL database's page has tabs that cover what pgAdmin does.

## Tabs

| Tab | What you can do |
| --- | --- |
| **Databases** | create, rename and drop databases in the cluster, and change their owner or connection limit |
| **Roles** | create and edit roles: sign-in and password, expiry, abilities, memberships, and read or write access per database (now and for tables created later) |
| **Explorer** | browse schemas, tables, views, sequences, functions and types; see columns, indexes, constraints, statistics and the `CREATE` statement; page through rows with sorting and filters (read-only); edit **privileges** per object as a grid |
| **Console** | run SQL in any database. It's read-only by default; **writes allowed** commits and audits the SQL. `EXPLAIN (ANALYZE, FORMAT JSON)` is drawn as a plan tree. |
| **Sessions** | live connections with their state, wait events and blockers; cancel a query or end a session |
| **Configuration**, **Replication**, **Backups** | see [PostgreSQL](postgres.md) and [Backups](postgres-backups.md) |
| **Metrics**, **Logs** | history from 15 minutes to 7 days; every member's output |

## From the CLI

```sh
synctl db sql orders "SELECT count(*) FROM items"            # read-only
synctl db sql orders --write - < migration.sql                 # writes, from stdin
synctl db sql orders --as reporting "SELECT * FROM sales"      # as another role
synctl db role create orders reporting --member-of pg_monitor  # prints the password once
synctl db grant orders reporting --access read                 # whole database, now and later
synctl db grant orders reporting --privileges SELECT,INSERT --on table:public.items
synctl db privileges orders --on table:public.items --role reporting
synctl db role drop orders reporting --reassign-to app
synctl db database create orders analytics
synctl db describe orders public.items --ddl
synctl db rows orders items --where 'status=open' --order id --desc --count
synctl db extension install orders vector --db analytics
synctl db sessions orders
```

## What's protected

- The platform keeps the superuser. `syncloud_admin`, `replicator` and the `postgres` database can be viewed, not changed.
- `SUPERUSER`, `REPLICATION`, `BYPASSRLS` and the file-access roles can't be granted.
- `app` keeps its name, password and login, because the credentials and the console use them.
- The console never runs as the superuser. It runs as `app`, or as a role `app` may become.

Passwords are hashed (SCRAM-SHA-256) in the browser or CLI before they reach the server. SynCloud doesn't store them, so a generated password is shown once.

## Permissions

Every operation is its own IAM action, so browsing can be granted without administration:

- **browsing:** `database:GetPgSchema`, `database:ReadPgRows`, `database:RunPgQuery`;
- **administration:** `database:AlterPgRole`, `database:ChangePgPrivileges`, `database:ExecutePgQuery`.

Every change is in the audit log ([Access control](access-control.md)).

---

Back to [PostgreSQL](postgres.md) · Next: [Backups](postgres-backups.md)
