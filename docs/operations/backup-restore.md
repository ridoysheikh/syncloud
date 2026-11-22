# Backup and restore (the controller)

The controller's state (projects, services, secrets, certificates, users, history) lives in one SQLite database. Back it up to S3; a restore brings the whole cluster's control plane back, on the same server or a new one.

Databases you create are backed up separately ([PostgreSQL backups](../guides/postgres-backups.md)).

## Configure backups

**Settings → Backups**. Choose an **S3 connection** registered under **Storage** (recommended): backups read its keys at every run, so when you rotate the key on the connection, backups, the registry and PostgreSQL all use the new one. A connection used by backups can't be deleted. Or enter an endpoint and keys only for backups. A key rotated at the provider must then be entered again here, and until it is, every backup fails with "the secret access key does not belong to access key ID …".

With keys entered by hand, from the CLI:

```sh
synctl backups config set --endpoint https://s3.example.com --region eu-central-1 \
  --bucket backups --prefix syncloud/prod --access-key-id AK… --secret-access-key …
```

- **Schedule:** hourly by default (`--interval`). The newest 48 are kept (`--retain`).
- **Contents:** one encrypted bundle holding a consistent database snapshot, the cluster CA, the registry and Traefik keys, and the master key wrapped by your **recovery key**.
- **Registry images are not in the bundle.** On the controller's disk, they are lost with the server. Rebuild or re-push them after a restore. [Keep them in S3](../guides/storage.md#registry-images-in-s3) and the restored registry has them all.
- **On demand:** `synctl backups run` backs up now. `synctl backups download -f file.synbak` saves a copy without S3.

The header warns until backups run, and again when they fail.

> Without the recovery key (`SYNRK-…`), a backup can't be opened. It is shown once, at install, and isn't stored on the controller, so keep it offline. No dashboard page, API call or `synctl` command can show or change it: it is managed only on the controller host, as root.

### Rotate the recovery key

Run this on the controller host. The controller keeps running:

```bash
sudo syncloud-controller recovery-key rotate
```

It makes a new key and shows it once. Save it before you close the terminal. It then re-keys the backups in S3 so the new key opens them all, and the old key no longer does. Nothing is re-encrypted, and services and data aren't affected. The next backup uses the new key without a restart. Downloaded `.synbak` files still need the key that was current when they were made.

- `sudo syncloud-controller recovery-key verify` checks that a key (typed, not echoed) opens this install. Use it to test an offline copy.
- `sudo syncloud-controller recovery-key rekey` retries the re-keying if S3 couldn't be reached during a rotation. It needs no key.
- **Settings → Backups** shows when the key was last set, never the key itself.

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
