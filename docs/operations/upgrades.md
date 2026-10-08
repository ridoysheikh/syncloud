# Upgrades

Releases are published on [GitHub](https://github.com/ridoysheikh/syncloud/releases). Read the [changelog](../../CHANGELOG.md) before upgrading, and make sure a recent [controller backup](backup-restore.md) exists.

## Upgrade the controller

**Settings → Updates** shows the running version and the newest one on your channel. Choose **Upgrade**. Or:

```sh
synctl system upgrade                         # the newest release on the channel
synctl system upgrade --version 0.2.0
sudo syncloud-controller upgrade --version 0.2.0   # on the controller host
```

What happens:

1. The release is downloaded and checked against its `SHA256SUMS`. The new binary must report the expected version.
2. The database is snapshotted, and the current binary kept, under `/var/lib/syncloud/upgrade/<id>/`.
3. A **guard** (a copy of the old binary, in its own systemd unit) stops the controller, installs the new one and starts it.
4. The new controller must be healthy within 5 minutes and **stay** healthy for 2 more (`--upgrade-settle`).
5. The release's agent and synctl binaries are copied to the worker downloads.

**Automatic rollback.** If the new controller exits, never becomes healthy, or stops being healthy while settling, the guard restores the old binary and database. Changes made in those few minutes are lost.

**Tasks keep running** throughout. The dashboard and API are unavailable for a few seconds.

## Upgrade the agents

After a controller upgrade, nodes show their agent as outdated. Agents upgrade **one node at a time**, from **Settings → Updates**, or:

```sh
synctl nodes upgrade-agents            # every outdated node
synctl nodes upgrade-agents w1 w2
```

Each node gets the binary over its existing connection, restarts the agent in place (containers keep running), and must reconnect within 2 minutes, or it rolls back by itself. A failure stops the rollout.

Older agents work with a newer controller, but upgrade them soon: only the previous release is tested against it.

## Channels and sources

| Setting | Values |
| --- | --- |
| `--release-channel` | `stable` (default): the latest release. `beta`: also prereleases such as `0.3.0-rc.1`. |
| `--release-url` | `https://github.com/ridoysheikh/syncloud` (default); a mirror with the layout `<url>/<version>/<file>` and `<url>/channels/<channel>`; or `file:///dir` for air-gapped hosts |

Set them in `/etc/syncloud/controller.env` (`SYNCLOUD_RELEASE_CHANNEL`, `SYNCLOUD_RELEASE_URL`) and restart the controller.

### Air-gapped upgrades

1. Download a release's files on a connected machine.
2. Copy them to the host as `/srv/syncloud/<version>/…`, and write the version into `/srv/syncloud/channels/stable`.
3. Set `SYNCLOUD_RELEASE_URL=file:///srv/syncloud`.

Put the release's `syncloud-postgres-*.tar.gz` files into `/var/lib/syncloud/images/` (or `/usr/local/lib/syncloud/downloads/images/`). The controller loads them when a PostgreSQL database needs them.

## Downgrading

Run `upgrade --version` with an older release. Downgrades aren't tested across database migrations. To go back after a migration, [restore](backup-restore.md) the backup taken before the upgrade.
