# Troubleshooting

## Start here

```sh
sudo syncloud-controller doctor      # on the controller: checks everything and prints a fix for each problem
synctl system health                 # database and platform components
synctl system tasks                  # Traefik, registry, VictoriaMetrics, … and their state
```

`doctor` checks the keys, database, API, disk, Docker, every platform component, DNS, certificates and backups.

`GET /api/v1/system/health` is public. It answers `200` when the controller is ready, and `503` with the problems otherwise. Point load balancers and uptime checks at it.

## Logs

| What | Where |
| --- | --- |
| controller | `journalctl -u syncloud-controller -f` |
| an agent | `journalctl -u syncloud-agent -f` on that node |
| a service | `synctl logs -f web -p shop`, or the service's **Logs** tab |
| a deployment's release commands | the deployment's page |
| a build | the **Builds** tab, or `synctl builds run api --wait` |

## Common problems

| Symptom | Likely cause and fix |
| --- | --- |
| **Installer: "port 80/443 is used"** | Another web server owns the port. Stop it; Traefik must own 80 and 443. |
| **No certificate, browser warning** | DNS doesn't point at the cluster yet, or ports 80/443 are blocked. `synctl certificates list` shows the error; `synctl certificates renew HOST` retries now. |
| **A node stays "Not ready"** | The agent can't reach the controller on 7443/tcp. Check `journalctl -u syncloud-agent` on the node and the firewall between them. |
| **Tasks of one node unreachable from others** | WireGuard (51820/udp) is blocked between the nodes. **Network → Topology** shows each link. |
| **Service stuck "starting"** | The health check fails. Open a task on the **Tasks** tab for its error, and the **Logs** tab. A wrong port or path is the usual cause. |
| **"No node can take it"** | No node has the CPU or memory reserved, or placement excludes them. The message names the reason. Lower the reservation, add a node, or widen placement. |
| **Deploy refused with 423** | The environment is [locked](../guides/deployments.md#locking-an-environment). |
| **Build waits** | No node has room for a build (0.25 CPU, 512 MB), or BuildKit isn't running (`synctl system tasks`). |
| **Webhook doesn't trigger builds** | The Git host can't reach your dashboard URL. Builds still start on the next poll (every minute). |
| **PostgreSQL waits for its image** | The controller is downloading the image from the release. With no internet access, place the archive by hand ([PostgreSQL](../guides/postgres.md#create-one)). |
| **Public database: `certificate verify failed`, or `Protocol error, got "H"`** | The client must use TLS and send the host name (SNI). For `redis-cli`, add `--tls --sni <db>.db.<domain>`; see [Valkey](../guides/valkey.md#from-outside-the-cluster). |
| **Backups warning in the header** | No backup destination, or the last backup failed: **Settings → Backups** shows why. |

## Still stuck?

Search or open an issue on [GitHub](https://github.com/ridoysheikh/syncloud/issues). Include:

- `syncloud-controller version`, or the version on **Settings → Updates**;
- the output of `doctor`;
- the relevant log lines.

Remove secrets from logs before you post them.
