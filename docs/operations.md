# Operations

## Health

`GET /api/v1/system/health` is public. It returns `200` with `{"version", "ready": true}` when two things hold:

- the database answers;
- every system task is running (checked 90 seconds after the controller starts).

Otherwise it returns `503` with a list of problems. Load balancers and upgrades use it.

Other checks:

- `synctl system health` shows the same result.
- `syncloud-controller doctor` on the host checks keys, database, API, disk, Docker, each system task, DNS, certificates and backups, and prints a fix for each problem.

## Upgrading the controller

You can start an upgrade from **Settings → Updates**, with `synctl system upgrade [--version V]`, or on the host:

```sh
sudo syncloud-controller upgrade            # newest release on the channel
sudo syncloud-controller upgrade --version 1.4.2
```

The upgrade runs these steps:

1. Download the release from `--release-url` (default `https://get.syncloud.dev/releases`; `file:///dir` works for air-gapped hosts) and check it against `SHA256SUMS`. The new binary must report the expected version.
2. Snapshot the database and keep the current binary, both under `/var/lib/syncloud/upgrade/<id>/`.
3. Start a **guard**: a copy of the old binary running as its own transient systemd unit. The guard stops the controller, installs the new binary and starts it again.
4. Wait for the new controller to report healthy at the new version within 5 minutes, then **stay healthy** for `--upgrade-settle` (default 2 minutes).
5. On success, copy the release's agent and synctl binaries into the worker downloads.

**Rollback.** If the new controller exits, does not become healthy, or stops being healthy while settling, the guard restores the previous binary and the database snapshot and starts the old controller again. Changes made during those few minutes are lost.

Tasks keep running throughout. The dashboard and API are unavailable for a few seconds.

The release channel is set with `--release-channel` (`stable` or `beta`). Progress is shown on the Updates page and stored in `/var/lib/syncloud/upgrade/state.json`.

## Upgrading agents

After a controller upgrade, the nodes show their agent as outdated. Agents are upgraded **one node at a time** to the controller's version. Start the rollout from **Settings → Updates**, or with:

```sh
synctl nodes upgrade-agents            # every outdated node
synctl nodes upgrade-agents w1 w2      # some nodes
```

For each node:

1. The binary is sent over the agent's existing connection and checked against its SHA-256.
2. The agent starts a guard, replaces itself and restarts in place. Containers keep running.
3. The new agent must reconnect within 2 minutes. If it exits or does not come back, the guard restores the old agent, which reports why.

A failed node stops the rollout, and the remaining nodes are skipped. Older agents keep working with a newer controller in the meantime, but upgrade them soon after the controller: only the previous release is tested against it.

## Backups

Configure backups under **Settings → Backups**, or with:

```sh
synctl backups config set --endpoint https://s3.example.com --region eu-central-1 \
  --bucket backups --prefix syncloud/prod --access-key-id AK… --secret-access-key …
```

- **Schedule:** a backup runs every hour by default (`--interval`). The newest 48 are kept (`--retain`).
- **Contents:** each backup is a single encrypted bundle. It holds a consistent database snapshot plus the cluster CA, the registry and Traefik keys, and the master key wrapped by the recovery key.
- **On demand:** `synctl backups run` takes a backup now. `synctl backups download -f file.synbak` saves one without S3.

## Restoring, and moving the controller to a new host

This procedure has been tested in `test/e2e/restore.sh`.

1. Install the binaries on the new host with `install.sh`, or copy them over. Do not start the controller yet.
2. Restore the backup, with the recovery key:

   ```sh
   sudo syncloud-controller restore --file backup.synbak            # or --s3-endpoint … --s3-bucket …
   ```

3. Start the controller with `systemctl start syncloud-controller`.
   - An **sslip.io/nip.io base domain follows the new public IP** automatically.
   - If you use your own domain, point its DNS records at the new host.
4. Re-join the controller's own agent. `restore` printed this command, using a single-use token kept in `/var/lib/syncloud/local-rejoin.token`:

   ```sh
   sudo syncloud-agent join --controller http://127.0.0.1:7070 --token-file /var/lib/syncloud/local-rejoin.token --name ctl-0
   ```

   Node `ctl-0` keeps its ID, pool and mesh address.
5. If the new host has a different address, point every worker at it, then restart each agent:

   ```sh
   sudo syncloud-agent set-controller --gateway NEW-IP:7443 --controller https://NEW-DASHBOARD
   sudo systemctl restart syncloud-agent
   ```

   Workers keep their identity, and their containers keep running.

Sign-in, users, services, secrets and certificates all come back. Tasks that ran on the old controller host are replaced on other nodes.

## Uninstall

```sh
sudo syncloud-controller uninstall [--purge]    # on the controller host
sudo syncloud-agent uninstall [--purge]         # on a worker
```

Uninstall removes:

- the systemd units,
- the SynCloud containers and Docker networks,
- the WireGuard interface `wg-syncloud`,
- the nftables tables.

Data is kept unless you pass `--purge`, which also deletes the data directories and the platform volumes. `install.sh --uninstall [--purge]` runs the same commands and then removes the binaries.

## Data retention

Every 6 hours the controller deletes old history:

| Data | Kept |
| --- | --- |
| Audit log | 365 days |
| Closed incidents | 90 days |
| Finished deployments | 90 days, and always the last 5 per service |
| Finished builds | 90 days, and always the deployed build of each service |
| Daily usage | 400 days |
| Task definitions (revisions) | the newest 50 per service, plus the current and previous revisions, those of in-flight deployments and those that running tasks use |
| Upgrade directories | the last 3 |

Other history has its own limits:

- Alert history is kept 90 days.
- Registry events are kept according to the registry settings.
- Scaling and pool histories keep their newest entries.

On each node, when the disk is above 85% full, the agent removes images that no container uses and that are older than a day.
