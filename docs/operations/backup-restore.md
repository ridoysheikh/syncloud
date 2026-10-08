# Backup and restore (the controller)

The controller's state (projects, services, secrets, certificates, users, history) lives in one SQLite database. Back it up to S3; a restore brings the whole cluster's control plane back, on the same server or a new one.

Databases you create are backed up separately ([PostgreSQL backups](../guides/postgres-backups.md)).

## Configure backups

**Settings → Backups**, or:

```sh
synctl backups config set --endpoint https://s3.example.com --region eu-central-1 \
  --bucket backups --prefix syncloud/prod --access-key-id AK… --secret-access-key …
```

- **Schedule:** hourly by default (`--interval`). The newest 48 are kept (`--retain`).
- **Contents:** one encrypted bundle holding a consistent database snapshot, the cluster CA, the registry and Traefik keys, and the master key wrapped by your **recovery key**.
- **On demand:** `synctl backups run` backs up now. `synctl backups download -f file.synbak` saves a copy without S3.

The header warns until backups run, and again when they fail.

> Without the recovery key (`SYNRK-…`, printed at install), a backup can't be opened. Keep it offline.

## Restore, or move to a new server

1. Install the binaries on the new server with [install.sh](../getting-started/install.md), or copy them. Don't start the controller yet.
2. Restore, giving the recovery key when asked:

   ```sh
   sudo syncloud-controller restore --file backup.synbak     # or --s3-endpoint … --s3-bucket …
   ```

3. Start it: `sudo systemctl start syncloud-controller`.
   - An sslip.io/nip.io base domain follows the new public IP automatically.
   - With your own domain, point its DNS records at the new server.
4. Re-join the controller's own agent with the command `restore` printed:

   ```sh
   sudo syncloud-agent join --controller http://127.0.0.1:7070 \
     --token-file /var/lib/syncloud/local-rejoin.token --name ctl-0
   ```

5. If the address changed, point every worker at it and restart its agent:

   ```sh
   sudo syncloud-agent set-controller --gateway NEW-IP:7443 --controller https://NEW-DASHBOARD
   sudo systemctl restart syncloud-agent
   ```

Workers keep their identity, and their containers keep running. Users, services, secrets and certificates all come back. Tasks that ran on the old controller host are started elsewhere. This drill is tested end to end (`test/e2e/restore.sh`).
