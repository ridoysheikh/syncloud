# Install

Run this on the server that will be the controller, as root:

```sh
curl -fsSL https://github.com/ridoysheikh/syncloud/releases/latest/download/install.sh | sudo bash
```

The installer:

1. Checks the host: OS, CPU, memory, disk, free ports 80 and 443, kernel modules and the clock.
2. Installs Docker if it's missing.
3. Downloads the controller and agent from the [GitHub release](https://github.com/ridoysheikh/syncloud/releases) and checks them against `SHA256SUMS`.
4. Writes the systemd units `syncloud-controller` and `syncloud-agent`, and the settings file `/etc/syncloud/controller.env`.
5. Joins the controller's own agent as node `ctl-0`, and waits for the platform components: Traefik, the registry, BuildKit, VictoriaMetrics and VictoriaLogs.

At the end it prints three things:

- the **dashboard URL**;
- a one-time **setup token**;
- the **recovery key** (`SYNRK-…`).

> **Keep the recovery key offline.** Every controller backup is encrypted, and only the recovery key can open it. Without it, a backup can't be restored.

## Options

Pass options after `bash -s --`:

```sh
curl -fsSL https://github.com/ridoysheikh/syncloud/releases/latest/download/install.sh \
  | sudo bash -s -- --base-domain cloud.example.com --acme-email ops@example.com
```

| Option | Effect |
| --- | --- |
| `--base-domain D` | Your own domain (point `*.D` at this server first). The default is `<public-ip>.sslip.io`. |
| `--acme-email E` | Contact address for Let's Encrypt. |
| `--public-ip IP` | Skip public IP detection, for example behind 1:1 NAT. |
| `--version V` | Install a specific release, for example `0.1.0`. The default is the latest. |
| `--from-dir DIR` | Install binaries you built yourself (`make release`). |
| `--skip-docker` | Don't install Docker. |
| `--force` | Continue even when preflight checks fail. |

### From the bundle

Each release also has a self-contained archive per architecture. Download it, unpack it, and install from it:

```sh
curl -fsSLO https://github.com/ridoysheikh/syncloud/releases/latest/download/syncloud_<version>_linux_amd64.tar.gz
tar xzf syncloud_*_linux_amd64.tar.gz && cd syncloud_*_linux_amd64
sudo ./install.sh --from-dir .
```

### From a mirror, or offline

Set `SYNCLOUD_RELEASE_URL` to a mirror with the layout `<url>/<version>/<file>` and `<url>/channels/stable`, or to `file:///path` on an air-gapped host:

```sh
sudo SYNCLOUD_RELEASE_URL=file:///srv/syncloud bash install.sh --version 0.1.0
```

The controller keeps using that location for [upgrades](../operations/upgrades.md).

## First sign-in

Open the dashboard URL. The setup page asks for:

- the setup token;
- the last 6 characters of the recovery key, to prove you saved it;
- an email and password for the **root account**.

The root account bypasses permission checks. For daily work, create normal users under **IAM** ([Access control](../guides/access-control.md)).

## Two things to do next

1. **Settings → Backups:** add an S3 destination. The header shows a warning until backups run ([Backup and restore](../operations/backup-restore.md)).
2. **Compute → Nodes → Add node:** join your other servers ([Add nodes](add-nodes.md)).

A single server works too: the controller is also a node. By default it doesn't take general workloads, but a project can be [allowed to use it](../operations/nodes-and-pools.md#which-nodes-a-project-may-use).

---

Next: [Your first app](first-app.md)
