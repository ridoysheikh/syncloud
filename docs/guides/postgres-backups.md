# PostgreSQL backups and point-in-time restore

PostgreSQL databases back up with [WAL-G](https://github.com/wal-g/wal-g) to any S3 endpoint registered under **Storage** ([S3 storage](storage.md)).

## Turn backups on

On the database's **Backups** tab, or:

```sh
synctl db backup config orders --endpoint r2 --bucket pg-backups                    # daily, keep 7 + 7 days
synctl db backup config orders --every 6 --retain-full 14 --retain-days 30
synctl db backup now orders
```

What you get:

- **Continuous archiving:** every member ships each WAL segment as it fills, and at least once a minute. At most about a minute of committed writes is at risk.
- **Base backups** on a schedule. They're taken from a replica when there is one, so the primary doesn't pay. Old ones are pruned: the newest *N* are kept, plus everything from the last *D* days.
- **Encryption** with a key only this database's members hold.
- **Faster replicas:** new replicas start from the latest base backup instead of copying the primary.

The tab shows the base backups, recent runs, the last archived WAL (with a warning if archiving fails), and the **restore window**: from the oldest base backup to the newest archived WAL.

## Restore

A restore always creates a **new** database, and the source is never touched. Choose a moment in the window, or the latest state for a clone:

```sh
synctl db backups orders                                                # the window
synctl db restore orders orders-before-migration --time 2026-10-07T14:05:00Z
synctl db restore orders orders-copy --standalone                       # the latest state
```

In the dashboard: **Backups → Restore**.

The new database:

- replays the archive up to that moment, then opens for writes on a new timeline;
- keeps the source's database name, roles and passwords, so apps switch by changing only the host;
- archives to the same bucket under its own prefix.

## Restore from another server

A WAL-G archive written by a PostgreSQL server **outside SynCloud** (a VM, another platform, a self-managed Patroni cluster) restores into a new managed database. SynCloud only reads the archive.

1. Register the S3 endpoint the archive is on under **Storage**.
2. Look at what the archive holds: its PostgreSQL version, whether it's encrypted, its base backups and the restore window.
3. Restore, to a moment or to the archive's end:

```sh
synctl db archive --endpoint aws --bucket old-backups --prefix walg/main
synctl db restore-archive shop -p shop --endpoint aws --bucket old-backups --prefix walg/main --database shop
synctl db restore-archive shop --standalone --endpoint aws --bucket old-backups --prefix walg/main \
  --key-file walg.key --key-transform hex --time 2026-10-12T19:00:00Z    # an encrypted archive, to a moment
```

In the dashboard: **Databases → Restore from another server**.

- **Prefix** is the server's `WALG_S3_PREFIX` without `s3://bucket/` (the folder that holds `basebackups_005/` and `wal_005/`).
- **Key:** an encrypted archive needs the server's `WALG_LIBSODIUM_KEY`, written as it is set there (`WALG_LIBSODIUM_KEY_TRANSFORM`: `none`, the WAL-G default, `hex` or `base64`). It's stored sealed with the new database's secrets. Archives encrypted with OpenPGP aren't supported.
- **Version:** the new database runs the archive's PostgreSQL major version (17 or 18).

The new database is an ordinary SynCloud database from then on:

- It gets **SynCloud's own passwords** for the superuser, the replication user and the app user (`app`). Get them with `synctl db credentials NAME`. The old server's roles stay as they were, with their old passwords.
- `app` becomes the owner of the **app database** (`--database`, by default the new database's name) and of the schemas, tables, views, sequences, functions and types in it. Objects of extensions keep their owner. If the archive has no database by that name, an empty one is created.
- The old server's `postgresql.conf`, `postgresql.auto.conf`, `pg_hba.conf` and `pg_ident.conf` are not used. They're kept in the data directory as `*.source` for reference. Set what you need on the **Config** tab, and turn on the add-ons the data uses (TimescaleDB, pg_cron, …) on the **Extensions** tab.
- It archives under its **own** prefix (by default the archive's bucket, `syncloud-pg/<id>`), encrypted with its own key, and never writes to the source archive.

## Good to know

- **Members must reach the S3 endpoint.** For an S3 service inside the cluster (MinIO, Garage…), its [security group](network-security.md) must let the database in.
- **Turning backups off** stops archiving; what's in S3 stays.
- **Changing backup settings** restarts the members one at a time, with a switchover.
- **Controller backups are separate.** These backups cover the database's data. The controller's own state is backed up separately ([Backup and restore](../operations/backup-restore.md)).

---

Back to [PostgreSQL](postgres.md)
