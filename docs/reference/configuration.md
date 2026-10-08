# Configuration

## Controller

The installer writes `/etc/syncloud/controller.env`. Every flag of `syncloud-controller` can also be set there as `SYNCLOUD_<FLAG>`, in upper case with `_` for `-` (`--base-domain` becomes `SYNCLOUD_BASE_DOMAIN`). Restart after a change: `sudo systemctl restart syncloud-controller`.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--data-dir` | `/var/lib/syncloud` | controller state (SQLite, keys) |
| `--listen` | `127.0.0.1:7070` | API and dashboard (Traefik proxies to it) |
| `--agent-listen` | `127.0.0.1:7443` (installer: `0.0.0.0:7443`) | where agents connect (mutual TLS) |
| `--agent-advertise` | `--agent-listen` | the address nodes use to reach it |
| `--public-ip` | detected | public IPv4 address |
| `--base-domain` | `<public-ip>.sslip.io` | the platform's domain. `off` means none. |
| `--acme`, `--acme-email`, `--acme-directory`, `--acme-ca-file` | Let's Encrypt | certificate issuing |
| `--public-http`, `--public-https` | `:80`, `:443` | Traefik's entrypoints |
| `--public-ports` | `20000-20999` | the range of public TCP/UDP service ports |
| `--public-db-ports` | `21000-21999` | the range of public database ports (two per public database); must not overlap `--public-ports` |
| `--public-postgres`, `--public-valkey` | `:5432`, `:6379` | the shared public database ports (TLS with SNI) |
| `--controller-schedulable` | `0` | whether general services may run on the controller node |
| `--build-node` | any node | run Git builds on this node |
| `--registry-pull-host` | `registry.<base-domain>` | the host nodes pull built-in registry images from |
| `--release-url` | `https://github.com/ridoysheikh/syncloud` | where upgrades and the PostgreSQL images come from ([Upgrades](../operations/upgrades.md)) |
| `--release-channel` | `stable` | `stable` or `beta` |
| `--upgrade-settle` | `2m` | how long an upgraded controller must stay healthy |
| `--downloads-dir` | `/usr/local/lib/syncloud/downloads` | binaries served to joining nodes at `/downloads/`; `images/` holds placed image archives |
| `--postgres-image` | the release's images | override per major version: `18=IMAGE,17=IMAGE` |
| `--firewall`, `--security-groups`, `--central-probes` | on | turn the host firewall, container isolation or controller probes off |
| `--system-tasks` | on | run the platform components (Traefik, registry, metrics, logs) on this host |
| `--shell-image` | `alpine:3.22` | the Cloud Shell container |
| `--dev` | off | development mode (local addresses, no ACME) |

`syncloud-controller --help` lists every flag. Other subcommands: `upgrade`, `restore`, `doctor`, `uninstall`.

## Agent

The agent is configured when it joins (`syncloud-agent join`). Its state lives in `/var/lib/syncloud-agent`.

| Setting | Meaning |
| --- | --- |
| `--no-host-shell` / `SYNCLOUD_AGENT_NO_HOST_SHELL=1` | refuse node shells on this machine |
| `SYNCLOUD_WIREGUARD_MODE=userspace` | use userspace WireGuard even when the kernel module exists |
| `syncloud-agent set-controller --gateway HOST:7443 --controller URL` | point it at a moved controller |

## Ports

| Port | Who listens | Reached by |
| --- | --- | --- |
| 80, 443/tcp | Traefik (controller and edge nodes) | the internet |
| 7443/tcp | controller (agent gateway) | nodes |
| 51820/udp | every node (WireGuard) | other nodes |
| 20000–20999 tcp/udp | Traefik | the internet, for ports you make public |
| 21000–21999/tcp | Traefik | the internet, for public databases (TLS or plain) |
| 5432, 6379/tcp | Traefik | the internet, for public databases (TLS with SNI) |
| 7070/tcp | controller API, on loopback | Traefik |

## Files

| Path | What |
| --- | --- |
| `/etc/syncloud/controller.env` | controller settings |
| `/var/lib/syncloud/` | controller state: database, CA, keys, upgrade snapshots |
| `/var/lib/syncloud-agent/` | agent identity and state |
| `/usr/local/bin/syncloud-{controller,agent}` | binaries |
| `/usr/local/lib/syncloud/downloads/` | binaries for joining nodes, and image archives |
| `/etc/systemd/system/syncloud-{controller,agent}.service` | systemd units |
