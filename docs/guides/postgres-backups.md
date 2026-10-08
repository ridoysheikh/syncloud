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

## Good to know

- **Members must reach the S3 endpoint.** For an S3 service inside the cluster (MinIO, Garage…), its [security group](network-security.md) must let the database in.
- **Turning backups off** stops archiving; what's in S3 stays.
- **Changing backup settings** restarts the members one at a time, with a switchover.
- **Controller backups are separate.** These backups cover the database's data. The controller's own state is backed up separately ([Backup and restore](../operations/backup-restore.md)).

---

Back to [PostgreSQL](postgres.md)
